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
use factory::workflow::{Trigger, WorkflowCatalog, scheduled_workflow_fingerprint};
use rusqlite::Connection;
use tokio_util::sync::CancellationToken;

struct Fixture {
    _temp: tempfile::TempDir,
    config: Config,
    catalog: WorkflowCatalog,
    config_path: PathBuf,
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
  if ! mkdir "$0.api-active" 2>/dev/null; then touch "$0.api-concurrent"; fi
  touch "$0.api-started"
  if [ -f "$0.api-block" ]; then
    while [ ! -f "$0.api-release" ]; do sleep 0.02; done
  fi
  endpoint="$4"
  case "$endpoint" in
    */comments*)
      number=$(printf '%s' "$endpoint" | sed -E 's#^.*/issues/([0-9]+)/comments.*#\1#')
      cat ".comments-$number.json"
      ;;
    *) cat .issues.json ;;
  esac
  rmdir "$0.api-active" 2>/dev/null || true
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
mode=initial
if [ "$1" = "exec" ] && [ "$2" = "resume" ]; then mode=resume; fi
output=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then shift; output="$1"; fi
  shift
done
slot=1
while ! mkdir "{root}/slot-$slot" 2>/dev/null; do slot=$((slot + 1)); done
echo $$ > "{root}/slot-$slot/pid"
echo "$mode" > "{root}/slot-$slot/mode"
sleep 1000 &
echo $! > "{root}/slot-$slot/child-pid"
cat > "{root}/slot-$slot/prompt"
touch "{root}/slot-$slot/started"
if [ "$mode" = "initial" ] && [ "$slot" != "1" ] && [ -f "{root}/fail-fallback-before-thread" ]; then
  exit 66
fi
echo "{{\"type\":\"thread.started\",\"thread_id\":\"thread-$slot\"}}"
echo "{{\"type\":\"item.completed\",\"item\":{{\"text\":\"active SECRET=hunter2\"}}}}"
if [ -f "{root}/emit-pr-first" ] && [ "$slot" = "1" ]; then
  echo "{{\"type\":\"item.completed\",\"item\":{{\"text\":\"https://github.com/example/repo-0/pull/77\"}}}}"
fi
if [ -f "{root}/malformed-once" ]; then
  rm "{root}/malformed-once"
  echo "not-json"
  exit 0
fi
if [ "$mode" = "resume" ] && [ -f "{root}/fail-resume" ]; then
  if [ -f "{root}/pause-resume-before-fail" ]; then
    while [ ! -f "{root}/release-resume" ]; do sleep 0.02; done
  fi
  echo "stored session is missing" >&2
  exit 44
fi
if [ -f "{root}/fail-all" ]; then
  echo "agent process exited unexpectedly" >&2
  exit 55
