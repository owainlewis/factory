use std::fmt;
use std::fs::{self, File};
use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::Duration;
use std::{collections::BTreeMap, fmt::Write};

use anyhow::Result;
use chrono_tz::Tz;

use crate::config::{Config, TriggerKind};
use crate::hash::sha256_hex;
use crate::table;

pub fn scheduled_workflow_fingerprint(
    expression: &str,
    timezone: Tz,
    runtime: &str,
    timeout: Duration,
    prompt: &str,
) -> Result<String> {
    let definition = serde_json::to_vec(&(
        expression,
        timezone.name(),
        runtime,
        timeout.as_secs(),
        timeout.subsec_nanos(),
        prompt,
    ))?;
    Ok(format!("v2:{}", sha256_hex(definition)))
}

pub fn workflow_content_hash(entry: &WorkflowEntry) -> Result<String> {
    let definition = serde_json::to_vec(&(
        &entry.id,
        entry.trigger.as_ref().map(ToString::to_string),
        entry.runtime.as_deref(),
        entry
            .timeout
            .map(|timeout| (timeout.as_secs(), timeout.subsec_nanos())),
        entry.prompt.as_deref(),
    ))?;
    Ok(format!("v1:{}", sha256_hex(definition)))
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct WorkflowCatalog {
    pub entries: Vec<WorkflowEntry>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct WorkflowEntry {
    pub repository: PathBuf,
    pub path: PathBuf,
    pub id: String,
    pub trigger: Option<Trigger>,
    pub runtime: Option<String>,
    pub timeout: Option<Duration>,
    pub prompt: Option<String>,
    pub errors: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Trigger {
    Schedule {
        expression: String,
        timezone: Tz,
    },
    Source {
        state: String,
        labels: Vec<String>,
    },
    #[doc(hidden)]
    Label(String),
    #[doc(hidden)]
    Status(String),
}

impl WorkflowCatalog {
    pub fn load(config: &Config) -> Result<Self> {
        let repository = &config.repositories[0];
        let mut entries = config
            .triggers
            .iter()
            .map(|trigger| load_trigger(repository, trigger, config))
            .collect::<Vec<_>>();
        entries.sort_by(|left, right| left.id.cmp(&right.id));
        Ok(Self { entries })
    }

    pub fn invalid_count(&self) -> usize {
        self.entries
            .iter()
            .filter(|entry| !entry.errors.is_empty())
            .count()
    }

    pub fn invalid_scheduled_entries(&self) -> impl Iterator<Item = &WorkflowEntry> {
        self.entries.iter().filter(|entry| {
            !entry.errors.is_empty() && matches!(entry.trigger, Some(Trigger::Schedule { .. }))
        })
    }

    pub fn validate_ticket_workflows(&self) -> Result<()> {
        let errors = self
            .entries
            .iter()
            .filter(|entry| {
                !entry.errors.is_empty() && !matches!(entry.trigger, Some(Trigger::Schedule { .. }))
            })
            .map(|entry| format!("{}: {}", entry.path.display(), entry.errors.join("; ")))
            .collect::<Vec<_>>();
        if !errors.is_empty() {
            anyhow::bail!(
                "Factory cannot start with invalid ticket workflows:\n{}",
                errors.join("\n")
            );
        }
        Ok(())
    }

    pub fn validate_all(&self) -> Result<()> {
        let errors = self
            .entries
            .iter()
            .filter(|entry| !entry.errors.is_empty())
            .map(|entry| format!("{}: {}", entry.path.display(), entry.errors.join("; ")))
            .collect::<Vec<_>>();
        if !errors.is_empty() {
            anyhow::bail!("invalid Factory workflows:\n{}", errors.join("\n"));
        }
        Ok(())
    }
}

impl fmt::Display for WorkflowCatalog {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        let mut repositories = BTreeMap::<String, Vec<[String; 5]>>::new();
        for entry in &self.entries {
            repositories
                .entry(sanitize_catalog_cell(
                    &entry.repository.display().to_string(),
                ))
                .or_default()
                .push(workflow_row(entry));
        }

        for (index, (repository, rows)) in repositories.iter().enumerate() {
            if index > 0 {
                formatter.write_char('\n')?;
            }
            writeln!(formatter, "Repository: {repository}\n")?;
            formatter.write_str(&table::render(
                ["WORKFLOW", "TRIGGER", "RUNTIME", "TIMEOUT", "VALIDITY"],
                rows,
                &[],
            ))?;
        }
        Ok(())
    }
}

fn workflow_row(entry: &WorkflowEntry) -> [String; 5] {
    let trigger = entry
        .trigger
        .as_ref()
        .map(ToString::to_string)
        .unwrap_or_else(|| "-".to_owned());
    let timeout = entry
        .timeout
        .map(humantime::format_duration)
        .map(|duration| duration.to_string())
        .unwrap_or_else(|| "-".to_owned());
    let validity = if entry.errors.is_empty() {
        "valid".to_owned()
    } else {
        format!("invalid: {}", entry.errors.join("; "))
    };

    [
        entry.id.clone(),
        trigger,
        entry.runtime.clone().unwrap_or_else(|| "-".to_owned()),
        timeout,
        validity,
    ]
    .map(|cell| sanitize_catalog_cell(&cell))
}

impl fmt::Display for Trigger {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Schedule {
                expression,
                timezone,
            } => write!(formatter, "schedule {expression:?} ({timezone})"),
            Self::Source { state, labels } => {
                write!(formatter, "source state {state:?}")?;
                if !labels.is_empty() {
                    write!(formatter, " labels {labels:?}")?;
                }
                Ok(())
            }
            Self::Label(label) => write!(formatter, "label {label:?}"),
            Self::Status(status) => write!(formatter, "status {status:?}"),
        }
    }
}

