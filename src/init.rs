use std::fmt;
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::Command;

use anyhow::{Context, Result, bail};
use tempfile::NamedTempFile;
use tokio_util::sync::CancellationToken;
use toml_edit::{Array, DocumentMut, Item, Value, value};

use crate::config::Config;
use crate::github::GitHubClient;

const WORKFLOW_RELATIVE_PATH: &str = ".factory/workflows/implement-ready-ticket.md";
const DEFAULT_WORKFLOW: &str = include_str!("../examples/implement-ready-ticket.md");

const READY_LABEL: Label = Label {
    name: "factory:ready",
    description: "Implementation is authorised and ready for Factory",
    color: "0E8A16",
};
const NEEDS_REVIEW_LABEL: Label = Label {
    name: "factory:needs-review",
    description: "A human must inspect a question, decision, or green PR",
    color: "FBCA04",
};

#[derive(Debug, Clone)]
pub struct InitOptions {
    pub repository: PathBuf,
    pub config_path: PathBuf,
    pub no_labels: bool,
    pub check: bool,
    pub update_workflow: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum PlannedAction {
    Create,
    Update,
    Unchanged,
    Conflict,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ResourceStatus {
    Created,
    Updated,
    Unchanged,
    WouldCreate,
    WouldUpdate,
    Conflict,
    Failed,
    Skipped,
}

impl ResourceStatus {
    fn label(self) -> &'static str {
        match self {
            Self::Created => "created",
            Self::Updated => "updated",
            Self::Unchanged => "unchanged",
            Self::WouldCreate => "would create",
            Self::WouldUpdate => "would update",
            Self::Conflict => "conflict",
            Self::Failed => "failed",
            Self::Skipped => "skipped",
        }
    }
}

#[derive(Debug, Clone)]
struct ResourceResult {
    status: ResourceStatus,
    resource: String,
    detail: Option<String>,
}

#[derive(Debug, Clone)]
pub struct InitReport {
    repository: PathBuf,
    name_with_owner: Option<String>,
    resources: Vec<ResourceResult>,
    check: bool,
}

impl InitReport {
    pub fn exit_code(&self) -> u8 {
        u8::from(self.resources.iter().any(|resource| {
            matches!(
                resource.status,
                ResourceStatus::WouldCreate
                    | ResourceStatus::WouldUpdate
                    | ResourceStatus::Conflict
                    | ResourceStatus::Failed
            )
        }))
    }
}

impl fmt::Display for InitReport {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        if let Some(name) = &self.name_with_owner {
            writeln!(
                formatter,
                "Factory initialization for {name} ({})",
                self.repository.display()
            )?;
        } else {
            writeln!(
                formatter,
                "Factory initialization for {}",
                self.repository.display()
            )?;
        }
        for resource in &self.resources {
            write!(
                formatter,
                "{}: {}",
                resource.status.label(),
                resource.resource
            )?;
            if let Some(detail) = &resource.detail {
                write!(formatter, " ({detail})")?;
            }
            writeln!(formatter)?;
        }

        if self.check {
            if self.exit_code() == 0 {
                writeln!(
                    formatter,
                    "Factory setup is complete; no changes were made."
                )
            } else {
                writeln!(
                    formatter,
                    "Factory setup is incomplete; run factory init to apply these changes."
                )
            }
        } else if self.exit_code() == 0 {
            writeln!(formatter, "Next:")?;
            writeln!(
                formatter,
                "  git -C {} add {}",
                self.repository.display(),
                WORKFLOW_RELATIVE_PATH
            )?;
            writeln!(formatter, "  factory validate")?;
            writeln!(formatter, "  factory run")
        } else if self
            .resources
            .iter()
            .any(|resource| resource.status == ResourceStatus::Conflict)
        {
            writeln!(
                formatter,
                "Factory did not overwrite conflicting resources."
            )
        } else {
            writeln!(
                formatter,
                "Factory initialization stopped after a failed resource; fix the error and retry."
            )
        }
    }
}

struct WorkflowPlan {
    path: PathBuf,
    action: PlannedAction,
}

struct ConfigPlan {
    path: PathBuf,
    workspace: PathBuf,
    workspace_action: PlannedAction,
    action: PlannedAction,
    candidate: Option<String>,
}

#[derive(Clone, Copy)]
struct Label {
    name: &'static str,
    description: &'static str,
    color: &'static str,
}