fi
while [ ! -f "{root}/gate" ]; do sleep 0.02; done
printf 'Draft PR: https://example.test/pr/%s' "$slot" > "$output"
exit 0
"#,
                root = runtime_dir.display()
            ),
        );
        Self {
            config_path,
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

    fn add_scheduled_workflow(&mut self) {
        fs::write(
            self.config.repositories[0].join(".factory/workflows/scheduled-maintenance.md"),
            "+++\nschedule = \"0 0 1 1 *\"\ntimezone = \"UTC\"\nruntime = \"codex\"\ntimeout = \"10s\"\n+++\n\nSCHEDULED MAINTENANCE WORKFLOW\n",
        )
        .unwrap();
        self.catalog = WorkflowCatalog::load(&self.config).unwrap();
    }

    fn scheduled_fingerprint(&self) -> String {
        let workflow = self
            .catalog
            .entries
            .iter()
            .find(|entry| entry.id == "scheduled-maintenance")
            .unwrap();
        let Trigger::Schedule {
            expression,
            timezone,
        } = workflow.trigger.as_ref().unwrap()
        else {
            panic!("scheduled maintenance workflow has the wrong trigger");
        };
        scheduled_workflow_fingerprint(
            expression,
            *timezone,
            workflow.runtime.as_deref().unwrap(),
            workflow.timeout.unwrap(),
            workflow.prompt.as_deref().unwrap(),
        )
        .unwrap()
    }

    fn scheduled_payload(&self, scheduled_at: &str) -> String {
        serde_json::json!({
            "scheduled_at": scheduled_at,
            "schedule_fingerprint": self.scheduled_fingerprint(),
        })
        .to_string()
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

fn process_is_alive(process_id: u32) -> bool {
    matches!(
        nix::sys::signal::kill(
            nix::unistd::Pid::from_raw(i32::try_from(process_id).unwrap()),
            None,
        ),
        Ok(()) | Err(nix::errno::Errno::EPERM)
    )
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
async fn workflow_deadline_is_terminal_and_does_not_queue_recovery() {
    let mut fixture = Fixture::new(&[vec![issue(28)]], 1, 1);
    fs::write(
        fixture.config.repositories[0].join(".factory/workflows/implement-ready-ticket.md"),
        "+++\nlabel = \"factory:ready\"\nruntime = \"codex\"\ntimeout = \"100ms\"\n+++\n\nTIME-BOUNDED WORKFLOW\n",
    )
    .unwrap();
    fixture.catalog = WorkflowCatalog::load(&fixture.config).unwrap();
    let shutdown = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let shutdown = shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };

    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| {
                tasks
                    .first()
                    .is_some_and(|task| task.state == TaskState::Failed)
            })
    })
    .await;
    tokio::time::sleep(Duration::from_millis(250)).await;
    shutdown.cancel();
    running.await.unwrap().unwrap();

    let ledger = Ledger::open(&fixture.ledger_path).unwrap();
    let runs = ledger.runs(None).unwrap();
    assert_eq!(runs.len(), 1);
    assert_eq!(runs[0].outcome, "failed");
    assert_eq!(runs[0].error.as_deref(), Some("Codex execution timed out"));
    assert_eq!(fixture.started_slots().len(), 1);
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
async fn restart_recovers_one_orphan_by_resuming_its_live_observed_session() {
    let fixture = Fixture::new(&[vec![issue(21)]], 1, 1);
    let first_daemon = Arc::new(fixture.daemon());
    let first_shutdown = CancellationToken::new();
    let first = {
        let daemon = Arc::clone(&first_daemon);
        let shutdown = first_shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };
    wait_for(|| fixture.started_slots().len() == 1).await;
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.runs(None))
            .is_ok_and(|runs| {
                runs.len() == 1
                    && runs[0].process_id.is_some()
                    && runs[0].session_id.as_deref() == Some("thread-1")
                    && runs[0].last_activity_at >= runs[0].started_at
                    && runs[0]
                        .activity
                        .as_deref()
                        .is_some_and(|activity| !activity.contains("hunter2"))
            })
    })
    .await;
    let owner_id = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap()[0]
        .owner_id
        .clone()
        .unwrap();
    first.abort();
    let _ = first.await;
    Ledger::open(&fixture.ledger_path)
        .unwrap()
        .remove_daemon_owner(&owner_id)
        .unwrap();

    let second_daemon = Arc::new(fixture.daemon());
    let second_shutdown = CancellationToken::new();
    let second = {
        let daemon = Arc::clone(&second_daemon);
        let shutdown = second_shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };
    wait_for(|| fixture.started_slots().len() == 2).await;
    let slots = fixture.started_slots();
    assert_eq!(
        fs::read_to_string(slots[1].join("mode")).unwrap().trim(),
        "resume"
    );
    let prompt = fs::read_to_string(slots[1].join("prompt")).unwrap();
    assert!(prompt.contains("Interrupted-run recovery"));
    assert!(prompt.contains("Inspect current repository, ticket, GitHub"));
    assert!(prompt.contains("thread-1"));
    fixture.open_gate();
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks[0].state == TaskState::Succeeded)
    })
    .await;
    let runs = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap();
    assert_eq!(runs.len(), 2);
    assert_eq!(runs[0].outcome, "failed");
    assert_eq!(runs[1].recovery_of, Some(runs[0].id));
    assert_eq!(runs[1].recovery_attempt, 1);
    second_shutdown.cancel();
    second.await.unwrap().unwrap();
}

