#![cfg(unix)]

use std::env;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::os::unix::net::UnixStream;
use std::process::Stdio;
use std::thread;
use std::time::{Duration, Instant};

use assert_cmd::Command;
use predicates::prelude::*;

fn initialize_repository(repository: &std::path::Path, data_home: &std::path::Path) {
    assert!(
        std::process::Command::new("git")
            .args(["init", "--quiet"])
            .current_dir(repository)
            .status()
            .unwrap()
            .success()
    );
    assert!(
        std::process::Command::new("git")
            .args([
                "remote",
                "add",
                "origin",
                "git@github.com:example/repository.git"
            ])
            .current_dir(repository)
            .status()
            .unwrap()
            .success()
    );
    Command::cargo_bin("factory")
        .unwrap()
        .current_dir(repository)
        .env("FACTORY_DATA_HOME", data_home)
        .arg("init")
        .assert()
        .success();
}

#[test]
fn manual_workflow_run_resolves_context_and_invokes_codex() {
    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository");
    let workflows = repository.join(".factory/workflows");
    let workspace = temp.path().join("worktrees");
    let data_home = temp.path().join("factory-data");
    let binaries = temp.path().join("bin");
    fs::create_dir_all(&workflows).unwrap();
    fs::create_dir(&binaries).unwrap();
    fs::write(
        workflows.join("read-only.md"),
        "+++\nlabel = \"factory:ready\"\ntimeout = \"30s\"\n+++\n\nInspect without changing files.\n",
    )
    .unwrap();
    initialize_repository(&repository, &data_home);
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
        .args(["workflow", "run", "read-only"])
        .current_dir(&repository)
        .env("FACTORY_DATA_HOME", &data_home)
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

#[test]
fn concurrent_manual_runs_exit_when_shared_output_is_full_and_unread() {
    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository");
    let workflows = repository.join(".factory/workflows");
    let data_home = temp.path().join("factory-data");
    let binaries = temp.path().join("bin");
    fs::create_dir_all(&workflows).unwrap();
    fs::create_dir(&binaries).unwrap();
    fs::write(
        workflows.join("verbose.md"),
        "+++\nlabel = \"factory:ready\"\ntimeout = \"30s\"\n+++\n\nBe verbose.\n",
    )
    .unwrap();
    initialize_repository(&repository, &data_home);
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
cat >/dev/null
i=0
while [ "$i" -lt 10000 ]; do
  echo '{"type":"item.completed","message":"activity output that fills the pipe"}'
  echo 'diagnostic output that fills the pipe' >&2
  i=$((i + 1))
done
printf 'Verbose workflow complete.' > "$output"
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

    let (_unread_output, shared_output) = UnixStream::pair().unwrap();
    let mut children = (0..2)
        .map(|_| {
            std::process::Command::new(assert_cmd::cargo::cargo_bin!("factory"))
                .args(["workflow", "run", "verbose"])
                .current_dir(&repository)
                .env("FACTORY_DATA_HOME", &data_home)
                .env("PATH", &path)
                .stdout(Stdio::from(std::os::fd::OwnedFd::from(
                    shared_output.try_clone().unwrap(),
                )))
                .stderr(Stdio::from(std::os::fd::OwnedFd::from(
                    shared_output.try_clone().unwrap(),
                )))
                .spawn()
                .unwrap()
        })
        .collect::<Vec<_>>();
    drop(shared_output);
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut statuses = vec![None; children.len()];
    loop {
        for (child, status) in children.iter_mut().zip(&mut statuses) {
            if status.is_none() {
                *status = child.try_wait().unwrap();
            }
        }
        if statuses.iter().all(Option::is_some) {
            break;
        }
        if Instant::now() >= deadline {
            for (child, status) in children.iter_mut().zip(&statuses) {
                if status.is_none() {
                    child.kill().unwrap();
                    child.wait().unwrap();
                }
            }
            panic!("Factory runs hung while shared stdout and stderr were full and unread");
        }
        thread::sleep(Duration::from_millis(10));
    }

    for status in statuses.into_iter().flatten() {
        assert!(status.success(), "Factory exited with {status}");
    }
}
