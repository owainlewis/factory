#![cfg(unix)]

use std::collections::{BTreeSet, HashMap};
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command as ProcessCommand;
use std::sync::Mutex;

use assert_cmd::Command;
use factory::fleet::{FleetConfig, RepositoryOperationSnapshot, activate_fleet};
use factory::storage::{Ledger, RunOutcome, TaskIdentity, TaskWorkspace};
use predicates::prelude::*;

struct RepositoryFixture {
    identity: String,
    path: PathBuf,
    data: PathBuf,
}

static ENVIRONMENT_LOCK: Mutex<()> = Mutex::new(());

struct EnvironmentRestore {
    name: &'static str,
    value: Option<std::ffi::OsString>,
}

impl EnvironmentRestore {
    fn set(name: &'static str, value: &Path) -> Self {
        let previous = std::env::var_os(name);
        // SAFETY: every test in this binary serializes environment changes with
        // ENVIRONMENT_LOCK, and child commands receive an explicit value too.
        unsafe { std::env::set_var(name, value) };
        Self {
            name,
            value: previous,
        }
    }
}

impl Drop for EnvironmentRestore {
    fn drop(&mut self) {
        // SAFETY: the EnvironmentRestore is dropped while ENVIRONMENT_LOCK is held.
        unsafe {
            if let Some(value) = &self.value {
                std::env::set_var(self.name, value);
            } else {
                std::env::remove_var(self.name);
            }
        }
    }
}

fn data_directories(root: &Path) -> BTreeSet<PathBuf> {
    fs::read_dir(root)
        .map(|entries| {
            entries
                .filter_map(Result::ok)
                .map(|entry| entry.path())
                .filter(|path| path.is_dir())
                .collect()
        })
        .unwrap_or_default()
}

fn repository(root: &Path, data_home: &Path, name: &str) -> RepositoryFixture {
    let path = root.join(name);
    let identity = format!("acme/{name}");
    fs::create_dir(&path).unwrap();
    assert!(
        ProcessCommand::new("git")
            .args(["init", "--quiet", "--initial-branch=main"])
            .current_dir(&path)
            .status()
            .unwrap()
            .success()
    );
    assert!(
        ProcessCommand::new("git")
            .args([
                "remote",
                "add",
                "origin",
                &format!("git@github.com:{identity}.git"),
            ])
            .current_dir(&path)
            .status()
            .unwrap()
            .success()
    );
    let before = data_directories(data_home);
    Command::cargo_bin("factory")
        .unwrap()
        .current_dir(&path)
        .env("FACTORY_DATA_HOME", data_home)
        .arg("init")
        .assert()
        .success();
    let config = path.join(".factory/config.toml");
    let contents = fs::read_to_string(&config).unwrap();
    fs::write(
        &config,
        contents.replace("poll_every = \"30s\"", "poll_every = \"60s\""),
    )
    .unwrap();
    let data = data_directories(data_home)
        .difference(&before)
        .next()
        .cloned()
        .expect("initialization creates one repository data directory");
    RepositoryFixture {
        identity,
        path: path.canonicalize().unwrap(),
        data,
    }
}

fn fleet(path: &Path, first: &RepositoryFixture, second: &RepositoryFixture, first_enabled: bool) {
    fs::write(
        path,
        format!(
            "max_concurrent = 2\n\
             [[repository]]\n\
             name = {:?}\n\
             path = {:?}\n\
             enabled = {}\n\
             [[repository]]\n\
             name = {:?}\n\
             path = {:?}\n",
            first.identity, first.path, first_enabled, second.identity, second.path
        ),
    )
    .unwrap();
}

