#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;

use factory::config::SourceConfig;
use factory::source::{SourceClient, source_rate_limit};
use tokio_util::sync::CancellationToken;

fn source_fixture(script: &str) -> (tempfile::TempDir, std::path::PathBuf, SourceConfig) {
    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository");
    fs::create_dir(&repository).unwrap();
    let source = temp.path().join("source.sh");
    fs::write(&source, format!("#!/bin/sh\n{script}\n")).unwrap();
    let mut permissions = fs::metadata(&source).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&source, permissions).unwrap();
    let config = SourceConfig {
        command: vec![source.display().to_string()],
        owner: String::new(),
        project_number: 0,
        status_field: String::new(),
        trusted_users: Vec::new(),
    };
    (temp, repository, config)
}

async fn source_error(script: &str) -> anyhow::Error {
    let (_temp, repository, source) = source_fixture(script);
    SourceClient
        .validate(&repository, &source, "open", &[], &CancellationToken::new())
        .await
        .unwrap_err()
}

#[tokio::test]
async fn structured_rate_limit_accepts_whitespace_and_normalizes_explicit_offsets() {
    let error = source_error(
        r#"printf ' \n{"error":{"kind":"rate_limited","message":"slow down","retry_at":"2099-01-01T01:30:00+01:30"}}\n '; exit 1"#,
    )
    .await;
    let rate_limit = source_rate_limit(&error).unwrap();
    assert_eq!(rate_limit.message, "slow down");
    assert_eq!(
        rate_limit.retry_at.unwrap().to_rfc3339(),
        "2099-01-01T00:00:00+00:00"
    );
}

#[tokio::test]
async fn missing_invalid_and_non_offset_retry_times_remain_actionable_with_fallback() {
    for retry in [
        "",
        r#","retry_at":"not-a-time""#,
        r#","retry_at":"2099-01-01T00:00:00""#,
    ] {
        let script = format!(
            r#"printf '%s' '{{"error":{{"kind":"rate_limited","message":"slow"{retry}}}}}'; exit 1"#
        );
        let error = source_error(&script).await;
        assert_eq!(source_rate_limit(&error).unwrap().retry_at, None);
    }
    for retry_at in [
        serde_json::json!(123),
        serde_json::json!({"future": true}),
        serde_json::json!(["2099-01-01T00:00:00Z"]),
        serde_json::Value::Null,
    ] {
        let envelope = serde_json::json!({
            "error": {
                "kind": "rate_limited",
                "message": "slow",
                "retry_at": retry_at
            }
        });
        let error = source_error(&format!("printf '%s' '{}'; exit 1", envelope)).await;
        assert_eq!(source_rate_limit(&error).unwrap().retry_at, None);
    }
}

#[tokio::test]
async fn unknown_or_malformed_envelopes_are_ordinary_transient_failures() {
    for output in [
        r#"{"error":{"kind":"future_kind","message":"no"}}"#,
        r#"{"error":{"kind":"RateLimited","message":"no"}}"#,
        r#"{"error":{"kind":"rate_limited"}}"#,
        r#"{"error":{"kind":"rate_limited","message":"","extra":true}}"#,
        r#"{"error":{"kind":"rate_limited","message":"line\nbreak"}}"#,
        r#"{"error":{"kind":"rate_limited","message":"ok"}} trailing"#,
        "",
    ] {
        let script = format!("printf '%s' '{}'; exit 1", output.replace('\'', "'\\''"));
        let error = source_error(&script).await;
        assert!(source_rate_limit(&error).is_none(), "{output:?}");
    }
}

#[tokio::test]
async fn structured_error_field_limits_are_enforced_exactly() {
    let invalid = [
        serde_json::json!({"error": {"kind": "a".repeat(65), "message": "ok"}}),
        serde_json::json!({"error": {"kind": "rate-limited", "message": "ok"}}),
        serde_json::json!({"error": {"kind": "rate_limited", "message": "x".repeat(501)}}),
    ];
    for envelope in invalid {
        let script = format!("printf '%s' '{}'; exit 1", envelope);
        let error = source_error(&script).await;
        assert!(source_rate_limit(&error).is_none(), "{envelope}");
    }

    let valid = serde_json::json!({
        "error": {
            "kind": "r".repeat(64),
            "message": "m".repeat(500)
        }
    });
    let error = source_error(&format!("printf '%s' '{}'; exit 1", valid)).await;
    assert!(source_rate_limit(&error).is_none());
}