#[tokio::test]
async fn subprocess_restart_kills_the_surviving_anchored_process_tree() {
    let fixture = Fixture::new(&[vec![issue(25)]], 1, 1);
    let search_path = std::env::join_paths(
        std::iter::once(fixture.gh.parent().unwrap().to_path_buf()).chain(
            std::env::var_os("PATH")
                .as_deref()
                .map(std::env::split_paths)
                .into_iter()
                .flatten(),
        ),
    )
    .unwrap();
    let mut first = Command::new(env!("CARGO_BIN_EXE_factory"));
    first
        .args([
            "run",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            fixture.ledger_path.parent().unwrap().to_str().unwrap(),
        ])
        .env("PATH", &search_path);
    let mut first = first.spawn().unwrap();
    wait_for(|| fixture.started_slots().len() == 1).await;
    let first_run = tokio::time::timeout(Duration::from_secs(8), async {
        loop {
            if let Ok(runs) =
                Ledger::open(&fixture.ledger_path).and_then(|ledger| ledger.runs(None))
                && let Some(run) = runs.first()
                && run.process_id.is_some()
                && run.process_identity.is_some()
                && run.session_id.is_some()
            {
                break run.clone();
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
    })
    .await
    .unwrap();
    let slot = &fixture.started_slots()[0];
    let codex_pid: u32 = fs::read_to_string(slot.join("pid"))
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    let child_pid: u32 = fs::read_to_string(slot.join("child-pid"))
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    let anchor_pid = first_run.process_id.unwrap();

    nix::sys::signal::kill(
        nix::unistd::Pid::from_raw(i32::try_from(first.id()).unwrap()),
        nix::sys::signal::Signal::SIGKILL,
    )
    .unwrap();
    first.wait().unwrap();
    assert!(process_is_alive(anchor_pid));
    assert!(process_is_alive(codex_pid));
    assert!(process_is_alive(child_pid));

    let mut second = Command::new(env!("CARGO_BIN_EXE_factory"));
    second
        .args([
            "run",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            fixture.ledger_path.parent().unwrap().to_str().unwrap(),
        ])
        .env("PATH", &search_path);
    let mut second = second.spawn().unwrap();
    wait_for(|| fixture.started_slots().len() == 2).await;
    wait_for(|| {
        !process_is_alive(anchor_pid)
            && !process_is_alive(codex_pid)
            && !process_is_alive(child_pid)
    })
    .await;
    assert_eq!(
        fs::read_to_string(fixture.started_slots()[1].join("mode"))
            .unwrap()
            .trim(),
        "resume"
    );

    fixture.open_gate();
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks[0].state == TaskState::Succeeded)
    })
    .await;
    nix::sys::signal::kill(
        nix::unistd::Pid::from_raw(i32::try_from(second.id()).unwrap()),
        nix::sys::signal::Signal::SIGINT,
    )
    .unwrap();
    assert!(second.wait().unwrap().success());
}

#[tokio::test]
async fn live_second_daemon_recovers_when_the_first_owner_disappears_later() {
    let fixture = Fixture::new(&[vec![issue(24)]], 1, 1);
    let first_daemon = Arc::new(fixture.daemon());
    let first = {
        let daemon = Arc::clone(&first_daemon);
        tokio::spawn(async move { daemon.run(CancellationToken::new()).await })
    };
    wait_for(|| fixture.started_slots().len() == 1).await;
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.runs(None))
            .is_ok_and(|runs| runs[0].session_id.as_deref() == Some("thread-1"))
    })
    .await;
    let first_owner = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap()[0]
        .owner_id
        .clone()
        .unwrap();

    let second_shutdown = CancellationToken::new();
    let second_daemon = Arc::new(fixture.daemon());
    let second = {
        let daemon = Arc::clone(&second_daemon);
        let shutdown = second_shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert_eq!(fixture.started_slots().len(), 1);

    first.abort();
    let _ = first.await;
    Ledger::open(&fixture.ledger_path)
        .unwrap()
        .remove_daemon_owner(&first_owner)
        .unwrap();
    wait_for(|| fixture.started_slots().len() == 2).await;
    let slots = fixture.started_slots();
    assert_eq!(
        fs::read_to_string(slots[1].join("mode")).unwrap().trim(),
        "resume"
    );
    fixture.open_gate();
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks[0].state == TaskState::Succeeded)
    })
    .await;
    second_shutdown.cancel();
    second.await.unwrap().unwrap();
}

