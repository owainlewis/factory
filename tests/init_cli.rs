#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::os::unix::fs::symlink;
use std::path::{Path, PathBuf};
use std::process::Command as ProcessCommand;

use assert_cmd::Command;
use factory::config::Config;
use predicates::prelude::*;

struct Fixture {
    _temp: tempfile::TempDir,
    home: PathBuf,
    repository: PathBuf,
    executable_path: String,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let home = temp.path().join("home");
        let repository = temp.path().join("repository");
        fs::create_dir(&home).unwrap();
        init_git_repository(&repository, "git@github.com:example/repository.git");

        let gh = temp.path().join("gh");
        fs::write(
            &gh,
            r#"#!/bin/sh
printf '%s\n' "$*" >> .gh-calls
if [ -n "${GH_REPO:-}" ]; then echo "GH_REPO leaked into gh" >&2; exit 65; fi
if [ "$1" = "--version" ]; then echo "gh version 2.80.0"; exit 0; fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then echo "logged in"; exit 0; fi
if [ "$1" = "repo" ] && [ "$2" = "view" ]; then echo "example/repository"; exit 0; fi
if [ "$1" = "label" ] && [ "$2" = "list" ]; then
  if [ -f .factory-test-labels ]; then cat .factory-test-labels; fi
  exit 0
fi
if [ "$1" = "label" ] && [ "$2" = "create" ]; then
  if [ "$3" = "factory:needs-review" ] && [ -f .fail-needs-review ]; then
    echo "simulated label failure" >&2
    exit 1
  fi
  printf '%s\n' "$3" >> .factory-test-labels
  exit 0
fi
if [ "$1" = "api" ]; then
  if [ "$2" = 'repos/{owner}/{repo}/labels' ]; then
    if [ -f .factory-test-labels ]; then cat .factory-test-labels; fi
  else
    printf '[[]]'
  fi
  exit 0
fi
echo "unexpected fake gh arguments: $*" >&2
exit 64
"#,
        )
        .unwrap();
        let mut permissions = fs::metadata(&gh).unwrap().permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(&gh, permissions).unwrap();
        let executable_path = format!(
            "{}:{}",
            temp.path().display(),
            std::env::var("PATH").unwrap()
        );
        Self {
            _temp: temp,
            home,
            repository,
            executable_path,
        }
    }

    fn command(&self) -> Command {
        let mut command = Command::cargo_bin("factory").unwrap();
        command
            .current_dir(&self.repository)
            .env("HOME", &self.home)
            .env("PATH", &self.executable_path);
        command
    }

    fn config_path(&self) -> PathBuf {
        self.home.join(".factory/config.toml")
    }

    fn workspace(&self) -> PathBuf {
        self.home.join(".factory/workspaces")
    }

    fn workflow(&self) -> PathBuf {
        self.repository
            .join(".factory/workflows/implement-ready-ticket.md")
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
fn init_creates_complete_setup_and_is_idempotent() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args(["init", "--repository"])
        .arg(&fixture.repository)
        .assert()
        .success()
        .stdout(predicate::str::contains("created: "))
        .stdout(predicate::str::contains("GitHub label factory:ready"))
        .stdout(predicate::str::contains("git -C "))
        .stdout(predicate::str::contains(
            ".factory/workflows/implement-ready-ticket.md",
        ))
        .stdout(predicate::str::contains("factory validate"));

    assert_eq!(
        fs::read_to_string(fixture.workflow()).unwrap(),
        include_str!("../examples/implement-ready-ticket.md")
    );
    let config = Config::load(&fixture.config_path()).unwrap();
    assert_eq!(
        config.repositories,
        vec![fixture.repository.canonicalize().unwrap()]
    );
    assert_eq!(
        config.workspace_root,
        fixture.workspace().canonicalize().unwrap()
    );
    assert_eq!(
        fs::read_to_string(fixture.repository.join(".factory-test-labels")).unwrap(),
        "factory:ready\nfactory:needs-review\n"
    );

    fixture
        .command()
        .args(["init", "--repository"])
        .arg(&fixture.repository)
        .assert()
        .success()
        .stdout(predicate::str::contains("unchanged:"));

    let contents = fs::read_to_string(fixture.config_path()).unwrap();
    assert_eq!(
        contents
            .matches(fixture.repository.to_str().unwrap())
            .count(),
        1
    );
    assert_eq!(
        fs::read_to_string(fixture.repository.join(".factory-test-labels")).unwrap(),
        "factory:ready\nfactory:needs-review\n"
    );
}

