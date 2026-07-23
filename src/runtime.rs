use std::fmt;
use std::fs::File;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::process::{ExitStatus, Stdio};
use std::sync::OnceLock;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::{Receiver, SyncSender, sync_channel};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};
use serde_json::Value;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWriteExt};
use tokio::process::{Child, Command};
use tokio::sync::watch;
use tokio::task::JoinHandle;
use tokio::time::{Instant as TokioInstant, sleep_until, timeout};
use tokio_util::sync::CancellationToken;

const DEFAULT_HEALTH_TIMEOUT: Duration = Duration::from_secs(10);
const TERMINATION_GRACE: Duration = Duration::from_secs(2);
const READER_GRACE: Duration = Duration::from_secs(2);
const MAX_ACTIVITY_LINE_BYTES: usize = 256 * 1024;
const MAX_THREAD_ID_BYTES: usize = 256;
const MAX_FINAL_RESPONSE_BYTES: usize = 256 * 1024;
const MAX_STDERR_BYTES: usize = 64 * 1024;
const MAX_OBSERVED_ACTIVITY_BYTES: usize = 64 * 1024;
const MAX_HEALTH_OUTPUT_BYTES: usize = 64 * 1024;
const OUTPUT_CHANNEL_CAPACITY: usize = 16;
const OUTPUT_ACK_TIMEOUT: Duration = Duration::from_millis(250);

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RuntimeHealth {
    pub version: String,
    pub authentication: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Termination {
    Exited,
    TimedOut,
    Cancelled,
}

#[derive(Debug)]
pub struct ExecutionResult {
    pub status: ExitStatus,
    pub termination: Termination,
    pub final_response: String,
    pub final_response_truncated: bool,
    pub thread_id: Option<String>,
    pub duration: Duration,
    pub activity_lines: usize,
    pub activity_error: Option<String>,
    pub stderr_tail: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RuntimeObservation {
    pub process_id: Option<u32>,
    pub process_identity: Option<String>,
    pub session_id: Option<String>,
    pub pull_request: Option<String>,
    pub activity: Option<String>,
    pub sequence: u64,
}

type BeforeCodexSpawn<'a> = Box<dyn FnOnce(&RuntimeObservation) -> Result<()> + Send + 'a>;

pub fn observation_channel() -> (
    watch::Sender<RuntimeObservation>,
    watch::Receiver<RuntimeObservation>,
) {
    watch::channel(RuntimeObservation::default())
}

#[derive(Debug)]
pub struct RuntimeCancelled;

impl fmt::Display for RuntimeCancelled {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("runtime check cancelled")
    }
}

impl std::error::Error for RuntimeCancelled {}

impl ExecutionResult {
    pub fn succeeded(&self) -> bool {
        self.termination == Termination::Exited && self.status.success()
    }
}

fn preparation_termination(
    cancellation: &CancellationToken,
    deadline: TokioInstant,
) -> Option<Termination> {
    if cancellation.is_cancelled() {
        Some(Termination::Cancelled)
    } else if TokioInstant::now() >= deadline {
        Some(Termination::TimedOut)
    } else {
        None
    }
}

#[cfg(unix)]
fn preparation_result(termination: Termination, started: Instant) -> ExecutionResult {
    use std::os::unix::process::ExitStatusExt;

    ExecutionResult {
        status: ExitStatus::from_raw(1 << 8),
        termination,
        final_response: String::new(),
        final_response_truncated: false,
        thread_id: None,
        duration: started.elapsed(),
        activity_lines: 0,
        activity_error: None,
        stderr_tail: String::new(),
    }
}

#[derive(Debug, Clone)]
pub struct CodexRuntime {
    executable: PathBuf,
    health_timeout: Duration,
    stream_activity: bool,
}

struct RunProcessGroup {
    #[cfg(unix)]
    anchor: Child,
    process_id: Option<u32>,
    process_identity: Option<String>,
}

impl RunProcessGroup {
    async fn start() -> Result<Self> {
        #[cfg(unix)]
        {
            let anchor_token = process_group_anchor_token();
            let mut command = Command::new("/bin/sh");
            command
                .args([
                    "-c",
                    "trap '' TERM; while :; do sleep 3600; done",
                    &anchor_token,
                ])
                .stdin(Stdio::null())
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .kill_on_drop(true);
            configure_process_group(&mut command);
            let mut anchor = spawn_with_retry(&mut command)
                .await
                .context("failed to start Codex process-group anchor")?;
            let process_id = anchor
                .id()
                .filter(|process_id| *process_id > 0)
                .context("Codex process-group anchor has no positive process ID")?;
            let Some(process_identity) = process_identity(process_id) else {
                let _ = stop_process_group_anchor(&mut anchor, process_id).await;
                bail!("could not establish Codex process-group anchor identity");
            };
            Ok(Self {
                anchor,
                process_id: Some(process_id),
                process_identity: Some(process_identity),
            })
        }
        #[cfg(not(unix))]
        {
            Ok(Self {
                process_id: None,
                process_identity: None,
            })
        }
    }

    fn configure(&self, command: &mut Command) -> Result<()> {
        #[cfg(unix)]
        {
            let process_id = self
                .process_id
                .context("Codex process-group anchor has no process ID")?;
            let process_id = i32::try_from(process_id)
                .context("Codex process-group anchor ID exceeds platform range")?;
            command.process_group(process_id);
        }
        #[cfg(not(unix))]
        let _ = command;
        Ok(())
    }

    async fn stop(&mut self) -> Result<()> {
        #[cfg(unix)]
        if let Some(process_id) = self.process_id {
            stop_process_group_anchor(&mut self.anchor, process_id).await?;
            self.process_id = None;
            self.process_identity = None;
        }
        Ok(())
    }

    async fn reap(&mut self) -> Result<()> {
        #[cfg(unix)]
        {
            self.anchor
                .wait()
                .await
                .context("failed to reap Codex process-group anchor")?;
            self.process_id = None;
            self.process_identity = None;
        }
        Ok(())
    }
}

impl Drop for RunProcessGroup {
    fn drop(&mut self) {
        #[cfg(unix)]
        if let Some(process_id) = self.process_id {
            let _ = signal_process_group(Some(process_id), true);
        }
    }
}

impl Default for CodexRuntime {
    fn default() -> Self {
        Self::new("codex")
    }
}

impl CodexRuntime {
    pub fn new(executable: impl Into<PathBuf>) -> Self {
        Self {
            executable: executable.into(),
            health_timeout: DEFAULT_HEALTH_TIMEOUT,
            stream_activity: true,
        }
    }

    pub fn with_health_timeout(mut self, health_timeout: Duration) -> Self {
        self.health_timeout = health_timeout;
        self
    }

    pub fn with_activity_streaming(mut self, stream_activity: bool) -> Self {
        self.stream_activity = stream_activity;
        self
    }

    pub async fn health_check(&self) -> Result<RuntimeHealth> {
        self.health_check_with_cancellation(CancellationToken::new())
            .await
    }

    pub async fn health_check_with_cancellation(
        &self,
        cancellation: CancellationToken,
    ) -> Result<RuntimeHealth> {
        let version = self
            .check_command(["--version"], "version", &cancellation)
            .await
            .with_context(|| {
                format!(
                    "Codex CLI health check failed; install codex or make {} executable",
                    self.executable.display()
                )
            })?;
        let authentication = self
            .check_command(["login", "status"], "authentication", &cancellation)
            .await
            .context(
                "Codex authentication check failed; run codex login and verify codex login status",
            )?;
        if !authentication.to_ascii_lowercase().contains("chatgpt") {
            bail!(
                "Codex must use ChatGPT subscription authentication, not an API key; run codex logout followed by codex login"
            );
        }
        Ok(RuntimeHealth {
            version,
            authentication,
        })
    }

    pub async fn run(
        &self,
        prompt: &str,
        working_directory: &Path,
        run_timeout: Duration,
        cancellation: CancellationToken,
    ) -> Result<ExecutionResult> {
        let (observations, _receiver) = observation_channel();
        self.run_with_session(
            prompt,
            working_directory,
            run_timeout,
            cancellation,
            None,
            observations,
        )
        .await
    }

    pub async fn run_with_session(
        &self,
        prompt: &str,
        working_directory: &Path,
        run_timeout: Duration,
        cancellation: CancellationToken,
        resume_session: Option<&str>,
        observations: watch::Sender<RuntimeObservation>,
    ) -> Result<ExecutionResult> {
        self.run_with_session_inner(
            prompt,
            working_directory,
            run_timeout,
            cancellation,
            resume_session,
            observations,
            None,
        )
        .await
    }

    #[allow(clippy::too_many_arguments)]
    pub async fn run_with_session_supervised<F>(
        &self,
        prompt: &str,
        working_directory: &Path,
        run_timeout: Duration,
        cancellation: CancellationToken,
        resume_session: Option<&str>,
        observations: watch::Sender<RuntimeObservation>,
        before_spawn: F,
    ) -> Result<ExecutionResult>
    where
        F: FnOnce(&RuntimeObservation) -> Result<()> + Send,
    {
        self.run_with_session_inner(
            prompt,
            working_directory,
            run_timeout,
            cancellation,
            resume_session,
            observations,
            Some(Box::new(before_spawn)),
        )
        .await
    }

    #[allow(clippy::too_many_arguments)]
    async fn run_with_session_inner(
        &self,
        prompt: &str,
        working_directory: &Path,
        run_timeout: Duration,
        cancellation: CancellationToken,
        resume_session: Option<&str>,
        observations: watch::Sender<RuntimeObservation>,
        before_spawn: Option<BeforeCodexSpawn<'_>>,
    ) -> Result<ExecutionResult> {
        let started = Instant::now();
        let deadline = TokioInstant::now() + run_timeout;
        if let Some(termination) = preparation_termination(&cancellation, deadline) {
            return Ok(preparation_result(termination, started));
        }
        let output_path = tempfile::NamedTempFile::new()
            .context("failed to create Codex final-response file")?
            .into_temp_path();
        let mut command = Command::new(&self.executable);
        command.arg("exec");
        if let Some(session_id) = resume_session {
            command.arg("resume");
            command
                .arg("--json")
                .arg("--output-last-message")
                .arg(&output_path)
                .arg(session_id)
                .arg("-");
        } else {
            command
                .arg("--json")
                .arg("--color")
                .arg("never")
                .arg("--output-last-message")
                .arg(&output_path)
                .arg("-");
        }
        command
            .current_dir(working_directory)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        let mut process_group = RunProcessGroup::start().await?;
        process_group.configure(&mut command)?;
        if let Some(termination) = preparation_termination(&cancellation, deadline) {
            process_group.stop().await?;
            return Ok(preparation_result(termination, started));
        }
        let anchor_observation = RuntimeObservation {
            process_id: process_group.process_id,
            process_identity: process_group.process_identity.clone(),
            sequence: 1,
            ..RuntimeObservation::default()
        };
        if let Some(before_spawn) = before_spawn
            && let Err(error) = before_spawn(&anchor_observation)
        {
            process_group
                .stop()
                .await
                .context("failed to stop Codex process group after persistence failure")?;
            return Err(error);
        }
        observations.send_replace(anchor_observation);
        if let Some(termination) = preparation_termination(&cancellation, deadline) {
            process_group.stop().await?;
            return Ok(preparation_result(termination, started));
        }

        let spawned = tokio::select! {
            biased;
            () = cancellation.cancelled() => Err(Termination::Cancelled),
            () = sleep_until(deadline) => Err(Termination::TimedOut),
            result = spawn_with_retry(&mut command) => Ok(result),
        };
        let mut child = match spawned {
            Err(termination) => {
                process_group.stop().await?;
                return Ok(preparation_result(termination, started));
            }
            Ok(Ok(child)) => child,
            Ok(Err(error)) => {
                let _ = process_group.stop().await;
                return Err(error).with_context(|| {
                    format!("failed to start Codex CLI at {}", self.executable.display())
                });
            }
        };
        let process_id = child.id();
        let observed_process_id = process_group.process_id.or(process_id);
        let observed_process_identity = process_group
            .process_identity
            .clone()
            .or_else(|| process_id.and_then(process_identity));
        update_observation(
            &observations,
            observed_process_id,
            observed_process_identity,
            None,
            None,
        );
        let mut stdin = child
            .stdin
            .take()
            .context("Codex process did not expose stdin")?;
        let stdout = child
            .stdout
            .take()
            .context("Codex process did not expose stdout")?;
        let stderr = child
            .stderr
            .take()
            .context("Codex process did not expose stderr")?;
        let stdout_stream = self.stream_activity.then_some(OutputStream::Stdout);
        let stderr_stream = self.stream_activity.then_some(OutputStream::Stderr);
        let stdout_task = tokio::spawn(read_stdout(stdout, stdout_stream, observations.clone()));
        let stderr_task = tokio::spawn(read_stderr(stderr, stderr_stream, observations.clone()));

        let deliver_prompt = async {
            stdin
                .write_all(prompt.as_bytes())
                .await
                .context("failed to send workflow prompt to Codex")?;
            stdin
                .shutdown()
                .await
                .context("failed to close Codex prompt input")?;
            drop(stdin);
            Result::<()>::Ok(())
        };
        tokio::pin!(deliver_prompt);
        let early_termination = tokio::select! {
            biased;
            () = cancellation.cancelled() => Some(Termination::Cancelled),
            () = sleep_until(deadline) => Some(Termination::TimedOut),
            delivery = &mut deliver_prompt => {
                if let Err(error) = delivery
                    && !is_broken_pipe(&error)
                {
                    cleanup_failed_delivery(
                        &mut child,
                        process_id,
                        process_group.process_id.or(process_id),
                        &mut process_group,
                        stdout_task,
                        stderr_task,
                    )
                    .await;
                    return Err(error);
                }
                None
            }
        };

        let (status, termination) = if let Some(termination) = early_termination {
            (
                terminate(
                    &mut child,
                    process_id,
                    process_group.process_id.or(process_id),
                )
                .await?,
                termination,
            )
        } else {
            tokio::select! {
                biased;
                () = cancellation.cancelled() => {
                    (terminate(&mut child, process_id, process_group.process_id.or(process_id)).await?, Termination::Cancelled)
                }
                () = sleep_until(deadline) => {
                    (terminate(&mut child, process_id, process_group.process_id.or(process_id)).await?, Termination::TimedOut)
                }
                status = wait_for_clean_exit(&mut child, process_id, process_group.process_id.or(process_id)) => {
                    (status.context("failed while waiting for Codex")?, Termination::Exited)
                }
            }
        };
        process_group.reap().await?;

        let capture = join_reader(stdout_task, "stdout").await?;
        let stderr_tail = join_reader(stderr_task, "stderr").await?;
        if termination == Termination::Exited
            && let Some(error) = &capture.malformed_line
        {
            bail!("Codex emitted malformed JSON activity: {error}");
        }
        let (final_response, final_response_truncated) =
            read_bounded(&output_path, MAX_FINAL_RESPONSE_BYTES)
                .context("failed to read Codex final response")?;

        Ok(ExecutionResult {
            status,
            termination,
            final_response,
            final_response_truncated,
            thread_id: capture.thread_id,
            duration: started.elapsed(),
            activity_lines: capture.lines,
            activity_error: capture.malformed_line,
            stderr_tail,
        })
    }

    async fn check_command<const N: usize>(
        &self,
        args: [&str; N],
        check: &str,
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let mut command = Command::new(&self.executable);
        command
            .args(args)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        configure_process_group(&mut command);
        let mut child = spawn_with_retry(&mut command)
            .await
            .with_context(|| format!("could not execute {}", self.executable.display()))?;
        let process_id = child.id();
        let stdout = child
            .stdout
            .take()
            .context("Codex health check did not expose stdout")?;
        let stderr = child
            .stderr
            .take()
            .context("Codex health check did not expose stderr")?;
        let stdout_task = tokio::spawn(read_pipe_bounded(stdout, MAX_HEALTH_OUTPUT_BYTES));
        let stderr_task = tokio::spawn(read_pipe_bounded(stderr, MAX_HEALTH_OUTPUT_BYTES));

        let deadline = TokioInstant::now() + self.health_timeout;
        let status = tokio::select! {
            biased;
            () = cancellation.cancelled() => {
                terminate(&mut child, process_id, process_id).await?;
                join_reader(stdout_task, "health-check stdout").await?;
                join_reader(stderr_task, "health-check stderr").await?;
                return Err(RuntimeCancelled.into());
            }
            () = sleep_until(deadline) => {
                terminate(&mut child, process_id, process_id).await?;
                join_reader(stdout_task, "health-check stdout").await?;
                join_reader(stderr_task, "health-check stderr").await?;
                bail!(
                    "Codex {check} check timed out after {}",
                    humantime::format_duration(self.health_timeout)
                );
            }
            status = wait_for_clean_exit(&mut child, process_id, process_id) => {
                status.context("failed while waiting for Codex health check")?
            }
        };
        let stdout =
            String::from_utf8_lossy(&join_reader(stdout_task, "health-check stdout").await?)
                .trim()
                .to_owned();
        let stderr =
            String::from_utf8_lossy(&join_reader(stderr_task, "health-check stderr").await?)
                .trim()
                .to_owned();
        if !status.success() {
            let detail = if stderr.is_empty() {
                stdout.clone()
            } else {
                stderr.clone()
            };
            bail!("Codex {check} check exited with {status}: {detail}");
        }
        let detail = if stdout.is_empty() { stderr } else { stdout };
        if detail.is_empty() {
            bail!("Codex {check} check returned no output");
        }
        Ok(detail)
    }
}

#[derive(Debug, Default)]
struct ActivityCapture {
    thread_id: Option<String>,
    malformed_line: Option<String>,
    lines: usize,
}

async fn read_stdout(
    mut stdout: tokio::process::ChildStdout,
    activity_stream: Option<OutputStream>,
    observations: watch::Sender<RuntimeObservation>,
) -> Result<ActivityCapture> {
    let mut capture = ActivityCapture::default();
    let mut chunk = [0_u8; 8192];
    let mut line = Vec::new();
    let mut discarding = false;
    loop {
        let read = stdout
            .read(&mut chunk)
            .await
            .context("failed to read Codex activity")?;
        if read == 0 {
            break;
        }
        if let Some(stream) = activity_stream {
            stream_output(stream, &chunk[..read]);
        }
        for byte in &chunk[..read] {
            if *byte == b'\n' {
                capture.lines += 1;
                if !discarding {
                    capture_activity_line(&line, &mut capture, &observations);
                }
                line.clear();
                discarding = false;
            } else if line.len() < MAX_ACTIVITY_LINE_BYTES {
                line.push(*byte);
            } else if !discarding {
                capture.malformed_line =
                    Some(format!("line exceeds {MAX_ACTIVITY_LINE_BYTES} bytes"));
                discarding = true;
            }
        }
    }
    if !line.is_empty() || discarding {
        capture.lines += 1;
        if !discarding {
            capture_activity_line(&line, &mut capture, &observations);
        }
    }
    Ok(capture)
}

fn capture_activity_line(
    line: &[u8],
    capture: &mut ActivityCapture,
    observations: &watch::Sender<RuntimeObservation>,
) {
    let line = line.strip_suffix(b"\r").unwrap_or(line);
    match serde_json::from_slice::<Value>(line) {
        Ok(event) => {
            if let Some(summary) = safe_activity_summary(&event) {
                update_observation(
                    observations,
                    None,
                    None,
                    None,
                    Some(format!("Codex progress: {summary}\n")),
                );
            }
            if let Some(pull_request) = find_pull_request_url(&event) {
                observations.send_if_modified(|observation| {
                    if observation.pull_request.as_deref() == Some(&pull_request) {
                        return false;
                    }
                    observation.pull_request = Some(pull_request.clone());
                    observation.sequence = observation.sequence.saturating_add(1);
                    true
                });
            }
            if capture.thread_id.is_none()
                && let Some(thread_id) = event.get("thread_id").and_then(Value::as_str)
            {
                if thread_id.len() <= MAX_THREAD_ID_BYTES {
                    capture.thread_id = Some(thread_id.to_owned());
                    update_observation(observations, None, None, Some(thread_id.to_owned()), None);
                } else if capture.malformed_line.is_none() {
                    capture.malformed_line =
                        Some(format!("thread ID exceeds {MAX_THREAD_ID_BYTES} bytes"));
                }
            }
        }
        Err(error) if capture.malformed_line.is_none() => {
            capture.malformed_line = Some(error.to_string());
        }
        Err(_) => {}
    }
}

pub(crate) fn safe_activity_summary(event: &Value) -> Option<&'static str> {
    let event_type = event
        .get("type")
        .and_then(Value::as_str)
        .filter(|value| {
            value.len() <= 80
                && value
                    .bytes()
                    .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b'-'))
        })
        .unwrap_or("unknown");
    match event_type {
        "thread.started" => Some("worker started"),
        "turn.started" => Some("working"),
        "turn.completed" => Some("turn completed"),
        "turn.failed" => Some("turn failed"),
        "error" => Some("runtime error"),
        "item.started" => match activity_item_type(event) {
            Some("command_execution") => Some("running a command"),
            Some("file_change") => Some("changing files"),
            Some("mcp_tool_call") => Some("using a tool"),
            Some("web_search") => Some("searching the web"),
            Some("collaboration_tool_call") => Some("coordinating a subtask"),
            Some("reasoning") => Some("reasoning"),
            Some("todo_list") => Some("updating the plan"),
            Some("agent_message") => Some("reporting progress"),
            _ => None,
        },
        "item.updated" => match activity_item_type(event) {
            Some("command_execution") => Some("command still running"),
            Some("file_change") => Some("changing files"),
            Some("mcp_tool_call") => Some("tool still running"),
            Some("web_search") => Some("web search in progress"),
            Some("collaboration_tool_call") => Some("subtask in progress"),
            Some("todo_list") => Some("plan updated"),
            Some("agent_message") => Some("reporting progress"),
            Some("reasoning") => Some("reasoning"),
            _ => None,
        },
        "item.completed" => match activity_item_type(event) {
            Some("command_execution") => Some("command finished"),
            Some("file_change") => Some("files changed"),
            Some("web_search") => Some("web search finished"),
            Some("todo_list") => Some("plan updated"),
            Some("agent_message") => Some("progress reported"),
            Some("reasoning" | "mcp_tool_call" | "collaboration_tool_call") => None,
            _ => None,
        },
        _ => None,
    }
}

