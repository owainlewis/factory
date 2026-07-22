#![cfg(unix)]

use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

use assert_cmd::Command as AssertCommand;
use factory::storage::{Ledger, RunOutcome, TaskIdentity, TaskWorkspace};
use predicates::prelude::*;

#[test]
fn reset_previews_then_removes_repository_and_legacy_state() {
    let fixture = Fixture::new();
    finish_task(&fixture.scoped_ledger, "scoped");
    finish_task(&fixture.legacy_ledger, "legacy");
    finish_task(&fixture.configured_legacy_ledger, "configured-legacy");

    fixture.command().assert().success().stdout(
        predicate::str::contains("target: repository")
            .and(predicate::str::contains("target: legacy-global"))
            .and(predicate::str::contains("target: configured-global"))
            .and(predicate::str::contains("action: preview only")),
    );
    assert!(fixture.scoped_ledger.join("factory.sqlite3").exists());
    assert!(fixture.legacy_ledger.join("factory.sqlite3").exists());
    assert!(
        fixture
            .configured_legacy_ledger
            .join("factory.sqlite3")
            .exists()
    );

    fixture
        .command()
        .arg("--confirm")
        .assert()
        .success()
        .stdout(predicate::str::contains("action: removed durable state"));
    assert!(!fixture.scoped_ledger.join("factory.sqlite3").exists());
    assert!(!fixture.legacy_ledger.join("factory.sqlite3").exists());
    assert!(
        !fixture
            .configured_legacy_ledger
            .join("factory.sqlite3")
            .exists()
    );
    assert!(fixture.repository.join(".factory/config.toml").exists());
    assert!(fixture.scoped_ledger.join("worktrees").is_dir());
}

#[test]
fn reset_refuses_queued_work() {
    let fixture = Fixture::new();
    Ledger::open_in(&fixture.scoped_ledger)
        .unwrap()
        .enqueue(&ticket("queued"))
        .unwrap();

    fixture
        .command()
        .arg("--confirm")
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "refusing to reset repository state",
        ));
    assert!(fixture.scoped_ledger.join("factory.sqlite3").exists());
}

#[test]
fn reset_refuses_retained_workspace_ownership() {
    let fixture = Fixture::new();
    let workspace = fixture.scoped_ledger.join("worktrees/issue-7");
    let mut ledger = Ledger::open_in(&fixture.scoped_ledger).unwrap();
    let task = ledger.enqueue(&ticket("retained")).unwrap().task;
    ledger
        .reserve_task_workspace(&TaskWorkspace {
            task_id: task.id,
            kind: "delivery".into(),
            backend: "worktree".into(),
            repository: task.repository.clone(),
            base_branch: "main".into(),
            base_sha: "0123456789abcdef".into(),
            factory_branch: Some("factory/7-retained".into()),
            path: workspace.clone(),
            state: "retained".into(),
            status_summary: Some("uncommitted work".into()),
            created_at: 0,
            updated_at: 0,
            cleaned_at: None,
        })
        .unwrap();
    drop(ledger);

    fixture
        .command()
        .arg("--confirm")
        .assert()
        .failure()
        .stdout(predicate::str::contains(workspace.to_str().unwrap()))
        .stderr(predicate::str::contains("retained resources"));
    assert!(fixture.scoped_ledger.join("factory.sqlite3").exists());
}

#[test]
fn reset_refuses_a_live_daemon() {
    let fixture = Fixture::new();
    let mut ledger = Ledger::open_in(&fixture.scoped_ledger).unwrap();
    ledger.register_daemon_owner("live-reset-test", 42).unwrap();
    drop(ledger);

    fixture
        .command()
        .arg("--confirm")
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "refusing to reset repository state",
        ));
    assert!(fixture.scoped_ledger.join("factory.sqlite3").exists());
}

