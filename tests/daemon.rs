#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Arc;
use std::time::Duration;

use assert_cmd::Command as AssertCommand;
use factory::config::Config;
use factory::daemon::FactoryDaemon;
use factory::github::GitHubClient;
use factory::runtime::CodexRuntime;
use factory::storage::{Ledger, TaskIdentity, TaskState};
use factory::workflow::WorkflowCatalog;
use tokio_util::sync::CancellationToken;

struct Fixture {
    _temp: tempfile::TempDir,
    config: Config,
    catalog: WorkflowCatalog,
    ledger_path: PathBuf,
    gh: PathBuf,
    codex: PathBuf,
    runtime_dir: PathBuf,
}

impl Fixture {
    fn new(issue_pages: &[Vec<String>], global_limit: usize, repository_limit: usize) -> Self {
        let temp = tempfile::tempdir().unwrap();
        let runtime_dir = temp.path().join("runtime");
        fs::create_dir(&runtime_dir).unwrap();
        let mut repositories = Vec::new();
        for (index, issues) in issue_pages.iter().enumerate() {
            let repository = temp.path().join(format!("repo-{index}"));
            fs::create_dir_all(repository.join(".factory/workflows")).unwrap();
            fs::write(
                repository.join(".factory/workflows/implement-ready-ticket.md"),
                "+++\nlabel = \"factory:ready\"\nruntime = \"codex\"\ntimeout = \"10s\"\n+++\n\nCUSTOM WORKFLOW: deliver a green draft PR and never merge it.\n",
            )
            .unwrap();
            fs::write(
                repository.join(".gh-name"),
                format!("example/repo-{index}\n"),
            )
            .unwrap();
            fs::write(
                repository.join(".issues.json"),
                format!("[[{}]]", issues.join(",")),
            )
            .unwrap();
            for issue in issues {
                let number = issue_number(issue);
                fs::write(
                    repository.join(format!(".comments-{number}.json")),
                    format!(
                        r#"[[{{"id":{number},"html_url":"https://example/comments/{number}","user":{{"login":"reviewer"}},"body":"Discussion {number}","created_at":"a","updated_at":"b"}}]]"#
                    ),
                )
                .unwrap();
            }
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
                "repositories = [{repository_values}]\npoll_every = \"20ms\"\ndefault_runtime = \"codex\"\ndefault_timeout = \"2h\"\nmaximum_timeout = \"8h\"\nmax_concurrent_runs = {global_limit}\nmax_concurrent_runs_per_repository = {repository_limit}\nworkspace_root = \"{}\"\n",
                workspace.display()
            ),
        )
        .unwrap();
        let config = Config::load(&config_path).unwrap();
        let catalog = WorkflowCatalog::load(&config).unwrap();
        let gh = temp.path().join("gh");
        write_executable(
            &gh,
            r#"#!/bin/sh
if [ "$1" = "--version" ]; then echo "gh version 2.80.0"; exit 0; fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  if [ -f "$0.auth-fail" ]; then echo "authentication expired" >&2; exit 1; fi
  echo "logged in"; exit 0
fi
if [ "$1" = "repo" ]; then cat .gh-name; exit 0; fi
if [ "$1" = "api" ]; then
  endpoint="$4"
  case "$endpoint" in
    */comments*)
      number=$(printf '%s' "$endpoint" | sed -E 's#^.*/issues/([0-9]+)/comments.*#\1#')
      cat ".comments-$number.json"
      ;;
    *) cat .issues.json ;;
  esac
  exit 0
