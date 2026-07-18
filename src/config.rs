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
    workspace_root: String,
}

impl Config {
    pub fn load(path: &Path) -> Result<Self> {
        let path = expand_path(path)?;
        let contents = fs::read_to_string(&path)
            .with_context(|| format!("failed to read config {}", path.display()))?;
        let raw: RawConfig = toml::from_str(&contents)
            .with_context(|| format!("failed to parse config {}", path.display()))?;

        Self::resolve(raw)
            .with_context(|| format!("invalid Factory configuration in {}", path.display()))
    }

    fn resolve(raw: RawConfig) -> Result<Self> {
        if raw.repositories.is_empty() {
            bail!("repositories must contain at least one path");
        }
        if raw.max_concurrent_runs == 0 {
            bail!("max_concurrent_runs must be greater than zero");
        }
        if raw.default_runtime.trim().is_empty() {
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
            .map(|path| canonical_directory("repository", Path::new(path)))
            .collect::<Result<Vec<_>>>()?;
        let workspace_root = canonical_directory("workspace_root", Path::new(&raw.workspace_root))?;
        validate_workspace(&workspace_root, &repositories)?;

        Ok(Self {
            repositories,
            poll_every,
            default_runtime: raw.default_runtime,
            default_timeout,
            maximum_timeout,
            max_concurrent_runs: raw.max_concurrent_runs,
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
            "workspace_root: {}",
            self.workspace_root.display()
        )
    }
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

fn canonical_directory(name: &str, path: &Path) -> Result<PathBuf> {
    let expanded = expand_path(path)?;
    let canonical = expanded
        .canonicalize()
        .with_context(|| format!("{name} path does not exist: {}", expanded.display()))?;
    if !canonical.is_dir() {
        bail!("{name} path is not a directory: {}", canonical.display());
    }
    Ok(canonical)
}

fn expand_path(path: &Path) -> Result<PathBuf> {
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
        Ok(env::current_dir()
            .context("failed to resolve current directory")?
            .join(expanded))
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

fn validate_workspace(workspace: &Path, repositories: &[PathBuf]) -> Result<()> {
    let is_root = workspace
        .components()
        .filter(|component| *component != Component::RootDir)
        .count()
        == 0;
    if is_root {
        bail!("workspace_root must not be the filesystem root");
    }
    if dirs::home_dir().is_some_and(|home| workspace == home) {
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

        let config = Config::resolve(raw(&repository, &workspace)).unwrap();

        assert_eq!(
            config.repositories,
            vec![repository.canonicalize().unwrap()]
        );
        assert_eq!(config.workspace_root, workspace.canonicalize().unwrap());
        assert_eq!(config.poll_every, Duration::from_secs(30));
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

        let error = Config::resolve(config).unwrap_err();

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

        let error = Config::resolve(config).unwrap_err();

        assert!(
            error
                .to_string()
                .contains("max_concurrent_runs must be greater than zero")
        );
    }

    #[test]
    fn rejects_missing_repository() {
        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&workspace).unwrap();
        let config = raw(&temp.path().join("missing"), &workspace);

        let error = Config::resolve(config).unwrap_err();

        assert!(error.to_string().contains("repository path does not exist"));
    }

    #[test]
    fn rejects_workspace_inside_repository() {
        let temp = tempfile::tempdir().unwrap();
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&workspace).unwrap();

        let error = Config::resolve(raw(temp.path(), &workspace)).unwrap_err();

        assert!(error.to_string().contains("must not overlap"));
    }

    #[test]
    fn rejects_repository_inside_workspace() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        fs::create_dir(&repository).unwrap();

        let error = Config::resolve(raw(&repository, temp.path())).unwrap_err();

        assert!(error.to_string().contains("must not overlap"));
    }
}