#[tokio::test]
async fn missing_stored_session_gets_one_fresh_recovery_fallback() {
    let fixture = Fixture::new(&[vec![issue(22)]], 1, 1);
    let first_daemon = Arc::new(fixture.daemon());
    let first = {
        let daemon = Arc::clone(&first_daemon);
        tokio::spawn(async move { daemon.run(CancellationToken::new()).await })
    };
    wait_for(|| fixture.started_slots().len() == 1).await;
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.runs(None))
            .is_ok_and(|runs| runs[0].session_id.as_deref() == Some("thread-1"))
    })
    .await;
    let owner_id = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap()[0]
        .owner_id
        .clone()
        .unwrap();
    first.abort();
    let _ = first.await;
    Ledger::open(&fixture.ledger_path)
        .unwrap()
        .remove_daemon_owner(&owner_id)
        .unwrap();
    fs::write(fixture.runtime_dir.join("fail-resume"), "yes").unwrap();

    let shutdown = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let restarted = {
        let daemon = Arc::clone(&daemon);
        let shutdown = shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };
    wait_for(|| fixture.started_slots().len() == 3).await;
    let slots = fixture.started_slots();
    assert_eq!(
        fs::read_to_string(slots[1].join("mode")).unwrap().trim(),
        "resume"
    );
    assert_eq!(
        fs::read_to_string(slots[2].join("mode")).unwrap().trim(),
        "initial"
    );
    let fallback_prompt = fs::read_to_string(slots[2].join("prompt")).unwrap();
    assert!(fallback_prompt.contains("Session fallback"));
    assert!(fallback_prompt.contains("Do not replay assumed steps"));
    fixture.open_gate();
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks[0].state == TaskState::Succeeded)
    })
    .await;
    let runs = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap();
    assert_eq!(
        runs.len(),
        2,
        "resume fallback stays within one durable recovery attempt"
    );
    let fallback_pid: u32 = fs::read_to_string(slots[2].join("pid"))
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    assert_ne!(runs[1].process_id, Some(fallback_pid));
    assert!(runs[1].process_identity.is_some());
    assert_eq!(runs[1].session_id.as_deref(), Some("thread-3"));
    shutdown.cancel();
    restarted.await.unwrap().unwrap();
}

#[tokio::test]
async fn repeated_agent_exit_uses_two_recoveries_then_leaves_failed_task() {
    let fixture = Fixture::new(&[vec![issue(23)]], 1, 1);
    fs::write(fixture.runtime_dir.join("fail-all"), "yes").unwrap();
    fs::write(fixture.runtime_dir.join("emit-pr-first"), "yes").unwrap();
    let shutdown = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let shutdown = shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };

    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| {
                tasks
                    .first()
                    .is_some_and(|task| task.state == TaskState::Failed)
            })
    })
    .await;
    let runs = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap();
    assert_eq!(runs.len(), 3);
    assert_eq!(
        runs.iter()
            .map(|run| run.recovery_attempt)
            .collect::<Vec<_>>(),
        [0, 1, 2]
    );
    assert!(runs.iter().all(|run| run.outcome == "failed"));
    assert_eq!(fixture.started_slots().len(), 5);
    let final_recovery_prompt =
        fs::read_to_string(fixture.started_slots()[3].join("prompt")).unwrap();
    assert!(final_recovery_prompt.contains("https://github.com/example/repo-0/pull/77"));
    shutdown.cancel();
    running.await.unwrap().unwrap();
}

#[tokio::test]
async fn observed_session_survives_later_malformed_activity_and_is_resumed() {
    let fixture = Fixture::new(&[vec![issue(25)]], 1, 1);
    fs::write(fixture.runtime_dir.join("malformed-once"), "yes").unwrap();
    let shutdown = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let shutdown = shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };

    wait_for(|| fixture.started_slots().len() == 2).await;
    let slots = fixture.started_slots();
    assert_eq!(
        fs::read_to_string(slots[1].join("mode")).unwrap().trim(),
        "resume"
    );
    let runs = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap();
    assert_eq!(runs[0].session_id.as_deref(), Some("thread-1"));
    fixture.open_gate();
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks[0].state == TaskState::Succeeded)
    })
    .await;
    shutdown.cancel();
    running.await.unwrap().unwrap();
}