fn activity_item_type(event: &Value) -> Option<&str> {
    event
        .get("item")
        .and_then(Value::as_object)
        .and_then(|item| item.get("type"))
        .and_then(Value::as_str)
}

pub(crate) fn find_pull_request_url(value: &Value) -> Option<String> {
    match value {
        Value::String(value) => value.split_whitespace().find_map(|word| {
            let candidate = word.trim_matches(|character: char| {
                matches!(
                    character,
                    '(' | ')' | '[' | ']' | ',' | '.' | ';' | '\'' | '"'
                )
            });
            let mut parts = candidate.strip_prefix("https://github.com/")?.split('/');
            let owner = parts.next()?;
            let repository = parts.next()?;
            let pull = parts.next()?;
            let number = parts.next()?;
            (!owner.is_empty()
                && !repository.is_empty()
                && pull == "pull"
                && number.bytes().all(|byte| byte.is_ascii_digit()))
            .then(|| candidate.to_owned())
        }),
        Value::Array(values) => values.iter().find_map(find_pull_request_url),
        Value::Object(values) => values.values().find_map(find_pull_request_url),
        _ => None,
    }
}

async fn read_stderr(
    mut stderr: tokio::process::ChildStderr,
    activity_stream: Option<OutputStream>,
    observations: watch::Sender<RuntimeObservation>,
) -> Result<String> {
    let mut tail = Vec::new();
    let mut chunk = [0_u8; 8192];
    loop {
        let read = stderr
            .read(&mut chunk)
            .await
            .context("failed to read Codex stderr")?;
        if read == 0 {
            break;
        }
        if let Some(stream) = activity_stream {
            stream_output(stream, &chunk[..read]);
        }
        update_observation(
            &observations,
            None,
            None,
            None,
            Some(format!("Codex stderr activity: {read} bytes\n")),
        );
        append_bounded(&mut tail, &chunk[..read], MAX_STDERR_BYTES);
    }
    Ok(String::from_utf8_lossy(&tail).into_owned())
}

