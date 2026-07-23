use std::fs;
use std::path::{Path, PathBuf};
use std::process::ExitCode;

use anyhow::{Context, Result, bail};
use clap::{Parser, Subcommand};
use serde::Serialize;
use tokio_util::sync::CancellationToken;

use factory::clone::CloneManager;
use factory::config::{Config, ExecutionMode, repository_config_path};
use factory::daemon::FactoryDaemon;
use factory::execution::ResolvedWorkflow;
use factory::github::GitHubClient;
use factory::init::{InitOptions, initialize};
use factory::inspection::{
    RunInspection, RunView, TaskView, print_inspection, print_runs, print_tasks,
};
use factory::runtime::{
    CodexRuntime, RuntimeCancelled, Termination, write_stderr_best_effort, write_stdout_best_effort,
};
use factory::sandbox::SandboxWorker;
use factory::source::{PollReport, SourceClient};
use factory::storage::{
    CancellationRequest, DATABASE_NAME, Ledger, OPERATOR_CONFIRMED_CLEANUP, TaskState,
    acquire_state_reset_lock, inspect_reset_state, validate_data_directory,
};
use factory::workflow::{Trigger, WorkflowCatalog};
use factory::workspace::WorkspaceManager;

#[derive(Debug, Parser)]
#[command(name = "factory", version, about)]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Debug, Subcommand)]
enum Command {
    /// Prepare a trusted GitHub repository for Factory.
    Init {
        /// Repository to initialize. Defaults to the current directory.
        #[arg(long)]
        repository: Option<PathBuf>,
        /// Report required changes without writing anything.
        #[arg(long)]
        check: bool,
        /// Execution backend for a new configuration.
        #[arg(long, value_enum, default_value_t = ExecutionMode::Worktree)]
        execution_mode: ExecutionMode,
    },
    /// Continuously evaluate work and execute tasks, or poll once without executing.
    Run {
        /// Schedule-triggered workflow ID to run once instead of starting the loop.
        workflow_id: Option<String>,
        /// Evaluate schedules and poll once without executing eligible tasks.
        #[arg(long, conflicts_with = "workflow_id")]
        once: bool,
        /// Path to the Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the durable Factory database.
        #[arg(long, conflicts_with = "workflow_id")]
        data_directory: Option<PathBuf>,
    },
    /// Validate configuration, workflows, and configured GitHub Project IDs.
    Validate {
        /// Path to the Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
    },
    /// List resolved workflows without executing their prompts.
    Workflows {
        /// Path to the Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
    },
    /// Run a workflow manually.
    Workflow {
        #[command(subcommand)]
        command: WorkflowCommand,
    },
    /// List durable tasks.
    Tasks {
        /// Print stable machine-readable JSON.
        #[arg(long)]
        json: bool,
        /// Path to the Factory configuration file used to locate the data directory.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the durable Factory database.
        #[arg(long)]
        data_directory: Option<PathBuf>,
    },
    /// List run attempts, optionally filtered by workflow.
    Runs {
        /// Workflow ID to filter by.
        workflow: Option<String>,
        /// Print stable machine-readable JSON.
        #[arg(long)]
        json: bool,
        /// Path to the Factory configuration file used to locate the data directory.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the durable Factory database.
        #[arg(long)]
        data_directory: Option<PathBuf>,
    },
    /// Inspect one run and its resolved task context.
    Inspect {
        run_id: i64,
        /// Print stable machine-readable JSON.
        #[arg(long)]
        json: bool,
        /// Path to the Factory configuration file used to locate the data directory.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the durable Factory database.
        #[arg(long)]
        data_directory: Option<PathBuf>,
    },
    /// Request cancellation of an active local run.
    Cancel {
        run_id: i64,
        /// Print stable machine-readable JSON.
        #[arg(long)]
        json: bool,
        /// Path to the Factory configuration file used to locate the data directory.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the durable Factory database.
        #[arg(long)]
        data_directory: Option<PathBuf>,
    },
    /// Preview or confirm removal of one retained Factory worktree.
    Cleanup {
        run_id: i64,
        /// Confirm removal after reviewing the preview. Dirty files are discarded.
        #[arg(long)]
        confirm: bool,
        /// Path to the repository-local Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the durable Factory database.
        #[arg(long)]
        data_directory: Option<PathBuf>,
    },
    /// Preview or confirm removal of durable state for the current repository.
    Reset {
        /// Remove state after reviewing the preview.
        #[arg(long)]
        confirm: bool,
        /// Path to the repository-local Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the repository-scoped Factory database.
        #[arg(long)]
        data_directory: Option<PathBuf>,
    },
}

