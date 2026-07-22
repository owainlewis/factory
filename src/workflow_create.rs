use std::fmt;
use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, bail};
use tempfile::NamedTempFile;
use tokio_util::sync::CancellationToken;
use toml_edit::{DocumentMut, value};

use crate::config::Config;
use crate::github::GitHubClient;
use crate::init::{discover_repository, validate_optional_directory};
use crate::workflow::WorkflowCatalog;

#[derive(Debug, Clone)]
pub struct CreateWorkflowOptions {
    pub id: String,
    pub repository: PathBuf,
    pub config_path: PathBuf,
    pub schedule: Option<String>,
    pub timezone: Option<String>,
    pub label: Option<String>,
    pub state: Option<String>,
    pub runtime: Option<String>,
    pub timeout: Option<String>,
    pub prompt: Option<String>,
    pub prompt_file: Option<PathBuf>,
}

#[derive(Debug, Clone)]
pub struct CreateWorkflowReport {
    repository: PathBuf,
    path: PathBuf,
    created_label: Option<String>,
}

impl fmt::Display for CreateWorkflowReport {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        writeln!(formatter, "Created workflow {}", self.path.display())?;
        if let Some(label) = &self.created_label {
            writeln!(formatter, "Created GitHub label {label}")?;
        }
        writeln!(formatter, "Next:")?;
        writeln!(
            formatter,
            "  git -C {} add {}",
            shell_quote(&self.repository),
            shell_quote(
                self.path
                    .strip_prefix(&self.repository)
                    .unwrap_or(&self.path)
            )
        )?;
        writeln!(formatter, "  factory workflows")?;
        writeln!(formatter, "  factory daemon")
    }
}

