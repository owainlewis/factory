use std::path::PathBuf;
use std::process::ExitCode;

use anyhow::{Context, Result, bail};
use clap::{ArgGroup, Parser, Subcommand};
use serde::Serialize;
use tokio_util::sync::CancellationToken;

use factory::approve::approve_issue;
use factory::config::{Config, repository_config_path, repository_remote_identity};
use factory::daemon::FactoryDaemon;
use factory::execution::ResolvedWorkflow;
use factory::github::{GitHubClient, PollReport};
use factory::init::{InitOptions, initialize};
use factory::inspection::{
    RunInspection, RunView, TaskView, print_inspection, print_runs, print_tasks,
};
use factory::runtime::{
    CodexRuntime, RuntimeCancelled, Termination, write_stderr_best_effort, write_stdout_best_effort,
};
use factory::storage::{CancellationRequest, DATABASE_NAME, Ledger};
use factory::workflow::WorkflowCatalog;
use factory::workflow_create::{CreateWorkflowOptions, create_workflow};

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
    },
    /// Poll this repository once and persist eligible tasks without executing them.
    Run {
        /// Required safety flag confirming this is a non-executing single evaluation.
        #[arg(long, required = true)]
        once: bool,
        /// Path to the Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the durable Factory database.
        #[arg(long)]
        data_directory: Option<PathBuf>,
    },
    /// Continuously evaluate schedules, poll for work, and execute eligible tasks.
    Daemon {
        /// Path to the repository-local Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the durable Factory database.
        #[arg(long)]
        data_directory: Option<PathBuf>,
    },
    /// Approve the current issue title, body, and delivery workflow revision.
    Approve {
        /// GitHub issue number to approve.
        issue: u64,
        /// Path to the repository-local Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
        /// Directory containing the durable Factory database.
        #[arg(long)]
        data_directory: Option<PathBuf>,
    },
    /// Validate configuration without starting workers or network activity.
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
}

#[derive(Debug, Subcommand)]
enum WorkflowCommand {
    /// Create a workflow from explicit trigger and prompt input.
    #[command(group(
        ArgGroup::new("trigger")
            .required(true)
            .args(["schedule", "label"])
    ))]
    #[command(group(
        ArgGroup::new("prompt_source")
            .required(true)
            .args(["prompt", "prompt_file"])
    ))]
    Create {
        /// Lowercase kebab-case workflow ID.
        workflow_id: String,
        /// Five-field cron expression.
        #[arg(long, requires = "timezone")]
        schedule: Option<String>,
        /// IANA timezone for a scheduled workflow.
        #[arg(long, requires = "schedule")]
        timezone: Option<String>,
        /// GitHub label for a label-triggered workflow.
        #[arg(long)]
        label: Option<String>,
        /// Runtime override. Inherits the configured default when omitted.
        #[arg(long)]
        runtime: Option<String>,
        /// Timeout override. Inherits the configured default when omitted.
        #[arg(long)]
        timeout: Option<String>,
        /// Workflow prompt text.
        #[arg(long)]
        prompt: Option<String>,
        /// Read workflow prompt text from this file, or from stdin with `-`.
        #[arg(long, value_name = "PATH")]
        prompt_file: Option<PathBuf>,
        /// Configured repository to create the workflow in. Defaults to the current directory.
        #[arg(long)]
        repository: Option<PathBuf>,
        /// Path to the Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
    },
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
        Command::Init { repository, check } => {
            let repository = repository
                .unwrap_or(std::env::current_dir().context("failed to resolve current directory")?);
            let repository = factory::init::discover_repository(&repository)?;
            let report = initialize(InitOptions {
                config_path: repository_config_path(&repository),
                repository,
                check,
            })?;
            let exit_code = report.exit_code();
            print!("{report}");
            return Ok(exit_code);
        }
        Command::Run {
            once,
            config,
            data_directory,
        } => {
            debug_assert!(once);
            return run_poller(config, data_directory, true).await;
        }
        Command::Daemon {
            config,
            data_directory,
        } => {
            return run_poller(config, data_directory, false).await;
        }
        Command::Approve {
            issue,
            config,
            data_directory,
        } => {
            let path = resolve_config_path(config)?;
            let config = Config::load(&path)?;
            let catalog = WorkflowCatalog::load(&config)?;
            catalog.validate_ticket_workflows()?;
            let data_directory = data_directory.unwrap_or_else(|| config.data_directory.clone());
            let mut ledger = Ledger::open_in(&data_directory)?;
            let report = approve_issue(
                &config,
                &catalog,
                &mut ledger,
                issue,
                &GitHubClient::default(),
            )
            .await?;
            print!("{report}");
        }
        Command::Validate { config } => {
            let path = resolve_config_path(config)?;
            let config = Config::load(&path)?;
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
                WorkflowCommand::Create {
                    workflow_id,
                    schedule,
                    timezone,
                    label,
                    runtime,
                    timeout,
                    prompt,
                    prompt_file,
                    repository,
                    config,
                },
        } => {
            let report = create_workflow(
                CreateWorkflowOptions {
                    id: workflow_id,
                    repository: repository.unwrap_or(
                        std::env::current_dir().context("failed to resolve current directory")?,
                    ),
                    config_path: resolve_config_path(config)?,
                    schedule,
                    timezone,
                    label,
                    runtime,
                    timeout,
                    prompt,
                    prompt_file,
                },
                &GitHubClient::default(),
            )
            .await?;
            print!("{report}");
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
            return run_workflow(&workflow_id, &repository, config).await;
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
            let inspection = RunInspection::new(&run, &task);
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
    }

    Ok(0)
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
    let data_directory = data_directory.unwrap_or_else(|| config.data_directory.clone());
    ensure_legacy_cutover(&config, &data_directory)?;
    let catalog = WorkflowCatalog::load(&config)?;
    for repository in catalog.repositories_without_ready_workflow(&config) {
        write_stderr_best_effort(
            format!(
                "No valid {:?} implementation workflow found for {}; create one with factory workflow create <workflow-id>\n",
                config.github.ready_label,
                repository.display()
            )
            .as_bytes(),
        );
    }
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
        write_stderr_best_effort(b"Factory evaluating schedules and polling GitHub once...\n");
        let daemon = FactoryDaemon::new(config, catalog, ledger.path());
        let report = daemon.evaluate_once(CancellationToken::new()).await?;
        write_stdout_best_effort(
            format!(
                "scheduled_tasks_created={}\n",
                report.scheduled_tasks_created
            )
            .as_bytes(),
        );
        print_poll_report(&report.github);
        return Ok(u8::from(report.github.failures() > 0));
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

