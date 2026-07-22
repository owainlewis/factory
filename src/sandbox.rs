use std::ffi::OsString;
use std::fmt;
use std::path::{Path, PathBuf};
use std::process::{ExitStatus, Stdio};
use std::time::{Duration, Instant};

use anyhow::{Context, Result, bail};
use serde_json::Value;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWriteExt};
use tokio::process::Command;
use tokio::sync::watch;
use tokio::time::Instant as TokioInstant;
use tokio_util::sync::CancellationToken;

use crate::config::WorkerConfig;
use crate::inspection::sanitize_for_storage;
use crate::runtime::{
    ExecutionResult, RuntimeObservation, Termination, find_pull_request_url,
    write_stderr_best_effort, write_stdout_best_effort,
};

const MAX_STREAM_BYTES: usize = 256 * 1024;
const MAX_STDERR_BYTES: usize = 64 * 1024;
const MAX_FINAL_BYTES: usize = 256 * 1024;
const STOP_TIMEOUT: Duration = Duration::from_secs(5);
const COMMAND_TIMEOUT: Duration = Duration::from_secs(30);
const CREATE_TIMEOUT: Duration = Duration::from_secs(5 * 60);
const CODEX_COMMAND: &str = "set -o pipefail; codex exec --ephemeral --ignore-user-config --sandbox danger-full-access --json --color never --output-last-message /tmp/factory-last-message - 2> >(tee /tmp/factory-stderr >&2) | tee /tmp/factory-stdout";
const SNAPSHOT_COMMAND: &str = "git add -A && if ! git diff --cached --quiet; then git -c user.name=Factory -c user.email=factory@localhost commit --no-gpg-sign --no-verify -m 'factory: preserve sandbox changes'; fi";

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SandboxIdentity {
    pub name: String,
    pub template: String,
    pub sbx_version: String,
    pub instance_id: String,
    pub run_id: i64,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct OwnedSandbox {
    pub name: String,
    pub instance_id: String,
    pub run_id: i64,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RecoveredSandbox {
    pub identity: OwnedSandbox,
    pub logs: String,
}

#[derive(Debug)]
pub struct SandboxRunFailure {
    pub identity: SandboxIdentity,
    source: anyhow::Error,
}

impl fmt::Display for SandboxRunFailure {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            formatter,
            "Docker Sandbox worker {} failed: {:#}",
            self.identity.name, self.source
        )
    }
}

impl std::error::Error for SandboxRunFailure {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(self.source.as_ref())
    }
}

#[derive(Debug, Clone)]
pub struct SandboxWorker {
    executable: PathBuf,
    config: WorkerConfig,
    instance_id: String,
    stream_activity: bool,
}

impl SandboxWorker {
    pub fn new(config: WorkerConfig, instance_id: impl Into<String>) -> Self {
        Self {
            executable: PathBuf::from("sbx"),
            config,
            instance_id: instance_id.into(),
            stream_activity: true,
        }
    }

    pub fn with_executable(mut self, executable: impl Into<PathBuf>) -> Self {
        self.executable = executable.into();
        self
    }

    pub fn with_activity_streaming(mut self, enabled: bool) -> Self {
        self.stream_activity = enabled;
        self
    }

    pub fn template(&self) -> &str {
        &self.config.template
    }

    pub fn instance_id(&self) -> &str {
        &self.instance_id
    }

    pub fn github_token_env(&self) -> &str {
        &self.config.github_token_env
    }

    pub fn limits_json(&self) -> String {
        serde_json::json!({
            "memory": self.config.memory,
            "cpus": self.config.cpus,
            "workspace": "private_clone",
            "isolation": "microvm",
        })
        .to_string()
    }

    pub async fn validate(&self, cancellation: &CancellationToken) -> Result<String> {
        let version = self
            .command_output(&["version"], COMMAND_TIMEOUT, cancellation)
            .await
            .context("Docker Sandboxes is unavailable; install `sbx`, sign in, and retry")?;
        let version = version.trim();
        if version.is_empty() {
            bail!("Docker Sandboxes returned an empty version");
        }
        self.validate_secret("openai", cancellation).await?;
        self.validate_secret("github", cancellation).await?;
        Ok(version.to_owned())
    }

