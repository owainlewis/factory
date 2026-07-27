#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Command as ProcessCommand;

use assert_cmd::Command;
use factory::storage::Ledger;
use predicates::prelude::*;

fn make_repository(root: &Path, data_home: &Path, name: &str, origin: &str) -> PathBuf {
    let repository = root.join(name);
    fs::create_dir(&repository).unwrap();
    assert!(
        ProcessCommand::new("git")
            .args(["init", "--quiet"])
            .current_dir(&repository)
            .status()
            .unwrap()
            .success()
    );
    assert!(
        ProcessCommand::new("git")
            .args(["remote", "add", "origin", origin])
            .current_dir(&repository)
            .status()
            .unwrap()
            .success()
    );
    Command::cargo_bin("factory")
        .unwrap()
        .current_dir(&repository)
        .env("FACTORY_DATA_HOME", data_home)
        .arg("init")
        .assert()
        .success();
    fs::write(
        repository.join(".factory/config.toml"),
        r#"version = 1
poll_every = "60s"

[worker]
runtime = "codex"
sandbox = "worktree"
timeout = "1h"
maximum_timeout = "8h"
max_concurrent = 2

[source]
command = ["./.factory/source.sh"]

[trigger.implement]
type = "source"
state = "ready"
labels = ["factory:ready"]
workflow = ".factory/workflows/implement.md"
"#,
    )
    .unwrap();
    fs::write(
        repository.join(".factory/workflows/implement.md"),
        "Implement the issue.\n",
    )
    .unwrap();
    let source = repository.join(".factory/source.sh");
    fs::write(
        &source,
        "#!/bin/sh\nprintf '%s\\n' '{\"issues\":[{\"key\":\"#42\",\"title\":\"Fleet task\",\"state\":\"ready\",\"labels\":[\"factory:ready\"]}]}'\n",
    )
    .unwrap();
    let mut permissions = fs::metadata(&source).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&source, permissions).unwrap();
    repository.canonicalize().unwrap()
}

fn fake_github(root: &Path) -> PathBuf {
    let bin = root.join("bin");
    fs::create_dir(&bin).unwrap();
    let gh = bin.join("gh");
    fs::write(
        &gh,
        "#!/bin/sh\ncase \"$1\" in\n  --version) echo 'gh version 2.80.0' ;;\n  auth) exit 0 ;;\n  repo) if [ -n \"$FACTORY_FAKE_GH_IDENTITY\" ]; then echo \"$FACTORY_FAKE_GH_IDENTITY\"; else echo \"Acme/$(basename \"$PWD\")\"; fi ;;\n  *) exit 64 ;;\nesac\n",
    )
    .unwrap();
    let mut permissions = fs::metadata(&gh).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&gh, permissions).unwrap();
    bin
}

fn fleet_command(bin: &Path, data_home: &Path) -> Command {
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let mut command = Command::cargo_bin("factory").unwrap();
    command
        .env("PATH", path)
        .env("FACTORY_DATA_HOME", data_home);
    command
}

fn ledgers(data_home: &Path) -> Vec<Ledger> {
    fs::read_dir(data_home)
        .unwrap()
        .filter_map(Result::ok)
        .map(|entry| entry.path())
        .filter(|path| path.is_dir())
        .map(|path| Ledger::open_in(&path).unwrap())
        .collect()
}

#[test]
fn evaluates_two_repositories_once_with_isolated_durable_tasks_and_no_workers() {
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let first = make_repository(
        temp.path(),
        &data_home,
        "first",
        "git@github.com:acme/first.git",
    );
    let second = make_repository(
        temp.path(),
        &data_home,
        "second",
        "https://github.com/acme/second.git",
    );
    let fleet = temp.path().join("fleet.toml");
    fs::write(
        &fleet,
        format!(
            "max_concurrent = 2\n[[repository]]\nname = \"acme/first\"\npath = \"$FACTORY_FIRST_REPOSITORY\"\n[[repository]]\nname = \"acme/second\"\npath = {:?}\n",
            second
        ),
    )
    .unwrap();
    let bin = fake_github(temp.path());

    fleet_command(&bin, &data_home)
        .env("FACTORY_FIRST_REPOSITORY", &first)
        .args(["run", "--fleet", fleet.to_str().unwrap(), "--once"])
        .assert()
        .success()
        .stdout(predicate::str::contains("repository=acme/first status=ok"))
        .stdout(predicate::str::contains("repository=acme/second status=ok"));

    let ledgers = ledgers(&data_home);
    assert_eq!(ledgers.len(), 2);
    for ledger in ledgers {
        let tasks = ledger.tasks().unwrap();
        assert_eq!(tasks.len(), 1);
        assert_eq!(tasks[0].source_item.as_deref(), Some("#42"));
        assert!(ledger.runs(None).unwrap().is_empty());
    }
}

