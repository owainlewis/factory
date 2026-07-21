use std::env;
use std::fmt;
use std::fs;
use std::path::{Component, Path, PathBuf};
use std::process::Command;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::Deserialize;
use sha2::{Digest, Sha256};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Config {
    pub repositories: Vec<PathBuf>,
    pub poll_every: Duration,
    pub default_runtime: String,
    pub default_timeout: Duration,
    pub maximum_timeout: Duration,
    pub max_concurrent_runs: usize,
    pub max_concurrent_runs_per_repository: usize,
    pub workspace_root: PathBuf,
    pub data_directory: PathBuf,
    pub github: GitHubConfig,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GitHubConfig {
    pub trusted_approvers: Vec<String>,
    pub ready_label: String,
    pub proposed_label: String,
    pub needs_review_label: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawConfig {
    version: u8,
    poll_every: String,
    default_runtime: String,
    default_timeout: String,
    maximum_timeout: String,
    max_concurrent_runs: usize,
    github: RawGitHubConfig,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawGitHubConfig {
    trusted_approvers: Vec<String>,
    ready_label: String,
    proposed_label: String,
    needs_review_label: String,
}

impl Config {
    pub fn load(path: &Path) -> Result<Self> {
        Self::load_with_workspace_probe(path, ensure_workspace_writable, false)
    }

    pub(crate) fn load_without_workspace_probe(path: &Path) -> Result<Self> {
        Self::load_with_workspace_probe(path, |_| Ok(()), true)
    }

    fn load_with_workspace_probe<F>(
        path: &Path,
        workspace_probe: F,
        allow_missing_workspace: bool,
    ) -> Result<Self>
    where
        F: FnOnce(&Path) -> Result<()>,
    {
        let current_dir = env::current_dir().context("failed to resolve current directory")?;
        let path = expand_path(path, &current_dir)?;
        let contents = fs::read_to_string(&path)
            .with_context(|| format!("failed to read config {}", path.display()))?;
        let raw: RawConfig = toml::from_str(&contents)
            .with_context(|| format!("failed to parse config {}", path.display()))?;
        let config_dir = path
            .parent()
            .context("configuration path has no parent directory")?;
        if config_dir.file_name().and_then(|name| name.to_str()) != Some(".factory") {
            bail!(
                "Factory v1 requires repository-local configuration at <git-root>/.factory/config.toml; legacy global configuration is not executable"
            );
        }
        let repository = config_dir
            .parent()
            .context(".factory configuration has no repository parent")?;
        let expected = repository_config_path(repository);
        if path != expected {
            bail!(
                "Factory repository configuration must be {}; got {}",
                expected.display(),
                path.display()
            );
        }

        Self::resolve_with_workspace_probe(
            raw,
            repository,
            workspace_probe,
            allow_missing_workspace,
        )
        .with_context(|| format!("invalid Factory configuration in {}", path.display()))
    }

    pub(crate) fn validate_candidate(contents: &str, repository: &Path) -> Result<Self> {
        let raw: RawConfig =
            toml::from_str(contents).context("failed to parse candidate config")?;
        Self::resolve_with_workspace_probe(raw, repository, |_| Ok(()), true)
            .context("invalid candidate Factory configuration")
    }

    #[cfg(test)]
    fn resolve(raw: RawConfig, repository: &Path) -> Result<Self> {
        Self::resolve_with_workspace_probe(raw, repository, |_| Ok(()), true)
    }

    fn resolve_with_workspace_probe<F>(
        raw: RawConfig,
        repository: &Path,
        workspace_probe: F,
        allow_missing_workspace: bool,
    ) -> Result<Self>
    where
        F: FnOnce(&Path) -> Result<()>,
    {
        if raw.version != 1 {
            bail!("version must be 1");
        }
        if raw.max_concurrent_runs == 0 {
            bail!("max_concurrent_runs must be greater than zero");
        }
        let github = resolve_github(raw.github)?;
        let default_runtime = raw.default_runtime.trim();
        if default_runtime.is_empty() {
            bail!("default_runtime must not be empty");
        }

        let poll_every = parse_positive_duration("poll_every", &raw.poll_every)?;
        let default_timeout = parse_positive_duration("default_timeout", &raw.default_timeout)?;
        let maximum_timeout = parse_positive_duration("maximum_timeout", &raw.maximum_timeout)?;
        if default_timeout > maximum_timeout {
            bail!("default_timeout must not exceed maximum_timeout");
        }

        let repository = canonical_directory("repository", repository, repository)?;
        let data_directory = repository_data_directory(&repository)?;
        let workspace_candidate = data_directory.join("worktrees");
        let workspace_root = if allow_missing_workspace {
            canonical_directory_or_missing("workspace_root", &workspace_candidate, &repository)?
        } else {
            canonical_directory("workspace_root", &workspace_candidate, &repository)?
        };
        let home = canonical_home_dir()?;
        validate_workspace(
            &workspace_root,
            std::slice::from_ref(&repository),
            home.as_deref(),
        )?;
        workspace_probe(&workspace_root)?;

        Ok(Self {
            repositories: vec![repository],
            poll_every,
            default_runtime: default_runtime.to_owned(),
            default_timeout,
            maximum_timeout,
            max_concurrent_runs: raw.max_concurrent_runs,
            max_concurrent_runs_per_repository: raw.max_concurrent_runs,
            workspace_root,
            data_directory,
            github,
        })
    }
}

impl fmt::Display for Config {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        writeln!(formatter, "Configuration is valid.")?;
        writeln!(formatter, "repository: {}", self.repositories[0].display())?;
        writeln!(
            formatter,
            "poll_every: {}",
            humantime::format_duration(self.poll_every)
        )?;
        writeln!(formatter, "default_runtime: {}", self.default_runtime)?;
        writeln!(
            formatter,
            "default_timeout: {}",
            humantime::format_duration(self.default_timeout)
        )?;
        writeln!(
            formatter,
            "maximum_timeout: {}",
            humantime::format_duration(self.maximum_timeout)
        )?;
        writeln!(
            formatter,
            "max_concurrent_runs: {}",
            self.max_concurrent_runs
        )?;
        writeln!(
            formatter,
            "state_directory: {}",
            self.data_directory.display()
        )?;
        writeln!(formatter, "worktrees: {}", self.workspace_root.display())
    }
}

fn resolve_github(raw: RawGitHubConfig) -> Result<GitHubConfig> {
    if raw.trusted_approvers.is_empty() {
        bail!("github.trusted_approvers must contain at least one login");
    }
    let mut trusted_approvers = Vec::with_capacity(raw.trusted_approvers.len());
    for login in raw.trusted_approvers {
        let login = login.trim();
        if login.is_empty()
            || !login
                .chars()
                .all(|character| character.is_ascii_alphanumeric() || character == '-')
        {
            bail!("github.trusted_approvers contains invalid login {login:?}");
        }
        if !trusted_approvers
            .iter()
            .any(|existing: &String| existing.eq_ignore_ascii_case(login))
        {
            trusted_approvers.push(login.to_owned());
        }
    }
    let label = |name: &str, value: String| -> Result<String> {
        let value = value.trim();
        if value.is_empty() {
            bail!("github.{name} must not be empty");
        }
        Ok(value.to_owned())
    };
    Ok(GitHubConfig {
        trusted_approvers,
        ready_label: label("ready_label", raw.ready_label)?,
        proposed_label: label("proposed_label", raw.proposed_label)?,
        needs_review_label: label("needs_review_label", raw.needs_review_label)?,
    })
}

pub fn repository_config_path(repository: &Path) -> PathBuf {
    repository.join(".factory/config.toml")
}

pub fn repository_data_directory(repository: &Path) -> Result<PathBuf> {
    let repository = repository
        .canonicalize()
        .with_context(|| format!("failed to resolve repository {}", repository.display()))?;
    ensure_primary_checkout(&repository)?;
    let identity = repository_remote_identity(&repository)?;
    let mut hasher = Sha256::new();
    hasher.update(identity.as_bytes());
    hasher.update(b"\0");
    hasher.update(repository.as_os_str().as_encoded_bytes());
    let digest = format!("{:x}", hasher.finalize());
    let base = env::var_os("FACTORY_DATA_HOME")
        .map(PathBuf::from)
        .or_else(|| dirs::data_local_dir().map(|path| path.join("factory")))
        .context("could not determine Factory data directory")?;
    let base = if base.is_absolute() {
        base
    } else {
        env::current_dir()
            .context("failed to resolve current directory")?
            .join(base)
    };
    Ok(base.join(&digest[..20]))
}

pub fn repository_remote_identity(repository: &Path) -> Result<String> {
    let origin = git_output(repository, &["remote", "get-url", "origin"])
        .context("repository has no readable origin remote")?;
    canonical_github_identity(origin.trim()).context("origin is not a supported GitHub remote")
}

fn canonical_github_identity(origin: &str) -> Result<String> {
    let path = if let Some(path) = origin.strip_prefix("git@github.com:") {
        path
    } else if let Some(remainder) = origin.strip_prefix("https://") {
        let (authority, path) = remainder
            .split_once('/')
            .context("GitHub HTTPS origin has no repository path")?;
        let host = authority
            .rsplit_once('@')
            .map_or(authority, |(_, host)| host);
        let host = host.split_once(':').map_or(host, |(host, _)| host);
        if !host.eq_ignore_ascii_case("github.com") {
            bail!("GitHub HTTPS origin has an unsupported host");
        }
        path
    } else if let Some(remainder) = origin.strip_prefix("ssh://git@") {
        let (authority, path) = remainder
            .split_once('/')
            .context("GitHub SSH origin has no repository path")?;
        let host = authority
            .split_once(':')
            .map_or(authority, |(host, _)| host);
        if !host.eq_ignore_ascii_case("github.com") && !host.eq_ignore_ascii_case("ssh.github.com")
        {
            bail!("GitHub SSH origin has an unsupported host");
        }
        path
    } else {
        bail!("unsupported GitHub origin syntax");
    };
    let path = path.trim_end_matches('/');
    let path = path.strip_suffix(".git").unwrap_or(path);
    let mut segments = path.split('/');
    let owner = segments
        .next()
        .filter(|value| !value.is_empty())
        .context("GitHub origin has no owner")?;
    let repository = segments
        .next()
        .filter(|value| !value.is_empty())
        .context("GitHub origin has no repository")?;
    if segments.next().is_some() {
        bail!("GitHub origin has an invalid repository path");
    }
    Ok(format!(
        "{}/{}",
        owner.to_ascii_lowercase(),
        repository.to_ascii_lowercase()
    ))
}

fn ensure_primary_checkout(repository: &Path) -> Result<()> {
    let git_dir = git_output(
        repository,
        &["rev-parse", "--path-format=absolute", "--git-dir"],
    )?;
    let common_dir = git_output(
        repository,
        &["rev-parse", "--path-format=absolute", "--git-common-dir"],
    )?;
    let git_dir = PathBuf::from(git_dir.trim())
        .canonicalize()
        .context("failed to resolve Git directory")?;
    let common_dir = PathBuf::from(common_dir.trim())
        .canonicalize()
        .context("failed to resolve common Git directory")?;
    if git_dir != common_dir {
        bail!("Factory must run from the primary checkout, not a linked Git worktree");
    }
    Ok(())
}

fn git_output(repository: &Path, arguments: &[&str]) -> Result<String> {
    let output = Command::new("git")
        .arg("-C")
        .arg(repository)
        .args(arguments)
        .output()
        .context("failed to start git")?;
    if !output.status.success() {
        bail!(
            "git {} failed: {}",
            arguments.join(" "),
            String::from_utf8_lossy(&output.stderr).trim()
        );
    }
    String::from_utf8(output.stdout).context("git output was not valid UTF-8")
}

fn parse_positive_duration(name: &str, value: &str) -> Result<Duration> {
    let duration = humantime::parse_duration(value)
        .with_context(|| format!("{name} has invalid duration {value:?}"))?;
    if duration.is_zero() {
        bail!("{name} must be greater than zero");
    }
    Ok(duration)
}

fn canonical_directory(name: &str, path: &Path, base: &Path) -> Result<PathBuf> {
    let expanded = expand_path(path, base)?;
    let canonical = expanded
        .canonicalize()
        .with_context(|| format!("{name} path does not exist: {}", expanded.display()))?;
    if !canonical.is_dir() {
        bail!("{name} path is not a directory: {}", canonical.display());
    }
    Ok(canonical)
}

fn canonical_directory_or_missing(name: &str, path: &Path, base: &Path) -> Result<PathBuf> {
    let expanded = expand_path(path, base)?;
    let mut ancestor = expanded.as_path();
    let mut missing = Vec::new();

    loop {
        match fs::symlink_metadata(ancestor) {
            Ok(_) => {
                let mut resolved = ancestor.canonicalize().with_context(|| {
                    format!("failed to resolve {name} path: {}", ancestor.display())
                })?;
                if !resolved.is_dir() {
                    bail!("{name} ancestor is not a directory: {}", resolved.display());
                }
                for component in missing.iter().rev() {
                    resolved.push(component);
                }
                return Ok(resolved);
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                if matches!(
                    ancestor.components().next_back(),
                    Some(Component::ParentDir)
                ) {
                    bail!(
                        "{name} path must not contain parent traversal in a missing suffix: {}",
                        expanded.display()
                    );
                }
                let component = ancestor.file_name().with_context(|| {
                    format!(
                        "{name} path has no existing ancestor: {}",
                        expanded.display()
                    )
                })?;
                missing.push(component.to_os_string());
                ancestor = ancestor.parent().with_context(|| {
                    format!(
                        "{name} path has no existing ancestor: {}",
                        expanded.display()
                    )
                })?;
            }
            Err(error) => {
                return Err(error).with_context(|| {
                    format!("failed to inspect {name} path: {}", ancestor.display())
                });
            }
        }
    }
}

fn expand_path(path: &Path, base: &Path) -> Result<PathBuf> {
    let text = path
        .to_str()
        .with_context(|| format!("path is not valid UTF-8: {}", path.display()))?;
    let expanded = expand_environment(text)?;
    let expanded = if expanded == "~" {
        home_dir()?
    } else if let Some(rest) = expanded.strip_prefix("~/") {
        home_dir()?.join(rest)
    } else {
        PathBuf::from(expanded)
    };

    if expanded.is_absolute() {
        Ok(expanded)
    } else {
        Ok(base.join(expanded))
    }
}

fn expand_environment(value: &str) -> Result<String> {
    let mut result = String::with_capacity(value.len());
    let mut chars = value.chars().peekable();

    while let Some(character) = chars.next() {
        if character != '$' {
            result.push(character);
            continue;
        }

        let mut name = String::new();
        while let Some(next) = chars.peek() {
            if next.is_ascii_alphanumeric() || *next == '_' {
                name.push(*next);
                chars.next();
            } else {
                break;
            }
        }
        if name.is_empty() {
            result.push('$');
            continue;
        }
        let replacement = env::var(&name)
            .with_context(|| format!("path references unset environment variable ${name}"))?;
        result.push_str(&replacement);
    }

    Ok(result)
}

fn home_dir() -> Result<PathBuf> {
    dirs::home_dir().context("could not determine the home directory for ~ expansion")
}

fn canonical_home_dir() -> Result<Option<PathBuf>> {
    dirs::home_dir()
        .map(|home| {
            home.canonicalize()
                .with_context(|| format!("failed to resolve home directory {}", home.display()))
        })
        .transpose()
}

fn validate_workspace(
    workspace: &Path,
    repositories: &[PathBuf],
    canonical_home: Option<&Path>,
) -> Result<()> {
    let is_root = workspace
        .components()
        .filter(|component| *component != Component::RootDir)
        .count()
        == 0;
    if is_root {
        bail!("workspace_root must not be the filesystem root");
    }
    if canonical_home.is_some_and(|home| workspace == home) {
        bail!("workspace_root must not be the home directory");
    }
    for repository in repositories {
        if workspace == repository
            || workspace.starts_with(repository)
            || repository.starts_with(workspace)
        {
            bail!(
                "workspace_root {} must not overlap configured repository {}",
                workspace.display(),
                repository.display()
            );
        }
    }
    Ok(())
}

fn ensure_workspace_writable(workspace: &Path) -> Result<()> {
    tempfile::tempfile_in(workspace)
        .with_context(|| format!("workspace_root is not writable: {}", workspace.display()))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn raw(repository: &Path, _workspace: &Path) -> RawConfig {
        fs::create_dir_all(repository).unwrap();
        if !repository.join(".git").exists() {
            assert!(
                Command::new("git")
                    .args(["init", "--quiet"])
                    .current_dir(repository)
                    .status()
                    .unwrap()
                    .success()
            );
            assert!(
                Command::new("git")
                    .args([
                        "remote",
                        "add",
                        "origin",
                        "git@github.com:example/repository.git"
                    ])
                    .current_dir(repository)
                    .status()
                    .unwrap()
                    .success()
            );
        }
        RawConfig {
            version: 1,
            poll_every: "30s".into(),
            default_runtime: "codex".into(),
            default_timeout: "2h".into(),
            maximum_timeout: "8h".into(),
            max_concurrent_runs: 2,
            github: RawGitHubConfig {
                trusted_approvers: vec!["owainlewis".into()],
                ready_label: "factory:ready".into(),
                proposed_label: "factory:proposed".into(),
                needs_review_label: "factory:needs-review".into(),
            },
        }
    }

    #[test]
    fn resolves_valid_configuration() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let config = Config::resolve(raw(&repository, &workspace), &repository).unwrap();

        assert_eq!(
            config.repositories,
            vec![repository.canonicalize().unwrap()]
        );
        assert!(config.workspace_root.ends_with("worktrees"));
        assert_eq!(config.poll_every, Duration::from_secs(30));
    }

    #[test]
    fn normalizes_runtime_name() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let mut input = raw(&repository, &workspace);
        input.default_runtime = "  codex  ".into();

        let config = Config::resolve(input, &repository).unwrap();

        assert_eq!(config.default_runtime, "codex");
    }

    #[test]
    fn rejects_invalid_duration() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let mut config = raw(&repository, &workspace);
        config.poll_every = "eventually".into();

        let error = Config::resolve(config, temp.path()).unwrap_err();

        assert!(
            error
                .to_string()
                .contains("poll_every has invalid duration")
        );
    }

    #[test]
    fn rejects_zero_concurrency() {
        let temp = tempfile::tempdir().unwrap();
        let mut config = raw(temp.path(), temp.path());
        config.max_concurrent_runs = 0;

        let error = Config::resolve(config, temp.path()).unwrap_err();

        assert!(
            error
                .to_string()
                .contains("max_concurrent_runs must be greater than zero")
        );
    }

    #[test]
    fn rejects_unsupported_version() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let mut config = raw(&repository, &workspace);
        config.version = 2;
        assert!(
            Config::resolve(config, temp.path())
                .unwrap_err()
                .to_string()
                .contains("version must be 1")
        );
    }

    #[test]
    fn rejects_missing_repository() {
        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&workspace).unwrap();
        let config = RawConfig {
            version: 1,
            poll_every: "30s".into(),
            default_runtime: "codex".into(),
            default_timeout: "2h".into(),
            maximum_timeout: "8h".into(),
            max_concurrent_runs: 1,
            github: RawGitHubConfig {
                trusted_approvers: vec!["owainlewis".into()],
                ready_label: "factory:ready".into(),
                proposed_label: "factory:proposed".into(),
                needs_review_label: "factory:needs-review".into(),
            },
        };

        let error = Config::resolve(config, &temp.path().join("missing")).unwrap_err();

        assert!(error.to_string().contains("repository path does not exist"));
    }

    #[test]
    fn derives_workspace_outside_repository() {
        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("worktrees");
        let config = Config::resolve(raw(temp.path(), &workspace), temp.path()).unwrap();
        assert!(!config.workspace_root.starts_with(temp.path()));
    }

    #[cfg(unix)]
    #[test]
    fn rejects_home_directory_through_symlink_alias() {
        use std::os::unix::fs::symlink;

        let temp = tempfile::tempdir().unwrap();
        let home = temp.path().join("home");
        let alias = temp.path().join("home-alias");
        fs::create_dir(&home).unwrap();
        symlink(&home, &alias).unwrap();

        let canonical_home = home.canonicalize().unwrap();
        let canonical_workspace = alias.canonicalize().unwrap();
        let error =
            validate_workspace(&canonical_workspace, &[], Some(&canonical_home)).unwrap_err();

        assert!(error.to_string().contains("must not be the home directory"));
    }

    #[test]
    fn reports_unwritable_workspace() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let error = Config::resolve_with_workspace_probe(
            raw(&repository, &workspace),
            &repository,
            |path| bail!("workspace_root is not writable: {}", path.display()),
            true,
        )
        .unwrap_err();

        assert!(
            error.to_string().contains("workspace_root is not writable"),
            "{error:#}"
        );
        assert!(error.to_string().contains("worktrees"));
    }

    #[test]
    fn repository_config_requires_version_and_rejects_legacy_registry_keys() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let valid = raw(&repository, &workspace);
        let valid = toml::to_string(&serde_json::json!({
            "version": valid.version,
            "poll_every": valid.poll_every,
            "default_runtime": valid.default_runtime,
            "default_timeout": valid.default_timeout,
            "maximum_timeout": valid.maximum_timeout,
            "max_concurrent_runs": valid.max_concurrent_runs,
            "github": {
                "trusted_approvers": valid.github.trusted_approvers,
                "ready_label": valid.github.ready_label,
                "proposed_label": valid.github.proposed_label,
                "needs_review_label": valid.github.needs_review_label,
            },
        }))
        .unwrap();
        let missing_version = valid
            .lines()
            .filter(|line| !line.starts_with("version ="))
            .collect::<Vec<_>>()
            .join("\n");
        let error = Config::validate_candidate(&missing_version, &repository).unwrap_err();
        assert!(format!("{error:#}").contains("missing field `version`"));
        for legacy in [
            "repositories = [\"/tmp/repo\"]",
            "workspace_root = \"/tmp/worktrees\"",
            "max_concurrent_runs_per_repository = 1",
        ] {
            let error = Config::validate_candidate(&format!("{valid}\n{legacy}\n"), &repository)
                .unwrap_err();
            assert!(format!("{error:#}").contains("unknown field"), "{error:#}");
        }
    }

    #[test]
    fn state_identity_normalizes_remote_syntax_but_distinguishes_clones() {
        let temp = tempfile::tempdir().unwrap();
        let first = temp.path().join("first");
        let second = temp.path().join("second");
        raw(&first, temp.path());
        raw(&second, temp.path());
        let ssh = repository_data_directory(&first).unwrap();
        assert!(
            Command::new("git")
                .args([
                    "remote",
                    "set-url",
                    "origin",
                    "https://github.com/Example/Repository.git",
                ])
                .current_dir(&first)
                .status()
                .unwrap()
                .success()
        );
        assert_eq!(ssh, repository_data_directory(&first).unwrap());
        assert_ne!(ssh, repository_data_directory(&second).unwrap());
    }

    #[test]
    fn linked_git_worktree_is_rejected() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        raw(&repository, temp.path());
        fs::write(repository.join("README.md"), "test\n").unwrap();
        assert!(
            Command::new("git")
                .args(["add", "."])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        assert!(
            Command::new("git")
                .args([
                    "-c",
                    "user.name=Test",
                    "-c",
                    "user.email=test@example.com",
                    "commit",
                    "-m",
                    "test"
                ])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        let linked = temp.path().join("linked");
        assert!(
            Command::new("git")
                .args(["worktree", "add", "-b", "linked", linked.to_str().unwrap()])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        let error = repository_data_directory(&linked).unwrap_err();
        assert!(error.to_string().contains("primary checkout"), "{error:#}");
    }
}
