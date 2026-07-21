#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use assert_cmd::Command as AssertCommand;
use chrono::Utc;
use factory::approval::{ApprovalArtifact, approved_content_hash, render};
use factory::config::{Config, GitHubConfig};
use factory::github::{GitHubClient, TicketContext};
use factory::storage::{Ledger, RunOutcome, TaskIdentity};
use factory::workflow::{
    Trigger, WorkflowCatalog, scheduled_workflow_fingerprint, workflow_content_hash,
};
use tokio_util::sync::CancellationToken;

struct Fixture {
    _temp: tempfile::TempDir,
    repositories: Vec<PathBuf>,
    config_path: PathBuf,
    config: Config,
    data_home: PathBuf,
    ledger_path: PathBuf,
    gh: PathBuf,
}

impl Fixture {
    fn new(repository_count: usize) -> Self {
        let temp = tempfile::tempdir().unwrap();
        let mut repositories = Vec::new();
        for index in 0..repository_count {
            let repository = temp.path().join(format!("repo-{index}"));
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
                    .args([
                        "remote",
                        "add",
                        "origin",
                        &format!("git@github.com:example/repo-{index}.git")
                    ])
                    .current_dir(&repository)
                    .status()
                    .unwrap()
                    .success()
            );
            fs::write(
                repository.join(".factory/workflows/implement-ready-ticket.md"),
                "+++\nlabel = \"factory:ready\"\n+++\n\nImplement the ticket.\n",
            )
            .unwrap();
            fs::write(
                repository.join(".gh-name"),
                format!("example/repo-{index}\n"),
            )
            .unwrap();
            fs::write(repository.join(".issues.json"), "[[]]").unwrap();
            repositories.push(repository.canonicalize().unwrap());
        }
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&workspace).unwrap();
        let data_home = temp.path().join("factory-data");
        let config_path = repositories[0].join(".factory/config.toml");
        AssertCommand::cargo_bin("factory")
            .unwrap()
            .current_dir(&repositories[0])
            .env("FACTORY_DATA_HOME", &data_home)
            .arg("init")
            .assert()
            .success();
        fs::write(
            &config_path,
            "version = 1\npoll_every = \"20ms\"\ndefault_runtime = \"codex\"\ndefault_timeout = \"2h\"\nmaximum_timeout = \"8h\"\nmax_concurrent_runs = 2\n\n[github]\ntrusted_approvers = [\"owainlewis\"]\nready_label = \"factory:ready\"\nproposed_label = \"factory:proposed\"\nneeds_review_label = \"factory:needs-review\"\n",
        )
        .unwrap();
        let config = Config {
            repositories: repositories.clone(),
            poll_every: Duration::from_millis(20),
            default_runtime: "codex".into(),
            default_timeout: Duration::from_secs(2 * 60 * 60),
            maximum_timeout: Duration::from_secs(8 * 60 * 60),
            max_concurrent_runs: 2,
            max_concurrent_runs_per_repository: 2,
            workspace_root: workspace,
            data_directory: temp.path().join("data"),
            source: None,
            github: GitHubConfig {
                trusted_approvers: vec!["owainlewis".into()],
                ready_label: "factory:ready".into(),
                proposed_label: "factory:proposed".into(),
                needs_review_label: "factory:needs-review".into(),
            },
        };
        let gh = temp.path().join("gh");
        fs::write(
            &gh,
            r##"#!/bin/sh
if [ -f "$0.hang" ]; then echo $$ > "$0.hang.pid"; exec sleep 30; fi
if [ "$1" = "--version" ]; then echo "gh version 2.80.0"; exit 0; fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  if [ -f "$0.auth-fail" ]; then echo "not logged in" >&2; exit 1; fi
  echo "logged in"; exit 0
fi
if [ "$1" = "repo" ] && [ "$2" = "view" ]; then
  if [ -f ".gh-fail" ]; then echo "repository denied" >&2; exit 1; fi
  cat .gh-name; exit 0
fi
printf '%s\n' "$*" >> "$0.log"
if [ "$1" = "issue" ] && [ "$2" = "edit" ]; then
  if [ "$4" = "--add-label" ]; then touch ".ready-$3"; else rm -f ".ready-$3"; fi
  exit 0
fi
if [ "$1" = "api" ]; then
  if [ -f ".api-fail" ]; then echo "API rate limit exceeded" >&2; exit 1; fi
  if [ "$2" = "--paginate" ] || [ "$2" = "--method" ]; then endpoint="$4"; else endpoint="$2"; fi
  case "$endpoint" in
    user|users/*) printf '{"id":42,"login":"owainlewis"}' ;;
    */timeline*)
      number=$(printf '%s' "$endpoint" | sed -E 's#^.*/issues/([0-9]+)/timeline.*#\1#')
      file=".timeline-$number.json"
      if [ -f "$file" ]; then cat "$file"; else printf '[[]]'; fi
      ;;
    */comments*)
      number=$(printf '%s' "$endpoint" | sed -E 's#^.*/issues/([0-9]+)/comments.*#\1#')
      file=".comments-$number.json"
      if [ "$2" = "--method" ]; then
        printf '%s' "${6#body=}" > ".posted-body-$number"
        printf '314\n'
      elif [ -f ".posted-body-$number" ]; then
        escaped=$(sed 's#\\#\\\\#g; s#"#\\"#g' ".posted-body-$number")
        printf '[[{"id":314,"html_url":"https://example/comment/314","user":{"id":42,"login":"owainlewis"},"body":"%s","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]]' "$escaped"
      elif [ -f "$file" ]; then cat "$file"; else printf '[[]]'; fi
      ;;
    */issues/[0-9]*)
      number=$(printf '%s' "$endpoint" | sed -E 's#^.*/issues/([0-9]+).*#\1#')
      if [ -f ".ready-$number" ]; then cat ".issue-$number-ready.json"; else cat ".issue-$number.json"; fi
      ;;
    *) cat .issues.json ;;
  esac
  exit 0