fn update_observation(
    observations: &watch::Sender<RuntimeObservation>,
    process_id: Option<u32>,
    process_identity: Option<String>,
    session_id: Option<String>,
    activity: Option<String>,
) {
    observations.send_if_modified(|observation| {
        let mut changed = false;
        if let Some(process_id) = process_id
            && observation.process_id != Some(process_id)
        {
            observation.process_id = Some(process_id);
            changed = true;
        }
        if let Some(process_identity) = process_identity
            && observation.process_identity.as_deref() != Some(&process_identity)
        {
            observation.process_identity = Some(process_identity);
            changed = true;
        }
        if let Some(session_id) = session_id
            && observation.session_id.as_deref() != Some(&session_id)
        {
            observation.session_id = Some(session_id);
            changed = true;
        }
        if let Some(activity) = activity {
            let activity = crate::inspection::sanitize_for_storage(&activity);
            let observed = observation.activity.get_or_insert_with(String::new);
            if observed.lines().next_back() != activity.lines().next_back() {
                observed.push_str(&activity);
                if observed.len() > MAX_OBSERVED_ACTIVITY_BYTES {
                    let mut start = observed.len() - MAX_OBSERVED_ACTIVITY_BYTES;
                    while !observed.is_char_boundary(start) {
                        start += 1;
                    }
                    observed.drain(..start);
                }
                changed = true;
            }
        }
        if changed {
            observation.sequence = observation.sequence.saturating_add(1);
        }
        changed
    });
}