    async fn validate_secret(&self, service: &str, cancellation: &CancellationToken) -> Result<()> {
        let output = self
            .command_output(
                &["secret", "ls", "--global", "--service", service],
                COMMAND_TIMEOUT,
                cancellation,
            )
            .await
            .with_context(|| format!("failed to inspect Docker Sandbox {service} credentials"))?;
        if !output
            .split_whitespace()
            .any(|field| field.eq_ignore_ascii_case(service))
        {
            let setup = if service == "openai" {
                "sbx secret set -g openai --oauth"
            } else {
                "gh auth token | sbx secret set -g github"
            };
            bail!("Docker Sandboxes has no {service} credential; run `{setup}`");
        }
        Ok(())
    }

    #[allow(clippy::too_many_arguments)]
    pub async fn run<F>(
        &self,
        run_id: i64,
        clone: &Path,
        prompt: &str,
        run_timeout: Duration,
        cancellation: CancellationToken,
        observations: watch::Sender<RuntimeObservation>,
        mut persist: F,
    ) -> Result<(ExecutionResult, SandboxIdentity)>
    where
        F: FnMut(&SandboxIdentity, &str) -> Result<()> + Send,
    {
        let started = Instant::now();
        let version = self
            .command_output(&["version"], COMMAND_TIMEOUT, &cancellation)
            .await?;
        let identity = SandboxIdentity {
            name: format!("factory-{}-{run_id}", self.instance_id),
            template: self.config.template.clone(),
            sbx_version: version.trim().to_owned(),
            instance_id: self.instance_id.clone(),
            run_id,
        };
        persist(&identity, "creating")?;
        if let Err(source) = self.create(&identity, clone, &cancellation).await {
            return Err(SandboxRunFailure { identity, source }.into());
        }
        if let Err(source) = persist(&identity, "created") {
            let _ = self.remove(&identity.name, &CancellationToken::new()).await;
            return Err(SandboxRunFailure { identity, source }.into());
        }
        let result = self
            .execute(
                &identity,
                prompt,
                run_timeout,
                cancellation,
                observations,
                started,
            )
            .await;
        match result {
            Ok(result) => Ok((result, identity)),
            Err(error) => {
                let _ = self.stop(&identity.name, &CancellationToken::new()).await;
                Err(SandboxRunFailure {
                    identity,
                    source: error,
                }
                .into())
            }
        }
    }

    async fn create(
        &self,
        identity: &SandboxIdentity,
        clone: &Path,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let clone = clone
            .canonicalize()
            .with_context(|| format!("failed to resolve clone {}", clone.display()))?;
        let arguments = vec![
            OsString::from("create"),
            OsString::from("--quiet"),
            OsString::from("--name"),
            OsString::from(&identity.name),
            OsString::from("--clone"),
            OsString::from("--cpus"),
            OsString::from(self.config.cpus.to_string()),
            OsString::from("--memory"),
            OsString::from(&self.config.memory),
            OsString::from("--template"),
            OsString::from(&self.config.template),
            OsString::from("codex"),
            clone.as_os_str().to_owned(),
        ];
        self.command_output_os(&arguments, CREATE_TIMEOUT, cancellation)
            .await
            .context("failed to create Docker Sandbox for the dedicated clone")?;
        Ok(())
    }