#[test]
fn init_accepts_credentialed_github_https_origin() {
    let fixture = Fixture::new();
    set_origin(
        &fixture.repository,
        "https://user:secret@github.com/example/repository.git",
    );

    fixture
        .command()
        .args(["init", "--no-labels"])
        .assert()
        .success();

    assert!(fixture.workflow().is_file());
    assert!(fixture.config_path().is_file());
}

#[test]
fn init_accepts_github_ssh_over_port_443_origin() {
    let fixture = Fixture::new();
    set_origin(
        &fixture.repository,
        "ssh://git@ssh.github.com:443/example/repository.git",
    );

    fixture
        .command()
        .args(["init", "--no-labels"])
        .assert()
        .success();

    assert!(fixture.workflow().is_file());
    assert!(fixture.config_path().is_file());
}

#[test]
fn label_discovery_uses_uncapped_paginated_api() {
    let fixture = Fixture::new();

    fixture.command().arg("init").assert().success();

    let calls = fs::read_to_string(fixture.repository.join(".gh-calls")).unwrap();
    assert!(calls.contains("api repos/{owner}/{repo}/labels --paginate --jq .[].name"));
    assert!(!calls.contains("--limit"));
}

#[test]
fn init_ignores_gh_repo_environment_override() {
    let fixture = Fixture::new();

    fixture
        .command()
        .env("GH_REPO", "wrong/repository")
        .args(["init", "--repository"])
        .arg(&fixture.repository)
        .assert()
        .success();

    assert!(fixture.workflow().is_file());
    assert_eq!(
        fs::read_to_string(fixture.repository.join(".factory-test-labels")).unwrap(),
        "factory:ready\nfactory:needs-review\n"
    );
}

#[test]
fn init_rejects_github_lookalike_origin_without_leaking_credentials() {
    let fixture = Fixture::new();
    set_origin(
        &fixture.repository,
        "https://user:secret@github.com.example.test/example/repository.git",
    );

    fixture
        .command()
        .args(["init", "--no-labels"])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "origin is not a supported GitHub remote",
        ))
        .stderr(predicate::str::contains("secret").not());

    assert!(!fixture.workflow().exists());
    assert!(!fixture.config_path().exists());
}

#[test]
fn partial_label_failure_reports_every_applied_and_failed_resource() {
    let fixture = Fixture::new();
    fs::write(fixture.repository.join(".fail-needs-review"), "").unwrap();

    fixture
        .command()
        .args(["init", "--repository"])
        .arg(&fixture.repository)
        .assert()
        .failure()
        .stdout(predicate::str::contains("created: "))
        .stdout(predicate::str::contains("GitHub label factory:ready"))
        .stdout(predicate::str::contains(
            "failed: GitHub label factory:needs-review",
        ))
        .stdout(predicate::str::contains("simulated label failure"))
        .stdout(predicate::str::contains("stopped after a failed resource"));

    assert!(fixture.config_path().is_file());
    assert!(fixture.workflow().is_file());
    assert_eq!(
        fs::read_to_string(fixture.repository.join(".factory-test-labels")).unwrap(),
        "factory:ready\n"
    );
}

#[test]
fn check_reports_every_missing_resource_without_writes() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args(["init", "--check", "--repository"])
        .arg(&fixture.repository)
        .assert()
        .failure()
        .stdout(predicate::str::contains("would create:"))
        .stdout(predicate::str::contains("Factory setup is incomplete"));

    assert!(!fixture.config_path().exists());
    assert!(!fixture.workspace().exists());
    assert!(!fixture.workflow().exists());
    assert!(!fixture.repository.join(".factory-test-labels").exists());
    let calls = fs::read_to_string(fixture.repository.join(".gh-calls")).unwrap();
    assert!(!calls.contains("label create"));
}

