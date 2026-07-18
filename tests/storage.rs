use std::collections::HashMap;
use std::sync::{Arc, Barrier};
use std::thread;

#[cfg(unix)]
use std::os::unix::process::CommandExt;
#[cfg(unix)]
use std::process::Command;

use factory::storage::{
    CancellationRequest, Ledger, MAX_ERROR_BYTES, MAX_RESULT_BYTES, RunOutcome, TaskIdentity,
    TaskState,
};
use rusqlite::Connection;

fn ticket(revision: &str) -> TaskIdentity {
    TaskIdentity::ticket(
        "owainlewis/factory",
        "implement-ready-ticket",
        "3",
        revision,
    )
    .unwrap()
}

#[test]
fn initializes_in_data_directory_and_persists_across_reopen() {
    let temp = tempfile::tempdir().unwrap();
    let data = temp.path().join("nested/data");
    let mut ledger = Ledger::open_in(&data).unwrap();
    let enqueued = ledger.enqueue(&ticket("revision-1")).unwrap();
    assert!(enqueued.created);
    let path = ledger.path().to_owned();
    drop(ledger);

    let reopened = Ledger::open(&path).unwrap();
    let persisted = reopened.task(enqueued.task.id).unwrap().unwrap();

    assert_eq!(persisted.state, TaskState::Queued);
    assert_eq!(persisted.repository, "owainlewis/factory");
    assert_eq!(persisted.source_item.as_deref(), Some("3"));
}

#[test]
fn ticket_and_schedule_identities_deduplicate_exact_triggers() {
    let temp = tempfile::tempdir().unwrap();
    let mut ledger = Ledger::open(temp.path().join("ledger.db").as_path()).unwrap();

    let first = ledger.enqueue(&ticket("revision-1")).unwrap();
    let duplicate = ledger.enqueue(&ticket("revision-1")).unwrap();
    let changed = ledger.enqueue(&ticket("revision-2")).unwrap();
    let scheduled =
        TaskIdentity::scheduled("owainlewis/factory", "find-bugs", "2026-07-20T09:00:00Z").unwrap();
    let first_schedule = ledger.enqueue(&scheduled).unwrap();
    let duplicate_schedule = ledger.enqueue(&scheduled).unwrap();

    assert!(first.created);
    assert!(!duplicate.created);
    assert_eq!(duplicate.task.id, first.task.id);
    assert!(changed.created);
    assert_ne!(changed.task.id, first.task.id);
    assert!(first_schedule.created);
    assert!(!duplicate_schedule.created);
    assert_eq!(duplicate_schedule.task.id, first_schedule.task.id);
}

#[test]
fn concurrent_claim_has_exactly_one_winner() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("ledger.db");
    let mut setup = Ledger::open(&path).unwrap();
    let task_id = setup.enqueue(&ticket("claim-revision")).unwrap().task.id;
    drop(setup);
    let barrier = Arc::new(Barrier::new(9));
    let handles = (0..8)
        .map(|_| {
            let path = path.clone();
            let barrier = Arc::clone(&barrier);
            thread::spawn(move || {
                let mut ledger = Ledger::open(&path).unwrap();
                barrier.wait();
                ledger.claim_next().unwrap().map(|task| task.id)
            })
        })
        .collect::<Vec<_>>();
    barrier.wait();
    let claims = handles
        .into_iter()
        .filter_map(|handle| handle.join().unwrap())
        .collect::<Vec<_>>();

    assert_eq!(claims, vec![task_id]);
}

