use std::collections::HashMap;
use std::fmt;
use std::fs::{self, File};
use std::io::Read;
use std::path::{Path, PathBuf};
use std::str::FromStr;
use std::time::Duration;

use anyhow::Result;
use chrono_tz::Tz;
use cron::Schedule;
use serde::Deserialize;
use sha2::{Digest, Sha256};

use crate::config::Config;

const WORKFLOW_DIRECTORY: &str = ".factory/workflows";

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
    Ok(format!("v2:{:x}", Sha256::digest(definition)))
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
    pub(crate) is_schedule_workflow: bool,
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

    pub fn invalid_scheduled_entries(&self) -> impl Iterator<Item = &WorkflowEntry> {
        self.entries
            .iter()
            .filter(|entry| !entry.errors.is_empty() && entry.is_schedule_workflow)
    }

    pub fn validate_ticket_workflows(&self) -> Result<()> {
        let errors = self
            .entries
            .iter()
            .filter(|entry| !entry.errors.is_empty() && !entry.is_schedule_workflow)
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
}

impl fmt::Display for WorkflowCatalog {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        let headers = [
            "REPOSITORY",
            "WORKFLOW",
            "TRIGGER",
            "RUNTIME",
            "TIMEOUT",
            "VALIDITY",
        ]
        .map(sanitize_catalog_cell);
        writeln!(formatter, "{}", headers.join("\t"))?;
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
            let cells = [
                entry.repository.display().to_string(),
                entry.id.clone(),
                trigger,
                runtime.to_owned(),
                timeout,
                validity,
            ]
            .map(|cell| sanitize_catalog_cell(&cell));
            writeln!(formatter, "{}", cells.join("\t"))?;
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
    let factory_directory = repository.join(".factory");
    let directory = repository.join(WORKFLOW_DIRECTORY);
    let factory_metadata = match fs::symlink_metadata(&factory_directory) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Vec::new(),
        Err(error) => {
            return vec![invalid_entry(
                repository,
                &directory,
                "<workflow-directory>",
                &format!("could not inspect .factory directory: {error}"),
            )];
        }
    };
    if !factory_metadata.file_type().is_dir() && !factory_metadata.file_type().is_symlink() {
        return vec![invalid_entry(
            repository,
            &directory,
            "<workflow-directory>",
            ".factory path must be a directory",
        )];
    }

    let metadata = match fs::symlink_metadata(&directory) {
        Ok(metadata) => metadata,
        Err(error)
            if error.kind() == std::io::ErrorKind::NotFound
                && !factory_metadata.file_type().is_symlink() =>
        {
            return Vec::new();
        }
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
    let canonical_directory = match directory.canonicalize() {
        Ok(directory) => directory,
        Err(error) => {
            return vec![invalid_entry(
                repository,
                &directory,
                "<workflow-directory>",
                &format!("could not resolve workflow directory: {error}"),
            )];
        }
    };
    if !canonical_directory.starts_with(repository) {
        return vec![invalid_entry(
            repository,
            &directory,
            "<workflow-directory>",
            "workflow directory resolves outside the configured repository",
        )];
    }

    let read_dir = match fs::read_dir(&canonical_directory) {
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
        is_schedule_workflow: false,
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
    match path.canonicalize() {
        Ok(canonical) if canonical.starts_with(repository) => {}
        Ok(_) => {
            entry
                .errors
                .push("workflow resolves outside the configured repository".to_owned());
            return entry;
        }
        Err(error) => {
            entry
                .errors
                .push(format!("could not resolve workflow file: {error}"));
            return entry;
        }
    }

    let mut file = match open_workflow_file(path) {
        Ok(file) => file,
        Err(error) => {
            entry
                .errors
                .push(format!("could not safely open workflow: {error}"));
            return entry;
        }
    };
    match file.metadata() {
        Ok(metadata) if metadata.is_file() => {}
        Ok(_) => {
            entry
                .errors
                .push("opened workflow is not a regular file".to_owned());
            return entry;
        }
        Err(error) => {
            entry
                .errors
                .push(format!("could not inspect opened workflow: {error}"));
            return entry;
        }
    }
    let mut contents = String::new();
    if let Err(error) = file.read_to_string(&mut contents) {
        entry
            .errors
            .push(format!("could not read workflow as UTF-8: {error}"));
        return entry;
    }
    let (frontmatter, prompt) = match split_frontmatter(&contents) {
        Ok(parts) => parts,
        Err(error) => {
            entry.is_schedule_workflow = missing_frontmatter_declares_only_schedule(&contents);
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

    entry.is_schedule_workflow = toml::from_str::<toml::Value>(&frontmatter)
        .ok()
        .and_then(|value| {
            value
                .as_table()
                .map(|table| table.contains_key("schedule") && !table.contains_key("label"))
        })
        .unwrap_or_else(|| declares_only_schedule(&frontmatter));
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

fn missing_frontmatter_declares_only_schedule(contents: &str) -> bool {
    let contents = contents.replace("\r\n", "\n");
    let Some(after_opening) = contents.strip_prefix("+++\n") else {
        return false;
    };
    let mut frontmatter_prefix = String::new();
    let mut multiline_delimiter = None;
    for line in after_opening.lines() {
        let trimmed = line.trim_start();
        if let Some(delimiter) = multiline_delimiter {
            frontmatter_prefix.push_str(line);
            frontmatter_prefix.push('\n');
            if contains_multiline_closing(trimmed, delimiter) {
                multiline_delimiter = None;
            }
            continue;
        }
        if trimmed.is_empty() || trimmed.starts_with('#') {
            frontmatter_prefix.push_str(line);
            frontmatter_prefix.push('\n');
            continue;
        }
        let Some((_, value)) = trimmed.split_once('=') else {
            break;
        };
        frontmatter_prefix.push_str(line);
        frontmatter_prefix.push('\n');
        let value = value.trim_start();
        for delimiter in ["\"\"\"", "'''"] {
            if let Some(remainder) = value.strip_prefix(delimiter)
                && !contains_multiline_closing(remainder, delimiter)
            {
                multiline_delimiter = Some(delimiter);
                break;
            }
        }
    }
    declares_only_schedule(&frontmatter_prefix)
}

fn declares_only_schedule(frontmatter: &str) -> bool {
    let mut schedule = false;
    let mut label = false;
    let mut multiline_delimiter = None;
    for line in frontmatter.lines() {
        let line = line.trim_start();
        if let Some(delimiter) = multiline_delimiter {
            if contains_multiline_closing(line, delimiter) {
                multiline_delimiter = None;
            }
            continue;
        }
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line.starts_with('[') {
            return false;
        }
        schedule |= line_declares_key(line, "schedule");
        label |= line_declares_key(line, "label");
        let Some((_, value)) = line.split_once('=') else {
            return false;
        };
        let value = value.trim_start();
        if matches!(value.as_bytes().first(), Some(b'{') | Some(b'['))
            && !inline_container_closes(value)
        {
            return false;
        }
        for delimiter in ["\"\"\"", "'''"] {
            if let Some(remainder) = value.strip_prefix(delimiter)
                && !contains_multiline_closing(remainder, delimiter)
            {
                multiline_delimiter = Some(delimiter);
                break;
            }
        }
    }
    schedule && !label && multiline_delimiter.is_none()
}

fn line_declares_key(line: &str, expected: &str) -> bool {
    let bytes = line.as_bytes();
    let mut index = 0;
    let mut brace_depth = 0_u32;
    let mut bracket_depth = 0_u32;
    let mut expecting_value = false;
    while index < bytes.len() {
        if bytes[index] == b'#' {
            return false;
        }
        if matches!(bytes[index], b'\'' | b'"') {
            let quoted_key_start = index;
            let quote = bytes[index];
            let start = index + 1;
            index = start;
            let mut escaped = false;
            while index < bytes.len() {
                if quote == b'"' && bytes[index] == b'\\' && !escaped {
                    escaped = true;
                    index += 1;
                    continue;
                }
                if bytes[index] == quote && !escaped {
                    break;
                }
                escaped = false;
                index += 1;
            }
            if expecting_value {
                expecting_value = false;
            } else if index < bytes.len()
                && brace_depth == 0
                && bracket_depth == 0
                && previous_non_whitespace(bytes, quoted_key_start) != Some(b'.')
                && &line[start..index] == expected
                && bytes[index + 1..]
                    .iter()
                    .copied()
                    .find(|byte| !byte.is_ascii_whitespace())
                    == Some(b'=')
            {
                return true;
            }
            index = index.saturating_add(1);
            continue;
        }
        if bytes[index].is_ascii_alphanumeric() || matches!(bytes[index], b'_' | b'-') {
            let start = index;
            index += 1;
            while index < bytes.len()
                && (bytes[index].is_ascii_alphanumeric() || matches!(bytes[index], b'_' | b'-'))
            {
                index += 1;
            }
            if expecting_value {
                expecting_value = false;
            } else if brace_depth == 0
                && bracket_depth == 0
                && previous_non_whitespace(bytes, start) != Some(b'.')
                && &line[start..index] == expected
                && bytes[index..]
                    .iter()
                    .copied()
                    .find(|byte| !byte.is_ascii_whitespace())
                    == Some(b'=')
            {
                return true;
            }
            continue;
        }
        match bytes[index] {
            b'=' if brace_depth == 0 && bracket_depth == 0 => expecting_value = true,
            b'{' => {
                brace_depth = brace_depth.saturating_add(1);
                expecting_value = false;
            }
            b'}' => brace_depth = brace_depth.saturating_sub(1),
            b'[' => {
                bracket_depth = bracket_depth.saturating_add(1);
                expecting_value = false;
            }
            b']' => bracket_depth = bracket_depth.saturating_sub(1),
            _ => {}
        }
        index += 1;
    }
    false
}

fn previous_non_whitespace(bytes: &[u8], before: usize) -> Option<u8> {
    bytes[..before]
        .iter()
        .rev()
        .copied()
        .find(|byte| !byte.is_ascii_whitespace())
}

fn contains_multiline_closing(value: &str, delimiter: &str) -> bool {
    value.match_indices(delimiter).any(|(index, _)| {
        delimiter == "'''"
            || value.as_bytes()[..index]
                .iter()
                .rev()
                .take_while(|byte| **byte == b'\\')
                .count()
                .is_multiple_of(2)
    })
}

fn inline_container_closes(value: &str) -> bool {
    let mut stack = Vec::new();
    let mut quote = None;
    let mut escaped = false;
    for (index, character) in value.char_indices() {
        if let Some(active_quote) = quote {
            if active_quote == '"' && character == '\\' && !escaped {
                escaped = true;
                continue;
            }
            if character == active_quote && !escaped {
                quote = None;
            }
            escaped = false;
            continue;
        }
        match character {
            '"' | '\'' => quote = Some(character),
            '{' => stack.push('}'),
            '[' => stack.push(']'),
            '}' | ']' if stack.pop() != Some(character) => return false,
            '}' | ']' if stack.is_empty() => {
                let trailing = value[index + character.len_utf8()..].trim_start();
                return trailing.is_empty() || trailing.starts_with('#');
            }
            _ => {}
        }
    }
    false
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

fn mark_duplicate_ids(entries: &mut [WorkflowEntry]) {
    let mut groups: HashMap<(PathBuf, String), Vec<usize>> = HashMap::new();
    for (index, entry) in entries.iter().enumerate() {
        if entry.is_schedule_workflow && !entry.errors.is_empty() {
            continue;
        }
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
        is_schedule_workflow: false,
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
    fn malformed_frontmatter_schedule_detection_is_conservative() {
        assert!(declares_only_schedule(
            "schedule = \"unterminated\ntimezone = \"UTC\""
        ));
        assert!(declares_only_schedule(
            "timezone = \"UTC\"\nruntime = \"codex\"\nschedule = \"unterminated"
        ));
        assert!(declares_only_schedule(
            "timezone = \"UTC\"\n\"schedule\" = \"unterminated"
        ));
        assert!(!declares_only_schedule(
            "description = \"\"\"\nschedule = \"not-a-key"
        ));
        assert!(!declares_only_schedule(
            r#"description = """
escaped triple: \"""
schedule = "not-a-key"#
        ));
        assert!(!declares_only_schedule(
            "[metadata]\nschedule = \"unterminated"
        ));
        assert!(!declares_only_schedule(
            "metadata = {\nschedule = \"unterminated"
        ));
        assert!(!declares_only_schedule(
            "metadata = [\nschedule = \"unterminated"
        ));
        assert!(!declares_only_schedule(
            "\"schedule=not-trigger\" = \"unterminated"
        ));
        assert!(!declares_only_schedule(
            "schedule = \"unterminated\nlabel = \"factory:ready\""
        ));
        assert!(!declares_only_schedule(
            "schedule = \"unterminated\n\"label\" = \"factory:ready\""
        ));
        assert!(!declares_only_schedule(
            "schedule = \"0 9 * * 1\" label = \"factory:ready\""
        ));
        assert!(declares_only_schedule(
            "schedule = \"0 9 * * 1\"\ndescription = \"label = factory:ready\""
        ));
        assert!(declares_only_schedule(
            "schedule = \"0 9 * * 1\"\nmetadata = { label = \"triage\" }\nbroken = ???"
        ));
        assert!(declares_only_schedule(
            "schedule = \"0 9 * * 1\"\nmetadata.label = \"triage\"\nbroken = ???"
        ));
        assert!(declares_only_schedule(
            "schedule = \"0 9 * * 1\"\n\"metadata\".\"label\" = \"triage\"\nbroken = ???"
        ));
        assert!(!declares_only_schedule("description = \"schedule\" = ???"));
        assert!(!declares_only_schedule("description = schedule = ???"));
        assert!(declares_only_schedule(
            "schedule = \"0 9 * * 1\"\ndescription = \"label\" = ???"
        ));
        assert!(declares_only_schedule(
            "schedule = \"0 9 * * 1\"\ndescription = label = ???"
        ));
        assert!(missing_frontmatter_declares_only_schedule(
            "+++\nschedule = \"0 9 * * 1\"\ntimezone = \"UTC\"\nImplement maintenance.\n"
        ));
        assert!(missing_frontmatter_declares_only_schedule(
            "+++\nschedule = \"0 9 * * 1\"\ntimezone = \"UTC\"\nCheck x = y before editing.\n"
        ));
        assert!(missing_frontmatter_declares_only_schedule(
            "+++\nschedule = \"0 9 * * 1\"\ntimeout = O(n) before pruning.\n"
        ));
        assert!(missing_frontmatter_declares_only_schedule(
            "+++\nschedule = \"0 9 * * 1\"\nruntime = \"\"\"\ncodex\n\"\"\"\nPrompt.\n"
        ));
        assert!(!missing_frontmatter_declares_only_schedule(
            "+++\nschedule = \"0 9 * * 1\"\nsurprise = true\nlabel = \"factory:ready\"\nPrompt.\n"
        ));
        assert!(!missing_frontmatter_declares_only_schedule(
            "+++\nschedule = \"0 9 * * 1\"\nlabel = \"factory:ready\"\nImplement it.\n"
        ));
        assert!(!missing_frontmatter_declares_only_schedule(
            "schedule = \"0 9 * * 1\"\ntimezone = \"UTC\"\n"
        ));
        assert!(!missing_frontmatter_declares_only_schedule(
            "+++\nschedule = \"0 9 * * 1\"\nruntime = \"\"\"\ncodex\n\"\"\"\nlabel = \"factory:ready\"\nPrompt.\n"
        ));
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

    #[test]
    fn invalid_schedule_does_not_poison_ticket_with_the_same_id() {
        let repository = PathBuf::from("/repo");
        let mut skipped_schedule = invalid_entry(
            &repository,
            Path::new("SAME.md"),
            "same",
            "invalid schedule",
        );
        skipped_schedule.is_schedule_workflow = true;
        let ticket = WorkflowEntry {
            repository: repository.clone(),
            path: PathBuf::from("same.md"),
            id: "same".to_owned(),
            trigger: Some(Trigger::Label("factory:ready".to_owned())),
            runtime: Some("codex".to_owned()),
            timeout: Some(Duration::from_secs(60)),
            prompt: Some("implement".to_owned()),
            errors: Vec::new(),
            is_schedule_workflow: false,
        };
        let mut entries = vec![skipped_schedule, ticket];

        mark_duplicate_ids(&mut entries);

        assert!(entries[1].errors.is_empty());
        assert!(
            !entries[0]
                .errors
                .iter()
                .any(|error| error.contains("duplicate workflow ID"))
        );
    }

    #[cfg(unix)]
    #[test]
    fn no_follow_open_rejects_symlinks() {
        use std::os::unix::fs::symlink;

        let temp = tempfile::tempdir().unwrap();
        let target = temp.path().join("target.md");
        let link = temp.path().join("link.md");
        fs::write(&target, "prompt").unwrap();
        symlink(target, &link).unwrap();

        assert!(open_workflow_file(&link).is_err());
    }
}
