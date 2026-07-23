use std::collections::{BTreeSet, HashMap};
use std::fs::{self, File, OpenOptions};
use std::path::{Path, PathBuf};
use std::str::FromStr;
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};
use fs2::FileExt;
use rusqlite::{Connection, OpenFlags, OptionalExtension, TransactionBehavior, params};

pub const DATABASE_NAME: &str = "factory.sqlite3";
const SCHEMA_VERSION: i64 = 10;
pub const MAX_RESULT_BYTES: usize = 256 * 1024;
pub const MAX_ERROR_BYTES: usize = 64 * 1024;
pub const MAX_SESSION_ID_BYTES: usize = 1024;
pub const MAX_ACTIVITY_BYTES: usize = 64 * 1024;
pub const MAX_RECOVERY_ATTEMPTS: u32 = 2;
pub const AUTOMATIC_DELIVERY_CLEANUP: &str =
    "clean published delivery with recorded pull request and handoff";
pub const OPERATOR_CONFIRMED_CLEANUP: &str = "operator-confirmed cleanup";
const DAEMON_OWNER_LEASE_MILLIS: i64 = 10_000;
const APPROVAL_RESERVATION_TTL_MILLIS: i64 = 10 * 60 * 1000;

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ResetSummary {
    pub tasks: usize,
    pub runs: usize,
    pub active_tasks: usize,
    pub active_runs: usize,
    pub managed_containers: usize,
    pub live_daemons: usize,
    pub repositories: Vec<String>,
    pub retained_workspaces: Vec<PathBuf>,
}

pub fn inspect_reset_state(database: &Path) -> Result<ResetSummary> {
    let connection = Connection::open_with_flags(
        database,
        OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_NO_MUTEX,
    )
    .with_context(|| {
        format!(
            "failed to open Factory database read-only {}",
            database.display()
        )
    })?;
    connection
        .busy_timeout(std::time::Duration::from_secs(5))
        .context("failed to configure reset inspection timeout")?;
    let version: i64 = connection
        .query_row("PRAGMA user_version", [], |row| row.get(0))
        .context("failed to inspect Factory schema version")?;
    if version > SCHEMA_VERSION {
        bail!(
            "Factory database schema version {version} is newer than supported version {SCHEMA_VERSION}"
        );
    }
    let count = |table: &str, condition: &str| -> Result<usize> {
        if !table_exists(&connection, table)? {
            return Ok(0);
        }
        connection
            .query_row(
                &format!("SELECT COUNT(*) FROM {table} {condition}"),
                [],
                |row| row.get(0),
            )
            .with_context(|| format!("failed to inspect Factory reset table {table}"))
    };
    let lease_cutoff = now_millis()?.saturating_sub(DAEMON_OWNER_LEASE_MILLIS);
    let live_daemons = if table_exists(&connection, "daemon_owners")? {
        let mut statement = connection
            .prepare("SELECT pid, heartbeat_at FROM daemon_owners")
            .context("failed to prepare Factory daemon inspection")?;
        statement
            .query_map([], |row| Ok((row.get::<_, u32>(0)?, row.get::<_, i64>(1)?)))
            .context("failed to inspect Factory daemons")?
            .collect::<rusqlite::Result<Vec<_>>>()
            .context("failed to read Factory daemons")?
            .into_iter()
            .filter(|(pid, heartbeat)| *heartbeat >= lease_cutoff || process_is_alive(*pid))
            .count()
    } else {
        0
    };
    let retained_workspaces = if table_exists(&connection, "task_workspaces")? {
        let mut statement = connection
            .prepare(
                "SELECT path FROM task_workspaces
                 WHERE state != 'cleaned' ORDER BY created_at, task_id",
            )
            .context("failed to prepare retained workspace inspection")?;
        statement
            .query_map([], |row| row.get::<_, String>(0).map(PathBuf::from))
            .context("failed to inspect retained workspaces")?
            .collect::<rusqlite::Result<Vec<_>>>()
            .context("failed to read retained workspaces")?
    } else {
        Vec::new()
    };
    let mut repositories = BTreeSet::new();
    for table in [
        "tasks",
        "runs",
        "trigger_observations",
        "schedule_cursors",
        "task_workspaces",
    ] {
        if !table_exists(&connection, table)? {
            continue;
        }
        let mut statement = connection
            .prepare(&format!("SELECT DISTINCT repository FROM {table}"))
            .with_context(|| format!("failed to prepare repository inspection for {table}"))?;
        repositories.extend(
            statement
                .query_map([], |row| row.get::<_, String>(0))
                .with_context(|| format!("failed to inspect repositories in {table}"))?
                .collect::<rusqlite::Result<Vec<_>>>()
                .with_context(|| format!("failed to read repositories in {table}"))?,
        );
    }
    Ok(ResetSummary {
        tasks: count("tasks", "")?,
        runs: count("runs", "")?,
        active_tasks: count("tasks", "WHERE state IN ('queued', 'running')")?,
        active_runs: count("runs", "WHERE outcome = 'running'")?,
        managed_containers: count("run_containers", "WHERE removed_at IS NULL")?,
        live_daemons,
        repositories: repositories.into_iter().collect(),
        retained_workspaces,
    })
}

fn table_exists(connection: &Connection, table: &str) -> Result<bool> {
    connection
        .query_row(
            "SELECT EXISTS(
                SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?1
             )",
            [table],
            |row| row.get(0),
        )
        .context("failed to inspect Factory database schema")
}

pub fn acquire_state_reset_lock(database: &Path) -> Result<StateLockGuard> {
    let file = open_state_lock(database)?;
    file.try_lock_exclusive().with_context(|| {
        format!(
            "Factory state is in use and cannot be reset: {}",
            database.display()
        )
    })?;
    Ok(StateLockGuard { _file: file })
}

fn acquire_shared_state_lock(database: &Path) -> Result<File> {
    let file = open_state_lock(database)?;
    file.lock_shared()
        .with_context(|| format!("failed to lock Factory state {}", database.display()))?;
    Ok(file)
}

fn open_state_lock(database: &Path) -> Result<File> {
    let path = path_with_suffix(database, ".lock");
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).with_context(|| {
            format!(
                "failed to create Factory state directory {}",
                parent.display()
            )
        })?;
    }
    match fs::symlink_metadata(&path) {
        Ok(metadata) if metadata.file_type().is_symlink() => {
            bail!(
                "Factory state lock must not be a symlink: {}",
                path.display()
            );
        }
        Ok(_) => {}
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => {
            return Err(error).with_context(|| {
                format!("failed to inspect Factory state lock {}", path.display())
            });
        }
    }
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(&path)
        .with_context(|| format!("failed to open Factory state lock {}", path.display()))?;
    let metadata = file
        .metadata()
        .with_context(|| format!("failed to inspect Factory state lock {}", path.display()))?;
    if !metadata.is_file() {
        bail!(
            "Factory state lock is not a regular file: {}",
            path.display()
        );
    }
    if fs::symlink_metadata(&path)
        .with_context(|| format!("failed to inspect Factory state lock {}", path.display()))?
        .file_type()
        .is_symlink()
    {
        bail!(
            "Factory state lock must not be a symlink: {}",
            path.display()
        );
    }
    Ok(file)
}

fn path_with_suffix(path: &Path, suffix: &str) -> PathBuf {
    let mut value = path.as_os_str().to_os_string();
    value.push(suffix);
    PathBuf::from(value)
}

pub fn validate_data_directory(data_directory: &Path) -> Result<()> {
    fs::create_dir_all(data_directory).with_context(|| {
        format!(
            "failed to create Factory data directory {}",
            data_directory.display()
        )
    })?;
    tempfile::NamedTempFile::new_in(data_directory).with_context(|| {
        format!(
            "Factory data directory is not writable: {}",
            data_directory.display()
        )
    })?;

    let database = data_directory.join(DATABASE_NAME);
    if !database.exists() {
        return Ok(());
    }
    let metadata = fs::symlink_metadata(&database)
        .with_context(|| format!("failed to inspect Factory database {}", database.display()))?;
    if !metadata.file_type().is_file() || metadata.file_type().is_symlink() {
        bail!(
            "Factory database must be a regular non-symlink file: {}",
            database.display()
        );
    }
    if metadata.permissions().readonly() {
        bail!(
            "Factory database is read-only and cannot be opened read-write: {}",
            database.display()
        );
    }
    let _state_lock = acquire_shared_state_lock(&database)?;
    let connection = Connection::open_with_flags(
        &database,
        OpenFlags::SQLITE_OPEN_READ_WRITE | OpenFlags::SQLITE_OPEN_NO_MUTEX,
    )
    .with_context(|| {
        format!(
            "Factory database cannot be opened read-write: {}",
            database.display()
        )
    })?;
    connection
        .execute_batch("BEGIN IMMEDIATE; ROLLBACK;")
        .with_context(|| {
            format!(
                "Factory database cannot acquire a write transaction: {}",
                database.display()
            )
        })?;
    Ok(())
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TaskState {
    Queued,
    Running,
    Succeeded,
    Failed,
    Cancelled,
}

impl TaskState {
    pub fn is_terminal(self) -> bool {
        matches!(self, Self::Succeeded | Self::Failed | Self::Cancelled)
    }

    fn as_str(self) -> &'static str {
        match self {
            Self::Queued => "queued",
            Self::Running => "running",
            Self::Succeeded => "succeeded",
            Self::Failed => "failed",
            Self::Cancelled => "cancelled",
        }
    }
}

impl FromStr for TaskState {
    type Err = anyhow::Error;

