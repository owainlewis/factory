use std::fs;
#[cfg(unix)]
use std::os::unix::fs::PermissionsExt;
use std::process::Command as ProcessCommand;

use assert_cmd::Command;
use predicates::prelude::*;

fn valid_config() -> (
    tempfile::TempDir,
    std::path::PathBuf,
    std::path::PathBuf,
    std::path::PathBuf,
) {
    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository");
    let data_home = temp.path().join("data");
    fs::create_dir_all(repository.join(".factory")).unwrap();
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
            .args([
                "remote",
                "add",
                "origin",
                "git@github.com:example/repository.git"
            ])
            .current_dir(&repository)
            .status()
            .unwrap()
            .success()
    );
    Command::cargo_bin("factory")
        .unwrap()
        .current_dir(&repository)
        .env("FACTORY_DATA_HOME", &data_home)
        .arg("init")
        .assert()
        .success();
    fs::remove_dir_all(repository.join(".factory/workflows")).unwrap();
    fs::create_dir(repository.join(".factory/workflows")).unwrap();
    let path = repository.join(".factory/config.toml");
    fs::write(
        &path,
        r#"version = 1
poll_every = "30s"
default_runtime = "codex"
default_timeout = "2h"
maximum_timeout = "8h"
max_concurrent_runs = 2

[github]
trusted_approvers = ["owainlewis"]
ready_label = "factory:ready"
proposed_label = "factory:proposed"
needs_review_label = "factory:needs-review"
"#,
    )
    .unwrap();
    (temp, path, repository, data_home)
}

#[cfg(unix)]
fn command_with_healthy_codex(temp: &tempfile::TempDir) -> Command {
    let bin = temp.path().join("healthy-bin");
    fs::create_dir_all(&bin).unwrap();
    let codex = bin.join("codex");
    fs::write(
        &codex,
        "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex 1.0.0'; exit 0; fi\nif [ \"$1 $2\" = \"login status\" ]; then echo 'Logged in using ChatGPT'; exit 0; fi\nexit 64\n",
    )
    .unwrap();
    let mut permissions = fs::metadata(&codex).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(codex, permissions).unwrap();
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let mut command = Command::cargo_bin("factory").unwrap();
    command.env("PATH", path);
    command
}

#[cfg(unix)]
#[test]
fn validates_explicit_config() {
    let (temp, path, _repository, data_home) = valid_config();

    command_with_healthy_codex(&temp)
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", data_home)
        .assert()
        .success()
        .stdout(predicate::str::contains("Configuration is valid."))
        .stdout(predicate::str::contains("default_runtime: codex"));
}

#[cfg(unix)]
#[test]
fn worktree_validation_requires_a_healthy_host_codex_cli() {
    let (temp, path, _repository, data_home) = valid_config();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    let codex = bin.join("codex");
    fs::write(&codex, "#!/bin/sh\necho broken codex >&2\nexit 64\n").unwrap();
    let mut permissions = fs::metadata(&codex).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&codex, permissions).unwrap();
    let path_value = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", data_home)
        .env("PATH", path_value)
        .assert()
        .failure()
        .stderr(predicate::str::contains("Codex CLI health check failed"));
}

#[cfg(unix)]
#[test]
fn rejects_an_existing_database_that_is_not_writable() {
    let (temp, path, _repository, data_home) = valid_config();
    command_with_healthy_codex(&temp)
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", &data_home)
        .assert()
        .success();
    let state_directory = fs::read_dir(&data_home)
        .unwrap()
        .next()
        .unwrap()
        .unwrap()
        .path();
    let database = state_directory.join("factory.sqlite3");
    rusqlite::Connection::open(&database)
        .unwrap()
        .execute_batch("CREATE TABLE proof (id INTEGER);")
        .unwrap();
    let mut permissions = fs::metadata(&database).unwrap().permissions();
    permissions.set_mode(0o400);
    fs::set_permissions(&database, permissions).unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", &data_home)
        .assert()
        .failure()
        .stderr(predicate::str::contains("Factory database is read-only"));

    let mut permissions = fs::metadata(&database).unwrap().permissions();
    permissions.set_mode(0o600);
    fs::set_permissions(database, permissions).unwrap();
}

