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
fn default_output_remains_unchanged() {
    let (_temp, path) = valid_config();
    let config_dir = path.parent().unwrap();
    let repository = config_dir.join("repository").canonicalize().unwrap();
    let workspace = config_dir.join("worktrees").canonicalize().unwrap();
    let expected = format!(
        "Configuration is valid.\nrepositories:\n  - {}\npoll_every: 30s\ndefault_runtime: codex\ndefault_timeout: 2h\nmaximum_timeout: 8h\nmax_concurrent_runs: 2\nmax_concurrent_runs_per_repository: 1\nworkspace_root: {}\n",
        repository.display(),
        workspace.display()
    );

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--config", path.to_str().unwrap()])
        .assert()
        .success()
        .stdout(expected);
}

#[test]
fn prints_resolved_config_as_json() {
    let (_temp, path) = valid_config();
    let config_dir = path.parent().unwrap();
    let repository = config_dir.join("repository").canonicalize().unwrap();
    let workspace = config_dir.join("worktrees").canonicalize().unwrap();

    let output = Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--json", "--config", path.to_str().unwrap()])
        .output()
        .unwrap();

    assert!(output.status.success());
    assert!(output.stderr.is_empty());
    let value: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(
        value,
        serde_json::json!({
            "repositories": [repository.display().to_string()],
            "poll_every": "30s",
            "default_runtime": "codex",
            "default_timeout": "2h",
            "maximum_timeout": "8h",
            "max_concurrent_runs": 2,
            "max_concurrent_runs_per_repository": 1,
            "workspace_root": workspace.display().to_string(),
        })
    );
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
fn json_mode_preserves_validation_failures() {
    let (_temp, path) = valid_config();
    let contents = fs::read_to_string(&path).unwrap();
    fs::write(
        &path,
        contents.replace("max_concurrent_runs = 2", "max_concurrent_runs = 0"),
    )
    .unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .args(["validate", "--json", "--config", path.to_str().unwrap()])
        .assert()
        .failure()
        .stdout(predicate::str::is_empty())
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

#[test]
fn resolves_relative_paths_from_config_directory() {
    let temp = tempfile::tempdir().unwrap();
    let config_dir = temp.path().join("configuration");
    let repository = config_dir.join("repository");
    let workspace = config_dir.join("worktrees");
    let launch_dir = temp.path().join("launch");
    fs::create_dir_all(&repository).unwrap();
    fs::create_dir(&workspace).unwrap();
    fs::create_dir(&launch_dir).unwrap();
    let path = config_dir.join("config.toml");
    fs::write(
        &path,
        r#"repositories = ["repository"]
poll_every = "30s"
default_runtime = "codex"
default_timeout = "2h"
maximum_timeout = "8h"
max_concurrent_runs = 2
workspace_root = "worktrees"
"#,
    )
    .unwrap();

    Command::cargo_bin("factory")
        .unwrap()
        .current_dir(launch_dir)
        .args(["validate", "--config", path.to_str().unwrap()])
        .assert()
        .success()
        .stdout(predicate::str::contains(repository.to_str().unwrap()))
        .stdout(predicate::str::contains(workspace.to_str().unwrap()));
}