    fn from_str(value: &str) -> Result<Self> {
        match value {
            "queued" => Ok(Self::Queued),
            "running" => Ok(Self::Running),
            "succeeded" => Ok(Self::Succeeded),
            "failed" => Ok(Self::Failed),
            "cancelled" => Ok(Self::Cancelled),
            other => bail!("database contains unknown task state {other:?}"),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TaskIdentity {
    Ticket {
        repository: String,
        workflow: String,
        ticket: String,
        revision: String,
    },
    Scheduled {
        repository: String,
        workflow: String,
        scheduled_at: String,
    },
}

impl TaskIdentity {
    pub fn ticket(
        repository: impl Into<String>,
        workflow: impl Into<String>,
        ticket: impl Into<String>,
        revision: impl Into<String>,
    ) -> Result<Self> {
        let identity = Self::Ticket {
            repository: repository.into(),
            workflow: workflow.into(),
            ticket: ticket.into(),
            revision: revision.into(),
        };
        identity.validate()?;
        Ok(identity)
    }

    pub fn scheduled(
        repository: impl Into<String>,
        workflow: impl Into<String>,
        scheduled_at: impl Into<String>,
    ) -> Result<Self> {
        let identity = Self::Scheduled {
            repository: repository.into(),
            workflow: workflow.into(),
            scheduled_at: scheduled_at.into(),
        };
        identity.validate()?;
        Ok(identity)
    }

    fn validate(&self) -> Result<()> {
        let fields: &[(&str, &str)] = match self {
            Self::Ticket {
                repository,
                workflow,
                ticket,
                revision,
            } => &[
                ("repository", repository),
                ("workflow", workflow),
                ("ticket", ticket),
                ("revision", revision),
            ],
            Self::Scheduled {
                repository,
                workflow,
                scheduled_at,
            } => &[
                ("repository", repository),
                ("workflow", workflow),
                ("scheduled_at", scheduled_at),
            ],
        };
        for (name, value) in fields {
            if value.trim().is_empty() {
                bail!("task identity {name} must not be empty");
            }
        }
        Ok(())
    }

    fn key(&self) -> String {
        fn component(value: &str) -> String {
            format!("{}:{value}", value.len())
        }
        match self {
            Self::Ticket {
                repository,
                workflow,
                ticket,
                revision,
            } => format!(
                "ticket:{}:{}:{}:{}",
                component(repository),
                component(workflow),
                component(ticket),
                component(revision)
            ),
            Self::Scheduled {
                repository,
                workflow,
                scheduled_at,
            } => format!(
                "scheduled:{}:{}:{}",
                component(repository),
                component(workflow),
                component(scheduled_at)
            ),
        }
    }

    fn repository(&self) -> &str {
        match self {
            Self::Ticket { repository, .. } | Self::Scheduled { repository, .. } => repository,
        }
    }

    fn workflow(&self) -> &str {
        match self {
            Self::Ticket { workflow, .. } | Self::Scheduled { workflow, .. } => workflow,
        }
    }

    fn source_item(&self) -> Option<&str> {
        match self {
            Self::Ticket { ticket, .. } => Some(ticket),
            Self::Scheduled { .. } => None,
        }
    }

    fn kind(&self) -> &'static str {
        match self {
            Self::Ticket { .. } => "ticket",
            Self::Scheduled { .. } => "scheduled",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Task {
    pub id: i64,
    pub identity_key: String,
    pub kind: String,
    pub repository: String,
    pub workflow: String,
    pub source_item: Option<String>,
    pub payload: Option<String>,
    pub state: TaskState,
    pub created_at: i64,
    pub updated_at: i64,
    pub recovery_source_run_id: Option<i64>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct EnqueuedTask {
    pub task: Task,
    pub created: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ScheduleCursor {
    pub next_due_at: i64,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ObservedTicket {
    pub source_item: String,
    pub revision: String,
    pub eligible: bool,
    pub payload: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ApprovalEvidence<'a> {
    pub artifact_id: u64,
    pub label_event_id: u64,
    pub approver_id: u64,
    pub content_hash: &'a str,
    pub workflow_hash: &'a str,
    pub source_revision: &'a str,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProjectClaimEvidence<'a> {
    pub project_id: &'a str,
    pub project_item_id: &'a str,
    pub status_field_id: &'a str,
    pub expected_option_id: &'a str,
    pub active_option_id: &'a str,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TaskWorkspace {
    pub task_id: i64,
    pub kind: String,
    pub backend: String,
    pub repository: String,
    pub base_branch: String,
    pub base_sha: String,
    pub factory_branch: Option<String>,
    pub path: PathBuf,
    pub state: String,
    pub status_summary: Option<String>,
    pub created_at: i64,
    pub updated_at: i64,
    pub cleaned_at: Option<i64>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RunContainer {
    pub run_id: i64,
    pub container_id: String,
    pub instance_id: String,
    pub image_ref: String,
    pub image_id: String,
    pub limits_json: String,
    pub state: String,
    pub exit_code: Option<i32>,
    pub logs: Option<String>,
    pub created_at: i64,
    pub updated_at: i64,
    pub removed_at: Option<i64>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RunOutcome {
    Running,
    Succeeded,
    Failed,
    Cancelled,
}

impl RunOutcome {
    fn as_str(self) -> &'static str {
        match self {
            Self::Running => "running",
            Self::Succeeded => "succeeded",
            Self::Failed => "failed",
            Self::Cancelled => "cancelled",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Run {
    pub id: i64,
    pub task_id: i64,
    pub workflow: String,
    pub repository: String,
    pub source_item: Option<String>,
    pub runtime: String,
    pub started_at: i64,
    pub finished_at: Option<i64>,
    pub outcome: String,
    pub result: Option<String>,
    pub error: Option<String>,
    pub session_id: Option<String>,
    pub cancellation_requested_at: Option<i64>,
    pub owner_pid: Option<u32>,
    pub owner_id: Option<String>,
    pub process_id: Option<u32>,
    pub process_identity: Option<String>,
    pub pull_request: Option<String>,
    pub last_activity_at: i64,
    pub activity: Option<String>,
    pub working_directory: Option<String>,
    pub recovery_of: Option<i64>,
    pub recovery_attempt: u32,
    pub base_branch: Option<String>,
    pub base_sha: Option<String>,
    pub factory_branch: Option<String>,
    pub workspace_kind: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CancellationRequest {
    Requested(Run),
    AlreadyRequested(Run),
    Terminal(Run),
    OwnedElsewhere(Run),
    NotFound,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ClaimedRun {
    pub task: Task,
    pub run: Run,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RecoveryReport {
    pub recovered_run_ids: Vec<i64>,
    pub exhausted_run_ids: Vec<i64>,
}

pub struct Ledger {
    connection: Connection,
    path: PathBuf,
    _state_lock: File,
}

pub struct StateLockGuard {
    _file: File,
}

impl Ledger {
    pub fn project_claim_matches(
        &self,
        task_id: i64,
        evidence: &ProjectClaimEvidence<'_>,
    ) -> Result<bool> {
        self.connection
            .query_row(
                "SELECT EXISTS(
                    SELECT 1 FROM project_claims
                    WHERE task_id = ?1 AND project_id = ?2 AND project_item_id = ?3
                      AND status_field_id = ?4 AND expected_option_id = ?5
                      AND active_option_id = ?6
                 )",
                params![
                    task_id,
                    evidence.project_id,
                    evidence.project_item_id,
                    evidence.status_field_id,
                    evidence.expected_option_id,
                    evidence.active_option_id,
                ],
                |row| row.get(0),
            )
            .context("failed to verify durable project claim")
    }

    pub fn record_project_claim(
        &mut self,
        task_id: i64,
        evidence: &ProjectClaimEvidence<'_>,
    ) -> Result<()> {
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin project claim transaction")?;
        let running = transaction.query_row(
            "SELECT EXISTS(SELECT 1 FROM tasks WHERE id = ?1 AND state = 'running')",
            [task_id],
            |row| row.get::<_, bool>(0),
        )?;
        if !running {
            bail!("task {task_id} must be running before its project claim is recorded");
        }
        transaction.execute(
            "INSERT OR IGNORE INTO project_claims
             (task_id, project_id, project_item_id, status_field_id, expected_option_id,
              active_option_id, claimed_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![
                task_id,
                evidence.project_id,
                evidence.project_item_id,
                evidence.status_field_id,
                evidence.expected_option_id,
                evidence.active_option_id,
                now_millis()?,
            ],
        )?;
        let exact = transaction.query_row(
            "SELECT EXISTS(
                SELECT 1 FROM project_claims
                WHERE task_id = ?1 AND project_id = ?2 AND project_item_id = ?3
                  AND status_field_id = ?4 AND expected_option_id = ?5
                  AND active_option_id = ?6
             )",
            params![
                task_id,
                evidence.project_id,
                evidence.project_item_id,
                evidence.status_field_id,
                evidence.expected_option_id,
                evidence.active_option_id,
            ],
            |row| row.get::<_, bool>(0),
        )?;
        if !exact {
            bail!("task {task_id} already has a different project claim");
        }
        transaction.commit()?;
        Ok(())
    }

    pub fn approval_is_consumed(&self, artifact_id: u64) -> Result<bool> {
        self.connection
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM approval_consumptions WHERE artifact_id = ?1)",
                [artifact_id],
                |row| row.get(0),
            )
            .context("failed to check approval consumption")
    }

    pub fn task_has_consumed_approval(&self, task_id: i64) -> Result<bool> {
        self.connection
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM approval_consumptions WHERE task_id = ?1)",
                [task_id],
                |row| row.get(0),
            )
            .context("failed to check task approval consumption")
    }

    pub fn task_consumed_exact_approval(
        &self,
        task_id: i64,
        evidence: &ApprovalEvidence<'_>,
    ) -> Result<bool> {
        self.connection
            .query_row(
                "SELECT EXISTS(
                    SELECT 1 FROM approval_consumptions
                    WHERE task_id = ?1 AND artifact_id = ?2 AND label_event_id = ?3
                      AND approver_id = ?4 AND content_hash = ?5 AND workflow_hash = ?6
                 )",
                params![
                    task_id,
                    evidence.artifact_id,
                    evidence.label_event_id,
                    evidence.approver_id,
                    evidence.content_hash,
                    evidence.workflow_hash,
                ],
                |row| row.get(0),
            )
            .context("failed to verify consumed task approval")
    }

    pub fn has_active_ticket_task(&self, repository: &str, issue: u64) -> Result<bool> {
        self.connection
            .query_row(
                "SELECT EXISTS(
                    SELECT 1 FROM tasks
                    WHERE repository = ?1 AND source_item = ?2
                      AND kind = 'ticket' AND state IN ('queued', 'running')
                 )",
                params![repository, issue.to_string()],
                |row| row.get(0),
            )
            .context("failed to check active ticket task")
    }

    pub fn reserve_issue_approval(
        &mut self,
        repository: &str,
        issue: u64,
        reservation_id: &str,
    ) -> Result<()> {
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin issue approval reservation")?;
        transaction.execute(
            "DELETE FROM approval_reservations WHERE created_at < ?1",
            [now_millis()? - APPROVAL_RESERVATION_TTL_MILLIS],
        )?;
        let active = transaction.query_row(
            "SELECT EXISTS(
                SELECT 1 FROM tasks
                WHERE repository = ?1 AND source_item = ?2
                  AND kind = 'ticket' AND state IN ('queued', 'running')
             )",
            params![repository, issue.to_string()],
            |row| row.get::<_, bool>(0),
        )?;
        if active {
            bail!("issue #{issue} has active Factory work; refusing to replace its approval");
        }
        transaction
            .execute(
                "INSERT INTO approval_reservations
                 (repository, issue, reservation_id, created_at) VALUES (?1, ?2, ?3, ?4)",
                params![repository, issue, reservation_id, now_millis()?],
            )
            .with_context(|| format!("issue #{issue} already has an approval operation"))?;
        transaction.commit()?;
        Ok(())
    }

    pub fn release_issue_approval(
        &mut self,
        repository: &str,
        issue: u64,
        reservation_id: &str,
    ) -> Result<()> {
        let deleted = self.connection.execute(
            "DELETE FROM approval_reservations
             WHERE repository = ?1 AND issue = ?2 AND reservation_id = ?3",
            params![repository, issue, reservation_id],
        )?;
        if deleted != 1 {
            bail!("issue #{issue} approval reservation was lost");
        }
        Ok(())
    }

    pub fn issue_approval_is_reserved(&self, repository: &str, issue: u64) -> Result<bool> {
        self.connection
            .query_row(
                "SELECT EXISTS(
                    SELECT 1 FROM approval_reservations
                    WHERE repository = ?1 AND issue = ?2 AND created_at >= ?3
                 )",
                params![
                    repository,
                    issue,
                    now_millis()? - APPROVAL_RESERVATION_TTL_MILLIS
                ],
                |row| row.get(0),
            )
            .context("failed to check issue approval reservation")
    }

    pub fn consume_task_approval(
        &mut self,
        task_id: i64,
        evidence: &ApprovalEvidence<'_>,
    ) -> Result<()> {
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin approval consumption transaction")?;
        let running = transaction
            .query_row(
                "SELECT EXISTS(SELECT 1 FROM tasks WHERE id = ?1 AND state = 'running')",
                [task_id],
                |row| row.get::<_, bool>(0),
            )
            .context("failed to validate approval task state")?;
        if !running {
            bail!("task {task_id} must be running before approval consumption");
        }
        let inserted = transaction
            .execute(
                "INSERT OR IGNORE INTO approval_consumptions
                 (artifact_id, label_event_id, task_id, approver_id, content_hash,
                  workflow_hash, source_revision, consumed_at)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
                params![
                    evidence.artifact_id,
                    evidence.label_event_id,
                    task_id,
                    evidence.approver_id,
                    evidence.content_hash,
                    evidence.workflow_hash,
                    evidence.source_revision,
                    now_millis()?,
                ],
            )
            .context("failed to persist approval consumption")?
            == 1;
        if !inserted {
            bail!(
                "approval artifact {} or label event {} has already been consumed",
                evidence.artifact_id,
                evidence.label_event_id
            );
        }
        transaction
            .commit()
            .context("failed to commit approval consumption")
    }

    pub fn open_in(data_directory: &Path) -> Result<Self> {
        fs::create_dir_all(data_directory).with_context(|| {
            format!(
                "failed to create Factory data directory {}",
                data_directory.display()
            )
        })?;
        Self::open(&data_directory.join(DATABASE_NAME))
    }

    pub fn open(path: &Path) -> Result<Self> {
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).with_context(|| {
                format!("failed to create database directory {}", parent.display())
            })?;
        }
        let state_lock = acquire_shared_state_lock(path)?;
        let connection = Connection::open(path)
            .with_context(|| format!("failed to open SQLite database {}", path.display()))?;
        connection
            .execute_batch("PRAGMA foreign_keys = ON;")
            .context("failed to enable SQLite foreign keys")?;
        configure_wal(&connection)?;
        connection
            .busy_timeout(std::time::Duration::from_secs(5))
            .context("failed to configure SQLite busy timeout")?;
        migrate(&connection)?;
        Ok(Self {
            connection,
            path: path.to_owned(),
            _state_lock: state_lock,
        })
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    pub fn enqueue(&mut self, identity: &TaskIdentity) -> Result<EnqueuedTask> {
        self.enqueue_with_payload(identity, None)
    }

    pub fn enqueue_with_payload(
        &mut self,
        identity: &TaskIdentity,
        payload: Option<&str>,
    ) -> Result<EnqueuedTask> {
        identity.validate()?;
        let now = now_millis()?;
        let key = identity.key();
        let transaction = self
            .connection
            .transaction()
            .context("failed to begin task enqueue transaction")?;
        let inserted = transaction
            .execute(
                "INSERT OR IGNORE INTO tasks
                 (identity_key, kind, repository, workflow, source_item, payload, state, created_at, updated_at)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'queued', ?7, ?7)",
                params![
                    key,
                    identity.kind(),
                    identity.repository(),
                    identity.workflow(),
                    identity.source_item(),
                    payload,
                    now,
                ],
            )
            .context("failed to persist queued task")?
            == 1;
        let task = query_task_by_key(&transaction, &identity.key())?
            .context("task disappeared during enqueue transaction")?;
        transaction
            .commit()
            .context("failed to commit task enqueue transaction")?;
        Ok(EnqueuedTask {
            task,
            created: inserted,
        })
    }

    pub fn reconcile_ticket_poll(
        &mut self,
        repository: &str,
        workflow: &str,
        observations: &[ObservedTicket],
    ) -> Result<Vec<EnqueuedTask>> {
        if repository.trim().is_empty() || workflow.trim().is_empty() {
            bail!("poll repository and workflow must not be empty");
        }
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin ticket poll transaction")?;
        let previous = {
            let mut statement = transaction
                .prepare(
                    "SELECT source_item, eligible, revision FROM trigger_observations
                     WHERE repository = ?1 AND workflow = ?2",
                )
                .context("failed to prepare prior ticket eligibility query")?;
            statement
                .query_map(params![repository, workflow], |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        (row.get::<_, bool>(1)?, row.get::<_, String>(2)?),
                    ))
                })
                .context("failed to query prior ticket eligibility")?
                .collect::<rusqlite::Result<HashMap<_, _>>>()
                .context("failed to read prior ticket eligibility")?
        };
        transaction
            .execute(
                "UPDATE trigger_observations SET eligible = 0
                 WHERE repository = ?1 AND workflow = ?2",
                params![repository, workflow],
            )
            .context("failed to reset ticket eligibility observations")?;
        let mut enqueued = Vec::new();
        for observation in observations {
            if observation.source_item.trim().is_empty() || observation.revision.trim().is_empty() {
                bail!("observed ticket source item and revision must not be empty");
            }
            let (was_eligible, previous_revision) = previous
                .get(&observation.source_item)
                .map(|(eligible, revision)| (*eligible, Some(revision.as_str())))
                .unwrap_or((false, None));
            let approval_changed = previous_revision != Some(observation.revision.as_str());
            if observation.eligible && (!was_eligible || approval_changed) {
                let task_revision =
                    if !was_eligible && previous_revision == Some(observation.revision.as_str()) {
                        let next_task_id = transaction
                            .query_row("SELECT COALESCE(MAX(id), 0) + 1 FROM tasks", [], |row| {
                                row.get::<_, i64>(0)
                            })
                            .context("failed to allocate a ticket visit generation")?;
                        format!("{}:visit:{next_task_id}", observation.revision)
                    } else {
                        observation.revision.clone()
                    };
                let identity = TaskIdentity::ticket(
                    repository,
                    workflow,
                    &observation.source_item,
                    task_revision,
                )?;
                let active_exists = transaction
                    .query_row(
                        "SELECT EXISTS(
                            SELECT 1 FROM tasks
                            WHERE repository = ?1 AND workflow = ?2 AND source_item = ?3
                              AND state IN ('queued', 'running')
                        )",
                        params![repository, workflow, observation.source_item],
                        |row| row.get::<_, bool>(0),
                    )
                    .context("failed to check for an active ticket task")?;
                if !active_exists {
                    let key = identity.key();
                    let now = now_millis()?;
                    let inserted = transaction
                        .execute(
                        "INSERT OR IGNORE INTO tasks
                         (identity_key, kind, repository, workflow, source_item, payload, state, created_at, updated_at)
                         VALUES (?1, 'ticket', ?2, ?3, ?4, ?5, 'queued', ?6, ?6)",
                        params![
                            key,
                            repository,
                            workflow,
                            observation.source_item,
                            observation.payload,
                            now
                        ],
                        )
                        .context("failed to enqueue observed ticket")?
                        == 1;
                    let task = query_task_by_key(&transaction, &identity.key())?
                        .context("observed ticket task disappeared")?;
                    enqueued.push(EnqueuedTask {
                        task,
                        created: inserted,
                    });
                }
            }
            transaction
                .execute(
                    "INSERT INTO trigger_observations
                     (repository, workflow, source_item, revision, eligible, payload, observed_at)
                     VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)
                     ON CONFLICT(repository, workflow, source_item) DO UPDATE SET
                       revision = excluded.revision,
                       eligible = excluded.eligible,
                       payload = excluded.payload,
                       observed_at = excluded.observed_at",
                    params![
                        repository,
                        workflow,
                        observation.source_item,
                        observation.revision,
                        observation.eligible,
                        observation.payload,
                        now_millis()?
                    ],
                )
                .context("failed to persist ticket observation")?;
        }
        transaction
            .commit()
            .context("failed to commit ticket poll transaction")?;
        Ok(enqueued)
    }