#[derive(Debug, Subcommand)]
enum WorkflowCommand {
    /// Run one validated workflow against a configured repository.
    Run {
        /// Workflow ID, derived from its Markdown filename.
        workflow_id: String,
        /// Repository to use. Defaults to the enclosing Git repository.
        #[arg(long)]
        repository: Option<PathBuf>,
        /// Path to the Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
    },
}

#[derive(Serialize)]
struct CancellationResponse {
    run_id: i64,
    status: &'static str,
    owner_kind: &'static str,
    owner_pid: Option<u32>,
    outcome: String,
    message: &'static str,
}

#[tokio::main]
async fn main() -> ExitCode {
    match run_cli().await {
        Ok(code) => ExitCode::from(code),
        Err(error) => {
            write_stderr_best_effort(format!("Error: {error:#}\n").as_bytes());
            ExitCode::FAILURE
        }
    }
}

async fn run_cli() -> Result<u8> {
    let cli = Cli::parse();

    match cli.command {
        Command::Init {
            repository,
            check,
            execution_mode,
        } => {
            let repository = repository
                .unwrap_or(std::env::current_dir().context("failed to resolve current directory")?);
            let repository = factory::init::discover_repository(&repository)?;
            let report = initialize(InitOptions {
                config_path: repository_config_path(&repository),
                repository,
                check,
                execution_mode,
            })?;
            let exit_code = report.exit_code();
            print!("{report}");
            return Ok(exit_code);
        }
        Command::Run {
            workflow_id,
            once,
            config,
            data_directory,
        } => {
            if let Some(workflow_id) = workflow_id {
                return run_workflow(&workflow_id, None, config, WorkflowRunMode::ScheduledOnly)
                    .await;
            }
            return run_poller(config, data_directory, once).await;
        }
        Command::Validate { config } => {
            let path = resolve_config_path(config)?;
            let config = Config::load(&path)?;
            let catalog = WorkflowCatalog::load(&config)?;
            catalog.validate_all()?;
            validate_data_directory(&config.data_directory)?;
            let cancellation = CancellationToken::new();
            if let Some(source) = &config.source {
                let github = GitHubClient::default();
                github.validate_global(&cancellation).await?;
                let source_client = SourceClient;
                for repository in &config.repositories {
                    if source.command.is_empty() {
                        let statuses = catalog
                            .entries
                            .iter()
                            .filter_map(|entry| match entry.trigger.as_ref() {
                                Some(factory::workflow::Trigger::Status(status)) => {
                                    Some(status.clone())
                                }
                                _ => None,
                            })
                            .collect::<Vec<_>>();
                        github
                            .validate_issue_source(repository, source, &cancellation)
                            .await?;
                        if !statuses.is_empty() {
                            github
                                .validate_project_source(
                                    repository,
                                    source,
                                    &statuses,
                                    &cancellation,
                                )
                                .await?;
                        }
                    } else {
                        for workflow in catalog.entries.iter().filter(|workflow| {
                            workflow.repository == *repository && workflow.errors.is_empty()
                        }) {
                            if let Some(factory::workflow::Trigger::Source { state, labels }) =
                                &workflow.trigger
                            {
                                source_client
                                    .validate(repository, source, state, labels, &cancellation)
                                    .await?;
                            }
                        }
                    }
                }
            }
            if let Some(worker) = &config.worker {
                GitHubClient::default()
                    .validate_token_env(&worker.github_token_env, &cancellation)
                    .await?;
                SandboxWorker::new(worker.clone(), "validate")
                    .validate(&cancellation)
                    .await?;
            } else {
                CodexRuntime::default()
                    .health_check_with_cancellation(cancellation)
                    .await?;
            }
            print!("{config}");
        }
        Command::Workflows { config } => {
            let path = resolve_config_path(config)?;
            let config = Config::load(&path)?;
            let catalog = WorkflowCatalog::load(&config)?;
            print!("{catalog}");
            let invalid = catalog.invalid_count();
            if invalid > 0 {
                anyhow::bail!("workflow catalog contains {invalid} invalid workflow(s)");
            }
        }
        Command::Workflow {
            command:
                WorkflowCommand::Run {
                    workflow_id,
                    repository,
                    config,
                },
        } => {
            let repository = repository
                .unwrap_or(std::env::current_dir().context("failed to resolve current directory")?);
            let repository = factory::init::discover_repository(&repository)?;
            return run_workflow(
                &workflow_id,
                Some(&repository),
                config,
                WorkflowRunMode::Any,
            )
            .await;
        }
        Command::Tasks {
            json,
            config,
            data_directory,
        } => {
            let ledger = open_data_ledger(config, data_directory)?;
            let tasks = ledger.tasks()?;
            if json {
                let views = tasks.iter().map(TaskView::from).collect::<Vec<_>>();
                print_json(&views)?;
            } else {
                print_tasks(&tasks);
            }
        }
        Command::Runs {
            workflow,
            json,
            config,
            data_directory,
        } => {
            let ledger = open_data_ledger(config, data_directory)?;
            let runs = ledger.runs(workflow.as_deref())?;
            if json {
                let views = runs.iter().map(RunView::from).collect::<Vec<_>>();
                print_json(&views)?;
            } else {
                print_runs(&runs);
            }
        }
        Command::Inspect {
            run_id,
            json,
            config,
            data_directory,
        } => {
            let ledger = open_data_ledger(config, data_directory)?;
            let run = ledger
                .run(run_id)?
                .with_context(|| format!("run {run_id} does not exist"))?;
            let task = ledger
                .task(run.task_id)?
                .with_context(|| format!("task {} for run {run_id} does not exist", run.task_id))?;
            let container = ledger.run_container(run_id)?;
            let sandbox = ledger.run_sandbox(run_id)?;
            let inspection = RunInspection::new(&run, &task, container.as_ref(), sandbox.as_ref());
            if json {
                print_json(&inspection)?;
            } else {
                print_inspection(&inspection);
            }
        }
        Command::Cancel {
            run_id,
            json,
            config,
            data_directory,
        } => {
            let mut ledger = open_data_ledger(config, data_directory)?;
            let response = match ledger.request_run_cancellation(run_id)? {
                CancellationRequest::Requested(run) => CancellationResponse {
                    run_id: run.id,
                    status: "requested",
                    owner_kind: "factory-daemon",
                    owner_pid: run.owner_pid,
                    outcome: run.outcome,
                    message: "cancellation requested; the owning Factory daemon will stop the active process tree",
                },
                CancellationRequest::AlreadyRequested(run) => CancellationResponse {
                    run_id: run.id,
                    status: "already_requested",
                    owner_kind: "factory-daemon",
                    owner_pid: run.owner_pid,
                    outcome: run.outcome,
                    message: "cancellation was already requested from the owning Factory daemon",
                },
                CancellationRequest::Terminal(run) => CancellationResponse {
                    run_id: run.id,
                    status: "already_terminal",
                    owner_kind: "none",
                    owner_pid: None,
                    outcome: run.outcome,
                    message: "run is already terminal",
                },
                CancellationRequest::OwnedElsewhere(run) => CancellationResponse {
                    run_id: run.id,
                    status: "owned_elsewhere",
                    owner_kind: "stale-or-foreign",
                    owner_pid: run.owner_pid,
                    outcome: run.outcome,
                    message: "run has no live local Factory daemon owner; inspect or recover it before retrying cancellation",
                },
                CancellationRequest::NotFound => bail!("run {run_id} does not exist"),
            };
            if json {
                print_json(&response)?;
            } else {
                println!(
                    "Run {}: {} ({})",
                    response.run_id, response.message, response.status
                );
            }
        }
        Command::Cleanup {
            run_id,
            confirm,
            config,
            data_directory,
        } => {
            let path = resolve_config_path(config)?;
            let config = Config::load(&path)?;
            let data_directory = data_directory.unwrap_or_else(|| config.data_directory.clone());
            let ledger = Ledger::open_in(&data_directory)?;
            let run = ledger
                .run(run_id)?
                .with_context(|| format!("run {run_id} does not exist"))?;
            let task = ledger
                .task(run.task_id)?
                .with_context(|| format!("task {} for run {run_id} does not exist", run.task_id))?;
            let workspace = ledger
                .task_workspace(task.id)?
                .with_context(|| format!("run {run_id} has no Factory-owned workspace"))?;
            if workspace.state == "cleaned" {
                println!("run: {run_id}");
                println!("workspace: {}", workspace.path.display());
                println!("branch preserved: true");
                println!("action: workspace reservation is already cleaned; no changes made");
                return Ok(0);
            }
            let manager = WorkspaceManager::new(&config.repositories[0], &config.workspace_root)?;
            let clone_manager = CloneManager::new(&config.workspace_root)?;
            if !workspace.path.exists() {
                println!("run: {run_id}");
                println!("workspace: {}", workspace.path.display());
                println!(
                    "branch: {}",
                    workspace.factory_branch.as_deref().unwrap_or("detached")
                );
                println!("workspace exists: false");
                println!("branch preserved: true");
                if !confirm {
                    println!(
                        "action: preview only; rerun with --confirm to release the workspace reservation"
                    );
                } else {
                    if matches!(task.state, TaskState::Queued | TaskState::Running) {
                        bail!(
                            "refusing to release workspace for {:?} task {}; cancel or finish it first",
                            task.state,
                            task.id
                        );
                    }
                    ledger.update_task_workspace_state(
                        task.id,
                        "cleaned",
                        Some("operator confirmed absent workspace; local branch preserved"),
                    )?;
                    println!("action: released workspace reservation; local branch preserved");
                }
                return Ok(0);
            }
            let preview = if workspace.backend == "clone" {
                clone_manager.preview_cleanup(&workspace.path)?
            } else {
                manager.preview_cleanup(&workspace.path)?
            };
            println!("run: {run_id}");
            println!("workspace: {}", preview.path.display());
            println!(
                "branch: {}",
                preview.branch.as_deref().unwrap_or("detached")
            );
            println!("dirty: {}", preview.dirty);
            println!("branch preserved: true");
            if !confirm {
                println!("action: preview only; rerun with --confirm to remove the workspace");
            } else {
                if matches!(task.state, TaskState::Queued | TaskState::Running) {
                    bail!(
                        "refusing to clean workspace for {:?} task {}; cancel or finish it first",
                        task.state,
                        task.id
                    );
                }
                ledger.update_task_workspace_state(
                    task.id,
                    "cleanup_pending",
                    Some(OPERATOR_CONFIRMED_CLEANUP),
                )?;
                if workspace.backend == "clone" {
                    clone_manager.remove(&workspace.path)?;
                } else {
                    manager.cleanup(&workspace.path, true)?;
                }
                ledger.update_task_workspace_state(
                    task.id,
                    "cleaned",
                    Some("operator-confirmed cleanup completed"),
                )?;
                if workspace.backend == "clone" {
                    println!("action: removed clone; remote branch preserved");
                } else {
                    println!("action: removed worktree; local branch preserved");
                }
            }
        }
        Command::Reset {
            confirm,
            config,
            data_directory,
        } => {
            let path = resolve_config_path(config)?;
            let config = Config::load_for_inspection(&path)?;
            let data_directory = data_directory.unwrap_or_else(|| config.data_directory.clone());
            let mut databases = vec![("repository", data_directory.join(DATABASE_NAME))];
            let global = dirs::home_dir()
                .context("could not determine Factory data directory")?
                .join(".factory")
                .join(DATABASE_NAME);
            push_reset_target(&mut databases, "legacy-global", global);
            if let Some(configured_global) = configured_unscoped_database_path()? {
                push_reset_target(&mut databases, "configured-global", configured_global);
            }
            let _reset_locks = confirm
                .then(|| {
                    databases
                        .iter()
                        .map(|(_, database)| acquire_state_reset_lock(database))
                        .collect::<Result<Vec<_>>>()
                })
                .transpose()?;
            let mut existing = Vec::new();
            for (kind, database) in databases {
                if database_has_state(&database)? {
                    existing.push((kind, database));
                }
            }
            if existing.is_empty() {
                println!("action: no Factory state exists; no changes made");
                return Ok(0);
            }
            for (kind, database) in &existing {
                for artifact in database_state_files(database) {
                    if state_file_exists(&artifact)? {
                        validate_regular_state_file(&artifact)?;
                    }
                }
                let summary = if state_file_exists(database)? {
                    inspect_reset_state(database)?
                } else {
                    Default::default()
                };
                println!("target: {kind}");
                println!("database: {}", database.display());
                println!("tasks: {}", summary.tasks);
                println!("runs: {}", summary.runs);
                println!("active tasks: {}", summary.active_tasks);
                println!("active runs: {}", summary.active_runs);
                println!("managed containers: {}", summary.managed_containers);
                println!("live daemons: {}", summary.live_daemons);
                for repository in &summary.repositories {
                    println!("repository: {repository}");
                }
                println!("retained workspaces: {}", summary.retained_workspaces.len());
                for workspace in &summary.retained_workspaces {
                    println!("retained workspace: {}", workspace.display());
                }
                if confirm
                    && (summary.active_tasks > 0
                        || summary.active_runs > 0
                        || summary.managed_containers > 0
                        || summary.live_daemons > 0
                        || !summary.retained_workspaces.is_empty())
                {
                    bail!(
                        "refusing to reset {kind} state while it owns active work or retained resources; stop Factory, finish or cancel active work, and clean retained runs first"
                    );
                }
            }
            if !confirm {
                println!("action: preview only; rerun with --confirm to remove durable state");
                return Ok(0);
            }
            for (_, database) in existing {
                remove_database_files(&database)?;
            }
            println!("action: removed durable state; configuration and worktrees preserved");
        }
    }

    Ok(0)
}