#[test]
fn continues_after_invalid_repository_and_returns_aggregate_failure() {
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let healthy = make_repository(
        temp.path(),
        &data_home,
        "healthy",
        "git@github.com:acme/healthy.git",
    );
    let changed = make_repository(
        temp.path(),
        &data_home,
        "changed",
        "git@github.com:acme/replacement.git",
    );
    let fleet = temp.path().join("fleet.toml");
    fs::write(
        &fleet,
        format!(
            "max_concurrent = 2\n[[repository]]\nname = \"acme/changed\"\npath = {:?}\n[[repository]]\nname = \"acme/healthy\"\npath = {:?}\n",
            changed, healthy
        ),
    )
    .unwrap();
    let bin = fake_github(temp.path());

    fleet_command(&bin, &data_home)
        .args(["run", "--fleet", fleet.to_str().unwrap(), "--once"])
        .assert()
        .failure()
        .stdout(predicate::str::contains(
            "repository=acme/changed status=invalid_config",
        ))
        .stdout(predicate::str::contains(
            "repository=acme/healthy status=ok",
        ));

    let task_counts = ledgers(&data_home)
        .into_iter()
        .map(|ledger| ledger.tasks().unwrap().len())
        .collect::<Vec<_>>();
    assert_eq!(task_counts.iter().sum::<usize>(), 1);
}

#[test]
fn missing_repository_is_reported_without_hiding_later_results() {
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let _healthy = make_repository(
        temp.path(),
        &data_home,
        "healthy",
        "git@github.com:acme/healthy.git",
    );
    let fleet = temp.path().join("fleet.toml");
    fs::write(
        &fleet,
        "max_concurrent = 2\n[[repository]]\nname = \"acme/missing\"\npath = \"missing\"\n[[repository]]\nname = \"acme/healthy\"\npath = \"~/healthy\"\n",
    )
    .unwrap();
    let bin = fake_github(temp.path());

    fleet_command(&bin, &data_home)
        .current_dir(temp.path())
        .env("HOME", temp.path())
        .args(["run", "--fleet", fleet.to_str().unwrap(), "--once"])
        .assert()
        .failure()
        .stdout(predicate::str::contains(
            "repository=acme/missing status=invalid_config",
        ))
        .stdout(predicate::str::contains(
            "repository=acme/healthy status=ok",
        ));
}

#[test]
fn fleet_requires_once_and_rejects_unset_path_variables() {
    let temp = tempfile::tempdir().unwrap();
    let fleet = temp.path().join("fleet.toml");
    fs::write(
        &fleet,
        "max_concurrent = 1\n[[repository]]\nname = \"acme/repo\"\npath = \"$FACTORY_TEST_UNSET/repo\"\n",
    )
    .unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .args(["run", "--fleet", fleet.to_str().unwrap()])
        .assert()
        .failure()
        .stderr(predicate::str::contains("--once"));
    Command::cargo_bin("factory")
        .unwrap()
        .env_remove("FACTORY_TEST_UNSET")
        .args(["run", "--fleet", fleet.to_str().unwrap(), "--once"])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "unset environment variable $FACTORY_TEST_UNSET",
        ));
}

#[test]
fn rejects_a_github_identity_that_differs_from_the_pinned_identity() {
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let repository = make_repository(
        temp.path(),
        &data_home,
        "repository",
        "git@github.com:acme/repository.git",
    );
    let fleet = temp.path().join("fleet.toml");
    fs::write(
        &fleet,
        format!(
            "max_concurrent = 1\n[[repository]]\nname = \"acme/repository\"\npath = {:?}\n",
            repository
        ),
    )
    .unwrap();
    let bin = fake_github(temp.path());

    fleet_command(&bin, &data_home)
        .env("FACTORY_FAKE_GH_IDENTITY", "acme/renamed")
        .args(["run", "--fleet", fleet.to_str().unwrap(), "--once"])
        .assert()
        .failure()
        .stdout(predicate::str::contains(
            "does not match pinned repository identity acme/repository",
        ));

    assert!(
        ledgers(&data_home)
            .into_iter()
            .all(|ledger| ledger.tasks().unwrap().is_empty())
    );
}