pub async fn initialize(options: InitOptions, github: &GitHubClient) -> Result<InitReport> {
    let repository = discover_repository(&options.repository)?;
    let workflow = plan_workflow(&repository, options.update_workflow)?;
    let config = plan_config(&options.config_path, &repository)?;
    let cancellation = CancellationToken::new();

    let (name_with_owner, missing_labels) = if options.no_labels {
        (None, Vec::new())
    } else {
        github.validate_global(&cancellation).await?;
        let name = github
            .validate_repository(&repository, &cancellation)
            .await?;
        let existing = github.labels(&repository, &cancellation).await?;
        let missing = [READY_LABEL, NEEDS_REVIEW_LABEL]
            .into_iter()
            .filter(|label| !existing.iter().any(|name| name == label.name))
            .collect::<Vec<_>>();
        (Some(name), missing)
    };

    let has_conflict = workflow.action == PlannedAction::Conflict;
    if options.check || has_conflict {
        return Ok(preflight_report(
            &options,
            repository,
            name_with_owner,
            &workflow,
            &config,
            &missing_labels,
        ));
    }

    let mut resources = Vec::new();
    if let Err(error) = apply_config(&config) {
        if config.workspace.exists() {
            resources.push(ResourceResult {
                status: applied_status(config.workspace_action),
                resource: config.workspace.display().to_string(),
                detail: Some("workspace directory".to_owned()),
            });
            resources.push(failed_resource(config.path.display().to_string(), error));
        } else {
            resources.push(failed_resource(
                config.workspace.display().to_string(),
                error,
            ));
            resources.push(skipped_resource(
                config.path.display().to_string(),
                "workspace setup failed",
            ));
        }
        resources.push(skipped_resource(
            workflow.path.display().to_string(),
            "configuration setup failed",
        ));
        resources.push(skipped_resource(
            "GitHub labels".to_owned(),
            "configuration setup failed",
        ));
        return Ok(InitReport {
            repository,
            name_with_owner,
            resources,
            check: false,
        });
    }
    resources.push(ResourceResult {
        status: applied_status(config.action),
        resource: config.path.display().to_string(),
        detail: Some("global configuration".to_owned()),
    });
    resources.push(ResourceResult {
        status: applied_status(config.workspace_action),
        resource: config.workspace.display().to_string(),
        detail: Some("workspace directory".to_owned()),
    });

    if let Err(error) = apply_workflow(&workflow) {
        resources.push(failed_resource(workflow.path.display().to_string(), error));
        resources.push(skipped_resource(
            "GitHub labels".to_owned(),
            "workflow setup failed",
        ));
        return Ok(InitReport {
            repository,
            name_with_owner,
            resources,
            check: false,
        });
    }
    resources.push(ResourceResult {
        status: applied_status(workflow.action),
        resource: workflow.path.display().to_string(),
        detail: Some("implementation workflow".to_owned()),
    });

    if options.no_labels {
        resources.push(ResourceResult {
            status: ResourceStatus::Skipped,
            resource: "GitHub labels".to_owned(),
            detail: Some("--no-labels".to_owned()),
        });
    } else {
        let labels = [READY_LABEL, NEEDS_REVIEW_LABEL];
        for (index, label) in labels.iter().enumerate() {
            if !missing_labels
                .iter()
                .any(|missing| missing.name == label.name)
            {
                resources.push(ResourceResult {
                    status: ResourceStatus::Unchanged,
                    resource: format!("GitHub label {}", label.name),
                    detail: None,
                });
                continue;
            }
            match github
                .create_label(
                    &repository,
                    label.name,
                    label.description,
                    label.color,
                    &cancellation,
                )
                .await
            {
                Ok(()) => resources.push(ResourceResult {
                    status: ResourceStatus::Created,
                    resource: format!("GitHub label {}", label.name),
                    detail: None,
                }),
                Err(error) => {
                    resources.push(failed_resource(
                        format!("GitHub label {}", label.name),
                        error,
                    ));
                    for remaining in &labels[index + 1..] {
                        resources.push(skipped_resource(
                            format!("GitHub label {}", remaining.name),
                            "earlier label setup failed",
                        ));
                    }
                    return Ok(InitReport {
                        repository,
                        name_with_owner,
                        resources,
                        check: false,
                    });
                }
            }
        }
    }

    Ok(InitReport {
        repository,
        name_with_owner,
        resources,
        check: false,
    })
}

fn failed_resource(resource: String, error: anyhow::Error) -> ResourceResult {
    ResourceResult {
        status: ResourceStatus::Failed,
        resource,
        detail: Some(format!("{error:#}")),
    }
}

fn skipped_resource(resource: String, reason: &str) -> ResourceResult {
    ResourceResult {
        status: ResourceStatus::Skipped,
        resource,
        detail: Some(reason.to_owned()),
    }
}