fi
exit 64
"#,
        );
        let codex = temp.path().join("codex");
        write_executable(
            &codex,
            &format!(
                r#"#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 1.2.3"; exit 0; fi
if [ "$1" = "login" ] && [ "$2" = "status" ]; then echo "Logged in using ChatGPT"; exit 0; fi
output=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then shift; output="$1"; fi
  shift
done
slot=1
while ! mkdir "{root}/slot-$slot" 2>/dev/null; do slot=$((slot + 1)); done
echo $$ > "{root}/slot-$slot/pid"
sleep 1000 &
echo $! > "{root}/slot-$slot/child-pid"
cat > "{root}/slot-$slot/prompt"
touch "{root}/slot-$slot/started"
while [ ! -f "{root}/gate" ]; do sleep 0.02; done
echo "{{\"type\":\"thread.started\",\"thread_id\":\"thread-$slot\"}}"
printf 'Draft PR: https://example.test/pr/%s' "$slot" > "$output"
exit 0
"#,
                root = runtime_dir.display()
            ),
        );
        Self {
            ledger_path: temp.path().join("data/factory.sqlite3"),
            _temp: temp,
            config,
            catalog,
            gh,
            codex,
            runtime_dir,
        }
    }

    fn daemon(&self) -> FactoryDaemon {
        FactoryDaemon::with_clients(
            self.config.clone(),
            self.catalog.clone(),
            &self.ledger_path,
            GitHubClient::new(&self.gh),
            CodexRuntime::new(&self.codex).with_activity_streaming(false),
        )
    }

    fn open_gate(&self) {
        fs::write(self.runtime_dir.join("gate"), "go").unwrap();
    }

    fn started_slots(&self) -> Vec<PathBuf> {
        let Ok(entries) = fs::read_dir(&self.runtime_dir) else {
            return Vec::new();
        };
        let mut slots = entries
            .filter_map(Result::ok)
            .map(|entry| entry.path())
            .filter(|path| path.is_dir() && path.join("started").exists())
            .collect::<Vec<_>>();
        slots.sort();
        slots
    }
}

fn issue(number: u64) -> String {
    format!(
        r#"{{"number":{number},"html_url":"https://github.com/example/repo/issues/{number}","title":"Ticket {number}","body":"Body {number}","labels":[{{"name":"factory:ready"}}],"updated_at":"revision-{number}"}}"#
    )
}

fn issue_number(issue: &str) -> u64 {
    serde_json::from_str::<serde_json::Value>(issue).unwrap()["number"]
        .as_u64()
        .unwrap()
}

fn write_executable(path: &Path, contents: &str) {
    fs::write(path, contents).unwrap();
    let mut permissions = fs::metadata(path).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(path, permissions).unwrap();
}

async fn wait_for<F>(mut condition: F)
where
    F: FnMut() -> bool,
{
    tokio::time::timeout(Duration::from_secs(8), async {
        loop {
            if condition() {
                return;
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
    })
    .await
    .unwrap();
}

#[tokio::test]
async fn discovers_claims_and_records_a_complete_codex_run() {
    let fixture = Fixture::new(&[vec![issue(6)]], 2, 1);
    fixture.open_gate();
    let cancellation = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let cancellation = cancellation.clone();
        tokio::spawn(async move { daemon.run(cancellation).await })
    };
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks.len() == 1 && tasks[0].state == TaskState::Succeeded)
    })
    .await;
    cancellation.cancel();
    running.await.unwrap().unwrap();

    let ledger = Ledger::open(&fixture.ledger_path).unwrap();
    let task = &ledger.tasks().unwrap()[0];
    let runs = ledger.runs_for_task(task.id).unwrap();
    assert_eq!(runs.len(), 1);
    assert_eq!(runs[0].outcome, "succeeded");
    assert_eq!(runs[0].session_id.as_deref(), Some("thread-1"));
    assert!(runs[0].result.as_deref().unwrap().contains("Draft PR"));
    let prompt = fs::read_to_string(fixture.started_slots()[0].join("prompt")).unwrap();
    for expected in [
        "Factory-created software pull requests must remain for human merge",
        "Repository: example/repo-0",
        "Ticket 6",
        "Discussion 6",
        "CUSTOM WORKFLOW",
        "Never merge",
    ] {
        assert!(prompt.contains(expected), "missing {expected:?} in prompt");
    }
}

