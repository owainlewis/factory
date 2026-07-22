use assert_cmd::Command;
use factory::storage::{Ledger, RunOutcome, RunSandbox, TaskIdentity};
use predicates::prelude::*;

struct Fixture {
    _temp: tempfile::TempDir,
    data: std::path::PathBuf,
    queued_id: i64,
    running_run_id: i64,
    failed_run_id: i64,
    cancelled_run_id: i64,
    succeeded_run_id: i64,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let data = temp.path().join("data");
        let mut ledger = Ledger::open_in(&data).unwrap();

        let running_task = enqueue(&mut ledger, "running", "implement-ready-ticket");
        claim(&mut ledger, running_task);
        let running_run_id = ledger.start_run(running_task, "codex").unwrap().id;
        ledger
            .record_run_sandbox(&RunSandbox {
                run_id: running_run_id,
                sandbox_name: format!("factory-test-{running_run_id}"),
                instance_id: "test".into(),
                template_ref: "docker/sandbox-templates:codex".into(),
                sbx_version: "sbx version 0.35.0".into(),
                limits_json: r#"{"memory":"8g","cpus":4}"#.into(),
                state: "created".into(),
                exit_code: None,
                logs: None,
                created_at: 100,
                updated_at: 100,
                removed_at: None,
            })
            .unwrap();

        let failed_task = enqueue(&mut ledger, "failed", "implement-ready-ticket");
        claim(&mut ledger, failed_task);
        let failed_run = ledger.start_run(failed_task, "codex").unwrap();
        let failed_run_id = ledger
            .finish_run_and_task(
                failed_run.id,
                RunOutcome::Failed,
                Some("partial result\nGITHUB_TOKEN=ghp_supersecret\nAWS_ACCESS_KEY_ID=AKIAEXAMPLE\nAuthorization: Bearer abc123\npostgres://standalone:password@host/db\nAKIAIOSFODNN7EXAMPLE"),
                Some(
                    "failure \u{1b}[31m detail DATABASE_URL=postgres://user:pass@host/db\nPASSWORD=\"correct horse battery staple\"\n{\"PASSWORD\":\"abc\\\"remaining secret\"}\n-----BEGIN PRIVATE KEY-----\ntruncated-key-material",
                ),
                Some("thread-failed"),
            )
            .unwrap()
            .id;

        let cancelled_task = enqueue(&mut ledger, "cancelled", "implement-ready-ticket");
        claim(&mut ledger, cancelled_task);
        let cancelled_run = ledger.start_run(cancelled_task, "codex").unwrap();
        let cancelled_run_id = ledger
            .finish_run_and_task(
                cancelled_run.id,
                RunOutcome::Cancelled,
                None,
                Some("cancelled"),
                None,
            )
            .unwrap()
            .id;

        let succeeded_task = enqueue(&mut ledger, "succeeded", "find-bugs");
        claim(&mut ledger, succeeded_task);
        let succeeded_run = ledger.start_run(succeeded_task, "codex").unwrap();
        let succeeded_run_id = ledger
            .finish_run_and_task(
                succeeded_run.id,
                RunOutcome::Succeeded,
                Some(&"x".repeat(20_000)),
                None,
                Some("thread-succeeded"),
            )
            .unwrap()
            .id;
        let queued_id = enqueue(&mut ledger, "queued", "implement-ready-ticket");

        Self {
            _temp: temp,
            data,
            queued_id,
            running_run_id,
            failed_run_id,
            cancelled_run_id,
            succeeded_run_id,
        }
    }
}

fn enqueue(ledger: &mut Ledger, revision: &str, workflow: &str) -> i64 {
    ledger
        .enqueue_with_payload(
            &TaskIdentity::ticket("example/repo", workflow, revision, revision).unwrap(),
            Some("unbounded ticket body is not printed\nSECRET=not-for-output"),
        )
        .unwrap()
        .task
        .id
}

fn claim(ledger: &mut Ledger, expected: i64) {
    assert_eq!(ledger.claim_next().unwrap().unwrap().id, expected);
}

fn command(fixture: &Fixture, args: &[&str]) -> Command {
    let mut command = Command::cargo_bin("factory").unwrap();
    command
        .env("HOME", fixture._temp.path().join("home"))
        .args(args)
        .args(["--data-directory", fixture.data.to_str().unwrap()]);
    command
}

#[test]
fn tasks_lists_all_states_and_json_is_parseable_without_payloads() {
    let fixture = Fixture::new();

    command(&fixture, &["tasks"])
        .assert()
        .success()
        .stdout(predicate::str::contains("queued"))
        .stdout(predicate::str::contains("running"))
        .stdout(predicate::str::contains("succeeded"))
        .stdout(predicate::str::contains("failed"))
        .stdout(predicate::str::contains("cancelled"))
        .stdout(predicate::str::contains("SECRET=").not());

    let output = command(&fixture, &["tasks", "--json"]).output().unwrap();
    assert!(output.status.success());
    let value: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
    let tasks = value.as_array().unwrap();
    assert_eq!(tasks.len(), 5);
    assert!(tasks.iter().any(|task| task["id"] == fixture.queued_id));
    assert!(tasks.iter().all(|task| task.get("payload").is_none()));
    let keys = tasks[0]
        .as_object()
        .unwrap()
        .keys()
        .cloned()
        .collect::<Vec<_>>();
    assert_eq!(
        keys,
        [
            "created_at",
            "id",
            "repository",
            "source_item",
            "state",
            "updated_at",
            "workflow",
        ]
    );
}