fn discover_repository(requested: &Path) -> Result<PathBuf> {
    let requested = requested
        .canonicalize()
        .with_context(|| format!("repository path does not exist: {}", requested.display()))?;
    if !requested.is_dir() {
        bail!(
            "repository path is not a directory: {}",
            requested.display()
        );
    }
    let root = git_output(&requested, &["rev-parse", "--show-toplevel"])
        .context("target is not a Git repository")?;
    let repository = PathBuf::from(root.trim())
        .canonicalize()
        .context("failed to resolve Git repository root")?;
    let origin = git_output(&repository, &["remote", "get-url", "origin"])
        .context("target repository has no origin remote")?;
    if !is_github_origin(origin.trim()) {
        bail!("origin is not a supported GitHub remote");
    }
    Ok(repository)
}

fn is_github_origin(origin: &str) -> bool {
    origin.starts_with("git@github.com:")
        || https_origin_has_github_host(origin)
        || ssh_origin_has_github_host(origin)
}

fn https_origin_has_github_host(origin: &str) -> bool {
    let Some(remainder) = origin.strip_prefix("https://") else {
        return false;
    };
    let Some((authority, path)) = remainder.split_once('/') else {
        return false;
    };
    if path.is_empty() {
        return false;
    }
    let host_and_port = authority
        .rsplit_once('@')
        .map_or(authority, |(_, host)| host);
    let host = match host_and_port.rsplit_once(':') {
        Some((host, port))
            if !port.is_empty() && port.chars().all(|item| item.is_ascii_digit()) =>
        {
            host
        }
        Some(_) => return false,
        None => host_and_port,
    };
    host.eq_ignore_ascii_case("github.com")
}

fn ssh_origin_has_github_host(origin: &str) -> bool {
    let Some(remainder) = origin.strip_prefix("ssh://") else {
        return false;
    };
    let Some((authority, path)) = remainder.split_once('/') else {
        return false;
    };
    if path.is_empty() {
        return false;
    }
    let Some((user, host_and_port)) = authority.rsplit_once('@') else {
        return false;
    };
    if user != "git" {
        return false;
    }
    match host_and_port.rsplit_once(':') {
        Some((host, "443")) => {
            host.eq_ignore_ascii_case("github.com") || host.eq_ignore_ascii_case("ssh.github.com")
        }
        Some((host, port)) => {
            host.eq_ignore_ascii_case("github.com")
                && !port.is_empty()
                && port.chars().all(|item| item.is_ascii_digit())
        }
        None => host_and_port.eq_ignore_ascii_case("github.com"),
    }
}

fn git_output(repository: &Path, arguments: &[&str]) -> Result<String> {
    let output = Command::new("git")
        .arg("-C")
        .arg(repository)
        .args(arguments)
        .output()
        .context("failed to start git")?;
    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        bail!(
            "git {} failed with status {}: {}",
            arguments.join(" "),
            output.status,
            stderr.trim()
        );
    }
    String::from_utf8(output.stdout).context("git output was not valid UTF-8")
}

fn plan_workflow(repository: &Path, update: bool) -> Result<WorkflowPlan> {
    let factory_directory = repository.join(".factory");
    let workflow_directory = repository.join(".factory/workflows");
    validate_optional_directory(&factory_directory)?;
    validate_optional_directory(&workflow_directory)?;

    let path = repository.join(WORKFLOW_RELATIVE_PATH);
    let action = match fs::symlink_metadata(&path) {
        Ok(metadata) => {
            if metadata.file_type().is_symlink() || !metadata.is_file() {
                bail!("workflow path must be a regular file: {}", path.display());
            }
            let current = fs::read_to_string(&path)
                .with_context(|| format!("failed to read workflow {}", path.display()))?;
            if current == DEFAULT_WORKFLOW {
                PlannedAction::Unchanged
            } else if update {
                PlannedAction::Update
            } else {
                PlannedAction::Conflict
            }
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => PlannedAction::Create,
        Err(error) => {
            return Err(error)
                .with_context(|| format!("failed to inspect workflow {}", path.display()));
        }
    };
    Ok(WorkflowPlan { path, action })
}

fn validate_optional_directory(path: &Path) -> Result<()> {
    match fs::symlink_metadata(path) {
        Ok(metadata) if metadata.file_type().is_symlink() || !metadata.is_dir() => {
            bail!("setup path must be a regular directory: {}", path.display())
        }
        Ok(_) => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error).with_context(|| format!("failed to inspect {}", path.display())),
    }
}

