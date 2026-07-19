use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use tokio::process::Command;
use tokio_util::sync::CancellationToken;

use crate::config::Config;
use crate::storage::{Ledger, ObservedTicket};
use crate::workflow::{Trigger, WorkflowCatalog, WorkflowEntry};

const DEFAULT_COMMAND_TIMEOUT: Duration = Duration::from_secs(30);

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TicketContext {
    pub number: u64,
    pub url: String,
    pub title: String,
    pub body: String,
    pub labels: Vec<String>,
    pub comments: Vec<TicketComment>,
    pub observed_revision: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TicketComment {
    pub id: u64,
    pub url: String,
    pub author: String,
    pub body: String,
    pub created_at: String,
    pub updated_at: String,
}

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

#[derive(Debug, Clone)]
pub struct GitHubClient {
    executable: PathBuf,
    command_timeout: Duration,
}

impl Default for GitHubClient {
    fn default() -> Self {
        Self::new("gh")
    }
}

impl GitHubClient {
    pub fn new(executable: impl Into<PathBuf>) -> Self {
        Self {
            executable: executable.into(),
            command_timeout: DEFAULT_COMMAND_TIMEOUT,
        }
    }

    pub fn with_command_timeout(mut self, command_timeout: Duration) -> Self {
        self.command_timeout = command_timeout;
        self
    }

    pub async fn validate_global(&self, cancellation: &CancellationToken) -> Result<()> {
        self.run(None, &["--version"], cancellation)
            .await
            .context("GitHub CLI is unavailable; install gh and ensure it is executable")?;
        self.run(None, &["auth", "status"], cancellation)
            .await
            .context("GitHub CLI is not authenticated; run gh auth login")?;
        Ok(())
    }

    pub async fn validate_repository(
        &self,
        repository: &Path,
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let output = self
            .run(
                Some(repository),
                &[
                    "repo",
                    "view",
                    "--json",
                    "nameWithOwner",
                    "--jq",
                    ".nameWithOwner",
                ],
                cancellation,
            )
            .await
            .with_context(|| {
                format!(
                    "GitHub CLI cannot read configured repository {}",
                    repository.display()
                )
            })?;
        let name = output.trim();
        if !valid_name_with_owner(name) {
            bail!("gh returned invalid repository identity {name:?}");
        }
        Ok(name.to_owned())
    }

    pub async fn labels(
        &self,
        repository: &Path,
        cancellation: &CancellationToken,
    ) -> Result<Vec<String>> {
        let output = self
            .run(
                Some(repository),
                &[
                    "label", "list", "--limit", "1000", "--json", "name", "--jq", ".[].name",
                ],
                cancellation,
            )
            .await
            .with_context(|| {
                format!("GitHub CLI cannot list labels for {}", repository.display())
            })?;
        Ok(output.lines().map(str::to_owned).collect())
    }

    pub async fn create_label(
        &self,
        repository: &Path,
        name: &str,
        description: &str,
        color: &str,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.run(
            Some(repository),
            &[
                "label",
                "create",
                name,
                "--description",
                description,
                "--color",
                color,
            ],
            cancellation,
        )
        .await
        .with_context(|| format!("failed to create GitHub label {name:?}"))?;
        Ok(())
    }

    pub async fn poll_once(
        &self,
        config: &Config,
        catalog: &WorkflowCatalog,
        ledger: &mut Ledger,
    ) -> Result<PollReport> {
        self.poll_once_with_cancellation(config, catalog, ledger, CancellationToken::new())
            .await
    }

    pub async fn poll_once_with_cancellation(
        &self,
        config: &Config,
        catalog: &WorkflowCatalog,
        ledger: &mut Ledger,
        cancellation: CancellationToken,
    ) -> Result<PollReport> {
        self.validate_global(&cancellation).await?;
        let workflows = label_workflows(catalog);
        let mut repositories = Vec::with_capacity(config.repositories.len());
        for repository in &config.repositories {
            let result = self
                .poll_repository(repository, workflows.get(repository), ledger, &cancellation)
                .await;
            repositories.push(match result {
                Ok(report) => report,
                Err(error) => RepositoryPoll {
                    repository: repository.clone(),
                    name_with_owner: None,
                    issues_seen: 0,
                    tasks_created: 0,
                    error: Some(format!("{error:#}")),
                },
            });
        }
        Ok(PollReport { repositories })
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
            let report = match self
                .poll_once_with_cancellation(config, catalog, ledger, cancellation.clone())
                .await
            {
                Ok(report) => report,
                Err(_) if cancellation.is_cancelled() => return Ok(()),
                Err(error) => return Err(error),
            };
            on_poll(&report);
            tokio::select! {
                _ = cancellation.cancelled() => return Ok(()),
                _ = tokio::time::sleep(config.poll_every) => {}
            }
        }
    }

    async fn poll_repository(
        &self,
        repository: &Path,
        workflows: Option<&Vec<&WorkflowEntry>>,
        ledger: &mut Ledger,
        cancellation: &CancellationToken,
    ) -> Result<RepositoryPoll> {
        let name = self.validate_repository(repository, cancellation).await?;
        let issues: Vec<ApiIssue> = self
            .api_pages(
                repository,
                &format!("repos/{name}/issues?state=open&per_page=100"),
                cancellation,
            )
            .await?;
        let issues = issues
            .into_iter()
            .filter(|issue| issue.pull_request.is_none())
            .collect::<Vec<_>>();
        let mut tasks_created = 0;
        for workflow in workflows.into_iter().flatten() {
            let Trigger::Label(label) = workflow.trigger.as_ref().expect("filtered workflow")
            else {
                unreachable!();
            };
            let mut observations = Vec::with_capacity(issues.len());
            for issue in &issues {
                let eligible = issue.labels.iter().any(|item| item.name == *label);
                let comments = if eligible {
                    self.api_pages::<ApiComment>(
                        repository,
                        &format!("repos/{name}/issues/{}/comments?per_page=100", issue.number),
                        cancellation,
                    )
                    .await?
                    .into_iter()
                    .map(TicketComment::from)
                    .collect()
                } else {
                    Vec::new()
                };
                let context = TicketContext {
                    number: issue.number,
                    url: issue.html_url.clone(),
                    title: issue.title.clone(),
                    body: issue.body.clone().unwrap_or_default(),
                    labels: issue.labels.iter().map(|item| item.name.clone()).collect(),
                    comments,
                    observed_revision: issue.updated_at.clone(),
                };
                observations.push(ObservedTicket {
                    source_item: issue.number.to_string(),
                    revision: issue.updated_at.clone(),
                    eligible,
                    payload: serde_json::to_string(&context)
                        .context("failed to serialize ticket context")?,
                });
            }
            tasks_created += ledger
                .reconcile_ticket_poll(&name, &workflow.id, &observations)?
                .into_iter()
                .filter(|task| task.created)
                .count();
        }
        Ok(RepositoryPoll {
            repository: repository.to_owned(),
            name_with_owner: Some(name),
            issues_seen: issues.len(),
            tasks_created,
            error: None,
        })
    }

    async fn api_pages<T: DeserializeOwned>(
        &self,
        repository: &Path,
        endpoint: &str,
        cancellation: &CancellationToken,
    ) -> Result<Vec<T>> {
        let output = self
            .run(
                Some(repository),
                &["api", "--paginate", "--slurp", endpoint],
                cancellation,
            )
            .await?;
        let pages: Vec<Vec<T>> = serde_json::from_str(&output)
            .with_context(|| format!("gh returned malformed paginated JSON for {endpoint}"))?;
        Ok(pages.into_iter().flatten().collect())
    }

    async fn run(
        &self,
        repository: Option<&Path>,
        arguments: &[&str],
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let mut command = Command::new(&self.executable);
        command.args(arguments);
        if let Some(repository) = repository {
            command.current_dir(repository);
        }
        command
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        let child = command.spawn().with_context(|| {
            format!(
                "failed to start GitHub CLI at {}",
                self.executable.display()
            )
        })?;
        let output = tokio::select! {
            _ = cancellation.cancelled() => bail!("GitHub CLI command cancelled"),
            result = tokio::time::timeout(self.command_timeout, child.wait_with_output()) => {
                match result {
                    Ok(output) => output.context("failed to wait for GitHub CLI")?,
                    Err(_) => bail!(
                        "gh {} timed out after {}",
                        arguments.join(" "),
                        humantime::format_duration(self.command_timeout)
                    ),
                }
            }
        };
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            bail!(
                "gh {} failed with status {}: {}",
                arguments.join(" "),
                output.status,
                stderr.trim()
            );
        }
        String::from_utf8(output.stdout).context("gh output was not valid UTF-8")
    }
}

