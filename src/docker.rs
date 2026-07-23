use std::ffi::OsString;
use std::fmt;
use std::path::{Path, PathBuf};
use std::process::{ExitStatus, Stdio};
use std::time::{Duration, Instant};

#[cfg(unix)]
use std::os::unix::fs::MetadataExt;

use anyhow::{Context, Result, bail};
use serde_json::Value;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWriteExt};
use tokio::process::Command;
use tokio::sync::watch;
use tokio::time::Instant as TokioInstant;
use tokio_util::sync::CancellationToken;

use crate::config::WorkerConfig;
use crate::runtime::{
    ExecutionResult, RuntimeObservation, Termination, find_pull_request_url, safe_activity_summary,
    write_stderr_best_effort, write_stdout_best_effort,
};

const MAX_STREAM_BYTES: usize = 256 * 1024;
const MAX_STDERR_BYTES: usize = 64 * 1024;
const MAX_FINAL_BYTES: usize = 256 * 1024;
const STOP_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CloneMount {
    ReadOnly,
    ReadWrite,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ContainerIdentity {
    pub id: String,
    pub image_id: String,
    pub instance_id: String,
    pub run_id: i64,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RecoveredContainer {
    pub identity: ContainerIdentity,
    pub state: String,
    pub exit_code: Option<i32>,
    pub logs: String,
}

#[derive(Debug)]
pub struct DockerRunFailure {
    pub identity: ContainerIdentity,
    source: anyhow::Error,
}

impl fmt::Display for DockerRunFailure {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            formatter,
            "Docker worker {} failed: {:#}",
            self.identity.id, self.source
        )
    }
}

impl std::error::Error for DockerRunFailure {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(self.source.as_ref())
    }
}

#[derive(Debug, Clone)]
pub struct DockerWorker {
    executable: PathBuf,
    config: WorkerConfig,
    instance_id: String,
    stream_activity: bool,
}