#[cfg(unix)]
fn process_group_anchor_token() -> String {
    static NEXT_TOKEN: AtomicU64 = AtomicU64::new(1);
    let timestamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    format!(
        "factory-anchor-{}-{timestamp}-{}",
        std::process::id(),
        NEXT_TOKEN.fetch_add(1, Ordering::Relaxed)
    )
}

#[cfg(target_os = "macos")]
pub(crate) fn process_identity(process_id: u32) -> Option<String> {
    let process_id = i32::try_from(process_id).ok()?;
    // SAFETY: proc_pidinfo initializes proc_bsdinfo when it returns the full
    // structure size. The buffer points to valid writable storage.
    let information = unsafe {
        let mut information = std::mem::zeroed::<nix::libc::proc_bsdinfo>();
        let expected = i32::try_from(std::mem::size_of_val(&information)).ok()?;
        let written = nix::libc::proc_pidinfo(
            process_id,
            nix::libc::PROC_PIDTBSDINFO,
            0,
            (&raw mut information).cast(),
            expected,
        );
        (written == expected).then_some(information)?
    };
    Some(format!(
        "macos:{}:{}",
        information.pbi_start_tvsec, information.pbi_start_tvusec
    ))
}

#[cfg(target_os = "linux")]
pub(crate) fn process_identity(process_id: u32) -> Option<String> {
    let stat = std::fs::read_to_string(format!("/proc/{process_id}/stat")).ok()?;
    let start_ticks = stat
        .get(stat.rfind(')')? + 1..)?
        .split_whitespace()
        .nth(19)?;
    Some(format!("linux:{start_ticks}"))
}