#[tokio::test]
async fn failed_fallback_before_thread_cannot_restore_stale_resume_observation() {
    let fixture = Fixture::new(&[vec![issue(26)]], 1, 1);
    fs::write(fixture.runtime_dir.join("malformed-once"), "yes").unwrap();
    fs::write(fixture.runtime_dir.join("fail-resume"), "yes").unwrap();
    fs::write(
        fixture.runtime_dir.join("fail-fallback-before-thread"),
        "yes",
    )
    .unwrap();
    let shutdown = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let shutdown = shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };

    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| {
                tasks
                    .first()
                    .is_some_and(|task| task.state == TaskState::Failed)
            })
    })
    .await;
    let runs = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap();
    assert_eq!(runs.len(), 3);
    assert_eq!(fixture.started_slots().len(), 4);
    for recovery in &runs[1..] {
        assert_eq!(recovery.session_id, None);
        assert_eq!(recovery.activity, None);
    }
    shutdown.cancel();
    running.await.unwrap().unwrap();
}

#[tokio::test]
async fn failed_fallback_reset_records_an_outcome_instead_of_stranding_the_run() {
    let fixture = Fixture::new(&[vec![issue(27)]], 1, 1);
    fs::write(fixture.runtime_dir.join("malformed-once"), "yes").unwrap();
    fs::write(fixture.runtime_dir.join("fail-resume"), "yes").unwrap();
    fs::write(fixture.runtime_dir.join("pause-resume-before-fail"), "yes").unwrap();
    let shutdown = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let shutdown = shutdown.clone();
        tokio::spawn(async move { daemon.run(shutdown).await })
    };

    wait_for(|| fixture.started_slots().len() == 2).await;
    rusqlite::Connection::open(&fixture.ledger_path)
        .unwrap()
        .execute_batch(
            "CREATE TRIGGER reject_fallback_reset
             BEFORE UPDATE OF process_id ON runs
             WHEN OLD.outcome = 'running'
              AND OLD.process_id IS NOT NULL
              AND NEW.process_id IS NULL
             BEGIN
               SELECT RAISE(ABORT, 'injected fallback reset failure');
             END;",
        )
        .unwrap();
    fs::write(fixture.runtime_dir.join("release-resume"), "go").unwrap();

    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks[0].state == TaskState::Failed)
    })
    .await;
    shutdown.cancel();
    running.await.unwrap().unwrap();

    let runs = Ledger::open(&fixture.ledger_path)
        .unwrap()
        .runs(None)
        .unwrap();
    assert_eq!(runs.len(), 3);
    assert!(runs.iter().all(|run| run.outcome != "running"));
    assert!(runs[1..].iter().all(|run| {
        run.error
            .as_deref()
            .is_some_and(|error| error.contains("failed to prepare a fresh recovery fallback"))
    }));
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
async fn scheduled_tasks_use_the_same_worker_and_run_history() {
    let mut fixture = Fixture::new(&[vec![]], 1, 1);
    fixture.add_scheduled_workflow();
    let repository = &fixture.config.repositories[0];
    for args in [
        vec!["init", "-b", "main"],
        vec!["config", "user.email", "factory@example.test"],
        vec!["config", "user.name", "Factory Test"],
        vec!["add", "."],
        vec!["commit", "-m", "scheduled fixture"],
    ] {
        assert!(
            Command::new("git")
                .args(args)
                .current_dir(repository)
                .status()
                .unwrap()
                .success()
        );
    }
    let inspected_commit = String::from_utf8(
        Command::new("git")
            .args(["rev-parse", "HEAD"])
            .current_dir(repository)
            .output()
            .unwrap()
            .stdout,
    )
    .unwrap()
    .trim()
    .to_owned();
    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    ledger
        .enqueue_with_payload(
            &TaskIdentity::scheduled(
                "example/repo-0",
                "scheduled-maintenance",
                "2026-07-18T12:00:00Z",
            )
            .unwrap(),
            Some(&fixture.scheduled_payload("2026-07-18T12:00:00Z")),
        )
        .unwrap();
    drop(ledger);
    fixture.open_gate();
    let cancellation = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let token = cancellation.clone();
        tokio::spawn(async move { daemon.run(token).await })
    };
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks[0].state == TaskState::Succeeded)
    })
    .await;
    cancellation.cancel();
    running.await.unwrap().unwrap();

    let ledger = Ledger::open(&fixture.ledger_path).unwrap();
    let tasks = ledger.tasks().unwrap();
    assert_eq!(tasks.len(), 1);
    assert_eq!(tasks[0].state, TaskState::Succeeded);
    assert_eq!(ledger.runs_for_task(tasks[0].id).unwrap().len(), 1);
    let prompt = fs::read_to_string(fixture.started_slots()[0].join("prompt")).unwrap();
    assert!(prompt.contains("Scheduled occurrence: 2026-07-18T12:00:00Z"));
    assert!(prompt.contains("You may use the authenticated gh CLI"));
    assert!(prompt.contains("Factory does not create tickets for you"));
    assert!(prompt.contains(&format!("Inspected repository commit: {inspected_commit}")));
}