    pub fn task(&self, id: i64) -> Result<Option<Task>> {
        query_task(&self.connection, id)
    }

    pub fn record_run_workspace(
        &self,
        run_id: i64,
        working_directory: &Path,
        base_branch: &str,
        base_sha: &str,
        factory_branch: Option<&str>,
        workspace_kind: &str,
    ) -> Result<()> {
        let changed = self.connection.execute(
            "UPDATE runs SET working_directory = ?1, base_branch = ?2, base_sha = ?3,
                    factory_branch = ?4, workspace_kind = ?5
             WHERE id = ?6 AND outcome = 'running'",
            params![
                working_directory.display().to_string(),
                base_branch,
                base_sha,
                factory_branch,
                workspace_kind,
                run_id
            ],
        )?;
        if changed != 1 {
            bail!("running run {run_id} disappeared before workspace persistence");
        }
        Ok(())
    }

    pub fn task_workspace(&self, task_id: i64) -> Result<Option<TaskWorkspace>> {
        self.connection
            .query_row(
                "SELECT task_id, kind, backend, repository, base_branch, base_sha, factory_branch, path,
                        state, status_summary, created_at, updated_at, cleaned_at
                 FROM task_workspaces WHERE task_id = ?1",
                [task_id],
                |row| {
                    Ok(TaskWorkspace {
                        task_id: row.get(0)?,
                        kind: row.get(1)?,
                        backend: row.get(2)?,
                        repository: row.get(3)?,
                        base_branch: row.get(4)?,
                        base_sha: row.get(5)?,
                        factory_branch: row.get(6)?,
                        path: PathBuf::from(row.get::<_, String>(7)?),
                        state: row.get(8)?,
                        status_summary: row.get(9)?,
                        created_at: row.get(10)?,
                        updated_at: row.get(11)?,
                        cleaned_at: row.get(12)?,
                    })
                },
            )
            .optional()
            .context("failed to query task workspace")
    }

