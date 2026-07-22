#![cfg(unix)]

use std::collections::HashMap;
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::process::Command;
use std::time::Duration;

use factory::config::{
    Config, ExecutionMode, GitHubConfig, PipelineState, SourceConfig, SourceStates,
};
use factory::github::{GitHubClient, ProjectTicketContext};
use factory::storage::{Ledger, RunOutcome};
use factory::workflow::WorkflowCatalog;
use tokio_util::sync::CancellationToken;

struct Fixture {
    _temp: tempfile::TempDir,
    repository: PathBuf,
    config: Config,
    catalog: WorkflowCatalog,
    ledger_path: PathBuf,
    gh: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        fs::create_dir_all(repository.join(".factory/workflows")).unwrap();
        assert!(
            Command::new("git")
                .args(["init", "--quiet", "-b", "main"])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        fs::write(
            repository.join(".factory/workflows/triage-ticket.md"),
            "+++\nstate = \"ready_for_spec\"\n+++\n\nTriage issue.\n",
        )
        .unwrap();
        fs::write(
            repository.join(".factory/workflows/implement-ready-ticket.md"),
            "+++\nstate = \"ready_to_implement\"\n+++\n\nImplement issue.\n",
        )
        .unwrap();
        fs::write(repository.join(".status-option"), "rfs").unwrap();
        fs::write(repository.join(".status-updated"), "2026-07-21T10:00:00Z").unwrap();
        fs::write(repository.join(".author-id"), "U_1").unwrap();
        fs::write(repository.join(".author-login"), "owainlewis").unwrap();
        fs::write(repository.join(".issue-state"), "OPEN").unwrap();
        let workspace_root = temp.path().join("workspaces");
        fs::create_dir(&workspace_root).unwrap();
        let source = SourceConfig {
            owner: "owainlewis".to_owned(),
            project_number: 16,
            status_field: "Status".to_owned(),
            trusted_users: vec!["owainlewis".to_owned()],
            states: SourceStates {
                ready_for_spec: "Ready For Spec".to_owned(),
                creating_spec: "Creating Spec".to_owned(),
                ready_to_implement: "Ready To Implement".to_owned(),
                implementing: "Implementing".to_owned(),
                ready_to_review: "Reviewing".to_owned(),
                done: "Done".to_owned(),
            },
        };
        let config = Config {
            repositories: vec![repository.canonicalize().unwrap()],
            poll_every: Duration::from_millis(20),
            default_runtime: "codex".to_owned(),
            default_timeout: Duration::from_secs(120),
            maximum_timeout: Duration::from_secs(300),
            max_concurrent_runs: 1,
            max_concurrent_runs_per_repository: 1,
            workspace_root,
            data_directory: temp.path().join("data"),
            execution_mode: ExecutionMode::Worktree,
            worker: None,
            source: Some(source),
            github: GitHubConfig {
                trusted_approvers: vec!["owainlewis".to_owned()],
                ready_label: "factory:ready".to_owned(),
                proposed_label: "factory:proposed".to_owned(),
                needs_review_label: "factory:needs-review".to_owned(),
            },
        };
        let catalog = WorkflowCatalog::load(&config).unwrap();
        let gh = temp.path().join("gh");
        fs::write(
            &gh,
            r##"#!/bin/sh
if [ "$1" = "--version" ]; then echo "gh version 2.80.0"; exit 0; fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then echo authenticated; exit 0; fi
if [ "$1" = "repo" ] && [ "$2" = "view" ]; then echo "example/repo"; exit 0; fi
if [ "$1" = "project" ] && [ "$2" = "view" ]; then printf '{"id":"PVT_%s"}' "$3"; exit 0; fi
if [ "$1" = "project" ] && [ "$2" = "field-list" ]; then
  if [ -f .missing-field ]; then printf '{"fields":[]}'; exit 0; fi
  cs=cs
  if [ -f .duplicate-option ]; then cs=rfs; fi
  printf '{"fields":[{"id":"FIELD_STATUS","name":"Status","type":"ProjectV2SingleSelectField","options":[{"id":"rfs","name":"Ready For Spec"},{"id":"%s","name":"Creating Spec"},{"id":"rti","name":"Ready To Implement"},{"id":"impl","name":"Implementing"},{"id":"review","name":"Reviewing"},{"id":"done","name":"Done"}]}]}' "$cs"
  exit 0
fi
if [ "$1" = "project" ] && [ "$2" = "item-edit" ]; then
  if [ -f .edit-fail-before ]; then echo unavailable >&2; exit 1; fi
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "--single-select-option-id" ]; then printf '%s' "$2" > .status-option; break; fi
    shift
  done
  printf '2026-07-21T10:01:00Z' > .status-updated
  if [ -f .edit-fail-after ]; then echo 'response lost' >&2; exit 1; fi
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "user" ]; then printf '{"id":99,"login":"factory-bot"}'; exit 0; fi
if [ "$1" = "api" ] && [ "$2" = "users/owainlewis" ]; then printf '{"id":1,"login":"owainlewis","node_id":"U_1"}'; exit 0; fi
if [ "$1" = "api" ] && [ "$2" = "graphql" ]; then
  option=$(cat .status-option)
  updated=$(cat .status-updated)
  author_id=$(cat .author-id)
  author_login=$(cat .author-login)
  issue_state=$(cat .issue-state)
  case "$*" in
    *'query($project:ID!'*)
      if [ -f .empty ]; then printf '{"data":{"node":{"items":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}}}}'; exit 0; fi
      printf '{"data":{"node":{"items":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[{"id":"ITEM_41","updatedAt":"%s","content":{"id":"ISSUE_41","number":41,"title":"Factory pipeline","url":"https://github.com/example/repo/issues/41","state":"%s","updatedAt":"2026-07-21T09:00:00Z","author":{"id":"%s","login":"%s"},"repository":{"nameWithOwner":"example/repo"}},"fieldValueByName":{"optionId":"%s","name":"state","updatedAt":"%s"}}]}}}}' "$updated" "$issue_state" "$author_id" "$author_login" "$option" "$updated"
      ;;
    *)
      printf '{"data":{"node":{"id":"ITEM_41","updatedAt":"%s","content":{"id":"ISSUE_41","number":41,"title":"Factory pipeline","url":"https://github.com/example/repo/issues/41","state":"%s","updatedAt":"2026-07-21T09:00:00Z","author":{"id":"%s","login":"%s"},"repository":{"nameWithOwner":"example/repo"}},"fieldValues":{"nodes":[{}, {"optionId":"%s","name":"state","updatedAt":"%s","field":{"id":"FIELD_STATUS"}}]}}}}' "$updated" "$issue_state" "$author_id" "$author_login" "$option" "$updated"
      ;;
  esac
  exit 0