    #[allow(clippy::too_many_arguments)]
    async fn execute(
        &self,
        identity: &SandboxIdentity,
        prompt: &str,
        run_timeout: Duration,
        cancellation: CancellationToken,
        observations: watch::Sender<RuntimeObservation>,
        started: Instant,
    ) -> Result<ExecutionResult> {
        let mut command = Command::new(&self.executable);
        command
            .args([
                "exec",
                "--interactive",
                &identity.name,
                "bash",
                "-c",
                CODEX_COMMAND,
            ])
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        let mut child = command
            .spawn()
            .context("failed to start Codex in Docker Sandbox")?;
        let mut stdin = child
            .stdin
            .take()
            .context("Docker Sandbox exec has no stdin")?;
        let stdout = child
            .stdout
            .take()
            .context("Docker Sandbox exec has no stdout")?;
        let stderr = child
            .stderr
            .take()
            .context("Docker Sandbox exec has no stderr")?;
        let stream = self.stream_activity;
        let stdout_task = tokio::spawn(capture_pipe(stdout, true, stream, observations.clone()));
        let stderr_task = tokio::spawn(capture_pipe(stderr, false, stream, observations));
        stdin.write_all(prompt.as_bytes()).await?;
        stdin.shutdown().await?;
        drop(stdin);

        let deadline = TokioInstant::now() + run_timeout;
        let (termination, status) = tokio::select! {
            () = cancellation.cancelled() => {
                self.stop(&identity.name, &CancellationToken::new()).await?;
                (Termination::Cancelled, None)
            }
            () = tokio::time::sleep_until(deadline) => {
                self.stop(&identity.name, &CancellationToken::new()).await?;
                (Termination::TimedOut, None)
            }
            status = child.wait() => {
                (Termination::Exited, Some(status.context("failed to wait for Docker Sandbox exec")?))
            }
        };
        if status.is_none()
            && tokio::time::timeout(STOP_TIMEOUT, child.wait())
                .await
                .is_err()
        {
            child.kill().await.ok();
            child.wait().await.ok();
        }
        let stdout = await_capture(stdout_task, "stdout").await?;
        let stderr = await_capture(stderr_task, "stderr").await?;
        let status = status.unwrap_or_else(|| exit_status(1));
        let final_response = self
            .final_response(&identity.name)
            .await
            .unwrap_or_default();
        Ok(ExecutionResult {
            status,
            termination,
            final_response,
            final_response_truncated: false,
            thread_id: None,
            duration: started.elapsed(),
            activity_lines: stdout.lines,
            activity_error: stdout.malformed,
            stderr_tail: stderr.text,
        })
    }

    pub async fn owned_sandboxes(
        &self,
        cancellation: &CancellationToken,
    ) -> Result<Vec<OwnedSandbox>> {
        let output = self
            .command_output(&["ls", "--quiet"], COMMAND_TIMEOUT, cancellation)
            .await?;
        let prefix = format!("factory-{}-", self.instance_id);
        output
            .lines()
            .filter_map(|line| {
                line.trim()
                    .strip_prefix(&prefix)
                    .map(|suffix| (line, suffix))
            })
            .map(|(line, suffix)| {
                let run_id = suffix
                    .parse::<i64>()
                    .context("owned Docker Sandbox name has invalid run ID")?;
                if run_id <= 0 {
                    bail!("owned Docker Sandbox name has invalid run ID");
                }
                Ok(OwnedSandbox {
                    name: line.trim().to_owned(),
                    instance_id: self.instance_id.clone(),
                    run_id,
                })
            })
            .collect()
    }

    pub async fn recover_sandbox(
        &self,
        identity: &OwnedSandbox,
        cancellation: &CancellationToken,
    ) -> Result<RecoveredSandbox> {
        let logs = self.logs(&identity.name).await.unwrap_or_default();
        let _ = self.stop(&identity.name, cancellation).await;
        Ok(RecoveredSandbox {
            identity: identity.clone(),
            logs,
        })
    }

    pub async fn remove_sandbox(&self, name: &str) -> Result<()> {
        self.remove(name, &CancellationToken::new()).await
    }

    pub async fn stop_sandbox(&self, name: &str) -> Result<()> {
        self.stop(name, &CancellationToken::new()).await
    }

    pub async fn sandbox_logs(&self, name: &str) -> Result<String> {
        self.logs(name).await
    }