#[test]
fn runs_filters_by_workflow_and_includes_bounded_summaries() {
    let fixture = Fixture::new();

    let output = command(&fixture, &["runs", "implement-ready-ticket", "--json"])
        .output()
        .unwrap();
    assert!(output.status.success());
    let value: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
    let runs = value.as_array().unwrap();
    assert_eq!(runs.len(), 3);
    assert!(
        runs.iter()
            .all(|run| run["workflow"] == "implement-ready-ticket")
    );
    assert!(runs.iter().any(|run| run["outcome"] == "running"));
    assert!(runs.iter().any(|run| run["outcome"] == "failed"));
    assert!(runs.iter().any(|run| run["outcome"] == "cancelled"));
    assert!(runs.iter().all(|run| {
        !run["summary"]
            .as_str()
            .unwrap_or_default()
            .contains("postgres://")
    }));
    let keys = runs[0]
        .as_object()
        .unwrap()
        .keys()
        .cloned()
        .collect::<Vec<_>>();
    assert_eq!(
        keys,
        [
            "base_branch",
            "base_sha",
            "cancellation_requested_at",
            "duration_ms",
            "factory_branch",
            "finished_at",
            "id",
            "last_activity_at",
            "outcome",
            "owner_id",
            "owner_pid",
            "process_id",
            "process_identity",
            "pull_request",
            "recovery_attempt",
            "recovery_of",
            "repository",
            "runtime",
            "source_item",
            "started_at",
            "summary",
            "task_id",
            "workflow",
            "working_directory",
            "workspace_kind",
        ]
    );
}

#[test]
fn inspect_resolves_task_context_bounds_detail_and_escapes_terminal_controls() {
    let fixture = Fixture::new();

    command(&fixture, &["inspect", &fixture.failed_run_id.to_string()])
        .assert()
        .success()
        .stdout(predicate::str::contains("Repository: example/repo"))
        .stdout(predicate::str::contains("thread-failed"))
        .stdout(predicate::str::contains("\\u{1b}"))
        .stdout(predicate::str::contains("\u{1b}").not())
        .stdout(predicate::str::contains("user:pass").not())
        .stdout(predicate::str::contains("standalone:password").not())
        .stdout(predicate::str::contains("ghp_supersecret").not())
        .stdout(predicate::str::contains("SECRET=").not());

    let failed_output = command(
        &fixture,
        &["inspect", &fixture.failed_run_id.to_string(), "--json"],
    )
    .output()
    .unwrap();
    let failed_json: serde_json::Value = serde_json::from_slice(&failed_output.stdout).unwrap();
    let encoded = serde_json::to_string(&failed_json).unwrap();
    assert!(!encoded.contains("ghp_supersecret"));
    assert!(!encoded.contains("user:pass"));
    assert!(!encoded.contains("abc123"));
    assert!(!encoded.contains("AKIAEXAMPLE"));
    assert!(!encoded.contains("standalone:password"));
    assert!(!encoded.contains("AKIAIOSFODNN7EXAMPLE"));
    assert!(!encoded.contains("horse battery"));
    assert!(!encoded.contains("remaining secret"));
    assert!(!encoded.contains("truncated-key-material"));
    assert!(encoded.contains("[REDACTED]"));

    let output = command(
        &fixture,
        &["inspect", &fixture.succeeded_run_id.to_string(), "--json"],
    )
    .output()
    .unwrap();
    assert!(output.status.success());
    let value: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(value["run"]["id"], fixture.succeeded_run_id);
    assert_eq!(value["session_id"], "thread-succeeded");
    assert_eq!(value["result"]["truncated"], true);
    assert!(value["result"]["value"].as_str().unwrap().len() <= 16 * 1024);
    let keys = value
        .as_object()
        .unwrap()
        .keys()
        .cloned()
        .collect::<Vec<_>>();
    assert_eq!(
        keys,
        [
            "activity",
            "container",
            "error",
            "result",
            "run",
            "sandbox",
            "session_id",
            "task",
        ]
    );

    let running_output = command(
        &fixture,
        &["inspect", &fixture.running_run_id.to_string(), "--json"],
    )
    .output()
    .unwrap();
    let running: serde_json::Value = serde_json::from_slice(&running_output.stdout).unwrap();
    assert_eq!(
        running["sandbox"]["template_ref"],
        "docker/sandbox-templates:codex"
    );
    assert_eq!(running["sandbox"]["state"], "created");
}

#[test]
fn cancel_reports_unowned_terminal_and_missing_runs() {
    let fixture = Fixture::new();

    command(
        &fixture,
        &["cancel", &fixture.running_run_id.to_string(), "--json"],
    )
    .assert()
    .success()
    .stdout(predicate::str::contains("\"status\": \"owned_elsewhere\""))
    .stdout(predicate::str::contains(
        "\"owner_kind\": \"stale-or-foreign\"",
    ))
    .stdout(predicate::str::contains("\"owner_pid\": null"));
    command(
        &fixture,
        &["cancel", &fixture.cancelled_run_id.to_string(), "--json"],
    )
    .assert()
    .success()
    .stdout(predicate::str::contains("already_terminal"));
    command(&fixture, &["cancel", "99999"])
        .assert()
        .failure()
        .stderr(predicate::str::contains("run 99999 does not exist"));
}

#[test]
fn empty_ledgers_have_stable_empty_outputs() {
    let temp = tempfile::tempdir().unwrap();
    let data = temp.path().join("empty");

    for subcommand in ["tasks", "runs"] {
        Command::cargo_bin("factory")
            .unwrap()
            .args([subcommand, "--json", "--data-directory"])
            .arg(&data)
            .assert()
            .success()
            .stdout("[]\n");
    }
    Command::cargo_bin("factory")
        .unwrap()
        .args(["tasks", "--data-directory"])
        .arg(&data)
        .assert()
        .success()
        .stdout("ID\tSTATE\tREPOSITORY\tWORKFLOW\tSOURCE\tCREATED\tUPDATED\n");
}
