#![cfg(unix)]

use std::fs;
use std::os::unix::fs::{PermissionsExt, symlink};
use std::path::{Path, PathBuf};
use std::process::Command as ProcessCommand;

use assert_cmd::Command;
use predicates::prelude::*;

struct Fixture {
    _temp: tempfile::TempDir,
    home: PathBuf,
    data_home: PathBuf,
    repository: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let home = temp.path().join("home");
        let repository = temp.path().join("repository");
        let data_home = temp.path().join("factory-data");
        fs::create_dir(&home).unwrap();
        init_git_repository(&repository, "git@github.com:example/repository.git");
        Self {
            _temp: temp,
            home,
            data_home,
            repository,
        }
    }

    fn command(&self) -> Command {
        let mut command = Command::cargo_bin("factory").unwrap();
        command
            .current_dir(&self.repository)
            .env("HOME", &self.home)
            .env("FACTORY_DATA_HOME", &self.data_home);
        command
    }

    fn config_path(&self) -> PathBuf {
        self.repository.join(".factory/config.toml")
    }

    fn workspace(&self) -> PathBuf {
        let state = fs::read_dir(&self.data_home)
            .unwrap()
            .next()
            .unwrap()
            .unwrap()
            .path();
        state.join("worktrees")
    }

    fn workflows(&self) -> PathBuf {
        self.repository.join(".factory/workflows")
    }
}

fn init_git_repository(path: &Path, origin: &str) {
    fs::create_dir_all(path).unwrap();
    assert!(
        ProcessCommand::new("git")
            .args(["init", "--quiet"])
            .current_dir(path)
            .status()
            .unwrap()
            .success()
    );
    assert!(
        ProcessCommand::new("git")
            .args(["remote", "add", "origin", origin])
            .current_dir(path)
            .status()
            .unwrap()
            .success()
    );
}

fn set_origin(path: &Path, origin: &str) {
    assert!(
        ProcessCommand::new("git")
            .args(["remote", "set-url", "origin", origin])
            .current_dir(path)
            .status()
            .unwrap()
            .success()
    );
}

#[test]
fn init_creates_complete_repository_factory_without_overwriting() {
    let fixture = Fixture::new();

    fixture
        .command()
        .arg("init")
        .assert()
        .success()
        .stdout(predicate::str::contains("repository configuration"))
        .stdout(predicate::str::contains("workflow directory"))
        .stdout(predicate::str::contains("triage-ticket.md"))
        .stdout(predicate::str::contains("implement-ready-ticket.md"))
        .stdout(predicate::str::contains("Dockerfile"))
        .stdout(predicate::str::contains("GitHub label").not())
        .stdout(predicate::str::contains("factory validate"));

    let config = fs::read_to_string(fixture.config_path()).unwrap();
    assert!(config.contains("version = 1"));
    assert!(config.contains("[source]"));
    assert!(config.contains("kind = \"github_project\""));
    assert!(config.contains("project_number = 16"));
    assert!(config.contains("[worker]"));
    assert!(config.contains("kind = \"docker\""));
    assert!(config.contains("image = \"factory-codex:dev\""));
    assert!(config.contains("memory = \"8g\""));
    assert!(config.contains("cpus = 4"));
    assert!(config.contains("pids = 512"));
    assert!(config.contains("max_concurrent_runs = 1"));
    assert!(config.contains("[source.states]"));
    assert!(!config.contains("[github]"));
    assert!(!config.contains("repositories"));
    assert!(!config.contains("workspace_root"));
    assert!(fixture.workspace().is_dir());
    assert!(fixture.workflows().is_dir());
    assert_eq!(fs::read_dir(fixture.workflows()).unwrap().count(), 2);
    assert_eq!(
        fs::read_to_string(fixture.workflows().join("triage-ticket.md")).unwrap(),
        include_str!("../.factory/workflows/triage-ticket.md")
    );
    assert_eq!(
        fs::read_to_string(fixture.workflows().join("implement-ready-ticket.md")).unwrap(),
        include_str!("../.factory/workflows/implement-ready-ticket.md")
    );
    assert_eq!(
        fs::read_to_string(fixture.repository.join(".factory/Dockerfile")).unwrap(),
        include_str!("../.factory/Dockerfile")
    );
    assert!(!fixture.repository.join(".gh-calls").exists());

    fixture
        .command()
        .arg("init")
        .assert()
        .success()
        .stdout(predicate::str::contains("unchanged:"));
    assert_eq!(fs::read_to_string(fixture.config_path()).unwrap(), config);
}

#[test]
fn check_reports_missing_resources_without_writes() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args(["init", "--check"])
        .assert()
        .failure()
        .stdout(predicate::str::contains("would create:"))
        .stdout(predicate::str::contains("repository configuration"))
        .stdout(predicate::str::contains("workspace directory"))
        .stdout(predicate::str::contains("workflow directory"))
        .stdout(predicate::str::contains("triage-ticket.md"))
        .stdout(predicate::str::contains("implement-ready-ticket.md"))
        .stdout(predicate::str::contains("Dockerfile"));

    assert!(!fixture.config_path().exists());
    assert!(!fixture.data_home.exists());
    assert!(!fixture.repository.join(".factory").exists());
}

#[test]
fn init_does_not_touch_existing_workflows() {
    let fixture = Fixture::new();
    fs::create_dir_all(fixture.workflows()).unwrap();
    let workflow = fixture.workflows().join("custom.md");
    fs::write(&workflow, "custom policy\n").unwrap();

    fixture.command().arg("init").assert().success();

    assert_eq!(fs::read_to_string(workflow).unwrap(), "custom policy\n");
    assert!(fixture.workflows().join("triage-ticket.md").is_file());
    assert!(
        fixture
            .workflows()
            .join("implement-ready-ticket.md")
            .is_file()
    );
}