    pub async fn preserve_workspace(&self, name: &str, clone: &Path) -> Result<()> {
        self.command_output(
            &["exec", name, "bash", "-c", SNAPSHOT_COMMAND],
            CREATE_TIMEOUT,
            &CancellationToken::new(),
        )
        .await
        .context("failed to snapshot Docker Sandbox workspace")?;
        let remote = format!("sandbox-{name}");
        self.git(clone, &["fetch", "--force", &remote, "HEAD"])
            .await
            .context("failed to fetch Docker Sandbox workspace")?;
        self.git(clone, &["reset", "--hard", "FETCH_HEAD"])
            .await
            .context("failed to restore Docker Sandbox workspace into host clone")?;
        Ok(())
    }

    async fn stop(&self, name: &str, cancellation: &CancellationToken) -> Result<()> {
        self.command_output(&["stop", name], COMMAND_TIMEOUT, cancellation)
            .await?;
        Ok(())
    }

    async fn remove(&self, name: &str, cancellation: &CancellationToken) -> Result<()> {
        self.command_output(&["rm", "--force", name], CREATE_TIMEOUT, cancellation)
            .await?;
        Ok(())
    }

    async fn final_response(&self, name: &str) -> Result<String> {
        let output = self
            .command_output(
                &["exec", name, "cat", "/tmp/factory-last-message"],
                COMMAND_TIMEOUT,
                &CancellationToken::new(),
            )
            .await?;
        Ok(truncate_head(&output, MAX_FINAL_BYTES))
    }

    async fn logs(&self, name: &str) -> Result<String> {
        let output = self
            .command_output(
                &[
                    "exec",
                    name,
                    "bash",
                    "-c",
                    "cat /tmp/factory-stdout /tmp/factory-stderr 2>/dev/null || true",
                ],
                COMMAND_TIMEOUT,
                &CancellationToken::new(),
            )
            .await?;
        Ok(truncate_tail(&output, MAX_STREAM_BYTES))
    }

    async fn command_output(
        &self,
        arguments: &[&str],
        timeout: Duration,
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let arguments = arguments.iter().map(OsString::from).collect::<Vec<_>>();
        self.command_output_os(&arguments, timeout, cancellation)
            .await
    }

    async fn command_output_os(
        &self,
        arguments: &[OsString],
        timeout: Duration,
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let mut command = Command::new(&self.executable);
        command
            .args(arguments)
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        let child = command.spawn().with_context(|| {
            format!(
                "failed to start Docker Sandboxes CLI at {}",
                self.executable.display()
            )
        })?;
        let output = tokio::select! {
            () = cancellation.cancelled() => bail!("Docker Sandboxes command cancelled"),
            output = tokio::time::timeout(timeout, child.wait_with_output()) => {
                output.context("Docker Sandboxes command timed out")??
            }
        };
        if !output.status.success() {
            bail!(
                "Docker Sandboxes command failed with {}: {}",
                output.status,
                String::from_utf8_lossy(&output.stderr).trim()
            );
        }
        String::from_utf8(output.stdout).context("Docker Sandboxes output was not UTF-8")
    }

    async fn git(&self, clone: &Path, arguments: &[&str]) -> Result<()> {
        let output = Command::new("git")
            .args([
                "-c",
                "core.hooksPath=/dev/null",
                "-c",
                "core.fsmonitor=false",
                "-c",
                "credential.helper=",
            ])
            .args(arguments)
            .current_dir(clone)
            .env("GIT_CONFIG_NOSYSTEM", "1")
            .env("GIT_CONFIG_GLOBAL", "/dev/null")
            .env_remove(&self.config.github_token_env)
            .env_remove("GH_TOKEN")
            .env_remove("GITHUB_TOKEN")
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true)
            .output()
            .await
            .context("failed to start host Git for Docker Sandbox preservation")?;
        if !output.status.success() {
            bail!(
                "host Git failed with {}: {}",
                output.status,
                String::from_utf8_lossy(&output.stderr).trim()
            );
        }
        Ok(())
    }
}