fn running_run(repository: &RepositoryFixture) -> i64 {
    let mut ledger = Ledger::open_in(&repository.data).unwrap();
    let owner = format!("owner-{}", repository.identity);
    ledger
        .register_daemon_owner(&owner, std::process::id())
        .unwrap();
    ledger
        .enqueue(
            &TaskIdentity::ticket(&repository.identity, "implement", "42", "revision").unwrap(),
        )
        .unwrap();
    let runtimes = HashMap::from([(
        (
            repository.identity.clone(),
            "implement".to_owned(),
            "ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    ledger
        .claim_and_start_run(
            std::slice::from_ref(&repository.identity),
            &runtimes,
            &owner,
            std::process::id(),
        )
        .unwrap()
        .unwrap()
        .run
        .id
}

fn command(data_home: &Path) -> Command {
    let mut command = Command::cargo_bin("factory").unwrap();
    command.env("FACTORY_DATA_HOME", data_home);
    command
}

#[test]
fn fleet_inspection_aggregates_and_numeric_mutation_is_repository_scoped() {
    let _environment = ENVIRONMENT_LOCK
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let first = repository(temp.path(), &data_home, "first");
    let second = repository(temp.path(), &data_home, "second");
    let fleet_path = temp.path().join("fleet.toml");
    fleet(&fleet_path, &first, &second, true);
    let initial_status = command(&data_home)
        .args(["status", "--fleet"])
        .arg(&fleet_path)
        .arg("--json")
        .output()
        .unwrap();
    assert!(initial_status.status.success());
    let initial_status: serde_json::Value = serde_json::from_slice(&initial_status.stdout).unwrap();
    assert!(
        initial_status
            .as_array()
            .unwrap()
            .iter()
            .all(|repository| repository["state"] == "loading")
    );
    let first_run = running_run(&first);
    let second_run = running_run(&second);
    assert_eq!(first_run, second_run);

    let tasks = command(&data_home)
        .args(["tasks", "--fleet"])
        .arg(&fleet_path)
        .arg("--json")
        .output()
        .unwrap();
    assert!(tasks.status.success());
    let tasks: serde_json::Value = serde_json::from_slice(&tasks.stdout).unwrap();
    assert_eq!(tasks.as_array().unwrap().len(), 2);
    assert!(
        tasks
            .as_array()
            .unwrap()
            .iter()
            .any(|task| task["repository"] == first.identity)
    );
    assert!(
        tasks
            .as_array()
            .unwrap()
            .iter()
            .any(|task| task["repository"] == second.identity)
    );

    command(&data_home)
        .args(["inspect", &first_run.to_string(), "--fleet"])
        .arg(&fleet_path)
        .assert()
        .failure()
        .stderr(predicate::str::contains("exists in multiple repositories"));

    command(&data_home)
        .args(["cancel", &first_run.to_string(), "--fleet"])
        .arg(&fleet_path)
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "--repository owner/name is required",
        ));
    command(&data_home)
        .args(["cancel", &first_run.to_string(), "--fleet"])
        .arg(&fleet_path)
        .args(["--repository", "acme/unknown"])
        .assert()
        .failure()
        .stderr(predicate::str::contains("is not configured"));
    assert!(
        !Ledger::open_in(&first.data)
            .unwrap()
            .cancellation_requested(first_run)
            .unwrap()
    );
    assert!(
        !Ledger::open_in(&second.data)
            .unwrap()
            .cancellation_requested(second_run)
            .unwrap()
    );

    command(&data_home)
        .args(["cancel", &first_run.to_string(), "--fleet"])
        .arg(&fleet_path)
        .args(["--repository", &first.identity, "--json"])
        .assert()
        .success()
        .stdout(predicate::str::contains(format!(
            "\"repository\": \"{}\"",
            first.identity
        )));
    assert!(
        Ledger::open_in(&first.data)
            .unwrap()
            .cancellation_requested(first_run)
            .unwrap()
    );
    assert!(
        !Ledger::open_in(&second.data)
            .unwrap()
            .cancellation_requested(second_run)
            .unwrap()
    );
}

#[test]
fn fleet_status_poll_workspace_recovery_and_disabled_lifecycle_remain_inspectable() {
    let _environment = ENVIRONMENT_LOCK
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let first = repository(temp.path(), &data_home, "first");
    let second = repository(temp.path(), &data_home, "second");
    let fleet_path = temp.path().join("fleet.toml");
    fleet(&fleet_path, &first, &second, true);

    let first_run = running_run(&first);
    let mut first_ledger = Ledger::open_in(&first.data).unwrap();
    let first_task = first_ledger.run(first_run).unwrap().unwrap().task_id;
    let retained = temp.path().join("retained-first");
    fs::create_dir(&retained).unwrap();
    first_ledger
        .reserve_task_workspace(&TaskWorkspace {
            task_id: first_task,
            kind: "delivery".into(),
            backend: "worktree".into(),
            repository: first.identity.clone(),
            base_branch: "main".into(),
            base_sha: "deadbeef".into(),
            factory_branch: Some("factory/42-first".into()),
            path: retained.clone(),
            state: "retained".into(),
            status_summary: Some("retained for operator".into()),
            created_at: 0,
            updated_at: 0,
            cleaned_at: None,
        })
        .unwrap();
    first_ledger
        .update_task_workspace_state(first_task, "retained", Some("retained for operator"))
        .unwrap();
    first_ledger
        .record_repository_health(
            &first.identity,
            "backing_off",
            Some("temporary source failure"),
            3,
            Some(1_800_000_000_000),
        )
        .unwrap();
    first_ledger
        .record_poll_status(
            &first.identity,
            "implement",
            "failed",
            0,
            0,
            Some("temporary source failure"),
        )
        .unwrap();

    let second_run = running_run(&second);
    let mut second_ledger = Ledger::open_in(&second.data).unwrap();
    second_ledger
        .observe_run(
            second_run,
            Some(std::process::id()),
            Some("test-process"),
            None,
            None,
            None,
        )
        .unwrap();
    second_ledger
        .finish_run_and_task(
            second_run,
            RunOutcome::Failed,
            None,
            Some("interrupted"),
            None,
        )
        .unwrap();
    let owner = format!("owner-{}", second.identity);
    let runtimes = HashMap::from([(
        (
            second.identity.clone(),
            "implement".to_owned(),
            "ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let first_recovery = second_ledger
        .claim_and_start_run(
            std::slice::from_ref(&second.identity),
            &runtimes,
            &owner,
            std::process::id(),
        )
        .unwrap()
        .unwrap()
        .run;
    assert_eq!(first_recovery.recovery_attempt, 1);
    second_ledger
        .observe_run(
            first_recovery.id,
            Some(std::process::id()),
            Some("test-process"),
            None,
            None,
            None,
        )
        .unwrap();
    second_ledger
        .finish_run_and_task(
            first_recovery.id,
            RunOutcome::Failed,
            None,
            Some("interrupted again"),
            None,
        )
        .unwrap();

    command(&data_home)
        .args(["status", "--fleet"])
        .arg(&fleet_path)
        .arg("--json")
        .assert()
        .success()
        .stdout(predicate::str::contains("\"state\": \"backing_off\""))
        .stdout(predicate::str::contains("\"consecutive_failures\": 3"))
        .stdout(predicate::str::contains(
            "\"validation_error\": \"temporary source failure\"",
        ))
        .stdout(predicate::str::contains("\"state\": \"loading\""));
    command(&data_home)
        .args(["polls", "--fleet"])
        .arg(&fleet_path)
        .assert()
        .success()
        .stdout(predicate::str::contains(&first.identity))
        .stdout(predicate::str::contains("temporary source failure"));
    command(&data_home)
        .args(["workspaces", "--fleet"])
        .arg(&fleet_path)
        .assert()
        .success()
        .stdout(predicate::str::contains(&first.identity))
        .stdout(predicate::str::contains(retained.display().to_string()));
    let recovery = command(&data_home)
        .args(["recovery", "--fleet"])
        .arg(&fleet_path)
        .arg("--json")
        .output()
        .unwrap();
    assert!(recovery.status.success());
    let recovery: serde_json::Value = serde_json::from_slice(&recovery.stdout).unwrap();
    let queued = recovery
        .as_array()
        .unwrap()
        .iter()
        .find(|item| item["outcome"] == "queued")
        .expect("queued recovery is reported");
    assert_eq!(queued["repository"], second.identity);
    assert_eq!(queued["recovery_attempt"], 2);

    fleet(&fleet_path, &first, &second, false);
    command(&data_home)
        .args(["status", "--fleet"])
        .arg(&fleet_path)
        .args(["--repository", &first.identity])
        .assert()
        .success()
        .stdout(predicate::str::contains("disabled"));
    command(&data_home)
        .args(["cleanup", &first_run.to_string(), "--fleet"])
        .arg(&fleet_path)
        .args(["--repository", &first.identity, "--confirm"])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "disabled; re-enable it and restart Factory",
        ));
    assert!(retained.exists());
    assert_eq!(
        Ledger::open_in(&first.data)
            .unwrap()
            .task_workspace(first_task)
            .unwrap()
            .unwrap()
            .state,
        "retained"
    );

    fs::write(
        &fleet_path,
        format!(
            "max_concurrent = 1\n[[repository]]\nname = {:?}\npath = {:?}\n",
            second.identity, second.path
        ),
    )
    .unwrap();
    command(&data_home)
        .args(["status", "--fleet"])
        .arg(&fleet_path)
        .assert()
        .success()
        .stdout(predicate::str::contains(&second.identity));
    assert!(first.data.exists());
    assert!(retained.exists());
}

#[test]
fn live_fleet_operations_use_the_supervisor_startup_snapshot_until_stop() {
    let _environment = ENVIRONMENT_LOCK
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let first = repository(temp.path(), &data_home, "first");
    let second = repository(temp.path(), &data_home, "second");
    let fleet_path = temp.path().join("fleet.toml");
    fleet(&fleet_path, &first, &second, true);
    let first_run = running_run(&first);
    let mut first_ledger = Ledger::open_in(&first.data).unwrap();
    let first_task = first_ledger.run(first_run).unwrap().unwrap().task_id;
    first_ledger
        .reserve_task_workspace(&TaskWorkspace {
            task_id: first_task,
            kind: "delivery".into(),
            backend: "worktree".into(),
            repository: first.identity.clone(),
            base_branch: "main".into(),
            base_sha: "deadbeef".into(),
            factory_branch: Some("factory/42-startup".into()),
            path: temp.path().join("absent-startup-workspace"),
            state: "retained".into(),
            status_summary: None,
            created_at: 0,
            updated_at: 0,
            cleaned_at: None,
        })
        .unwrap();

    let _restore = EnvironmentRestore::set("FACTORY_DATA_HOME", &data_home);
    let startup = FleetConfig::load(&fleet_path).unwrap();
    let snapshots = startup
        .repositories
        .iter()
        .map(|repository| {
            RepositoryOperationSnapshot::from_runtime(&repository.load_runtime().unwrap())
        })
        .collect::<Vec<_>>();
    let active = activate_fleet(&fleet_path, &startup, &snapshots).unwrap();

    fs::write(
        first.path.join(".factory/config.toml"),
        "this is no longer valid repository configuration",
    )
    .unwrap();
    fs::write(
        &fleet_path,
        format!(
            "max_concurrent = 1\n[[repository]]\nname = {:?}\npath = {:?}\n",
            second.identity, second.path
        ),
    )
    .unwrap();

    command(&data_home)
        .args(["status", "--fleet"])
        .arg(&fleet_path)
        .assert()
        .success()
        .stdout(predicate::str::contains(&first.identity))
        .stdout(predicate::str::contains(&second.identity));
    command(&data_home)
        .args(["cleanup", &first_run.to_string(), "--fleet"])
        .arg(&fleet_path)
        .args(["--repository", &first.identity])
        .assert()
        .success()
        .stdout(predicate::str::contains("preview only"));
    command(&data_home)
        .args(["cancel", &first_run.to_string(), "--fleet"])
        .arg(&fleet_path)
        .args(["--repository", &first.identity])
        .assert()
        .success();
    assert!(
        Ledger::open_in(&first.data)
            .unwrap()
            .cancellation_requested(first_run)
            .unwrap()
    );

    drop(active);

    command(&data_home)
        .args(["status", "--fleet"])
        .arg(&fleet_path)
        .assert()
        .success()
        .stdout(predicate::str::contains(&second.identity))
        .stdout(predicate::str::contains(&first.identity).not());
}