#[test]
fn init_preserves_existing_default_assets_byte_for_byte() {
    let fixture = Fixture::new();
    fs::create_dir_all(fixture.workflows()).unwrap();
    let triage = fixture.workflows().join("triage-ticket.md");
    let implement = fixture.workflows().join("implement-ready-ticket.md");
    let dockerfile = fixture.repository.join(".factory/Dockerfile");
    fs::write(&triage, "custom triage\n").unwrap();
    fs::write(&implement, "custom implementation\n").unwrap();
    fs::write(&dockerfile, "FROM custom-image\n").unwrap();

    fixture.command().arg("init").assert().success();

    assert_eq!(fs::read_to_string(triage).unwrap(), "custom triage\n");
    assert_eq!(
        fs::read_to_string(implement).unwrap(),
        "custom implementation\n"
    );
    assert_eq!(
        fs::read_to_string(dockerfile).unwrap(),
        "FROM custom-image\n"
    );
}

#[test]
fn init_rejects_symlinked_default_asset_without_touching_target() {
    let fixture = Fixture::new();
    fs::create_dir_all(fixture.workflows()).unwrap();
    let outside = fixture._temp.path().join("outside-triage.md");
    fs::write(&outside, "outside policy\n").unwrap();
    symlink(&outside, fixture.workflows().join("triage-ticket.md")).unwrap();

    fixture
        .command()
        .arg("init")
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "setup path must be a regular file",
        ));

    assert_eq!(fs::read_to_string(outside).unwrap(), "outside policy\n");
    assert!(!fixture.config_path().exists());
    assert!(
        !fixture
            .workflows()
            .join("implement-ready-ticket.md")
            .exists()
    );
    assert!(!fixture.repository.join(".factory/Dockerfile").exists());
}

#[test]
fn existing_config_is_preserved_byte_for_byte() {
    let fixture = Fixture::new();
    fixture.command().arg("init").assert().success();
    let original = fs::read_to_string(fixture.config_path()).unwrap();
    let original = format!("# keep this comment\n{original}");
    fs::write(fixture.config_path(), &original).unwrap();

    for _ in 0..2 {
        fixture.command().arg("init").assert().success();
    }

    assert_eq!(fs::read_to_string(fixture.config_path()).unwrap(), original);
}

#[test]
fn init_recreates_workspace_missing_from_existing_config() {
    let fixture = Fixture::new();
    fixture.command().arg("init").assert().success();
    let original = fs::read_to_string(fixture.config_path()).unwrap();
    fs::remove_dir(fixture.workspace()).unwrap();

    fixture
        .command()
        .args(["init", "--check"])
        .assert()
        .failure()
        .stdout(predicate::str::contains("would create:"))
        .stdout(predicate::str::contains("workspace directory"));
    fixture.command().arg("init").assert().success();

    assert!(fixture.workspace().is_dir());
    assert_eq!(fs::read_to_string(fixture.config_path()).unwrap(), original);
}

#[test]
fn check_does_not_probe_existing_workspace_writability() {
    let fixture = Fixture::new();
    fixture.command().arg("init").assert().success();
    let mut permissions = fs::metadata(fixture.workspace()).unwrap().permissions();
    permissions.set_mode(0o555);
    fs::set_permissions(fixture.workspace(), permissions).unwrap();

    let output = fixture
        .command()
        .args(["init", "--check"])
        .output()
        .unwrap();

    let mut permissions = fs::metadata(fixture.workspace()).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(fixture.workspace(), permissions).unwrap();
    assert!(output.status.success());
    assert!(
        String::from_utf8(output.stdout)
            .unwrap()
            .contains("no changes were made")
    );
}

#[test]
fn init_rejects_a_symlinked_factory_directory() {
    let fixture = Fixture::new();
    let outside = fixture._temp.path().join("outside");
    fs::create_dir(&outside).unwrap();
    symlink(&outside, fixture.repository.join(".factory")).unwrap();

    fixture
        .command()
        .arg("init")
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "setup path must be a regular directory",
        ));

    assert!(!outside.join("config.toml").exists());
}

#[test]
fn init_resolves_symlinked_config_ancestors_before_writes() {
    let fixture = Fixture::new();
    let state = fixture._temp.path().join("factory-state");
    fs::create_dir(&state).unwrap();
    symlink(&state, fixture.repository.join(".factory")).unwrap();

    fixture
        .command()
        .arg("init")
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "setup path must be a regular directory",
        ));

    assert!(!state.join("config.toml").exists());
}

#[test]
fn init_targets_the_selected_repository_only() {
    let fixture = Fixture::new();
    let nested_repository = fixture._temp.path().join("nested-repository");
    init_git_repository(
        &nested_repository,
        "https://github.com/example/nested-repository.git",
    );
    fixture
        .command()
        .args(["init", "--repository"])
        .arg(&nested_repository)
        .assert()
        .success();

    assert!(!fixture.config_path().exists());
    assert!(nested_repository.join(".factory/config.toml").is_file());
}

#[test]
fn init_accepts_supported_github_origins() {
    for origin in [
        "https://token@github.com/example/repository.git",
        "ssh://git@ssh.github.com:443/example/repository.git",
    ] {
        let fixture = Fixture::new();
        set_origin(&fixture.repository, origin);
        fixture.command().arg("init").assert().success();
    }
}

#[test]
fn init_rejects_github_lookalike_origin_without_leaking_credentials() {
    let fixture = Fixture::new();
    set_origin(
        &fixture.repository,
        "https://secret-token@github.com.example.org/example/repository.git",
    );

    fixture
        .command()
        .arg("init")
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "origin is not a supported GitHub remote",
        ))
        .stderr(predicate::str::contains("secret-token").not());
}