    pub fn reserve_task_workspace(&mut self, workspace: &TaskWorkspace) -> Result<TaskWorkspace> {
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin workspace reservation transaction")?;
        let existing = query_task_workspace(&transaction, workspace.task_id)?;
        if let Some(existing) = existing {
            ensure_same_workspace(&existing, workspace)?;
            transaction
                .commit()
                .context("failed to finish existing workspace reservation")?;
            return Ok(existing);
        }
        if workspace.kind == "delivery" {
            let retained: i64 = transaction
                .query_row(
                    "SELECT COUNT(*) FROM task_workspaces
                     WHERE kind = 'delivery' AND state != 'cleaned'",
                    [],
                    |row| row.get(0),
                )
                .context("failed to count retained delivery workspaces")?;
            if retained >= 10 {
                bail!(
                    "Factory retains at most ten delivery worktrees; run `factory cleanup <run-id>` for one of: {}",
                    retained_delivery_run_ids(&transaction)?
                );
            }
        }
        let now = now_millis()?;
        transaction
            .execute(
                "INSERT INTO task_workspaces
                 (task_id, kind, backend, repository, base_branch, base_sha, factory_branch, path,
                  state, status_summary, created_at, updated_at, cleaned_at)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, 'preparing', NULL, ?9, ?9, NULL)",
                params![
                    workspace.task_id,
                    workspace.kind,
                    workspace.backend,
                    workspace.repository,
                    workspace.base_branch,
                    workspace.base_sha,
                    workspace.factory_branch,
                    workspace.path.display().to_string(),
                    now,
                ],
            )
            .context("failed to reserve task workspace")?;
        let reserved = query_task_workspace(&transaction, workspace.task_id)?
            .context("reserved task workspace disappeared")?;
        transaction
            .commit()
            .context("failed to commit workspace reservation")?;
        Ok(reserved)
    }

    pub fn update_task_workspace_state(
        &self,
        task_id: i64,
        state: &str,
        status_summary: Option<&str>,
    ) -> Result<()> {
        if !matches!(
            state,
            "preparing" | "ready" | "retained" | "cleanup_pending" | "cleaned"
        ) {
            bail!("invalid task workspace state {state:?}");
        }
        let now = now_millis()?;
        let cleaned_at = (state == "cleaned").then_some(now);
        let changed = self.connection.execute(
            "UPDATE task_workspaces SET state = ?1, status_summary = ?2, updated_at = ?3,
                    cleaned_at = CASE WHEN ?1 = 'cleaned' THEN ?4 ELSE cleaned_at END
             WHERE task_id = ?5",
            params![state, status_summary, now, cleaned_at, task_id],
        )?;
        if changed != 1 {
            bail!("task {task_id} has no workspace to update");
        }
        Ok(())
    }

    pub fn record_run_container(&self, container: &RunContainer) -> Result<()> {
        let running = self.connection.query_row(
            "SELECT EXISTS(SELECT 1 FROM runs WHERE id = ?1 AND outcome = 'running')",
            [container.run_id],
            |row| row.get::<_, bool>(0),
        )?;
        if !running {
            bail!(
                "run {} must be running before container persistence",
                container.run_id
            );
        }
        self.connection
            .execute(
                "INSERT INTO run_containers
                 (run_id, container_id, instance_id, image_ref, image_id, limits_json,
                  state, exit_code, logs, created_at, updated_at, removed_at)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?10, ?11)",
                params![
                    container.run_id,
                    container.container_id,
                    container.instance_id,
                    container.image_ref,
                    container.image_id,
                    container.limits_json,
                    container.state,
                    container.exit_code,
                    container.logs,
                    container.created_at,
                    container.removed_at,
                ],
            )
            .context("failed to persist run container")?;
        Ok(())
    }

    pub fn finish_run_container(
        &self,
        run_id: i64,
        state: &str,
        exit_code: Option<i32>,
        logs: Option<&str>,
        removed: bool,
    ) -> Result<()> {
        let logs = logs.map(|value| truncate_tail_utf8(value, MAX_ACTIVITY_BYTES));
        let now = now_millis()?;
        let changed = self.connection.execute(
            "UPDATE run_containers SET state = ?1, exit_code = ?2,
                    logs = COALESCE(?3, logs), updated_at = ?4,
                    removed_at = CASE WHEN ?5 THEN ?4 ELSE removed_at END
             WHERE run_id = ?6",
            params![state, exit_code, logs, now, removed, run_id],
        )?;
        if changed != 1 {
            bail!("run {run_id} has no persisted container");
        }
        Ok(())
    }

    pub fn run_container(&self, run_id: i64) -> Result<Option<RunContainer>> {
        self.connection
            .query_row(
                "SELECT run_id, container_id, instance_id, image_ref, image_id, limits_json,
                        state, exit_code, logs, created_at, updated_at, removed_at
                 FROM run_containers WHERE run_id = ?1",
                [run_id],
                |row| {
                    Ok(RunContainer {
                        run_id: row.get(0)?,
                        container_id: row.get(1)?,
                        instance_id: row.get(2)?,
                        image_ref: row.get(3)?,
                        image_id: row.get(4)?,
                        limits_json: row.get(5)?,
                        state: row.get(6)?,
                        exit_code: row.get(7)?,
                        logs: row.get(8)?,
                        created_at: row.get(9)?,
                        updated_at: row.get(10)?,
                        removed_at: row.get(11)?,
                    })
                },
            )
            .optional()
            .context("failed to read run container")
    }

    pub fn retained_delivery_workspace_count(&self) -> Result<usize> {
        let count = self.connection.query_row(
            "SELECT COUNT(*) FROM task_workspaces
             WHERE kind = 'delivery' AND state != 'cleaned'",
            [],
            |row| row.get::<_, usize>(0),
        )?;
        Ok(count)
    }

    pub fn retained_delivery_run_ids(&self) -> Result<Vec<i64>> {
        let mut statement = self.connection.prepare(
            "SELECT COALESCE(MAX(r.id), 0)
             FROM task_workspaces w
             LEFT JOIN runs r ON r.task_id = w.task_id
             WHERE w.kind = 'delivery' AND w.state != 'cleaned'
             GROUP BY w.task_id ORDER BY w.created_at, w.task_id",
        )?;
        Ok(statement
            .query_map([], |row| row.get::<_, i64>(0))?
            .collect::<rusqlite::Result<Vec<_>>>()?
            .into_iter()
            .filter(|id| *id > 0)
            .collect())
    }

    pub fn task_workspaces_in_state(&self, state: &str) -> Result<Vec<TaskWorkspace>> {
        let mut statement = self.connection.prepare(
            "SELECT task_id, kind, backend, repository, base_branch, base_sha, factory_branch, path,
                    state, status_summary, created_at, updated_at, cleaned_at
             FROM task_workspaces WHERE state = ?1 ORDER BY task_id",
        )?;
        statement
            .query_map([state], |row| {
                Ok(TaskWorkspace {
                    task_id: row.get(0)?,
                    kind: row.get(1)?,
                    backend: row.get(2)?,
                    repository: row.get(3)?,
                    base_branch: row.get(4)?,
                    base_sha: row.get(5)?,
                    factory_branch: row.get(6)?,
                    path: PathBuf::from(row.get::<_, String>(7)?),
                    state: row.get(8)?,
                    status_summary: row.get(9)?,
                    created_at: row.get(10)?,
                    updated_at: row.get(11)?,
                    cleaned_at: row.get(12)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()
            .context("failed to read task workspaces")
    }

    pub fn active_task_workspaces(&self) -> Result<Vec<TaskWorkspace>> {
        let mut statement = self.connection.prepare(
            "SELECT task_id, kind, backend, repository, base_branch, base_sha, factory_branch, path,
                    state, status_summary, created_at, updated_at, cleaned_at
             FROM task_workspaces WHERE state != 'cleaned' ORDER BY task_id",
        )?;
        statement
            .query_map([], |row| {
                Ok(TaskWorkspace {
                    task_id: row.get(0)?,
                    kind: row.get(1)?,
                    backend: row.get(2)?,
                    repository: row.get(3)?,
                    base_branch: row.get(4)?,
                    base_sha: row.get(5)?,
                    factory_branch: row.get(6)?,
                    path: PathBuf::from(row.get::<_, String>(7)?),
                    state: row.get(8)?,
                    status_summary: row.get(9)?,
                    created_at: row.get(10)?,
                    updated_at: row.get(11)?,
                    cleaned_at: row.get(12)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()
            .context("failed to read active task workspaces")
    }

    pub fn claim_next(&mut self) -> Result<Option<Task>> {
        self.claim_next_matching(None)
    }

    pub fn claim_next_for_repositories(&mut self, repositories: &[String]) -> Result<Option<Task>> {
        if repositories.is_empty() {
            return Ok(None);
        }
        let repositories = serde_json::to_string(repositories)
            .context("failed to encode claim repository filter")?;
        self.claim_next_matching(Some(&repositories))
    }

    pub fn initialize_schedule_cursor(
        &mut self,
        repository: &str,
        workflow: &str,
        fingerprint: &str,
        next_due_at: i64,
        startup_at: i64,
        owner_id: &str,
    ) -> Result<ScheduleCursor> {
        if repository.trim().is_empty()
            || workflow.trim().is_empty()
            || fingerprint.trim().is_empty()
            || owner_id.trim().is_empty()
        {
            bail!("schedule repository, workflow, fingerprint, and owner must not be empty");
        }
        if next_due_at <= startup_at {
            bail!("initialized schedule occurrence must be after startup");
        }
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin schedule initialization transaction")?;
        let prior = transaction
            .query_row(
                "SELECT fingerprint, next_due_at FROM schedule_cursors
                 WHERE repository = ?1 AND workflow = ?2",
                params![repository, workflow],
                |row| Ok((row.get::<_, String>(0)?, row.get::<_, i64>(1)?)),
            )
            .optional()
            .context("failed to query schedule cursor")?;
        let lease_cutoff = now_millis()? - DAEMON_OWNER_LEASE_MILLIS;
        let live_owner_fingerprints = {
            let mut statement = transaction
                .prepare(
                    "SELECT DISTINCT schedules.fingerprint, owners.pid
                     FROM schedule_owners schedules
                     JOIN daemon_owners owners ON owners.owner_id = schedules.owner_id
                     WHERE schedules.repository = ?1 AND schedules.workflow = ?2
                       AND owners.owner_id != ?3 AND owners.heartbeat_at >= ?4",
                )
                .context("failed to prepare live schedule owner query")?;
            statement
                .query_map(
                    params![repository, workflow, owner_id, lease_cutoff],
                    |row| Ok((row.get::<_, String>(0)?, row.get::<_, u32>(1)?)),
                )
                .context("failed to query live schedule owners")?
                .collect::<rusqlite::Result<Vec<_>>>()
                .context("failed to read live schedule owners")?
                .into_iter()
                .filter_map(|(fingerprint, pid)| process_is_alive(pid).then_some(fingerprint))
                .collect::<Vec<_>>()
        };
        let other_matching_owner = live_owner_fingerprints
            .iter()
            .any(|owner_fingerprint| owner_fingerprint == fingerprint);
        let current_owner_matches = transaction
            .query_row(
                "SELECT EXISTS(
                    SELECT 1 FROM schedule_owners schedules
                    JOIN daemon_owners owners ON owners.owner_id = schedules.owner_id
                    WHERE schedules.repository = ?1 AND schedules.workflow = ?2
                      AND schedules.owner_id = ?3 AND schedules.fingerprint = ?4
                      AND owners.heartbeat_at >= ?5
                 )",
                params![repository, workflow, owner_id, fingerprint, lease_cutoff],
                |row| row.get::<_, bool>(0),
            )
            .context("failed to query current schedule ownership")?;
        if let Some((prior_fingerprint, _)) = &prior
            && prior_fingerprint != fingerprint
            && live_owner_fingerprints
                .iter()
                .any(|owner_fingerprint| owner_fingerprint == prior_fingerprint)
        {
            bail!(
                "schedule {repository}/{workflow} has live owner using different fingerprint {prior_fingerprint:?}"
            );
        }
        let resolved = match prior {
            Some((prior_fingerprint, prior_due))
                if prior_fingerprint == fingerprint
                    && (prior_due > startup_at
                        || other_matching_owner
                        || current_owner_matches) =>
            {
                prior_due
            }
            _ => {
                transaction
                    .execute(
                        "INSERT INTO schedule_cursors
                         (repository, workflow, fingerprint, next_due_at, updated_at)
                         VALUES (?1, ?2, ?3, ?4, ?5)
                         ON CONFLICT(repository, workflow) DO UPDATE SET
                           fingerprint = excluded.fingerprint,
                           next_due_at = excluded.next_due_at,
                           updated_at = excluded.updated_at",
                        params![
                            repository,
                            workflow,
                            fingerprint,
                            next_due_at,
                            now_millis()?
                        ],
                    )
                    .context("failed to initialize schedule cursor")?;
                next_due_at
            }
        };
        transaction
            .execute(
                "INSERT OR REPLACE INTO schedule_owners
                 (repository, workflow, owner_id, fingerprint)
                 VALUES (?1, ?2, ?3, ?4)",
                params![repository, workflow, owner_id, fingerprint],
            )
            .context("failed to register schedule owner")?;
        transaction
            .commit()
            .context("failed to commit schedule initialization")?;
        Ok(ScheduleCursor {
            next_due_at: resolved,
        })
    }

    pub fn enqueue_scheduled_occurrence(
        &mut self,
        identity: &TaskIdentity,
        payload: &str,
        expected_fingerprint: &str,
        expected_due_at: i64,
        next_due_at: i64,
    ) -> Result<Option<EnqueuedTask>> {
        let TaskIdentity::Scheduled {
            repository,
            workflow,
            ..
        } = identity
        else {
            bail!("scheduled occurrence requires a scheduled task identity");
        };
        identity.validate()?;
        if next_due_at <= expected_due_at {
            bail!("next scheduled occurrence must follow the due occurrence");
        }
        let mut payload = serde_json::from_str::<serde_json::Value>(payload)
            .context("scheduled occurrence payload is not valid JSON")?;
        let payload = payload
            .as_object_mut()
            .context("scheduled occurrence payload must be a JSON object")?;
        payload.insert(
            "schedule_fingerprint".to_owned(),
            serde_json::Value::String(expected_fingerprint.to_owned()),
        );
        let payload = serde_json::to_string(payload)
            .context("failed to encode scheduled occurrence payload")?;
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin scheduled occurrence transaction")?;
        let cursor = transaction
            .query_row(
                "SELECT fingerprint, next_due_at FROM schedule_cursors
                 WHERE repository = ?1 AND workflow = ?2",
                params![repository, workflow],
                |row| Ok((row.get::<_, String>(0)?, row.get::<_, i64>(1)?)),
            )
            .optional()
            .context("failed to read due schedule cursor")?;
        if cursor != Some((expected_fingerprint.to_owned(), expected_due_at)) {
            transaction
                .commit()
                .context("failed to finish superseded schedule occurrence")?;
            return Ok(None);
        }
        let now = now_millis()?;
        let inserted = transaction
            .execute(
                "INSERT OR IGNORE INTO tasks
                 (identity_key, kind, repository, workflow, source_item, payload, state, created_at, updated_at)
                 VALUES (?1, 'scheduled', ?2, ?3, NULL, ?4, 'queued', ?5, ?5)",
                params![identity.key(), repository, workflow, payload, now],
            )
            .context("failed to enqueue scheduled occurrence")?
            == 1;
        let task = query_task_by_key(&transaction, &identity.key())?
            .context("scheduled occurrence task disappeared")?;
        let changed = transaction
            .execute(
                "UPDATE schedule_cursors SET next_due_at = ?1, updated_at = ?2
                 WHERE repository = ?3 AND workflow = ?4
                   AND fingerprint = ?5 AND next_due_at = ?6",
                params![
                    next_due_at,
                    now,
                    repository,
                    workflow,
                    expected_fingerprint,
                    expected_due_at
                ],
            )
            .context("failed to advance schedule cursor")?;
        if changed != 1 {
            bail!("scheduled occurrence cursor changed during enqueue");
        }
        transaction
            .commit()
            .context("failed to commit scheduled occurrence")?;
        Ok(Some(EnqueuedTask {
            task,
            created: inserted,
        }))
    }

    pub fn claim_and_start_run(
        &mut self,
        available_repositories: &[String],
        workflow_runtimes: &HashMap<(String, String, String), String>,
        owner_id: &str,
        owner_pid: u32,
    ) -> Result<Option<ClaimedRun>> {
        let working_directories = available_repositories
            .iter()
            .map(|repository| (repository.clone(), repository.clone()))
            .collect();
        self.claim_and_start_run_with_workdirs(
            available_repositories,
            workflow_runtimes,
            owner_id,
            owner_pid,
            &working_directories,
        )
    }

    pub fn claim_and_start_run_with_workdirs(
        &mut self,
        available_repositories: &[String],
        workflow_runtimes: &HashMap<(String, String, String), String>,
        owner_id: &str,
        owner_pid: u32,
        working_directories: &HashMap<String, String>,
    ) -> Result<Option<ClaimedRun>> {
        self.claim_and_start_run_with_workdirs_filtered(
            available_repositories,
            workflow_runtimes,
            owner_id,
            owner_pid,
            working_directories,
            true,
        )
    }

    pub fn claim_and_start_run_with_workdirs_filtered(
        &mut self,
        available_repositories: &[String],
        workflow_runtimes: &HashMap<(String, String, String), String>,
        owner_id: &str,
        owner_pid: u32,
        working_directories: &HashMap<String, String>,
        allow_new_ticket_tasks: bool,
    ) -> Result<Option<ClaimedRun>> {
        if available_repositories.is_empty() {
            return Ok(None);
        }
        let available = available_repositories
            .iter()
            .collect::<std::collections::HashSet<_>>();
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin atomic task and run claim")?;
        let now = now_millis()?;
        let owner_is_live = transaction
            .query_row(
                "SELECT EXISTS(
                    SELECT 1 FROM daemon_owners
                    WHERE owner_id = ?1 AND pid = ?2 AND heartbeat_at >= ?3
                 )",
                params![
                    owner_id,
                    owner_pid,
                    now.saturating_sub(DAEMON_OWNER_LEASE_MILLIS)
                ],
                |row| row.get::<_, bool>(0),
            )
            .context("failed to validate task claim owner lease")?;
        if !owner_is_live {
            bail!("daemon owner {owner_id:?} has no live lease for task claims");
        }
        let schedule_ownership = {
            let mut statement = transaction
                .prepare(
                    "SELECT owners.repository, owners.workflow, owners.fingerprint
                     FROM schedule_owners owners
                     JOIN schedule_cursors cursors
                       ON cursors.repository = owners.repository
                      AND cursors.workflow = owners.workflow
                      AND cursors.fingerprint = owners.fingerprint
                     WHERE owners.owner_id = ?1",
                )
                .context("failed to prepare schedule ownership query")?;
            statement
                .query_map([owner_id], |row| {
                    Ok(((row.get(0)?, row.get(1)?), row.get(2)?))
                })
                .context("failed to query schedule ownership")?
                .collect::<rusqlite::Result<HashMap<_, _>>>()
                .context("failed to read schedule ownership")?
        };
        let tasks_with_workspaces = {
            let mut statement = transaction
                .prepare("SELECT task_id FROM task_workspaces WHERE state != 'cleaned'")
                .context("failed to prepare workspace ownership query")?;
            statement
                .query_map([], |row| row.get::<_, i64>(0))
                .context("failed to query workspace ownership")?
                .collect::<rusqlite::Result<std::collections::HashSet<_>>>()
                .context("failed to read workspace ownership")?
        };
        let running_ticket_sources = {
            let mut statement = transaction
                .prepare(
                    "SELECT repository, source_item FROM tasks
                     WHERE kind = 'ticket' AND state = 'running' AND source_item IS NOT NULL",
                )
                .context("failed to prepare running ticket source query")?;
            statement
                .query_map([], |row| {
                    Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
                })
                .context("failed to query running ticket sources")?
                .collect::<rusqlite::Result<std::collections::HashSet<_>>>()
                .context("failed to read running ticket sources")?
        };
        let candidates = {
            let mut statement = transaction
                .prepare(
                    "SELECT * FROM tasks
                     WHERE state = 'queued'
                     ORDER BY created_at, id",
                )
                .context("failed to prepare task claim query")?;
            statement
                .query_map([], row_to_task)
                .context("failed to query queued tasks")?
                .collect::<rusqlite::Result<Vec<_>>>()
                .context("failed to read queued tasks")?
        };
        let Some(task) = candidates.into_iter().find(|task| {
            if task.kind == "ticket"
                && task.source_item.as_ref().is_some_and(|source_item| {
                    running_ticket_sources.contains(&(task.repository.clone(), source_item.clone()))
                })
            {
                return false;
            }
            if task.kind == "ticket"
                && !allow_new_ticket_tasks
                && !tasks_with_workspaces.contains(&task.id)
            {
                return false;
            }
            if !available.contains(&task.repository)
                || !workflow_runtimes.contains_key(&(
                    task.repository.clone(),
                    task.workflow.clone(),
                    task.kind.clone(),
                ))
            {
                return false;
            }
            if task.kind != "scheduled" {
                return true;
            }
            let task_fingerprint = task
                .payload
                .as_deref()
                .and_then(|payload| serde_json::from_str::<serde_json::Value>(payload).ok())
                .and_then(|payload| {
                    payload
                        .get("schedule_fingerprint")
                        .and_then(serde_json::Value::as_str)
                        .map(str::to_owned)
                });
            matches!(
                (
                    task_fingerprint.as_deref(),
                    schedule_ownership
                        .get(&(task.repository.clone(), task.workflow.clone()))
                        .map(String::as_str),
                ),
                (Some(task_fingerprint), Some(owner_fingerprint))
                    if task_fingerprint == owner_fingerprint
            )
        }) else {
            transaction
                .commit()
                .context("failed to finish empty task claim")?;
            return Ok(None);
        };
        let runtime = workflow_runtimes
            .get(&(
                task.repository.clone(),
                task.workflow.clone(),
                task.kind.clone(),
            ))
            .context("claimed workflow runtime disappeared")?;
        let changed = transaction
            .execute(
                "UPDATE tasks SET state = 'running', updated_at = ?1
                 WHERE id = ?2 AND state = 'queued'",
                params![now, task.id],
            )
            .context("failed to claim queued ticket task")?;
        if changed != 1 {
            bail!("queued task {} could not be claimed atomically", task.id);
        }
        let recovery_source = task.recovery_source_run_id;
        let recovery_attempt = recovery_source
            .map(|run_id| {
                transaction.query_row(
                    "SELECT recovery_attempt + 1 FROM runs WHERE id = ?1",
                    [run_id],
                    |row| row.get::<_, u32>(0),
                )
            })
            .transpose()
            .context("failed to resolve recovery attempt")?
            .unwrap_or(0);
        let working_directory = working_directories
            .get(&task.repository)
            .context("claimed repository working directory disappeared")?;
        transaction
            .execute(
                "INSERT INTO runs
                 (task_id, workflow, repository, source_item, runtime, started_at, outcome,
                  owner_pid, owner_id, last_activity_at, working_directory, recovery_of, recovery_attempt)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'running', ?7, ?8, ?6, ?9, ?10, ?11)",
                params![
                    task.id,
                    task.workflow,
                    task.repository,
                    task.source_item,
                    runtime,
                    now,
                    owner_pid,
                    owner_id,
                    working_directory,
                    recovery_source,
                    recovery_attempt,
                ],
            )
            .context("failed to create run in task claim transaction")?;
        let run_id = transaction.last_insert_rowid();
        let task = query_task(&transaction, task.id)?.context("claimed task disappeared")?;
        let run = query_run(&transaction, run_id)?.context("claimed run disappeared")?;
        transaction
            .execute(
                "UPDATE tasks SET recovery_source_run_id = NULL WHERE id = ?1",
                [task.id],
            )
            .context("failed to clear claimed recovery source")?;
        transaction
            .commit()
            .context("failed to commit atomic task and run claim")?;
        Ok(Some(ClaimedRun { task, run }))
    }

    fn claim_next_matching(&mut self, repositories: Option<&str>) -> Result<Option<Task>> {
        let now = now_millis()?;
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin atomic task claim")?;
        let id = transaction
            .query_row(
                "SELECT id FROM tasks
                 WHERE state = 'queued'
                   AND (?1 IS NULL OR repository IN (SELECT value FROM json_each(?1)))
                 ORDER BY created_at, id LIMIT 1",
                [repositories],
                |row| row.get::<_, i64>(0),
            )
            .optional()
            .context("failed to select queued task for claim")?;
        let Some(id) = id else {
            transaction
                .commit()
                .context("failed to finish empty claim")?;
            return Ok(None);
        };
        let changed = transaction
            .execute(
                "UPDATE tasks SET state = 'running', updated_at = ?1
                 WHERE id = ?2 AND state = 'queued'",
                params![now, id],
            )
            .context("failed to claim queued task")?;
        if changed != 1 {
            bail!("queued task {id} could not be claimed atomically");
        }
        let task = query_task(&transaction, id)?.context("claimed task disappeared")?;
        transaction
            .commit()
            .context("failed to commit atomic task claim")?;
        Ok(Some(task))
    }

    pub fn latest_session(
        &self,
        repository: &str,
        workflow: &str,
        source_item: Option<&str>,
    ) -> Result<Option<String>> {
        self.connection
            .query_row(
                "SELECT session_id FROM runs
                 WHERE repository = ?1 AND workflow = ?2
                   AND source_item IS ?3 AND session_id IS NOT NULL
                 ORDER BY id DESC LIMIT 1",
                params![repository, workflow, source_item],
                |row| row.get(0),
            )
            .optional()
            .context("failed to query prior agent session")
    }

    pub fn latest_successful_scheduled_run_finished_at(
        &self,
        repository: &str,
        workflow: &str,
    ) -> Result<Option<i64>> {
        self.connection
            .query_row(
                "SELECT runs.finished_at FROM runs
                 JOIN tasks ON tasks.id = runs.task_id
                 WHERE runs.repository = ?1 AND runs.workflow = ?2
                   AND tasks.kind = 'scheduled'
                   AND runs.outcome = 'succeeded' AND runs.finished_at IS NOT NULL
                 ORDER BY runs.finished_at DESC, runs.id DESC LIMIT 1",
                params![repository, workflow],
                |row| row.get(0),
            )
            .optional()
            .context("failed to query previous successful workflow run")
    }

    pub fn latest_pull_request_for_task(&self, task_id: i64) -> Result<Option<String>> {
        self.connection
            .query_row(
                "SELECT pull_request FROM runs
                 WHERE task_id = ?1 AND pull_request IS NOT NULL
                 ORDER BY id DESC LIMIT 1",
                [task_id],
                |row| row.get(0),
            )
            .optional()
            .context("failed to query recovery pull-request context")
    }

    pub fn tasks(&self) -> Result<Vec<Task>> {
        let mut statement = self
            .connection
            .prepare("SELECT * FROM tasks ORDER BY id")
            .context("failed to prepare tasks query")?;
        statement
            .query_map([], row_to_task)
            .context("failed to query tasks")?
            .collect::<rusqlite::Result<Vec<_>>>()
            .context("failed to read tasks")
    }

    pub fn start_run(&mut self, task_id: i64, runtime: &str) -> Result<Run> {
        if runtime.trim().is_empty() {
            bail!("run runtime must not be empty");
        }
        let transaction = self
            .connection
            .transaction()
            .context("failed to begin run transaction")?;
        let task = query_task(&transaction, task_id)?
            .with_context(|| format!("task {task_id} does not exist"))?;
        if task.state != TaskState::Running {
            bail!("task {task_id} must be running before a run can start");
        }
        transaction
            .execute(
                "INSERT INTO runs
                 (task_id, workflow, repository, source_item, runtime, started_at, outcome, last_activity_at)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'running', ?6)",
                params![
                    task_id,
                    task.workflow,
                    task.repository,
                    task.source_item,
                    runtime.trim(),
                    now_millis()?
                ],
            )
            .context("failed to persist run attempt")?;
        let id = transaction.last_insert_rowid();
        let run = query_run(&transaction, id)?.context("new run disappeared")?;
        transaction.commit().context("failed to commit run start")?;
        Ok(run)
    }

    pub fn finish_run_and_task(
        &mut self,
        id: i64,
        outcome: RunOutcome,
        result: Option<&str>,
        error: Option<&str>,
        session_id: Option<&str>,
    ) -> Result<Run> {
        self.finish_run_and_task_with_recovery(id, outcome, result, error, session_id, true)
    }

    pub fn finish_run_and_task_terminal(
        &mut self,
        id: i64,
        outcome: RunOutcome,
        result: Option<&str>,
        error: Option<&str>,
        session_id: Option<&str>,
    ) -> Result<Run> {
        self.finish_run_and_task_with_recovery(id, outcome, result, error, session_id, false)
    }

    pub fn fail_prelaunch_and_requeue(&mut self, id: i64, error: &str) -> Result<Run> {
        let error = truncate_utf8(
            &crate::inspection::sanitize_for_storage(error),
            MAX_ERROR_BYTES,
        );
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin prelaunch recovery transaction")?;
        let (task_id, recovery_attempt) = transaction
            .query_row(
                "SELECT task_id, recovery_attempt FROM runs
                 WHERE id = ?1 AND outcome = 'running' AND process_id IS NULL",
                [id],
                |row| Ok((row.get::<_, i64>(0)?, row.get::<_, u32>(1)?)),
            )
            .optional()?
            .with_context(|| format!("run {id} is missing, terminal, or already launched"))?;
        let now = now_millis()?;
        let changed = transaction.execute(
            "UPDATE runs SET outcome = 'failed', finished_at = ?1, error = ?2
             WHERE id = ?3 AND outcome = 'running'",
            params![now, error, id],
        )?;
        if changed != 1 {
            bail!("run {id} changed during prelaunch recovery");
        }
        let changed = if recovery_attempt < MAX_RECOVERY_ATTEMPTS {
            transaction.execute(
                "UPDATE tasks SET state = 'queued', updated_at = ?1,
                        recovery_source_run_id = ?2
                 WHERE id = ?3 AND state = 'running'",
                params![now, id, task_id],
            )?
        } else {
            transaction.execute(
                "UPDATE tasks SET state = 'failed', updated_at = ?1
                 WHERE id = ?2 AND state = 'running'",
                params![now, task_id],
            )?
        };
        if changed != 1 {
            bail!("task {task_id} changed during prelaunch recovery");
        }
        let run = query_run(&transaction, id)?.context("failed prelaunch run disappeared")?;
        transaction
            .commit()
            .context("failed to commit prelaunch recovery")?;
        Ok(run)
    }

    fn finish_run_and_task_with_recovery(
        &mut self,
        id: i64,
        outcome: RunOutcome,
        result: Option<&str>,
        error: Option<&str>,
        session_id: Option<&str>,
        allow_recovery: bool,
    ) -> Result<Run> {
        if outcome == RunOutcome::Running {
            bail!("finish_run_and_task requires a terminal outcome");
        }
        let result = result.map(|value| {
            truncate_utf8(
                &crate::inspection::sanitize_for_storage(value),
                MAX_RESULT_BYTES,
            )
        });
        let mut error = error.map(|value| {
            truncate_utf8(
                &crate::inspection::sanitize_for_storage(value),
                MAX_ERROR_BYTES,
            )
        });
        let session_id = session_id.map(|value| truncate_utf8(value, MAX_SESSION_ID_BYTES));
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin run completion transaction")?;
        let (task_id, cancellation_requested, recovery_attempt, process_started) = transaction
            .query_row(
                "SELECT task_id, cancellation_requested_at IS NOT NULL, recovery_attempt,
                        process_id IS NOT NULL
                 FROM runs WHERE id = ?1 AND outcome = 'running'",
                [id],
                |row| {
                    Ok((
                        row.get::<_, i64>(0)?,
                        row.get::<_, bool>(1)?,
                        row.get::<_, u32>(2)?,
                        row.get::<_, bool>(3)?,
                    ))
                },
            )
            .optional()
            .context("failed to resolve active run task")?
            .with_context(|| format!("run {id} is missing or already terminal"))?;
        let outcome = if cancellation_requested {
            if error.is_none() {
                error = Some("Cancellation requested by local operator".to_owned());
            }
            RunOutcome::Cancelled
        } else {
            outcome
        };
        let changed = transaction
            .execute(
                "UPDATE runs SET finished_at = ?1, outcome = ?2, result = ?3, error = ?4,
                 session_id = COALESCE(?5, session_id) WHERE id = ?6 AND outcome = 'running'",
                params![
                    now_millis()?,
                    outcome.as_str(),
                    result,
                    error,
                    session_id,
                    id
                ],
            )
            .context("failed to record terminal run outcome")?;
        if changed != 1 {
            bail!("run {id} is missing or already terminal");
        }
        let retry_recovery = allow_recovery
            && outcome == RunOutcome::Failed
            && process_started
            && recovery_attempt < MAX_RECOVERY_ATTEMPTS
            && !cancellation_requested;
        let task_state = match outcome {
            RunOutcome::Succeeded => TaskState::Succeeded,
            RunOutcome::Failed => TaskState::Failed,
            RunOutcome::Cancelled => TaskState::Cancelled,
            RunOutcome::Running => unreachable!(),
        };
        let changed = if retry_recovery {
            transaction.execute(
                "UPDATE tasks SET state = 'queued', updated_at = ?1, recovery_source_run_id = ?2
                 WHERE id = ?3 AND state = 'running'",
                params![now_millis()?, id, task_id],
            )
        } else {
            transaction.execute(
                "UPDATE tasks SET state = ?1, updated_at = ?2
                 WHERE id = ?3 AND state = 'running'",
                params![task_state.as_str(), now_millis()?, task_id],
            )
        }
        .context("failed to record terminal task state")?;
        if changed != 1 {
            bail!("task {task_id} is not running; run completion was not recorded");
        }
        let run = query_run(&transaction, id)?.context("completed run disappeared")?;
        transaction
            .commit()
            .context("failed to commit run completion")?;
        Ok(run)
    }

    pub fn runs_for_task(&self, task_id: i64) -> Result<Vec<Run>> {
        let mut statement = self
            .connection
            .prepare("SELECT * FROM runs WHERE task_id = ?1 ORDER BY id")
            .context("failed to prepare run history query")?;
        let runs = statement
            .query_map([task_id], row_to_run)
            .context("failed to query run history")?
            .collect::<rusqlite::Result<Vec<_>>>()
            .context("failed to read run history")?;
        Ok(runs)
    }

    pub fn runs(&self, workflow: Option<&str>) -> Result<Vec<Run>> {
        let mut statement = self
            .connection
            .prepare(
                "SELECT * FROM runs
                 WHERE (?1 IS NULL OR workflow = ?1)
                 ORDER BY id",
            )
            .context("failed to prepare runs query")?;
        statement
            .query_map([workflow], row_to_run)
            .context("failed to query runs")?
            .collect::<rusqlite::Result<Vec<_>>>()
            .context("failed to read runs")
    }

    pub fn run(&self, id: i64) -> Result<Option<Run>> {
        query_run(&self.connection, id)
    }

    pub fn request_run_cancellation(&mut self, id: i64) -> Result<CancellationRequest> {
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin cancellation request transaction")?;
        let Some(run) = query_run(&transaction, id)? else {
            transaction
                .commit()
                .context("failed to finish missing cancellation request")?;
            return Ok(CancellationRequest::NotFound);
        };
        if run.outcome != "running" {
            transaction
                .commit()
                .context("failed to finish terminal cancellation request")?;
            return Ok(CancellationRequest::Terminal(run));
        }
        if run.cancellation_requested_at.is_some() {
            transaction
                .commit()
                .context("failed to finish repeated cancellation request")?;
            return Ok(CancellationRequest::AlreadyRequested(run));
        }
        let owner_is_live = match (&run.owner_id, run.owner_pid) {
            (Some(owner_id), Some(owner_pid)) => {
                let lease_cutoff = now_millis()?.saturating_sub(DAEMON_OWNER_LEASE_MILLIS);
                transaction
                    .query_row(
                        "SELECT EXISTS(
                            SELECT 1 FROM daemon_owners
                            WHERE owner_id = ?1 AND pid = ?2 AND heartbeat_at >= ?3
                        )",
                        params![owner_id, owner_pid, lease_cutoff],
                        |row| row.get::<_, bool>(0),
                    )
                    .context("failed to validate run owner lease")?
                    && process_is_alive(owner_pid)
            }
            _ => false,
        };
        if !owner_is_live {
            transaction
                .commit()
                .context("failed to finish unowned cancellation request")?;
            return Ok(CancellationRequest::OwnedElsewhere(run));
        }
        transaction
            .execute(
                "UPDATE runs SET cancellation_requested_at = ?1
                 WHERE id = ?2 AND outcome = 'running' AND cancellation_requested_at IS NULL",
                params![now_millis()?, id],
            )
            .context("failed to persist cancellation request")?;
        let run = query_run(&transaction, id)?.context("cancelled run disappeared")?;
        transaction
            .commit()
            .context("failed to commit cancellation request")?;
        Ok(CancellationRequest::Requested(run))
    }

    pub fn cancellation_requested(&self, id: i64) -> Result<bool> {
        self.connection
            .query_row(
                "SELECT cancellation_requested_at IS NOT NULL
                 FROM runs WHERE id = ?1 AND outcome = 'running'",
                [id],
                |row| row.get(0),
            )
            .optional()
            .context("failed to query cancellation request")
            .map(|requested| requested.unwrap_or(false))
    }

    pub fn observe_run(
        &mut self,
        id: i64,
        process_id: Option<u32>,
        process_identity: Option<&str>,
        session_id: Option<&str>,
        pull_request: Option<&str>,
        activity: Option<&str>,
    ) -> Result<()> {
        if process_id == Some(0) {
            bail!("run process ID must be positive");
        }
        let session_id = session_id.map(|value| truncate_utf8(value, MAX_SESSION_ID_BYTES));
        let pull_request = pull_request.map(|value| truncate_utf8(value, 2048));
        let activity = activity.map(|value| {
            truncate_tail_utf8(
                &crate::inspection::sanitize_for_storage(value),
                MAX_ACTIVITY_BYTES,
            )
        });
        let changed = self
            .connection
            .execute(
                "UPDATE runs SET
               process_id = COALESCE(?1, process_id),
               process_identity = COALESCE(?2, process_identity),
               session_id = COALESCE(?3, session_id),
               pull_request = COALESCE(?4, pull_request),
               activity = COALESCE(?5, activity),
               last_activity_at = ?6
             WHERE id = ?7 AND outcome = 'running'",
                params![
                    process_id,
                    process_identity,
                    session_id,
                    pull_request,
                    activity,
                    now_millis()?,
                    id
                ],
            )
            .context("failed to persist runtime activity")?;
        if changed != 1 {
            bail!("run {id} is missing or already terminal");
        }
        Ok(())
    }

    pub fn reset_run_runtime_observation(&mut self, id: i64) -> Result<()> {
        let changed = self
            .connection
            .execute(
                "UPDATE runs SET process_id = NULL, process_identity = NULL,
                 session_id = NULL, activity = NULL, last_activity_at = ?1
                 WHERE id = ?2 AND outcome = 'running'",
                params![now_millis()?, id],
            )
            .context("failed to reset failed session-resume observation")?;
        if changed != 1 {
            bail!("run {id} is missing or already terminal");
        }
        Ok(())
    }

    pub fn recover_orphaned_runs(&mut self) -> Result<RecoveryReport> {
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin orphan recovery transaction")?;
        let active = {
            let mut statement = transaction
                .prepare("SELECT * FROM runs WHERE outcome = 'running' ORDER BY id")
                .context("failed to prepare active run recovery query")?;
            statement
                .query_map([], row_to_run)
                .context("failed to query active runs for recovery")?
                .collect::<rusqlite::Result<Vec<_>>>()
                .context("failed to read active runs for recovery")?
        };
        let mut recovered_run_ids = Vec::new();
        let mut exhausted_run_ids = Vec::new();
        let lease_cutoff = now_millis()?.saturating_sub(DAEMON_OWNER_LEASE_MILLIS);
        for run in active {
            let owner_is_live = match (&run.owner_id, run.owner_pid) {
                (Some(owner_id), Some(owner_pid)) => {
                    transaction
                        .query_row(
                            "SELECT EXISTS(SELECT 1 FROM daemon_owners
                     WHERE owner_id = ?1 AND pid = ?2 AND heartbeat_at >= ?3)",
                            params![owner_id, owner_pid, lease_cutoff],
                            |row| row.get::<_, bool>(0),
                        )
                        .context("failed to validate orphan owner lease")?
                        && process_is_alive(owner_pid)
                }
                _ => false,
            };
            if owner_is_live {
                continue;
            }
            if let (Some(process_id), Some(recorded_identity)) =
                (run.process_id, run.process_identity.as_deref())
                && process_id > 0
                && crate::runtime::process_identity(process_id).as_deref()
                    == Some(recorded_identity)
            {
                terminate_orphaned_process_group(process_id).with_context(|| {
                    format!("failed to stop orphaned process tree for run {}", run.id)
                })?;
            }
            let now = now_millis()?;
            let cancellation_requested = run.cancellation_requested_at.is_some();
            let outcome = if cancellation_requested {
                "cancelled"
            } else {
                "failed"
            };
            let error = if cancellation_requested {
                "Factory completed a durable cancellation after its owning daemon stopped"
            } else {
                "Factory detected an interrupted run without a live owned process"
            };
            transaction
                .execute(
                    "UPDATE runs SET outcome = ?1, finished_at = ?2,
                 error = ?3 WHERE id = ?4 AND outcome = 'running'",
                    params![outcome, now, error, run.id],
                )
                .context("failed to close orphaned run")?;
            if cancellation_requested {
                transaction
                    .execute(
                        "UPDATE tasks SET state = 'cancelled', updated_at = ?1
                     WHERE id = ?2 AND state = 'running'",
                        params![now, run.task_id],
                    )
                    .context("failed to complete orphaned run cancellation")?;
                continue;
            }
            if run.recovery_attempt < MAX_RECOVERY_ATTEMPTS {
                transaction.execute(
                    "UPDATE tasks SET state = 'queued', updated_at = ?1, recovery_source_run_id = ?2
                     WHERE id = ?3 AND state = 'running'",
                    params![now, run.id, run.task_id],
                ).context("failed to queue orphan recovery")?;
                recovered_run_ids.push(run.id);
            } else {
                transaction
                    .execute(
                        "UPDATE tasks SET state = 'failed', updated_at = ?1
                     WHERE id = ?2 AND state = 'running'",
                        params![now, run.task_id],
                    )
                    .context("failed to exhaust orphan recovery")?;
                exhausted_run_ids.push(run.id);
            }
        }
        transaction
            .commit()
            .context("failed to commit orphan recovery")?;
        Ok(RecoveryReport {
            recovered_run_ids,
            exhausted_run_ids,
        })
    }

    pub fn register_daemon_owner(&mut self, owner_id: &str, pid: u32) -> Result<()> {
        if owner_id.trim().is_empty() {
            bail!("daemon owner ID must not be empty");
        }
        self.connection
            .execute(
                "INSERT INTO daemon_owners(owner_id, pid, heartbeat_at)
                 VALUES (?1, ?2, ?3)
                 ON CONFLICT(owner_id) DO UPDATE SET
                   pid = excluded.pid,
                   heartbeat_at = excluded.heartbeat_at",
                params![owner_id, pid, now_millis()?],
            )
            .context("failed to register daemon owner")?;
        Ok(())
    }

    pub fn heartbeat_daemon_owner(&mut self, owner_id: &str) -> Result<()> {
        let now = now_millis()?;
        let changed = self
            .connection
            .execute(
                "UPDATE daemon_owners SET heartbeat_at = ?1
                 WHERE owner_id = ?2 AND heartbeat_at >= ?3",
                params![now, owner_id, now - DAEMON_OWNER_LEASE_MILLIS],
            )
            .context("failed to update daemon owner heartbeat")?;
        if changed != 1 {
            bail!("daemon owner {owner_id:?} is not registered or its lease expired");
        }
        Ok(())
    }

    pub fn remove_daemon_owner(&mut self, owner_id: &str) -> Result<()> {
        self.connection
            .execute("DELETE FROM daemon_owners WHERE owner_id = ?1", [owner_id])
            .context("failed to remove daemon owner")?;
        Ok(())
    }
}

fn configure_wal(connection: &Connection) -> Result<()> {
    connection
        .busy_timeout(std::time::Duration::from_millis(10))
        .context("failed to configure SQLite WAL setup timeout")?;
    let mut last_error = None;
    for _ in 0..100 {
        match connection.query_row("PRAGMA journal_mode = WAL", [], |row| {
            row.get::<_, String>(0)
        }) {
            Ok(mode) if mode.eq_ignore_ascii_case("wal") => return Ok(()),
            Ok(mode) => bail!("SQLite refused WAL journal mode and selected {mode:?}"),
            Err(error) if sqlite_is_busy(&error) => {
                last_error = Some(error);
                std::thread::sleep(std::time::Duration::from_millis(10));
            }
            Err(error) => return Err(error).context("failed to configure SQLite WAL mode"),
        }
    }
    Err(anyhow::Error::new(
        last_error.expect("WAL retry loop records each lock failure"),
    ))
    .context("failed to configure SQLite WAL mode after lock retries")
}

fn sqlite_is_busy(error: &rusqlite::Error) -> bool {
    matches!(
        error,
        rusqlite::Error::SqliteFailure(code, _)
            if matches!(
                code.code,
                rusqlite::ErrorCode::DatabaseBusy | rusqlite::ErrorCode::DatabaseLocked
            )
    )
}

fn migrate(connection: &Connection) -> Result<()> {
    connection
        .execute_batch("BEGIN IMMEDIATE;")
        .context("failed to lock SQLite schema for migration")?;
    let result = (|| -> Result<()> {
        let version: i64 = connection
            .pragma_query_value(None, "user_version", |row| row.get(0))
            .context("failed to read SQLite schema version")?;
        if version > SCHEMA_VERSION {
            bail!(
                "SQLite schema version {version} is newer than supported version {SCHEMA_VERSION}"
            );
        }
        if version < 1 {
            migrate_v1(connection)?;
        }
        if version < 2 {
            migrate_v2(connection)?;
        }
        if version < 3 {
            migrate_v3(connection)?;
        }
        if version < 4 {
            migrate_v4(connection)?;
        }
        if version < 5 {
            migrate_v5(connection)?;
        }
        if version < 6 {
            migrate_v6(connection)?;
        }
        if version < 7 {
            migrate_v7(connection)?;
        }
        if version < 8 {
            migrate_v8(connection)?;
        }
        if version < 9 {
            migrate_v9(connection)?;
        }
        if version < 10 {
            migrate_v10(connection)?;
        }
        Ok(())
    })();
    match result {
        Ok(()) => connection
            .execute_batch("COMMIT;")
            .context("failed to commit SQLite schema migration"),
        Err(error) => {
            let _ = connection.execute_batch("ROLLBACK;");
            Err(error)
        }
    }
}

fn migrate_v1(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "CREATE TABLE IF NOT EXISTS schema_migrations (
                 version INTEGER PRIMARY KEY,
                 applied_at INTEGER NOT NULL
             );
             CREATE TABLE IF NOT EXISTS tasks (
                 id INTEGER PRIMARY KEY,
                 identity_key TEXT NOT NULL UNIQUE,
                 kind TEXT NOT NULL CHECK (kind IN ('ticket', 'scheduled')),
                 repository TEXT NOT NULL,
                 workflow TEXT NOT NULL,
                 source_item TEXT,
                 state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
                 created_at INTEGER NOT NULL,
                 updated_at INTEGER NOT NULL
             );
             CREATE INDEX IF NOT EXISTS tasks_state_created_idx ON tasks(state, created_at, id);
             CREATE TABLE IF NOT EXISTS runs (
                 id INTEGER PRIMARY KEY,
                 task_id INTEGER NOT NULL REFERENCES tasks(id),
                 workflow TEXT NOT NULL,
                 repository TEXT NOT NULL,
                 source_item TEXT,
                 runtime TEXT NOT NULL,
                 started_at INTEGER NOT NULL,
                 finished_at INTEGER,
                 outcome TEXT NOT NULL CHECK (outcome IN ('running', 'succeeded', 'failed', 'cancelled')),
                 result TEXT,
                 error TEXT,
                 session_id TEXT
             );
             CREATE INDEX IF NOT EXISTS runs_task_idx ON runs(task_id, id);
             CREATE UNIQUE INDEX IF NOT EXISTS runs_one_active_per_task_idx
                 ON runs(task_id) WHERE outcome = 'running';
             INSERT OR IGNORE INTO schema_migrations(version, applied_at)
                 VALUES (1, unixepoch('subsec') * 1000);
             PRAGMA user_version = 1;",
        )
        .context("failed to initialize or migrate SQLite ledger")?;
    Ok(())
}