#[tokio::test]
async fn failing_scheduled_task_does_not_block_ticket_polling() {
    let mut fixture = Fixture::new(&[vec![issue(29)]], 1, 1);
    fixture.add_scheduled_workflow();
    Ledger::open(&fixture.ledger_path)
        .unwrap()
        .enqueue_with_payload(
            &TaskIdentity::scheduled(
                "example/repo-0",
                "scheduled-maintenance",
                "2026-07-18T12:00:00Z",
            )
            .unwrap(),
            Some(
                &serde_json::json!({
                    "schedule_fingerprint": fixture.scheduled_fingerprint(),
                })
                .to_string(),
            ),
        )
        .unwrap();
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
            .is_ok_and(|tasks| {
                tasks.len() == 2
                    && tasks
                        .iter()
                        .any(|task| task.kind == "scheduled" && task.state == TaskState::Failed)
                    && tasks
                        .iter()
                        .any(|task| task.kind == "ticket" && task.state == TaskState::Succeeded)
            })
    })
    .await;
    cancellation.cancel();
    running.await.unwrap().unwrap();
}

#[tokio::test]
async fn invalid_scheduled_workflow_does_not_block_valid_ticket_workflow() {
    let fixture = Fixture::new(&[vec![issue(31)]], 1, 1);
    fs::write(
        fixture.config.repositories[0].join(".factory/workflows/invalid-schedule.md"),
        "+++\nschedule = \"eventually\"\ntimezone = \"UTC\"\n+++\n\nINVALID SCHEDULE\n",
    )
    .unwrap();
    fs::write(
        fixture.config.repositories[0]
            .join(".factory/workflows/invalid-schedule-frontmatter.md"),
        "+++\nschedule = \"* * * * *\"\ntimezone = \"UTC\"\nunknown = true\n+++\n\nINVALID SCHEDULE FRONTMATTER\n",
    )
    .unwrap();
    fs::write(
        fixture.config.repositories[0].join(".factory/workflows/malformed-schedule.md"),
        "+++\nschedule = \"unterminated\ntimezone = \"UTC\"\n+++\n\nMALFORMED SCHEDULE\n",
    )
    .unwrap();
    assert_eq!(
        WorkflowCatalog::load(&fixture.config)
            .unwrap()
            .invalid_count(),
        3
    );
    fixture.open_gate();
    let search_path = std::env::join_paths(
        std::iter::once(fixture.gh.parent().unwrap().to_path_buf()).chain(
            std::env::var_os("PATH")
                .as_deref()
                .map(std::env::split_paths)
                .into_iter()
                .flatten(),
        ),
    )
    .unwrap();
    let mut daemon = Command::new(env!("CARGO_BIN_EXE_factory"));
    daemon
        .args([
            "run",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            fixture.ledger_path.parent().unwrap().to_str().unwrap(),
        ])
        .env("PATH", search_path);
    let mut daemon = daemon.spawn().unwrap();

    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| {
                tasks.len() == 1
                    && tasks[0].kind == "ticket"
                    && tasks[0].state == TaskState::Succeeded
            })
    })
    .await;
    nix::sys::signal::kill(
        nix::unistd::Pid::from_raw(i32::try_from(daemon.id()).unwrap()),
        nix::sys::signal::Signal::SIGINT,
    )
    .unwrap();
    assert!(daemon.wait().unwrap().success());
}