fn plan_config(path: &Path, repository: &Path) -> Result<ConfigPlan> {
    let path = absolute_path(path)?;
    match fs::symlink_metadata(&path) {
        Ok(metadata) => {
            if metadata.file_type().is_symlink() || !metadata.is_file() {
                bail!("config path must be a regular file: {}", path.display());
            }
            let config = Config::load_without_workspace_probe(&path)?;
            let workspace_action = if config.workspace_root.exists() {
                PlannedAction::Unchanged
            } else {
                PlannedAction::Create
            };
            if config.repositories.iter().any(|item| item == repository) {
                return Ok(ConfigPlan {
                    path,
                    workspace: config.workspace_root,
                    workspace_action,
                    action: PlannedAction::Unchanged,
                    candidate: None,
                });
            }
            let contents = fs::read_to_string(&path)
                .with_context(|| format!("failed to read config {}", path.display()))?;
            let mut document = contents
                .parse::<DocumentMut>()
                .with_context(|| format!("failed to edit config {}", path.display()))?;
            let repositories = document["repositories"]
                .as_array_mut()
                .context("config repositories must be an array")?;
            repositories.push(repository.display().to_string());
            let candidate = document.to_string();
            let config_directory = path
                .parent()
                .context("configuration path has no parent directory")?;
            Config::validate_candidate(&candidate, config_directory)
                .context("generated configuration is invalid")?;
            Ok(ConfigPlan {
                path,
                workspace: config.workspace_root,
                workspace_action,
                action: PlannedAction::Update,
                candidate: Some(candidate),
            })
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            let config_directory = path
                .parent()
                .context("configuration path has no parent directory")?;
            let workspace = config_directory.join("workspaces");
            validate_non_overlapping_workspace(&workspace, repository)?;
            Ok(ConfigPlan {
                path,
                workspace: workspace.clone(),
                workspace_action: if workspace.exists() {
                    PlannedAction::Unchanged
                } else {
                    PlannedAction::Create
                },
                action: PlannedAction::Create,
                candidate: Some(default_config(repository, &workspace)),
            })
        }
        Err(error) => {
            Err(error).with_context(|| format!("failed to inspect config {}", path.display()))
        }
    }
}

fn absolute_path(path: &Path) -> Result<PathBuf> {
    if path.is_absolute() {
        Ok(path.to_path_buf())
    } else {
        Ok(std::env::current_dir()
            .context("failed to resolve current directory")?
            .join(path))
    }
}

fn validate_non_overlapping_workspace(workspace: &Path, repository: &Path) -> Result<()> {
    if workspace == repository
        || workspace.starts_with(repository)
        || repository.starts_with(workspace)
    {
        bail!(
            "default workspace {} overlaps repository {}; use an existing custom config",
            workspace.display(),
            repository.display()
        );
    }
    Ok(())
}

fn default_config(repository: &Path, workspace: &Path) -> String {
    let mut document = DocumentMut::new();
    let mut repositories = Array::new();
    repositories.push(repository.display().to_string());
    document["repositories"] = Item::Value(Value::Array(repositories));
    document["poll_every"] = value("30s");
    document["default_runtime"] = value("codex");
    document["default_timeout"] = value("2h");
    document["maximum_timeout"] = value("8h");
    document["max_concurrent_runs"] = value(2);
    document["max_concurrent_runs_per_repository"] = value(1);
    document["workspace_root"] = value(workspace.display().to_string());
    document.to_string()
}

fn preflight_report(
    options: &InitOptions,
    repository: PathBuf,
    name_with_owner: Option<String>,
    workflow: &WorkflowPlan,
    config: &ConfigPlan,
    missing_labels: &[Label],
) -> InitReport {
    let mut resources = vec![
        planned_resource(
            config.action,
            &config.path,
            "global configuration",
            options.check,
        ),
        ResourceResult {
            status: match config.workspace_action {
                PlannedAction::Create => ResourceStatus::WouldCreate,
                PlannedAction::Update => ResourceStatus::WouldUpdate,
                PlannedAction::Unchanged => ResourceStatus::Unchanged,
                PlannedAction::Conflict => ResourceStatus::Conflict,
            },
            resource: config.workspace.display().to_string(),
            detail: Some("workspace directory".to_owned()),
        },
        planned_resource(
            workflow.action,
            &workflow.path,
            "implementation workflow",
            options.check,
        ),
    ];
    if options.no_labels {
        resources.push(ResourceResult {
            status: ResourceStatus::Skipped,
            resource: "GitHub labels".to_owned(),
            detail: Some("--no-labels".to_owned()),
        });
    } else {
        for label in [READY_LABEL, NEEDS_REVIEW_LABEL] {
            resources.push(ResourceResult {
                status: if missing_labels
                    .iter()
                    .any(|missing| missing.name == label.name)
                {
                    ResourceStatus::WouldCreate
                } else {
                    ResourceStatus::Unchanged
                },
                resource: format!("GitHub label {}", label.name),
                detail: None,
            });
        }
    }
    InitReport {
        repository,
        name_with_owner,
        resources,
        check: options.check,
    }
}