fn migrate_v2(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "ALTER TABLE tasks ADD COLUMN payload TEXT;
             CREATE TABLE trigger_observations (
                 repository TEXT NOT NULL,
                 workflow TEXT NOT NULL,
                 source_item TEXT NOT NULL,
                 revision TEXT NOT NULL,
                 eligible INTEGER NOT NULL CHECK (eligible IN (0, 1)),
                 payload TEXT NOT NULL,
                 observed_at INTEGER NOT NULL,
                 PRIMARY KEY(repository, workflow, source_item)
             );
             INSERT INTO schema_migrations(version, applied_at)
                 VALUES (2, unixepoch('subsec') * 1000);
             PRAGMA user_version = 2;",
        )
        .context("failed to migrate SQLite ledger to version 2")?;
    Ok(())
}

fn migrate_v3(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "ALTER TABLE runs ADD COLUMN cancellation_requested_at INTEGER;
             ALTER TABLE runs ADD COLUMN owner_pid INTEGER;
             ALTER TABLE runs ADD COLUMN owner_id TEXT;
             CREATE TABLE daemon_owners (
                 owner_id TEXT PRIMARY KEY,
                 pid INTEGER NOT NULL,
                 heartbeat_at INTEGER NOT NULL
             );
             INSERT INTO schema_migrations(version, applied_at)
                 VALUES (3, unixepoch('subsec') * 1000);
             PRAGMA user_version = 3;",
        )
        .context("failed to migrate SQLite ledger to version 3")?;
    Ok(())
}

