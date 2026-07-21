#![cfg(unix)]

use std::fs;
use std::path::Path;
use std::process::Command;

use assert_cmd::Command as AssertCommand;
use factory::storage::{Ledger, RunOutcome, TaskIdentity, TaskWorkspace};
use factory::workspace::{DeliveryReuse, WorkspaceManager};
use predicates::prelude::*;

#[test]
fn cleanup_previews_then_removes_a_retained_worktree_and_preserves_its_branch() {
    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository");
    let remote = temp.path().join("origin.git");
    let data_home = temp.path().join("factory-data");
    let ledger_directory = temp.path().join("ledger");
    fs::create_dir(&repository).unwrap();
    git(&repository, &["init", "-b", "main"]);
    git(
        &repository,
        &["config", "user.email", "factory@example.test"],
    );
    git(&repository, &["config", "user.name", "Factory Test"]);
    fs::write(repository.join("README.md"), "fixture\n").unwrap();
    git(&repository, &["add", "README.md"]);
    git(&repository, &["commit", "-m", "fixture"]);
    git(temp.path(), &["init", "--bare", remote.to_str().unwrap()]);
    let github_remote = "git@github.com:example/cleanup-fixture.git";
    git(&repository, &["remote", "add", "origin", github_remote]);

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .current_dir(&repository)
        .env("FACTORY_DATA_HOME", &data_home)
        .arg("init")
        .assert()
        .success();

    let rewrite = format!("url.file://{}.insteadOf", remote.display());
    git(&repository, &["config", &rewrite, github_remote]);
    git(&repository, &["push", "-u", "origin", "main"]);
    let repository = repository.canonicalize().unwrap();
    let state_directory = fs::read_dir(&data_home)
        .unwrap()
        .next()
        .unwrap()
        .unwrap()
        .path();
    let workspace_root = state_directory.join("worktrees").canonicalize().unwrap();
    let manager = WorkspaceManager::new(&repository, &workspace_root).unwrap();
    let base_sha = manager.fetch_default_branch("main").unwrap();

    let mut ledger = Ledger::open_in(&ledger_directory).unwrap();
    let task = ledger
        .enqueue(
            &TaskIdentity::ticket(
                "example/cleanup-fixture",
                "implement-ready-ticket",
                "7",
                "approved-revision",
            )
            .unwrap(),
        )
        .unwrap()
        .task;
    let claimed = ledger.claim_next().unwrap().unwrap();
    assert_eq!(claimed.id, task.id);
    let run = ledger.start_run(task.id, "codex").unwrap();
    let prepared = manager
        .prepare_delivery(
            7,
            "Retain this work",
            "main",
            &base_sha,
            DeliveryReuse::Reject,
        )
        .unwrap();
    ledger
        .reserve_task_workspace(&TaskWorkspace {
            task_id: task.id,
            kind: "delivery".into(),
            repository: task.repository.clone(),
            base_branch: "main".into(),
            base_sha: base_sha.clone(),
            factory_branch: prepared.branch.clone(),
            path: prepared.path.clone(),
            state: "preparing".into(),
            status_summary: None,
            created_at: 0,
            updated_at: 0,
            cleaned_at: None,
        })
        .unwrap();
    ledger
        .record_run_workspace(
            run.id,
            &prepared.path,
            "main",
            &base_sha,
            prepared.branch.as_deref(),
            "delivery",
        )
        .unwrap();
    ledger
        .update_task_workspace_state(task.id, "retained", Some("test fixture"))
        .unwrap();
    ledger
        .finish_run_and_task_terminal(
            run.id,
            RunOutcome::Failed,
            None,
            Some("retained for cleanup"),
            None,
        )
        .unwrap();
    drop(ledger);

    let config = repository.join(".factory/config.toml");
    let run_id = run.id.to_string();
    let mut preview = cleanup_command(&repository, &data_home, &config, &ledger_directory, &run_id);
    preview.assert().success().stdout(
        predicate::str::contains("action: preview only")
            .and(predicate::str::contains("branch preserved: true")),
    );
    assert!(prepared.path.exists());

    let mut confirm = cleanup_command(&repository, &data_home, &config, &ledger_directory, &run_id);
    confirm
        .arg("--confirm")
        .assert()
        .success()
        .stdout(predicate::str::contains("action: removed worktree"));
    assert!(!prepared.path.exists());
    let branch = prepared.branch.unwrap();
    git(
        &repository,
        &["show-ref", "--verify", &format!("refs/heads/{branch}")],
    );
    assert_eq!(
        Ledger::open_in(&ledger_directory)
            .unwrap()
            .task_workspace(task.id)
            .unwrap()
            .unwrap()
            .state,
        "cleaned"
    );

    git(
        &repository,
        &[
            "worktree",
            "add",
            "-b",
            "factory/7-new-revision",
            prepared.path.to_str().unwrap(),
            &base_sha,
        ],
    );
    let mut old_run_preview =
        cleanup_command(&repository, &data_home, &config, &ledger_directory, &run_id);
    old_run_preview
        .assert()
        .success()
        .stdout(predicate::str::contains("already cleaned; no changes made"));
    let mut old_run_confirm =
        cleanup_command(&repository, &data_home, &config, &ledger_directory, &run_id);
    old_run_confirm
        .arg("--confirm")
        .assert()
        .success()
        .stdout(predicate::str::contains("already cleaned; no changes made"));
    assert!(prepared.path.exists());
    assert_eq!(
        run_git(&prepared.path, &["branch", "--show-current"]),
        "factory/7-new-revision"
    );
}

fn cleanup_command(
    repository: &Path,
    data_home: &Path,
    config: &Path,
    ledger_directory: &Path,
    run_id: &str,
) -> AssertCommand {
    let mut command = AssertCommand::cargo_bin("factory").unwrap();
    command
        .current_dir(repository)
        .env("FACTORY_DATA_HOME", data_home)
        .args([
            "cleanup",
            run_id,
            "--config",
            config.to_str().unwrap(),
            "--data-directory",
            ledger_directory.to_str().unwrap(),
        ]);
    command
}

fn git(directory: &Path, arguments: &[&str]) {
    let output = Command::new("git")
        .args(arguments)
        .current_dir(directory)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "git {} failed: {}",
        arguments.join(" "),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn run_git(directory: &Path, arguments: &[&str]) -> String {
    let output = Command::new("git")
        .args(arguments)
        .current_dir(directory)
        .output()
        .unwrap();
    assert!(output.status.success());
    String::from_utf8(output.stdout).unwrap().trim().to_owned()
}
