use std::collections::HashSet;
use std::env;
use std::fs::{self, File, OpenOptions};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};
use chrono::{DateTime, Utc};
use fs2::FileExt;
use serde::{Deserialize, Serialize};

use crate::config::{
    Config, canonical_directory_or_missing, ensure_primary_checkout, expand_path,
    repository_config_path, repository_data_directory_for_identity, repository_remote_identity,
};
use crate::storage::DATABASE_NAME;
use crate::workflow::{Trigger, WorkflowCatalog};

pub const MAX_ENABLED_REPOSITORIES: usize = 20;
pub const MAX_SOURCE_WORKFLOWS_PER_REPOSITORY: usize = 3;
pub const MIN_POLL_SECONDS: u64 = 60;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct FleetConfig {
    pub max_concurrent: usize,
    pub repositories: Vec<FleetRepository>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct FleetRepository {
    pub identity: String,
    pub display_name: String,
    pub path: PathBuf,
    pub enabled: bool,
    pub max_concurrent: Option<usize>,
}

#[derive(Debug, Serialize, Deserialize)]
struct ActiveFleetManifest {
    process_id: u32,
    process_identity: String,
    fleet: FleetConfig,
    repositories: Vec<RepositoryOperationSnapshot>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RepositoryOperationSnapshot {
    pub identity: String,
    pub repository_path: PathBuf,
    pub ledger_path: Option<PathBuf>,
    pub workspace_root: Option<PathBuf>,
    pub validation_error: Option<String>,
}

impl RepositoryOperationSnapshot {
    pub fn from_runtime(runtime: &RepositoryRuntime) -> Self {
        Self {
            identity: runtime.identity.clone(),
            repository_path: runtime.path.clone(),
            ledger_path: Some(runtime.ledger_path.clone()),
            workspace_root: Some(runtime.workspace_root.clone()),
            validation_error: None,
        }
    }

    pub fn from_error(repository: &FleetRepository, error: &anyhow::Error) -> Self {
        Self {
            identity: repository.identity.clone(),
            repository_path: repository.path.clone(),
            ledger_path: repository.pinned_ledger_path().ok(),
            workspace_root: None,
            validation_error: Some(
                format!("{error:#}")
                    .split_whitespace()
                    .collect::<Vec<_>>()
                    .join(" "),
            ),
        }
    }
}

pub struct ActiveFleetGuard {
    state_path: PathBuf,
    process_id: u32,
    process_identity: String,
    _lock: File,
}

#[derive(Debug, Clone)]
pub struct RepositoryRuntime {
    pub identity: String,
    pub path: PathBuf,
    pub enabled: bool,
    pub config: Config,
    pub catalog: WorkflowCatalog,
    pub ledger_path: PathBuf,
    pub workspace_root: PathBuf,
    pub health: RepositoryHealth,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RepositoryHealth {
    Loading,
    Healthy,
    BackingOff,
    RateLimited,
    InvalidConfig,
    Unavailable,
    Disabled,
}

pub const INITIAL_BACKOFF: Duration = Duration::from_secs(5);
pub const MAX_BACKOFF: Duration = Duration::from_secs(15 * 60);
pub const RATE_LIMIT_FALLBACK: Duration = Duration::from_secs(60);

pub fn repository_backoff(consecutive_failures: u32) -> Duration {
    let exponent = consecutive_failures.saturating_sub(1).min(31);
    INITIAL_BACKOFF
        .saturating_mul(1_u32 << exponent)
        .min(MAX_BACKOFF)
}

pub fn polling_stagger(repository: &str, workflow: &str, interval: Duration) -> Duration {
    let interval_millis = interval.as_millis();
    if interval_millis == 0 {
        return Duration::ZERO;
    }
    let digest = crate::hash::sha256_hex(format!("{repository}\0{workflow}"));
    let hash = u64::from_str_radix(&digest[..16], 16)
        .expect("a SHA-256 hexadecimal prefix is always a valid u64");
    Duration::from_millis((u128::from(hash) % interval_millis) as u64)
}

pub fn delay_to_next_poll(
    repository: &str,
    workflow: &str,
    interval: Duration,
    now: SystemTime,
) -> Result<Duration> {
    let interval_millis = interval.as_millis();
    if interval_millis == 0 {
        bail!("polling interval must be greater than zero");
    }
    let now_millis = now
        .duration_since(UNIX_EPOCH)
        .context("system clock is before the Unix epoch")?
        .as_millis();
    let offset = polling_stagger(repository, workflow, interval).as_millis();
    let period_start = now_millis - (now_millis % interval_millis);
    let mut due = period_start + offset;
    if due <= now_millis {
        due += interval_millis;
    }
    Ok(Duration::from_millis((due - now_millis) as u64))
}

pub fn resolve_rate_limit_delay(
    retry_at: Option<DateTime<Utc>>,
    observed_at: DateTime<Utc>,
) -> Duration {
    retry_at
        .filter(|retry_at| *retry_at > observed_at)
        .and_then(|retry_at| (retry_at - observed_at).to_std().ok())
        .unwrap_or(RATE_LIMIT_FALLBACK)
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawFleetConfig {
    max_concurrent: usize,
    #[serde(rename = "repository")]
    repositories: Vec<RawFleetRepository>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawFleetRepository {
    name: String,
    path: PathBuf,
    #[serde(default = "enabled_by_default")]
    enabled: bool,
    max_concurrent: Option<usize>,
}

fn enabled_by_default() -> bool {
    true
}

impl FleetConfig {
    pub fn load(path: &Path) -> Result<Self> {
        let current_dir = std::env::current_dir().context("failed to resolve current directory")?;
        let path = expand_path(path, &current_dir)?;
        let contents = fs::read_to_string(&path)
            .with_context(|| format!("failed to read fleet config {}", path.display()))?;
        let raw: RawFleetConfig = toml::from_str(&contents)
            .with_context(|| format!("failed to parse fleet config {}", path.display()))?;
        if raw.max_concurrent == 0 {
            bail!("max_concurrent must be greater than zero");
        }
        if raw.repositories.is_empty() {
            bail!("repository must contain at least one entry");
        }
        let enabled_count = raw
            .repositories
            .iter()
            .filter(|repository| repository.enabled)
            .count();
        if enabled_count == 0 {
            bail!("fleet must contain at least one enabled repository");
        }
        if enabled_count > MAX_ENABLED_REPOSITORIES {
            bail!(
                "fleet supports at most {MAX_ENABLED_REPOSITORIES} enabled repositories; got {enabled_count}"
            );
        }
        let base = path
            .parent()
            .context("fleet configuration path has no parent directory")?;
        let mut identities = HashSet::new();
        let mut paths = HashSet::new();
        let repositories = raw
            .repositories
            .into_iter()
            .enumerate()
            .map(|(index, repository)| {
                let display_name = validate_identity(index, &repository.name)?;
                let identity = display_name.to_ascii_lowercase();
                if !identities.insert(identity.clone()) {
                    bail!("duplicate repository identity {identity}");
                }
                if repository.max_concurrent == Some(0) {
                    bail!("repository {display_name} max_concurrent must be greater than zero");
                }
                let path = resolve_repository_path(&repository.path, base)?;
                if !paths.insert(path.clone()) {
                    bail!("duplicate canonical repository path {}", path.display());
                }
                Ok(FleetRepository {
                    identity,
                    display_name,
                    path,
                    enabled: repository.enabled,
                    max_concurrent: repository.max_concurrent,
                })
            })
            .collect::<Result<Vec<_>>>()?;
        Ok(Self {
            max_concurrent: raw.max_concurrent,
            repositories,
        })
    }

    pub fn load_for_operation(path: &Path) -> Result<Self> {
        if let Some(manifest) = active_fleet_manifest(path)? {
            return Ok(manifest.fleet);
        }
        Self::load(path)
    }
}

pub fn activate_fleet(
    path: &Path,
    fleet: &FleetConfig,
    repositories: &[RepositoryOperationSnapshot],
) -> Result<ActiveFleetGuard> {
    let snapshot_identities = repositories
        .iter()
        .map(|repository| repository.identity.as_str())
        .collect::<HashSet<_>>();
    if snapshot_identities.len() != repositories.len()
        || repositories.len() != fleet.repositories.len()
        || fleet
            .repositories
            .iter()
            .any(|repository| !snapshot_identities.contains(repository.identity.as_str()))
    {
        bail!("active fleet startup snapshot does not match the fleet repositories");
    }
    let state_path = active_fleet_state_path(path)?;
    let parent = state_path
        .parent()
        .context("active fleet state path has no parent directory")?;
    fs::create_dir_all(parent).with_context(|| {
        format!(
            "failed to create fleet state directory {}",
            parent.display()
        )
    })?;
    let lock_path = state_path.with_extension("lock");
    let lock = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(&lock_path)
        .with_context(|| {
            format!(
                "failed to open fleet supervisor lock {}",
                lock_path.display()
            )
        })?;
    lock.try_lock_exclusive().with_context(|| {
        format!(
            "fleet configuration is already supervised; lock {} is held",
            lock_path.display()
        )
    })?;
    let _ = fs::remove_file(&state_path);
    let process_id = std::process::id();
    let process_identity = crate::runtime::process_identity(process_id)
        .context("failed to resolve Factory supervisor process identity")?;
    let manifest = ActiveFleetManifest {
        process_id,
        process_identity: process_identity.clone(),
        fleet: fleet.clone(),
        repositories: repositories.to_vec(),
    };
    let bytes =
        serde_json::to_vec(&manifest).context("failed to encode active fleet startup snapshot")?;
    let temporary = parent.join(format!(
        ".{}.{}.tmp",
        state_path
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or("fleet"),
        process_id
    ));
    fs::write(&temporary, bytes).with_context(|| {
        format!(
            "failed to write active fleet startup snapshot {}",
            temporary.display()
        )
    })?;
    fs::rename(&temporary, &state_path).with_context(|| {
        format!(
            "failed to publish active fleet startup snapshot {}",
            state_path.display()
        )
    })?;
    Ok(ActiveFleetGuard {
        state_path,
        process_id,
        process_identity,
        _lock: lock,
    })
}

pub fn active_repository_operation(
    path: &Path,
    identity: &str,
) -> Result<Option<RepositoryOperationSnapshot>> {
    Ok(active_fleet_manifest(path)?.and_then(|manifest| {
        manifest
            .repositories
            .into_iter()
            .find(|repository| repository.identity == identity)
    }))
}

impl Drop for ActiveFleetGuard {
    fn drop(&mut self) {
        let Ok(contents) = fs::read(&self.state_path) else {
            return;
        };
        let Ok(manifest) = serde_json::from_slice::<ActiveFleetManifest>(&contents) else {
            return;
        };
        if manifest.process_id == self.process_id
            && manifest.process_identity == self.process_identity
        {
            let _ = fs::remove_file(&self.state_path);
        }
    }
}

fn active_fleet_manifest(path: &Path) -> Result<Option<ActiveFleetManifest>> {
    let state_path = active_fleet_state_path(path)?;
    let contents = match fs::read(&state_path) {
        Ok(contents) => contents,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => {
            return Err(error).with_context(|| {
                format!("failed to read active fleet state {}", state_path.display())
            });
        }
    };
    let manifest: ActiveFleetManifest = serde_json::from_slice(&contents).with_context(|| {
        format!(
            "failed to parse active fleet state {}",
            state_path.display()
        )
    })?;
    let active = crate::runtime::process_identity(manifest.process_id)
        .is_some_and(|identity| identity == manifest.process_identity);
    if active {
        return Ok(Some(manifest));
    }
    Ok(None)
}

fn active_fleet_state_path(path: &Path) -> Result<PathBuf> {
    let current_dir = env::current_dir().context("failed to resolve current directory")?;
    let resolved = expand_path(path, &current_dir)?;
    let resolved = resolved.canonicalize().unwrap_or(resolved);
    let digest = crate::hash::sha256_hex(resolved.as_os_str().as_encoded_bytes());
    let base = match env::var_os("FACTORY_DATA_HOME").map(PathBuf::from) {
        Some(base) if base.is_absolute() => base,
        Some(base) => current_dir.join(base),
        None => dirs::home_dir()
            .map(|home| home.join(".factory"))
            .context("could not determine Factory data directory")?,
    };
    Ok(base.join("fleets").join(format!("{}.json", &digest[..20])))
}

fn resolve_repository_path(path: &Path, base: &Path) -> Result<PathBuf> {
    let expanded = expand_path(path, base)?;
    match expanded.canonicalize() {
        Ok(path) => Ok(path),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            canonical_directory_or_missing("repository", &expanded, base).or(Ok(expanded))
        }
        Err(_) => Ok(expanded),
    }
}

impl FleetRepository {
    pub fn pinned_ledger_path(&self) -> Result<PathBuf> {
        Ok(repository_data_directory_for_identity(&self.path, &self.identity)?.join(DATABASE_NAME))
    }

    pub fn load_runtime(&self) -> Result<RepositoryRuntime> {
        validate_checkout(&self.path, &self.identity)?;
        let config_path = repository_config_path(&self.path);
        let mut config = if self.enabled {
            Config::load(&config_path)?
        } else {
            Config::load_for_inspection(&config_path)?
        };
        let catalog = WorkflowCatalog::load(&config)?;
        if self.enabled {
            if config.poll_every.as_secs() < MIN_POLL_SECONDS {
                bail!("poll_every must be at least {MIN_POLL_SECONDS}s in fleet mode");
            }
            catalog.validate_ticket_workflows()?;
            let source_workflows = catalog
                .entries
                .iter()
                .filter(|entry| {
                    entry.errors.is_empty()
                        && matches!(
                            entry.trigger,
                            Some(Trigger::Source { .. } | Trigger::Status(_) | Trigger::Label(_))
                        )
                })
                .count();
            if source_workflows > MAX_SOURCE_WORKFLOWS_PER_REPOSITORY {
                bail!(
                    "fleet supports at most {MAX_SOURCE_WORKFLOWS_PER_REPOSITORY} source-triggered workflows per repository; got {source_workflows}"
                );
            }
        }
        if let Some(limit) = self.max_concurrent {
            let effective = config.max_concurrent_runs_per_repository.min(limit);
            config.max_concurrent_runs = effective;
            config.max_concurrent_runs_per_repository = effective;
        }
        let ledger_path = config.data_directory.join(DATABASE_NAME);
        let workspace_root = config.workspace_root.clone();
        Ok(RepositoryRuntime {
            identity: self.identity.clone(),
            path: self.path.clone(),
            enabled: self.enabled,
            config,
            catalog,
            ledger_path,
            workspace_root,
            health: if self.enabled {
                RepositoryHealth::Healthy
            } else {
                RepositoryHealth::Disabled
            },
        })
    }
}

fn validate_identity(index: usize, value: &str) -> Result<String> {
    let value = value.trim();
    let mut parts = value.split('/');
    let owner = parts.next().unwrap_or_default();
    let repository = parts.next().unwrap_or_default();
    if parts.next().is_some()
        || !valid_identity_segment(owner)
        || !valid_identity_segment(repository)
    {
        bail!(
            "repository entry {} name must have the GitHub owner/name shape",
            index + 1
        );
    }
    Ok(value.to_owned())
}

fn valid_identity_segment(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 100
        && !value.starts_with('.')
        && !value.ends_with('.')
        && value.chars().all(|character| {
            character.is_ascii_alphanumeric() || matches!(character, '-' | '_' | '.')
        })
}

fn validate_checkout(path: &Path, expected_identity: &str) -> Result<()> {
    let bare = git_output(path, &["rev-parse", "--is-bare-repository"])
        .context("repository path is not a Git checkout")?;
    if bare.trim() != "false" {
        bail!("repository path is a bare Git repository");
    }
    let root = git_output(path, &["rev-parse", "--show-toplevel"])
        .context("repository path is not a Git checkout")?;
    let root = PathBuf::from(root.trim())
        .canonicalize()
        .context("failed to resolve Git checkout root")?;
    if root != path {
        bail!(
            "repository path must be the canonical Git checkout root; got {}",
            path.display()
        );
    }
    ensure_primary_checkout(path)?;
    let actual = repository_remote_identity(path)?;
    if actual != expected_identity {
        bail!(
            "origin identity {actual} does not match pinned repository identity {expected_identity}"
        );
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

#[cfg(test)]
mod tests {
    use super::*;

    fn repository(root: &Path, name: &str, origin: &str) -> PathBuf {
        let path = root.join(name);
        fs::create_dir(&path).unwrap();
        assert!(
            Command::new("git")
                .args(["init", "--quiet"])
                .current_dir(&path)
                .status()
                .unwrap()
                .success()
        );
        assert!(
            Command::new("git")
                .args(["remote", "add", "origin", origin])
                .current_dir(&path)
                .status()
                .unwrap()
                .success()
        );
        path.canonicalize().unwrap()
    }

    #[test]
    fn loads_defaults_relative_paths_and_normalized_identities() {
        let temp = tempfile::tempdir().unwrap();
        let first = repository(temp.path(), "first", "git@github.com:Acme/First.git");
        let second = repository(temp.path(), "second", "https://github.com/acme/second");
        let fleet = temp.path().join("fleet.toml");
        fs::write(
            &fleet,
            "max_concurrent = 4\n[[repository]]\nname = \"Acme/First\"\npath = \"first\"\n[[repository]]\nname = \"acme/second\"\npath = \"second\"\nenabled = false\nmax_concurrent = 1\n",
        )
        .unwrap();
        let loaded = FleetConfig::load(&fleet).unwrap();
        assert_eq!(loaded.max_concurrent, 4);
        assert_eq!(loaded.repositories[0].identity, "acme/first");
        assert_eq!(loaded.repositories[0].path, first);
        assert!(loaded.repositories[0].enabled);
        assert_eq!(loaded.repositories[1].path, second);
        assert!(!loaded.repositories[1].enabled);
        assert_eq!(loaded.repositories[1].max_concurrent, Some(1));
    }

    #[test]
    fn rejects_unknown_fields_zero_limits_and_empty_enabled_set() {
        let temp = tempfile::tempdir().unwrap();
        let path = repository(temp.path(), "repo", "git@github.com:acme/repo.git");
        for contents in [
            format!(
                "version = 1\nmax_concurrent = 1\n[[repository]]\nname = \"acme/repo\"\npath = {:?}\n",
                path
            ),
            format!(
                "max_concurrent = 0\n[[repository]]\nname = \"acme/repo\"\npath = {:?}\n",
                path
            ),
            format!(
                "max_concurrent = 1\n[[repository]]\nname = \"acme/repo\"\npath = {:?}\nenabled = false\n",
                path
            ),
            format!(
                "max_concurrent = 1\n[[repository]]\nname = \"acme/repo\"\npath = {:?}\nmax_concurrent = 0\n",
                path
            ),
        ] {
            let fleet = temp.path().join("fleet.toml");
            fs::write(&fleet, contents).unwrap();
            assert!(FleetConfig::load(&fleet).is_err());
        }
    }

    #[test]
    fn rejects_duplicate_normalized_identity_and_canonical_path() {
        let temp = tempfile::tempdir().unwrap();
        let first = repository(temp.path(), "first", "git@github.com:acme/first.git");
        let second = repository(temp.path(), "second", "git@github.com:acme/second.git");
        let fleet = temp.path().join("fleet.toml");
        fs::write(
            &fleet,
            format!(
                "max_concurrent = 1\n[[repository]]\nname = \"Acme/First\"\npath = {:?}\n[[repository]]\nname = \"acme/first\"\npath = {:?}\n",
                first, second
            ),
        )
        .unwrap();
        assert!(
            FleetConfig::load(&fleet)
                .unwrap_err()
                .to_string()
                .contains("duplicate repository identity")
        );
        fs::write(
            &fleet,
            format!(
                "max_concurrent = 1\n[[repository]]\nname = \"acme/first\"\npath = {:?}\n[[repository]]\nname = \"acme/second\"\npath = {:?}\n",
                first, first
            ),
        )
        .unwrap();
        assert!(
            FleetConfig::load(&fleet)
                .unwrap_err()
                .to_string()
                .contains("duplicate canonical repository path")
        );

        let real = temp.path().join("real");
        fs::create_dir(&real).unwrap();
        std::os::unix::fs::symlink(&real, temp.path().join("link")).unwrap();
        fs::write(
            &fleet,
            "max_concurrent = 1\n[[repository]]\nname = \"acme/first\"\npath = \"real/missing\"\n[[repository]]\nname = \"acme/second\"\npath = \"link/missing\"\n",
        )
        .unwrap();
        assert!(
            FleetConfig::load(&fleet)
                .unwrap_err()
                .to_string()
                .contains("duplicate canonical repository path")
        );
    }

    #[test]
    fn checkout_identity_is_pinned() {
        let temp = tempfile::tempdir().unwrap();
        let path = repository(
            temp.path(),
            "repo",
            "ssh://git@ssh.github.com/acme/repo.git",
        );
        validate_checkout(&path, "acme/repo").unwrap();
        let error = validate_checkout(&path, "acme/changed").unwrap_err();
        assert!(error.to_string().contains("does not match pinned"));
    }

    #[test]
    fn rejects_more_than_the_supported_enabled_repository_envelope() {
        let temp = tempfile::tempdir().unwrap();
        let entries = (0..=MAX_ENABLED_REPOSITORIES)
            .map(|index| {
                format!("[[repository]]\nname = \"acme/repo-{index}\"\npath = \"repo-{index}\"\n")
            })
            .collect::<String>();
        let fleet = temp.path().join("fleet.toml");
        fs::write(&fleet, format!("max_concurrent = 1\n{entries}")).unwrap();
        let error = FleetConfig::load(&fleet).unwrap_err();
        assert!(error.to_string().contains("supports at most 20"));
    }

    #[test]
    fn repository_backoff_doubles_from_five_seconds_and_caps_at_fifteen_minutes() {
        assert_eq!(repository_backoff(1), Duration::from_secs(5));
        assert_eq!(repository_backoff(2), Duration::from_secs(10));
        assert_eq!(repository_backoff(8), Duration::from_secs(640));
        assert_eq!(repository_backoff(9), Duration::from_secs(900));
        assert_eq!(repository_backoff(100), Duration::from_secs(900));
    }

    #[test]
    fn polling_stagger_is_stable_and_aligned_across_restart() {
        let interval = Duration::from_secs(60);
        let first = polling_stagger("acme/first", "implement", interval);
        assert_eq!(first, polling_stagger("acme/first", "implement", interval));
        assert!(first < interval);
        assert_ne!(first, polling_stagger("acme/second", "implement", interval));
        let now = UNIX_EPOCH + Duration::from_secs(1_700_000_013);
        let delay = delay_to_next_poll("acme/first", "implement", interval, now).unwrap();
        let restarted = delay_to_next_poll("acme/first", "implement", interval, now).unwrap();
        assert_eq!(delay, restarted);
        assert!(delay <= interval);
    }

    #[test]
    fn rate_limit_retry_uses_future_timestamp_or_sixty_second_fallback() {
        let observed = DateTime::parse_from_rfc3339("2026-07-27T12:00:00Z")
            .unwrap()
            .with_timezone(&Utc);
        let future = DateTime::parse_from_rfc3339("2026-07-27T12:02:30+00:00")
            .unwrap()
            .with_timezone(&Utc);
        let past = DateTime::parse_from_rfc3339("2026-07-27T11:59:59Z")
            .unwrap()
            .with_timezone(&Utc);
        assert_eq!(
            resolve_rate_limit_delay(Some(future), observed),
            Duration::from_secs(150)
        );
        assert_eq!(
            resolve_rate_limit_delay(None, observed),
            RATE_LIMIT_FALLBACK
        );
        assert_eq!(
            resolve_rate_limit_delay(Some(past), observed),
            RATE_LIMIT_FALLBACK
        );
    }
}
