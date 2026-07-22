#![cfg(unix)]

use std::collections::HashMap;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::process::Command;
use std::time::Duration;

use factory::config::{Config, ExecutionMode, SourceConfig, TriggerConfig, TriggerKind};
use factory::github::GitHubClient;
use factory::storage::{Ledger, RunOutcome};
use factory::workflow::WorkflowCatalog;
use tokio_util::sync::CancellationToken;

struct Fixture {
    _temp: tempfile::TempDir,
    repository: std::path::PathBuf,
    config: Config,
    catalog: WorkflowCatalog,
    gh: std::path::PathBuf,
    ledger: Ledger,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        fs::create_dir_all(repository.join(".factory/workflows")).unwrap();
        assert!(
            Command::new("git")
                .args(["init", "--quiet"])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        assert!(
            Command::new("git")
                .args(["remote", "add", "origin", "git@github.com:example/repo.git",])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        let workflow = repository.join(".factory/workflows/implement.md");
        fs::write(&workflow, "Fetch issue $FACTORY_ISSUE and implement it.\n").unwrap();
        fs::write(repository.join(".label-present"), "yes").unwrap();
        fs::write(repository.join(".event-id"), "101").unwrap();
        let repository = repository.canonicalize().unwrap();
        let config = Config {
            repositories: vec![repository.clone()],
            poll_every: Duration::from_secs(30),
            default_runtime: "codex".to_owned(),
            default_timeout: Duration::from_secs(120),
            maximum_timeout: Duration::from_secs(300),
            max_concurrent_runs: 1,
            max_concurrent_runs_per_repository: 1,
            workspace_root: temp.path().join("worktrees"),
            data_directory: temp.path().join("data"),
            execution_mode: ExecutionMode::Worktree,
            worker: None,
            triggers: vec![TriggerConfig {
                id: "implement".to_owned(),
                workflow,
                timeout: Duration::from_secs(120),
                kind: TriggerKind::Label("agent:ready".to_owned()),
            }],
            source: Some(SourceConfig {
                owner: "example".to_owned(),
                project_number: 16,
                status_field: "Status".to_owned(),
                trusted_users: vec!["owainlewis".to_owned()],
            }),
        };
        let catalog = WorkflowCatalog::load(&config).unwrap();
        let gh = temp.path().join("gh");
        fs::write(
            &gh,
            r##"#!/bin/sh
if [ "$1" = "--version" ]; then echo "gh version 2.80.0"; exit 0; fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then echo authenticated; exit 0; fi
if [ "$1" = "repo" ] && [ "$2" = "view" ]; then echo "example/repo"; exit 0; fi
if [ "$1" = "api" ]; then
  endpoint="$2"
  if [ "$2" = "--paginate" ]; then endpoint="$4"; fi
  case "$endpoint" in
    users/owainlewis) printf '{"id":42,"login":"owainlewis","node_id":"U_42"}' ;;
    repos/example/repo/issues\?*)
      labels='[]'; [ "$(cat .label-present)" = yes ] && labels='[{"name":"agent:ready"}]'
      printf '[[{"number":7,"html_url":"https://github.com/example/repo/issues/7","title":"Fix it","body":"Clear acceptance criteria","labels":%s,"updated_at":"2026-07-22T10:00:00Z","state":"open","pull_request":null,"user":{"id":42,"login":"owainlewis"}}]]' "$labels"
      ;;
    repos/example/repo/issues/7/timeline*)
      printf '[[{"id":%s,"event":"labeled","actor":{"id":42,"login":"owainlewis"},"label":{"name":"agent:ready"},"created_at":"2026-07-22T09:00:00Z"}]]' "$(cat .event-id)"
      ;;
    repos/example/repo/issues/7)
      labels='[]'; [ "$(cat .label-present)" = yes ] && labels='[{"name":"agent:ready"}]'
      printf '{"number":7,"html_url":"https://github.com/example/repo/issues/7","title":"Fix it","body":"Clear acceptance criteria","labels":%s,"updated_at":"2026-07-22T10:00:00Z","state":"open","pull_request":null,"user":{"id":42,"login":"owainlewis"}}' "$labels"
      ;;
    *) echo "unexpected endpoint $endpoint" >&2; exit 1 ;;
  esac
  exit 0
fi
echo "unexpected gh command: $*" >&2
exit 1
"##,
        )
        .unwrap();
        let mut permissions = fs::metadata(&gh).unwrap().permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(&gh, permissions).unwrap();
        let ledger = Ledger::open_in(&config.data_directory).unwrap();
        Self {
            _temp: temp,
            repository,
            config,
            catalog,
            gh,
            ledger,
        }
    }

    async fn poll(&mut self) -> usize {
        let report = GitHubClient::new(&self.gh)
            .poll_once(&self.config, &self.catalog, &mut self.ledger)
            .await
            .unwrap();
        assert_eq!(report.failures(), 0, "{report:?}");
        report.tasks_created()
    }
}

#[tokio::test]
async fn label_trigger_runs_once_per_label_entry_and_rearms() {
    let mut fixture = Fixture::new();
    assert_eq!(fixture.poll().await, 1);
    assert_eq!(fixture.poll().await, 0);
    fixture
        .ledger
        .register_daemon_owner("owner", std::process::id())
        .unwrap();
    let runtimes = HashMap::from([(
        (
            "example/repo".to_owned(),
            "implement".to_owned(),
            "ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let claimed = fixture
        .ledger
        .claim_and_start_run(
            &["example/repo".to_owned()],
            &runtimes,
            "owner",
            std::process::id(),
        )
        .unwrap()
        .unwrap();
    fixture
        .ledger
        .finish_run_and_task(
            claimed.run.id,
            RunOutcome::Succeeded,
            Some("done"),
            None,
            None,
        )
        .unwrap();

    fs::write(fixture.repository.join(".label-present"), "no").unwrap();
    assert_eq!(fixture.poll().await, 0);
    fs::write(fixture.repository.join(".label-present"), "yes").unwrap();
    fs::write(fixture.repository.join(".event-id"), "102").unwrap();
    assert_eq!(fixture.poll().await, 1);
}

#[tokio::test]
async fn claim_revalidates_the_live_label() {
    let mut fixture = Fixture::new();
    assert_eq!(fixture.poll().await, 1);
    let task = fixture.ledger.tasks().unwrap().remove(0);
    fs::write(fixture.repository.join(".label-present"), "no").unwrap();
    let error = GitHubClient::new(&fixture.gh)
        .authorize_label_claim(
            &fixture.repository,
            fixture.config.source.as_ref().unwrap(),
            "agent:ready",
            &task,
            &CancellationToken::new(),
        )
        .await
        .unwrap_err();
    assert!(format!("{error:#}").contains("changed before claim"));
}