fn ensure_legacy_cutover(config: &Config, data_directory: &std::path::Path) -> Result<()> {
    let legacy_directory = std::env::var_os("FACTORY_LEGACY_DATA_DIRECTORY")
        .map(PathBuf::from)
        .or_else(|| dirs::home_dir().map(|home| home.join(".factory")))
        .context("could not determine legacy Factory data directory")?;
    let legacy_database = legacy_directory.join(DATABASE_NAME);
    if !legacy_database.is_file() {
        return Ok(());
    }
    if data_directory
        .components()
        .any(|component| component == std::path::Component::ParentDir)
    {
        bail!(
            "Factory data directory must not contain parent traversal: {}",
            data_directory.display()
        );
    }
    let legacy_directory = legacy_directory
        .canonicalize()
        .context("failed to resolve legacy Factory data directory")?;
    let effective_data_directory = if data_directory.exists() {
        data_directory
            .canonicalize()
            .context("failed to resolve Factory data directory")?
    } else if data_directory.is_absolute() {
        data_directory.to_path_buf()
    } else {
        std::env::current_dir()
            .context("failed to resolve current directory")?
            .join(data_directory)
    };
    if effective_data_directory == legacy_directory {
        bail!(
            "repository-local Factory cannot use the legacy data directory {}; choose the derived repository state directory and leave legacy history untouched",
            legacy_directory.display()
        );
    }
    let repository = repository_remote_identity(&config.repositories[0])?;
    let active = Ledger::existing_non_terminal_tasks_for_repository(&legacy_database, &repository)?;
    if active > 0 {
        bail!(
            "legacy Factory has {active} non-terminal task(s) for {repository}; stop the old daemon and finish or cancel that work before starting repository-local Factory. The legacy database was left unchanged at {}",
            legacy_database.display()
        );
    }
    Ok(())
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

async fn run_workflow(
    workflow_id: &str,
    repository: &std::path::Path,
    config_path: Option<PathBuf>,
) -> Result<u8> {
    let path = resolve_config_path(config_path)?;
    let config = Config::load(&path)?;
    let catalog = WorkflowCatalog::load(&config)?;
    let workflow = ResolvedWorkflow::resolve(&config, &catalog, workflow_id, repository)?;
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
