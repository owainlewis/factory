use std::collections::HashMap;
use std::fmt;
use std::fs;
use std::path::{Path, PathBuf};
use std::str::FromStr;
use std::time::Duration;

use anyhow::Result;
use chrono_tz::Tz;
use cron::Schedule;
use serde::Deserialize;

use crate::config::Config;

const WORKFLOW_DIRECTORY: &str = ".factory/workflows";

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
    Schedule { expression: String, timezone: Tz },
    Label(String),
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Frontmatter {
    schedule: Option<String>,
    timezone: Option<String>,
    label: Option<String>,
    runtime: Option<String>,
    timeout: Option<String>,
}

impl WorkflowCatalog {
    pub fn load(config: &Config) -> Result<Self> {
        let mut entries = Vec::new();
        for repository in &config.repositories {
            entries.extend(load_repository(repository, config));
        }
        mark_duplicate_ids(&mut entries);
        entries.sort_by(|left, right| {
            (&left.repository, &left.id, &left.path).cmp(&(
                &right.repository,
                &right.id,
                &right.path,
            ))
        });
        Ok(Self { entries })
    }

    pub fn invalid_count(&self) -> usize {
        self.entries
            .iter()
            .filter(|entry| !entry.errors.is_empty())
            .count()
    }
}

impl fmt::Display for WorkflowCatalog {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        writeln!(
            formatter,
            "REPOSITORY\tWORKFLOW\tTRIGGER\tRUNTIME\tTIMEOUT\tVALIDITY"
        )?;
        for entry in &self.entries {
            let trigger = entry
                .trigger
                .as_ref()
                .map(ToString::to_string)
                .unwrap_or_else(|| "-".to_owned());
            let runtime = entry.runtime.as_deref().unwrap_or("-");
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
            writeln!(
                formatter,
                "{}\t{}\t{}\t{}\t{}\t{}",
                entry.repository.display(),
                entry.id,
                trigger,
                runtime,
                timeout,
                validity
            )?;
        }
        Ok(())
    }
}

impl fmt::Display for Trigger {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Schedule {
                expression,
                timezone,
            } => write!(formatter, "schedule {expression:?} ({timezone})"),
            Self::Label(label) => write!(formatter, "label {label:?}"),
        }
    }
}

fn load_repository(repository: &Path, config: &Config) -> Vec<WorkflowEntry> {
    let directory = repository.join(WORKFLOW_DIRECTORY);
    let metadata = match fs::symlink_metadata(&directory) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Vec::new(),
        Err(error) => {
            return vec![invalid_entry(
                repository,
                &directory,
                "<workflow-directory>",
                &format!("could not inspect workflow directory: {error}"),
            )];
        }
    };
    if !metadata.file_type().is_dir() || metadata.file_type().is_symlink() {
        return vec![invalid_entry(
            repository,
            &directory,
            "<workflow-directory>",
            "workflow path must be a regular directory and not a symlink",
        )];
    }

    let read_dir = match fs::read_dir(&directory) {
        Ok(read_dir) => read_dir,
        Err(error) => {
            return vec![invalid_entry(
                repository,
                &directory,
                "<workflow-directory>",
                &format!("could not read workflow directory: {error}"),
            )];
        }
    };
    let mut paths = Vec::new();
    let mut entries = Vec::new();
    for (index, result) in read_dir.enumerate() {
        match result {
            Ok(entry) => paths.push(entry.path()),
            Err(error) => entries.push(invalid_entry(
                repository,
                &directory,
                &format!("<unreadable-entry-{index}>"),
                &format!("could not read workflow directory entry: {error}"),
            )),
        }
    }
    paths.sort();

    entries.extend(
        paths
            .into_iter()
            .filter(|path| is_markdown_path(path))
            .map(|path| load_file(repository, &path, config)),
    );
    entries
}

