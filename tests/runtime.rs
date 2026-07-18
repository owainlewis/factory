#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::time::Duration;

use factory::runtime::{CodexRuntime, RuntimeCancelled, Termination};
use tokio_util::sync::CancellationToken;

fn fake_codex(directory: &Path, execution: &str) -> PathBuf {
    let path = directory.join("codex");
    let script = format!(
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
{execution}
"#
    );
    fs::write(&path, script).unwrap();
    let mut permissions = fs::metadata(&path).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&path, permissions).unwrap();
    path
}

async fn assert_process_gone(pid: i32) {
    for _ in 0..200 {
        if matches!(
            nix::sys::signal::kill(nix::unistd::Pid::from_raw(pid), None),
            Err(nix::errno::Errno::ESRCH)
        ) {
            return;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    panic!("process {pid} survived cancellation");
}

#[tokio::test]
async fn health_check_and_success_capture_final_response_and_thread() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(
        temp.path(),
        r#"cat >/dev/null
echo '{"type":"thread.started","thread_id":"thread-123"}'
echo '{"type":"item.completed"}'
printf 'Workflow complete.' > "$output"
exit 0"#,
    );
    let runtime = CodexRuntime::new(executable);

    let health = runtime.health_check().await.unwrap();
    let result = runtime
        .run(
            "A harmless prompt.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(health.version, "codex-cli 1.2.3");
    assert_eq!(health.authentication, "Logged in using ChatGPT");
    assert!(result.succeeded());
    assert_eq!(result.thread_id.as_deref(), Some("thread-123"));
    assert_eq!(result.final_response, "Workflow complete.");
    assert_eq!(result.activity_lines, 2);
    assert!(!result.final_response_truncated);
}

#[tokio::test]
async fn returns_the_real_non_zero_exit_status() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(
        temp.path(),
        r#"cat >/dev/null
echo '{"type":"thread.started","thread_id":"failed-thread"}'
printf 'Could not complete.' > "$output"
echo "runtime failed" >&2
exit 17"#,
    );
    let runtime = CodexRuntime::new(executable);

    let result = runtime
        .run(
            "Fail safely.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(result.status.code(), Some(17));
    assert!(!result.succeeded());
    assert!(result.stderr_tail.contains("runtime failed"));
}

#[tokio::test]
async fn early_exit_before_prompt_read_preserves_status_and_output() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(
        temp.path(),
        r#"echo '{"type":"thread.started","thread_id":"early-thread"}'
printf 'Rejected before prompt.' > "$output"
exit 23"#,
    );
    let runtime = CodexRuntime::new(executable);
    let prompt = "large prompt ".repeat(200_000);

    let result = runtime
        .run(
            &prompt,
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(result.status.code(), Some(23));
    assert_eq!(result.thread_id.as_deref(), Some("early-thread"));
    assert_eq!(result.final_response, "Rejected before prompt.");
}

#[tokio::test]
async fn rejects_malformed_json_activity() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(
        temp.path(),
        r#"cat >/dev/null
echo 'not json'
printf 'Untrusted final response.' > "$output"
exit 0"#,
    );
    let runtime = CodexRuntime::new(executable);

    let error = runtime
        .run(
            "Malformed output.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
        )
        .await
        .unwrap_err();

    assert!(error.to_string().contains("malformed JSON activity"));
}

#[tokio::test]
async fn timeout_stops_the_run() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(temp.path(), "cat >/dev/null\nsleep 30");
    let runtime = CodexRuntime::new(executable);

    let result = runtime
        .run(
            "Time out.",
            temp.path(),
            Duration::from_secs(2),
            CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(result.termination, Termination::TimedOut);
}

#[tokio::test]
async fn cancellation_stops_the_run() {
    let temp = tempfile::tempdir().unwrap();
    let descendant = temp.path().join("cancelled-descendant.pid");
    let executable = fake_codex(
        temp.path(),
        &format!(
            "sleep 30 &\necho $! > \"{}\"\ncat >/dev/null\nwait",
            descendant.display()
        ),
    );
    let runtime = CodexRuntime::new(executable);
    let cancellation = CancellationToken::new();
    let cancel_later = cancellation.clone();
    let descendant_ready = descendant.clone();
    tokio::spawn(async move {
        for _ in 0..1000 {
            if descendant_ready.exists() {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        cancel_later.cancel();
    });

    let result = runtime
        .run(
            "Cancel.",
            temp.path(),
            Duration::from_secs(15),
            cancellation,
        )
        .await
        .unwrap();

    assert_eq!(result.termination, Termination::Cancelled);
    let pid: i32 = fs::read_to_string(descendant)
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    assert_process_gone(pid).await;
}

#[tokio::test]
async fn authentication_failure_is_actionable() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("codex");
    fs::write(
        &path,
        r#"#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "codex-cli 1.2.3"
  exit 0
fi
echo "not logged in" >&2
exit 1
"#,
    )
    .unwrap();
    let mut permissions = fs::metadata(&path).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&path, permissions).unwrap();

    let error = CodexRuntime::new(path).health_check().await.unwrap_err();
    let message = format!("{error:#}");

    assert!(message.contains("run codex login"));
    assert!(message.contains("not logged in"));
}

#[tokio::test]
async fn api_key_authentication_is_rejected() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("codex");
    fs::write(
        &path,
        r#"#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "codex-cli 1.2.3"
  exit 0
fi
echo "Logged in using an API key"
exit 0
"#,
    )
    .unwrap();
    let mut permissions = fs::metadata(&path).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&path, permissions).unwrap();

    let error = CodexRuntime::new(path).health_check().await.unwrap_err();

    assert!(
        error
            .to_string()
            .contains("ChatGPT subscription authentication")
    );
}