fn planned_resource(
    action: PlannedAction,
    path: &Path,
    detail: &str,
    check: bool,
) -> ResourceResult {
    let status = match action {
        PlannedAction::Create => ResourceStatus::WouldCreate,
        PlannedAction::Update => ResourceStatus::WouldUpdate,
        PlannedAction::Unchanged => ResourceStatus::Unchanged,
        PlannedAction::Conflict => ResourceStatus::Conflict,
    };
    ResourceResult {
        status,
        resource: path.display().to_string(),
        detail: Some(if action == PlannedAction::Conflict && !check {
            "customized workflow; use --update-workflow".to_owned()
        } else {
            detail.to_owned()
        }),
    }
}

fn applied_status(action: PlannedAction) -> ResourceStatus {
    match action {
        PlannedAction::Create => ResourceStatus::Created,
        PlannedAction::Update => ResourceStatus::Updated,
        PlannedAction::Unchanged => ResourceStatus::Unchanged,
        PlannedAction::Conflict => ResourceStatus::Conflict,
    }
}

fn apply_config(plan: &ConfigPlan) -> Result<()> {
    if plan.workspace_action == PlannedAction::Create {
        fs::create_dir_all(&plan.workspace).with_context(|| {
            format!(
                "failed to create workspace directory {}",
                plan.workspace.display()
            )
        })?;
    }
    if plan.action == PlannedAction::Unchanged {
        Config::load(&plan.path)?;
        return Ok(());
    }
    let parent = plan
        .path
        .parent()
        .context("configuration path has no parent directory")?;
    fs::create_dir_all(parent)
        .with_context(|| format!("failed to create config directory {}", parent.display()))?;
    let candidate = plan
        .candidate
        .as_deref()
        .context("configuration update has no candidate contents")?;
    validated_atomic_config_write(&plan.path, candidate)
}

fn validated_atomic_config_write(path: &Path, contents: &str) -> Result<()> {
    let parent = path
        .parent()
        .context("configuration path has no parent directory")?;
    let mut temporary = NamedTempFile::new_in(parent)
        .with_context(|| format!("failed to create temporary config in {}", parent.display()))?;
    temporary
        .write_all(contents.as_bytes())
        .context("failed to write temporary configuration")?;
    temporary
        .as_file_mut()
        .sync_all()
        .context("failed to sync temporary configuration")?;
    Config::load(temporary.path()).context("generated configuration is invalid")?;
    if let Ok(metadata) = fs::metadata(path) {
        temporary
            .as_file()
            .set_permissions(metadata.permissions())
            .context("failed to preserve config permissions")?;
    }
    temporary
        .persist(path)
        .map_err(|error| error.error)
        .with_context(|| format!("failed to replace config {}", path.display()))?;
    sync_parent(parent)
}

fn apply_workflow(plan: &WorkflowPlan) -> Result<()> {
    if plan.action == PlannedAction::Unchanged {
        return Ok(());
    }
    let parent = plan
        .path
        .parent()
        .context("workflow path has no parent directory")?;
    fs::create_dir_all(parent)
        .with_context(|| format!("failed to create workflow directory {}", parent.display()))?;
    atomic_write(&plan.path, DEFAULT_WORKFLOW)
}

fn atomic_write(path: &Path, contents: &str) -> Result<()> {
    let parent = path.parent().context("path has no parent directory")?;
    let mut temporary = NamedTempFile::new_in(parent)
        .with_context(|| format!("failed to create temporary file in {}", parent.display()))?;
    temporary
        .write_all(contents.as_bytes())
        .with_context(|| format!("failed to write temporary file for {}", path.display()))?;
    temporary
        .as_file_mut()
        .sync_all()
        .with_context(|| format!("failed to sync temporary file for {}", path.display()))?;
    temporary
        .persist(path)
        .map_err(|error| error.error)
        .with_context(|| format!("failed to replace {}", path.display()))?;
    sync_parent(parent)
}

fn sync_parent(parent: &Path) -> Result<()> {
    OpenOptions::new()
        .read(true)
        .open(parent)
        .and_then(|directory| directory.sync_all())
        .with_context(|| format!("failed to sync directory {}", parent.display()))
}
