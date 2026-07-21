#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Command;

use assert_cmd::Command as AssertCommand;
use factory::agent::RunPolicy;
use factory::storage::{Ledger, TaskIdentity, TaskWorkspace};
use factory::workspace::{DeliveryReuse, WorkspaceManager};
use predicates::prelude::*;
use sha2::{Digest, Sha256};

struct Fixture {
    _temp: tempfile::TempDir,
    repository: PathBuf,
    workspace: PathBuf,
    factory_branch: String,
    ledger_path: PathBuf,
    run_id: i64,
    token: String,
    bin: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repository");
        let remote = temp.path().join("origin.git");
        let workspace_root = temp.path().join("worktrees");
        let bin = temp.path().join("bin");
        fs::create_dir(&repository).unwrap();
        fs::create_dir(&workspace_root).unwrap();
        fs::create_dir(&bin).unwrap();
        let gh = bin.join("gh");
        fs::write(
            &gh,
            r#"#!/bin/sh
set -eu
printf '%s\n' "$@" >> factory-gh.log
if [ -n "${FACTORY_TEST_GH_FAIL_ONCE:-}" ] && [ -f "$FACTORY_TEST_GH_FAIL_ONCE" ]; then
  rm "$FACTORY_TEST_GH_FAIL_ONCE"
  echo "transient GitHub failure" >&2
  exit 90
fi
if [ "$1" = "api" ] && [ "$2" = "--paginate" ]; then
  case "$4" in
    */pulls*)
      if [ -f pulls.json ]; then cat pulls.json
      elif [ -f factory-pr-created ]; then
        printf '%s\n' '[[{"number":7,"html_url":"https://github.com/example/agent-fixture/pull/7","draft":true,"state":"open","merged_at":null,"head":{"ref":"factory/7-agent-commands","repo":{"full_name":"example/agent-fixture"}}}]]'
      else printf '%s\n' '[[]]'
      fi
      ;;
    *) printf '%s\n' '[[]]' ;;
  esac
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "--method" ] && [ "$3" = "POST" ]; then
  case "$4" in
    */pulls)
      touch factory-pr-created
      printf '%s\n' '{"number":7,"html_url":"https://github.com/example/agent-fixture/pull/7","draft":true,"state":"open","merged_at":null,"head":{"ref":"factory/7-agent-commands","repo":{"full_name":"example/agent-fixture"}}}'
      ;;
    *) printf '%s\n' '123' ;;
  esac
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "--method" ] && [ "$3" = "PATCH" ]; then
  printf '%s\n' '{"number":7,"html_url":"https://github.com/example/agent-fixture/pull/7","draft":true,"state":"open","merged_at":null,"head":{"ref":"factory/7-agent-commands","repo":{"full_name":"example/agent-fixture"}}}'
  exit 0