fn load_file(repository: &Path, path: &Path, config: &Config) -> WorkflowEntry {
    let id = workflow_id(path);
    let mut entry = WorkflowEntry {
        repository: repository.to_path_buf(),
        path: path.to_path_buf(),
        id,
        trigger: None,
        runtime: None,
        timeout: None,
        prompt: None,
        errors: Vec::new(),
    };

    if !valid_workflow_id(&entry.id) {
        entry.errors.push(format!(
            "filename must produce a lowercase kebab-case workflow ID, got {:?}",
            entry.id
        ));
    }

    let metadata = match fs::symlink_metadata(path) {
        Ok(metadata) => metadata,
        Err(error) => {
            entry
                .errors
                .push(format!("could not inspect file: {error}"));
            return entry;
        }
    };
    if !metadata.file_type().is_file() || metadata.file_type().is_symlink() {
        entry
            .errors
            .push("workflow must be a regular file and not a symlink".to_owned());
        return entry;
    }

    let contents = match fs::read_to_string(path) {
        Ok(contents) => contents,
        Err(error) => {
            entry
                .errors
                .push(format!("could not read workflow as UTF-8: {error}"));
            return entry;
        }
    };
    let (frontmatter, prompt) = match split_frontmatter(&contents) {
        Ok(parts) => parts,
        Err(error) => {
            entry.errors.push(error);
            return entry;
        }
    };
    if prompt.trim().is_empty() {
        entry
            .errors
            .push("prompt body must not be empty".to_owned());
    } else {
        entry.prompt = Some(prompt);
    }

    let raw: Frontmatter = match toml::from_str(&frontmatter) {
        Ok(raw) => raw,
        Err(error) => {
            entry
                .errors
                .push(format!("invalid TOML frontmatter: {error}"));
            return entry;
        }
    };
    let Frontmatter {
        schedule,
        timezone,
        label,
        runtime,
        timeout,
    } = raw;
    entry.runtime = resolve_runtime(runtime, &config.default_runtime, &mut entry.errors);
    entry.timeout = resolve_timeout(
        timeout,
        config.default_timeout,
        config.maximum_timeout,
        &mut entry.errors,
    );
    entry.trigger = resolve_trigger(schedule, timezone, label, &mut entry.errors);
    entry
}

fn split_frontmatter(contents: &str) -> std::result::Result<(String, String), String> {
    let contents = contents.replace("\r\n", "\n");
    let Some(after_opening) = contents.strip_prefix("+++\n") else {
        return Err("workflow must begin with TOML frontmatter delimited by +++".to_owned());
    };
    let Some(closing) = after_opening
        .lines()
        .scan(0, |offset, line| {
            let start = *offset;
            *offset += line.len() + 1;
            Some((start, line))
        })
        .find_map(|(offset, line)| (line == "+++").then_some(offset))
    else {
        return Err("workflow frontmatter is missing its closing +++ delimiter".to_owned());
    };
    let prompt_start = closing + "+++".len();
    let prompt_start = if after_opening.as_bytes().get(prompt_start) == Some(&b'\n') {
        prompt_start + 1
    } else {
        prompt_start
    };
    Ok((
        after_opening[..closing].to_owned(),
        after_opening[prompt_start..].to_owned(),
    ))
}

fn resolve_runtime(
    runtime: Option<String>,
    default: &str,
    errors: &mut Vec<String>,
) -> Option<String> {
    let runtime = runtime.as_deref().unwrap_or(default).trim();
    if runtime.is_empty() {
        errors.push("runtime must not be empty".to_owned());
        None
    } else {
        Some(runtime.to_owned())
    }
}

fn resolve_timeout(
    timeout: Option<String>,
    default: Duration,
    maximum: Duration,
    errors: &mut Vec<String>,
) -> Option<Duration> {
    let Some(timeout) = timeout else {
        return Some(default);
    };
    match humantime::parse_duration(&timeout) {
        Ok(duration) if duration.is_zero() => {
            errors.push("timeout must be greater than zero".to_owned());
            None
        }
        Ok(duration) if duration > maximum => {
            errors.push(format!(
                "timeout {} exceeds maximum_timeout {}",
                humantime::format_duration(duration),
                humantime::format_duration(maximum)
            ));
            None
        }
        Ok(duration) => Some(duration),
        Err(error) => {
            errors.push(format!("timeout has invalid duration {timeout:?}: {error}"));
            None
        }
    }
}

