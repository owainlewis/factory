#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Output, Stdio};
use std::thread;
use std::time::{Duration, Instant, SystemTime};

use factory::fleet::delay_to_next_poll;

fn executable(path: &Path, contents: &str) {
    fs::write(path, contents).unwrap();
    let mut permissions = fs::metadata(path).unwrap().permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(path, permissions).unwrap();
}

fn repository(root: &Path, data_home: &Path, name: &str, source_body: &str) -> PathBuf {
    let path = root.join(name);
    fs::create_dir(&path).unwrap();
    assert!(
        Command::new("git")
            .args(["init", "--quiet", "--initial-branch=main"])
            .current_dir(&path)
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
                &format!("git@github.com:acme/{name}.git"),
            ])
            .current_dir(&path)
            .status()
            .unwrap()
            .success()
    );
    assert!(
        Command::new(env!("CARGO_BIN_EXE_factory"))
            .arg("init")
            .current_dir(&path)
            .env("FACTORY_DATA_HOME", data_home)
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .unwrap()
            .success()
    );
    fs::create_dir_all(path.join(".factory/workflows")).unwrap();
    fs::write(path.join(".factory/workflows/implement.md"), "Implement.\n").unwrap();
    fs::write(
        path.join(".factory/config.toml"),
        r#"version = 1
poll_every = "60s"

[worker]
runtime = "codex"
sandbox = "worktree"
timeout = "1h"
maximum_timeout = "8h"
max_concurrent = 1

[source]
command = ["./.factory/source.sh"]

[trigger.implement]
type = "source"
state = "ready"
labels = ["factory:ready"]
workflow = ".factory/workflows/implement.md"
"#,
    )
    .unwrap();
    executable(&path.join(".factory/source.sh"), source_body);
    for arguments in [
        &["config", "user.email", "factory@example.com"][..],
        &["config", "user.name", "Factory Test"][..],
        &["add", "."][..],
        &["commit", "--quiet", "-m", "fixture"][..],
        &["update-ref", "refs/remotes/origin/main", "HEAD"][..],
    ] {
        assert!(
            Command::new("git")
                .args(arguments)
                .current_dir(&path)
                .status()
                .unwrap()
                .success()
        );
    }
    path.canonicalize().unwrap()
}

fn fake_binaries(root: &Path, worker_marker: &Path) -> PathBuf {
    let bin = root.join("bin");
    fs::create_dir(&bin).unwrap();
    executable(
        &bin.join("gh"),
        r#"#!/bin/sh
case "$1" in
  --version) echo "gh version 2.80.0" ;;
  auth) echo "authenticated" ;;
  repo)
    case "$*" in
      *defaultBranchRef*) echo "main" ;;
      *) echo "Acme/$(basename "$PWD")" ;;
    esac
    ;;
  *) exit 64 ;;
esac
"#,
    );
    executable(
        &bin.join("codex"),
        &format!(
            r#"#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 1.2.3"; exit 0; fi
if [ "$1" = "login" ] && [ "$2" = "status" ]; then
  echo "Logged in using ChatGPT"
  exit 0
fi
output=""
previous=""
for argument in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then output="$argument"; fi
  previous="$argument"
done
cat >/dev/null
git remote get-url origin >> {:?}
echo '{{"type":"thread.started","thread_id":"fleet-thread"}}'
printf '%s' 'completed' > "$output"
"#,
            worker_marker
        ),
    );
    let real_git =
        String::from_utf8(Command::new("which").arg("git").output().unwrap().stdout).unwrap();
    executable(
        &bin.join("git"),
        &format!(
            r#"#!/bin/sh
if [ "$1" = "fetch" ]; then exit 0; fi
exec {:?} "$@"
"#,
            real_git.trim()
        ),
    );
    bin
}

fn spawn_fleet(fleet: &Path, data_home: &Path, bin: &Path) -> Child {
    let path = format!(
        "{}:{}",
        bin.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    Command::new(env!("CARGO_BIN_EXE_factory"))
        .args(["run", "--fleet", fleet.to_str().unwrap()])
        .env("PATH", path)
        .env("FACTORY_DATA_HOME", data_home)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap()
}

fn wait_for_files(paths: &[&Path]) {
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if paths.iter().all(|path| path.exists()) {
            return;
        }
        thread::sleep(Duration::from_millis(20));
    }
    panic!("timed out waiting for {paths:?}");
}