fi
echo "unexpected fake gh arguments: $*" >&2
exit 64
"##,
        )
        .unwrap();
        let mut permissions = fs::metadata(&gh).unwrap().permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(&gh, permissions).unwrap();
        let ledger_path = temp.path().join("factory.db");
        Self {
            _temp: temp,
            repositories,
            config_path,
            config,
            data_home,
            ledger_path,
            gh,
        }
    }

    async fn poll(&self) -> anyhow::Result<(factory::github::PollReport, Ledger)> {
        let config = self.config.clone();
        let catalog = WorkflowCatalog::load(&config).unwrap();
        let mut ledger = Ledger::open(&self.ledger_path).unwrap();
        let report = GitHubClient::new(&self.gh)
            .poll_once(&config, &catalog, &mut ledger)
            .await?;
        Ok((report, ledger))
    }

    fn approve(&self, repository_index: usize, issue: u64, artifact_id: u64, event_id: u64) {
        let catalog = WorkflowCatalog::load(&self.config).unwrap();
        let workflow = catalog
            .entries
            .iter()
            .find(|entry| entry.id == "implement-ready-ticket")
            .unwrap();
        let workflow_hash = workflow_content_hash(workflow).unwrap();
        let content_hash = approved_content_hash(
            issue,
            &format!("Ticket {issue}"),
            &format!("Body {issue}"),
            &workflow.id,
            &workflow_hash,
        )
        .unwrap();
        let artifact = ApprovalArtifact {
            version: 1,
            issue,
            workflow_id: workflow.id.clone(),
            workflow_hash,
            approved_content_hash: content_hash,
            approver_id: 42,
            nonce: format!("nonce-{artifact_id}"),
        };
        let body = render(&artifact).unwrap();
        fs::write(
            self.repositories[repository_index].join(format!(".comments-{issue}.json")),
            serde_json::json!([[{
                "id": artifact_id,
                "html_url": format!("https://example/comment/{artifact_id}"),
                "user": {"id": 42, "login": "owainlewis"},
                "body": body,
                "created_at": "2026-01-01T00:00:00Z",
                "updated_at": "2026-01-01T00:00:00Z"
            }]])
            .to_string(),
        )
        .unwrap();
        fs::write(
            self.repositories[repository_index].join(format!(".timeline-{issue}.json")),
            serde_json::json!([[{
                "id": event_id,
                "event": "labeled",
                "actor": {"id": 42, "login": "owainlewis"},
                "label": {"name": "factory:ready"},
                "created_at": "2026-01-01T00:00:01Z"
            }]])
            .to_string(),
        )
        .unwrap();
    }
}