async fn await_capture(
    mut task: tokio::task::JoinHandle<Result<PipeCapture>>,
    stream: &str,
) -> Result<PipeCapture> {
    match tokio::time::timeout(STOP_TIMEOUT, &mut task).await {
        Ok(result) => result.with_context(|| format!("Docker Sandbox {stream} task panicked"))?,
        Err(_) => {
            task.abort();
            let _ = task.await;
            bail!("Docker Sandbox {stream} did not close after worker termination")
        }
    }
}

#[derive(Debug)]
struct PipeCapture {
    text: String,
    lines: usize,
    malformed: Option<String>,
}

async fn capture_pipe<R: AsyncRead + Unpin>(
    reader: R,
    json_activity: bool,
    stream: bool,
    observations: watch::Sender<RuntimeObservation>,
) -> Result<PipeCapture> {
    if json_activity {
        return capture_json_activity(reader, stream, observations).await;
    }
    let mut reader = reader;
    let mut bytes = Vec::new();
    let mut chunk = [0_u8; 8192];
    loop {
        let read = reader.read(&mut chunk).await?;
        if read == 0 {
            break;
        }
        if stream {
            write_stderr_best_effort(&chunk[..read]);
        }
        append_bounded(&mut bytes, &chunk[..read], MAX_STDERR_BYTES);
    }
    let text = String::from_utf8_lossy(&bytes).into_owned();
    if !text.is_empty() {
        observations.send_modify(|observation| {
            observation.activity = Some(format!(
                "Docker Sandbox stderr: {}",
                sanitize_for_storage(&text)
            ));
            observation.sequence = observation.sequence.saturating_add(1);
        });
    }
    Ok(PipeCapture {
        text,
        lines: 0,
        malformed: None,
    })
}

async fn capture_json_activity<R: AsyncRead + Unpin>(
    mut reader: R,
    stream: bool,
    observations: watch::Sender<RuntimeObservation>,
) -> Result<PipeCapture> {
    let mut bytes = Vec::new();
    let mut pending = Vec::new();
    let mut oversized = false;
    let mut malformed = None;
    let mut lines = 0;
    let mut chunk = [0_u8; 8192];
    loop {
        let read = reader.read(&mut chunk).await?;
        if read == 0 {
            break;
        }
        if stream {
            write_stdout_best_effort(&chunk[..read]);
        }
        append_bounded(&mut bytes, &chunk[..read], MAX_STREAM_BYTES);
        for byte in &chunk[..read] {
            if *byte == b'\n' {
                record_activity_line(
                    &pending,
                    oversized,
                    &observations,
                    &mut lines,
                    &mut malformed,
                );
                pending.clear();
                oversized = false;
            } else if pending.len() < MAX_STREAM_BYTES {
                pending.push(*byte);
            } else {
                oversized = true;
            }
        }
    }
    if !pending.is_empty() || oversized {
        record_activity_line(
            &pending,
            oversized,
            &observations,
            &mut lines,
            &mut malformed,
        );
    }
    Ok(PipeCapture {
        text: String::from_utf8_lossy(&bytes).into_owned(),
        lines,
        malformed,
    })
}

fn record_activity_line(
    line: &[u8],
    oversized: bool,
    observations: &watch::Sender<RuntimeObservation>,
    lines: &mut usize,
    malformed: &mut Option<String>,
) {
    if line.iter().all(u8::is_ascii_whitespace) && !oversized {
        return;
    }
    *lines += 1;
    if oversized {
        malformed.get_or_insert_with(|| {
            format!("Codex activity line exceeded {MAX_STREAM_BYTES} bytes")
        });
        return;
    }
    match serde_json::from_slice::<Value>(line) {
        Ok(event) => observe_event(observations, &event),
        Err(error) if malformed.is_none() => *malformed = Some(error.to_string()),
        Err(_) => {}
    }
}