fn wait_for_lines(path: &Path, expected: usize, timeout: Duration) {
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        let lines = fs::read_to_string(path)
            .map(|contents| contents.lines().count())
            .unwrap_or(0);
        if lines >= expected {
            return;
        }
        thread::sleep(Duration::from_millis(20));
    }
    panic!(
        "timed out waiting for {expected} lines in {}",
        path.display()
    );
}

fn repositories_due_soon() -> [String; 2] {
    let now = SystemTime::now();
    let mut names = Vec::new();
    for index in 0..10_000 {
        let name = format!("poll-{index}");
        let identity = format!("acme/{name}");
        let delay =
            delay_to_next_poll(&identity, "implement", Duration::from_secs(60), now).unwrap();
        if (Duration::from_secs(20)..=Duration::from_secs(25)).contains(&delay) {
            names.push(name);
            if names.len() == 2 {
                return names.try_into().unwrap();
            }
        }
    }
    panic!("could not find two repository identities due to poll soon");
}

fn source_with_issue_after_validation(counter: &Path) -> String {
    format!(
        r##"#!/bin/sh
count=$(cat {:?} 2>/dev/null || echo 0)
count=$((count + 1))
printf '%s' "$count" > {:?}
if [ "$count" -eq 1 ]; then
  printf '%s\n' '{{"issues":[]}}'
else
  printf '%s\n' '{{"issues":[{{"key":"#42","title":"Fleet task","state":"ready","labels":["factory:ready"]}}]}}'
fi
"##,
        counter, counter
    )
}

fn stop(child: Child) -> Output {
    nix::sys::signal::kill(
        nix::unistd::Pid::from_raw(child.id() as i32),
        nix::sys::signal::Signal::SIGINT,
    )
    .unwrap();
    child.wait_with_output().unwrap()
}

fn wait_for_exit(mut child: Child) -> Output {
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if child.try_wait().unwrap().is_some() {
            return child.wait_with_output().unwrap();
        }
        thread::sleep(Duration::from_millis(20));
    }
    let _ = child.kill();
    panic!("fleet process did not exit");
}

#[test]
fn continuous_polling_enqueues_and_dispatches_work_across_two_repositories() {
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let first_counter = temp.path().join("first-source-count");
    let second_counter = temp.path().join("second-source-count");
    let worker_marker = temp.path().join("workers-launched");
    let [first_name, second_name] = repositories_due_soon();
    let first = repository(
        temp.path(),
        &data_home,
        &first_name,
        &source_with_issue_after_validation(&first_counter),
    );
    let second = repository(
        temp.path(),
        &data_home,
        &second_name,
        &source_with_issue_after_validation(&second_counter),
    );
    let fleet = temp.path().join("fleet.toml");
    fs::write(
        &fleet,
        format!(
            "max_concurrent = 2\n[[repository]]\nname = \"acme/{first_name}\"\npath = {:?}\nmax_concurrent = 1\n[[repository]]\nname = \"acme/{second_name}\"\npath = {:?}\nmax_concurrent = 1\n",
            first, second
        ),
    )
    .unwrap();

    let child = spawn_fleet(
        &fleet,
        &data_home,
        &fake_binaries(temp.path(), &worker_marker),
    );
    wait_for_lines(&worker_marker, 2, Duration::from_secs(35));
    let output = stop(child);
    assert!(output.status.success(), "{output:?}");

    let launches = fs::read_to_string(&worker_marker).unwrap();
    assert!(
        launches.contains(&format!("acme/{first_name}.git")),
        "{launches}"
    );
    assert!(
        launches.contains(&format!("acme/{second_name}.git")),
        "{launches}"
    );
    for counter in [&first_counter, &second_counter] {
        let queries = fs::read_to_string(counter).unwrap().parse::<u32>().unwrap();
        assert!(
            queries >= 3,
            "{} was queried only {queries} times",
            counter.display()
        );
    }
}