pub async fn create_workflow(
    options: CreateWorkflowOptions,
    github: &GitHubClient,
) -> Result<CreateWorkflowReport> {
    validate_workflow_id(&options.id)?;
    validate_options(&options)?;

    let repository = discover_repository(&options.repository)?;
    let config = Config::load(&options.config_path)?;
    if !config.repositories.iter().any(|item| item == &repository) {
        bail!(
            "repository is not configured: {}; run factory init from that repository",
            repository.display()
        );
    }

    let factory_directory = repository.join(".factory");
    let workflow_directory = factory_directory.join("workflows");
    validate_optional_directory(&factory_directory)?;
    validate_optional_directory(&workflow_directory)?;
    if !workflow_directory.is_dir() {
        bail!(
            "workflow directory does not exist: {}; run factory init from that repository",
            workflow_directory.display()
        );
    }

    let path = workflow_directory.join(format!("{}.md", options.id));
    match fs::symlink_metadata(&path) {
        Ok(_) => bail!("workflow already exists: {}", path.display()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => {
            return Err(error)
                .with_context(|| format!("failed to inspect workflow {}", path.display()));
        }
    }

    let prompt = read_prompt(&options)?;
    let contents = render_workflow(&options, &prompt);
    persist_new_file(&path, &contents)?;

    let catalog = WorkflowCatalog::load(&config)?;
    let validation_errors = catalog
        .entries
        .iter()
        .find(|entry| entry.repository == repository && entry.path == path)
        .map(|entry| entry.errors.clone());
    let Some(validation_errors) = validation_errors else {
        rollback_new_file(&path, "created workflow was not discovered")?;
        bail!("created workflow was not discovered");
    };
    if !validation_errors.is_empty() {
        rollback_new_file(&path, "created workflow was invalid")?;
        bail!("workflow is invalid: {}", validation_errors.join("; "));
    }

    let created_label = if let Some(label) = &options.label {
        match ensure_label(github, &repository, &options.id, label).await {
            Ok(created) => created.then(|| label.clone()),
            Err(error) => {
                rollback_new_file(&path, "GitHub label setup failed")?;
                return Err(error);
            }
        }
    } else {
        None
    };

    Ok(CreateWorkflowReport {
        repository,
        path,
        created_label,
    })
}

async fn ensure_label(
    github: &GitHubClient,
    repository: &Path,
    workflow_id: &str,
    label: &str,
) -> Result<bool> {
    let cancellation = CancellationToken::new();
    github.validate_global(&cancellation).await?;
    github
        .validate_repository(repository, &cancellation)
        .await?;
    let labels = github.labels(repository, &cancellation).await?;
    if labels.iter().any(|existing| existing == label) {
        return Ok(false);
    }
    github
        .create_label(
            repository,
            label,
            &format!("Triggers the {workflow_id} Factory workflow"),
            "5319E7",
            &cancellation,
        )
        .await?;
    Ok(true)
}

fn validate_options(options: &CreateWorkflowOptions) -> Result<()> {
    let trigger_count = usize::from(options.schedule.is_some())
        + usize::from(options.label.is_some())
        + usize::from(options.state.is_some());
    if trigger_count != 1 {
        bail!("workflow must declare exactly one trigger: schedule, label, or state");
    }
    match (&options.schedule, &options.label, &options.state) {
        (Some(_), None, None) if options.timezone.is_none() => {
            bail!("scheduled workflow must declare a timezone")
        }
        (None, Some(_), None) | (None, None, Some(_)) if options.timezone.is_some() => {
            bail!("timezone is only valid with a schedule trigger")
        }
        _ => {}
    }
    match (&options.prompt, &options.prompt_file) {
        (Some(_), Some(_)) => bail!("use exactly one of --prompt or --prompt-file"),
        (None, None) => bail!("use exactly one of --prompt or --prompt-file"),
        _ => Ok(()),
    }
}

fn validate_workflow_id(id: &str) -> Result<()> {
    let valid = !id.is_empty()
        && id.split('-').all(|part| {
            !part.is_empty()
                && part
                    .chars()
                    .all(|character| character.is_ascii_lowercase() || character.is_ascii_digit())
        });
    if !valid {
        bail!("workflow ID must be lowercase kebab-case, got {id:?}");
    }
    Ok(())
}

fn read_prompt(options: &CreateWorkflowOptions) -> Result<String> {
    let prompt = if let Some(prompt) = &options.prompt {
        prompt.clone()
    } else {
        let path = options
            .prompt_file
            .as_deref()
            .context("prompt source was not provided")?;
        if path == Path::new("-") {
            let mut prompt = String::new();
            std::io::stdin()
                .read_to_string(&mut prompt)
                .context("failed to read workflow prompt from stdin")?;
            prompt
        } else {
            fs::read_to_string(path)
                .with_context(|| format!("failed to read prompt file {}", path.display()))?
        }
    };
    if prompt.trim().is_empty() {
        bail!("workflow prompt must not be empty");
    }
    Ok(prompt)
}

fn render_workflow(options: &CreateWorkflowOptions, prompt: &str) -> String {
    let mut frontmatter = DocumentMut::new();
    if let Some(schedule) = &options.schedule {
        frontmatter["schedule"] = value(schedule);
    }
    if let Some(timezone) = &options.timezone {
        frontmatter["timezone"] = value(timezone);
    }
    if let Some(label) = &options.label {
        frontmatter["label"] = value(label);
    }
    if let Some(state) = &options.state {
        frontmatter["state"] = value(state);
    }
    if let Some(runtime) = &options.runtime {
        frontmatter["runtime"] = value(runtime);
    }
    if let Some(timeout) = &options.timeout {
        frontmatter["timeout"] = value(timeout);
    }
    format!("+++\n{}+++\n\n{}\n", frontmatter, prompt.trim())
}

fn persist_new_file(path: &Path, contents: &str) -> Result<()> {
    let parent = path
        .parent()
        .context("workflow path has no parent directory")?;
    let mut temporary = NamedTempFile::new_in(parent).with_context(|| {
        format!(
            "failed to create temporary workflow in {}",
            parent.display()
        )
    })?;
    temporary
        .write_all(contents.as_bytes())
        .with_context(|| format!("failed to write temporary workflow for {}", path.display()))?;
    temporary
        .as_file_mut()
        .sync_all()
        .with_context(|| format!("failed to sync temporary workflow for {}", path.display()))?;
    temporary
        .persist_noclobber(path)
        .map_err(|error| error.error)
        .with_context(|| format!("failed to create workflow {}", path.display()))?;
    if let Err(error) = sync_parent(parent) {
        if let Err(cleanup_error) = rollback_new_file(path, "workflow directory sync failed") {
            return Err(error.context(format!("rollback also failed: {cleanup_error:#}")));
        }
        return Err(error);
    }
    Ok(())
}

fn rollback_new_file(path: &Path, reason: &str) -> Result<()> {
    fs::remove_file(path).with_context(|| {
        format!(
            "{reason} and the new workflow could not be removed: {}",
            path.display()
        )
    })?;
    let parent = path
        .parent()
        .context("workflow path has no parent directory")?;
    sync_parent(parent).with_context(|| {
        format!("{reason}; the new workflow was removed but the rollback could not be synced")
    })
}

fn shell_quote(path: &Path) -> String {
    format!("'{}'", path.to_string_lossy().replace('\'', "'\"'\"'"))
}

#[cfg(unix)]
fn sync_parent(parent: &Path) -> Result<()> {
    fs::OpenOptions::new()
        .read(true)
        .open(parent)
        .and_then(|directory| directory.sync_all())
        .with_context(|| format!("failed to sync directory {}", parent.display()))
}

#[cfg(not(unix))]
fn sync_parent(_parent: &Path) -> Result<()> {
    Ok(())
}