fi
echo "unexpected fake gh: $*" >&2
exit 64
"##,
        )
        .unwrap();
        let mut permissions = fs::metadata(&gh).unwrap().permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(&gh, permissions).unwrap();
        Self {
            _temp: temp,
            repository,
            config,
            catalog,
            ledger_path: PathBuf::new(),
            gh,
        }
        .with_ledger_path()
    }

    fn with_ledger_path(mut self) -> Self {
        self.ledger_path = self.repository.parent().unwrap().join("factory.db");
        self
    }

    async fn poll(&self) -> (factory::github::PollReport, Ledger) {
        let mut ledger = Ledger::open(&self.ledger_path).unwrap();
        let report = GitHubClient::new(&self.gh)
            .poll_once(&self.config, &self.catalog, &mut ledger)
            .await
            .unwrap();
        (report, ledger)
    }

    fn set_state(&self, option: &str, updated_at: &str) {
        fs::write(self.repository.join(".status-option"), option).unwrap();
        fs::write(self.repository.join(".status-updated"), updated_at).unwrap();
    }
}

#[tokio::test]
async fn dispatches_both_ready_states_and_claims_their_active_states() {
    let fixture = Fixture::new();
    let (report, mut ledger) = fixture.poll().await;
    assert_eq!(report.tasks_created(), 1);
    ledger
        .register_daemon_owner("owner", std::process::id())
        .unwrap();
    let runtimes = HashMap::from([
        (
            (
                "example/repo".to_owned(),
                "triage-ticket".to_owned(),
                "ticket".to_owned(),
            ),
            "codex".to_owned(),
        ),
        (
            (
                "example/repo".to_owned(),
                "implement-ready-ticket".to_owned(),
                "ticket".to_owned(),
            ),
            "codex".to_owned(),
        ),
    ]);
    let triage_claim = ledger
        .claim_and_start_run(
            &["example/repo".to_owned()],
            &runtimes,
            "owner",
            std::process::id(),
        )
        .unwrap()
        .unwrap();
    let triage = triage_claim.task.clone();
    let context: ProjectTicketContext =
        serde_json::from_str(triage.payload.as_deref().unwrap()).unwrap();
    assert_eq!(context.expected_state, PipelineState::ReadyForSpec);
    GitHubClient::new(&fixture.gh)
        .authorize_project_claim(
            &fixture.repository,
            fixture.config.source.as_ref().unwrap(),
            &triage,
            &mut ledger,
            &CancellationToken::new(),
        )
        .await
        .unwrap();
    assert_eq!(
        fs::read_to_string(fixture.repository.join(".status-option")).unwrap(),
        "cs"
    );
    GitHubClient::new(&fixture.gh)
        .authorize_project_claim(
            &fixture.repository,
            fixture.config.source.as_ref().unwrap(),
            &triage,
            &mut ledger,
            &CancellationToken::new(),
        )
        .await
        .unwrap();
    ledger
        .finish_run_and_task(
            triage_claim.run.id,
            RunOutcome::Succeeded,
            Some("done"),
            None,
            None,
        )
        .unwrap();

    fixture.set_state("rti", "2026-07-21T11:00:00Z");
    let (report, mut ledger) = fixture.poll().await;
    assert_eq!(report.tasks_created(), 1);
    ledger
        .register_daemon_owner("owner-2", std::process::id())
        .unwrap();
    let implementation_claim = ledger
        .claim_and_start_run(
            &["example/repo".to_owned()],
            &runtimes,
            "owner-2",
            std::process::id(),
        )
        .unwrap()
        .unwrap();
    let implementation = implementation_claim.task;
    GitHubClient::new(&fixture.gh)
        .authorize_project_claim(
            &fixture.repository,
            fixture.config.source.as_ref().unwrap(),
            &implementation,
            &mut ledger,
            &CancellationToken::new(),
        )
        .await
        .unwrap();
    assert_eq!(
        fs::read_to_string(fixture.repository.join(".status-option")).unwrap(),
        "impl"
    );
}