#[test]
fn records_bounded_run_history_and_terminal_tasks_cannot_be_reclaimed() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("ledger.db");
    let mut ledger = Ledger::open(&path).unwrap();
    let task = ledger.enqueue(&ticket("run-revision")).unwrap().task;
    assert_eq!(ledger.claim_next().unwrap().unwrap().id, task.id);
    let run = ledger.start_run(task.id, "codex").unwrap();

    let result = "é".repeat(MAX_RESULT_BYTES);
    let error = "x".repeat(MAX_ERROR_BYTES + 100);
    let completed = ledger
        .finish_run_and_task(
            run.id,
            RunOutcome::Failed,
            Some(&result),
            Some(&error),
            Some("thread-123"),
        )
        .unwrap();
    let task = ledger.task(task.id).unwrap().unwrap();

    assert_eq!(task.state, TaskState::Failed);
    assert!(completed.finished_at.is_some());
    assert_eq!(completed.outcome, "failed");
    assert!(completed.result.unwrap().len() <= MAX_RESULT_BYTES);
    assert_eq!(completed.error.unwrap().len(), MAX_ERROR_BYTES);
    assert_eq!(completed.session_id.as_deref(), Some("thread-123"));
    assert!(ledger.claim_next().unwrap().is_none());
    assert!(
        ledger
            .finish_run_and_task(run.id, RunOutcome::Succeeded, None, None, None)
            .is_err()
    );
    assert_eq!(ledger.runs_for_task(task.id).unwrap().len(), 1);
}

#[test]
fn only_one_active_run_can_start_for_a_claimed_task() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("ledger.db");
    let mut setup = Ledger::open(&path).unwrap();
    let task = setup.enqueue(&ticket("active-run-revision")).unwrap().task;
    setup.claim_next().unwrap().unwrap();
    drop(setup);
    let barrier = Arc::new(Barrier::new(3));
    let handles = (0..2)
        .map(|_| {
            let path = path.clone();
            let barrier = Arc::clone(&barrier);
            thread::spawn(move || {
                let mut ledger = Ledger::open(&path).unwrap();
                barrier.wait();
                ledger.start_run(task.id, "codex").is_ok()
            })
        })
        .collect::<Vec<_>>();
    barrier.wait();
    let winners = handles
        .into_iter()
        .map(|handle| handle.join().unwrap())
        .filter(|started| *started)
        .count();

    assert_eq!(winners, 1);
}

#[test]
fn rejects_a_database_from_a_newer_factory_version_without_changing_it() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("future.db");
    let connection = Connection::open(&path).unwrap();
    connection.pragma_update(None, "user_version", 99).unwrap();
    drop(connection);

    let error = Ledger::open(&path).err().unwrap();

    assert!(error.to_string().contains("newer than supported"));
    let connection = Connection::open(path).unwrap();
    let version: i64 = connection
        .pragma_query_value(None, "user_version", |row| row.get(0))
        .unwrap();
    assert_eq!(version, 99);
}

#[test]
fn migrates_a_version_one_ledger_without_losing_tasks() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("version-one.db");
    let connection = Connection::open(&path).unwrap();
    connection
        .execute_batch(
            "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
             CREATE TABLE tasks (
                 id INTEGER PRIMARY KEY,
                 identity_key TEXT NOT NULL UNIQUE,
                 kind TEXT NOT NULL,
                 repository TEXT NOT NULL,
                 workflow TEXT NOT NULL,
                 source_item TEXT,
                 state TEXT NOT NULL,
                 created_at INTEGER NOT NULL,
                 updated_at INTEGER NOT NULL
             );
             CREATE TABLE runs (
                 id INTEGER PRIMARY KEY,
                 task_id INTEGER NOT NULL REFERENCES tasks(id),
                 workflow TEXT NOT NULL,
                 repository TEXT NOT NULL,
                 source_item TEXT,
                 runtime TEXT NOT NULL,
                 started_at INTEGER NOT NULL,
                 finished_at INTEGER,
                 outcome TEXT NOT NULL,
                 result TEXT,
                 error TEXT,
                 session_id TEXT
             );
             INSERT INTO schema_migrations VALUES (1, 1);
             INSERT INTO tasks VALUES (7, 'legacy', 'ticket', 'example/repo', 'workflow', '4', 'queued', 1, 1);
             PRAGMA user_version = 1;",
        )
        .unwrap();
    drop(connection);

    let ledger = Ledger::open(&path).unwrap();
    let task = ledger.task(7).unwrap().unwrap();

    assert_eq!(task.repository, "example/repo");
    assert_eq!(task.payload, None);
    let connection = Connection::open(path).unwrap();
    let version: i64 = connection
        .pragma_query_value(None, "user_version", |row| row.get(0))
        .unwrap();
    assert_eq!(version, 4);
}

