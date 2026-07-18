use std::fmt;
use std::fs::File;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::process::{ExitStatus, Stdio};
use std::sync::OnceLock;
use std::sync::mpsc::{Receiver, SyncSender, sync_channel};
use std::time::{Duration, Instant};

use anyhow::{Context, Result, bail};
use serde_json::Value;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWriteExt};
use tokio::process::{Child, Command};
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

#[derive(Debug, Clone)]
pub struct CodexRuntime {
    executable: PathBuf,
    health_timeout: Duration,
    stream_activity: bool,
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
        let output_path = tempfile::NamedTempFile::new()
            .context("failed to create Codex final-response file")?
            .into_temp_path();
        let mut command = Command::new(&self.executable);
        command
            .arg("exec")
            .arg("--json")
            .arg("--color")
            .arg("never")
            .arg("--output-last-message")
            .arg(&output_path)
            .arg("-")
            .current_dir(working_directory)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        configure_process_group(&mut command);

        let started = Instant::now();
        let deadline = TokioInstant::now() + run_timeout;
        let mut child = spawn_with_retry(&mut command).await.with_context(|| {
            format!("failed to start Codex CLI at {}", self.executable.display())
        })?;
        let process_id = child.id();
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
        let stdout_task = tokio::spawn(read_stdout(stdout, stdout_stream));
        let stderr_task = tokio::spawn(read_stderr(stderr, Some(OutputStream::Stderr)));

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
            (terminate(&mut child, process_id).await?, termination)
        } else {
            tokio::select! {
                biased;
                () = cancellation.cancelled() => {
                    (terminate(&mut child, process_id).await?, Termination::Cancelled)
                }
                () = sleep_until(deadline) => {
                    (terminate(&mut child, process_id).await?, Termination::TimedOut)
                }
                status = wait_for_clean_exit(&mut child, process_id) => {
                    (status.context("failed while waiting for Codex")?, Termination::Exited)
                }
            }
        };

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
                terminate(&mut child, process_id).await?;
                join_reader(stdout_task, "health-check stdout").await?;
                join_reader(stderr_task, "health-check stderr").await?;
                return Err(RuntimeCancelled.into());
            }
            () = sleep_until(deadline) => {
                terminate(&mut child, process_id).await?;
                join_reader(stdout_task, "health-check stdout").await?;
                join_reader(stderr_task, "health-check stderr").await?;
                bail!(
                    "Codex {check} check timed out after {}",
                    humantime::format_duration(self.health_timeout)
                );
            }
            status = wait_for_clean_exit(&mut child, process_id) => {
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
                    capture_activity_line(&line, &mut capture);
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
            capture_activity_line(&line, &mut capture);
        }
    }
    Ok(capture)
}

fn capture_activity_line(line: &[u8], capture: &mut ActivityCapture) {
    let line = line.strip_suffix(b"\r").unwrap_or(line);
    match serde_json::from_slice::<Value>(line) {
        Ok(event) => {
            if capture.thread_id.is_none()
                && let Some(thread_id) = event.get("thread_id").and_then(Value::as_str)
            {
                if thread_id.len() <= MAX_THREAD_ID_BYTES {
                    capture.thread_id = Some(thread_id.to_owned());
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

async fn read_stderr(
    mut stderr: tokio::process::ChildStderr,
    activity_stream: Option<OutputStream>,
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
        append_bounded(&mut tail, &chunk[..read], MAX_STDERR_BYTES);
    }
    Ok(String::from_utf8_lossy(&tail).into_owned())
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
    stdout_task: JoinHandle<Result<ActivityCapture>>,
    stderr_task: JoinHandle<Result<String>>,
) {
    let _ = terminate(child, process_id).await;
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
async fn wait_for_clean_exit(child: &mut Child, process_id: Option<u32>) -> Result<ExitStatus> {
    let process_id = process_id.context("Codex process has no process ID")?;
    observe_exit(process_id).await?;
    cleanup_descendants(Some(process_id))?;
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
async fn wait_for_clean_exit(child: &mut Child, _process_id: Option<u32>) -> Result<ExitStatus> {
    child
        .wait()
        .await
        .context("failed to wait for Codex process")
}

#[cfg(unix)]
async fn terminate(child: &mut Child, process_id: Option<u32>) -> Result<ExitStatus> {
    let process_id = process_id.context("Codex process has no process ID")?;
    signal_process_group(Some(process_id), false)?;
    match timeout(TERMINATION_GRACE, observe_exit(process_id)).await {
        Ok(observed) => observed?,
        Err(_) => {
            signal_process_group(Some(process_id), true)?;
            observe_exit(process_id).await?;
        }
    }
    cleanup_descendants(Some(process_id))?;
    child
        .wait()
        .await
        .context("failed while reaping cancelled Codex process")
}

#[cfg(not(unix))]
async fn terminate(child: &mut Child, _process_id: Option<u32>) -> Result<ExitStatus> {
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
    let signal = if force {
        Signal::SIGKILL
    } else {
        Signal::SIGTERM
    };
    match killpg(Pid::from_raw(process_id as i32), signal) {
        Ok(()) | Err(Errno::ESRCH) => Ok(()),
        Err(error) => Err(error).context("failed to signal Codex process group"),
    }
}