#[test]
fn reset_refuses_a_stale_lease_when_the_recorded_pid_is_alive() {
    let fixture = Fixture::new();
    let mut ledger = Ledger::open_in(&fixture.scoped_ledger).unwrap();
    ledger
        .register_daemon_owner("stale-live-reset-test", std::process::id())
        .unwrap();
    drop(ledger);
    let connection =
        rusqlite::Connection::open(fixture.scoped_ledger.join("factory.sqlite3")).unwrap();
    connection
        .execute(
            "UPDATE daemon_owners SET heartbeat_at = 0 WHERE owner_id = ?1",
            ["stale-live-reset-test"],
        )
        .unwrap();
    drop(connection);

    fixture
        .command()
        .arg("--confirm")
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "refusing to reset repository state",
        ));
    assert!(fixture.scoped_ledger.join("factory.sqlite3").exists());
}

#[test]
fn reset_preview_with_no_state_creates_no_lock_files() {
    let fixture = Fixture::new();

    fixture
        .command()
        .assert()
        .success()
        .stdout(predicate::str::contains("no Factory state exists"));

    assert!(!fixture.scoped_ledger.join("factory.sqlite3.lock").exists());
    assert!(!fixture.legacy_ledger.join("factory.sqlite3.lock").exists());
    assert!(
        !fixture
            .configured_legacy_ledger
            .join("factory.sqlite3.lock")
            .exists()
    );
}

#[test]
fn reset_preview_does_not_require_or_recreate_the_workspace_directory() {
    let fixture = Fixture::new();
    let workspace = fixture.scoped_ledger.join("worktrees");
    fs::remove_dir(&workspace).unwrap();

    fixture
        .command()
        .assert()
        .success()
        .stdout(predicate::str::contains("no Factory state exists"));
    assert!(!workspace.exists());
}

#[test]
fn reset_refuses_while_another_command_holds_the_state_lock() {
    let fixture = Fixture::new();
    let ledger = Ledger::open_in(&fixture.scoped_ledger).unwrap();

    fixture
        .command()
        .arg("--confirm")
        .assert()
        .failure()
        .stderr(predicate::str::contains("state is in use"));
    drop(ledger);
    assert!(fixture.scoped_ledger.join("factory.sqlite3").exists());
}

#[test]
fn reset_preview_does_not_migrate_or_create_wal_state() {
    let fixture = Fixture::new();
    let database = fixture.scoped_ledger.join("factory.sqlite3");
    let connection = rusqlite::Connection::open(&database).unwrap();
    connection
        .execute_batch(
            "CREATE TABLE tasks (
                 id INTEGER PRIMARY KEY,
                 identity_key TEXT NOT NULL UNIQUE,
                 kind TEXT NOT NULL,
                 repository TEXT NOT NULL,
                 workflow TEXT NOT NULL,
                 source_item TEXT,
                 state TEXT NOT NULL,
                 created_at INTEGER NOT NULL,
                 updated_at INTEGER NOT NULL
             );
             CREATE TABLE runs (
                 id INTEGER PRIMARY KEY,
                 task_id INTEGER NOT NULL,
                 workflow TEXT NOT NULL,
                 repository TEXT NOT NULL,
                 source_item TEXT,
                 runtime TEXT NOT NULL,
                 started_at INTEGER NOT NULL,
                 finished_at INTEGER,
                 outcome TEXT NOT NULL,
                 result TEXT,
                 error TEXT,
                 session_id TEXT
             );
             PRAGMA user_version = 1;",
        )
        .unwrap();
    drop(connection);

    fixture.command().assert().success();
    let connection = rusqlite::Connection::open_with_flags(
        &database,
        rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY,
    )
    .unwrap();
    assert_eq!(
        connection
            .query_row("PRAGMA user_version", [], |row| row.get::<_, i64>(0))
            .unwrap(),
        1
    );
    assert!(!fixture.scoped_ledger.join("factory.sqlite3-wal").exists());
}