fn remove_database_files(database: &Path) -> Result<()> {
    for path in database_state_files(database) {
        match fs::remove_file(&path) {
            Ok(()) => {}
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => {
                return Err(error).with_context(|| {
                    format!("failed to remove Factory state file {}", path.display())
                });
            }
        }
    }
    Ok(())
}

fn database_state_files(database: &Path) -> [PathBuf; 4] {
    let with_suffix = |suffix: &str| {
        let mut value = database.as_os_str().to_os_string();
        value.push(suffix);
        PathBuf::from(value)
    };
    [
        database.to_path_buf(),
        with_suffix("-wal"),
        with_suffix("-shm"),
        with_suffix("-journal"),
    ]
}

fn database_has_state(database: &Path) -> Result<bool> {
    database_state_files(database)
        .iter()
        .try_fold(false, |found, path| Ok(found || state_file_exists(path)?))
}

fn state_file_exists(path: &Path) -> Result<bool> {
    match fs::symlink_metadata(path) {
        Ok(_) => Ok(true),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(false),
        Err(error) => Err(error)
            .with_context(|| format!("failed to inspect Factory state {}", path.display())),
    }
}

fn push_reset_target(
    databases: &mut Vec<(&'static str, PathBuf)>,
    kind: &'static str,
    database: PathBuf,
) {
    if !databases.iter().any(|(_, existing)| existing == &database) {
        databases.push((kind, database));
    }
}

fn validate_regular_state_file(path: &Path) -> Result<()> {
    let metadata = fs::symlink_metadata(path)
        .with_context(|| format!("failed to inspect Factory state file {}", path.display()))?;
    if !metadata.file_type().is_file() || metadata.file_type().is_symlink() {
        bail!(
            "refusing to use non-regular Factory state file {}",
            path.display()
        );
    }
    Ok(())
}

fn open_data_ledger(
    config_path: Option<PathBuf>,
    data_directory: Option<PathBuf>,
) -> Result<Ledger> {
    if let Some(data_directory) = data_directory {
        return Ledger::open_in(&data_directory);
    }
    let config_path = resolve_config_path(config_path)?;
    let config = Config::load(&config_path)?;
    Ledger::open_in(&config.data_directory)
}

fn resolve_config_path(config_path: Option<PathBuf>) -> Result<PathBuf> {
    if let Some(path) = config_path {
        return Ok(path);
    }
    let current = std::env::current_dir().context("failed to resolve current directory")?;
    let repository = factory::init::discover_repository(&current)?;
    Ok(repository_config_path(&repository))
}

fn print_json(value: &impl serde::Serialize) -> Result<()> {
    println!(
        "{}",
        serde_json::to_string_pretty(value).context("failed to encode JSON output")?
    );
    Ok(())
}

async fn run_poller(
    config_path: Option<PathBuf>,
    data_directory: Option<PathBuf>,
    once: bool,
) -> Result<u8> {
    let path = resolve_config_path(config_path)?;
    let mode = if once { "once" } else { "continuous" };
    write_stderr_best_effort(
        format!("Factory starting: mode={mode} config={}\n", path.display()).as_bytes(),
    );
    let config = Config::load(&path)?;
    ensure_no_unscoped_ledger_overlap()?;
    let data_directory = data_directory.unwrap_or_else(|| config.data_directory.clone());
    let catalog = WorkflowCatalog::load(&config)?;
    let ticket_validation = catalog.validate_ticket_workflows();
    if !once && ticket_validation.is_err() {
        for entry in catalog.invalid_scheduled_entries() {
            eprintln!(
                "Factory skipped invalid scheduled workflow {}: {}",
                entry.path.display(),
                entry.errors.join("; ")
            );
        }
    }
    ticket_validation?;
    write_stderr_best_effort(
        format!(
            "Factory loaded: repositories={} workflows={} data={} poll_every={}\n",
            config.repositories.len(),
            catalog.entries.len(),
            data_directory.display(),
            humantime::format_duration(config.poll_every)
        )
        .as_bytes(),
    );
    let ledger = Ledger::open_in(&data_directory)?;
    if once {
        write_stderr_best_effort(b"Factory evaluating schedules and polling the source once...\n");
        let daemon = FactoryDaemon::new(config, catalog, ledger.path());
        let report = daemon.evaluate_once(CancellationToken::new()).await?;
        write_stdout_best_effort(
            format!(
                "scheduled_tasks_created={}\n",
                report.scheduled_tasks_created
            )
            .as_bytes(),
        );
        print_poll_report(&report.source);
        return Ok(u8::from(report.source.failures() > 0));
    }

    let cancellation = CancellationToken::new();
    let signal_token = cancellation.clone();
    let signal_task = tokio::spawn(async move {
        if tokio::signal::ctrl_c().await.is_ok() {
            signal_token.cancel();
        }
    });
    let daemon = FactoryDaemon::new(config, catalog, ledger.path());
    daemon.run(cancellation).await?;
    signal_task.abort();
    write_stderr_best_effort(b"Factory stopped.\n");
    Ok(0)
}

fn ensure_no_unscoped_ledger_overlap() -> Result<()> {
    let default_base = dirs::home_dir()
        .map(|home| home.join(".factory"))
        .context("could not determine Factory data directory")?;
    let global_database = default_base.join(DATABASE_NAME);
    if global_database.exists() {
        bail!(
            "Factory found a global ledger at {} and refused to start repository-scoped state because old queued or running work could overlap; stop the old Factory process, finish or cancel its work, then archive the global ledger before continuing",
            global_database.display()
        );
    }

    let Some(unscoped_database) = configured_unscoped_database_path()? else {
        return Ok(());
    };
    if unscoped_database.exists() {
        bail!(
            "Factory found an unscoped ledger at {} and refused to start repository-scoped state because old queued or running work could overlap; stop the old Factory process, finish or cancel its work, then archive the unscoped ledger before using this data root",
            unscoped_database.display()
        );
    }
    Ok(())
}

fn configured_unscoped_database_path() -> Result<Option<PathBuf>> {
    let Some(configured_base) = std::env::var_os("FACTORY_DATA_HOME").map(PathBuf::from) else {
        return Ok(None);
    };
    let configured_base = if configured_base.is_absolute() {
        configured_base
    } else {
        std::env::current_dir()
            .context("failed to resolve current directory")?
            .join(configured_base)
    };
    Ok(Some(configured_base.join(DATABASE_NAME)))
}

fn print_poll_report(report: &PollReport) {
    for repository in &report.repositories {
        if let Some(error) = &repository.error {
            write_stderr_best_effort(
                format!(
                    "Poll failed for {}: {error}\n",
                    repository.repository.display()
                )
                .as_bytes(),
            );
        } else {
            write_stdout_best_effort(
                format!(
                    "repository={} issues_seen={} tasks_created={}\n",
                    repository.name_with_owner.as_deref().unwrap_or("-"),
                    repository.issues_seen,
                    repository.tasks_created
                )
                .as_bytes(),
            );
        }
    }
}

#[derive(Clone, Copy)]
enum WorkflowRunMode {
    Any,
    ScheduledOnly,
}

#[cfg(all(test, unix))]
mod tests {
    use std::ffi::OsString;
    use std::os::unix::ffi::{OsStrExt, OsStringExt};
    use std::path::PathBuf;

    use super::database_state_files;

    #[test]
    fn sqlite_sidecar_paths_preserve_non_utf8_database_bytes() {
        let database = PathBuf::from(OsString::from_vec(b"/tmp/factory-\xFF.sqlite3".to_vec()));
        let files = database_state_files(&database);

        assert_eq!(
            files[0].as_os_str().as_bytes(),
            database.as_os_str().as_bytes()
        );
        assert_eq!(
            files[1].as_os_str().as_bytes(),
            b"/tmp/factory-\xFF.sqlite3-wal"
        );
        assert_eq!(
            files[2].as_os_str().as_bytes(),
            b"/tmp/factory-\xFF.sqlite3-shm"
        );
        assert_eq!(
            files[3].as_os_str().as_bytes(),
            b"/tmp/factory-\xFF.sqlite3-journal"
        );
    }
}

async fn run_workflow(
    workflow_id: &str,
    repository: Option<&std::path::Path>,
    config_path: Option<PathBuf>,
    mode: WorkflowRunMode,
) -> Result<u8> {
    let path = resolve_config_path(config_path)?;
    let config = Config::load(&path)?;
    let catalog = WorkflowCatalog::load(&config)?;
    let repository = repository
        .or_else(|| config.repositories.first().map(PathBuf::as_path))
        .context("Factory configuration has no repository")?;
    let workflow = ResolvedWorkflow::resolve(&config, &catalog, workflow_id, repository)?;
    if matches!(mode, WorkflowRunMode::ScheduledOnly) {
        let entry = catalog
            .entries
            .iter()
            .find(|entry| entry.repository == workflow.repository && entry.id == workflow.id)
            .context("resolved workflow disappeared from the workflow catalog")?;
        if !matches!(entry.trigger, Some(Trigger::Schedule { .. })) {
            bail!(
                "workflow {:?} cannot be run directly with `factory run`; only schedule-triggered workflows are allowed",
                workflow.id
            );
        }
        if config.execution_mode == ExecutionMode::DockerSandbox {
            bail!(
                "workflow {:?} cannot be run directly when worker.sandbox is \"docker_sandbox\"; start the `factory run` loop to preserve Docker Sandbox isolation",
                workflow.id
            );
        }
    }
    if workflow.runtime != "codex" {
        bail!(
            "workflow {:?} resolves to unsupported runtime {:?}; Factory v1 supports codex",
            workflow.id,
            workflow.runtime
        );
    }

    let cancellation = CancellationToken::new();
    let signal_token = cancellation.clone();
    let signal_task = tokio::spawn(async move {
        if tokio::signal::ctrl_c().await.is_ok() {
            signal_token.cancel();
        }
    });
    let runtime = CodexRuntime::default();
    let health = match runtime
        .health_check_with_cancellation(cancellation.clone())
        .await
    {
        Ok(health) => health,
        Err(error) if error.downcast_ref::<RuntimeCancelled>().is_some() => {
            signal_task.abort();
            return Ok(130);
        }
        Err(error) => return Err(error),
    };
    write_stderr_best_effort(
        format!(
            "Codex ready: {} ({})\nRunning workflow {:?} in {} with timeout {}\n",
            health.version,
            health.authentication,
            workflow.id,
            workflow.working_directory.display(),
            humantime::format_duration(workflow.timeout)
        )
        .as_bytes(),
    );
    let result = runtime
        .run(
            &workflow.prompt,
            &workflow.working_directory,
            workflow.timeout,
            cancellation,
        )
        .await?;
    signal_task.abort();

    if !result.final_response.is_empty() {
        write_final_response(&result.final_response);
    }
    write_stderr_best_effort(
        format!(
            "Run finished: status={} termination={:?} duration={} thread={} activity_lines={} activity_error={} response_truncated={}\n",
            result.status,
            result.termination,
            humantime::format_duration(result.duration),
            result.thread_id.as_deref().unwrap_or("-"),
            result.activity_lines,
            result.activity_error.as_deref().unwrap_or("-"),
            result.final_response_truncated
        )
        .as_bytes(),
    );

    match result.termination {
        Termination::TimedOut => Ok(124),
        Termination::Cancelled => Ok(130),
        Termination::Exited if result.status.success() => Ok(0),
        Termination::Exited => Ok(result
            .status
            .code()
            .and_then(|code| u8::try_from(code).ok())
            .unwrap_or(1)),
    }
}

fn write_final_response(response: &str) {
    let mut output = response.as_bytes().to_vec();
    if !response.ends_with('\n') {
        output.push(b'\n');
    }
    write_stdout_best_effort(&output);
}
