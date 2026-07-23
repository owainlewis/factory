#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::process::Command;

use serde_json::Value;

const RAW_ISSUES: &str = r#"[{"number":42,"title":"Fix polling","body":"The daemon misses eligible work.","url":"https://github.com/example/repository/issues/42","labels":[{"name":"factory:ready-to-implement"}]}]"#;

fn fake_gh(bin: &std::path::Path, raw_issues: &str) {
    let executable = bin.join("gh");
    fs::write(
        &executable,
        format!(
            r#"#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$GH_CALLS"
if [ "$1" = "issue" ] && [ "$2" = "list" ]; then
  shift 2
  jqexpr=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --jq) jqexpr="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  printf '%s' '{raw_issues}' | jq "$jqexpr"
  exit 0
fi
exit 64
"#
        ),
    )
    .unwrap();
    let mut permissions = fs::metadata(&executable).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(executable, permissions).unwrap();
}

fn fake_gh_with_generated_count(bin: &std::path::Path, count: usize) {
    let executable = bin.join("gh");
    fs::write(
        &executable,
        format!(
            r#"#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$GH_CALLS"
if [ "$1" = "issue" ] && [ "$2" = "list" ]; then
  shift 2
  jqexpr=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --jq) jqexpr="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  jq -n '[range({count}) | {{number: ., title: "t", body: "", url: "", labels: []}}]' | jq "$jqexpr"
  exit 0
fi
exit 64
"#
        ),
    )
    .unwrap();
    let mut permissions = fs::metadata(&executable).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(executable, permissions).unwrap();
}

#[test]
fn github_adapter_lists_issues_by_state_and_label_with_no_project_or_author_filtering() {
    let temp = tempfile::tempdir().unwrap();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    fake_gh(&bin, RAW_ISSUES);
    let calls = temp.path().join("calls");
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let adapter = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(".factory/sources/github");
    let output = Command::new(adapter)
        .args(["--state", "open", "--label", "factory:ready-to-implement"])
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
    assert_eq!(value["issues"][0]["key"], "#42");
    assert_eq!(value["issues"][0]["title"], "Fix polling");
    assert_eq!(
        value["issues"][0]["description"],
        "The daemon misses eligible work."
    );
    assert_eq!(value["issues"][0]["state"], "open");
    assert_eq!(
        value["issues"][0]["labels"][0],
        "factory:ready-to-implement"
    );
    assert_eq!(
        value["issues"][0]["url"],
        "https://github.com/example/repository/issues/42"
    );
    assert!(value["issues"][0].get("author").is_none());

    let invocation = fs::read_to_string(calls).unwrap();
    assert!(invocation.contains("issue list --state open"));
    assert!(invocation.contains("--label factory:ready-to-implement"));
    assert!(!invocation.contains("project"));
    assert!(!invocation.contains("graphql"));
    assert!(!invocation.contains("trusted-user"));
}

#[test]
fn github_adapter_requires_state() {
    let adapter = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(".factory/sources/github");
    let output = Command::new(adapter)
        .args(["--label", "factory:ready-to-implement"])
        .output()
        .unwrap();
    assert!(!output.status.success());
    assert!(String::from_utf8_lossy(&output.stderr).contains("--state is required"));
}

#[test]
fn github_adapter_fails_instead_of_silently_truncating_work() {
    let temp = tempfile::tempdir().unwrap();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    fake_gh_with_generated_count(&bin, 1001);
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let adapter = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(".factory/sources/github");
    let output = Command::new(adapter)
        .args(["--state", "open"])
        .env("PATH", path)
        .env("GH_CALLS", temp.path().join("calls"))
        .output()
        .unwrap();
    assert!(!output.status.success());
    assert!(
        String::from_utf8_lossy(&output.stderr).contains("truncated"),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
}

#[test]
fn github_adapter_accepts_exactly_the_maximum_result_count() {
    let temp = tempfile::tempdir().unwrap();
    let bin = temp.path().join("bin");
    fs::create_dir(&bin).unwrap();
    fake_gh_with_generated_count(&bin, 1000);
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let adapter = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join(".factory/sources/github");
    let output = Command::new(adapter)
        .args(["--state", "open"])
        .env("PATH", path)
        .env("GH_CALLS", temp.path().join("calls"))
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
    let value: Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(value["issues"].as_array().unwrap().len(), 1000);
}