fi
exit 91
"#,
        )
        .unwrap();
        let mut permissions = fs::metadata(&gh).unwrap().permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(&gh, permissions).unwrap();
        git(&repository, &["init", "-b", "main"]);
        git(
            &repository,
            &["config", "user.email", "factory@example.test"],
        );
        git(&repository, &["config", "user.name", "Factory Test"]);
        fs::write(repository.join("README.md"), "fixture\n").unwrap();
        git(&repository, &["add", "README.md"]);
        git(&repository, &["commit", "-m", "fixture"]);
        git(temp.path(), &["init", "--bare", remote.to_str().unwrap()]);
        let github_remote = "git@github.com:example/agent-fixture.git";
        git(&repository, &["remote", "add", "origin", github_remote]);
        let rewrite = format!("url.file://{}.insteadOf", remote.display());
        git(&repository, &["config", &rewrite, github_remote]);
        git(&repository, &["push", "-u", "origin", "main"]);
        let repository = repository.canonicalize().unwrap();
        let workspace_root = workspace_root.canonicalize().unwrap();
        let manager = WorkspaceManager::new(&repository, &workspace_root).unwrap();
        let base_sha = manager.fetch_default_branch("main").unwrap();
        let prepared = manager
            .prepare_delivery(
                7,
                "Agent commands",
                "main",
                &base_sha,
                DeliveryReuse::Reject,
            )
            .unwrap();

        let ledger_path = temp.path().join("factory.sqlite3");
        let mut ledger = Ledger::open(&ledger_path).unwrap();
        let task = ledger
            .enqueue_with_payload(
                &TaskIdentity::ticket(
                    "example/agent-fixture",
                    "implement-ready-ticket",
                    "7",
                    "revision-1",
                )
                .unwrap(),
                Some(r#"{"number":7,"title":"Agent commands"}"#),
            )
            .unwrap()
            .task;
        ledger.claim_next().unwrap().unwrap();
        let run = ledger.start_run(task.id, "codex").unwrap();
        ledger
            .reserve_task_workspace(&TaskWorkspace {
                task_id: task.id,
                kind: "delivery".into(),
                repository: task.repository.clone(),
                base_branch: "main".into(),
                base_sha: base_sha.clone(),
                factory_branch: prepared.branch.clone(),
                path: prepared.path.clone(),
                state: "preparing".into(),
                status_summary: None,
                created_at: 0,
                updated_at: 0,
                cleaned_at: None,
            })
            .unwrap();
        ledger
            .update_task_workspace_state(task.id, "ready", None)
            .unwrap();
        ledger
            .record_run_workspace(
                run.id,
                &prepared.path,
                "main",
                &base_sha,
                prepared.branch.as_deref(),
                "delivery",
            )
            .unwrap();
        let policy = RunPolicy {
            version: 1,
            repository: task.repository,
            canonical_repository: repository.clone(),
            workspace_root,
            worktree: prepared.path.clone(),
            effect: "delivery".into(),
            ready_label: "factory:ready".into(),
            proposed_label: "factory:proposed".into(),
            needs_review_label: "factory:needs-review".into(),
        };
        let token = "test-capability".to_owned();
        let token_hash = format!("sha256:{:x}", Sha256::digest(token.as_bytes()));
        ledger
            .activate_run_context(
                run.id,
                "delivery",
                "v2:test-workflow",
                &serde_json::to_string(&policy).unwrap(),
                &token_hash,
            )
            .unwrap();
        Self {
            _temp: temp,
            repository,
            workspace: prepared.path,
            factory_branch: prepared.branch.unwrap(),
            ledger_path,
            run_id: run.id,
            token,
            bin,
        }
    }

    fn command(&self) -> AssertCommand {
        let mut command = AssertCommand::cargo_bin("factory").unwrap();
        command
            .current_dir(&self.workspace)
            .env("FACTORY_RUN_ID", self.run_id.to_string())
            .env("FACTORY_LEDGER_PATH", &self.ledger_path)
            .env("FACTORY_RUN_TOKEN", &self.token)
            .env(
                "PATH",
                std::env::join_paths(std::iter::once(self.bin.clone()).chain(
                    std::env::split_paths(&std::env::var_os("PATH").unwrap_or_default()),
                ))
                .unwrap(),
            );
        command
    }
}

#[test]
fn task_commands_are_bound_to_the_active_worktree_and_capability() {
    let fixture = Fixture::new();
    fixture
        .command()
        .args(["task", "show"])
        .assert()
        .success()
        .stdout(predicate::str::contains(r#""effect":"delivery""#));

    let mut wrong_directory = fixture.command();
    wrong_directory
        .current_dir(&fixture.repository)
        .args(["task", "show"])
        .assert()
        .failure()
        .stderr(predicate::str::contains("active Factory worktree root"));

    let mut wrong_token = fixture.command();
    wrong_token
        .env("FACTORY_RUN_TOKEN", "wrong")
        .args(["task", "show"])
        .assert()
        .failure()
        .stderr(predicate::str::contains("capability is invalid"));

    let effects = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .run_effects(fixture.run_id)
        .unwrap();
    assert_eq!(
        effects
            .iter()
            .filter(|effect| effect.outcome == "rejected")
            .count(),
        2
    );
}

#[test]
fn strict_payloads_and_effect_profiles_reject_expanded_authority() {
    let fixture = Fixture::new();
    let proposal = fixture._temp.path().join("proposal.json");
    fs::write(
        &proposal,
        r#"{"version":1,"idempotency_key":"proposal-1","title":"Proposal","problem":"Problem","acceptance_criteria":["Done"]}"#,
    )
    .unwrap();
    fixture
        .command()
        .args(["proposal", "create", "--file", proposal.to_str().unwrap()])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "delivery workflow cannot perform an operation requiring proposal effect",
        ));

    let publish = fixture._temp.path().join("publish.json");
    fs::write(
        &publish,
        r#"{"version":1,"idempotency_key":"publish-1","title":"Change","summary":"Summary","tests":[],"merge":true}"#,
    )
    .unwrap();
    fixture
        .command()
        .args(["change", "publish", "--file", publish.to_str().unwrap()])
        .assert()
        .failure()
        .stderr(predicate::str::contains("does not match v1 schema"));

    let effects = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .run_effects(fixture.run_id)
        .unwrap();
    assert_eq!(effects.len(), 2);
    assert!(effects.iter().all(|effect| effect.outcome == "rejected"));
}