#[tokio::test]
async fn stderr_never_supplies_the_structured_retry_signal() {
    let error = source_error(
        r#"printf '%s' '{"error":{"kind":"rate_limited","message":"slow"}}' >&2; exit 1"#,
    )
    .await;
    assert!(source_rate_limit(&error).is_none());
}

#[tokio::test]
async fn oversized_nonzero_stdout_is_not_treated_as_a_structured_error() {
    let error = source_error("head -c 1048577 /dev/zero | tr '\\000' x; exit 1").await;
    assert!(source_rate_limit(&error).is_none());
}

#[tokio::test]
async fn an_error_envelope_on_success_is_invalid_source_output() {
    let error =
        source_error(r#"printf '%s' '{"error":{"kind":"rate_limited","message":"slow"}}'; exit 0"#)
            .await;
    assert!(source_rate_limit(&error).is_none());
    assert!(format!("{error:#}").contains("invalid JSON"));
}

#[tokio::test]
async fn stderr_retains_its_tail_and_marks_truncation_without_driving_retry() {
    let error = source_error(
        "head -c 70000 /dev/zero | tr '\\000' x >&2; printf ' FINAL-DIAGNOSTIC' >&2; exit 1",
    )
    .await;
    let display = format!("{error:#}");
    assert!(display.contains("FINAL-DIAGNOSTIC"), "{display}");
    assert!(display.contains("[stderr truncated]"), "{display}");
    assert!(source_rate_limit(&error).is_none());
}

#[tokio::test]
async fn cancellation_kills_adapter_descendants_that_hold_output_pipes() {
    let temp = tempfile::tempdir().unwrap();
    let marker = temp.path().join("started");
    let script = format!("touch {:?}; sleep 300 & wait", marker);
    let (_fixture, repository, source) = source_fixture(&script);
    let cancellation = CancellationToken::new();
    let query_cancellation = cancellation.clone();
    let task = tokio::spawn(async move {
        SourceClient
            .validate(&repository, &source, "open", &[], &query_cancellation)
            .await
    });
    let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(10);
    while !marker.exists() {
        assert!(!task.is_finished(), "source query exited before its marker");
        assert!(tokio::time::Instant::now() < deadline);
        tokio::time::sleep(std::time::Duration::from_millis(10)).await;
    }
    cancellation.cancel();
    let result = tokio::time::timeout(std::time::Duration::from_secs(5), task)
        .await
        .expect("source cancellation should not wait for descendants")
        .unwrap();
    assert!(format!("{:#}", result.unwrap_err()).contains("cancelled"));
}

#[tokio::test]
async fn cancellation_still_applies_after_adapter_parent_exits_with_open_descendant_pipes() {
    for exit in [0, 1] {
        let temp = tempfile::tempdir().unwrap();
        let marker = temp.path().join("parent-exited");
        let descendant_pid = temp.path().join("descendant.pid");
        let script = format!(
            "sleep 300 & printf '%s' \"$!\" > {:?}; printf '%s' '{{\"issues\":[]}}'; touch {:?}; exit {exit}",
            descendant_pid, marker
        );
        let (_fixture, repository, source) = source_fixture(&script);
        let cancellation = CancellationToken::new();
        let query_cancellation = cancellation.clone();
        let task = tokio::spawn(async move {
            SourceClient
                .validate(&repository, &source, "open", &[], &query_cancellation)
                .await
        });
        let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(5);
        while !marker.exists() {
            assert!(
                !task.is_finished(),
                "source parent exited before its marker"
            );
            assert!(tokio::time::Instant::now() < deadline);
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        }
        cancellation.cancel();
        let result = tokio::time::timeout(std::time::Duration::from_secs(5), task)
            .await
            .expect("pipe draining must remain cancellable after parent exit")
            .unwrap();
        assert!(format!("{:#}", result.unwrap_err()).contains("cancelled"));
        let process_id = fs::read_to_string(&descendant_pid)
            .unwrap()
            .parse::<i32>()
            .unwrap();
        let deadline = tokio::time::Instant::now() + std::time::Duration::from_secs(2);
        loop {
            if matches!(
                nix::sys::signal::kill(nix::unistd::Pid::from_raw(process_id), None),
                Err(nix::errno::Errno::ESRCH)
            ) {
                break;
            }
            assert!(
                tokio::time::Instant::now() < deadline,
                "source descendant {process_id} survived cancellation"
            );
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        }
    }
}