impl DockerWorker {
    pub fn new(config: WorkerConfig, instance_id: impl Into<String>) -> Self {
        Self {
            executable: PathBuf::from("docker"),
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

    pub fn image(&self) -> &str {
        &self.config.image
    }

    pub fn instance_id(&self) -> &str {
        &self.instance_id
    }

    pub fn github_token_env(&self) -> &str {
        &self.config.github_token_env
    }

    pub fn limits_json(&self, clone: &Path) -> Result<String> {
        let (uid, gid) = host_owner(clone)?;
        Ok(serde_json::json!({
            "memory": self.config.memory,
            "cpus": self.config.cpus,
            "pids": self.config.pids,
            "read_only_root": true,
            "user": format!("{uid}:{gid}"),
        })
        .to_string())
    }

    pub async fn validate(&self, cancellation: &CancellationToken) -> Result<String> {
        self.command_output(
            &["version", "--format", "{{.Server.Version}}"],
            cancellation,
        )
        .await
        .context("Docker daemon is unavailable; start Docker and retry")?;
        let image_id = self.image_id(cancellation).await?;
        let metadata = std::fs::symlink_metadata(&self.config.codex_auth).with_context(|| {
            format!(
                "dedicated Codex auth file is missing: {}",
                self.config.codex_auth.display()
            )
        })?;
        if !metadata.file_type().is_file() || metadata.file_type().is_symlink() {
            bail!("dedicated Codex auth path must be a regular non-symlink file");
        }
        std::fs::OpenOptions::new()
            .read(true)
            .write(true)
            .open(&self.config.codex_auth)
            .with_context(|| {
                format!(
                    "dedicated Codex auth file must be writable for OAuth refresh: {}",
                    self.config.codex_auth.display()
                )
            })?;
        self.validate_codex_auth(&image_id, cancellation).await?;
        let token = std::env::var(&self.config.github_token_env).with_context(|| {
            format!(
                "GitHub token is missing; export {} for the dedicated Factory identity",
                self.config.github_token_env
            )
        })?;
        if token.trim().is_empty() {
            bail!("{} must not be empty", self.config.github_token_env);
        }
        eprintln!(
            "Warning: Factory cannot prove that {} is a least-privilege bot token. A personal token may allow the agent to merge or bypass review; use a dedicated identity and protected branches.",
            self.config.github_token_env
        );
        Ok(image_id)
    }

    async fn validate_codex_auth(
        &self,
        image_id: &str,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let auth = self.config.codex_auth.canonicalize().with_context(|| {
            format!(
                "failed to resolve auth file {}",
                self.config.codex_auth.display()
            )
        })?;
        let (uid, gid) = host_owner(&auth)?;
        let auth_mount = format!(
            "type=bind,src={},dst=/home/agent/.codex/auth.json",
            docker_path(&auth)?
        );
        let arguments = vec![
            OsString::from("run"),
            OsString::from("--rm"),
            OsString::from("--user"),
            OsString::from(format!("{uid}:{gid}")),
            OsString::from("--read-only"),
            OsString::from("--network"),
            OsString::from("none"),
            OsString::from("--cap-drop"),
            OsString::from("ALL"),
            OsString::from("--security-opt"),
            OsString::from("no-new-privileges"),
            OsString::from("--pids-limit"),
            OsString::from(self.config.pids.to_string()),
            OsString::from("--memory"),
            OsString::from(self.config.memory.clone()),
            OsString::from("--cpus"),
            OsString::from(self.config.cpus.to_string()),
            OsString::from("--tmpfs"),
            OsString::from(format!(
                "/home/agent/.codex:rw,size=64m,uid={uid},gid={gid},mode=700"
            )),
            OsString::from("--mount"),
            OsString::from(auth_mount),
            OsString::from("--env"),
            OsString::from("HOME=/home/agent"),
            OsString::from("--env"),
            OsString::from("CODEX_HOME=/home/agent/.codex"),
            OsString::from(image_id),
            OsString::from("codex"),
            OsString::from("login"),
            OsString::from("status"),
        ];
        self.command_output_os(&arguments, cancellation)
            .await
            .context(
                "Codex authentication is invalid in the configured worker image; refresh the dedicated login",
            )?;
        Ok(())
    }

    #[allow(clippy::too_many_arguments)]
    pub async fn run<F>(
        &self,
        run_id: i64,
        clone: &Path,
        mount: CloneMount,
        prompt: &str,
        run_timeout: Duration,
        cancellation: CancellationToken,
        observations: watch::Sender<RuntimeObservation>,
        before_start: F,
    ) -> Result<(ExecutionResult, ContainerIdentity)>
    where
        F: FnOnce(&ContainerIdentity) -> Result<()> + Send,
    {
        let started = Instant::now();
        let image_id = self.image_id(&cancellation).await?;
        let identity = self
            .create(run_id, clone, mount, &image_id, &cancellation)
            .await?;
        if let Err(error) = before_start(&identity) {
            let _ = self.remove(&identity.id, &CancellationToken::new()).await;
            return Err(error);
        }
        let result = self
            .start_and_wait(
                &identity,
                prompt,
                run_timeout,
                cancellation,
                observations,
                started,
            )
            .await;
        let result = match result {
            Ok(result) => result,
            Err(error) => {
                let _ = self.stop(&identity.id, &CancellationToken::new()).await;
                return Err(DockerRunFailure {
                    identity,
                    source: error,
                }
                .into());
            }
        };
        Ok((result, identity))
    }

    pub async fn remove_container(&self, id: &str) -> Result<()> {
        self.remove(id, &CancellationToken::new()).await
    }

    pub async fn container_logs(&self, id: &str) -> Result<String> {
        self.logs(id, &CancellationToken::new()).await
    }

    pub async fn owned_containers(
        &self,
        cancellation: &CancellationToken,
    ) -> Result<Vec<ContainerIdentity>> {
        let filter = format!("label=dev.factory.instance={}", self.instance_id);
        let output = self
            .command_output(
                &[
                    "ps",
                    "-a",
                    "--filter",
                    "label=dev.factory.managed=true",
                    "--filter",
                    &filter,
                    "--format",
                    "{{.ID}}\t{{.Label \"dev.factory.run\"}}",
                ],
                cancellation,
            )
            .await?;
        let mut containers = Vec::new();
        for line in output.lines().filter(|line| !line.trim().is_empty()) {
            let mut fields = line.split('\t');
            let id = fields.next().unwrap_or_default().trim();
            let run_id = fields
                .next()
                .context("owned container has no run label")?
                .parse::<i64>()
                .context("owned container has invalid run label")?;
            if id.is_empty() || run_id <= 0 {
                bail!("Docker returned an invalid owned container record");
            }
            containers.push(ContainerIdentity {
                id: id.to_owned(),
                image_id: self.inspect_image(id, cancellation).await?,
                instance_id: self.instance_id.clone(),
                run_id,
            });
        }
        Ok(containers)
    }

    pub async fn recover_container(
        &self,
        identity: &ContainerIdentity,
        cancellation: &CancellationToken,
    ) -> Result<RecoveredContainer> {
        let (state, _) = self.inspect_state(&identity.id, cancellation).await?;
        if state == "running" {
            self.stop(&identity.id, cancellation).await?;
        }
        let (state, exit_code) = self.inspect_state(&identity.id, cancellation).await?;
        let logs = self
            .logs(&identity.id, cancellation)
            .await
            .unwrap_or_default();
        Ok(RecoveredContainer {
            identity: identity.clone(),
            state,
            exit_code,
            logs,
        })
    }

    async fn image_id(&self, cancellation: &CancellationToken) -> Result<String> {
        let output = self
            .command_output(
                &[
                    "image",
                    "inspect",
                    &self.config.image,
                    "--format",
                    "{{.Id}}",
                ],
                cancellation,
            )
            .await
            .with_context(|| format!("Docker image {:?} is unavailable", self.config.image))?;
        let image_id = output.trim();
        if !image_id.starts_with("sha256:") {
            bail!("Docker returned invalid image ID {image_id:?}");
        }
        Ok(image_id.to_owned())
    }

    async fn create(
        &self,
        run_id: i64,
        clone: &Path,
        mount: CloneMount,
        image_id: &str,
        cancellation: &CancellationToken,
    ) -> Result<ContainerIdentity> {
        let clone = clone
            .canonicalize()
            .with_context(|| format!("failed to resolve clone {}", clone.display()))?;
        let auth = self.config.codex_auth.canonicalize().with_context(|| {
            format!(
                "failed to resolve auth file {}",
                self.config.codex_auth.display()
            )
        })?;
        let (uid, gid) = host_owner(&clone)?;
        let mut clone_mount = format!("type=bind,src={},dst=/workspace", docker_path(&clone)?);
        if mount == CloneMount::ReadOnly {
            clone_mount.push_str(",readonly");
        }
        let auth_mount = format!(
            "type=bind,src={},dst=/home/agent/.codex/auth.json",
            docker_path(&auth)?
        );
        let name = format!("factory-{}-{run_id}", self.instance_id);
        let run_label = format!("dev.factory.run={run_id}");
        let instance_label = format!("dev.factory.instance={}", self.instance_id);
        let memory = self.config.memory.clone();
        let cpus = self.config.cpus.to_string();
        let pids = self.config.pids.to_string();
        let arguments = vec![
            OsString::from("create"),
            OsString::from("--interactive"),
            OsString::from("--name"),
            OsString::from(name),
            OsString::from("--label"),
            OsString::from("dev.factory.managed=true"),
            OsString::from("--label"),
            OsString::from(run_label),
            OsString::from("--label"),
            OsString::from(instance_label),
            OsString::from("--user"),
            OsString::from(format!("{uid}:{gid}")),
            OsString::from("--read-only"),
            OsString::from("--cap-drop"),
            OsString::from("ALL"),
            OsString::from("--security-opt"),
            OsString::from("no-new-privileges"),
            OsString::from("--pids-limit"),
            OsString::from(pids),
            OsString::from("--memory"),
            OsString::from(memory),
            OsString::from("--cpus"),
            OsString::from(cpus),
            OsString::from("--log-opt"),
            OsString::from("max-size=10m"),
            OsString::from("--log-opt"),
            OsString::from("max-file=1"),
            OsString::from("--tmpfs"),
            OsString::from(format!("/tmp:rw,size=1g,uid={uid},gid={gid},mode=1777")),
            OsString::from("--tmpfs"),
            OsString::from(format!(
                "/home/agent/.codex:rw,size=64m,uid={uid},gid={gid},mode=700"
            )),
            OsString::from("--mount"),
            OsString::from(clone_mount),
            OsString::from("--mount"),
            OsString::from(auth_mount),
            OsString::from("--env"),
            OsString::from("GH_TOKEN"),
            OsString::from("--env"),
            OsString::from("HOME=/home/agent"),
            OsString::from("--env"),
            OsString::from("CODEX_HOME=/home/agent/.codex"),
            OsString::from("--workdir"),
            OsString::from("/workspace"),
            OsString::from(image_id),
            OsString::from("codex"),
            OsString::from("exec"),
            OsString::from("--ephemeral"),
            OsString::from("--ignore-user-config"),
            OsString::from("--sandbox"),
            OsString::from("danger-full-access"),
            OsString::from("--json"),
            OsString::from("--color"),
            OsString::from("never"),
            OsString::from("--output-last-message"),
            OsString::from("/tmp/factory-last-message"),
            OsString::from("-"),
        ];
        let output = self
            .command_output_os(&arguments, cancellation)
            .await
            .context("failed to create Docker worker")?;
        let id = output.trim();
        if id.is_empty() || !id.bytes().all(|byte| byte.is_ascii_hexdigit()) {
            bail!("Docker create returned invalid container ID {id:?}");
        }
        Ok(ContainerIdentity {
            id: id.to_owned(),
            image_id: image_id.to_owned(),
            instance_id: self.instance_id.clone(),
            run_id,
        })
    }

    #[allow(clippy::too_many_arguments)]
    async fn start_and_wait(
        &self,
        identity: &ContainerIdentity,
        prompt: &str,
        run_timeout: Duration,
        cancellation: CancellationToken,
        observations: watch::Sender<RuntimeObservation>,
        started: Instant,
    ) -> Result<ExecutionResult> {
        let mut command = Command::new(&self.executable);
        command
            .args(["start", "--attach", "--interactive", &identity.id])
            .env(
                &self.config.github_token_env,
                std::env::var(&self.config.github_token_env)?,
            )
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        let mut child = command.spawn().context("failed to start Docker worker")?;
        let mut stdin = child.stdin.take().context("Docker attach has no stdin")?;
        let stdout = child.stdout.take().context("Docker attach has no stdout")?;
        let stderr = child.stderr.take().context("Docker attach has no stderr")?;
        let stream = self.stream_activity;
        let stdout_task = tokio::spawn(capture_pipe(stdout, true, stream, observations.clone()));
        let stderr_task = tokio::spawn(capture_pipe(stderr, false, stream, observations));
        stdin.write_all(prompt.as_bytes()).await?;
        stdin.shutdown().await?;
        drop(stdin);
        let deadline = TokioInstant::now() + run_timeout;
        let termination = tokio::select! {
            () = cancellation.cancelled() => {
                self.stop(&identity.id, &CancellationToken::new()).await?;
                Termination::Cancelled
            }
            () = tokio::time::sleep_until(deadline) => {
                self.stop(&identity.id, &CancellationToken::new()).await?;
                Termination::TimedOut
            }
            status = child.wait() => {
                status.context("failed to wait for Docker attach")?;
                Termination::Exited
            }
        };
        if termination != Termination::Exited
            && tokio::time::timeout(STOP_TIMEOUT, child.wait())
                .await
                .is_err()
        {
            child.kill().await.ok();
            child.wait().await.ok();
        }
        let stdout = await_capture(stdout_task, "stdout").await?;
        let stderr = await_capture(stderr_task, "stderr").await?;
        let (_, exit_code) = self
            .inspect_state(&identity.id, &CancellationToken::new())
            .await?;
        let exit_code = exit_code.unwrap_or(1);
        let final_response = self
            .copy_final_response(&identity.id)
            .await
            .unwrap_or_default();
        Ok(ExecutionResult {
            status: exit_status(exit_code),
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

    async fn stop(&self, id: &str, cancellation: &CancellationToken) -> Result<()> {
        if self
            .command_output(&["stop", "--time", "2", id], cancellation)
            .await
            .is_err()
        {
            self.command_output(&["kill", id], cancellation).await?;
        }
        Ok(())
    }

    async fn remove(&self, id: &str, cancellation: &CancellationToken) -> Result<()> {
        self.command_output(&["rm", "--force", id], cancellation)
            .await?;
        Ok(())
    }

    async fn inspect_state(
        &self,
        id: &str,
        cancellation: &CancellationToken,
    ) -> Result<(String, Option<i32>)> {
        let output = self
            .command_output(
                &[
                    "inspect",
                    "--format",
                    "{{.State.Status}}\t{{.State.ExitCode}}",
                    id,
                ],
                cancellation,
            )
            .await?;
        let mut fields = output.trim().split('\t');
        let state = fields.next().unwrap_or_default().to_owned();
        let exit_code = fields.next().and_then(|value| value.parse().ok());
        Ok((state, exit_code))
    }

    async fn inspect_image(&self, id: &str, cancellation: &CancellationToken) -> Result<String> {
        let image = self
            .command_output(&["inspect", "--format", "{{.Image}}", id], cancellation)
            .await?;
        let image = image.trim();
        if !image.starts_with("sha256:") {
            bail!("Docker returned invalid container image ID {image:?}");
        }
        Ok(image.to_owned())
    }

    async fn logs(&self, id: &str, cancellation: &CancellationToken) -> Result<String> {
        let mut command = Command::new(&self.executable);
        command
            .args(["logs", "--tail", "500", id])
            .env(
                "GH_TOKEN",
                std::env::var(&self.config.github_token_env).unwrap_or_default(),
            )
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        let child = command.spawn().context("failed to start Docker logs")?;
        let output = tokio::select! {
            () = cancellation.cancelled() => bail!("Docker logs cancelled"),
            output = tokio::time::timeout(Duration::from_secs(30), child.wait_with_output()) => {
                output.context("Docker logs timed out")??
            }
        };
        if !output.status.success() {
            bail!(
                "Docker logs failed with {}: {}",
                output.status,
                String::from_utf8_lossy(&output.stderr).trim()
            );
        }
        let mut combined = output.stdout;
        if !combined.is_empty() && !output.stderr.is_empty() {
            combined.push(b'\n');
        }
        combined.extend_from_slice(&output.stderr);
        Ok(truncate_tail(
            &String::from_utf8_lossy(&combined),
            MAX_STREAM_BYTES,
        ))
    }

    async fn copy_final_response(&self, id: &str) -> Result<String> {
        let temp = tempfile::NamedTempFile::new()?;
        let source = format!("{id}:/tmp/factory-last-message");
        let destination = temp.path().to_string_lossy().into_owned();
        self.command_output(&["cp", &source, &destination], &CancellationToken::new())
            .await?;
        let bytes = std::fs::read(temp.path())?;
        Ok(String::from_utf8_lossy(&bytes[..bytes.len().min(MAX_FINAL_BYTES)]).into_owned())
    }

    async fn command_output(
        &self,
        arguments: &[&str],
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let arguments = arguments.iter().map(OsString::from).collect::<Vec<_>>();
        self.command_output_os(&arguments, cancellation).await
    }

    async fn command_output_os(
        &self,
        arguments: &[OsString],
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let mut command = Command::new(&self.executable);
        command
            .args(arguments)
            .env(
                &self.config.github_token_env,
                std::env::var(&self.config.github_token_env).unwrap_or_default(),
            )
            .env(
                "GH_TOKEN",
                std::env::var(&self.config.github_token_env).unwrap_or_default(),
            )
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        let child = command.spawn().with_context(|| {
            format!(
                "failed to start Docker CLI at {}",
                self.executable.display()
            )
        })?;
        let output = tokio::select! {
            () = cancellation.cancelled() => bail!("Docker command cancelled"),
            output = tokio::time::timeout(Duration::from_secs(30), child.wait_with_output()) => {
                output.context("Docker command timed out")??
            }
        };
        if !output.status.success() {
            bail!(
                "Docker command failed with {}: {}",
                output.status,
                String::from_utf8_lossy(&output.stderr).trim()
            );
        }
        String::from_utf8(output.stdout).context("Docker output was not UTF-8")
    }
}

async fn await_capture(
    mut task: tokio::task::JoinHandle<Result<PipeCapture>>,
    stream: &str,
) -> Result<PipeCapture> {
    match tokio::time::timeout(STOP_TIMEOUT, &mut task).await {
        Ok(result) => result.with_context(|| format!("Docker {stream} task panicked"))?,
        Err(_) => {
            task.abort();
            let _ = task.await;
            bail!("Docker {stream} did not close after worker termination")
        }
    }
}

#[cfg(unix)]
fn host_owner(path: &Path) -> Result<(u32, u32)> {
    let metadata = std::fs::metadata(path).with_context(|| {
        format!(
            "failed to inspect workspace ownership for {}",
            path.display()
        )
    })?;
    Ok((metadata.uid(), metadata.gid()))
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
        observations.send_if_modified(|observation| {
            let stderr_activity = format!("Codex stderr activity: {} bytes\n", bytes.len());
            let mut activity = observation.activity.take().unwrap_or_default();
            if activity.lines().next_back() == stderr_activity.lines().next_back() {
                observation.activity = Some(activity);
                return false;
            }
            activity.push_str(&stderr_activity);
            observation.activity = Some(truncate_tail(&activity, MAX_STREAM_BYTES));
            observation.sequence = observation.sequence.saturating_add(1);
            true
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
    let summary = safe_activity_summary(event);
    let pull_request = find_pull_request_url(event);
    if summary.is_none() && pull_request.is_none() {
        return;
    }
    observations.send_if_modified(|observation| {
        let mut changed = false;
        if let Some(summary) = summary {
            let progress = format!("Codex progress: {summary}\n");
            let mut activity = observation.activity.take().unwrap_or_default();
            if activity.lines().next_back() != progress.lines().next_back() {
                activity.push_str(&progress);
                changed = true;
            }
            observation.activity = Some(truncate_tail(&activity, MAX_STREAM_BYTES));
        }
        if let Some(pull_request) = pull_request
            && observation.pull_request.as_deref() != Some(&pull_request)
        {
            observation.pull_request = Some(pull_request);
            changed = true;
        }
        if changed {
            observation.sequence = observation.sequence.saturating_add(1);
        }
        changed
    });
}

fn docker_path(path: &Path) -> Result<String> {
    let value = path
        .to_str()
        .context("Docker mount path is not valid UTF-8")?;
    if value.contains(',') || value.contains('\n') || value.contains('\r') {
        bail!("Docker mount path contains unsupported characters");
    }
    Ok(value.to_owned())
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

#[cfg(unix)]
fn exit_status(code: i32) -> ExitStatus {
    use std::os::unix::process::ExitStatusExt;
    ExitStatus::from_raw(code << 8)
}

#[cfg(all(test, unix))]
mod tests {
    use std::fs;
    use std::io::Write;
    use std::os::unix::fs::PermissionsExt;

    use super::*;
    use crate::runtime::observation_channel;

    const TOKEN_ENV: &str = "FACTORY_DOCKER_TEST_TOKEN";

    fn executable(path: &Path, contents: &str) {
        fs::write(path, contents).unwrap();
        let mut permissions = fs::metadata(path).unwrap().permissions();
        permissions.set_mode(0o700);
        fs::set_permissions(path, permissions).unwrap();
    }

    #[tokio::test]
    async fn validates_codex_login_in_a_hardened_temporary_container() {
        let temp = tempfile::tempdir().unwrap();
        let log = temp.path().join("docker.log");
        let docker = temp.path().join("docker");
        let auth = temp.path().join("auth.json");
        fs::write(&auth, "{}").unwrap();
        executable(
            &docker,
            &format!(
                r#"#!/bin/sh
set -eu
printf '%s\n' "$*" >> '{}'
case "$1 ${{2:-}}" in
  'version --format') printf '%s\n' '27.0.0' ;;
  'image inspect') printf '%s\n' 'sha256:abcdef0123456789' ;;
  'run --rm') printf '%s\n' 'Logged in using ChatGPT' ;;
  *) printf 'unexpected fake docker command: %s\n' "$*" >&2; exit 1 ;;
esac
"#,
                log.display()
            ),
        );
        // SAFETY: all tests in this module use the same harmless test value.
        unsafe { std::env::set_var(TOKEN_ENV, "super-secret-docker-token") };
        let worker = DockerWorker::new(
            WorkerConfig {
                image: "factory-codex:test".to_owned(),
                memory: "4g".to_owned(),
                cpus: 2,
                pids: 128,
                codex_auth: auth.clone(),
                github_token_env: TOKEN_ENV.to_owned(),
            },
            "validate-instance",
        )
        .with_executable(&docker);

        let image = worker.validate(&CancellationToken::new()).await.unwrap();
        assert_eq!(image, "sha256:abcdef0123456789");
        let commands = fs::read_to_string(log).unwrap();
        let login = commands
            .lines()
            .find(|line| line.starts_with("run --rm"))
            .unwrap();
        assert!(login.contains("--read-only"));
        assert!(login.contains("--network none"));
        assert!(login.contains("--cap-drop ALL"));
        assert!(login.contains("--security-opt no-new-privileges"));
        assert!(login.contains("--pids-limit 128"));
        assert!(login.contains("--memory 4g"));
        assert!(login.contains("--cpus 2"));
        assert!(login.contains("codex login status"));
        assert!(login.contains(&format!(
            "type=bind,src={},dst=/home/agent/.codex/auth.json",
            auth.canonicalize().unwrap().display()
        )));
        assert!(!login.contains("super-secret-docker-token"));
        assert!(!login.contains("docker.sock"));
    }

    #[tokio::test]
    async fn creates_hardened_read_only_and_read_write_workers_after_persistence() {
        let temp = tempfile::tempdir().unwrap();
        let log = temp.path().join("docker.log");
        let docker = temp.path().join("docker");
        let auth = temp.path().join("auth.json");
        let read_only_clone = temp.path().join("triage");
        let read_write_clone = temp.path().join("implementation");
        fs::write(&auth, "{}").unwrap();
        fs::create_dir(&read_only_clone).unwrap();
        fs::create_dir(&read_write_clone).unwrap();
        executable(
            &docker,
            &format!(
                r#"#!/bin/sh
set -eu
printf '%s\n' "$*" >> '{}'
case "$1 ${{2:-}}" in
  'image inspect') printf '%s\n' 'sha256:abcdef0123456789' ;;
  'create --interactive') printf '%s\n' 'abcdef0123456789' ;;
  'start --attach')
    cat >/dev/null
    printf '%s\n' '{{"type":"item.completed"}}'
    ;;
  'inspect --format') printf 'exited\t0\n' ;;
  'logs --tail') printf 'container stdout'; printf 'container stderr' >&2 ;;
  'cp '*) printf '%s' 'finished' > "$3" ;;
  'rm --force') ;;
  *) printf 'unexpected fake docker command: %s\n' "$*" >&2; exit 1 ;;