#[test]
fn validates_configurable_github_project_states() {
    let (temp, path, repository, data_home) = valid_config();
    let contents =
        fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/examples/config.toml")).unwrap();
    let auth = temp.path().join("codex/auth.json");
    fs::create_dir_all(auth.parent().unwrap()).unwrap();
    fs::write(&auth, "{}").unwrap();
    fs::write(
        &path,
        contents
            .replace("Ready To Implement", "Queued for engineering")
            .replace(
                "~/.local/share/factory/codex/auth.json",
                auth.to_str().unwrap(),
            ),
    )
    .unwrap();
    fs::write(
        repository.join(".factory/workflows/triage-ticket.md"),
        "+++\nstate = \"ready_for_spec\"\n+++\nTriage.\n",
    )
    .unwrap();
    fs::write(
        repository.join(".factory/workflows/implement-ready-ticket.md"),
        "+++\nstate = \"ready_to_implement\"\n+++\nImplement.\n",
    )
    .unwrap();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    let gh = bin.join("gh");
    fs::write(
        &gh,
        r#"#!/bin/sh
if [ "$1" = "--version" ]; then echo "gh version 2.80.0"; exit 0; fi
if [ "$1" = "auth" ]; then exit 0; fi
if [ "$1" = "repo" ]; then echo "example/repository"; exit 0; fi
if [ "$1" = "api" ] && [ "$2" = "user" ] && [ "$GH_TOKEN" = "dedicated-test-token" ]; then echo '{"id":99,"login":"factory-bot"}'; exit 0; fi
if [ "$1" = "api" ] && [ "$2" = "users/owainlewis" ]; then echo '{"id":1,"login":"owainlewis","node_id":"U_1"}'; exit 0; fi
if [ "$1" = "project" ] && [ "$2" = "view" ]; then echo '{"id":"PVT_16"}'; exit 0; fi
if [ "$1" = "project" ] && [ "$2" = "field-list" ]; then
  echo '{"fields":[{"id":"STATUS","name":"Status","type":"ProjectV2SingleSelectField","options":[{"id":"1","name":"Ready For Spec"},{"id":"2","name":"Creating Spec"},{"id":"3","name":"Queued for engineering"},{"id":"4","name":"Implementing"},{"id":"5","name":"Reviewing"},{"id":"6","name":"Done"}]}]}'
  exit 0
fi
exit 64
"#,
    )
    .unwrap();
    let mut permissions = fs::metadata(&gh).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&gh, permissions).unwrap();
    let docker = bin.join("docker");
    fs::write(
        &docker,
        r#"#!/bin/sh
if [ "$1" = "version" ]; then echo "27.0.0"; exit 0; fi
if [ "$1" = "image" ] && [ "$2" = "inspect" ]; then echo "sha256:abcdef"; exit 0; fi
if [ "$1" = "run" ] && [ "$2" = "--rm" ]; then echo "Logged in using ChatGPT"; exit 0; fi
exit 64
"#,
    )
    .unwrap();
    let mut permissions = fs::metadata(&docker).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&docker, permissions).unwrap();
    let path_value = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", data_home)
        .env("FACTORY_GITHUB_TOKEN", "dedicated-test-token")
        .env("PATH", path_value)
        .assert()
        .success()
        .stdout(predicate::str::contains("Configuration is valid."));
}

#[test]
fn rejects_invalid_github_project_source() {
    let (_temp, path, _repository, data_home) = valid_config();
    let contents =
        fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/examples/config.toml")).unwrap();
    fs::write(
        &path,
        contents.replace("project_number = 16", "project_number = 0"),
    )
    .unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", data_home)
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "source.project_number must be greater than zero",
        ));
}

#[test]
fn reports_specific_validation_failures() {
    let (_temp, path, _repository, data_home) = valid_config();
    let contents = fs::read_to_string(&path).unwrap();
    fs::write(
        &path,
        contents.replace("max_concurrent_runs = 2", "max_concurrent_runs = 0"),
    )
    .unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", data_home)
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "max_concurrent_runs must be greater than zero",
        ));
}

#[cfg(unix)]
#[test]
fn uses_default_config_path() {
    let (temp, _path, repository, data_home) = valid_config();
    fs::create_dir_all(repository.join("nested/directory")).unwrap();

    command_with_healthy_codex(&temp)
        .arg("validate")
        .current_dir(repository.join("nested/directory"))
        .env("FACTORY_DATA_HOME", data_home)
        .assert()
        .success()
        .stdout(predicate::str::contains("Configuration is valid."));
}

#[cfg(unix)]
#[test]
fn resolves_relative_paths_from_config_directory() {
    let (temp, path, repository, data_home) = valid_config();
    let launch_dir = repository.join("nested");
    fs::create_dir(&launch_dir).unwrap();

    command_with_healthy_codex(&temp)
        .current_dir(launch_dir)
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", &data_home)
        .assert()
        .success()
        .stdout(predicate::str::contains(repository.to_str().unwrap()))
        .stdout(predicate::str::contains(data_home.to_str().unwrap()));
}
