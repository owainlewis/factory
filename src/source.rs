use std::collections::HashSet;
use std::fmt;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use tokio::io::{AsyncRead, AsyncReadExt};
use tokio::process::Command;
use tokio_util::sync::CancellationToken;

use crate::config::{Config, SourceConfig, repository_remote_identity};
use crate::hash::sha256_hex;
use crate::storage::{Ledger, ObservedTicket, Task};
use crate::workflow::{Trigger, WorkflowCatalog, WorkflowEntry};

const COMMAND_TIMEOUT: Duration = Duration::from_secs(30);
const MAX_OUTPUT_BYTES: usize = 1024 * 1024;
const MAX_STDERR_BYTES: usize = 64 * 1024;
const STDERR_TRUNCATION_MARKER: &str = " [stderr truncated]";

#[derive(Debug, Clone, Default)]
pub struct SourceClient;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RepositoryPoll {
    pub repository: PathBuf,
    pub name_with_owner: Option<String>,
    pub issues_seen: usize,
    pub tasks_created: usize,
    pub error: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PollReport {
    pub repositories: Vec<RepositoryPoll>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SourceRateLimit {
    pub message: String,
    pub retry_at: Option<DateTime<Utc>>,
}

#[derive(Debug)]
struct SourceCommandFailure {
    message: String,
    rate_limit: Option<SourceRateLimit>,
}

impl fmt::Display for SourceCommandFailure {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for SourceCommandFailure {}

pub fn source_rate_limit(error: &anyhow::Error) -> Option<&SourceRateLimit> {
    error
        .downcast_ref::<SourceCommandFailure>()
        .and_then(|failure| failure.rate_limit.as_ref())
}

#[cfg(test)]
pub(crate) fn test_source_rate_limit_error(
    message: impl Into<String>,
    retry_at: Option<DateTime<Utc>>,
) -> anyhow::Error {
    SourceCommandFailure {
        message: "test source rate limit".to_owned(),
        rate_limit: Some(SourceRateLimit {
            message: message.into(),
            retry_at,
        }),
    }
    .into()
}

impl PollReport {
    pub fn tasks_created(&self) -> usize {
        self.repositories
            .iter()
            .map(|item| item.tasks_created)
            .sum()
    }

    pub fn failures(&self) -> usize {
        self.repositories
            .iter()
            .filter(|item| item.error.is_some())
            .count()
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SourceTicketContext {
    pub key: String,
    pub title: String,
    #[serde(default)]
    pub description: String,
    pub state: String,
    #[serde(default)]
    pub labels: Vec<String>,
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub author: String,
    pub observed_revision: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct SourceOutput {
    issues: Vec<SourceIssue>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct SourceErrorEnvelope {
    error: SourceErrorBody,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct SourceErrorBody {
    kind: String,
    message: String,
    retry_at: Option<serde_json::Value>,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
struct SourceIssue {
    key: String,
    title: String,
    #[serde(default)]
    description: String,
    state: String,
    #[serde(default)]
    labels: Vec<String>,
    #[serde(default)]
    url: String,
    #[serde(default)]
    author: String,
    #[serde(default)]
    revision: Option<String>,
}

impl SourceClient {
    pub async fn validate(
        &self,
        repository: &Path,
        source: &SourceConfig,
        state: &str,
        labels: &[String],
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.query(repository, source, state, labels, cancellation)
            .await
            .context("source command validation failed")?;
        Ok(())
    }

    pub async fn poll_once(
        &self,
        config: &Config,
        catalog: &WorkflowCatalog,
        ledger: &mut Ledger,
        cancellation: CancellationToken,
    ) -> Result<PollReport> {
        let source = config
            .source
            .as_ref()
            .context("source triggers require a configured source command")?;
        let mut repositories = Vec::with_capacity(config.repositories.len());
        for repository in &config.repositories {
            let result: Result<RepositoryPoll> = async {
                let name = repository_remote_identity(repository)?;
                let workflows = catalog.entries.iter().filter(|workflow| {
                    workflow.repository == *repository
                        && workflow.errors.is_empty()
                        && matches!(workflow.trigger, Some(Trigger::Source { .. }))
                });
                let mut issues_seen = 0;
                let mut tasks_created = 0;
                for workflow in workflows {
                    let report = self
                        .poll_workflow(repository, &name, source, workflow, ledger, &cancellation)
                        .await?;
                    issues_seen += report.issues_seen;
                    tasks_created += report.tasks_created;
                }
                Ok(RepositoryPoll {
                    repository: repository.to_owned(),
                    name_with_owner: Some(name),
                    issues_seen,
                    tasks_created,
                    error: None,
                })
            }
            .await;
            repositories.push(match result {
                Ok(report) => report,
                Err(error) => RepositoryPoll {
                    repository: repository.to_owned(),
                    name_with_owner: None,
                    issues_seen: 0,
                    tasks_created: 0,
                    error: Some(format!("{error:#}")),
                },
            });
        }
        Ok(PollReport { repositories })
    }

    pub async fn poll_workflow(
        &self,
        repository: &Path,
        identity: &str,
        source: &SourceConfig,
        workflow: &WorkflowEntry,
        ledger: &mut Ledger,
        cancellation: &CancellationToken,
    ) -> Result<RepositoryPoll> {
        let Trigger::Source { state, labels } = workflow
            .trigger
            .as_ref()
            .context("workflow is not source-triggered")?
        else {
            bail!("workflow is not source-triggered");
        };
        let issues = self
            .query(repository, source, state, labels, cancellation)
            .await?;
        let issues_seen = issues.len();
        let observations = issues
            .into_iter()
            .map(|issue| observed_ticket(issue, state, labels))
            .collect::<Result<Vec<_>>>()?;
        let mut tasks_created = 0;
        for task in ledger.reconcile_ticket_poll(identity, &workflow.id, &observations)? {
            if !task.created {
                continue;
            }
            tasks_created += 1;
            if let Some(payload) = task.task.payload.as_deref()
                && let Ok(ticket) = serde_json::from_str::<SourceTicketContext>(payload)
            {
                eprintln!(
                    "Factory matched {}: {} [{}; labels={}]",
                    ticket.key,
                    one_line(&ticket.title, 120),
                    ticket.state,
                    ticket.labels.join(", ")
                );
                if !ticket.description.trim().is_empty() {
                    eprintln!(
                        "Factory issue description: {}",
                        one_line(&ticket.description, 240)
                    );
                }
            }
        }
        Ok(RepositoryPoll {
            repository: repository.to_owned(),
            name_with_owner: Some(identity.to_owned()),
            issues_seen,
            tasks_created,
            error: None,
        })
    }

    pub async fn poll_until_cancelled<F>(
        &self,
        config: &Config,
        catalog: &WorkflowCatalog,
        ledger: &mut Ledger,
        cancellation: CancellationToken,
        mut on_poll: F,
    ) -> Result<()>
    where
        F: FnMut(&PollReport),
    {
        loop {
            if cancellation.is_cancelled() {
                return Ok(());
            }
            let report = self
                .poll_once(config, catalog, ledger, cancellation.clone())
                .await?;
            on_poll(&report);
            tokio::select! {
                _ = cancellation.cancelled() => return Ok(()),
                _ = tokio::time::sleep(config.poll_every) => {}
            }
        }
    }

    pub async fn authorize(
        &self,
        repository: &Path,
        source: &SourceConfig,
        trigger: &Trigger,
        task: &Task,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let Trigger::Source { state, labels } = trigger else {
            bail!("only source triggers can authorize source tickets");
        };
        let payload = task
            .payload
            .as_deref()
            .context("source task has no ticket context")?;
        let expected: SourceTicketContext =
            serde_json::from_str(payload).context("source task context is invalid")?;
        if task.source_item.as_deref() != Some(expected.key.as_str()) {
            bail!("source task context does not match its durable identity");
        }
        let current = self
            .query(repository, source, state, labels, cancellation)
            .await?;
        if !current.iter().any(|issue| issue.key == expected.key) {
            bail!(
                "{} no longer matches state {:?} and labels {:?}",
                expected.key,
                state,
                labels
            );
        }
        Ok(())
    }

    async fn query(
        &self,
        repository: &Path,
        source: &SourceConfig,
        state: &str,
        labels: &[String],
        cancellation: &CancellationToken,
    ) -> Result<Vec<SourceIssue>> {
        let executable = source
            .command
            .first()
            .context("source command has no executable")?;
        let mut command = Command::new(executable);
        command
            .args(&source.command[1..])
            .arg("--state")
            .arg(state)
            .current_dir(repository)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        command.process_group(0);
        for label in labels {
            command.arg("--label").arg(label);
        }
        let mut child = command
            .spawn()
            .with_context(|| format!("failed to start source command {:?}", source.command))?;
        let process_group_id = child.id();
        let stdout = child
            .stdout
            .take()
            .context("source command stdout is unavailable")?;
        let stderr = child
            .stderr
            .take()
            .context("source command stderr is unavailable")?;
        let mut stdout_task = tokio::spawn(read_bounded(stdout, MAX_OUTPUT_BYTES, false));
        let mut stderr_task = tokio::spawn(read_bounded(stderr, MAX_STDERR_BYTES, true));
        let deadline = tokio::time::Instant::now() + COMMAND_TIMEOUT;
        let result: Result<_> = tokio::select! {
            _ = cancellation.cancelled() => Err(anyhow::anyhow!("source command cancelled")),
            _ = tokio::time::sleep_until(deadline) => {
                Err(anyhow::anyhow!("source command timed out after 30s"))
            }
            result = async {
                let status = child.wait().await.context("failed to wait for source command")?;
                let stdout = (&mut stdout_task)
                    .await
                    .context("source stdout reader task panicked")??;
                let stderr = (&mut stderr_task)
                    .await
                    .context("source stderr reader task panicked")??;
                Ok((status, stdout, stderr))
            } => result,
        };
        if result.is_err() {
            kill_source_process_group(process_group_id);
            let _ = child.start_kill();
            let _ = child.wait().await;
            stdout_task.abort();
            stderr_task.abort();
        }
        let (status, stdout, stderr) = result?;
        let diagnostic = format_stderr(&stderr);
        if !status.success() {
            let rate_limit = (!stdout.truncated)
                .then(|| parse_rate_limit(&stdout.bytes))
                .flatten();
            return Err(SourceCommandFailure {
                message: format!("source command exited with {status}; stderr: {diagnostic}"),
                rate_limit,
            }
            .into());
        }
        if stdout.truncated {
            bail!("source command output exceeded 1 MiB");
        }
        let parsed: SourceOutput = serde_json::from_slice(&stdout.bytes)
            .context("source command returned invalid JSON")?;
        validate_issues(parsed.issues, state, labels)
    }
}

fn kill_source_process_group(process_group_id: Option<u32>) {
    if let Some(process_id) = process_group_id
        && let Ok(process_id) = i32::try_from(process_id)
    {
        let _ = nix::sys::signal::killpg(
            nix::unistd::Pid::from_raw(process_id),
            nix::sys::signal::Signal::SIGKILL,
        );
    }
}

struct BoundedBytes {
    bytes: Vec<u8>,
    truncated: bool,
}

async fn read_bounded(
    mut reader: impl AsyncRead + Unpin,
    maximum: usize,
    retain_tail: bool,
) -> std::io::Result<BoundedBytes> {
    let mut bytes = Vec::with_capacity(maximum.min(8192));
    let mut truncated = false;
    let mut buffer = [0_u8; 8192];
    loop {
        let read = reader.read(&mut buffer).await?;
        if read == 0 {
            break;
        }
        if retain_tail {
            if read >= maximum {
                bytes.clear();
                bytes.extend_from_slice(&buffer[read - maximum..read]);
                truncated = true;
            } else {
                let overflow = bytes.len().saturating_add(read).saturating_sub(maximum);
                if overflow > 0 {
                    bytes.drain(..overflow);
                    truncated = true;
                }
                bytes.extend_from_slice(&buffer[..read]);
            }
        } else {
            let remaining = maximum.saturating_sub(bytes.len());
            bytes.extend_from_slice(&buffer[..read.min(remaining)]);
            truncated |= read > remaining;
        }
    }
    Ok(BoundedBytes { bytes, truncated })
}

fn format_stderr(stderr: &BoundedBytes) -> String {
    let sanitized =
        crate::inspection::sanitize_for_storage(&String::from_utf8_lossy(&stderr.bytes))
            .chars()
            .map(|character| {
                if character.is_control() {
                    ' '
                } else {
                    character
                }
            })
            .collect::<String>();
    let line = sanitized.split_whitespace().collect::<Vec<_>>().join(" ");
    let mut diagnostic = if line.chars().count() <= 500 {
        line
    } else {
        let mut tail = line.chars().rev().take(499).collect::<Vec<_>>();
        tail.reverse();
        format!("…{}", tail.into_iter().collect::<String>())
    };
    if diagnostic.is_empty() {
        diagnostic.push('-');
    }
    if stderr.truncated {
        diagnostic.push_str(STDERR_TRUNCATION_MARKER);
    }
    diagnostic
}

fn parse_rate_limit(stdout: &[u8]) -> Option<SourceRateLimit> {
    if stdout.is_empty() || stdout.len() > MAX_OUTPUT_BYTES {
        return None;
    }
    let envelope: SourceErrorEnvelope = serde_json::from_slice(stdout).ok()?;
    let kind = envelope.error.kind;
    if kind.is_empty()
        || kind.len() > 64
        || !kind
            .bytes()
            .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || byte == b'_')
        || kind != "rate_limited"
    {
        return None;
    }
    let message = envelope.error.message;
    if message.is_empty() || message.chars().count() > 500 || message.chars().any(char::is_control)
    {
        return None;
    }
    let retry_at = envelope.error.retry_at.and_then(|value| {
        value.as_str().and_then(|value| {
            DateTime::parse_from_rfc3339(value)
                .ok()
                .map(|timestamp| timestamp.with_timezone(&Utc))
        })
    });
    Some(SourceRateLimit { message, retry_at })
}

fn validate_issues(
    issues: Vec<SourceIssue>,
    state: &str,
    labels: &[String],
) -> Result<Vec<SourceIssue>> {
    let mut keys = HashSet::new();
    for issue in &issues {
        if issue.key.trim().is_empty()
            || issue.key.chars().count() > 100
            || issue.key.chars().any(char::is_control)
        {
            bail!("source issue key must be 1-100 characters without control characters");
        }
        if !keys.insert(issue.key.clone()) {
            bail!(
                "source command returned duplicate issue key {:?}",
                issue.key
            );
        }
        if issue.title.trim().is_empty() || issue.title.chars().any(char::is_control) {
            bail!("source issue {:?} has an invalid title", issue.key);
        }
        if issue.state != state || !labels.iter().all(|label| issue.labels.contains(label)) {
            bail!(
                "source command returned issue {:?} that does not match state {:?} and labels {:?}",
                issue.key,
                state,
                labels
            );
        }
        if issue.revision.as_deref().is_some_and(|revision| {
            revision.trim().is_empty() || revision.chars().any(char::is_control)
        }) {
            bail!("source issue {:?} has an invalid revision", issue.key);
        }
    }
    Ok(issues)
}

fn observed_ticket(issue: SourceIssue, state: &str, labels: &[String]) -> Result<ObservedTicket> {
    let revision = issue.revision.clone().unwrap_or_else(|| {
        let mut selected_labels = labels.to_vec();
        selected_labels.sort();
        format!(
            "source:{}",
            sha256_hex(
                serde_json::to_vec(&(issue.key.as_str(), state, selected_labels))
                    .expect("source revision tuple is serializable")
            )
        )
    });
    let context = SourceTicketContext {
        key: issue.key.clone(),
        title: issue.title,
        description: issue.description,
        state: issue.state,
        labels: issue.labels,
        url: issue.url,
        author: issue.author,
        observed_revision: revision.clone(),
    };
    Ok(ObservedTicket {
        source_item: issue.key,
        revision,
        eligible: true,
        payload: serde_json::to_string(&context).context("failed to serialize source ticket")?,
    })
}

fn one_line(value: &str, maximum: usize) -> String {
    let sanitized = crate::inspection::sanitize_for_storage(value);
    let printable = sanitized
        .chars()
        .map(|character| {
            if character.is_control() {
                ' '
            } else {
                character
            }
        })
        .collect::<String>();
    let line = printable.split_whitespace().collect::<Vec<_>>().join(" ");
    if line.chars().count() <= maximum {
        return line;
    }
    let mut truncated = line
        .chars()
        .take(maximum.saturating_sub(1))
        .collect::<String>();
    truncated.push('…');
    truncated
}

pub fn source_workflows(catalog: &WorkflowCatalog) -> impl Iterator<Item = &WorkflowEntry> {
    catalog.entries.iter().filter(|workflow| {
        workflow.errors.is_empty() && matches!(workflow.trigger, Some(Trigger::Source { .. }))
    })
}