#[test]
fn a_pending_idempotency_key_cannot_execute_twice() {
    let fixture = Fixture::new();
    let payload = fixture._temp.path().join("comment.json");
    let raw = r#"{"version":1,"idempotency_key":"comment-1","body":"Working on it"}"#;
    fs::write(&payload, raw).unwrap();
    let payload_hash = format!("sha256:{:x}", Sha256::digest(raw.as_bytes()));
    Ledger::open(&fixture.ledger_path)
        .unwrap()
        .reserve_run_effect(
            fixture.run_id,
            "task.comment",
            "delivery",
            "comment-1",
            1,
            &payload_hash,
        )
        .unwrap();

    fixture
        .command()
        .args(["task", "comment", "--file", payload.to_str().unwrap()])
        .assert()
        .failure()
        .stderr(predicate::str::contains(
            "idempotency key is already in progress",
        ));
}

#[test]
fn a_failed_external_effect_can_retry_with_the_same_idempotency_key() {
    let fixture = Fixture::new();
    let payload = fixture._temp.path().join("comment-retry.json");
    fs::write(
        &payload,
        r#"{"version":1,"idempotency_key":"comment-retry","body":"Working on it"}"#,
    )
    .unwrap();
    let fail_once = fixture._temp.path().join("fail-gh-once");
    fs::write(&fail_once, "fail").unwrap();

    fixture
        .command()
        .env("FACTORY_TEST_GH_FAIL_ONCE", &fail_once)
        .args(["task", "comment", "--file", payload.to_str().unwrap()])
        .assert()
        .failure()
        .stderr(predicate::str::contains("transient GitHub failure"));
    fixture
        .command()
        .env("FACTORY_TEST_GH_FAIL_ONCE", &fail_once)
        .args(["task", "comment", "--file", payload.to_str().unwrap()])
        .assert()
        .success();

    let effects = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .run_effects(fixture.run_id)
        .unwrap();
    assert_eq!(
        effects
            .iter()
            .filter(|effect| effect.action == "task.comment")
            .map(|effect| effect.outcome.as_str())
            .collect::<Vec<_>>(),
        ["failed", "applied"]
    );
}

#[test]
fn change_publish_pushes_the_recorded_branch_and_reuses_one_draft_pull_request() {
    let fixture = Fixture::new();
    let first = fixture._temp.path().join("publish-1.json");
    let second = fixture._temp.path().join("publish-2.json");
    fs::write(
        &first,
        r#"{"version":1,"idempotency_key":"publish-1","title":"Agent commands","summary":"First publication","tests":["cargo test"]}"#,
    )
    .unwrap();
    fs::write(
        &second,
        r#"{"version":1,"idempotency_key":"publish-2","title":"Agent commands updated","summary":"Updated publication","tests":["cargo test"]}"#,
    )
    .unwrap();

    fixture
        .command()
        .args(["change", "publish", "--file", first.to_str().unwrap()])
        .assert()
        .success()
        .stdout(predicate::str::contains(
            "https://github.com/example/agent-fixture/pull/7",
        ));
    let log_after_first = fs::read_to_string(fixture.workspace.join("factory-gh.log")).unwrap();
    fixture
        .command()
        .args(["change", "publish", "--file", first.to_str().unwrap()])
        .assert()
        .success();
    assert_eq!(
        fs::read_to_string(fixture.workspace.join("factory-gh.log")).unwrap(),
        log_after_first,
        "an exact idempotent replay must not call GitHub again"
    );
    fixture
        .command()
        .args(["change", "publish", "--file", second.to_str().unwrap()])
        .assert()
        .success();

    let log = fs::read_to_string(fixture.workspace.join("factory-gh.log")).unwrap();
    assert_eq!(log.lines().filter(|line| *line == "POST").count(), 1);
    assert_eq!(log.lines().filter(|line| *line == "PATCH").count(), 1);
    let ledger = Ledger::open(&fixture.ledger_path).unwrap();
    assert_eq!(
        ledger
            .run_effects(fixture.run_id)
            .unwrap()
            .iter()
            .filter(|effect| effect.action == "change.publish" && effect.outcome == "applied")
            .count(),
        2
    );
    assert_eq!(
        ledger
            .run(fixture.run_id)
            .unwrap()
            .unwrap()
            .pull_request
            .as_deref(),
        Some("https://github.com/example/agent-fixture/pull/7")
    );
}