fn observe_event(observations: &watch::Sender<RuntimeObservation>, event: &Value) {
    let kind = event
        .get("type")
        .and_then(Value::as_str)
        .unwrap_or("unknown");
    observations.send_modify(|observation| {
        let mut activity = observation.activity.take().unwrap_or_default();
        activity.push_str(&format!("Codex event: {}\n", sanitize_for_storage(kind)));
        observation.activity = Some(truncate_tail(&activity, MAX_STREAM_BYTES));
        if let Some(pull_request) = find_pull_request_url(event) {
            observation.pull_request = Some(pull_request);
        }
        observation.sequence = observation.sequence.saturating_add(1);
    });
}

fn append_bounded(target: &mut Vec<u8>, chunk: &[u8], maximum: usize) {
    target.extend_from_slice(chunk);
    if target.len() > maximum {
        target.drain(..target.len() - maximum);
    }
}

fn truncate_tail(value: &str, maximum: usize) -> String {
    if value.len() <= maximum {
        return value.to_owned();
    }
    let mut start = value.len() - maximum;
    while !value.is_char_boundary(start) {
        start += 1;
    }
    value[start..].to_owned()
}

fn truncate_head(value: &str, maximum: usize) -> String {
    if value.len() <= maximum {
        return value.to_owned();
    }
    let mut end = maximum;
    while !value.is_char_boundary(end) {
        end -= 1;
    }
    value[..end].to_owned()
}

#[cfg(unix)]
fn exit_status(code: i32) -> ExitStatus {
    use std::os::unix::process::ExitStatusExt;
    ExitStatus::from_raw(code << 8)
}

#[cfg(all(test, unix))]
mod tests {
    use std::fs;
    use std::os::unix::fs::PermissionsExt;
    use std::sync::{Arc, Mutex};

    use super::*;
    use crate::runtime::observation_channel;

    fn executable(path: &Path, contents: &str) {
        fs::write(path, contents).unwrap();
        let mut permissions = fs::metadata(path).unwrap().permissions();
        permissions.set_mode(0o700);
        fs::set_permissions(path, permissions).unwrap();
    }

    fn worker(executable: &Path) -> SandboxWorker {
        SandboxWorker::new(
            WorkerConfig {
                template: "docker/sandbox-templates:codex".to_owned(),
                memory: "4g".to_owned(),
                cpus: 2,
                github_token_env: "FACTORY_GITHUB_TOKEN".to_owned(),
            },
            "test-instance",
        )
        .with_executable(executable)
        .with_activity_streaming(false)
    }

    #[tokio::test]
    async fn validates_cli_and_proxy_managed_credentials() {
        let temp = tempfile::tempdir().unwrap();
        let sbx = temp.path().join("sbx");
        executable(
            &sbx,
            r#"#!/bin/sh
set -eu
case "$1 ${2:-}" in
  'version ') printf '%s\n' 'sbx version 0.35.0' ;;
  'secret ls')
    [ "$3" = --global ]
    [ "$4" = --service ]
    printf '%s\n' "global service $5"
    ;;
  *) exit 1 ;;
esac
"#,
        );

        let version = worker(&sbx)
            .validate(&CancellationToken::new())
            .await
            .unwrap();

        assert_eq!(version, "sbx version 0.35.0");
    }

    #[tokio::test]
    async fn creates_sandbox_for_dedicated_clone_before_exec_and_removes_it() {
        let temp = tempfile::tempdir().unwrap();
        let log = temp.path().join("sbx.log");
        let sbx = temp.path().join("sbx");
        let clone = temp.path().join("clone");
        fs::create_dir(&clone).unwrap();
        executable(
            &sbx,
            &format!(
                r#"#!/bin/sh
set -eu
printf '%s\n' "$*" >> '{}'
case "$1 ${{2:-}}" in
  'version ') printf '%s\n' 'sbx version 0.35.0' ;;
  'create --quiet') ;;
  'exec --interactive')
    cat >/dev/null
    printf '%s\n' '{{"type":"item.completed"}}'
    ;;
  'exec factory-test-instance-11')
    if [ "$3" = cat ]; then printf '%s' 'finished'; else printf '%s' 'saved logs'; fi
    ;;
  'rm --force') ;;
  *) printf 'unexpected command: %s\n' "$*" >&2; exit 1 ;;