#[tokio::test]
async fn enforces_global_and_per_repository_concurrency() {
    let fixture = Fixture::new(&[vec![issue(1), issue(2)], vec![issue(3)]], 2, 1);
    let cancellation = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let cancellation = cancellation.clone();
        tokio::spawn(async move { daemon.run(cancellation).await })
    };
    wait_for(|| fixture.started_slots().len() == 2).await;
    let first_prompts = fixture
        .started_slots()
        .iter()
        .map(|slot| fs::read_to_string(slot.join("prompt")).unwrap())
        .collect::<Vec<_>>();
    assert!(
        first_prompts
            .iter()
            .any(|prompt| prompt.contains("example/repo-0"))
    );
    assert!(
        first_prompts
            .iter()
            .any(|prompt| prompt.contains("example/repo-1"))
    );
    assert_eq!(fixture.started_slots().len(), 2);

    fixture.open_gate();
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| {
                tasks.len() == 3 && tasks.iter().all(|task| task.state == TaskState::Succeeded)
            })
    })
    .await;
    cancellation.cancel();
    running.await.unwrap().unwrap();
    assert_eq!(fixture.started_slots().len(), 3);
}

#[tokio::test]
async fn two_daemons_cannot_execute_the_same_task() {
    let fixture = Fixture::new(&[vec![issue(8)]], 1, 1);
    let cancellation = CancellationToken::new();
    let first = Arc::new(fixture.daemon());
    let second = Arc::new(fixture.daemon());
    let first_run = {
        let daemon = Arc::clone(&first);
        let token = cancellation.clone();
        tokio::spawn(async move { daemon.run(token).await })
    };
    let second_run = {
        let daemon = Arc::clone(&second);
        let token = cancellation.clone();
        tokio::spawn(async move { daemon.run(token).await })
    };
    wait_for(|| fixture.started_slots().len() == 1).await;
    tokio::time::sleep(Duration::from_millis(250)).await;
    assert_eq!(fixture.started_slots().len(), 1);
    cancellation.cancel();
    first_run.await.unwrap().unwrap();
    second_run.await.unwrap().unwrap();
    let tasks = Ledger::open(&fixture.ledger_path).unwrap().tasks().unwrap();
    assert_eq!(tasks.len(), 1);
    assert_eq!(tasks[0].state, TaskState::Cancelled);
}

#[tokio::test]
async fn shutdown_cancels_the_active_codex_process_and_records_it() {
    let fixture = Fixture::new(&[vec![issue(10)]], 1, 1);
    let cancellation = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let token = cancellation.clone();
        tokio::spawn(async move { daemon.run(token).await })
    };
    wait_for(|| fixture.started_slots().len() == 1).await;
    let pid = fs::read_to_string(fixture.started_slots()[0].join("pid")).unwrap();
    let child_pid = fs::read_to_string(fixture.started_slots()[0].join("child-pid")).unwrap();
    cancellation.cancel();
    running.await.unwrap().unwrap();

    let tasks = Ledger::open(&fixture.ledger_path).unwrap().tasks().unwrap();
    assert_eq!(tasks[0].state, TaskState::Cancelled);
    assert!(
        !Command::new("kill")
            .args(["-0", pid.trim()])
            .status()
            .unwrap()
            .success()
    );
    assert!(
        !Command::new("kill")
            .args(["-0", child_pid.trim()])
            .status()
            .unwrap()
            .success()
    );
}

