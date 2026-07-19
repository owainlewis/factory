#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::process::Command as ProcessCommand;

use assert_cmd::Command;
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
        fs::create_dir(&repository).unwrap();
        let gh = temp.path().join("gh");
        fs::write(
            &gh,
            r#"#!/bin/sh
printf '%s\n' "$*" >> .gh-calls
if [ "$1" = "--version" ]; then echo "gh version 2.80.0"; exit 0; fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then echo "logged in"; exit 0; fi
if [ "$1" = "repo" ] && [ "$2" = "view" ]; then echo "example/repository"; exit 0; fi
if [ "$1" = "api" ]; then
  if [ -f .factory-test-labels ]; then cat .factory-test-labels; fi
  exit 0
fi
if [ "$1" = "label" ] && [ "$2" = "create" ]; then
  if [ -f .fail-label ]; then echo "simulated label failure" >&2; exit 1; fi
  printf '%s\n' "$3" >> .factory-test-labels
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
                    "git@github.com:example/repository.git",
                ])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        let fixture = Self {
            _temp: temp,
            home,
            repository,
            executable_path,
        };
        fixture.command().arg("init").assert().success();
        fixture
    }

    fn command(&self) -> Command {
        let mut command = Command::cargo_bin("factory").unwrap();
        command
            .current_dir(&self.repository)
            .env("HOME", &self.home)
            .env("PATH", &self.executable_path);
        command
    }

    fn workflow(&self, id: &str) -> PathBuf {
        self.repository
            .join(".factory/workflows")
            .join(format!("{id}.md"))
    }
}

#[test]
fn creates_scheduled_workflow_from_inline_prompt_without_editor() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args([
            "workflow",
            "create",
            "triage-pull-requests",
            "--schedule",
            "*/30 * * * *",
            "--timezone",
            "Europe/London",
            "--runtime",
            "codex",
            "--timeout",
            "1h",
            "--prompt",
            "Review and triage open pull requests without labels.",
        ])
        .env("EDITOR", "/path/that/must/not/run")
        .assert()
        .success()
        .stdout(predicate::str::contains("Created workflow"))
        .stdout(predicate::str::contains("git -C"));

    assert_eq!(
        fs::read_to_string(fixture.workflow("triage-pull-requests")).unwrap(),
        "+++\nschedule = \"*/30 * * * *\"\ntimezone = \"Europe/London\"\nruntime = \"codex\"\ntimeout = \"1h\"\n+++\n\nReview and triage open pull requests without labels.\n"
    );
    assert!(!fixture.repository.join(".gh-calls").exists());
}

#[test]
fn creates_label_workflow_from_prompt_file() {
    let fixture = Fixture::new();
    let prompt = fixture._temp.path().join("policy.md");
    fs::write(
        &prompt,
        "# Review a ticket\n\nClassify the supplied ticket.\n",
    )
    .unwrap();

    fixture
        .command()
        .args(["workflow", "create", "triage-ticket", "--label", "triage"])
        .arg("--prompt-file")
        .arg(&prompt)
        .assert()
        .success()
        .stdout(predicate::str::contains("Created GitHub label triage"));

    let contents = fs::read_to_string(fixture.workflow("triage-ticket")).unwrap();
    assert!(contents.contains("label = \"triage\""));
    assert!(contents.contains("# Review a ticket"));
    assert_eq!(
        fs::read_to_string(fixture.repository.join(".factory-test-labels")).unwrap(),
        "triage\n"
    );
}

#[test]
fn creates_workflow_from_standard_input() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args([
            "workflow",
            "create",
            "stdin-policy",
            "--label",
            "triage",
            "--prompt-file",
            "-",
        ])
        .write_stdin("Use the supplied ticket.\n")
        .assert()
        .success();

    assert!(
        fs::read_to_string(fixture.workflow("stdin-policy"))
            .unwrap()
            .contains("Use the supplied ticket.")
    );
}

#[test]
fn existing_trigger_label_is_not_recreated() {
    let fixture = Fixture::new();
    fs::write(fixture.repository.join(".factory-test-labels"), "triage\n").unwrap();

    fixture
        .command()
        .args([
            "workflow",
            "create",
            "existing-label",
            "--label",
            "triage",
            "--prompt",
            "Do work.",
        ])
        .assert()
        .success()
        .stdout(predicate::str::contains("Created GitHub label").not());

    let calls = fs::read_to_string(fixture.repository.join(".gh-calls")).unwrap();
    assert!(!calls.contains("label create"));
}

