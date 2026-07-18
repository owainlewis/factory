use std::fs;

use assert_cmd::Command;
use predicates::prelude::*;

fn valid_config() -> (tempfile::TempDir, std::path::PathBuf) {
    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository");
    let workspace = temp.path().join("worktrees");
    fs::create_dir(&repository).unwrap();
    fs::create_dir(&workspace).unwrap();
    let path = temp.path().join("config.toml");
    fs::write(
        &path,
        format!(
            r#"repositories = ["{}"]
poll_every = "30s"
default_runtime = "codex"
default_timeout = "2h"
maximum_timeout = "8h"
max_concurrent_runs = 2
workspace_root = "{}"
"#,
            repository.display(),
            workspace.display()
        ),
    )
    .unwrap();
    (temp, path)
}

#[test]
fn validates_explicit_config() {
    let (_temp, path) = valid_config();

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--config", path.to_str().unwrap()])
        .assert()
        .success()
        .stdout(predicate::str::contains("Configuration is valid."))
        .stdout(predicate::str::contains("default_runtime: codex"));
}

#[test]
fn reports_specific_validation_failures() {
    let (_temp, path) = valid_config();
    let contents = fs::read_to_string(&path).unwrap();
    fs::write(
        &path,
        contents.replace("max_concurrent_runs = 2", "max_concurrent_runs = 0"),
    )
    .unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--config", path.to_str().unwrap()])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "max_concurrent_runs must be greater than zero",
        ));
}

#[test]
fn uses_default_config_path() {
    let (temp, path) = valid_config();
    let home = temp.path().join("home");
    let config_dir = home.join(".factory");
    fs::create_dir_all(&config_dir).unwrap();
    fs::copy(path, config_dir.join("config.toml")).unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .arg("validate")
        .env("HOME", home)
        .assert()
        .success()
        .stdout(predicate::str::contains("Configuration is valid."));
}
