use std::fs;
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
    let path = repository.join(".factory/config.toml");
    fs::write(
        &path,
        r#"version = 1
poll_every = "30s"
default_runtime = "codex"
default_timeout = "2h"
maximum_timeout = "8h"
max_concurrent_runs = 2
"#,
    )
    .unwrap();
    (temp, path, repository, data_home)
}

#[test]
fn validates_explicit_config() {
    let (_temp, path, _repository, data_home) = valid_config();

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", data_home)
        .assert()
        .success()
        .stdout(predicate::str::contains("Configuration is valid."))
        .stdout(predicate::str::contains("default_runtime: codex"));
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

#[test]
fn uses_default_config_path() {
    let (_temp, _path, repository, data_home) = valid_config();
    fs::create_dir_all(repository.join("nested/directory")).unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .arg("validate")
        .current_dir(repository.join("nested/directory"))
        .env("FACTORY_DATA_HOME", data_home)
        .assert()
        .success()
        .stdout(predicate::str::contains("Configuration is valid."));
}

#[test]
fn resolves_relative_paths_from_config_directory() {
    let (_temp, path, repository, data_home) = valid_config();
    let launch_dir = repository.join("nested");
    fs::create_dir(&launch_dir).unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .current_dir(launch_dir)
        .args(["validate", "--config", path.to_str().unwrap()])
        .env("FACTORY_DATA_HOME", &data_home)
        .assert()
        .success()
        .stdout(predicate::str::contains(repository.to_str().unwrap()))
        .stdout(predicate::str::contains(data_home.to_str().unwrap()));
}