#[cfg(all(unix, not(any(target_os = "macos", target_os = "linux"))))]
pub(crate) fn process_identity(process_id: u32) -> Option<String> {
    let process_id = i32::try_from(process_id).ok().filter(|value| *value > 0)?;
    let output = std::process::Command::new("ps")
        .env("LC_ALL", "C")
        .args([
            "-ww",
            "-o",
            "lstart=",
            "-o",
            "command=",
            "-p",
            &process_id.to_string(),
        ])
        .output()
        .ok()?;
    output
        .status
        .success()
        .then(|| String::from_utf8_lossy(&output.stdout).trim().to_owned())
        .filter(|value| !value.is_empty())
        .map(|value| format!("unix:{value}"))
}

#[cfg(not(unix))]
pub(crate) fn process_identity(_process_id: u32) -> Option<String> {
    None
}

async fn read_pipe_bounded<R>(mut reader: R, maximum: usize) -> Result<Vec<u8>>
where
    R: AsyncRead + Unpin,
{
    let mut output = Vec::new();
    let mut chunk = [0_u8; 8192];
    loop {
        let read = reader.read(&mut chunk).await?;
        if read == 0 {
            break;
        }
        append_bounded(&mut output, &chunk[..read], maximum);
    }
    Ok(output)
}

fn append_bounded(output: &mut Vec<u8>, chunk: &[u8], maximum: usize) {
    if chunk.len() >= maximum {
        output.clear();
        output.extend_from_slice(&chunk[chunk.len() - maximum..]);
        return;
    }
    let excess = output
        .len()
        .saturating_add(chunk.len())
        .saturating_sub(maximum);
    if excess > 0 {
        output.drain(..excess);
    }
    output.extend_from_slice(chunk);
}