fn resolve_trigger(
    schedule: Option<String>,
    timezone: Option<String>,
    label: Option<String>,
    errors: &mut Vec<String>,
) -> Option<Trigger> {
    match (schedule, label) {
        (Some(_), Some(_)) => {
            errors.push("workflow must declare exactly one trigger, not schedule and label".into());
            None
        }
        (None, None) => {
            errors.push("workflow must declare exactly one trigger: schedule or label".into());
            None
        }
        (Some(schedule), None) => {
            let error_count = errors.len();
            let timezone = match timezone {
                Some(timezone) => match Tz::from_str(timezone.trim()) {
                    Ok(timezone) => Some(timezone),
                    Err(_) => {
                        errors.push(format!("timezone is invalid: {timezone:?}"));
                        None
                    }
                },
                None => {
                    errors.push("scheduled workflow must declare timezone".to_owned());
                    None
                }
            };
            if !valid_cron(&schedule) {
                errors.push(format!(
                    "schedule must be a valid five-field cron expression: {schedule:?}"
                ));
            }
            if errors.len() == error_count {
                Some(Trigger::Schedule {
                    expression: schedule,
                    timezone: timezone.expect("timezone was validated"),
                })
            } else {
                None
            }
        }
        (None, Some(label)) => {
            let error_count = errors.len();
            if timezone.is_some() {
                errors.push("timezone is only valid with a schedule trigger".to_owned());
            }
            if !valid_label(&label) {
                errors.push(
                    "label must be 1-50 characters without leading, trailing, or control whitespace"
                        .to_owned(),
                );
            }
            if errors.len() == error_count {
                Some(Trigger::Label(label))
            } else {
                None
            }
        }
    }
}

fn valid_cron(value: &str) -> bool {
    value.split_whitespace().count() == 5 && Schedule::from_str(&format!("0 {value}")).is_ok()
}

fn valid_label(value: &str) -> bool {
    !value.is_empty()
        && value.chars().count() <= 50
        && value.trim() == value
        && !value.chars().any(char::is_control)
}

fn is_markdown_path(path: &Path) -> bool {
    path.extension()
        .and_then(|extension| extension.to_str())
        .is_some_and(|extension| extension.eq_ignore_ascii_case("md"))
}

fn workflow_id(path: &Path) -> String {
    path.file_stem()
        .and_then(|stem| stem.to_str())
        .unwrap_or("<invalid-filename>")
        .to_ascii_lowercase()
}

fn valid_workflow_id(id: &str) -> bool {
    !id.is_empty()
        && id != "<invalid-filename>"
        && id.split('-').all(|part| {
            !part.is_empty()
                && part
                    .chars()
                    .all(|character| character.is_ascii_lowercase() || character.is_ascii_digit())
        })
}

fn mark_duplicate_ids(entries: &mut [WorkflowEntry]) {
    let mut groups: HashMap<(PathBuf, String), Vec<usize>> = HashMap::new();
    for (index, entry) in entries.iter().enumerate() {
        groups
            .entry((entry.repository.clone(), entry.id.clone()))
            .or_default()
            .push(index);
    }
    for ((_, id), indices) in groups {
        if indices.len() > 1 {
            for index in indices {
                entries[index]
                    .errors
                    .push(format!("duplicate workflow ID {id:?} in repository"));
            }
        }
    }
}

fn invalid_entry(repository: &Path, path: &Path, id: &str, error: &str) -> WorkflowEntry {
    WorkflowEntry {
        repository: repository.to_path_buf(),
        path: path.to_path_buf(),
        id: id.to_owned(),
        trigger: None,
        runtime: None,
        timeout: None,
        prompt: None,
        errors: vec![error.to_owned()],
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validates_five_field_cron() {
        assert!(valid_cron("0 9 * * 1"));
        assert!(!valid_cron("0 0 9 * * 1"));
        assert!(!valid_cron("eventually"));
    }

    #[test]
    fn marks_every_duplicate_invalid() {
        let repository = PathBuf::from("/repo");
        let mut entries = vec![
            invalid_entry(&repository, Path::new("one.md"), "same", "first"),
            invalid_entry(&repository, Path::new("two.md"), "same", "second"),
        ];

        mark_duplicate_ids(&mut entries);

        assert!(entries.iter().all(|entry| {
            entry
                .errors
                .iter()
                .any(|error| error.contains("duplicate workflow ID"))
        }));
    }
}