#[test]
fn orphan_recovery_is_deduplicated_bounded_and_excludes_terminal_runs() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("ledger.db");
    let mut ledger = Ledger::open(&path).unwrap();
    ledger
        .register_daemon_owner("interrupted-owner", std::process::id())
        .unwrap();
    let task = ledger.enqueue(&ticket("recovery-revision")).unwrap().task;
    let runtimes = HashMap::from([(
        (
            "owainlewis/factory".to_owned(),
            "implement-ready-ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let workdirs = HashMap::from([(
        "owainlewis/factory".to_owned(),
        "/worktrees/factory-3".to_owned(),
    )]);
    let interrupted = ledger
        .claim_ticket_and_start_run_with_workdirs(
            &["owainlewis/factory".to_owned()],
            &runtimes,
            "interrupted-owner",
            std::process::id(),
            &workdirs,
        )
        .unwrap()
        .unwrap()
        .run;
    ledger
        .observe_run(
            interrupted.id,
            None,
            None,
            Some("thread-recover"),
            Some("https://github.com/owainlewis/factory/pull/99"),
            Some("PR https://github.com/owainlewis/factory/pull/99 SECRET=hunter2"),
        )
        .unwrap();
    ledger.remove_daemon_owner("interrupted-owner").unwrap();

    let report = ledger.recover_orphaned_runs().unwrap();
    assert_eq!(report.recovered_run_ids, [interrupted.id]);
    assert!(report.exhausted_run_ids.is_empty());
    let report = ledger.recover_orphaned_runs().unwrap();
    assert!(report.recovered_run_ids.is_empty());
    assert!(report.exhausted_run_ids.is_empty());
    let closed = ledger.run(interrupted.id).unwrap().unwrap();
    assert_eq!(closed.outcome, "failed");
    assert_eq!(closed.process_id, None);
    assert_eq!(closed.session_id.as_deref(), Some("thread-recover"));
    assert!(!closed.activity.unwrap().contains("hunter2"));
    assert_eq!(
        ledger.task(task.id).unwrap().unwrap().state,
        TaskState::Queued
    );

    ledger
        .register_daemon_owner("recovery-owner", std::process::id())
        .unwrap();
    let first_recovery = ledger
        .claim_ticket_and_start_run_with_workdirs(
            &["owainlewis/factory".to_owned()],
            &runtimes,
            "recovery-owner",
            std::process::id(),
            &workdirs,
        )
        .unwrap()
        .unwrap()
        .run;
    assert_eq!(first_recovery.recovery_of, Some(interrupted.id));
    assert_eq!(first_recovery.recovery_attempt, 1);
    assert_eq!(
        first_recovery.working_directory.as_deref(),
        Some("/worktrees/factory-3")
    );
    ledger
        .observe_run(first_recovery.id, None, None, None, None, None)
        .unwrap();
    ledger.remove_daemon_owner("recovery-owner").unwrap();
    let report = ledger.recover_orphaned_runs().unwrap();
    assert_eq!(report.recovered_run_ids, [first_recovery.id]);
    assert!(report.exhausted_run_ids.is_empty());
    assert_eq!(
        ledger.task(task.id).unwrap().unwrap().state,
        TaskState::Queued
    );

    ledger
        .register_daemon_owner("recovery-owner", std::process::id())
        .unwrap();
    let final_recovery = ledger
        .claim_ticket_and_start_run_with_workdirs(
            &["owainlewis/factory".to_owned()],
            &runtimes,
            "recovery-owner",
            std::process::id(),
            &workdirs,
        )
        .unwrap()
        .unwrap()
        .run;
    assert_eq!(final_recovery.recovery_of, Some(first_recovery.id));
    assert_eq!(final_recovery.recovery_attempt, 2);
    ledger
        .observe_run(final_recovery.id, None, None, None, None, None)
        .unwrap();
    ledger.remove_daemon_owner("recovery-owner").unwrap();
    let report = ledger.recover_orphaned_runs().unwrap();
    assert!(report.recovered_run_ids.is_empty());
    assert_eq!(report.exhausted_run_ids, [final_recovery.id]);
    assert_eq!(
        ledger.task(task.id).unwrap().unwrap().state,
        TaskState::Failed
    );
    let report = ledger.recover_orphaned_runs().unwrap();
    assert!(report.recovered_run_ids.is_empty());
    assert!(report.exhausted_run_ids.is_empty());
    assert_eq!(
        ledger
            .run(final_recovery.id)
            .unwrap()
            .unwrap()
            .error
            .as_deref(),
        Some("Factory detected an interrupted run without a live owned process")
    );
}

