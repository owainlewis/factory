#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::process::Command;

use serde_json::Value;

fn fake_gh(bin: &std::path::Path) {
    let executable = bin.join("gh");
    fs::write(
        &executable,
        r#"#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$GH_CALLS"
case "$1 $2" in
  "repo view")
    printf 'owainlewis/factory\n'
    ;;
  "project view")
    printf 'PVT_FACTORY\n'
    ;;
  "project field-list")
    printf '%s\n' "${PROJECT_FIELD_SUMMARY:-1	ProjectV2SingleSelectField	1}"
    ;;
  "issue list")
    printf '2\n'
    ;;
  "api graphql")
    jqexpr=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --jq) jqexpr="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    jq -c "$jqexpr" <<'JSON'
{"data":{"search":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
  {"number":42,"title":"Public report","body":"Observed failure.","url":"https://github.com/owainlewis/factory/issues/42","author":{"login":"outside-reporter"},"labels":{"nodes":[{"name":"bug"}]},"projectItems":{"nodes":[{"project":{"id":"PVT_FACTORY"},"fieldValueByName":{"name":"Ready For Spec"}}]}},
  {"number":43,"title":"Not ready","body":"","url":"https://github.com/owainlewis/factory/issues/43","author":{"login":"owainlewis"},"labels":{"nodes":[{"name":"enhancement"}]},"projectItems":{"nodes":[{"project":{"id":"PVT_FACTORY"},"fieldValueByName":{"name":"Creating Spec"}}]}}
]}}}
JSON
    ;;
  *)
    exit 64
    ;;
esac
"#,
    )
    .unwrap();
    let mut permissions = fs::metadata(&executable).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(executable, permissions).unwrap();
}

fn adapter() -> std::path::PathBuf {
    std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(".factory/sources/github-project")
}

#[test]
fn lists_project_status_issues_without_filtering_public_reporters() {
    let temp = tempfile::tempdir().unwrap();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    fake_gh(&bin);
    let calls = temp.path().join("calls");
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let output = Command::new(adapter())
        .args([
            "--project-owner",
            "owainlewis",
            "--project-number",
            "16",
            "--status-field",
            "Status",
            "--state",
            "Ready For Spec",
        ])
        .env("PATH", path)
        .env("GH_CALLS", &calls)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
    let value: Value = serde_json::from_slice(&output.stdout).unwrap();
    let issues = value["issues"].as_array().unwrap();
    assert_eq!(issues.len(), 1);
    assert_eq!(issues[0]["key"], "#42");
    assert_eq!(issues[0]["state"], "Ready For Spec");
    assert_eq!(issues[0]["labels"][0], "bug");
    assert!(issues[0].get("author").is_none());

    let invocation = fs::read_to_string(calls).unwrap();
    assert!(invocation.contains("project view 16 --owner owainlewis"));
    assert!(invocation.contains("project field-list 16 --owner owainlewis --limit 1000"));
    assert!(invocation.contains("statusField=Status"));
    assert!(!invocation.contains("trusted-user"));
}

#[test]
fn requires_project_configuration_and_state() {
    let output = Command::new(adapter()).output().unwrap();
    assert!(!output.status.success());
    assert!(String::from_utf8_lossy(&output.stderr).contains("--project-owner is required"));
}

#[test]
fn rejects_a_missing_status_field() {
    let temp = tempfile::tempdir().unwrap();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    fake_gh(&bin);
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let output = Command::new(adapter())
        .args([
            "--project-owner",
            "owainlewis",
            "--project-number",
            "16",
            "--status-field",
            "Missing",
            "--state",
            "Ready For Spec",
        ])
        .env("PATH", path)
        .env("GH_CALLS", temp.path().join("calls"))
        .env("PROJECT_FIELD_SUMMARY", "0\t\t0")
        .output()
        .unwrap();
    assert!(!output.status.success());
    assert!(
        String::from_utf8_lossy(&output.stderr)
            .contains("Project field 'Missing' must exist exactly once")
    );
}

#[test]
fn rejects_a_missing_status_option() {
    let temp = tempfile::tempdir().unwrap();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    fake_gh(&bin);
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let output = Command::new(adapter())
        .args([
            "--project-owner",
            "owainlewis",
            "--project-number",
            "16",
            "--status-field",
            "Status",
            "--state",
            "Missing",
        ])
        .env("PATH", path)
        .env("GH_CALLS", temp.path().join("calls"))
        .env("PROJECT_FIELD_SUMMARY", "1\tProjectV2SingleSelectField\t0")
        .output()
        .unwrap();
    assert!(!output.status.success());
    assert!(
        String::from_utf8_lossy(&output.stderr)
            .contains("Project field 'Status' must contain status 'Missing' exactly once")
    );
}