#[tokio::test]
async fn cli_cancellation_stops_the_owned_process_tree_and_records_cancelled() {
    let fixture = Fixture::new(&[vec![issue(12)]], 1, 1);
    let shutdown = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let shutdown = shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };
    wait_for(|| fixture.started_slots().len() == 1).await;
    let pid = fs::read_to_string(fixture.started_slots()[0].join("pid")).unwrap();
    let child_pid = fs::read_to_string(fixture.started_slots()[0].join("child-pid")).unwrap();
    let run_id = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap()[0]
        .id;

    let run_id_string = run_id.to_string();
    for args in [
        vec!["tasks", "--json"],
        vec!["runs", "--json"],
        vec!["inspect", run_id_string.as_str(), "--json"],
    ] {
        let output = AssertCommand::cargo_bin("factory")
            .unwrap()
            .args(args)
            .args([
                "--data-directory",
                fixture.ledger_path.parent().unwrap().to_str().unwrap(),
            ])
            .output()
            .unwrap();
        assert!(output.status.success());
        let value: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
        assert!(value.is_array() || value.is_object());
        assert!(
            String::from_utf8(output.stdout)
                .unwrap()
                .contains("running")
        );
    }

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "cancel",
            &run_id.to_string(),
            "--data-directory",
            fixture.ledger_path.parent().unwrap().to_str().unwrap(),
            "--json",
        ])
        .assert()
        .success();

    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks[0].state == TaskState::Cancelled)
    })
    .await;
    assert!(
        !Command::new("kill")
            .args(["-0", pid.trim()])
            .status()
            .unwrap()
            .success()
    );
    assert!(
        !Command::new("kill")
            .args(["-0", child_pid.trim()])
            .status()
            .unwrap()
            .success()
    );
    shutdown.cancel();
    running.await.unwrap().unwrap();
}

#[tokio::test]
async fn losing_the_durable_owner_lease_is_a_daemon_error() {
    let fixture = Fixture::new(&[vec![issue(13)]], 1, 1);
    let cancellation = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let cancellation = cancellation.clone();
        tokio::spawn(async move { daemon.run(cancellation).await })
    };
    wait_for(|| fixture.started_slots().len() == 1).await;
    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    let owner_id = ledger.runs(None).unwrap()[0].owner_id.clone().unwrap();
    ledger.remove_daemon_owner(&owner_id).unwrap();
    drop(ledger);

    let error = running.await.unwrap().unwrap_err();

    assert!(format!("{error:#}").contains("is not registered"));
    let ledger = Ledger::open(&fixture.ledger_path).unwrap();
    assert_eq!(ledger.tasks().unwrap()[0].state, TaskState::Cancelled);
}

#[tokio::test]
async fn polling_error_cancels_and_drains_an_active_run() {
    let fixture = Fixture::new(&[vec![issue(11)]], 1, 1);
    let cancellation = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let token = cancellation.clone();
        tokio::spawn(async move { daemon.run(token).await })
    };
    wait_for(|| fixture.started_slots().len() == 1).await;
    let pid = fs::read_to_string(fixture.started_slots()[0].join("pid")).unwrap();
    fs::write(format!("{}.auth-fail", fixture.gh.display()), "fail").unwrap();

    let error = running.await.unwrap().unwrap_err();

    assert!(format!("{error:#}").contains("GitHub polling failed"));
    let ledger = Ledger::open(&fixture.ledger_path).unwrap();
    let task = &ledger.tasks().unwrap()[0];
    assert_eq!(task.state, TaskState::Cancelled);
    assert_eq!(
        ledger.runs_for_task(task.id).unwrap()[0].outcome,
        "cancelled"
    );
    assert!(
        !Command::new("kill")
            .args(["-0", pid.trim()])
            .status()
            .unwrap()
            .success()
    );
}

#[tokio::test]
async fn scheduled_tasks_are_not_claimed_by_ticket_workers() {
    let fixture = Fixture::new(&[vec![]], 1, 1);
    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    ledger
        .enqueue(
            &TaskIdentity::scheduled(
                "example/repo-0",
                "implement-ready-ticket",
                "2026-07-18T12:00:00Z",
            )
            .unwrap(),
        )
        .unwrap();
    drop(ledger);
    let cancellation = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let token = cancellation.clone();
        tokio::spawn(async move { daemon.run(token).await })
    };
    tokio::time::sleep(Duration::from_millis(200)).await;
    cancellation.cancel();
    running.await.unwrap().unwrap();

    let tasks = Ledger::open(&fixture.ledger_path).unwrap().tasks().unwrap();
    assert_eq!(tasks.len(), 1);
    assert_eq!(tasks[0].state, TaskState::Queued);
    assert!(fixture.started_slots().is_empty());
}