#[tokio::test]
async fn deduplicates_restarts_and_rearms_after_leaving_the_ready_state() {
    let fixture = Fixture::new();
    let (_, mut ledger) = fixture.poll().await;
    assert_eq!(fixture.poll().await.0.tasks_created(), 0);
    ledger
        .register_daemon_owner("owner", std::process::id())
        .unwrap();
    let runtimes = HashMap::from([(
        (
            "example/repo".to_owned(),
            "triage-ticket".to_owned(),
            "ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let claimed = ledger
        .claim_and_start_run(
            &["example/repo".to_owned()],
            &runtimes,
            "owner",
            std::process::id(),
        )
        .unwrap()
        .unwrap();
    ledger
        .finish_run_and_task(
            claimed.run.id,
            RunOutcome::Succeeded,
            Some("done"),
            None,
            None,
        )
        .unwrap();
    fixture.set_state("cs", "2026-07-21T10:05:00Z");
    assert_eq!(fixture.poll().await.0.tasks_created(), 0);
    fixture.set_state("rfs", "2026-07-21T12:00:00Z");
    assert_eq!(fixture.poll().await.0.tasks_created(), 1);
}

#[tokio::test]
async fn review_reentry_creates_one_new_implementation_generation() {
    let fixture = Fixture::new();
    fixture.set_state("rti", "2026-07-21T14:00:00Z");
    let (first_report, mut ledger) = fixture.poll().await;
    assert_eq!(first_report.tasks_created(), 1);
    ledger
        .register_daemon_owner("implementation-owner", std::process::id())
        .unwrap();
    let runtimes = HashMap::from([(
        (
            "example/repo".to_owned(),
            "implement-ready-ticket".to_owned(),
            "ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let first = ledger
        .claim_and_start_run(
            &["example/repo".to_owned()],
            &runtimes,
            "implementation-owner",
            std::process::id(),
        )
        .unwrap()
        .unwrap();
    GitHubClient::new(&fixture.gh)
        .authorize_project_claim(
            &fixture.repository,
            fixture.config.source.as_ref().unwrap(),
            &first.task,
            &mut ledger,
            &CancellationToken::new(),
        )
        .await
        .unwrap();
    ledger
        .finish_run_and_task(
            first.run.id,
            RunOutcome::Succeeded,
            Some("existing pull request ready for review"),
            None,
            None,
        )
        .unwrap();

    fixture.set_state("review", "2026-07-21T15:00:00Z");
    assert_eq!(fixture.poll().await.0.tasks_created(), 0);
    fixture.set_state("rti", "2026-07-21T16:00:00Z");
    let (reentry_report, ledger) = fixture.poll().await;
    assert_eq!(reentry_report.tasks_created(), 1);
    let tasks = ledger.tasks().unwrap();
    assert_eq!(tasks.len(), 2);
    assert!(tasks.iter().all(|task| {
        task.workflow == "implement-ready-ticket" && task.source_item.as_deref() == Some("41")
    }));
    assert_ne!(tasks[0].identity_key, tasks[1].identity_key);

    assert_eq!(fixture.poll().await.0.tasks_created(), 0);
    drop(ledger);
    assert_eq!(fixture.poll().await.0.tasks_created(), 0);
}

#[tokio::test]
async fn ignores_untrusted_closed_and_non_ready_issues() {
    let fixture = Fixture::new();
    fs::write(fixture.repository.join(".author-id"), "U_OTHER").unwrap();
    assert_eq!(fixture.poll().await.0.tasks_created(), 0);
    fs::write(fixture.repository.join(".author-id"), "U_1").unwrap();
    fs::write(fixture.repository.join(".issue-state"), "CLOSED").unwrap();
    assert_eq!(fixture.poll().await.0.tasks_created(), 0);
    fs::write(fixture.repository.join(".issue-state"), "OPEN").unwrap();
    fixture.set_state("review", "2026-07-21T13:00:00Z");
    assert_eq!(fixture.poll().await.0.tasks_created(), 0);
}

#[tokio::test]
async fn empty_project_poll_creates_no_task() {
    let fixture = Fixture::new();
    fs::write(fixture.repository.join(".empty"), "").unwrap();
    let (report, ledger) = fixture.poll().await;
    assert_eq!(report.repositories[0].issues_seen, 0);
    assert_eq!(report.tasks_created(), 0);
    assert!(ledger.tasks().unwrap().is_empty());
}

#[tokio::test]
async fn validation_rejects_missing_fields_and_duplicate_provider_options() {
    let fixture = Fixture::new();
    let client = GitHubClient::new(&fixture.gh);
    fs::write(fixture.repository.join(".missing-field"), "").unwrap();
    let error = client
        .validate_project_source(
            &fixture.repository,
            fixture.config.source.as_ref().unwrap(),
            &CancellationToken::new(),
        )
        .await
        .unwrap_err();
    assert!(format!("{error:#}").contains("does not contain status field"));
    fs::remove_file(fixture.repository.join(".missing-field")).unwrap();
    fs::write(fixture.repository.join(".duplicate-option"), "").unwrap();
    let error = client
        .validate_project_source(
            &fixture.repository,
            fixture.config.source.as_ref().unwrap(),
            &CancellationToken::new(),
        )
        .await
        .unwrap_err();
    assert!(format!("{error:#}").contains("distinct project option"));
}

#[tokio::test]
async fn queued_task_cannot_mutate_a_different_configured_project() {
    let fixture = Fixture::new();
    let (_, mut ledger) = fixture.poll().await;
    ledger
        .register_daemon_owner("owner", std::process::id())
        .unwrap();
    let runtimes = HashMap::from([(
        (
            "example/repo".to_owned(),
            "triage-ticket".to_owned(),
            "ticket".to_owned(),
        ),
        "codex".to_owned(),
    )]);
    let claimed = ledger
        .claim_and_start_run(
            &["example/repo".to_owned()],
            &runtimes,
            "owner",
            std::process::id(),
        )
        .unwrap()
        .unwrap();
    let mut changed = fixture.config.source.clone().unwrap();
    changed.project_number = 99;
    let error = GitHubClient::new(&fixture.gh)
        .authorize_project_claim(
            &fixture.repository,
            &changed,
            &claimed.task,
            &mut ledger,
            &CancellationToken::new(),
        )
        .await
        .unwrap_err();
    assert!(format!("{error:#}").contains("configuration changed"));
    assert_eq!(
        fs::read_to_string(fixture.repository.join(".status-option")).unwrap(),
        "rfs"
    );
}

#[tokio::test]
async fn project_transition_failures_recover_before_and_after_remote_apply() {
    for failure_marker in [".edit-fail-before", ".edit-fail-after"] {
        let fixture = Fixture::new();
        let (_, mut ledger) = fixture.poll().await;
        ledger
            .register_daemon_owner("owner", std::process::id())
            .unwrap();
        let runtimes = HashMap::from([(
            (
                "example/repo".to_owned(),
                "triage-ticket".to_owned(),
                "ticket".to_owned(),
            ),
            "codex".to_owned(),
        )]);
        let first = ledger
            .claim_and_start_run(
                &["example/repo".to_owned()],
                &runtimes,
                "owner",
                std::process::id(),
            )
            .unwrap()
            .unwrap();
        fs::write(fixture.repository.join(failure_marker), "").unwrap();
        GitHubClient::new(&fixture.gh)
            .authorize_project_claim(
                &fixture.repository,
                fixture.config.source.as_ref().unwrap(),
                &first.task,
                &mut ledger,
                &CancellationToken::new(),
            )
            .await
            .unwrap_err();
        ledger
            .fail_prelaunch_and_requeue(first.run.id, "ambiguous project transition")
            .unwrap();
        fs::remove_file(fixture.repository.join(failure_marker)).unwrap();
        let recovery = ledger
            .claim_and_start_run(
                &["example/repo".to_owned()],
                &runtimes,
                "owner",
                std::process::id(),
            )
            .unwrap()
            .unwrap();
        GitHubClient::new(&fixture.gh)
            .authorize_project_claim(
                &fixture.repository,
                fixture.config.source.as_ref().unwrap(),
                &recovery.task,
                &mut ledger,
                &CancellationToken::new(),
            )
            .await
            .unwrap();
        assert_eq!(
            fs::read_to_string(fixture.repository.join(".status-option")).unwrap(),
            "cs"
        );
    }
}
