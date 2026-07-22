#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::process::Command;
use std::time::Duration;

use factory::config::{Config, ExecutionMode, SourceConfig, TriggerConfig, TriggerKind};
use factory::source::{SourceClient, SourceTicketContext};
use factory::storage::{Ledger, RunOutcome};
use factory::workflow::WorkflowCatalog;
use tokio_util::sync::CancellationToken;

fn fixture() -> (
    tempfile::TempDir,
    Config,
    WorkflowCatalog,
    Ledger,
    std::path::PathBuf,
) {
    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository");
    fs::create_dir(&repository).unwrap();
    assert!(
        Command::new("git")
            .args(["init", "-q"])
            .current_dir(&repository)
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
                "git@github.com:example/repository.git",
            ])
            .current_dir(&repository)
            .status()
            .unwrap()
            .success()
    );
    let repository = repository.canonicalize().unwrap();
    let workflow = repository.join(".factory/workflows/implement/WORKFLOW.md");
    fs::create_dir_all(workflow.parent().unwrap()).unwrap();
    fs::write(&workflow, "Implement the issue.\n").unwrap();
    let source = repository.join(".factory/sources/test");
    fs::create_dir_all(source.parent().unwrap()).unwrap();
    write_source(&source, true);
    let workspace_root = temp.path().join("worktrees");
    fs::create_dir(&workspace_root).unwrap();
    let config = Config {
        repositories: vec![repository],
        poll_every: Duration::from_millis(10),
        default_runtime: "codex".into(),
        default_timeout: Duration::from_secs(60),
        maximum_timeout: Duration::from_secs(300),
        max_concurrent_runs: 1,
        max_concurrent_runs_per_repository: 1,
        workspace_root,
        data_directory: temp.path().join("data"),
        execution_mode: ExecutionMode::Worktree,
        worker: None,
        triggers: vec![TriggerConfig {
            id: "implement".into(),
            workflow,
            timeout: Duration::from_secs(60),
            kind: TriggerKind::Source {
                state: "Ready To Implement".into(),
                labels: vec!["factory:ready".into()],
            },
        }],
        source: Some(SourceConfig {
            command: vec![source.display().to_string()],
            owner: String::new(),
            project_number: 0,
            status_field: String::new(),
            trusted_users: Vec::new(),
        }),
    };
    let catalog = WorkflowCatalog::load(&config).unwrap();
    let ledger = Ledger::open(&temp.path().join("ledger.db")).unwrap();
    (temp, config, catalog, ledger, source)
}

fn write_source(path: &std::path::Path, matching: bool) {
    let output = if matching {
        r##"{"issues":[{"key":"#42","title":"Fix polling","description":"The daemon misses eligible work.","state":"Ready To Implement","labels":["factory:ready","bug"],"url":"https://github.com/example/repository/issues/42"}]}"##
    } else {
        r#"{"issues":[]}"#
    };
    fs::write(
        path,
        format!("#!/bin/sh\nprintf '%s\\n' \"$*\" > .source-args\nprintf '%s\\n' '{output}'\n"),
    )
    .unwrap();
    let mut permissions = fs::metadata(path).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(path, permissions).unwrap();
}

#[tokio::test]
async fn source_conditions_are_passed_and_matching_work_is_revalidated() {
    let (_temp, config, catalog, mut ledger, source_path) = fixture();
    let client = SourceClient;
    let report = client
        .poll_once(&config, &catalog, &mut ledger, CancellationToken::new())
        .await
        .unwrap();
    assert_eq!(report.tasks_created(), 1);
    assert_eq!(
        fs::read_to_string(config.repositories[0].join(".source-args")).unwrap(),
        "--state Ready To Implement --label factory:ready\n"
    );
    let task = ledger.tasks().unwrap().remove(0);
    let context: SourceTicketContext =
        serde_json::from_str(task.payload.as_deref().unwrap()).unwrap();
    assert_eq!(context.key, "#42");
    assert_eq!(context.title, "Fix polling");
    assert_eq!(context.description, "The daemon misses eligible work.");

    let trigger = catalog.entries[0].trigger.as_ref().unwrap();
    client
        .authorize(
            &config.repositories[0],
            config.source.as_ref().unwrap(),
            trigger,
            &task,
            &CancellationToken::new(),
        )
        .await
        .unwrap();

    write_source(&source_path, false);
    let error = client
        .authorize(
            &config.repositories[0],
            config.source.as_ref().unwrap(),
            trigger,
            &task,
            &CancellationToken::new(),
        )
        .await
        .unwrap_err();
    assert!(format!("{error:#}").contains("no longer matches"));

    let claimed = ledger.claim_next().unwrap().unwrap();
    let run = ledger.start_run(claimed.id, "codex").unwrap();
    ledger
        .finish_run_and_task(run.id, RunOutcome::Succeeded, None, None, None)
        .unwrap();

    let absent = client
        .poll_once(&config, &catalog, &mut ledger, CancellationToken::new())
        .await
        .unwrap();
    assert_eq!(absent.tasks_created(), 0);

    write_source(&source_path, true);
    let reentered = client
        .poll_once(&config, &catalog, &mut ledger, CancellationToken::new())
        .await
        .unwrap();
    assert_eq!(reentered.tasks_created(), 1);
    assert_eq!(ledger.tasks().unwrap().len(), 2);
}
