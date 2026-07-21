use std::fmt;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::Command;

use anyhow::{Context, Result, bail};
use tempfile::NamedTempFile;
use toml_edit::{DocumentMut, value};

use crate::config::Config;

#[derive(Debug, Clone)]
pub struct InitOptions {
    pub repository: PathBuf,
    pub config_path: PathBuf,
    pub check: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum PlannedAction {
    Create,
    Unchanged,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ResourceStatus {
    Created,
    Unchanged,
    WouldCreate,
    Failed,
    Skipped,
}

impl ResourceStatus {
    fn label(self) -> &'static str {
        match self {
            Self::Created => "created",
            Self::Unchanged => "unchanged",
            Self::WouldCreate => "would create",
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
    resources: Vec<ResourceResult>,
    check: bool,
}

impl InitReport {
    pub fn exit_code(&self) -> u8 {
        u8::from(self.resources.iter().any(|resource| {
            matches!(
                resource.status,
                ResourceStatus::WouldCreate | ResourceStatus::Failed
            )
        }))
    }
}

impl fmt::Display for InitReport {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        writeln!(
            formatter,
            "Factory initialization for {}",
            self.repository.display()
        )?;
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
            writeln!(formatter, "  factory workflow create <workflow-id> --help")?;
            writeln!(formatter, "  factory validate")?;
            writeln!(formatter, "  factory daemon")
        } else {
            writeln!(
                formatter,
                "Factory initialization stopped after a failed resource; fix the error and retry."
            )
        }
    }
}

struct DirectoryPlan {
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

pub fn initialize(options: InitOptions) -> Result<InitReport> {
    let repository = discover_repository(&options.repository)?;
    let workflows = plan_workflow_directory(&repository)?;
    let config = plan_config(&options.config_path, &repository)?;

    if options.check {
        return Ok(InitReport {
            repository,
            resources: vec![
                planned_resource(config.action, &config.path, "repository configuration"),
                planned_resource(
                    config.workspace_action,
                    &config.workspace,
                    "workspace directory",
                ),
                planned_resource(workflows.action, &workflows.path, "workflow directory"),
            ],
            check: true,
        });
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
            workflows.path.display().to_string(),
            "configuration setup failed",
        ));
        return Ok(InitReport {
            repository,
            resources,
            check: false,
        });
    }
    resources.push(ResourceResult {
        status: applied_status(config.action),
        resource: config.path.display().to_string(),
        detail: Some("repository configuration".to_owned()),
    });
    resources.push(ResourceResult {
        status: applied_status(config.workspace_action),
        resource: config.workspace.display().to_string(),
        detail: Some("workspace directory".to_owned()),
    });

    if let Err(error) = apply_directory(&workflows) {
        resources.push(failed_resource(workflows.path.display().to_string(), error));
        return Ok(InitReport {
            repository,
            resources,
            check: false,
        });
    }
    resources.push(ResourceResult {
        status: applied_status(workflows.action),
        resource: workflows.path.display().to_string(),
        detail: Some("workflow directory".to_owned()),
    });

    Ok(InitReport {
        repository,
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

pub fn discover_repository(requested: &Path) -> Result<PathBuf> {
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
    let origin = git_output(&repository, &["config", "--get", "remote.origin.url"])
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

fn plan_workflow_directory(repository: &Path) -> Result<DirectoryPlan> {
    let factory_directory = repository.join(".factory");
    validate_optional_directory(&factory_directory)?;
    let path = factory_directory.join("workflows");
    validate_optional_directory(&path)?;
    Ok(DirectoryPlan {
        action: if path.exists() {
            PlannedAction::Unchanged
        } else {
            PlannedAction::Create
        },
        path,
    })
}

pub(crate) fn validate_optional_directory(path: &Path) -> Result<()> {
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
    let expected = repository.join(".factory/config.toml");
    if path != expected {
        bail!(
            "repository configuration must be {}; got {}",
            expected.display(),
            path.display()
        );
    }
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
            Ok(ConfigPlan {
                path,
                workspace: config.workspace_root,
                workspace_action,
                action: PlannedAction::Unchanged,
                candidate: None,
            })
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            let candidate = default_config();
            let validated = Config::validate_candidate(&candidate, repository)
                .context("generated configuration is invalid")?;
            let workspace = validated.workspace_root;
            Ok(ConfigPlan {
                path,
                workspace: workspace.clone(),
                workspace_action: if workspace.exists() {
                    PlannedAction::Unchanged
                } else {
                    PlannedAction::Create
                },
                action: PlannedAction::Create,
                candidate: Some(candidate),
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

fn default_config() -> String {
    let mut document = DocumentMut::new();
    document["version"] = value(1);
    document["poll_every"] = value("30s");
    document["default_runtime"] = value("codex");
    document["default_timeout"] = value("2h");
    document["maximum_timeout"] = value("8h");
    document["max_concurrent_runs"] = value(1);
    document["github"]["trusted_approvers"] =
        toml_edit::value(toml_edit::Array::from_iter(["owainlewis"]));
    document["github"]["ready_label"] = value("factory:ready");
    document["github"]["proposed_label"] = value("factory:proposed");
    document["github"]["needs_review_label"] = value("factory:needs-review");
    document.to_string()
}

fn planned_resource(action: PlannedAction, path: &Path, detail: &str) -> ResourceResult {
    let status = match action {
        PlannedAction::Create => ResourceStatus::WouldCreate,
        PlannedAction::Unchanged => ResourceStatus::Unchanged,
    };
    ResourceResult {
        status,
        resource: path.display().to_string(),
        detail: Some(detail.to_owned()),
    }
}

fn applied_status(action: PlannedAction) -> ResourceStatus {
    match action {
        PlannedAction::Create => ResourceStatus::Created,
        PlannedAction::Unchanged => ResourceStatus::Unchanged,
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
    let repository = parent
        .parent()
        .context("repository configuration has no repository parent")?;
    let written =
        fs::read_to_string(temporary.path()).context("failed to read temporary configuration")?;
    Config::validate_candidate(&written, repository)
        .context("generated configuration is invalid")?;
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

fn apply_directory(plan: &DirectoryPlan) -> Result<()> {
    if plan.action == PlannedAction::Create {
        fs::create_dir_all(&plan.path).with_context(|| {
            format!(
                "failed to create workflow directory {}",
                plan.path.display()
            )
        })?;
        sync_parent(
            plan.path
                .parent()
                .context("workflow directory has no parent")?,
        )?;
    }
    Ok(())
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