fn label_workflows(catalog: &WorkflowCatalog) -> HashMap<PathBuf, Vec<&WorkflowEntry>> {
    let mut workflows = HashMap::<PathBuf, Vec<&WorkflowEntry>>::new();
    for workflow in &catalog.entries {
        if workflow.errors.is_empty() && matches!(workflow.trigger, Some(Trigger::Label(_))) {
            workflows
                .entry(workflow.repository.clone())
                .or_default()
                .push(workflow);
        }
    }
    workflows
}

fn valid_name_with_owner(value: &str) -> bool {
    let mut parts = value.split('/');
    matches!((parts.next(), parts.next(), parts.next()), (Some(owner), Some(repository), None) if !owner.is_empty() && !repository.is_empty())
}

#[derive(Debug, Deserialize)]
struct ApiIssue {
    number: u64,
    html_url: String,
    title: String,
    body: Option<String>,
    labels: Vec<ApiLabel>,
    updated_at: String,
    pull_request: Option<serde_json::Value>,
}

#[derive(Debug, Deserialize)]
struct ApiLabel {
    name: String,
}

#[derive(Debug, Deserialize)]
struct ApiComment {
    id: u64,
    html_url: String,
    user: ApiUser,
    body: Option<String>,
    created_at: String,
    updated_at: String,
}

#[derive(Debug, Deserialize)]
struct ApiUser {
    login: String,
}

impl From<ApiComment> for TicketComment {
    fn from(comment: ApiComment) -> Self {
        Self {
            id: comment.id,
            url: comment.html_url,
            author: comment.user.login,
            body: comment.body.unwrap_or_default(),
            created_at: comment.created_at,
            updated_at: comment.updated_at,
        }
    }
}