#[test]
fn invalid_label_workflow_fails_daemon_startup() {
    let fixture = Fixture::new(&[vec![issue(32)]], 1, 1);
    fs::write(
        fixture.config.repositories[0].join(".factory/workflows/implement-ready-ticket.md"),
        "+++\nlabel = \"factory:ready\"\ntimeout = \"0s\"\n+++\n\nINVALID TICKET WORKFLOW\n",
    )
    .unwrap();
    fs::write(
        fixture.config.repositories[0].join(".factory/workflows/invalid-schedule.md"),
        "+++\nschedule = \"eventually\"\ntimezone = \"UTC\"\n+++\n\nINVALID SCHEDULE\n",
    )
    .unwrap();
    let search_path = std::env::join_paths(
        std::iter::once(fixture.gh.parent().unwrap().to_path_buf()).chain(
            std::env::var_os("PATH")
                .as_deref()
                .map(std::env::split_paths)
                .into_iter()
                .flatten(),
        ),
    )
    .unwrap();

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            fixture.ledger_path.parent().unwrap().to_str().unwrap(),
        ])
        .env("PATH", search_path)
        .assert()
        .failure()
        .stderr(predicates::str::contains(
            "Factory skipped invalid scheduled workflow",
        ))
        .stderr(predicates::str::contains(
            "Factory cannot start with invalid ticket workflows",
        ))
        .stderr(predicates::str::contains(
            "timeout must be greater than zero",
        ));
    assert!(!fixture.ledger_path.exists());
}

#[test]
fn ambiguous_schedule_and_label_workflow_fails_daemon_startup() {
    let fixture = Fixture::new(&[vec![issue(33)]], 1, 1);
    fs::write(
        fixture.config.repositories[0].join(".factory/workflows/ambiguous.md"),
        "+++\nschedule = \"* * * * *\"\ntimezone = \"UTC\"\nlabel = \"factory:ready\"\n+++\n\nAMBIGUOUS WORKFLOW\n",
    )
    .unwrap();
    let search_path = std::env::join_paths(
        std::iter::once(fixture.gh.parent().unwrap().to_path_buf()).chain(
            std::env::var_os("PATH")
                .as_deref()
                .map(std::env::split_paths)
                .into_iter()
                .flatten(),
        ),
    )
    .unwrap();

    AssertCommand::cargo_bin("factory")
        .unwrap()
        .args([
            "run",
            "--config",
            fixture.config_path.to_str().unwrap(),
            "--data-directory",
            fixture.ledger_path.parent().unwrap().to_str().unwrap(),
        ])
        .env("PATH", search_path)
        .assert()
        .failure()
        .stderr(predicates::str::contains(
            "Factory cannot start with invalid ticket workflows",
        ))
        .stderr(predicates::str::contains(
            "workflow must declare exactly one trigger",
        ));
}