#[test]
fn change_publish_rejects_and_audits_a_merged_pull_request() {
    let fixture = Fixture::new();
    fs::write(
        fixture.workspace.join("pulls.json"),
        format!(
            r#"[[{{"number":7,"html_url":"https://github.com/example/agent-fixture/pull/7","draft":false,"state":"closed","merged_at":"2026-07-21T12:00:00Z","head":{{"ref":"{}","repo":{{"full_name":"example/agent-fixture"}}}}}}]]"#,
            fixture.factory_branch
        ),
    )
    .unwrap();
    let payload = fixture._temp.path().join("publish-merged.json");
    fs::write(
        &payload,
        r#"{"version":1,"idempotency_key":"publish-merged","title":"Agent commands","summary":"Publication","tests":[]}"#,
    )
    .unwrap();

    fixture
        .command()
        .args(["change", "publish", "--file", payload.to_str().unwrap()])
        .assert()
        .failure()
        .stderr(predicate::str::contains("already merged"));

    let remote_ref = format!("refs/heads/{}", fixture.factory_branch);
    let remote = Command::new("git")
        .args(["ls-remote", "--heads", "origin", &remote_ref])
        .current_dir(&fixture.workspace)
        .output()
        .unwrap();
    assert!(remote.status.success());
    assert!(
        remote.stdout.is_empty(),
        "rejected publication pushed a branch"
    );
    let log = fs::read_to_string(fixture.workspace.join("factory-gh.log")).unwrap();
    assert!(!log.lines().any(|line| line == "POST"));
    let effects = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .run_effects(fixture.run_id)
        .unwrap();
    assert!(effects.iter().any(|effect| {
        effect.action == "change.publish"
            && effect.outcome == "failed"
            && effect.detail.contains("already merged")
    }));
}

#[test]
fn run_complete_is_idempotent_and_records_the_structured_handoff() {
    let fixture = Fixture::new();
    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    ledger
        .observe_run(
            fixture.run_id,
            None,
            None,
            None,
            Some("https://github.com/example/agent-fixture/pull/1"),
            None,
        )
        .unwrap();
    drop(ledger);
    let payload = fixture._temp.path().join("complete.json");
    fs::write(
        &payload,
        r#"{"version":1,"idempotency_key":"complete-1","summary":"Ready for review","checks":["cargo test"]}"#,
    )
    .unwrap();

    fixture
        .command()
        .args(["run", "complete", "--file", payload.to_str().unwrap()])
        .assert()
        .failure()
        .stderr(predicate::str::contains("exact Factory branch is pushed"));
    let refspec = format!("refs/heads/{0}:refs/heads/{0}", fixture.factory_branch);
    git(&fixture.workspace, &["push", "origin", &refspec]);

    for _ in 0..2 {
        fixture
            .command()
            .args(["run", "complete", "--file", payload.to_str().unwrap()])
            .assert()
            .success()
            .stdout(predicate::str::contains(r#""outcome":"applied""#));
    }

    let ledger = Ledger::open(&fixture.ledger_path).unwrap();
    let run = ledger.run(fixture.run_id).unwrap().unwrap();
    assert_eq!(run.disposition.as_deref(), Some("completed"));
    assert_eq!(
        ledger
            .run_effects(fixture.run_id)
            .unwrap()
            .iter()
            .filter(|effect| effect.action == "run.complete" && effect.outcome == "applied")
            .count(),
        1
    );
}

fn git(directory: &Path, arguments: &[&str]) {
    let output = Command::new("git")
        .args(arguments)
        .current_dir(directory)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "git {} failed: {}",
        arguments.join(" "),
        String::from_utf8_lossy(&output.stderr)
    );
}