async fn join_reader<T>(mut task: JoinHandle<Result<T>>, name: &str) -> Result<T> {
    match timeout(READER_GRACE, &mut task).await {
        Ok(result) => {
            result.with_context(|| format!("Codex {name} reader stopped unexpectedly"))?
        }
        Err(_) => {
            task.abort();
            bail!("Codex {name} reader did not close after process termination");
        }
    }
}

#[derive(Clone, Copy)]
enum OutputStream {
    Stdout,
    Stderr,
}

struct OutputMessage {
    bytes: Vec<u8>,
    acknowledgement: Option<SyncSender<()>>,
}

fn output_sender(stream: OutputStream) -> &'static SyncSender<OutputMessage> {
    static STDOUT: OnceLock<SyncSender<OutputMessage>> = OnceLock::new();
    static STDERR: OnceLock<SyncSender<OutputMessage>> = OnceLock::new();
    match stream {
        OutputStream::Stdout => {
            STDOUT.get_or_init(|| spawn_output_thread("factory-stdout", std::io::stdout()))
        }
        OutputStream::Stderr => {
            STDERR.get_or_init(|| spawn_output_thread("factory-stderr", std::io::stderr()))
        }
    }
}

fn spawn_output_thread(
    name: &str,
    writer: impl Write + Send + 'static,
) -> SyncSender<OutputMessage> {
    let (sender, receiver) = sync_channel(OUTPUT_CHANNEL_CAPACITY);
    let _ = std::thread::Builder::new()
        .name(name.to_owned())
        .spawn(move || write_output(writer, receiver));
    sender
}