#[tokio::test]
async fn later_schedule_prompt_includes_previous_successful_run() {
    let mut fixture = Fixture::new(&[vec![]], 1, 1);
    fixture.add_scheduled_workflow();
    let mut ledger = Ledger::open(&fixture.ledger_path).unwrap();
    ledger
        .enqueue_with_payload(
            &TaskIdentity::scheduled(
                "example/repo-0",
                "scheduled-maintenance",
                "2026-07-18T12:00:00Z",
            )
            .unwrap(),
            Some(&fixture.scheduled_payload("2026-07-18T12:00:00Z")),
        )
        .unwrap();
    drop(ledger);
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
            .is_ok_and(|tasks| tasks[0].state == TaskState::Succeeded)
    })
    .await;

    Ledger::open(&fixture.ledger_path)
        .unwrap()
        .enqueue_with_payload(
            &TaskIdentity::scheduled(
                "example/repo-0",
                "scheduled-maintenance",
                "2026-07-18T12:01:00Z",
            )
            .unwrap(),
            Some(&fixture.scheduled_payload("2026-07-18T12:01:00Z")),
        )
        .unwrap();
    wait_for(|| fixture.started_slots().len() == 2).await;
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks.len() == 2 && tasks[1].state == TaskState::Succeeded)
    })
    .await;
    cancellation.cancel();
    running.await.unwrap().unwrap();

    let prompt = fs::read_to_string(fixture.started_slots()[1].join("prompt")).unwrap();
    assert!(prompt.contains("Previous successful run: 20"));
    assert!(!prompt.contains("Previous successful run: none"));
}

#[tokio::test]
async fn blocked_github_poll_does_not_delay_schedule_ticks_or_start_another_poll() {
    let mut fixture = Fixture::new(&[vec![]], 1, 1);
    fixture.add_scheduled_workflow();
    fixture.open_gate();
    let api_block = PathBuf::from(format!("{}.api-block", fixture.gh.display()));
    let api_started = PathBuf::from(format!("{}.api-started", fixture.gh.display()));
    let api_release = PathBuf::from(format!("{}.api-release", fixture.gh.display()));
    let api_concurrent = PathBuf::from(format!("{}.api-concurrent", fixture.gh.display()));
    fs::write(&api_block, "block").unwrap();
    let cancellation = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let cancellation = cancellation.clone();
        tokio::spawn(async move { daemon.run(cancellation).await })
    };
    wait_for(|| api_started.exists()).await;
    Connection::open(&fixture.ledger_path)
        .unwrap()
        .execute(
            "UPDATE schedule_cursors
             SET next_due_at = (CAST(strftime('%s', 'now') AS INTEGER) * 1000) - 100",
            [],
        )
        .unwrap();

    tokio::time::timeout(Duration::from_secs(3), async {
        while fixture.started_slots().is_empty() {
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
    })
    .await
    .expect("scheduled task did not start while GitHub polling was blocked");
    assert!(api_block.exists());
    assert!(!api_release.exists());
    assert!(!api_concurrent.exists());

    fs::write(api_release, "release").unwrap();
    cancellation.cancel();
    running.await.unwrap().unwrap();
    assert_eq!(fixture.started_slots().len(), 1);
}

#[tokio::test]
async fn scheduled_and_ticket_tasks_share_concurrency_capacity() {
    let mut fixture = Fixture::new(&[vec![issue(30)]], 2, 2);
    fixture.add_scheduled_workflow();
    Ledger::open(&fixture.ledger_path)
        .unwrap()
        .enqueue_with_payload(
            &TaskIdentity::scheduled(
                "example/repo-0",
                "scheduled-maintenance",
                "2026-07-18T12:00:00Z",
            )
            .unwrap(),
            Some(&fixture.scheduled_payload("2026-07-18T12:00:00Z")),
        )
        .unwrap();
    let cancellation = CancellationToken::new();
    let daemon = Arc::new(fixture.daemon());
    let running = {
        let daemon = Arc::clone(&daemon);
        let cancellation = cancellation.clone();
        tokio::spawn(async move { daemon.run(cancellation).await })
    };

    wait_for(|| fixture.started_slots().len() == 2).await;
    let prompts = fixture
        .started_slots()
        .iter()
        .map(|slot| fs::read_to_string(slot.join("prompt")).unwrap())
        .collect::<Vec<_>>();
    assert!(
        prompts
            .iter()
            .any(|prompt| prompt.contains("Scheduled occurrence"))
    );
    assert!(
        prompts
            .iter()
            .any(|prompt| prompt.contains("Current ticket and discussion"))
    );
    fixture.open_gate();
    wait_for(|| {
        Ledger::open(&fixture.ledger_path)
            .and_then(|ledger| ledger.tasks())
            .is_ok_and(|tasks| tasks.iter().all(|task| task.state == TaskState::Succeeded))
    })
    .await;
    cancellation.cancel();
    running.await.unwrap().unwrap();
}