esac
"#,
                log.display()
            ),
        );
        let worker = worker(&sbx);
        let (observations, _) = observation_channel();
        let callback_log = log.clone();
        let (result, identity) = worker
            .run(
                11,
                &clone,
                "work on the issue",
                Duration::from_secs(5),
                CancellationToken::new(),
                observations,
                move |_, state| {
                    let commands = fs::read_to_string(&callback_log)?;
                    if state == "creating" {
                        assert!(!commands.contains("create --quiet"));
                    } else {
                        assert_eq!(state, "created");
                        assert!(commands.contains("create --quiet"));
                        assert!(!commands.contains("exec --interactive"));
                    }
                    Ok(())
                },
            )
            .await
            .unwrap();
        assert!(result.succeeded());
        assert_eq!(result.final_response, "finished");
        assert_eq!(identity.name, "factory-test-instance-11");
        worker.remove_sandbox(&identity.name).await.unwrap();

        let commands = fs::read_to_string(log).unwrap();
        let create = commands
            .lines()
            .find(|line| line.starts_with("create --quiet"))
            .unwrap();
        assert!(create.contains("--clone"));
        assert!(create.contains("--cpus 2"));
        assert!(create.contains("--memory 4g"));
        assert!(create.contains("--template docker/sandbox-templates:codex"));
        assert!(create.contains(clone.canonicalize().unwrap().to_str().unwrap()));
        assert!(commands.contains("exec --interactive factory-test-instance-11"));
        assert!(commands.contains("codex exec --ephemeral --ignore-user-config"));
        assert!(commands.contains("rm --force factory-test-instance-11"));
    }

    #[tokio::test]
    async fn cancellation_stops_the_sandbox() {
        let temp = tempfile::tempdir().unwrap();
        let log = temp.path().join("sbx.log");
        let stopped = temp.path().join("stopped");
        let started = temp.path().join("started");
        let clone = temp.path().join("clone");
        fs::create_dir(&clone).unwrap();
        let sbx = temp.path().join("sbx");
        executable(
            &sbx,
            &format!(
                r#"#!/bin/sh
set -eu
printf '%s\n' "$*" >> '{}'
case "$1 ${{2:-}}" in
  'version ') printf '%s\n' 'sbx version 0.35.0' ;;
  'create --quiet') ;;
  'exec --interactive')
    cat >/dev/null
    touch '{}'
    while [ ! -f '{}' ]; do sleep 0.01; done
    exit 1
    ;;
  'stop factory-test-instance-12') touch '{}' ;;
  'exec factory-test-instance-12') printf '%s' 'cancelled logs' ;;
  'rm --force') ;;
  *) exit 1 ;;