fn migrate_v4(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "ALTER TABLE tasks ADD COLUMN recovery_source_run_id INTEGER REFERENCES runs(id);
         ALTER TABLE runs ADD COLUMN process_id INTEGER;
         ALTER TABLE runs ADD COLUMN process_identity TEXT;
         ALTER TABLE runs ADD COLUMN pull_request TEXT;
         ALTER TABLE runs ADD COLUMN last_activity_at INTEGER;
         ALTER TABLE runs ADD COLUMN activity TEXT;
         ALTER TABLE runs ADD COLUMN working_directory TEXT;
         ALTER TABLE runs ADD COLUMN recovery_of INTEGER REFERENCES runs(id);
         ALTER TABLE runs ADD COLUMN recovery_attempt INTEGER NOT NULL DEFAULT 0;
         UPDATE runs SET last_activity_at = started_at WHERE last_activity_at IS NULL;
         INSERT INTO schema_migrations(version, applied_at)
             VALUES (4, unixepoch('subsec') * 1000);
         PRAGMA user_version = 4;",
        )
        .context("failed to migrate SQLite ledger to version 4")?;
    Ok(())
}

fn migrate_v5(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "CREATE TABLE schedule_cursors (
                 repository TEXT NOT NULL,
                 workflow TEXT NOT NULL,
                 fingerprint TEXT NOT NULL,
                 next_due_at INTEGER NOT NULL,
                 updated_at INTEGER NOT NULL,
                 PRIMARY KEY(repository, workflow)
             );
             CREATE TABLE schedule_owners (
                 repository TEXT NOT NULL,
                 workflow TEXT NOT NULL,
                 owner_id TEXT NOT NULL REFERENCES daemon_owners(owner_id) ON DELETE CASCADE,
                 fingerprint TEXT NOT NULL,
                 PRIMARY KEY(repository, workflow, owner_id)
             );
             INSERT INTO schema_migrations(version, applied_at)
                 VALUES (5, unixepoch('subsec') * 1000);
             PRAGMA user_version = 5;",
        )
        .context("failed to migrate SQLite ledger to version 5")?;
    Ok(())
}

