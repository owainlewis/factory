use std::env;
use std::fmt;
use std::fs;
use std::path::{Component, Path, PathBuf};
use std::process::Command;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::{Deserialize, Serialize};
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
    pub worker: Option<WorkerConfig>,
    pub source: Option<SourceConfig>,
    pub github: GitHubConfig,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct WorkerConfig {
    pub image: String,
    pub memory: String,
    pub cpus: u32,
    pub pids: u32,
    pub codex_auth: PathBuf,
    pub github_token_env: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SourceConfig {
    pub owner: String,
    pub project_number: u64,
    pub status_field: String,
    pub trusted_users: Vec<String>,
    pub states: SourceStates,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SourceStates {
    pub ready_for_spec: String,
    pub creating_spec: String,
    pub ready_to_implement: String,
    pub implementing: String,
    pub ready_to_review: String,
    pub done: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PipelineState {
    ReadyForSpec,
    CreatingSpec,
    ReadyToImplement,
    Implementing,
    ReadyToReview,
    Done,
}

impl PipelineState {
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::ReadyForSpec => "ready_for_spec",
            Self::CreatingSpec => "creating_spec",
            Self::ReadyToImplement => "ready_to_implement",
            Self::Implementing => "implementing",
            Self::ReadyToReview => "ready_to_review",
            Self::Done => "done",
        }
    }
}

impl fmt::Display for PipelineState {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

impl std::str::FromStr for PipelineState {
    type Err = anyhow::Error;

    fn from_str(value: &str) -> Result<Self> {
        match value {
            "ready_for_spec" => Ok(Self::ReadyForSpec),
            "creating_spec" => Ok(Self::CreatingSpec),
            "ready_to_implement" => Ok(Self::ReadyToImplement),
            "implementing" => Ok(Self::Implementing),
            "ready_to_review" => Ok(Self::ReadyToReview),
            "done" => Ok(Self::Done),
            _ => bail!("unknown pipeline state {value:?}"),
        }
    }
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
    worker: Option<RawWorkerConfig>,
    source: Option<RawSourceConfig>,
    github: Option<RawGitHubConfig>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawWorkerConfig {
    kind: String,
    image: String,
    memory: String,
    cpus: u32,
    pids: u32,
    codex_auth: Option<String>,
    github_token_env: Option<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawSourceConfig {
    kind: String,
    owner: String,
    project_number: u64,
    status_field: String,
    trusted_users: Vec<String>,
    states: RawSourceStates,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawSourceStates {
    ready_for_spec: String,
    creating_spec: String,
    ready_to_implement: String,
    implementing: String,
    ready_to_review: String,
    done: String,
}

#[derive(Debug, Clone, Deserialize)]
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
        if raw.worker.is_some() && raw.max_concurrent_runs != 1 {
            bail!("Docker workers require max_concurrent_runs = 1");
        }
        if raw.source.is_some() && raw.github.is_some() {
            bail!("configure [source] or legacy [github], not both");
        }
        let source = raw.source.map(resolve_source).transpose()?;
        let github = match raw.github {
            Some(github) => resolve_github(github)?,
            None => {
                let source = source
                    .as_ref()
                    .context("configuration must contain [source] or legacy [github]")?;
                GitHubConfig {
                    trusted_approvers: source.trusted_users.clone(),
                    ready_label: "factory:ready".to_owned(),
                    proposed_label: "factory:proposed".to_owned(),
                    needs_review_label: "factory:needs-review".to_owned(),
                }
            }
        };
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
        let worker = raw
            .worker
            .map(|worker| resolve_worker(worker, &repository, &data_directory))
            .transpose()?;
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
            worker,
            source,
            github,
        })
    }
}

impl SourceConfig {
    pub fn state_name(&self, state: PipelineState) -> &str {
        match state {
            PipelineState::ReadyForSpec => &self.states.ready_for_spec,
            PipelineState::CreatingSpec => &self.states.creating_spec,
            PipelineState::ReadyToImplement => &self.states.ready_to_implement,
            PipelineState::Implementing => &self.states.implementing,
            PipelineState::ReadyToReview => &self.states.ready_to_review,
            PipelineState::Done => &self.states.done,
        }
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
        if let Some(worker) = &self.worker {
            writeln!(formatter, "worker: docker")?;
            writeln!(formatter, "worker.image: {}", worker.image)?;
            writeln!(formatter, "worker.memory: {}", worker.memory)?;
            writeln!(formatter, "worker.cpus: {}", worker.cpus)?;
            writeln!(formatter, "worker.pids: {}", worker.pids)?;
            writeln!(
                formatter,
                "worker.codex_auth: {}",
                worker.codex_auth.display()
            )?;
            writeln!(
                formatter,
                "worker.github_token_env: {}",
                worker.github_token_env
            )?;
        }
        if let Some(source) = &self.source {
            writeln!(
                formatter,
                "source: github_project {}/{}",
                source.owner, source.project_number
            )?;
            writeln!(formatter, "status_field: {}", source.status_field)?;
            for state in [
                PipelineState::ReadyForSpec,
                PipelineState::CreatingSpec,
                PipelineState::ReadyToImplement,
                PipelineState::Implementing,
                PipelineState::ReadyToReview,
                PipelineState::Done,
            ] {
                writeln!(formatter, "state.{state}: {}", source.state_name(state))?;
            }
        }
        writeln!(
            formatter,
            "state_directory: {}",
            self.data_directory.display()
        )?;
        writeln!(formatter, "worktrees: {}", self.workspace_root.display())
    }
}

fn resolve_worker(
    raw: RawWorkerConfig,
    repository: &Path,
    data_directory: &Path,
) -> Result<WorkerConfig> {
    if raw.kind != "docker" {
        bail!("worker.kind must be \"docker\"");
    }

    let image = raw.image.trim();
    if image.is_empty()
        || image.chars().count() > 255
        || !image.chars().all(|character| {
            character.is_ascii_alphanumeric()
                || matches!(character, '.' | '_' | '/' | ':' | '@' | '-')
        })
        || !image
            .chars()
            .next()
            .is_some_and(|character| character.is_ascii_alphanumeric())
        || (!image.rsplit('/').next().unwrap_or(image).contains(':') && !image.contains('@'))
    {
        bail!("worker.image must be a valid, explicitly tagged Docker image reference");
    }

    let memory = raw.memory.trim().to_ascii_lowercase();
    let suffix_start = memory
        .find(|character: char| !character.is_ascii_digit())
        .unwrap_or(memory.len());
    let (amount, suffix) = memory.split_at(suffix_start);
    if amount.is_empty()
        || amount.starts_with('0')
        || !amount.chars().all(|character| character.is_ascii_digit())
        || !matches!(
            suffix,
            "" | "b" | "k" | "kb" | "m" | "mb" | "g" | "gb" | "t" | "tb"
        )
    {
        bail!("worker.memory must be a positive Docker memory limit such as \"8g\"");
    }
    if raw.cpus == 0 {
        bail!("worker.cpus must be greater than zero");
    }
    if raw.pids == 0 {
        bail!("worker.pids must be greater than zero");
    }

    let codex_auth = match raw.codex_auth {
        Some(path) => expand_path(Path::new(path.trim()), repository)?,
        None => data_directory.join("codex/auth.json"),
    };
    if codex_auth.file_name().and_then(|name| name.to_str()) != Some("auth.json") {
        bail!("worker.codex_auth must name an auth.json file");
    }
    let codex_auth = resolve_file_or_missing("worker.codex_auth", &codex_auth)?;
    if codex_auth.starts_with(repository) {
        bail!("worker.codex_auth must be outside the repository");
    }
    if let Some(home) = dirs::home_dir()
        && codex_auth == home.join(".codex/auth.json")
    {
        bail!("worker.codex_auth must use a dedicated Factory credential, not ~/.codex/auth.json");
    }

    let github_token_env = raw
        .github_token_env
        .unwrap_or_else(|| "FACTORY_GITHUB_TOKEN".to_owned());
    let github_token_env = github_token_env.trim();
    if github_token_env.is_empty()
        || !github_token_env
            .chars()
            .next()
            .is_some_and(|character| character.is_ascii_alphabetic() || character == '_')
        || !github_token_env
            .chars()
            .all(|character| character.is_ascii_alphanumeric() || character == '_')
    {
        bail!("worker.github_token_env must be a valid environment variable name");
    }

    Ok(WorkerConfig {
        image: image.to_owned(),
        memory,
        cpus: raw.cpus,
        pids: raw.pids,
        codex_auth,
        github_token_env: github_token_env.to_owned(),
    })
}

fn resolve_file_or_missing(name: &str, path: &Path) -> Result<PathBuf> {
    match fs::symlink_metadata(path) {
        Ok(metadata) => {
            if metadata.file_type().is_symlink() || !metadata.is_file() {
                bail!("{name} must be a regular file: {}", path.display());
            }
            path.canonicalize()
                .with_context(|| format!("failed to resolve {name}: {}", path.display()))
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            let parent = path
                .parent()
                .with_context(|| format!("{name} has no parent directory"))?;
            let parent = canonical_directory_or_missing(name, parent, parent)?;
            Ok(parent.join("auth.json"))
        }
        Err(error) => {
            Err(error).with_context(|| format!("failed to inspect {name}: {}", path.display()))
        }
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
        if value.chars().count() > 50
            || value
                .chars()
                .any(|character| matches!(character, '\0' | '\n' | '\r'))
        {
            bail!("github.{name} must be a valid GitHub label of at most 50 characters");
        }
        Ok(value.to_owned())
    };
    let ready_label = label("ready_label", raw.ready_label)?;
    let proposed_label = label("proposed_label", raw.proposed_label)?;
    let needs_review_label = label("needs_review_label", raw.needs_review_label)?;
    if ready_label.eq_ignore_ascii_case(&proposed_label)
        || ready_label.eq_ignore_ascii_case(&needs_review_label)
        || proposed_label.eq_ignore_ascii_case(&needs_review_label)
    {
        bail!("github ready, proposed, and needs-review labels must be distinct");
    }
    Ok(GitHubConfig {
        trusted_approvers,
        ready_label,
        proposed_label,
        needs_review_label,
    })
}

fn resolve_source(raw: RawSourceConfig) -> Result<SourceConfig> {
    if raw.kind != "github_project" {
        bail!("source.kind must be \"github_project\"");
    }
    let owner = validate_github_login("source.owner", raw.owner)?;
    if raw.project_number == 0 {
        bail!("source.project_number must be greater than zero");
    }
    let status_field = validate_display_name("source.status_field", raw.status_field)?;
    if raw.trusted_users.is_empty() {
        bail!("source.trusted_users must contain at least one login");
    }
    let mut trusted_users = Vec::with_capacity(raw.trusted_users.len());
    for user in raw.trusted_users {
        let user = validate_github_login("source.trusted_users", user)?;
        if !trusted_users
            .iter()
            .any(|existing: &String| existing.eq_ignore_ascii_case(&user))
        {
            trusted_users.push(user);
        }
    }
    let states = SourceStates {
        ready_for_spec: validate_display_name(
            "source.states.ready_for_spec",
            raw.states.ready_for_spec,
        )?,
        creating_spec: validate_display_name(
            "source.states.creating_spec",
            raw.states.creating_spec,
        )?,
        ready_to_implement: validate_display_name(
            "source.states.ready_to_implement",
            raw.states.ready_to_implement,
        )?,
        implementing: validate_display_name("source.states.implementing", raw.states.implementing)?,
        ready_to_review: validate_display_name(
            "source.states.ready_to_review",
            raw.states.ready_to_review,
        )?,
        done: validate_display_name("source.states.done", raw.states.done)?,
    };
    let names = [
        &states.ready_for_spec,
        &states.creating_spec,
        &states.ready_to_implement,
        &states.implementing,
        &states.ready_to_review,
        &states.done,
    ];
    for (index, name) in names.iter().enumerate() {
        if names[..index]
            .iter()
            .any(|existing| existing.eq_ignore_ascii_case(name))
        {
            bail!("source state display names must be distinct");
        }
    }
    Ok(SourceConfig {
        owner,
        project_number: raw.project_number,
        status_field,
        trusted_users,
        states,
    })
}

fn validate_github_login(name: &str, value: String) -> Result<String> {
    let value = value.trim();
    if value.is_empty()
        || !value
            .chars()
            .all(|character| character.is_ascii_alphanumeric() || character == '-')
    {
        bail!("{name} contains invalid login {value:?}");
    }
    Ok(value.to_owned())
}

fn validate_display_name(name: &str, value: String) -> Result<String> {
    let value = value.trim();
    if value.is_empty() || value.chars().count() > 100 || value.chars().any(char::is_control) {
        bail!("{name} must be 1-100 characters without control characters");
    }
    Ok(value.to_owned())
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
    let origin = git_output(repository, &["config", "--get", "remote.origin.url"])
        .context("repository has no configured origin remote")?;
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
            worker: None,
            source: None,
            github: Some(RawGitHubConfig {
                trusted_approvers: vec!["owainlewis".into()],
                ready_label: "factory:ready".into(),
                proposed_label: "factory:proposed".into(),
                needs_review_label: "factory:needs-review".into(),
            }),
        }
    }

    fn docker_worker() -> RawWorkerConfig {
        RawWorkerConfig {
            kind: "docker".into(),
            image: "factory-codex:dev".into(),
            memory: "8g".into(),
            cpus: 4,
            pids: 512,
            codex_auth: None,
            github_token_env: None,
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
    fn resolves_github_project_source_and_synthesizes_legacy_defaults() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let mut input = raw(&repository, &workspace);
        input.github = None;
        input.source = Some(RawSourceConfig {
            kind: "github_project".into(),
            owner: "owainlewis".into(),
            project_number: 16,
            status_field: "Workflow".into(),
            trusted_users: vec!["OwainLewis".into(), "owainlewis".into()],
            states: RawSourceStates {
                ready_for_spec: "Needs triage".into(),
                creating_spec: "Writing spec".into(),
                ready_to_implement: "Ready".into(),
                implementing: "Building".into(),
                ready_to_review: "Review".into(),
                done: "Shipped".into(),
            },
        });

        let config = Config::resolve(input, &repository).unwrap();

        let source = config.source.unwrap();
        assert_eq!(source.status_field, "Workflow");
        assert_eq!(source.state_name(PipelineState::ReadyToImplement), "Ready");
        assert_eq!(source.trusted_users, ["OwainLewis"]);
        assert_eq!(config.github.trusted_approvers, ["OwainLewis"]);
        assert_eq!(config.github.ready_label, "factory:ready");
    }

    #[test]
    fn resolves_docker_worker_with_dedicated_default_credentials() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let mut input = raw(&repository, &workspace);
        input.max_concurrent_runs = 1;
        input.worker = Some(docker_worker());

        let config = Config::resolve(input, &repository).unwrap();
        let worker = config.worker.unwrap();

        assert_eq!(worker.image, "factory-codex:dev");
        assert_eq!(worker.memory, "8g");
        assert_eq!(worker.cpus, 4);
        assert_eq!(worker.pids, 512);
        assert_eq!(worker.github_token_env, "FACTORY_GITHUB_TOKEN");
        assert_eq!(
            worker.codex_auth,
            config.data_directory.join("codex/auth.json")
        );
    }

    #[test]
    fn docker_worker_requires_one_concurrent_run() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let mut input = raw(&repository, &workspace);
        input.worker = Some(docker_worker());

        let error = Config::resolve(input, &repository).unwrap_err();

        assert!(error.to_string().contains("max_concurrent_runs = 1"));
    }

    #[test]
    fn rejects_unsafe_docker_worker_values() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");

        for (field, change) in [
            ("tagged image", 0_u8),
            ("memory limit", 1),
            ("CPU limit", 2),
            ("process limit", 3),
            ("token environment", 4),
        ] {
            let mut input = raw(&repository, &workspace);
            input.max_concurrent_runs = 1;
            let mut worker = docker_worker();
            match change {
                0 => worker.image = "factory-codex".into(),
                1 => worker.memory = "0g".into(),
                2 => worker.cpus = 0,
                3 => worker.pids = 0,
                4 => worker.github_token_env = Some("BAD-NAME".into()),
                _ => unreachable!(),
            }
            input.worker = Some(worker);

            let error = Config::resolve(input, &repository).unwrap_err();
            assert!(!error.to_string().is_empty(), "accepted invalid {field}");
        }
    }

    #[test]
    fn rejects_repository_owned_codex_auth() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let mut input = raw(&repository, &workspace);
        input.max_concurrent_runs = 1;
        let mut worker = docker_worker();
        worker.codex_auth = Some(".factory/auth.json".into());
        input.worker = Some(worker);

        let error = Config::resolve(input, &repository).unwrap_err();

        assert!(error.to_string().contains("outside the repository"));
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
    fn rejects_overlapping_github_labels() {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        let workspace = temp.path().join("worktrees");
        let mut config = raw(&repository, &workspace);
        config.github.as_mut().unwrap().proposed_label = "Factory:Ready".into();

        let error = Config::resolve(config, &repository).unwrap_err();

        assert!(error.to_string().contains("labels must be distinct"));
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
            worker: None,
            source: None,
            github: Some(RawGitHubConfig {
                trusted_approvers: vec!["owainlewis".into()],
                ready_label: "factory:ready".into(),
                proposed_label: "factory:proposed".into(),
                needs_review_label: "factory:needs-review".into(),
            }),
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
        let github = valid.github.as_ref().unwrap().clone();
        let valid = toml::to_string(&serde_json::json!({
            "version": valid.version,
            "poll_every": valid.poll_every,
            "default_runtime": valid.default_runtime,
            "default_timeout": valid.default_timeout,
            "maximum_timeout": valid.maximum_timeout,
            "max_concurrent_runs": valid.max_concurrent_runs,
            "github": {
                "trusted_approvers": github.trusted_approvers,
                "ready_label": github.ready_label,
                "proposed_label": github.proposed_label,
                "needs_review_label": github.needs_review_label,
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