#[test]
fn check_does_not_probe_existing_workspace_writability() {
    let fixture = Fixture::new();
    fixture
        .command()
        .args(["init", "--no-labels"])
        .assert()
        .success();
    let mut permissions = fs::metadata(fixture.workspace()).unwrap().permissions();
    permissions.set_mode(0o555);
    fs::set_permissions(fixture.workspace(), permissions).unwrap();

    let output = fixture
        .command()
        .args(["init", "--no-labels", "--check"])
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
        .args(["init", "--no-labels", "--check"])
        .assert()
        .failure()
        .stdout(predicate::str::contains("would create:"))
        .stdout(predicate::str::contains("workspace directory"));
    assert!(!fixture.workspace().exists());

    fixture
        .command()
        .args(["init", "--no-labels"])
        .assert()
        .success()
        .stdout(predicate::str::contains("created:"))
        .stdout(predicate::str::contains("workspace directory"));

    assert!(fixture.workspace().is_dir());
    assert_eq!(fs::read_to_string(fixture.config_path()).unwrap(), original);
}

#[test]
fn init_rejects_parent_traversal_in_missing_workspace_without_writes() {
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
        .args(["init", "--no-labels"])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "must not contain parent traversal in a missing suffix",
        ));

    assert!(!fixture.repository.join("workspaces").exists());
    assert!(!fixture.workflow().exists());
}

#[test]
fn init_resolves_symlinked_config_ancestors_before_workspace_writes() {
    let fixture = Fixture::new();
    let state = fixture.repository.join(".factory-state");
    fs::create_dir(&state).unwrap();
    symlink(&state, fixture.home.join(".factory")).unwrap();

    fixture
        .command()
        .args(["init", "--no-labels"])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "generated configuration is invalid",
        ))
        .stderr(predicate::str::contains("must not overlap"));

    assert!(!state.join("workspaces").exists());
    assert!(!fixture.workflow().exists());
    assert!(!fixture.config_path().exists());
}

#[test]
fn no_labels_never_invokes_github_cli() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args(["init", "--no-labels"])
        .assert()
        .success()
        .stdout(predicate::str::contains("skipped: GitHub labels"));

    assert!(!fixture.repository.join(".gh-calls").exists());
    assert!(fixture.workflow().is_file());
    assert!(fixture.config_path().is_file());
}

#[test]
fn customized_workflow_requires_explicit_update() {
    let fixture = Fixture::new();
    fs::create_dir_all(fixture.workflow().parent().unwrap()).unwrap();
    fs::write(fixture.workflow(), "custom policy\n").unwrap();

    fixture
        .command()
        .args(["init", "--no-labels", "--repository"])
        .arg(&fixture.repository)
        .assert()
        .failure()
        .stdout(predicate::str::contains("conflict:"))
        .stdout(predicate::str::contains("--update-workflow"));
    assert_eq!(
        fs::read_to_string(fixture.workflow()).unwrap(),
        "custom policy\n"
    );
    assert!(!fixture.config_path().exists());

    fixture
        .command()
        .args(["init", "--no-labels", "--update-workflow", "--repository"])
        .arg(&fixture.repository)
        .assert()
        .success()
        .stdout(predicate::str::contains("updated:"));
    assert_eq!(
        fs::read_to_string(fixture.workflow()).unwrap(),
        include_str!("../examples/implement-ready-ticket.md")
    );
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
        fixture
            .command()
            .args(["init", "--no-labels", "--repository"])
            .arg(&fixture.repository)
            .assert()
            .success();
    }

    let contents = fs::read_to_string(fixture.config_path()).unwrap();
    assert!(contents.starts_with("# keep this comment\n"));
    assert_eq!(
        contents
            .matches(fixture.repository.to_str().unwrap())
            .count(),
        1
    );
    let config = Config::load(&fixture.config_path()).unwrap();
    assert_eq!(config.repositories.len(), 2);
}

#[test]
fn invalid_candidate_leaves_existing_config_and_workflow_untouched() {
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
        .args(["init", "--no-labels", "--repository"])
        .arg(&nested_repository)
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "generated configuration is invalid",
        ));

    assert_eq!(fs::read_to_string(fixture.config_path()).unwrap(), original);
    assert!(
        !nested_repository
            .join(".factory/workflows/implement-ready-ticket.md")
            .exists()
    );
}