#[test]
fn reset_discovers_and_removes_orphaned_rollback_journal_state() {
    let fixture = Fixture::new();
    let journal = fixture.scoped_ledger.join("factory.sqlite3-journal");
    fs::write(&journal, "orphaned journal").unwrap();

    fixture
        .command()
        .assert()
        .success()
        .stdout(predicate::str::contains("target: repository"));
    assert!(journal.exists());

    fixture
        .command()
        .arg("--confirm")
        .assert()
        .success()
        .stdout(predicate::str::contains("action: removed durable state"));
    assert!(!journal.exists());
}

#[cfg(unix)]
#[test]
fn reset_rejects_a_symlinked_database_without_touching_its_target() {
    use std::os::unix::fs::symlink;

    let fixture = Fixture::new();
    drop(Ledger::open_in(&fixture.scoped_ledger).unwrap());
    let database = fixture.scoped_ledger.join("factory.sqlite3");
    let target = fixture.scoped_ledger.join("target.sqlite3");
    fs::rename(&database, &target).unwrap();
    symlink(&target, &database).unwrap();

    fixture
        .command()
        .arg("--confirm")
        .assert()
        .failure()
        .stderr(predicate::str::contains("non-regular Factory state file"));
    assert!(target.exists());
}

struct Fixture {
    _temp: tempfile::TempDir,
    repository: PathBuf,
    home: PathBuf,
    data_home: PathBuf,
    scoped_ledger: PathBuf,
    legacy_ledger: PathBuf,
    configured_legacy_ledger: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repository");
        let home = temp.path().join("home");
        let data_home = temp.path().join("factory-data");
        fs::create_dir(&repository).unwrap();
        fs::create_dir(&home).unwrap();
        git(&repository, &["init", "-b", "main"]);
        git(
            &repository,
            &["config", "user.email", "factory@example.test"],
        );
        git(&repository, &["config", "user.name", "Factory Test"]);
        fs::write(repository.join("README.md"), "fixture\n").unwrap();
        git(&repository, &["add", "README.md"]);
        git(&repository, &["commit", "-m", "fixture"]);
        git(
            &repository,
            &[
                "remote",
                "add",
                "origin",
                "git@github.com:example/reset-fixture.git",
            ],
        );
        AssertCommand::cargo_bin("factory")
            .unwrap()
            .current_dir(&repository)
            .env("HOME", &home)
            .env("FACTORY_DATA_HOME", &data_home)
            .arg("init")
            .assert()
            .success();
        let scoped_ledger = fs::read_dir(&data_home)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        let legacy_ledger = home.join(".factory");
        let configured_legacy_ledger = data_home.clone();
        fs::create_dir_all(scoped_ledger.join("worktrees")).unwrap();
        Self {
            _temp: temp,
            repository,
            home,
            data_home,
            scoped_ledger,
            legacy_ledger,
            configured_legacy_ledger,
        }
    }

    fn command(&self) -> AssertCommand {
        let mut command = AssertCommand::cargo_bin("factory").unwrap();
        command
            .current_dir(&self.repository)
            .env("HOME", &self.home)
            .env("FACTORY_DATA_HOME", &self.data_home)
            .args(["reset", "--config", ".factory/config.toml"]);
        command
    }
}

fn finish_task(directory: &Path, revision: &str) {
    let mut ledger = Ledger::open_in(directory).unwrap();
    let task = ledger.enqueue(&ticket(revision)).unwrap().task;
    let claimed = ledger.claim_next().unwrap().unwrap();
    assert_eq!(claimed.id, task.id);
    let run = ledger.start_run(task.id, "codex").unwrap();
    ledger
        .finish_run_and_task_terminal(run.id, RunOutcome::Succeeded, None, None, None)
        .unwrap();
}

fn ticket(revision: &str) -> TaskIdentity {
    TaskIdentity::ticket("example/reset-fixture", "implement", "7", revision).unwrap()
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
