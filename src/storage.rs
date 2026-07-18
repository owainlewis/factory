use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::str::FromStr;
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};
use rusqlite::{Connection, OptionalExtension, TransactionBehavior, params};

const DATABASE_NAME: &str = "factory.sqlite3";
const SCHEMA_VERSION: i64 = 2;
pub const MAX_RESULT_BYTES: usize = 256 * 1024;
pub const MAX_ERROR_BYTES: usize = 64 * 1024;
pub const MAX_SESSION_ID_BYTES: usize = 1024;

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
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct EnqueuedTask {
    pub task: Task,
    pub created: bool,
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
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ClaimedRun {
    pub task: Task,
    pub run: Run,
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
            .busy_timeout(std::time::Duration::from_secs(5))
            .context("failed to configure SQLite busy timeout")?;
        connection
            .execute_batch("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;")
            .context("failed to configure SQLite database")?;
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

    pub fn claim_ticket_and_start_run(
        &mut self,
        available_repositories: &[String],
        workflow_runtimes: &HashMap<(String, String), String>,
    ) -> Result<Option<ClaimedRun>> {
        if available_repositories.is_empty() {
            return Ok(None);
        }
        let available = available_repositories
            .iter()
            .collect::<std::collections::HashSet<_>>();
        let now = now_millis()?;
        let transaction = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .context("failed to begin atomic task and run claim")?;
        let candidates = {
            let mut statement = transaction
                .prepare(
                    "SELECT * FROM tasks
                     WHERE state = 'queued' AND kind = 'ticket'
                     ORDER BY created_at, id",
                )
                .context("failed to prepare ticket claim query")?;
            statement
                .query_map([], row_to_task)
                .context("failed to query queued ticket tasks")?
                .collect::<rusqlite::Result<Vec<_>>>()
                .context("failed to read queued ticket tasks")?
        };
        let Some(task) = candidates.into_iter().find(|task| {
            available.contains(&task.repository)
                && workflow_runtimes.contains_key(&(task.repository.clone(), task.workflow.clone()))
        }) else {
            transaction
                .commit()
                .context("failed to finish empty ticket claim")?;
            return Ok(None);
        };
        let runtime = workflow_runtimes
            .get(&(task.repository.clone(), task.workflow.clone()))
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
        transaction
            .execute(
                "INSERT INTO runs
                 (task_id, workflow, repository, source_item, runtime, started_at, outcome)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'running')",
                params![
                    task.id,
                    task.workflow,
                    task.repository,
                    task.source_item,
                    runtime,
                    now
                ],
            )
            .context("failed to create run in task claim transaction")?;
        let run_id = transaction.last_insert_rowid();
        let task = query_task(&transaction, task.id)?.context("claimed task disappeared")?;
        let run = query_run(&transaction, run_id)?.context("claimed run disappeared")?;
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
                 (task_id, workflow, repository, source_item, runtime, started_at, outcome)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, 'running')",
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
        if outcome == RunOutcome::Running {
            bail!("finish_run_and_task requires a terminal outcome");
        }
        let result = result.map(|value| truncate_utf8(value, MAX_RESULT_BYTES));
        let error = error.map(|value| truncate_utf8(value, MAX_ERROR_BYTES));
        let session_id = session_id.map(|value| truncate_utf8(value, MAX_SESSION_ID_BYTES));
        let transaction = self
            .connection
            .transaction()
            .context("failed to begin run completion transaction")?;
        let task_id = transaction
            .query_row(
                "SELECT task_id FROM runs WHERE id = ?1 AND outcome = 'running'",
                [id],
                |row| row.get::<_, i64>(0),
            )
            .optional()
            .context("failed to resolve active run task")?
            .with_context(|| format!("run {id} is missing or already terminal"))?;
        let changed = transaction
            .execute(
                "UPDATE runs SET finished_at = ?1, outcome = ?2, result = ?3, error = ?4,
                 session_id = ?5 WHERE id = ?6 AND outcome = 'running'",
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
        let task_state = match outcome {
            RunOutcome::Succeeded => TaskState::Succeeded,
            RunOutcome::Failed => TaskState::Failed,
            RunOutcome::Cancelled => TaskState::Cancelled,
            RunOutcome::Running => unreachable!(),
        };
        let changed = transaction
            .execute(
                "UPDATE tasks SET state = ?1, updated_at = ?2
                 WHERE id = ?3 AND state = 'running'",
                params![task_state.as_str(), now_millis()?, task_id],
            )
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
}

fn migrate(connection: &Connection) -> Result<()> {
    let version: i64 = connection
        .pragma_query_value(None, "user_version", |row| row.get(0))
        .context("failed to read SQLite schema version")?;
    if version > SCHEMA_VERSION {
        bail!("SQLite schema version {version} is newer than supported version {SCHEMA_VERSION}");
    }
    if version < 1 {
        migrate_v1(connection)?;
    }
    if version < 2 {
        migrate_v2(connection)?;
    }
    Ok(())
}

fn migrate_v1(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "BEGIN IMMEDIATE;
             CREATE TABLE IF NOT EXISTS schema_migrations (
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
             PRAGMA user_version = 1;
             COMMIT;",
        )
        .context("failed to initialize or migrate SQLite ledger")?;
    Ok(())
}

fn migrate_v2(connection: &Connection) -> Result<()> {
    connection
        .execute_batch(
            "BEGIN IMMEDIATE;
             ALTER TABLE tasks ADD COLUMN payload TEXT;
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
             PRAGMA user_version = 2;
             COMMIT;",
        )
        .context("failed to migrate SQLite ledger to version 2")?;
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
    })
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