fn migrate_v6(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "CREATE TABLE approval_consumptions (
                 artifact_id INTEGER PRIMARY KEY,
                 label_event_id INTEGER NOT NULL UNIQUE,
                 task_id INTEGER NOT NULL UNIQUE REFERENCES tasks(id),
                 approver_id INTEGER NOT NULL,
                 content_hash TEXT NOT NULL,
                 workflow_hash TEXT NOT NULL,
                 source_revision TEXT NOT NULL,
                 consumed_at INTEGER NOT NULL
             );
             CREATE TABLE approval_reservations (
                 repository TEXT NOT NULL,
                 issue INTEGER NOT NULL,
                 reservation_id TEXT NOT NULL,
                 created_at INTEGER NOT NULL,
                 PRIMARY KEY(repository, issue),
                 UNIQUE(reservation_id)
             );
             INSERT INTO schema_migrations(version, applied_at)
                 VALUES (6, unixepoch('subsec') * 1000);
             PRAGMA user_version = 6;",
        )
        .context("failed to migrate SQLite ledger to version 6")?;
    Ok(())
}

fn migrate_v7(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "ALTER TABLE runs ADD COLUMN base_branch TEXT;
             ALTER TABLE runs ADD COLUMN base_sha TEXT;
             ALTER TABLE runs ADD COLUMN factory_branch TEXT;
             ALTER TABLE runs ADD COLUMN workspace_kind TEXT;
             CREATE TABLE task_workspaces (
                 task_id INTEGER PRIMARY KEY REFERENCES tasks(id),
                 kind TEXT NOT NULL CHECK (kind IN ('delivery', 'proposal')),
                 repository TEXT NOT NULL,
                 base_branch TEXT NOT NULL,
                 base_sha TEXT NOT NULL,
                 factory_branch TEXT,
                 path TEXT NOT NULL,
                 state TEXT NOT NULL CHECK (state IN
                     ('preparing', 'ready', 'retained', 'cleanup_pending', 'cleaned')),
                 status_summary TEXT,
                 created_at INTEGER NOT NULL,
                 updated_at INTEGER NOT NULL,
                 cleaned_at INTEGER
             );
             CREATE UNIQUE INDEX task_workspaces_active_branch_idx
                 ON task_workspaces(factory_branch)
                 WHERE factory_branch IS NOT NULL AND state != 'cleaned';
             CREATE UNIQUE INDEX task_workspaces_active_path_idx
                 ON task_workspaces(path) WHERE state != 'cleaned';
             INSERT INTO schema_migrations(version, applied_at)
                 VALUES (7, unixepoch('subsec') * 1000);
             PRAGMA user_version = 7;",
        )
        .context("failed to migrate SQLite ledger to version 7")?;
    Ok(())
}