#[test]
fn run_hints_at_init_when_ready_workflow_is_missing() {
    let fixture = Fixture::new();
    let workspace = fixture._temp.path().join("workspaces");
    write_config(
        &fixture.config_path(),
        &[&fixture.repository],
        &workspace,
        "",
    );
    fs::create_dir_all(fixture.repository.join(".factory/workflows")).unwrap();
    fs::write(
        fixture
            .repository
            .join(".factory/workflows/nightly-maintenance.md"),
        "+++\nschedule = \"0 2 * * *\"\ntimezone = \"UTC\"\n+++\n\nMaintain the repository.\n",
    )
    .unwrap();

    fixture
        .command()
        .args(["run", "--once", "--config"])
        .arg(fixture.config_path())
        .arg("--data-directory")
        .arg(fixture._temp.path().join("data"))
        .assert()
        .success()
        .stderr(predicate::str::contains(
            "No valid factory:ready implementation workflow found",
        ))
        .stderr(predicate::str::contains("run factory init --repository"));
}

#[test]
fn run_accepts_valid_custom_ready_workflow_as_initialized() {
    let fixture = Fixture::new();
    let workspace = fixture._temp.path().join("workspaces");
    write_config(
        &fixture.config_path(),
        &[&fixture.repository],
        &workspace,
        "",
    );
    let workflows = fixture.repository.join(".factory/workflows");
    fs::create_dir_all(&workflows).unwrap();
    fs::write(
        workflows.join("custom-ready-policy.md"),
        "+++\nlabel = \"factory:ready\"\n+++\n\nUse the repository-specific delivery policy.\n",
    )
    .unwrap();

    fixture
        .command()
        .args(["run", "--once", "--config"])
        .arg(fixture.config_path())
        .arg("--data-directory")
        .arg(fixture._temp.path().join("data"))
        .assert()
        .success()
        .stderr(
            predicate::str::contains("No valid factory:ready implementation workflow found").not(),
        );
}

#[test]
fn init_does_not_install_duplicate_beside_custom_ready_workflow() {
    let fixture = Fixture::new();
    let workflows = fixture.repository.join(".factory/workflows");
    let custom = workflows.join("custom-ready-policy.md");
    fs::create_dir_all(&workflows).unwrap();
    fs::write(
        &custom,
        "+++\nlabel = \"factory:ready\"\n+++\n\nUse the repository-specific delivery policy.\n",
    )
    .unwrap();

    fixture
        .command()
        .args(["init", "--no-labels"])
        .assert()
        .success()
        .stdout(predicate::str::contains(custom.to_str().unwrap()))
        .stdout(predicate::str::contains("unchanged:"))
        .stdout(predicate::str::contains("git -C").not())
        .stdout(predicate::str::contains("implement-ready-ticket.md").not());

    assert!(!fixture.workflow().exists());
    assert!(custom.is_file());
    assert!(fixture.config_path().is_file());
}

#[test]
fn update_workflow_replaces_custom_ready_workflow_in_place() {
    let fixture = Fixture::new();
    let workflows = fixture.repository.join(".factory/workflows");
    let custom = workflows.join("custom-ready-policy.md");
    fs::create_dir_all(&workflows).unwrap();
    fs::write(
        &custom,
        "+++\nlabel = \"factory:ready\"\n+++\n\nUse the repository-specific delivery policy.\n",
    )
    .unwrap();

    fixture
        .command()
        .args(["init", "--no-labels", "--update-workflow"])
        .assert()
        .success()
        .stdout(predicate::str::contains("updated:"))
        .stdout(predicate::str::contains(
            "git -C ".to_owned()
                + fixture.repository.canonicalize().unwrap().to_str().unwrap()
                + " add .factory/workflows/custom-ready-policy.md",
        ));

    assert_eq!(
        fs::read_to_string(&custom).unwrap(),
        include_str!("../examples/implement-ready-ticket.md")
    );
    assert!(!fixture.workflow().exists());
}
