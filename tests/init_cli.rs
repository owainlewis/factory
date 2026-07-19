#![cfg(unix)]

use std::fs;
use std::os::unix::fs::{PermissionsExt, symlink};
use std::path::{Path, PathBuf};
use std::process::Command as ProcessCommand;

use assert_cmd::Command;
use factory::config::Config;
use predicates::prelude::*;

struct Fixture {
    _temp: tempfile::TempDir,
    home: PathBuf,
    repository: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let home = temp.path().join("home");
        let repository = temp.path().join("repository");
        fs::create_dir(&home).unwrap();
        init_git_repository(&repository, "git@github.com:example/repository.git");
        Self {
            _temp: temp,
            home,
            repository,
        }
    }

    fn command(&self) -> Command {
        let mut command = Command::cargo_bin("factory").unwrap();
        command
            .current_dir(&self.repository)
            .env("HOME", &self.home);
        command
    }

    fn config_path(&self) -> PathBuf {
        self.home.join(".factory/config.toml")
    }

    fn workspace(&self) -> PathBuf {
        self.home.join(".factory/workspaces")
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

fn write_config(path: &Path, repositories: &[&Path], workspace: &Path, prefix: &str) -> String {
    fs::create_dir_all(path.parent().unwrap()).unwrap();
    fs::create_dir_all(workspace).unwrap();
    let repositories = repositories
        .iter()
        .map(|repository| format!("\"{}\"", repository.display()))
        .collect::<Vec<_>>()
        .join(", ");
    let contents = format!(
        "{prefix}repositories = [{repositories}]\npoll_every = \"30s\"\ndefault_runtime = \"codex\"\ndefault_timeout = \"2h\"\nmaximum_timeout = \"8h\"\nmax_concurrent_runs = 2\nmax_concurrent_runs_per_repository = 1\nworkspace_root = \"{}\"\n",
        workspace.display()
    );
    fs::write(path, &contents).unwrap();
    contents
}

#[test]
fn init_creates_only_configuration_and_workflow_directory() {
    let fixture = Fixture::new();

    fixture
        .command()
        .arg("init")
        .assert()
        .success()
        .stdout(predicate::str::contains("global configuration"))
        .stdout(predicate::str::contains("workflow directory"))
        .stdout(predicate::str::contains("factory workflow create"))
        .stdout(predicate::str::contains("GitHub label").not())
        .stdout(predicate::str::contains("implement-ready-ticket.md").not());

    let config = Config::load(&fixture.config_path()).unwrap();
    assert_eq!(
        config.repositories,
        vec![fixture.repository.canonicalize().unwrap()]
    );
    assert_eq!(
        config.workspace_root,
        fixture.workspace().canonicalize().unwrap()
    );
    assert!(fixture.workflows().is_dir());
    assert_eq!(fs::read_dir(fixture.workflows()).unwrap().count(), 0);
    assert!(!fixture.repository.join(".gh-calls").exists());

    fixture
        .command()
        .arg("init")
        .assert()
        .success()
        .stdout(predicate::str::contains("unchanged:"));
    assert_eq!(
        fs::read_to_string(fixture.config_path())
            .unwrap()
            .matches(fixture.repository.to_str().unwrap())
            .count(),
        1
    );
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
        .stdout(predicate::str::contains("global configuration"))
        .stdout(predicate::str::contains("workspace directory"))
        .stdout(predicate::str::contains("workflow directory"));

    assert!(!fixture.config_path().exists());
    assert!(!fixture.workspace().exists());
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
}

#[test]
fn existing_config_preserves_comments_and_registers_repository_once() {
    let fixture = Fixture::new();
    let other = fixture._temp.path().join("other");
    fs::create_dir(&other).unwrap();
    let workspace = fixture._temp.path().join("workspaces");
    write_config(
        &fixture.config_path(),
        &[&other],
        &workspace,
        "# keep this comment\n",
    );

    for _ in 0..2 {
        fixture.command().arg("init").assert().success();
    }

    let contents = fs::read_to_string(fixture.config_path()).unwrap();
    assert!(contents.starts_with("# keep this comment\n"));
    assert_eq!(
        contents
            .matches(fixture.repository.to_str().unwrap())
            .count(),
        1
    );
    assert_eq!(
        Config::load(&fixture.config_path())
            .unwrap()
            .repositories
            .len(),
        2
    );
}

#[test]
fn init_recreates_workspace_missing_from_existing_config() {
    let fixture = Fixture::new();
    let original = write_config(
        &fixture.config_path(),
        &[&fixture.repository],
        &fixture.workspace(),
        "# keep this comment\n",
    );
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
fn init_rejects_parent_traversal_without_repository_writes() {
    let fixture = Fixture::new();
    let safe_workspace = fixture._temp.path().join("safe-workspace");
    let original = write_config(
        &fixture.config_path(),
        &[&fixture.repository],
        &safe_workspace,
        "",
    );
    let dangerous_workspace = fixture
        ._temp
        .path()
        .join("missing/../repository/workspaces");
    fs::remove_dir(&safe_workspace).unwrap();
    fs::write(
        fixture.config_path(),
        original.replace(
            safe_workspace.to_str().unwrap(),
            dangerous_workspace.to_str().unwrap(),
        ),
    )
    .unwrap();

    fixture
        .command()
        .arg("init")
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "must not contain parent traversal in a missing suffix",
        ));

    assert!(!fixture.repository.join(".factory").exists());
}

#[test]
fn init_resolves_symlinked_config_ancestors_before_writes() {
    let fixture = Fixture::new();
    let state = fixture.repository.join(".factory-state");
    fs::create_dir(&state).unwrap();
    symlink(&state, fixture.home.join(".factory")).unwrap();

    fixture
        .command()
        .arg("init")
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "generated configuration is invalid",
        ))
        .stderr(predicate::str::contains("must not overlap"));

    assert!(!state.join("workspaces").exists());
    assert!(!fixture.repository.join(".factory").exists());
}

#[test]
fn invalid_candidate_leaves_existing_config_and_repository_untouched() {
    let fixture = Fixture::new();
    let other = fixture._temp.path().join("other");
    fs::create_dir(&other).unwrap();
    let workspace = fixture._temp.path().join("workspaces");
    let nested_repository = workspace.join("nested-repository");
    init_git_repository(
        &nested_repository,
        "https://github.com/example/nested-repository.git",
    );
    let original = write_config(
        &fixture.config_path(),
        &[&other],
        &workspace,
        "# must survive\n",
    );

    fixture
        .command()
        .args(["init", "--repository"])
        .arg(&nested_repository)
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "generated configuration is invalid",
        ));

    assert_eq!(fs::read_to_string(fixture.config_path()).unwrap(), original);
    assert!(!nested_repository.join(".factory").exists());
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
