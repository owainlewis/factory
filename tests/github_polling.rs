#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use assert_cmd::Command as AssertCommand;
use factory::config::Config;
use factory::github::{GitHubClient, TicketContext};
use factory::storage::{Ledger, RunOutcome};
use factory::workflow::WorkflowCatalog;
use tokio_util::sync::CancellationToken;

struct Fixture {
    _temp: tempfile::TempDir,
    repositories: Vec<PathBuf>,
    config_path: PathBuf,
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
            repositories.push(repository);
        }
        let workspace = temp.path().join("worktrees");
        fs::create_dir(&workspace).unwrap();
        let config_path = temp.path().join("config.toml");
        let repository_values = repositories
            .iter()
            .map(|path| format!("\"{}\"", path.display()))
            .collect::<Vec<_>>()
            .join(", ");
        fs::write(
            &config_path,
            format!(
                "repositories = [{repository_values}]\npoll_every = \"20ms\"\ndefault_runtime = \"codex\"\ndefault_timeout = \"2h\"\nmaximum_timeout = \"8h\"\nmax_concurrent_runs = 2\nworkspace_root = \"{}\"\n",
                workspace.display()
            ),
        )
        .unwrap();
        let gh = temp.path().join("gh");
        fs::write(
            &gh,
            r#"#!/bin/sh
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
if [ "$1" = "api" ]; then
  if [ -f ".api-fail" ]; then echo "API rate limit exceeded" >&2; exit 1; fi
  endpoint="$4"
  case "$endpoint" in
    */comments*)
      number=$(printf '%s' "$endpoint" | sed -E 's#^.*/issues/([0-9]+)/comments.*#\1#')
      file=".comments-$number.json"
      if [ -f "$file" ]; then cat "$file"; else printf '[[]]'; fi
      ;;
    *) cat .issues.json ;;
  esac
  exit 0
fi
echo "unexpected fake gh arguments: $*" >&2
exit 64
"#,
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
            ledger_path,
            gh,
        }
    }

    async fn poll(&self) -> anyhow::Result<(factory::github::PollReport, Ledger)> {
        let config = Config::load(&self.config_path).unwrap();
        let catalog = WorkflowCatalog::load(&config).unwrap();
        let mut ledger = Ledger::open(&self.ledger_path).unwrap();
        let report = GitHubClient::new(&self.gh)
            .poll_once(&config, &catalog, &mut ledger)
            .await?;
        Ok((report, ledger))
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
        .env("PATH", path)
        .assert()
        .success()
        .stdout(predicates::str::contains("tasks_created=1"))
        .stderr(predicates::str::contains("Factory starting: mode=once"))
        .stderr(predicates::str::contains(
            "Factory loaded: repositories=1 workflows=1",
        ))
        .stderr(predicates::str::contains("Factory polling GitHub once..."));

    assert!(!codex_marker.exists());
    let mut ledger = Ledger::open_in(&data).unwrap();
    assert_eq!(
        ledger.claim_next().unwrap().unwrap().source_item.as_deref(),
        Some("9")
    );
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
        .env("PATH", path)
        .output()
        .unwrap();

    assert!(!output.status.success());
    let stderr = String::from_utf8(output.stderr).unwrap();
    let starting = stderr.find("Factory starting: mode=once").unwrap();
    let polling = stderr.find("Factory polling GitHub once...").unwrap();
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
async fn pagination_and_comments_create_one_complete_durable_task() {
    let fixture = Fixture::new(1);
    write_issues(
        &fixture.repositories[0],
        &[
            vec![issue(1, "revision-1", &["bug"])],
            vec![issue(2, "revision-2", &["factory:ready", "enhancement"])],
        ],
    );
    fs::write(
        fixture.repositories[0].join(".comments-2.json"),
        r#"[[{"id":10,"html_url":"https://example/comment/10","user":{"login":"alice"},"body":"First","created_at":"a","updated_at":"a"}],[{"id":11,"html_url":"https://example/comment/11","user":{"login":"bob"},"body":"Second","created_at":"b","updated_at":"c"}]]"#,
    )
    .unwrap();

    let (report, mut ledger) = fixture.poll().await.unwrap();
    let task = ledger.claim_next().unwrap().unwrap();
    let context: TicketContext = serde_json::from_str(task.payload.as_deref().unwrap()).unwrap();

    assert_eq!(report.tasks_created(), 1);
    assert_eq!(report.repositories[0].issues_seen, 2);
    assert_eq!(context.number, 2);
    assert_eq!(context.labels, vec!["factory:ready", "enhancement"]);
    assert_eq!(context.comments.len(), 2);
    assert_eq!(context.comments[1].author, "bob");
}

#[tokio::test]
async fn unchanged_and_changed_eligible_tickets_do_not_duplicate_until_relabelled() {
    let fixture = Fixture::new(1);
    let repository = &fixture.repositories[0];
    write_issues(
        repository,
        &[vec![issue(4, "revision-1", &["factory:ready"])]],
    );
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
    let config = Config::load(&fixture.config_path).unwrap();
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
    let config = Config::load(&fixture.config_path).unwrap();
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