#[test]
fn two_repository_idle_fleet_stays_running_and_launches_no_workers() {
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let first_marker = temp.path().join("first-polled");
    let second_marker = temp.path().join("second-polled");
    let worker_marker = temp.path().join("worker-launched");
    let first = repository(
        temp.path(),
        &data_home,
        "first",
        &format!(
            "#!/bin/sh\ntouch {:?}\nprintf '%s\\n' '{{\"issues\":[]}}'\n",
            first_marker
        ),
    );
    let second = repository(
        temp.path(),
        &data_home,
        "second",
        &format!(
            "#!/bin/sh\ntouch {:?}\nprintf '%s\\n' '{{\"issues\":[]}}'\n",
            second_marker
        ),
    );
    let fleet = temp.path().join("fleet.toml");
    fs::write(
        &fleet,
        format!(
            "max_concurrent = 2\n[[repository]]\nname = \"acme/first\"\npath = {:?}\n[[repository]]\nname = \"acme/second\"\npath = {:?}\n",
            first, second
        ),
    )
    .unwrap();
    let child = spawn_fleet(
        &fleet,
        &data_home,
        &fake_binaries(temp.path(), &worker_marker),
    );
    wait_for_files(&[&first_marker, &second_marker]);
    thread::sleep(Duration::from_millis(200));
    let output = stop(child);
    assert!(output.status.success(), "{output:?}");
    assert!(!worker_marker.exists());
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(stderr.contains("Factory fleet ready: repositories=2 healthy=2"));
}

#[test]
fn unavailable_repository_does_not_stop_a_healthy_peer() {
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let failed_marker = temp.path().join("failed-polled");
    let healthy_marker = temp.path().join("healthy-polled");
    let worker_marker = temp.path().join("worker-launched");
    let failed = repository(
        temp.path(),
        &data_home,
        "failed",
        &format!("#!/bin/sh\ntouch {:?}\nexit 1\n", failed_marker),
    );
    let healthy = repository(
        temp.path(),
        &data_home,
        "healthy",
        &format!(
            "#!/bin/sh\ntouch {:?}\nprintf '%s\\n' '{{\"issues\":[]}}'\n",
            healthy_marker
        ),
    );
    let fleet = temp.path().join("fleet.toml");
    fs::write(
        &fleet,
        format!(
            "max_concurrent = 2\n[[repository]]\nname = \"acme/failed\"\npath = {:?}\n[[repository]]\nname = \"acme/healthy\"\npath = {:?}\n",
            failed, healthy
        ),
    )
    .unwrap();
    let child = spawn_fleet(
        &fleet,
        &data_home,
        &fake_binaries(temp.path(), &worker_marker),
    );
    wait_for_files(&[&failed_marker, &healthy_marker]);
    thread::sleep(Duration::from_millis(200));
    let output = stop(child);
    assert!(output.status.success(), "{output:?}");
    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(stderr.contains("repository=acme/failed status=unavailable"));
    assert!(stderr.contains("Factory fleet ready: repositories=2 healthy=1"));
    assert!(!worker_marker.exists());
}

#[test]
fn fleet_with_no_healthy_repository_exits_nonzero() {
    let temp = tempfile::tempdir().unwrap();
    let data_home = temp.path().join("data");
    let failed_marker = temp.path().join("failed-polled");
    let worker_marker = temp.path().join("worker-launched");
    let failed = repository(
        temp.path(),
        &data_home,
        "failed",
        &format!("#!/bin/sh\ntouch {:?}\nexit 1\n", failed_marker),
    );
    let fleet = temp.path().join("fleet.toml");
    fs::write(
        &fleet,
        format!(
            "max_concurrent = 1\n[[repository]]\nname = \"acme/failed\"\npath = {:?}\n",
            failed
        ),
    )
    .unwrap();
    let child = spawn_fleet(
        &fleet,
        &data_home,
        &fake_binaries(temp.path(), &worker_marker),
    );
    let output = wait_for_exit(child);
    assert!(!output.status.success());
    assert!(failed_marker.exists(), "{output:?}");
    assert!(
        String::from_utf8_lossy(&output.stderr)
            .contains("fleet has no healthy enabled repository at startup")
    );
    assert!(!worker_marker.exists());
}
