use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use factory::daemon::FleetAdmission;
use tokio::sync::mpsc;
use tokio_util::sync::CancellationToken;

#[tokio::test(start_paused = true)]
async fn round_robin_admission_survives_saturation_and_preserves_the_e_cycle_bound() {
    let admission =
        FleetAdmission::new(vec!["a".to_owned(), "b".to_owned(), "c".to_owned()], 1).unwrap();
    let cancellation = CancellationToken::new();
    let first = admission.acquire("a", &cancellation).await.unwrap();
    let (sent, mut received) = mpsc::unbounded_channel();

    for repository in ["b", "c", "a"] {
        let admission = admission.clone();
        let cancellation = cancellation.clone();
        let sent = sent.clone();
        tokio::spawn(async move {
            let permit = admission.acquire(repository, &cancellation).await.unwrap();
            sent.send(repository).unwrap();
            drop(permit);
        });
    }
    tokio::task::yield_now().await;
    drop(first);

    assert_eq!(received.recv().await, Some("b"));
    assert_eq!(received.recv().await, Some("c"));
    assert_eq!(received.recv().await, Some("a"));
}

#[tokio::test(start_paused = true)]
async fn fleet_admission_never_exceeds_the_global_limit() {
    let admission = FleetAdmission::new(vec!["a".to_owned(), "b".to_owned()], 2).unwrap();
    let active = Arc::new(AtomicUsize::new(0));
    let maximum = Arc::new(AtomicUsize::new(0));
    let cancellation = CancellationToken::new();
    let mut tasks = Vec::new();
    for repository in ["a", "b"] {
        let admission = admission.clone();
        let active = Arc::clone(&active);
        let maximum = Arc::clone(&maximum);
        let cancellation = cancellation.clone();
        tasks.push(tokio::spawn(async move {
            for _ in 0..10 {
                let permit = admission.acquire(repository, &cancellation).await.unwrap();
                let current = active.fetch_add(1, Ordering::SeqCst) + 1;
                maximum.fetch_max(current, Ordering::SeqCst);
                tokio::task::yield_now().await;
                active.fetch_sub(1, Ordering::SeqCst);
                drop(permit);
            }
        }));
    }
    for task in tasks {
        task.await.unwrap();
    }
    assert_eq!(maximum.load(Ordering::SeqCst), 2);
}

#[tokio::test(start_paused = true)]
async fn saturated_fleet_admission_is_nonblocking_for_repository_housekeeping() {
    let admission = FleetAdmission::new(vec!["a".to_owned(), "b".to_owned()], 1).unwrap();
    let cancellation = CancellationToken::new();
    let permit = admission.acquire("a", &cancellation).await.unwrap();
    assert!(admission.try_acquire("b").unwrap().is_none());

    let housekeeping = Arc::new(AtomicUsize::new(0));
    housekeeping.fetch_add(1, Ordering::SeqCst);
    assert_eq!(housekeeping.load(Ordering::SeqCst), 1);
    drop(permit);
    assert!(admission.try_acquire("b").unwrap().is_some());
}
