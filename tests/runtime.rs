#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use factory::runtime::{CodexRuntime, RuntimeCancelled, Termination, observation_channel};
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
    let descendant = temp.path().join("deadline-descendant.pid");
    let executable = fake_codex(
        temp.path(),
        &format!(
            "sleep 30 &\necho $! > \"{}\"\ncat >/dev/null\nwait",
            descendant.display()
        ),
    );
    let runtime = CodexRuntime::new(executable);

    let result = runtime
        .run(
            "Time out.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(result.termination, Termination::TimedOut);
    let pid: i32 = fs::read_to_string(descendant)
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    assert_process_gone(pid).await;
}

#[tokio::test]
async fn active_long_running_agent_is_allowed_to_finish_before_its_deadline() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(
        temp.path(),
        r#"cat >/dev/null
echo '{"type":"thread.started","thread_id":"active-thread"}'
echo '{"type":"item.completed","sequence":1}'
sleep 1
echo '{"type":"item.completed","sequence":2}'
printf 'active work completed' > "$output"
exit 0"#,
    );

    let result = CodexRuntime::new(executable)
        .run(
            "Keep working while active.",
            temp.path(),
            Duration::from_secs(10),
            CancellationToken::new(),
        )
        .await
        .unwrap();

    assert!(result.succeeded());
    assert_eq!(result.activity_lines, 3);
    assert_eq!(result.final_response, "active work completed");
}