esac
"#,
                log.display()
            ),
        );
        // SAFETY: this test uses a process-unique environment variable name and
        // no other test in this crate reads or writes it.
        unsafe { std::env::set_var(TOKEN_ENV, "super-secret-docker-token") };
        let worker = DockerWorker::new(
            WorkerConfig {
                image: "factory-codex:test".to_owned(),
                memory: "4g".to_owned(),
                cpus: 2,
                pids: 128,
                codex_auth: auth.clone(),
                github_token_env: TOKEN_ENV.to_owned(),
            },
            "test-instance",
        )
        .with_executable(&docker)
        .with_activity_streaming(false);

        for (run_id, clone, mount) in [
            (11, read_only_clone.as_path(), CloneMount::ReadOnly),
            (12, read_write_clone.as_path(), CloneMount::ReadWrite),
        ] {
            let (observations, _) = observation_channel();
            let callback_log = log.clone();
            let (result, identity) = worker
                .run(
                    run_id,
                    clone,
                    mount,
                    "work on the issue",
                    Duration::from_secs(5),
                    CancellationToken::new(),
                    observations,
                    move |identity| {
                        let commands = fs::read_to_string(&callback_log)?;
                        let latest_create = commands.rfind("create --interactive").unwrap();
                        assert!(
                            !commands[latest_create..]
                                .contains(&format!("start --attach --interactive {}", identity.id))
                        );
                        writeln!(
                            fs::OpenOptions::new().append(true).open(&callback_log)?,
                            "persisted {}",
                            identity.run_id
                        )?;
                        Ok(())
                    },
                )
                .await
                .unwrap();
            assert!(result.succeeded());
            assert_eq!(result.final_response, "finished");
            assert_eq!(identity.run_id, run_id);
            let logs = worker.container_logs(&identity.id).await.unwrap();
            assert!(logs.contains("container stdout"));
            assert!(logs.contains("container stderr"));
            worker.remove_container(&identity.id).await.unwrap();
        }

        let commands = fs::read_to_string(log).unwrap();
        let create = commands
            .lines()
            .filter(|line| line.starts_with("create --interactive"))
            .collect::<Vec<_>>();
        assert_eq!(create.len(), 2);
        for line in &create {
            assert!(line.contains("--read-only"));
            assert!(line.contains("--cap-drop ALL"));
            assert!(line.contains("--security-opt no-new-privileges"));
            assert!(line.contains("--pids-limit 128"));
            assert!(line.contains("--memory 4g"));
            assert!(line.contains("--cpus 2"));
            assert!(line.contains("--env GH_TOKEN"));
            assert!(line.contains("--sandbox danger-full-access"));
            assert!(line.contains("--ephemeral --ignore-user-config"));
            assert!(!line.contains("super-secret-docker-token"));
            assert!(!line.contains("docker.sock"));
        }
        let read_only_path = read_only_clone.canonicalize().unwrap();
        assert!(create[0].contains(&format!(
            "type=bind,src={},dst=/workspace,readonly",
            read_only_path.display()
        )));
        let read_write_path = read_write_clone.canonicalize().unwrap();
        assert!(create[1].contains(&format!(
            "type=bind,src={},dst=/workspace",
            read_write_path.display()
        )));
        assert!(!create[1].contains("dst=/workspace,readonly"));
        assert!(create[0].contains(&format!(
            "type=bind,src={},dst=/home/agent/.codex/auth.json",
            auth.canonicalize().unwrap().display()
        )));

        for run_id in [11, 12] {
            let persisted = commands.find(&format!("persisted {run_id}")).unwrap();
            let started = commands[persisted..]
                .find("start --attach --interactive")
                .map(|offset| offset + persisted)
                .unwrap();
            assert!(persisted < started);
        }
    }

    #[tokio::test]
    async fn streams_complete_activity_lines_before_eof() {
        let (mut writer, reader) = tokio::io::duplex(1024);
        let (observations, mut receiver) = observation_channel();
        let capture = tokio::spawn(capture_pipe(reader, true, false, observations));

        writer
            .write_all(b"{\"type\":\"turn.started\"}\n")
            .await
            .unwrap();
        writer.flush().await.unwrap();
        tokio::time::timeout(Duration::from_secs(1), receiver.changed())
            .await
            .expect("activity was not streamed before EOF")
            .unwrap();
        assert!(
            receiver
                .borrow()
                .activity
                .as_deref()
                .unwrap()
                .contains("Codex progress: working")
        );

        drop(writer);
        assert_eq!(capture.await.unwrap().unwrap().lines, 1);
    }

    #[tokio::test]
    async fn stderr_activity_records_only_a_structural_byte_count() {
        let secret = "unstructured-secret-value";
        let (mut writer, reader) = tokio::io::duplex(1024);
        let (observations, mut receiver) = observation_channel();
        observations.send_modify(|observation| {
            observation.activity = Some("Codex progress: working\n".to_owned());
        });
        receiver.borrow_and_update();
        let capture = tokio::spawn(capture_pipe(reader, false, false, observations));

        writer.write_all(secret.as_bytes()).await.unwrap();
        drop(writer);
        tokio::time::timeout(Duration::from_secs(1), receiver.changed())
            .await
            .expect("stderr activity was not observed")
            .unwrap();

        let activity = receiver.borrow().activity.clone().unwrap();
        assert_eq!(
            activity,
            format!(
                "Codex progress: working\nCodex stderr activity: {} bytes\n",
                secret.len()
            )
        );
        assert!(!activity.contains(secret));
        assert_eq!(capture.await.unwrap().unwrap().text, secret);
    }

    #[test]
    fn unknown_activity_without_a_pull_request_is_ignored() {
        let (observations, receiver) = observation_channel();

        observe_event(
            &observations,
            &serde_json::json!({
                "type": "future.event",
                "payload": "untrusted value"
            }),
        );

        assert_eq!(receiver.borrow().sequence, 0);
        assert_eq!(receiver.borrow().activity, None);
        assert_eq!(receiver.borrow().pull_request, None);
    }

    #[test]
    fn duplicate_and_unknown_item_activity_do_not_notify_or_advance_sequence() {
        let (observations, mut receiver) = observation_channel();
        let command = serde_json::json!({
            "type": "item.started",
            "item": { "type": "command_execution", "command": "untrusted" }
        });

        observe_event(&observations, &command);
        assert!(receiver.has_changed().unwrap());
        receiver.borrow_and_update();
        let sequence = receiver.borrow().sequence;

        observe_event(&observations, &command);
        observe_event(
            &observations,
            &serde_json::json!({
                "type": "item.started",
                "item": { "type": "future_item", "payload": "untrusted" }
            }),
        );

        assert!(!receiver.has_changed().unwrap());
        assert_eq!(receiver.borrow().sequence, sequence);
    }

    #[test]
    fn updated_plan_activity_is_safe_and_deduplicated() {
        let (observations, mut receiver) = observation_channel();
        let update = serde_json::json!({
            "type": "item.updated",
            "item": {
                "type": "todo_list",
                "items": [{ "text": "untrusted plan content" }]
            }
        });

        observe_event(&observations, &update);
        assert_eq!(
            receiver.borrow().activity.as_deref(),
            Some("Codex progress: plan updated\n")
        );
        assert!(
            !receiver
                .borrow()
                .activity
                .as_deref()
                .unwrap()
                .contains("untrusted")
        );
        receiver.borrow_and_update();
        let sequence = receiver.borrow().sequence;

        observe_event(&observations, &update);

        assert!(!receiver.has_changed().unwrap());
        assert_eq!(receiver.borrow().sequence, sequence);
    }

    #[tokio::test]
    async fn bounds_and_rejects_malformed_activity_lines() {
        let (mut writer, reader) = tokio::io::duplex(1024);
        let (observations, _) = observation_channel();
        let capture = tokio::spawn(capture_pipe(reader, true, false, observations));
        let writer_task = tokio::spawn(async move {
            let mut oversized = vec![b'x'; MAX_STREAM_BYTES + 1];
            oversized.push(b'\n');
            writer.write_all(&oversized).await.unwrap();
            writer.write_all(b"not-json\n").await.unwrap();
        });

        writer_task.await.unwrap();
        let captured = capture.await.unwrap().unwrap();
        assert_eq!(captured.lines, 2);
        assert!(captured.malformed.as_deref().unwrap().contains("exceeded"));
        assert!(captured.text.len() <= MAX_STREAM_BYTES);
    }
}
