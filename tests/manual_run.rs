#![cfg(unix)]

use std::env;
use std::fs;
use std::os::unix::fs::PermissionsExt;

use assert_cmd::Command;
use predicates::prelude::*;

#[test]
fn manual_workflow_run_resolves_context_and_invokes_codex() {
    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository");
    let workflows = repository.join(".factory/workflows");
    let workspace = temp.path().join("worktrees");
    let binaries = temp.path().join("bin");
    fs::create_dir_all(&workflows).unwrap();
    fs::create_dir(&workspace).unwrap();
    fs::create_dir(&binaries).unwrap();
    fs::write(
        workflows.join("read-only.md"),
        "+++\nlabel = \"factory:ready\"\ntimeout = \"30s\"\n+++\n\nInspect without changing files.\n",
    )
    .unwrap();
    let config = temp.path().join("config.toml");
    fs::write(
        &config,
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
    let prompt_capture = temp.path().join("prompt.txt");
    let executable = binaries.join("codex");
    fs::write(
        &executable,
        r#"#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "codex-cli 1.2.3"
  exit 0
fi
if [ "$1" = "login" ] && [ "$2" = "status" ]; then
  echo "Logged in using ChatGPT"
  exit 0
fi
output=""
previous=""
for argument in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then
    output="$argument"
  fi
  previous="$argument"
done
cat > "$FACTORY_PROMPT_CAPTURE"
echo '{"type":"thread.started","thread_id":"manual-thread"}'
printf 'Read-only workflow complete.' > "$output"
"#,
    )
    .unwrap();
    let mut permissions = fs::metadata(&executable).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&executable, permissions).unwrap();
    let path = format!(
        "{}:{}",
        binaries.display(),
        env::var("PATH").unwrap_or_default()
    );

    Command::cargo_bin("factory")
        .unwrap()
        .args([
            "workflow",
            "run",
            "read-only",
            "--repository",
            repository.to_str().unwrap(),
            "--config",
            config.to_str().unwrap(),
        ])
        .env("PATH", path)
        .env("FACTORY_PROMPT_CAPTURE", &prompt_capture)
        .assert()
        .success()
        .stdout(predicate::str::contains(
            r#"{"type":"thread.started","thread_id":"manual-thread"}"#,
        ))
        .stdout(predicate::str::contains("Read-only workflow complete."))
        .stderr(predicate::str::contains("Codex ready: codex-cli 1.2.3"))
        .stderr(predicate::str::contains("thread=manual-thread"));

    let prompt = fs::read_to_string(prompt_capture).unwrap();
    assert!(prompt.contains("Inspect without changing files."));
    assert!(prompt.contains("Workflow: read-only"));
    assert!(prompt.contains(repository.to_str().unwrap()));
    assert!(!prompt.contains(workspace.to_str().unwrap()));
    assert!(!prompt.contains("max_concurrent_runs"));
}