#[tokio::test]
async fn persisted_activity_is_structural_and_never_contains_raw_secret_output() {
    let temp = tempfile::tempdir().unwrap();
    let executable = fake_codex(
        temp.path(),
        r#"cat >/dev/null
printf '{"type":"item.completed","text":"TOKEN='
awk 'BEGIN { for (i = 0; i < 70000; i++) printf "s" }'
echo '","url":"https://github.com/owainlewis/factory/pull/123"}'
printf 'done' > "$output"
exit 0"#,
    );
    let (observations, receiver) = observation_channel();

    let result = CodexRuntime::new(executable)
        .with_activity_streaming(false)
        .run_with_session(
            "Observe safely.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
            None,
            observations,
        )
        .await
        .unwrap();
    let observation = receiver.borrow().clone();

    assert!(result.succeeded());
    assert_eq!(
        observation.pull_request.as_deref(),
        Some("https://github.com/owainlewis/factory/pull/123")
    );
    let activity = observation.activity.unwrap();
    assert_eq!(activity, "Codex event: item.completed\n");
    assert!(!activity.contains("TOKEN"));
    assert!(!activity.contains('s'));
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

#[cfg(any(target_os = "macos", target_os = "linux"))]
#[tokio::test]
async fn durable_runs_use_a_verified_process_group_anchor() {
    let temp = tempfile::tempdir().unwrap();
    let codex_pid_path = temp.path().join("codex.pid");
    let executable = fake_codex(
        temp.path(),
        &format!(
            "echo $$ > \"{}\"\ncat >/dev/null\nsleep 30",
            codex_pid_path.display()
        ),
    );
    let cancellation = CancellationToken::new();
    let (observations, receiver) = observation_channel();
    let runtime_cancellation = cancellation.clone();
    let working_directory = temp.path().to_owned();
    let run = tokio::spawn(async move {
        CodexRuntime::new(executable)
            .run_with_session(
                "Stay anchored.",
                &working_directory,
                Duration::from_secs(5),
                runtime_cancellation,
                None,
                observations,
            )
            .await
    });

    for _ in 0..500 {
        if receiver.borrow().process_id.is_some() && codex_pid_path.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    let observation = receiver.borrow().clone();
    let anchor_pid = observation.process_id.unwrap();
    let codex_pid: u32 = fs::read_to_string(&codex_pid_path)
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    assert_ne!(anchor_pid, codex_pid);
    assert!(observation.process_identity.is_some());
    assert!(matches!(
        nix::sys::signal::kill(
            nix::unistd::Pid::from_raw(i32::try_from(anchor_pid).unwrap()),
            None,
        ),
        Ok(()) | Err(nix::errno::Errno::EPERM)
    ));

    cancellation.cancel();
    assert_eq!(
        run.await.unwrap().unwrap().termination,
        Termination::Cancelled
    );
    assert_process_gone(i32::try_from(anchor_pid).unwrap()).await;
    assert_process_gone(i32::try_from(codex_pid).unwrap()).await;
}

#[cfg(any(target_os = "macos", target_os = "linux"))]
#[tokio::test]
async fn aborting_a_run_reaps_its_process_group_anchor() {
    let temp = tempfile::tempdir().unwrap();
    let codex_pid_path = temp.path().join("aborted-codex.pid");
    let executable = fake_codex(
        temp.path(),
        &format!(
            "echo $$ > \"{}\"\ncat >/dev/null\nsleep 30",
            codex_pid_path.display()
        ),
    );
    let (observations, receiver) = observation_channel();
    let working_directory = temp.path().to_owned();
    let run = tokio::spawn(async move {
        CodexRuntime::new(executable)
            .run_with_session(
                "Abort safely.",
                &working_directory,
                Duration::from_secs(30),
                CancellationToken::new(),
                None,
                observations,
            )
            .await
    });

    for _ in 0..500 {
        if receiver.borrow().process_id.is_some() && codex_pid_path.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    let anchor_pid = receiver.borrow().process_id.unwrap();
    let codex_pid: u32 = fs::read_to_string(&codex_pid_path)
        .unwrap()
        .trim()
        .parse()
        .unwrap();

    run.abort();
    let _ = run.await;
    assert_process_gone(i32::try_from(anchor_pid).unwrap()).await;
    assert_process_gone(i32::try_from(codex_pid).unwrap()).await;
}

#[cfg(any(target_os = "macos", target_os = "linux"))]
#[tokio::test]
async fn supervisor_persists_the_anchor_before_codex_spawn() {
    let temp = tempfile::tempdir().unwrap();
    let persisted = Arc::new(Mutex::new(None));
    let persisted_for_callback = Arc::clone(&persisted);
    let (observations, _receiver) = observation_channel();

    let error = CodexRuntime::new(temp.path().join("missing-codex"))
        .run_with_session_supervised(
            "Persist first.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
            None,
            observations,
            move |observation| {
                *persisted_for_callback.lock().unwrap() = Some(observation.clone());
                Ok(())
            },
        )
        .await
        .unwrap_err();

    assert!(format!("{error:#}").contains("failed to start Codex CLI"));
    let observation = persisted.lock().unwrap().clone().unwrap();
    let anchor_pid = observation.process_id.unwrap();
    assert!(observation.process_identity.is_some());
    assert_process_gone(i32::try_from(anchor_pid).unwrap()).await;
}

#[cfg(any(target_os = "macos", target_os = "linux"))]
#[tokio::test]
async fn failed_anchor_persistence_prevents_spawn_and_reaps_the_group() {
    let temp = tempfile::tempdir().unwrap();
    let started_path = temp.path().join("codex-started");
    let executable = fake_codex(
        temp.path(),
        &format!("touch \"{}\"", started_path.display()),
    );
    let captured_anchor = Arc::new(Mutex::new(None));
    let captured_for_callback = Arc::clone(&captured_anchor);
    let (observations, _receiver) = observation_channel();

    let error = CodexRuntime::new(executable)
        .run_with_session_supervised(
            "Fail persistence.",
            temp.path(),
            Duration::from_secs(5),
            CancellationToken::new(),
            None,
            observations,
            move |observation| {
                *captured_for_callback.lock().unwrap() = observation.process_id;
                anyhow::bail!("simulated durable persistence failure")
            },
        )
        .await
        .unwrap_err();

    assert!(format!("{error:#}").contains("simulated durable persistence failure"));
    assert!(!started_path.exists());
    let anchor_pid = captured_anchor.lock().unwrap().unwrap();
    assert_process_gone(i32::try_from(anchor_pid).unwrap()).await;
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