fn migrate_v8(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "ALTER TABLE runs ADD COLUMN effect TEXT;
             ALTER TABLE runs ADD COLUMN workflow_hash TEXT;
             ALTER TABLE runs ADD COLUMN policy_json TEXT;
             ALTER TABLE runs ADD COLUMN context_token_hash TEXT;
             ALTER TABLE runs ADD COLUMN disposition TEXT;
             ALTER TABLE runs ADD COLUMN handoff_json TEXT;
             CREATE TABLE run_effects (
                 id INTEGER PRIMARY KEY,
                 requested_run_id INTEGER,
                 run_id INTEGER REFERENCES runs(id),
                 action TEXT NOT NULL,
                 effect TEXT,
                 idempotency_key TEXT,
                 payload_version INTEGER,
                 payload_hash TEXT,
                 outcome TEXT NOT NULL CHECK (outcome IN ('pending', 'applied', 'rejected', 'failed')),
                 external_ref TEXT,
                 detail TEXT NOT NULL,
                 created_at INTEGER NOT NULL,
                 updated_at INTEGER NOT NULL
             );
             CREATE INDEX run_effects_run_idx ON run_effects(run_id, id);
             CREATE UNIQUE INDEX run_effects_idempotency_idx
                 ON run_effects(run_id, action, idempotency_key)
                 WHERE run_id IS NOT NULL AND idempotency_key IS NOT NULL
                   AND outcome IN ('pending', 'applied');
             INSERT INTO schema_migrations(version, applied_at)
                 VALUES (8, unixepoch('subsec') * 1000);
             PRAGMA user_version = 8;",
        )
        .context("failed to migrate SQLite ledger to version 8")?;
    Ok(())
}

fn migrate_v9(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "CREATE TABLE project_claims (
                 task_id INTEGER PRIMARY KEY REFERENCES tasks(id),
                 project_id TEXT NOT NULL,
                 project_item_id TEXT NOT NULL,
                 status_field_id TEXT NOT NULL,
                 expected_option_id TEXT NOT NULL,
                 active_option_id TEXT NOT NULL,
                 claimed_at INTEGER NOT NULL
             );
             INSERT INTO schema_migrations(version, applied_at)
                 VALUES (9, unixepoch('subsec') * 1000);
             PRAGMA user_version = 9;",
        )
        .context("failed to migrate SQLite ledger to version 9")?;
    Ok(())
}

fn migrate_v10(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "ALTER TABLE task_workspaces ADD COLUMN backend TEXT NOT NULL DEFAULT 'worktree'
                 CHECK (backend IN ('worktree', 'clone'));
             CREATE TABLE run_containers (
                 run_id INTEGER PRIMARY KEY REFERENCES runs(id),
                 container_id TEXT NOT NULL UNIQUE,
                 instance_id TEXT NOT NULL,
                 image_ref TEXT NOT NULL,
                 image_id TEXT NOT NULL,
                 limits_json TEXT NOT NULL,
                 state TEXT NOT NULL,
                 exit_code INTEGER,
                 logs TEXT,
                 created_at INTEGER NOT NULL,
                 updated_at INTEGER NOT NULL,
                 removed_at INTEGER
             );
             CREATE INDEX run_containers_instance_state_idx
                 ON run_containers(instance_id, state, run_id);
             INSERT INTO schema_migrations(version, applied_at)
                 VALUES (10, unixepoch('subsec') * 1000);
             PRAGMA user_version = 10;",
        )
        .context("failed to migrate SQLite ledger to version 10")?;
    Ok(())
}

fn query_task(connection: &Connection, id: i64) -> Result<Option<Task>> {
    connection
        .query_row("SELECT * FROM tasks WHERE id = ?1", [id], row_to_task)
        .optional()
        .context("failed to query task")
}

fn query_task_workspace(connection: &Connection, task_id: i64) -> Result<Option<TaskWorkspace>> {
    connection
        .query_row(
            "SELECT task_id, kind, backend, repository, base_branch, base_sha, factory_branch, path,
                    state, status_summary, created_at, updated_at, cleaned_at
             FROM task_workspaces WHERE task_id = ?1",
            [task_id],
            |row| {
                Ok(TaskWorkspace {
                    task_id: row.get(0)?,
                    kind: row.get(1)?,
                    backend: row.get(2)?,
                    repository: row.get(3)?,
                    base_branch: row.get(4)?,
                    base_sha: row.get(5)?,
                    factory_branch: row.get(6)?,
                    path: PathBuf::from(row.get::<_, String>(7)?),
                    state: row.get(8)?,
                    status_summary: row.get(9)?,
                    created_at: row.get(10)?,
                    updated_at: row.get(11)?,
                    cleaned_at: row.get(12)?,
                })
            },
        )
        .optional()
        .context("failed to query task workspace")
}

fn ensure_same_workspace(existing: &TaskWorkspace, requested: &TaskWorkspace) -> Result<()> {
    if existing.kind != requested.kind
        || existing.backend != requested.backend
        || existing.repository != requested.repository
        || existing.base_branch != requested.base_branch
        || existing.base_sha != requested.base_sha
        || existing.factory_branch != requested.factory_branch
        || existing.path != requested.path
    {
        bail!(
            "task {} already owns a different workspace; refusing to replace durable Git ownership",
            requested.task_id
        );
    }
    Ok(())
}

fn retained_delivery_run_ids(connection: &Connection) -> Result<String> {
    let mut statement = connection.prepare(
        "SELECT COALESCE(MAX(r.id), 0)
         FROM task_workspaces w
         LEFT JOIN runs r ON r.task_id = w.task_id
         WHERE w.kind = 'delivery' AND w.state != 'cleaned'
         GROUP BY w.task_id ORDER BY w.created_at, w.task_id",
    )?;
    let ids = statement
        .query_map([], |row| row.get::<_, i64>(0))?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    Ok(ids
        .into_iter()
        .filter(|id| *id > 0)
        .map(|id| id.to_string())
        .collect::<Vec<_>>()
        .join(", "))
}

fn query_task_by_key(connection: &Connection, key: &str) -> Result<Option<Task>> {
    connection
        .query_row(
            "SELECT * FROM tasks WHERE identity_key = ?1",
            [key],
            row_to_task,
        )
        .optional()
        .context("failed to query task identity")
}

fn row_to_task(row: &rusqlite::Row<'_>) -> rusqlite::Result<Task> {
    let state: String = row.get("state")?;
    let state = state.parse().map_err(|error: anyhow::Error| {
        rusqlite::Error::FromSqlConversionFailure(
            state.len(),
            rusqlite::types::Type::Text,
            error.into(),
        )
    })?;
    Ok(Task {
        id: row.get("id")?,
        identity_key: row.get("identity_key")?,
        kind: row.get("kind")?,
        repository: row.get("repository")?,
        workflow: row.get("workflow")?,
        source_item: row.get("source_item")?,
        payload: row.get("payload")?,
        state,
        created_at: row.get("created_at")?,
        updated_at: row.get("updated_at")?,
        recovery_source_run_id: row.get("recovery_source_run_id")?,
    })
}

fn query_run(connection: &Connection, id: i64) -> Result<Option<Run>> {
    connection
        .query_row("SELECT * FROM runs WHERE id = ?1", [id], row_to_run)
        .optional()
        .context("failed to query run")
}

fn row_to_run(row: &rusqlite::Row<'_>) -> rusqlite::Result<Run> {
    Ok(Run {
        id: row.get("id")?,
        task_id: row.get("task_id")?,
        workflow: row.get("workflow")?,
        repository: row.get("repository")?,
        source_item: row.get("source_item")?,
        runtime: row.get("runtime")?,
        started_at: row.get("started_at")?,
        finished_at: row.get("finished_at")?,
        outcome: row.get("outcome")?,
        result: row.get("result")?,
        error: row.get("error")?,
        session_id: row.get("session_id")?,
        cancellation_requested_at: row.get("cancellation_requested_at")?,
        owner_pid: row.get("owner_pid")?,
        owner_id: row.get("owner_id")?,
        process_id: row.get("process_id")?,
        process_identity: row.get("process_identity")?,
        pull_request: row.get("pull_request")?,
        last_activity_at: row.get("last_activity_at")?,
        activity: row.get("activity")?,
        working_directory: row.get("working_directory")?,
        recovery_of: row.get("recovery_of")?,
        recovery_attempt: row.get("recovery_attempt")?,
        base_branch: row.get("base_branch")?,
        base_sha: row.get("base_sha")?,
        factory_branch: row.get("factory_branch")?,
        workspace_kind: row.get("workspace_kind")?,
    })
}

#[cfg(unix)]
fn process_is_alive(process_id: u32) -> bool {
    let Ok(process_id) = i32::try_from(process_id) else {
        return false;
    };
    match nix::sys::signal::kill(nix::unistd::Pid::from_raw(process_id), None) {
        Ok(()) | Err(nix::errno::Errno::EPERM) => true,
        Err(_) => false,
    }
}

#[cfg(unix)]
fn terminate_orphaned_process_group(process_id: u32) -> Result<()> {
    use nix::sys::signal::{Signal, killpg};
    if process_id == 0 {
        bail!("refusing to signal process group zero");
    }
    let process_id = i32::try_from(process_id).context("process ID exceeds platform range")?;
    match killpg(nix::unistd::Pid::from_raw(process_id), Signal::SIGKILL) {
        Ok(()) | Err(nix::errno::Errno::ESRCH) => Ok(()),
        Err(error) => Err(error).context("failed to signal orphaned process group"),
    }
}

#[cfg(not(unix))]
fn process_is_alive(_process_id: u32) -> bool {
    false
}

#[cfg(not(unix))]
fn terminate_orphaned_process_group(_process_id: u32) -> Result<()> {
    Ok(())
}

fn now_millis() -> Result<i64> {
    let millis = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .context("system clock is before the Unix epoch")?
        .as_millis();
    i64::try_from(millis).context("system time cannot be represented by SQLite")
}

fn truncate_utf8(value: &str, maximum_bytes: usize) -> String {
    if value.len() <= maximum_bytes {
        return value.to_owned();
    }
    let mut end = maximum_bytes;
    while !value.is_char_boundary(end) {
        end -= 1;
    }
    value[..end].to_owned()
}

fn truncate_tail_utf8(value: &str, maximum_bytes: usize) -> String {
    if value.len() <= maximum_bytes {
        return value.to_owned();
    }
    let mut start = value.len() - maximum_bytes;
    while !value.is_char_boundary(start) {
        start += 1;
    }
    value[start..].to_owned()
}