esac
"#,
                log.display(),
                started.display(),
                stopped.display(),
                stopped.display(),
            ),
        );
        let cancellation = CancellationToken::new();
        let trigger = cancellation.clone();
        tokio::spawn(async move {
            while !started.exists() {
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
            trigger.cancel();
        });
        let (observations, _) = observation_channel();

        let (result, identity) = worker(&sbx)
            .run(
                12,
                &clone,
                "work on the issue",
                Duration::from_secs(5),
                cancellation,
                observations,
                |_, _| Ok(()),
            )
            .await
            .unwrap();

        assert_eq!(result.termination, Termination::Cancelled);
        assert!(
            fs::read_to_string(log)
                .unwrap()
                .contains("stop factory-test-instance-12")
        );
        worker(&sbx).remove_sandbox(&identity.name).await.unwrap();
    }

    #[tokio::test]
    async fn snapshots_and_fetches_private_clone_changes_before_removal() {
        let temp = tempfile::tempdir().unwrap();
        let host = temp.path().join("host");
        let private = temp.path().join("private");
        fs::create_dir(&host).unwrap();
        assert!(
            std::process::Command::new("git")
                .args(["init", "--quiet"])
                .current_dir(&host)
                .status()
                .unwrap()
                .success()
        );
        fs::write(host.join("tracked"), "base\n").unwrap();
        assert!(
            std::process::Command::new("git")
                .args([
                    "-c",
                    "user.name=Factory",
                    "-c",
                    "user.email=factory@localhost",
                    "add",
                    ".",
                ])
                .current_dir(&host)
                .status()
                .unwrap()
                .success()
        );
        assert!(
            std::process::Command::new("git")
                .args([
                    "-c",
                    "user.name=Factory",
                    "-c",
                    "user.email=factory@localhost",
                    "commit",
                    "--quiet",
                    "-m",
                    "base",
                ])
                .current_dir(&host)
                .status()
                .unwrap()
                .success()
        );
        assert!(
            std::process::Command::new("git")
                .args([
                    "clone",
                    "--quiet",
                    host.to_str().unwrap(),
                    private.to_str().unwrap()
                ])
                .status()
                .unwrap()
                .success()
        );
        fs::write(private.join("tracked"), "changed\n").unwrap();
        fs::write(private.join("untracked"), "new\n").unwrap();
        assert!(
            std::process::Command::new("git")
                .args([
                    "remote",
                    "add",
                    "sandbox-factory-test-instance-14",
                    private.to_str().unwrap(),
                ])
                .current_dir(&host)
                .status()
                .unwrap()
                .success()
        );
        let sbx = temp.path().join("sbx");
        executable(
            &sbx,
            &format!(
                r#"#!/bin/sh
set -eu
if [ "$1 $2" = 'exec factory-test-instance-14' ]; then
  cd '{}'
  exec bash -c "$5"
fi
exit 1
"#,
                private.display()
            ),
        );

        worker(&sbx)
            .preserve_workspace("factory-test-instance-14", &host)
            .await
            .unwrap();

        assert_eq!(
            fs::read_to_string(host.join("tracked")).unwrap(),
            "changed\n"
        );
        assert_eq!(fs::read_to_string(host.join("untracked")).unwrap(), "new\n");
    }

    #[tokio::test]
    async fn reserves_identity_before_cancellable_creation() {
        let temp = tempfile::tempdir().unwrap();
        let clone = temp.path().join("clone");
        fs::create_dir(&clone).unwrap();
        let creating = temp.path().join("creating");
        let sbx = temp.path().join("sbx");
        executable(
            &sbx,
            &format!(
                r#"#!/bin/sh
set -eu
if [ "$1" = version ]; then printf '%s\n' 'sbx version 0.35.0'; exit 0; fi
if [ "$1" = create ]; then touch '{}'; sleep 60; exit 0; fi
exit 1
"#,
                creating.display()
            ),
        );
        let states = Arc::new(Mutex::new(Vec::new()));
        let observed_states = states.clone();
        let cancellation = CancellationToken::new();
        let trigger = cancellation.clone();
        tokio::spawn(async move {
            while !creating.exists() {
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
            trigger.cancel();
        });
        let (observations, _) = observation_channel();

        let error = worker(&sbx)
            .run(
                13,
                &clone,
                "work on the issue",
                Duration::from_secs(5),
                cancellation,
                observations,
                move |_, state| {
                    observed_states.lock().unwrap().push(state.to_owned());
                    Ok(())
                },
            )
            .await
            .unwrap_err();

        assert_eq!(*states.lock().unwrap(), ["creating"]);
        let failure = error.downcast_ref::<SandboxRunFailure>().unwrap();
        assert_eq!(failure.identity.name, "factory-test-instance-13");
    }

    #[tokio::test]
    async fn lists_only_sandboxes_owned_by_this_factory_instance() {
        let temp = tempfile::tempdir().unwrap();
        let sbx = temp.path().join("sbx");
        executable(
            &sbx,
            r#"#!/bin/sh
set -eu
printf '%s\n' factory-test-instance-11 factory-other-instance-12 codex-project
"#,
        );

        let owned = worker(&sbx)
            .owned_sandboxes(&CancellationToken::new())
            .await
            .unwrap();

        assert_eq!(owned.len(), 1);
        assert_eq!(owned[0].run_id, 11);
    }
}