#[test]
fn label_creation_failure_rolls_back_new_workflow() {
    let fixture = Fixture::new();
    fs::write(fixture.repository.join(".fail-label"), "").unwrap();

    fixture
        .command()
        .args([
            "workflow",
            "create",
            "failed-label",
            "--label",
            "triage",
            "--prompt",
            "Do work.",
        ])
        .assert()
        .failure()
        .stderr(predicate::str::contains("failed to create GitHub label"));

    assert!(!fixture.workflow("failed-label").exists());
}

#[test]
fn requires_explicit_trigger_and_prompt_source() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args([
            "workflow",
            "create",
            "missing-trigger",
            "--prompt",
            "Do work.",
        ])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "--schedule <SCHEDULE>|--label <LABEL>",
        ));

    fixture
        .command()
        .args(["workflow", "create", "missing-prompt", "--label", "triage"])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "--prompt <PROMPT>|--prompt-file <PATH>",
        ));
}

#[test]
fn schedule_requires_timezone() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args([
            "workflow",
            "create",
            "scheduled",
            "--schedule",
            "0 9 * * 1",
            "--prompt",
            "Do work.",
        ])
        .assert()
        .failure()
        .stderr(predicate::str::contains("--timezone <TIMEZONE>"));
}

#[test]
fn invalid_workflow_is_not_left_behind() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args([
            "workflow",
            "create",
            "bad-schedule",
            "--schedule",
            "eventually",
            "--timezone",
            "UTC",
            "--prompt",
            "Do work.",
        ])
        .assert()
        .failure()
        .stderr(predicate::str::contains("workflow is invalid"));

    assert!(!fixture.workflow("bad-schedule").exists());
}

#[test]
fn refuses_to_overwrite_existing_workflow() {
    let fixture = Fixture::new();
    let path = fixture.workflow("existing");
    fs::write(&path, "keep me\n").unwrap();

    fixture
        .command()
        .args([
            "workflow",
            "create",
            "existing",
            "--label",
            "triage",
            "--prompt",
            "Replace it.",
        ])
        .assert()
        .failure()
        .stderr(predicate::str::contains("workflow already exists"));

    assert_eq!(fs::read_to_string(path).unwrap(), "keep me\n");
}

#[test]
fn rejects_invalid_workflow_id_before_writing() {
    let fixture = Fixture::new();

    fixture
        .command()
        .args([
            "workflow",
            "create",
            "../escape",
            "--label",
            "triage",
            "--prompt",
            "Do work.",
        ])
        .assert()
        .failure()
        .stderr(predicate::str::contains("lowercase kebab-case"));

    assert!(!fixture.repository.join(".factory/escape.md").exists());
}

#[test]
fn requires_repository_initialization() {
    let fixture = Fixture::new();
    fs::remove_dir(fixture.repository.join(".factory/workflows")).unwrap();

    fixture
        .command()
        .args([
            "workflow", "create", "triage", "--label", "triage", "--prompt", "Do work.",
        ])
        .assert()
        .failure()
        .stderr(predicate::str::contains("run factory init"));
}

#[test]
fn staging_command_is_shell_safe() {
    let mut fixture = Fixture::new();
    let old_repository = fixture.repository.canonicalize().unwrap();
    let repository = fixture._temp.path().join("repository with ' quote");
    fs::rename(&fixture.repository, &repository).unwrap();
    fixture.repository = repository;
    let config = fixture.home.join(".factory/config.toml");
    let contents = fs::read_to_string(&config).unwrap().replace(
        old_repository.to_str().unwrap(),
        fixture.repository.canonicalize().unwrap().to_str().unwrap(),
    );
    fs::write(config, contents).unwrap();

    let output = fixture
        .command()
        .args([
            "workflow", "create", "safe", "--label", "triage", "--prompt", "Do work.",
        ])
        .output()
        .unwrap();
    assert!(output.status.success());
    let stdout = String::from_utf8(output.stdout).unwrap();
    let command = stdout
        .lines()
        .find(|line| line.trim_start().starts_with("git -C "))
        .unwrap()
        .trim();
    assert!(
        ProcessCommand::new("sh")
            .args(["-c", command])
            .status()
            .unwrap()
            .success()
    );
}
