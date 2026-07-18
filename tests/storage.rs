use std::sync::{Arc, Barrier};
use std::thread;

use factory::storage::{
    Ledger, MAX_ERROR_BYTES, MAX_RESULT_BYTES, RunOutcome, TaskIdentity, TaskState,
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
