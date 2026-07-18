use std::path::PathBuf;
use std::process::ExitCode;

use anyhow::{Result, bail};
use clap::{Parser, Subcommand};
use tokio_util::sync::CancellationToken;

use factory::config::{Config, default_config_path};
use factory::execution::ResolvedWorkflow;
use factory::runtime::{CodexRuntime, RuntimeCancelled, Termination};
use factory::workflow::WorkflowCatalog;

#[derive(Debug, Parser)]
#[command(name = "factory", version, about)]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Debug, Subcommand)]
enum Command {
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
}

#[derive(Debug, Subcommand)]
enum WorkflowCommand {
    /// Run one validated workflow against a configured repository.
    Run {
        /// Workflow ID, derived from its Markdown filename.
        workflow_id: String,
        /// Configured repository to use as the workflow target.
        #[arg(long)]
        repository: PathBuf,
        /// Path to the Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
    },
}

#[tokio::main]
async fn main() -> ExitCode {
    match run_cli().await {
        Ok(code) => ExitCode::from(code),
        Err(error) => {
            eprintln!("Error: {error:#}");
            ExitCode::FAILURE
        }
    }
}

async fn run_cli() -> Result<u8> {
    let cli = Cli::parse();

    match cli.command {
        Command::Validate { config } => {
            let path = config.unwrap_or_else(default_config_path);
            let config = Config::load(&path)?;
            print!("{config}");
        }
        Command::Workflows { config } => {
            let path = config.unwrap_or_else(default_config_path);
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
            return run_workflow(&workflow_id, &repository, config).await;
        }
    }

    Ok(0)
}

async fn run_workflow(
    workflow_id: &str,
    repository: &std::path::Path,
    config_path: Option<PathBuf>,
) -> Result<u8> {
    let path = config_path.unwrap_or_else(default_config_path);
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
    eprintln!(
        "Codex ready: {} ({})",
        health.version, health.authentication
    );
    eprintln!(
        "Running workflow {:?} in {} with timeout {}",
        workflow.id,
        workflow.working_directory.display(),
        humantime::format_duration(workflow.timeout)
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
        print!("{}", result.final_response);
        if !result.final_response.ends_with('\n') {
            println!();
        }
    }
    eprintln!(
        "Run finished: status={} termination={:?} duration={} thread={} activity_lines={} activity_error={} response_truncated={}",
        result.status,
        result.termination,
        humantime::format_duration(result.duration),
        result.thread_id.as_deref().unwrap_or("-"),
        result.activity_lines,
        result.activity_error.as_deref().unwrap_or("-"),
        result.final_response_truncated
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