fn write_output(mut writer: impl Write, receiver: Receiver<OutputMessage>) {
    while let Ok(message) = receiver.recv() {
        let delivered = writer
            .write_all(&message.bytes)
            .and_then(|()| writer.flush())
            .is_ok();
        if let Some(acknowledgement) = message.acknowledgement {
            let _ = acknowledgement.try_send(());
        }
        if !delivered {
            return;
        }
    }
}

fn stream_output(stream: OutputStream, bytes: &[u8]) {
    let _ = output_sender(stream).try_send(OutputMessage {
        bytes: bytes.to_vec(),
        acknowledgement: None,
    });
}

fn write_output_best_effort(stream: OutputStream, bytes: &[u8]) {
    let (acknowledgement, delivered) = sync_channel(1);
    if output_sender(stream)
        .try_send(OutputMessage {
            bytes: bytes.to_vec(),
            acknowledgement: Some(acknowledgement),
        })
        .is_ok()
    {
        let _ = delivered.recv_timeout(OUTPUT_ACK_TIMEOUT);
    }
}

pub fn write_stdout_best_effort(bytes: &[u8]) {
    write_output_best_effort(OutputStream::Stdout, bytes);
}

pub fn write_stderr_best_effort(bytes: &[u8]) {
    write_output_best_effort(OutputStream::Stderr, bytes);
}

async fn cleanup_failed_delivery(
    child: &mut Child,
    process_id: Option<u32>,
    process_group_id: Option<u32>,
    process_group: &mut RunProcessGroup,
    stdout_task: JoinHandle<Result<ActivityCapture>>,
    stderr_task: JoinHandle<Result<String>>,
) {
    let _ = terminate(child, process_id, process_group_id).await;
    let _ = process_group.stop().await;
    let _ = join_reader(stdout_task, "stdout").await;
    let _ = join_reader(stderr_task, "stderr").await;
}

fn is_broken_pipe(error: &anyhow::Error) -> bool {
    error
        .downcast_ref::<std::io::Error>()
        .is_some_and(|error| error.kind() == std::io::ErrorKind::BrokenPipe)
}

fn read_bounded(path: &Path, maximum: usize) -> Result<(String, bool)> {
    let file = File::open(path)?;
    let mut bytes = Vec::with_capacity(maximum.min(8192));
    file.take((maximum + 1) as u64).read_to_end(&mut bytes)?;
    let truncated = bytes.len() > maximum;
    bytes.truncate(maximum);
    Ok((String::from_utf8_lossy(&bytes).into_owned(), truncated))
}