fn issue(number: u64, revision: &str, labels: &[&str]) -> String {
    let labels = labels
        .iter()
        .map(|label| format!(r#"{{"name":"{label}"}}"#))
        .collect::<Vec<_>>()
        .join(",");
    format!(
        r#"{{"number":{number},"html_url":"https://github.com/example/repo/issues/{number}","title":"Ticket {number}","body":"Body {number}","labels":[{labels}],"updated_at":"{revision}"}}"#
    )
}

fn write_issues(repository: &Path, pages: &[Vec<String>]) {
    let contents = pages
        .iter()
        .map(|page| format!("[{}]", page.join(",")))
        .collect::<Vec<_>>()
        .join(",");
    fs::write(repository.join(".issues.json"), format!("[{contents}]")).unwrap();
}

#[test]
fn factory_run_once_persists_a_task_without_launching_codex() {
    let fixture = Fixture::new(1);
    write_issues(
        &fixture.repositories[0],
        &[vec![issue(9, "revision-1", &["factory:ready"])]],
    );
    fixture.approve(0, 9, 109, 209);
    let data = fixture._temp.path().join("data");
    let codex_marker = fixture._temp.path().join("codex-launched");
    let codex = fixture._temp.path().join("codex");
    fs::write(
        &codex,
        format!("#!/bin/sh\ntouch '{}'\nexit 99\n", codex_marker.display()),
    )
    .unwrap();
    let mut permissions = fs::metadata(&codex).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(codex, permissions).unwrap();
    let path = format!(
        "{}:{}",
        fixture._temp.path().display(),
        std::env::var("PATH").unwrap()
    );

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--once",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            data.to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("PATH", path)
        .assert()
        .success()
        .stdout(predicates::str::contains("tasks_created=1"))
        .stderr(predicates::str::contains("Factory starting: mode=once"))
        .stderr(predicates::str::contains(
            "Factory loaded: repositories=1 workflows=1",
        ))
        .stderr(predicates::str::contains(
            "Factory evaluating schedules and polling GitHub once...",
        ));

    assert!(!codex_marker.exists());
    let mut ledger = Ledger::open_in(&data).unwrap();
    assert_eq!(
        ledger.claim_next().unwrap().unwrap().source_item.as_deref(),
        Some("9")
    );
}

#[test]
fn factory_approve_posts_an_artifact_then_applies_and_verifies_ready() {
    let fixture = Fixture::new(1);
    let repository = &fixture.repositories[0];
    let unready = issue(14, "revision-1", &[]);
    let ready = issue(14, "revision-2", &["factory:ready"]);
    fs::write(repository.join(".issue-14.json"), unready).unwrap();
    fs::write(repository.join(".issue-14-ready.json"), ready).unwrap();
    fs::write(repository.join(".ready-14"), "").unwrap();
    fs::write(
        repository.join(".comments-14.json"),
        serde_json::json!([[{
            "id": 314,
            "html_url": "https://example/comment/314",
            "user": {"id": 42, "login": "owainlewis"},
            "body": "artifact is recorded by the fake gh argument log",
            "created_at": "2026-01-01T00:00:00Z",
            "updated_at": "2026-01-01T00:00:00Z"
        }]])
        .to_string(),
    )
    .unwrap();
    fs::write(
        repository.join(".timeline-14.json"),
        serde_json::json!([[{
            "id": 414,
            "event": "labeled",
            "actor": {"id": 42, "login": "owainlewis"},
            "label": {"name": "factory:ready"},
            "created_at": "2026-01-01T00:00:01Z"
        }]])
        .to_string(),
    )
    .unwrap();
    let path = format!(
        "{}:{}",
        fixture._temp.path().display(),
        std::env::var("PATH").unwrap()
    );

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .current_dir(repository)
        .args([
            "approve",
            "14",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            fixture.config.data_directory.to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("PATH", path)
        .assert()
        .success()
        .stdout(predicates::str::contains("Approved issue #14"))
        .stdout(predicates::str::contains("approval_artifact: 314"))
        .stdout(predicates::str::contains("ready_label_event: 414"));

    let calls = fs::read_to_string(format!("{}.log", fixture.gh.display())).unwrap();
    assert!(calls.contains("factory-approval:v1"));
    let remove = calls
        .find("issue edit 14 --remove-label factory:ready")
        .unwrap();
    let add = calls
        .find("issue edit 14 --add-label factory:ready")
        .unwrap();
    assert!(remove < add);
}

#[test]
fn factory_run_once_reports_progress_before_authentication_failure() {
    let fixture = Fixture::new(1);
    fs::write(format!("{}.auth-fail", fixture.gh.display()), "").unwrap();
    let data = fixture._temp.path().join("data");
    let path = format!(
        "{}:{}",
        fixture._temp.path().display(),
        std::env::var("PATH").unwrap()
    );

    let output = AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--once",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            data.to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("PATH", path)
        .output()
        .unwrap();

    assert!(!output.status.success());
    let stderr = String::from_utf8(output.stderr).unwrap();
    let starting = stderr.find("Factory starting: mode=once").unwrap();
    let polling = stderr
        .find("Factory evaluating schedules and polling GitHub once...")
        .unwrap();
    let failure = stderr.find("Error:").unwrap();
    assert!(starting < polling);
    assert!(polling < failure);
    assert!(stderr.contains("run gh auth login"));
}

#[test]
fn factory_run_once_fails_for_invalid_ticket_workflow() {
    let fixture = Fixture::new(1);
    fs::write(
        fixture.repositories[0].join(".factory/workflows/implement-ready-ticket.md"),
        "+++\nlabel = \"factory:ready\"\ntimeout = \"0s\"\n+++\n\nINVALID TICKET WORKFLOW\n",
    )
    .unwrap();
    let data = fixture._temp.path().join("data");
    let path = format!(
        "{}:{}",
        fixture._temp.path().display(),
        std::env::var("PATH").unwrap()
    );

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--once",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            data.to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("PATH", path)
        .assert()
        .failure()
        .stderr(predicates::str::contains(
            "Factory cannot start with invalid ticket workflows",
        ))
        .stderr(predicates::str::contains(
            "timeout must be greater than zero",
        ));
    assert!(!data.exists());
}

#[test]
fn factory_run_once_skips_invalid_schedule_and_polls_tickets() {
    let fixture = Fixture::new(1);
    fs::write(
        fixture.repositories[0].join(".factory/workflows/invalid-schedule.md"),
        "+++\nschedule = \"unterminated\ntimezone = \"UTC\"\n+++\n\nINVALID SCHEDULE\n",
    )
    .unwrap();
    write_issues(
        &fixture.repositories[0],
        &[vec![issue(10, "revision-1", &["factory:ready"])]],
    );
    fixture.approve(0, 10, 110, 210);
    let data = fixture._temp.path().join("data");
    let path = format!(
        "{}:{}",
        fixture._temp.path().display(),
        std::env::var("PATH").unwrap()
    );

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--once",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            data.to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("PATH", path)
        .assert()
        .success()
        .stdout(predicates::str::contains("tasks_created=1"))
        .stderr(predicates::str::contains(
            "Factory skipped invalid scheduled workflow",
        ));
}

#[tokio::test]
async fn no_matches_creates_no_tasks() {
    let fixture = Fixture::new(1);
    write_issues(
        &fixture.repositories[0],
        &[vec![issue(1, "revision-1", &["bug"])]],
    );

    let (report, mut ledger) = fixture.poll().await.unwrap();

    assert_eq!(report.tasks_created(), 0);
    assert_eq!(report.repositories[0].issues_seen, 1);
    assert!(ledger.claim_next().unwrap().is_none());
}

#[tokio::test]
async fn ready_label_without_exact_approval_never_creates_a_task() {
    let fixture = Fixture::new(1);
    write_issues(
        &fixture.repositories[0],
        &[vec![issue(12, "revision-1", &["factory:ready"])]],
    );

    let (report, mut ledger) = fixture.poll().await.unwrap();

    assert_eq!(report.tasks_created(), 0);
    assert!(ledger.claim_next().unwrap().is_none());
}

#[tokio::test]
async fn changing_approved_ticket_content_invalidates_authorization() {
    let fixture = Fixture::new(1);
    write_issues(
        &fixture.repositories[0],
        &[vec![issue(13, "revision-1", &["factory:ready"])]],
    );
    fixture.approve(0, 13, 113, 213);
    let changed = issue(13, "revision-2", &["factory:ready"]).replace("Ticket 13", "Changed title");
    write_issues(&fixture.repositories[0], &[vec![changed]]);

    let (report, mut ledger) = fixture.poll().await.unwrap();

    assert_eq!(report.tasks_created(), 0);
    assert!(ledger.claim_next().unwrap().is_none());
}

#[tokio::test]
async fn untrusted_stable_actor_id_invalidates_authorization() {
    let fixture = Fixture::new(1);
    write_issues(
        &fixture.repositories[0],
        &[vec![issue(17, "revision-1", &["factory:ready"])]],
    );
    fixture.approve(0, 17, 117, 217);
    for name in [".comments-17.json", ".timeline-17.json"] {
        let path = fixture.repositories[0].join(name);
        let untrusted = fs::read_to_string(&path)
            .unwrap()
            .replace("\"id\":42", "\"id\":99");
        fs::write(path, untrusted).unwrap();
    }

    let (report, mut ledger) = fixture.poll().await.unwrap();

    assert_eq!(report.tasks_created(), 0);
    assert!(ledger.claim_next().unwrap().is_none());
}

#[tokio::test]
async fn a_later_malformed_approval_fails_closed() {
    let fixture = Fixture::new(1);
    write_issues(
        &fixture.repositories[0],
        &[vec![issue(15, "revision-1", &["factory:ready"])]],
    );
    fixture.approve(0, 15, 115, 215);
    let comments_path = fixture.repositories[0].join(".comments-15.json");
    let mut pages: serde_json::Value =
        serde_json::from_str(&fs::read_to_string(&comments_path).unwrap()).unwrap();
    pages.as_array_mut().unwrap().push(serde_json::json!([{
        "id": 116,
        "html_url": "https://example/comment/116",
        "user": {"id": 42, "login": "owainlewis"},
        "body": "<!-- factory-approval:v2 malformed -->",
        "created_at": "2026-01-01T00:00:02Z",
        "updated_at": "2026-01-01T00:00:02Z"
    }]));
    fs::write(comments_path, pages.to_string()).unwrap();

    let (report, mut ledger) = fixture.poll().await.unwrap();

    assert_eq!(report.tasks_created(), 0);
    assert!(ledger.claim_next().unwrap().is_none());
}

#[tokio::test]
async fn github_claim_record_prevents_replay_with_a_fresh_ledger() {
    let fixture = Fixture::new(1);
    write_issues(
        &fixture.repositories[0],
        &[vec![issue(16, "revision-1", &["factory:ready"])]],
    );
    fixture.approve(0, 16, 116, 216);
    let comments_path = fixture.repositories[0].join(".comments-16.json");
    let mut pages: serde_json::Value =
        serde_json::from_str(&fs::read_to_string(&comments_path).unwrap()).unwrap();
    pages.as_array_mut().unwrap().push(serde_json::json!([{
        "id": 316,
        "html_url": "https://example/comment/316",
        "user": {"id": 42, "login": "owainlewis"},
        "body": "<!-- factory-claim:v1 {\"version\":1,\"task_id\":99,\"approval_artifact_id\":116,\"label_event_id\":216} -->",
        "created_at": "2026-01-01T00:00:02Z",
        "updated_at": "2026-01-01T00:00:02Z"
    }]));
    fs::write(comments_path, pages.to_string()).unwrap();

    let (report, mut ledger) = fixture.poll().await.unwrap();

    assert_eq!(report.tasks_created(), 0);
    assert!(ledger.claim_next().unwrap().is_none());
}

#[tokio::test]
async fn pagination_and_comments_create_one_complete_durable_task() {
    let fixture = Fixture::new(1);
    write_issues(
        &fixture.repositories[0],
        &[
            vec![issue(1, "revision-1", &["bug"])],
            vec![issue(2, "revision-2", &["factory:ready", "enhancement"])],
        ],
    );
    fixture.approve(0, 2, 102, 202);

    let (report, mut ledger) = fixture.poll().await.unwrap();
    let task = ledger.claim_next().unwrap().unwrap();
    let payload = task.payload.as_deref().unwrap();
    let context: TicketContext = serde_json::from_str(payload).unwrap();

    assert_eq!(report.tasks_created(), 1);
    assert_eq!(report.repositories[0].issues_seen, 2);
    assert_eq!(context.number, 2);
    assert_eq!(context.title, "Ticket 2");
    assert_eq!(context.body, "Body 2");
    assert_eq!(context.approval.artifact_id, 102);
    assert!(!payload.contains("labels"));
    assert!(!payload.contains("comments"));
}

#[tokio::test]
async fn unchanged_and_changed_eligible_tickets_do_not_duplicate_until_relabelled() {
    let fixture = Fixture::new(1);
    let repository = &fixture.repositories[0];
    write_issues(
        repository,
        &[vec![issue(4, "revision-1", &["factory:ready"])]],
    );
    fixture.approve(0, 4, 104, 204);
    assert_eq!(fixture.poll().await.unwrap().0.tasks_created(), 1);
    assert_eq!(fixture.poll().await.unwrap().0.tasks_created(), 0);

    write_issues(
        repository,
        &[vec![issue(4, "revision-2", &["factory:ready"])]],
    );
    assert_eq!(fixture.poll().await.unwrap().0.tasks_created(), 0);
    write_issues(
        repository,
        &[vec![issue(4, "revision-3", &["enhancement"])]],
    );
    assert_eq!(fixture.poll().await.unwrap().0.tasks_created(), 0);
    write_issues(
        repository,
        &[vec![issue(4, "revision-4", &["factory:ready"])]],
    );
    assert_eq!(fixture.poll().await.unwrap().0.tasks_created(), 0);

    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    let task = ledger.claim_next().unwrap().unwrap();
    let run = ledger.start_run(task.id, "codex").unwrap();
    drop(ledger);
    write_issues(
        repository,
        &[vec![issue(4, "revision-5", &["enhancement"])]],
    );
    assert_eq!(fixture.poll().await.unwrap().0.tasks_created(), 0);
    write_issues(
        repository,
        &[vec![issue(4, "revision-6", &["factory:ready"])]],
    );
    assert_eq!(fixture.poll().await.unwrap().0.tasks_created(), 0);

    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    ledger
        .finish_run_and_task(run.id, RunOutcome::Failed, None, Some("expected"), None)
        .unwrap();
    drop(ledger);
    write_issues(
        repository,
        &[vec![issue(4, "revision-7", &["enhancement"])]],
    );
    assert_eq!(fixture.poll().await.unwrap().0.tasks_created(), 0);
    write_issues(
        repository,
        &[vec![issue(4, "revision-8", &["factory:ready"])]],
    );
    fixture.approve(0, 4, 105, 205);
    assert_eq!(fixture.poll().await.unwrap().0.tasks_created(), 1);
}

#[tokio::test]
async fn authentication_failure_is_actionable_and_creates_nothing() {
    let fixture = Fixture::new(1);
    fs::write(format!("{}.auth-fail", fixture.gh.display()), "").unwrap();

    let error = match fixture.poll().await {
        Ok(_) => panic!("authentication failure unexpectedly succeeded"),
        Err(error) => error,
    };

    assert!(format!("{error:#}").contains("run gh auth login"));
    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    assert!(ledger.claim_next().unwrap().is_none());
}

#[tokio::test]
async fn malformed_output_and_rate_limiting_are_isolated_from_healthy_repositories() {
    let fixture = Fixture::new(2);
    fs::write(fixture.repositories[0].join(".issues.json"), "not json").unwrap();
    write_issues(
        &fixture.repositories[1],
        &[vec![issue(6, "revision-1", &["factory:ready"])]],
    );
    fixture.approve(1, 6, 106, 206);

    let (report, _) = fixture.poll().await.unwrap();

    assert_eq!(report.failures(), 1);
    assert!(
        report.repositories[0]
            .error
            .as_deref()
            .unwrap()
            .contains("malformed paginated JSON")
    );
    assert_eq!(report.repositories[1].tasks_created, 1);

    fs::remove_file(fixture.repositories[0].join(".issues.json")).unwrap();
    fs::write(fixture.repositories[0].join(".api-fail"), "").unwrap();
    let (report, _) = fixture.poll().await.unwrap();
    assert_eq!(report.failures(), 1);
    assert!(
        report.repositories[0]
            .error
            .as_deref()
            .unwrap()
            .contains("rate limit")
    );
    assert_eq!(report.repositories[1].tasks_created, 0);
}

#[tokio::test]
async fn polling_repeats_at_the_configured_interval_and_stops_on_cancellation() {
    let fixture = Fixture::new(1);
    let config = fixture.config.clone();
    let catalog = WorkflowCatalog::load(&config).unwrap();
    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    let cancellation = CancellationToken::new();
    let cancel_after_two = cancellation.clone();
    let polls = AtomicUsize::new(0);
    tokio::time::timeout(
        Duration::from_secs(3),
        GitHubClient::new(&fixture.gh).poll_until_cancelled(
            &config,
            &catalog,
            &mut ledger,
            cancellation,
            |_| {
                if polls.fetch_add(1, Ordering::Relaxed) == 1 {
                    cancel_after_two.cancel();
                }
            },
        ),
    )
    .await
    .unwrap()
    .unwrap();

    assert!(polls.load(Ordering::Relaxed) >= 2);
}

#[tokio::test]
async fn cancellation_terminates_a_hung_gh_process() {
    let fixture = Fixture::new(1);
    fs::write(format!("{}.hang", fixture.gh.display()), "").unwrap();
    let pid_path = PathBuf::from(format!("{}.hang.pid", fixture.gh.display()));
    let config = fixture.config.clone();
    let catalog = WorkflowCatalog::load(&config).unwrap();
    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    let cancellation = CancellationToken::new();
    let cancel = cancellation.clone();
    let wait_for_pid = pid_path.clone();
    tokio::spawn(async move {
        for _ in 0..1000 {
            if wait_for_pid.exists() {
                cancel.cancel();
                return;
            }
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
        cancel.cancel();
    });

    GitHubClient::new(&fixture.gh)
        .with_command_timeout(Duration::from_secs(15))
        .poll_until_cancelled(&config, &catalog, &mut ledger, cancellation, |_| {})
        .await
        .unwrap();

    let pid = fs::read_to_string(pid_path).expect("fake gh never reported its process ID");
    let mut gone = false;
    for _ in 0..100 {
        if !Command::new("kill")
            .args(["-0", pid.trim()])
            .status()
            .unwrap()
            .success()
        {
            gone = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert!(gone, "hung gh process {pid:?} survived cancellation");
}

#[test]
fn factory_run_once_evaluates_due_schedules_without_launching_codex() {
    let fixture = Fixture::new(1);
    fs::write(
        fixture.repositories[0].join(".factory/workflows/scheduled.md"),
        "+++\nschedule = \"* * * * *\"\ntimezone = \"UTC\"\n+++\n\nReview the repository.\n",
    )
    .unwrap();
    let catalog = WorkflowCatalog::load(&fixture.config).unwrap();
    let workflow = catalog
        .entries
        .iter()
        .find(|entry| entry.id == "scheduled")
        .unwrap();
    let Trigger::Schedule {
        expression,
        timezone,
    } = workflow.trigger.as_ref().unwrap()
    else {
        panic!("scheduled workflow has the wrong trigger");
    };
    let fingerprint = scheduled_workflow_fingerprint(
        expression,
        *timezone,
        workflow.runtime.as_deref().unwrap(),
        workflow.timeout.unwrap(),
        workflow.prompt.as_deref().unwrap(),
    )
    .unwrap();
    let data = fixture._temp.path().join("schedule-data");
    let mut ledger = Ledger::open_in(&data).unwrap();
    ledger
        .register_daemon_owner("schedule-seed", std::process::id())
        .unwrap();
    let now = Utc::now().timestamp_millis();
    let due = now.div_euclid(60_000) * 60_000;
    ledger
        .initialize_schedule_cursor(
            "example/repo-0",
            "scheduled",
            &fingerprint,
            due,
            due - 1,
            "schedule-seed",
        )
        .unwrap();
    ledger.remove_daemon_owner("schedule-seed").unwrap();
    drop(ledger);
    let path = format!(
        "{}:{}",
        fixture._temp.path().display(),
        std::env::var("PATH").unwrap()
    );

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--once",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            data.to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("PATH", path)
        .assert()
        .success()
        .stdout(predicates::str::contains("scheduled_tasks_created=1"));

    let ledger = Ledger::open_in(&data).unwrap();
    assert_eq!(
        ledger
            .tasks()
            .unwrap()
            .iter()
            .filter(|task| task.kind == "scheduled")
            .count(),
        1
    );
    assert!(ledger.runs(None).unwrap().is_empty());
}

#[test]
fn repository_local_startup_blocks_active_legacy_work_without_mutating_it() {
    let fixture = Fixture::new(1);
    let legacy = fixture._temp.path().join("legacy");
    let mut ledger = Ledger::open_in(&legacy).unwrap();
    ledger
        .enqueue(
            &TaskIdentity::ticket(
                "example/repo-0",
                "implement-ready-ticket",
                "77",
                "legacy-revision",
            )
            .unwrap(),
        )
        .unwrap();
    drop(ledger);
    let database = legacy.join(factory::storage::DATABASE_NAME);
    let before = fs::read(&database).unwrap();

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--once",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            fixture._temp.path().join("new-data").to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("FACTORY_LEGACY_DATA_DIRECTORY", &legacy)
        .assert()
        .failure()
        .stderr(predicates::str::contains("stop the old daemon"))
        .stderr(predicates::str::contains("left unchanged"));

    assert_eq!(fs::read(database).unwrap(), before);

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--once",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            legacy.to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("FACTORY_LEGACY_DATA_DIRECTORY", &legacy)
        .assert()
        .failure()
        .stderr(predicates::str::contains(
            "cannot use the legacy data directory",
        ));

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--once",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            legacy.join("missing/..").to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("FACTORY_LEGACY_DATA_DIRECTORY", &legacy)
        .assert()
        .failure()
        .stderr(predicates::str::contains(
            "must not contain parent traversal",
        ));
}

#[test]
fn terminal_or_unrelated_legacy_work_does_not_block_repository_local_startup() {
    let fixture = Fixture::new(1);
    let legacy = fixture._temp.path().join("legacy");
    let mut ledger = Ledger::open_in(&legacy).unwrap();
    let terminal = ledger
        .enqueue(
            &TaskIdentity::ticket(
                "example/repo-0",
                "implement-ready-ticket",
                "78",
                "legacy-terminal",
            )
            .unwrap(),
        )
        .unwrap()
        .task;
    ledger.claim_next().unwrap().unwrap();
    let run = ledger.start_run(terminal.id, "codex").unwrap();
    ledger
        .finish_run_and_task(run.id, RunOutcome::Succeeded, Some("done"), None, None)
        .unwrap();
    ledger
        .enqueue(
            &TaskIdentity::ticket(
                "example/other",
                "implement-ready-ticket",
                "79",
                "legacy-other",
            )
            .unwrap(),
        )
        .unwrap();
    drop(ledger);
    let path = format!(
        "{}:{}",
        fixture._temp.path().display(),
        std::env::var("PATH").unwrap()
    );

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--once",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            fixture._temp.path().join("new-data").to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .env("FACTORY_LEGACY_DATA_DIRECTORY", &legacy)
        .env("PATH", path)
        .assert()
        .success();
}
