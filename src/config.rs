use std::env;
use std::fmt;
use std::fs;
use std::path::{Component, Path, PathBuf};
use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::Deserialize;

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
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawConfig {
    repositories: Vec<String>,
    poll_every: String,
    default_runtime: String,
    default_timeout: String,
    maximum_timeout: String,
    max_concurrent_runs: usize,
    #[serde(default = "default_repository_concurrency")]
    max_concurrent_runs_per_repository: usize,
    workspace_root: String,
}

impl Config {
    pub fn load(path: &Path) -> Result<Self> {
        Self::load_with_workspace_probe(path, ensure_workspace_writable)
    }

    pub(crate) fn load_without_workspace_probe(path: &Path) -> Result<Self> {
        Self::load_with_workspace_probe(path, |_| Ok(()))
    }

    fn load_with_workspace_probe<F>(path: &Path, workspace_probe: F) -> Result<Self>
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

        Self::resolve_with_workspace_probe(raw, config_dir, workspace_probe)
            .with_context(|| format!("invalid Factory configuration in {}", path.display()))
    }

    pub(crate) fn validate_candidate(contents: &str, config_dir: &Path) -> Result<Self> {
        let raw: RawConfig =
            toml::from_str(contents).context("failed to parse candidate config")?;
        Self::resolve_with_workspace_probe(raw, config_dir, |_| Ok(()))
            .context("invalid candidate Factory configuration")
    }

    #[cfg(test)]
    fn resolve(raw: RawConfig, config_dir: &Path) -> Result<Self> {
        Self::resolve_with_workspace_probe(raw, config_dir, ensure_workspace_writable)
    }

    fn resolve_with_workspace_probe<F>(
        raw: RawConfig,
        config_dir: &Path,
        workspace_probe: F,
    ) -> Result<Self>
    where
        F: FnOnce(&Path) -> Result<()>,
    {
        if raw.repositories.is_empty() {
            bail!("repositories must contain at least one path");
        }
        if raw.max_concurrent_runs == 0 {
            bail!("max_concurrent_runs must be greater than zero");
        }
        if raw.max_concurrent_runs_per_repository == 0 {
            bail!("max_concurrent_runs_per_repository must be greater than zero");
        }
        if raw.max_concurrent_runs_per_repository > raw.max_concurrent_runs {
            bail!("max_concurrent_runs_per_repository must not exceed max_concurrent_runs");
        }
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

        let repositories = raw
            .repositories
            .iter()
            .map(|path| canonical_directory("repository", Path::new(path), config_dir))
            .collect::<Result<Vec<_>>>()?;
        let workspace_root =
            canonical_directory("workspace_root", Path::new(&raw.workspace_root), config_dir)?;
        let home = canonical_home_dir()?;
        validate_workspace(&workspace_root, &repositories, home.as_deref())?;
        workspace_probe(&workspace_root)?;

        Ok(Self {
            repositories,
            poll_every,
            default_runtime: default_runtime.to_owned(),
            default_timeout,
            maximum_timeout,
            max_concurrent_runs: raw.max_concurrent_runs,
            max_concurrent_runs_per_repository: raw.max_concurrent_runs_per_repository,
            workspace_root,
        })
    }
}

impl fmt::Display for Config {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        writeln!(formatter, "Configuration is valid.")?;
        writeln!(formatter, "repositories:")?;
        for repository in &self.repositories {
            writeln!(formatter, "  - {}", repository.display())?;
        }
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
            "max_concurrent_runs_per_repository: {}",
            self.max_concurrent_runs_per_repository
        )?;
        writeln!(
            formatter,
            "workspace_root: {}",
            self.workspace_root.display()
        )
    }
}

fn default_repository_concurrency() -> usize {
    1
}

pub fn default_config_path() -> PathBuf {
    dirs::home_dir()
        .map(|home| home.join(".factory/config.toml"))
        .unwrap_or_else(|| PathBuf::from(".factory/config.toml"))
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

    fn raw(repository: &Path, workspace: &Path) -> RawConfig {
        RawConfig {
            repositories: vec![repository.display().to_string()],
            poll_every: "30s".into(),
            default_runtime: "codex".into(),
            default_timeout: "2h".into(),
            maximum_timeout: "8h".into(),
            max_concurrent_runs: 2,
            max_concurrent_runs_per_repository: 1,
            workspace_root: workspace.display().to_string(),
        }
    }

    #[test]
    fn resolves_valid_configuration() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&repository).unwrap();
        fs::create_dir(&workspace).unwrap();

        let config = Config::resolve(raw(&repository, &workspace), temp.path()).unwrap();

        assert_eq!(
            config.repositories,
            vec![repository.canonicalize().unwrap()]
        );
        assert_eq!(config.workspace_root, workspace.canonicalize().unwrap());
        assert_eq!(config.poll_every, Duration::from_secs(30));
    }

    #[test]
    fn normalizes_runtime_name() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&repository).unwrap();
        fs::create_dir(&workspace).unwrap();
        let mut input = raw(&repository, &workspace);
        input.default_runtime = "  codex  ".into();

        let config = Config::resolve(input, temp.path()).unwrap();

        assert_eq!(config.default_runtime, "codex");
    }

    #[test]
    fn rejects_invalid_duration() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&repository).unwrap();
        fs::create_dir(&workspace).unwrap();
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
    fn rejects_invalid_repository_concurrency() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&repository).unwrap();
        fs::create_dir(&workspace).unwrap();
        let mut config = raw(&repository, &workspace);
        config.max_concurrent_runs_per_repository = 0;
        assert!(
            Config::resolve(config, temp.path())
                .unwrap_err()
                .to_string()
                .contains("must be greater than zero")
        );

        let mut config = raw(&repository, &workspace);
        config.max_concurrent_runs_per_repository = 3;
        assert!(
            Config::resolve(config, temp.path())
                .unwrap_err()
                .to_string()
                .contains("must not exceed")
        );
    }

    #[test]
    fn rejects_missing_repository() {
        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&workspace).unwrap();
        let config = raw(&temp.path().join("missing"), &workspace);

        let error = Config::resolve(config, temp.path()).unwrap_err();

        assert!(error.to_string().contains("repository path does not exist"));
    }

    #[test]
    fn rejects_workspace_inside_repository() {
        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&workspace).unwrap();

        let error = Config::resolve(raw(temp.path(), &workspace), temp.path()).unwrap_err();

        assert!(error.to_string().contains("must not overlap"));
    }

    #[test]
    fn rejects_repository_inside_workspace() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        fs::create_dir(&repository).unwrap();

        let error = Config::resolve(raw(&repository, temp.path()), temp.path()).unwrap_err();

        assert!(error.to_string().contains("must not overlap"));
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
        fs::create_dir(&repository).unwrap();
        fs::create_dir(&workspace).unwrap();

        let error = Config::resolve_with_workspace_probe(
            raw(&repository, &workspace),
            temp.path(),
            |path| bail!("workspace_root is not writable: {}", path.display()),
        )
        .unwrap_err();

        assert!(error.to_string().contains("workspace_root is not writable"));
        assert!(error.to_string().contains(workspace.to_str().unwrap()));
    }
}