#[cfg(unix)]
#[test]
fn orphan_recovery_does_not_signal_a_reused_process_group() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("ledger.db");
    let mut ledger = Ledger::open(&path).unwrap();
    ledger
        .register_daemon_owner("gone-owner", std::process::id())
        .unwrap();
    ledger.enqueue(&ticket("pid-reuse-revision")).unwrap();
    let runtimes = HashMap::from([(
        (
            "owainlewis/factory".to_owned(),
            "implement-ready-ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let run = ledger
        .claim_ticket_and_start_run(
            &["owainlewis/factory".to_owned()],
            &runtimes,
            "gone-owner",
            std::process::id(),
        )
        .unwrap()
        .unwrap()
        .run;
    assert!(
        ledger
            .observe_run(run.id, Some(0), Some("invalid"), None, None, None)
            .unwrap_err()
            .to_string()
            .contains("must be positive")
    );
    let mut unrelated = Command::new("sleep")
        .arg("30")
        .process_group(0)
        .spawn()
        .unwrap();
    let unrelated_pid = unrelated.id();
    ledger
        .observe_run(
            run.id,
            Some(unrelated_pid),
            Some("different-process-start-identity"),
            None,
            None,
            None,
        )
        .unwrap();
    ledger.remove_daemon_owner("gone-owner").unwrap();

    ledger.recover_orphaned_runs().unwrap();

    assert!(matches!(
        nix::sys::signal::kill(
            nix::unistd::Pid::from_raw(i32::try_from(unrelated_pid).unwrap()),
            None,
        ),
        Ok(()) | Err(nix::errno::Errno::EPERM)
    ));
    unrelated.kill().unwrap();
    unrelated.wait().unwrap();
}

#[test]
fn cancellation_requests_are_durable_idempotent_and_force_cancelled_outcome() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("ledger.db");
    let mut ledger = Ledger::open(&path).unwrap();
    let task = ledger.enqueue(&ticket("cancel-revision")).unwrap().task;
    let runtimes = HashMap::from([(
        (
            "owainlewis/factory".to_owned(),
            "implement-ready-ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    ledger
        .register_daemon_owner("storage-test-owner", std::process::id())
        .unwrap();
    let run = ledger
        .claim_ticket_and_start_run(
            &["owainlewis/factory".to_owned()],
            &runtimes,
            "storage-test-owner",
            std::process::id(),
        )
        .unwrap()
        .unwrap()
        .run;

    assert!(matches!(
        ledger.request_run_cancellation(run.id).unwrap(),
        CancellationRequest::Requested(_)
    ));
    drop(ledger);

    let mut reopened = Ledger::open(&path).unwrap();
    assert!(reopened.cancellation_requested(run.id).unwrap());
    reopened.remove_daemon_owner("storage-test-owner").unwrap();
    assert!(matches!(
        reopened.request_run_cancellation(run.id).unwrap(),
        CancellationRequest::AlreadyRequested(_)
    ));
    let completed = reopened
        .finish_run_and_task(
            run.id,
            RunOutcome::Succeeded,
            Some("runtime exited during cancellation"),
            None,
            Some("thread-cancel"),
        )
        .unwrap();

    assert_eq!(completed.outcome, "cancelled");
    assert_eq!(
        reopened.task(task.id).unwrap().unwrap().state,
        TaskState::Cancelled
    );
    assert!(matches!(
        reopened.request_run_cancellation(run.id).unwrap(),
        CancellationRequest::Terminal(_)
    ));
    assert!(matches!(
        reopened.request_run_cancellation(99_999).unwrap(),
        CancellationRequest::NotFound
    ));
}

#[test]
fn cancellation_rejects_a_running_row_without_a_live_daemon_owner() {
    let temp = tempfile::tempdir().unwrap();
    let mut ledger = Ledger::open(&temp.path().join("ledger.db")).unwrap();
    let task = ledger.enqueue(&ticket("unowned-revision")).unwrap().task;
    ledger.claim_next().unwrap().unwrap();
    let run = ledger.start_run(task.id, "codex").unwrap();

    let status = ledger.request_run_cancellation(run.id).unwrap();

    assert!(matches!(status, CancellationRequest::OwnedElsewhere(_)));
    assert!(!ledger.cancellation_requested(run.id).unwrap());
}

#[test]
fn cancellation_rejects_a_reused_live_pid_when_the_owner_lease_is_stale() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("ledger.db");
    let mut ledger = Ledger::open(&path).unwrap();
    ledger
        .register_daemon_owner("stale-owner", std::process::id())
        .unwrap();
    ledger.enqueue(&ticket("stale-owner-revision")).unwrap();
    let runtimes = HashMap::from([(
        (
            "owainlewis/factory".to_owned(),
            "implement-ready-ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let run = ledger
        .claim_ticket_and_start_run(
            &["owainlewis/factory".to_owned()],
            &runtimes,
            "stale-owner",
            std::process::id(),
        )
        .unwrap()
        .unwrap()
        .run;
    drop(ledger);
    let connection = Connection::open(&path).unwrap();
    connection
        .execute("UPDATE daemon_owners SET heartbeat_at = 0", [])
        .unwrap();
    drop(connection);
    let mut ledger = Ledger::open(&path).unwrap();

    assert!(matches!(
        ledger.request_run_cancellation(run.id).unwrap(),
        CancellationRequest::OwnedElsewhere(_)
    ));
    assert!(!ledger.cancellation_requested(run.id).unwrap());
}

#[test]
fn concurrent_completion_and_cancellation_always_leave_a_terminal_run() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("ledger.db");
    let runtimes = HashMap::from([(
        (
            "owainlewis/factory".to_owned(),
            "implement-ready-ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let mut setup = Ledger::open(&path).unwrap();
    setup
        .register_daemon_owner("race-owner", std::process::id())
        .unwrap();

    for index in 0..25 {
        setup.heartbeat_daemon_owner("race-owner").unwrap();
        let task = setup
            .enqueue(&ticket(&format!("race-revision-{index}")))
            .unwrap()
            .task;
        let run = setup
            .claim_ticket_and_start_run(
                &["owainlewis/factory".to_owned()],
                &runtimes,
                "race-owner",
                std::process::id(),
            )
            .unwrap()
            .unwrap()
            .run;
        let run_id = run.id;
        let barrier = Arc::new(Barrier::new(3));
        let finish = {
            let path = path.clone();
            let barrier = Arc::clone(&barrier);
            thread::spawn(move || {
                let mut ledger = Ledger::open(&path).unwrap();
                barrier.wait();
                ledger.finish_run_and_task(
                    run_id,
                    RunOutcome::Succeeded,
                    Some("complete"),
                    None,
                    None,
                )
            })
        };
        let cancel = {
            let path = path.clone();
            let barrier = Arc::clone(&barrier);
            thread::spawn(move || {
                let mut ledger = Ledger::open(&path).unwrap();
                barrier.wait();
                ledger.request_run_cancellation(run_id)
            })
        };
        barrier.wait();

        finish.join().unwrap().unwrap();
        cancel.join().unwrap().unwrap();
        let completed = setup.run(run_id).unwrap().unwrap();
        assert_ne!(completed.outcome, "running");
        assert!(setup.task(task.id).unwrap().unwrap().state.is_terminal());
    }
}

#[test]
fn failed_writes_do_not_damage_prior_state() {
    let temp = tempfile::tempdir().unwrap();
    let mut ledger = Ledger::open(&temp.path().join("ledger.db")).unwrap();
    let task = ledger.enqueue(&ticket("safe-revision")).unwrap().task;

    let error = ledger.start_run(task.id, "codex").unwrap_err();

    assert!(error.to_string().contains("must be running"));
    assert_eq!(
        ledger.task(task.id).unwrap().unwrap().state,
        TaskState::Queued
    );
    assert!(ledger.runs_for_task(task.id).unwrap().is_empty());
}
