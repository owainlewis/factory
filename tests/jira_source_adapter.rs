#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::process::Command;

use serde_json::Value;

fn fake_jiractrl(bin: &std::path::Path, total: usize) {
    let executable = bin.join("jiractrl");
    fs::write(
        &executable,
        format!(
            r#"#!/bin/sh
printf '%s\n' "$*" > "$JIRACTRL_CALLS"
printf '%s\n' '{{"startAt":0,"maxResults":100,"total":{total},"issues":[{{"id":"1","key":"SPS-123","fields":{{"summary":"Fix polling","description":"The daemon misses work.","status":{{"id":"3","name":"Ready To Implement"}},"labels":["factory-ready"]}}}}]}}'
"#
        ),
    )
    .unwrap();
    let mut permissions = fs::metadata(&executable).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(executable, permissions).unwrap();
}

#[test]
fn jira_adapter_builds_exact_jql_and_normalizes_issues() {
    let temp = tempfile::tempdir().unwrap();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    fake_jiractrl(&bin, 1);
    let calls = temp.path().join("calls");
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let adapter = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(".factory/sources/jira");
    let output = Command::new(adapter)
        .args([
            "--project",
            "SPS",
            "--state",
            "Ready To Implement",
            "--label",
            "factory-ready",
        ])
        .env("PATH", path)
        .env("JIRACTRL_CALLS", &calls)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
    let value: Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(value["issues"][0]["key"], "SPS-123");
    assert_eq!(value["issues"][0]["title"], "Fix polling");
    assert_eq!(value["issues"][0]["state"], "Ready To Implement");
    assert_eq!(value["issues"][0]["labels"][0], "factory-ready");

    let invocation = fs::read_to_string(calls).unwrap();
    assert!(invocation.contains(
        "project = \"SPS\" AND creator = currentUser() AND status = \"Ready To Implement\" AND labels = \"factory-ready\" ORDER BY updated ASC"
    ));
    assert!(invocation.contains("--fields summary,description,status,labels"));
}

#[test]
fn jira_adapter_fails_instead_of_silently_truncating_work() {
    let temp = tempfile::tempdir().unwrap();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    fake_jiractrl(&bin, 101);
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let adapter = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(".factory/sources/jira");
    let output = Command::new(adapter)
        .args(["--project", "SPS", "--state", "Ready To Implement"])
        .env("PATH", path)
        .env("JIRACTRL_CALLS", temp.path().join("calls"))
        .output()
        .unwrap();
    assert!(!output.status.success());
    assert!(
        String::from_utf8_lossy(&output.stderr).contains("result was truncated"),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
}