async fn spawn_with_retry(command: &mut Command) -> std::io::Result<Child> {
    const MAX_ATTEMPTS: usize = 5;

    for attempt in 1..=MAX_ATTEMPTS {
        match command.spawn() {
            Ok(child) => return Ok(child),
            Err(error) if text_file_busy(&error) && attempt < MAX_ATTEMPTS => {
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
            Err(error) => return Err(error),
        }
    }
    unreachable!("spawn retry loop always returns")
}

#[cfg(unix)]
fn text_file_busy(error: &std::io::Error) -> bool {
    error.raw_os_error() == Some(nix::libc::ETXTBSY)
}

#[cfg(not(unix))]
fn text_file_busy(_error: &std::io::Error) -> bool {
    false
}

#[cfg(unix)]
async fn wait_for_clean_exit(
    child: &mut Child,
    process_id: Option<u32>,
    process_group_id: Option<u32>,
) -> Result<ExitStatus> {
    let process_id = process_id.context("Codex process has no process ID")?;
    observe_exit(process_id).await?;
    cleanup_descendants(process_group_id)?;
    child
        .wait()
        .await
        .context("failed to reap Codex process after observing its exit")
}

#[cfg(unix)]
async fn observe_exit(process_id: u32) -> Result<()> {
    tokio::task::spawn_blocking(move || wait_without_reaping(process_id))
        .await
        .context("Codex exit observer stopped unexpectedly")?
}

#[cfg(unix)]
fn wait_without_reaping(process_id: u32) -> Result<()> {
    use nix::libc;

    loop {
        // SAFETY: waitid initializes the provided siginfo_t. WNOWAIT leaves the
        // child waitable so Tokio can reap it after the process group is cleaned.
        let result = unsafe {
            let mut information = std::mem::zeroed();
            libc::waitid(
                libc::P_PID,
                process_id as libc::id_t,
                &mut information,
                libc::WEXITED | libc::WNOWAIT,
            )
        };
        if result == 0 {
            return Ok(());
        }
        let error = std::io::Error::last_os_error();
        if error.kind() != std::io::ErrorKind::Interrupted {
            return Err(error).context("failed to observe Codex process exit");
        }
    }
}

#[cfg(not(unix))]
async fn wait_for_clean_exit(
    child: &mut Child,
    _process_id: Option<u32>,
    _process_group_id: Option<u32>,
) -> Result<ExitStatus> {
    child
        .wait()
        .await
        .context("failed to wait for Codex process")
}

#[cfg(unix)]
async fn terminate(
    child: &mut Child,
    process_id: Option<u32>,
    process_group_id: Option<u32>,
) -> Result<ExitStatus> {
    let process_id = process_id.context("Codex process has no process ID")?;
    signal_process_group(process_group_id, false)?;
    match timeout(TERMINATION_GRACE, observe_exit(process_id)).await {
        Ok(observed) => observed?,
        Err(_) => {
            signal_process_group(process_group_id, true)?;
            observe_exit(process_id).await?;
        }
    }
    cleanup_descendants(process_group_id)?;
    child
        .wait()
        .await
        .context("failed while reaping cancelled Codex process")
}

#[cfg(not(unix))]
async fn terminate(
    child: &mut Child,
    _process_id: Option<u32>,
    _process_group_id: Option<u32>,
) -> Result<ExitStatus> {
    child
        .start_kill()
        .context("failed to cancel Codex process")?;
    child
        .wait()
        .await
        .context("failed while reaping cancelled Codex process")
}

#[cfg(unix)]
fn configure_process_group(command: &mut Command) {
    command.process_group(0);
}

#[cfg(not(unix))]
fn configure_process_group(_command: &mut Command) {}

#[cfg(unix)]
fn cleanup_descendants(process_id: Option<u32>) -> Result<()> {
    match signal_process_group(process_id, true) {
        Ok(()) => Ok(()),
        Err(error)
            if error.downcast_ref::<nix::errno::Errno>() == Some(&nix::errno::Errno::EPERM) =>
        {
            Ok(())
        }
        Err(error) => Err(error),
    }
}

#[cfg(not(unix))]
fn cleanup_descendants(_process_id: Option<u32>) -> Result<()> {
    Ok(())
}

#[cfg(unix)]
fn signal_process_group(process_id: Option<u32>, force: bool) -> Result<()> {
    use nix::errno::Errno;
    use nix::sys::signal::{Signal, killpg};
    use nix::unistd::Pid;

    let Some(process_id) = process_id else {
        return Ok(());
    };
    if process_id == 0 {
        bail!("refusing to signal process group zero");
    }
    let process_id =
        i32::try_from(process_id).context("process group ID exceeds platform range")?;
    let signal = if force {
        Signal::SIGKILL
    } else {
        Signal::SIGTERM
    };
    match killpg(Pid::from_raw(process_id), signal) {
        Ok(()) | Err(Errno::ESRCH) => Ok(()),
        Err(error) => Err(error).context("failed to signal Codex process group"),
    }
}

#[cfg(unix)]
async fn stop_process_group_anchor(anchor: &mut Child, process_id: u32) -> Result<()> {
    signal_process_group(Some(process_id), true)?;
    anchor
        .wait()
        .await
        .context("failed to reap Codex process-group anchor")?;
    Ok(())
}

#[cfg(test)]
mod observation_tests {
    use super::*;

    #[test]
    fn duplicate_activity_does_not_notify_or_advance_the_sequence() {
        let (observations, mut receiver) = observation_channel();
        let activity = "Codex progress: working\n".to_owned();

        update_observation(&observations, None, None, None, Some(activity.clone()));
        assert!(receiver.has_changed().unwrap());
        receiver.borrow_and_update();
        let sequence = receiver.borrow().sequence;

        update_observation(&observations, None, None, None, Some(activity));

        assert!(!receiver.has_changed().unwrap());
        assert_eq!(receiver.borrow().sequence, sequence);
    }

    #[test]
    fn unknown_item_types_have_no_safe_activity_summary() {
        assert_eq!(
            safe_activity_summary(&serde_json::json!({
                "type": "item.started",
                "item": { "type": "future_item", "payload": "untrusted" }
            })),
            None
        );
        assert_eq!(
            safe_activity_summary(&serde_json::json!({ "type": "item.completed" })),
            None
        );
    }
}