#[tokio::test]
async fn timeout_applies_while_prompt_delivery_is_blocked() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(temp.path(), "sleep 30");
    let runtime = CodexRuntime::new(executable);
    let prompt = "x".repeat(2 * 1024 * 1024);

    let result = runtime
        .run(
            &prompt,
            temp.path(),
            Duration::from_millis(200),
            CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(result.termination, Termination::TimedOut);
    assert!(result.duration < Duration::from_secs(3));
}

#[tokio::test]
async fn normal_leader_exit_cleans_up_descendants_holding_pipes() {
    let temp = tempfile::tempdir().unwrap();
    let descendant = temp.path().join("pipe-descendant.pid");
    let executable = fake_codex(
        temp.path(),
        &format!(
            "cat >/dev/null\nsleep 30 >/dev/null 2>&1 &\necho $! > \"{}\"\nexit 0",
            descendant.display()
        ),
    );
    let runtime = CodexRuntime::new(executable);

    let result = runtime
        .run(
            "Exit with a child.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
        )
        .await
        .unwrap();

    assert!(result.succeeded());
    let pid: i32 = fs::read_to_string(descendant)
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    assert_process_gone(pid).await;
}

#[tokio::test]
async fn health_timeout_cleans_up_the_process_group() {
    let temp = tempfile::tempdir().unwrap();
    let descendant = temp.path().join("health-descendant.pid");
    let executable = temp.path().join("codex");
    fs::write(
        &executable,
        format!(
            r#"#!/bin/sh
sleep 30 &
echo $! > "{}"
wait
"#,
            descendant.display()
        ),
    )
    .unwrap();
    let mut permissions = fs::metadata(&executable).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&executable, permissions).unwrap();
    let runtime = CodexRuntime::new(executable).with_health_timeout(Duration::from_secs(5));

    let error = runtime.health_check().await.unwrap_err();

    assert!(format!("{error:#}").contains("timed out"));
    let pid: i32 = fs::read_to_string(descendant)
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    assert_process_gone(pid).await;
}

#[tokio::test]
async fn health_check_cancellation_cleans_up_the_process_group() {
    let temp = tempfile::tempdir().unwrap();
    let descendant = temp.path().join("cancelled-health-descendant.pid");
    let executable = temp.path().join("codex");
    fs::write(
        &executable,
        format!(
            r#"#!/bin/sh
sleep 30 &
echo $! > "{}"
wait
"#,
            descendant.display()
        ),
    )
    .unwrap();
    let mut permissions = fs::metadata(&executable).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&executable, permissions).unwrap();
    let cancellation = CancellationToken::new();
    let cancel_later = cancellation.clone();
    let descendant_ready = descendant.clone();
    tokio::spawn(async move {
        for _ in 0..1000 {
            if descendant_ready.exists() {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        cancel_later.cancel();
    });

    let error = CodexRuntime::new(executable)
        .health_check_with_cancellation(cancellation)
        .await
        .unwrap_err();

    assert!(error.downcast_ref::<RuntimeCancelled>().is_some());
    let pid: i32 = fs::read_to_string(descendant)
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    assert_process_gone(pid).await;
}

#[tokio::test]
async fn cancellation_is_not_masked_by_partial_json_activity() {
    let temp = tempfile::tempdir().unwrap();
    let activity_ready = temp.path().join("partial-activity-ready");
    let executable = fake_codex(
        temp.path(),
        &format!(
            "cat >/dev/null\nprintf '{{\"type\":'\necho ready > \"{}\"\nsleep 30",
            activity_ready.display()
        ),
    );
    let runtime = CodexRuntime::new(executable);
    let cancellation = CancellationToken::new();
    let cancel_later = cancellation.clone();
    let activity_ready_for_cancel = activity_ready.clone();
    tokio::spawn(async move {
        for _ in 0..1000 {
            if activity_ready_for_cancel.exists() {
                break;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        cancel_later.cancel();
    });

    let result = runtime
        .run(
            "Cancel partial output.",
            temp.path(),
            Duration::from_secs(5),
            cancellation,
        )
        .await
        .unwrap();

    assert_eq!(result.termination, Termination::Cancelled);
    assert!(result.activity_error.is_some());
}

#[tokio::test]
async fn rejects_oversized_activity_and_thread_metadata() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(
        temp.path(),
        r#"cat >/dev/null
printf '{"type":"thread.started","thread_id":"'
printf '%0300d' 0 | tr '0' 'a'
echo '"}'
printf 'done' > "$output"
exit 0"#,
    );
    let runtime = CodexRuntime::new(executable);

    let error = runtime
        .run(
            "Bound metadata.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
        )
        .await
        .unwrap_err();

    assert!(error.to_string().contains("thread ID exceeds 256 bytes"));
}

#[tokio::test]
async fn rejects_an_oversized_activity_line_without_unbounded_buffering() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(
        temp.path(),
        r#"cat >/dev/null
awk 'BEGIN { for (i = 0; i < 300000; i++) printf "x" }'
echo
printf 'done' > "$output"
exit 0"#,
    );
    let runtime = CodexRuntime::new(executable).with_activity_streaming(false);

    let error = runtime
        .run(
            "Bound activity.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
        )
        .await
        .unwrap_err();

    assert!(error.to_string().contains("line exceeds 262144 bytes"));
}
