use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::str::FromStr;
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};
use rusqlite::{Connection, OptionalExtension, TransactionBehavior, params};

const DATABASE_NAME: &str = "factory.sqlite3";
const SCHEMA_VERSION: i64 = 5;
pub const MAX_RESULT_BYTES: usize = 256 * 1024;
pub const MAX_ERROR_BYTES: usize = 64 * 1024;
pub const MAX_SESSION_ID_BYTES: usize = 1024;
pub const MAX_ACTIVITY_BYTES: usize = 64 * 1024;
pub const MAX_RECOVERY_ATTEMPTS: u32 = 2;
const DAEMON_OWNER_LEASE_MILLIS: i64 = 10_000;

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
}

impl Ledger {
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
                    "SELECT source_item, eligible FROM trigger_observations
                     WHERE repository = ?1 AND workflow = ?2",
                )
                .context("failed to prepare prior ticket eligibility query")?;
            statement
                .query_map(params![repository, workflow], |row| {
                    Ok((row.get::<_, String>(0)?, row.get::<_, bool>(1)?))
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
            let was_eligible = previous
                .get(&observation.source_item)
                .copied()
                .unwrap_or(false);
            if observation.eligible && !was_eligible {
                let identity = TaskIdentity::ticket(
                    repository,
                    workflow,
                    &observation.source_item,
                    &observation.revision,
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
            available.contains(&task.repository)
                && workflow_runtimes.contains_key(&(
                    task.repository.clone(),
                    task.workflow.clone(),
                    task.kind.clone(),
                ))
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

fn query_task(connection: &Connection, id: i64) -> Result<Option<Task>> {
    connection
        .query_row("SELECT * FROM tasks WHERE id = ?1", [id], row_to_task)
        .optional()
        .context("failed to query task")
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