fn load_trigger(
    repository: &Path,
    configured: &crate::config::TriggerConfig,
    config: &Config,
) -> WorkflowEntry {
    let trigger = match &configured.kind {
        TriggerKind::Source { state, labels } => Some(Trigger::Source {
            state: state.clone(),
            labels: labels.clone(),
        }),
        TriggerKind::Status(status) => Some(Trigger::Status(status.clone())),
        TriggerKind::Label(label) => Some(Trigger::Label(label.clone())),
        TriggerKind::Schedule {
            expression,
            timezone,
        } => timezone.parse().ok().map(|timezone| Trigger::Schedule {
            expression: expression.clone(),
            timezone,
        }),
    };
    let mut entry = WorkflowEntry {
        repository: repository.to_owned(),
        path: configured.workflow.clone(),
        id: configured.id.clone(),
        trigger,
        runtime: Some(config.default_runtime.clone()),
        timeout: Some(configured.timeout),
        prompt: None,
        errors: Vec::new(),
    };
    read_plain_prompt(repository, &mut entry);
    entry
}

fn read_plain_prompt(repository: &Path, entry: &mut WorkflowEntry) {
    let metadata = match fs::symlink_metadata(&entry.path) {
        Ok(metadata) => metadata,
        Err(error) => {
            entry
                .errors
                .push(format!("could not inspect workflow file: {error}"));
            return;
        }
    };
    if !metadata.file_type().is_file() || metadata.file_type().is_symlink() {
        entry
            .errors
            .push("workflow must be a regular file and not a symlink".to_owned());
        return;
    }
    match entry.path.canonicalize() {
        Ok(canonical) if canonical.starts_with(repository) => {}
        Ok(_) => {
            entry
                .errors
                .push("workflow resolves outside the configured repository".to_owned());
            return;
        }
        Err(error) => {
            entry
                .errors
                .push(format!("could not resolve workflow file: {error}"));
            return;
        }
    }
    let mut file = match open_workflow_file(&entry.path) {
        Ok(file) => file,
        Err(error) => {
            entry
                .errors
                .push(format!("could not safely open workflow: {error}"));
            return;
        }
    };
    let mut contents = String::new();
    if let Err(error) = file.read_to_string(&mut contents) {
        entry
            .errors
            .push(format!("could not read workflow as UTF-8: {error}"));
    } else if contents.trim().is_empty() {
        entry
            .errors
            .push("workflow prompt must not be empty".to_owned());
    } else if contents.replace("\r\n", "\n").starts_with("+++\n") {
        entry.errors.push(
            "workflow must be plain Markdown; move trigger settings into config.toml".to_owned(),
        );
    } else {
        entry.prompt = Some(contents);
    }
}

fn sanitize_catalog_cell(value: &str) -> String {
    let mut sanitized = String::with_capacity(value.len());
    for character in value.chars() {
        if character.is_control() {
            sanitized.extend(character.escape_default());
        } else {
            sanitized.push(character);
        }
    }
    sanitized
}

#[cfg(unix)]
fn open_workflow_file(path: &Path) -> std::io::Result<File> {
    use rustix::fs::{Mode, OFlags, open};

    let descriptor = open(
        path,
        OFlags::RDONLY | OFlags::CLOEXEC | OFlags::NOFOLLOW,
        Mode::empty(),
    )?;
    Ok(descriptor.into())
}

#[cfg(not(unix))]
fn open_workflow_file(path: &Path) -> std::io::Result<File> {
    File::open(path)
}
