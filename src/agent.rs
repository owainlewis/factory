use std::fmt;
use std::fs::File;
use std::io::{Read, stdin};
use std::path::{Path, PathBuf};
use std::process::Command;

use anyhow::{Context, Result, bail};
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use tokio_util::sync::CancellationToken;

use crate::config::repository_remote_identity;
use crate::github::{DraftPullRequestRequest, GitHubClient, ProposalIssueRequest};
use crate::storage::{ActiveRunContext, EffectReservation, Ledger, RunEffect};
use crate::workspace::WorkspaceManager;

const MAX_PAYLOAD_BYTES: usize = 64 * 1024;
const MAX_BODY_BYTES: usize = 32 * 1024;
const MAX_TITLE_BYTES: usize = 256;
const MAX_LIST_ITEMS: usize = 100;
const MAX_ITEM_BYTES: usize = 2048;

#[derive(Debug, Clone)]
pub enum AgentCommand {
    TaskShow,
    TaskComment(PathBuf),
    TaskBlock(PathBuf),
    ProposalCreate(PathBuf),
    ChangePublish(PathBuf),
    RunComplete(PathBuf),
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct RunPolicy {
    pub version: u8,
    pub repository: String,
    pub canonical_repository: PathBuf,
    pub workspace_root: PathBuf,
    pub worktree: PathBuf,
    pub effect: String,
    pub ready_label: String,
    pub proposed_label: String,
    pub needs_review_label: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct CommentPayload {
    version: u8,
    idempotency_key: String,
    body: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct BlockPayload {
    version: u8,
    idempotency_key: String,
    reason: String,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct ProposalPayload {
    version: u8,
    idempotency_key: String,
    title: String,
    problem: String,
    #[serde(default)]
    evidence: Vec<String>,
    acceptance_criteria: Vec<String>,
    #[serde(default)]
    verification: Vec<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct PublishPayload {
    version: u8,
    idempotency_key: String,
    title: String,
    summary: String,
    #[serde(default)]
    tests: Vec<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct CompletePayload {
    version: u8,
    idempotency_key: String,
    summary: String,
    #[serde(default)]
    checks: Vec<String>,
}

struct AgentSession {
    ledger: Ledger,
    context: ActiveRunContext,
    policy: RunPolicy,
}

struct PreparedPayload<T> {
    value: T,
    raw: String,
    hash: String,
    idempotency_key: String,
    version: u32,
}

#[derive(Debug)]
struct RecordedEffectFailure;

impl fmt::Display for RecordedEffectFailure {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("effect execution failed after reservation")
    }
}

pub async fn execute(command: AgentCommand) -> Result<Value> {
    let action = command.action();
    let mut session = match AgentSession::from_environment() {
        Ok(session) => session,
        Err(error) => {
            audit_context_rejection(action, &error);
            return Err(error);
        }
    };
    let result = execute_scoped(&mut session, command).await;
    if let Err(error) = &result
        && error.downcast_ref::<RecordedEffectFailure>().is_none()
    {
        let _ = session.ledger.reject_run_effect(
            Some(session.context.run.id),
            Some(session.context.run.id),
            action,
            session.context.run.effect.as_deref(),
            None,
            None,
            None,
            &bounded_error(error),
        );
    }
    result
}

impl AgentCommand {
    fn action(&self) -> &'static str {
        match self {
            Self::TaskShow => "task.show",
            Self::TaskComment(_) => "task.comment",
            Self::TaskBlock(_) => "task.block",
            Self::ProposalCreate(_) => "proposal.create",
            Self::ChangePublish(_) => "change.publish",
            Self::RunComplete(_) => "run.complete",
        }
    }
}

impl AgentSession {
    fn from_environment() -> Result<Self> {
        let run_id = required_environment("FACTORY_RUN_ID")?
            .parse::<i64>()
            .context("FACTORY_RUN_ID is invalid")?;
        let ledger_path = PathBuf::from(required_environment("FACTORY_LEDGER_PATH")?);
        if !ledger_path.is_absolute() {
            bail!("FACTORY_LEDGER_PATH must be absolute");
        }
        let token = required_environment("FACTORY_RUN_TOKEN")?;
        let token_hash = hash_text(&token);
        let ledger = Ledger::open(&ledger_path)?;
        let context = ledger.active_run_context(run_id, &token_hash)?;
        let policy: RunPolicy = serde_json::from_str(
            context
                .run
                .policy_json
                .as_deref()
                .context("active run has no durable policy")?,
        )
        .context("active run policy is invalid")?;
        validate_session_scope(&context, &policy)?;
        Ok(Self {
            ledger,
            context,
            policy,
        })
    }
}

async fn execute_scoped(session: &mut AgentSession, command: AgentCommand) -> Result<Value> {
    match command {
        AgentCommand::TaskShow => task_show(session),
        AgentCommand::TaskComment(path) => {
            let payload = read_payload::<CommentPayload>(&path, |payload| {
                (&payload.idempotency_key, payload.version)
            })?;
            task_comment(session, payload).await
        }
        AgentCommand::TaskBlock(path) => {
            let payload = read_payload::<BlockPayload>(&path, |payload| {
                (&payload.idempotency_key, payload.version)
            })?;
            task_block(session, payload).await
        }
        AgentCommand::ProposalCreate(path) => {
            let payload = read_payload::<ProposalPayload>(&path, |payload| {
                (&payload.idempotency_key, payload.version)
            })?;
            proposal_create(session, payload).await
        }
        AgentCommand::ChangePublish(path) => {
            let payload = read_payload::<PublishPayload>(&path, |payload| {
                (&payload.idempotency_key, payload.version)
            })?;
            change_publish(session, payload).await
        }
        AgentCommand::RunComplete(path) => {
            let payload = read_payload::<CompletePayload>(&path, |payload| {
                (&payload.idempotency_key, payload.version)
            })?;
            run_complete(session, payload)
        }
    }
}

fn task_show(session: &mut AgentSession) -> Result<Value> {
    let effects = session
        .ledger
        .run_effects(session.context.run.id)?
        .into_iter()
        .map(|effect| {
            json!({
                "action": effect.action,
                "outcome": effect.outcome,
                "external_ref": effect.external_ref,
                "detail": effect.detail,
            })
        })
        .collect::<Vec<_>>();
    Ok(json!({
        "version": 1,
        "run_id": session.context.run.id,
        "task_id": session.context.task.id,
        "workflow": session.context.task.workflow,
        "effect": session.policy.effect,
        "repository": session.policy.repository,
        "source_item": session.context.task.source_item,
        "payload": session.context.task.payload,
        "base_branch": session.context.workspace.base_branch,
        "base_sha": session.context.workspace.base_sha,
        "factory_branch": session.context.workspace.factory_branch,
        "worktree": session.context.workspace.path,
        "effects": effects,
    }))
}

async fn task_comment(
    session: &mut AgentSession,
    payload: PreparedPayload<CommentPayload>,
) -> Result<Value> {
    validate_version(payload.value.version)?;
    validate_text("comment body", &payload.value.body, MAX_BODY_BYTES)?;
    let issue = source_issue(session)?;
    let effect = reserve(session, "task.comment", &payload)?;
    if let Some(value) = replay(&effect) {
        return Ok(value);
    }
    let marker = marker("comment", session, &payload.idempotency_key);
    let github = GitHubClient::default();
    let cancellation = CancellationToken::new();
    let operation = async {
        let comments = github
            .issue_comments(
                &session.context.workspace.path,
                &session.policy.repository,
                issue,
                &cancellation,
            )
            .await?;
        let external_ref = if let Some(comment) = comments.iter().find(|comment| {
            comment
                .body
                .as_deref()
                .is_some_and(|body| body.contains(&marker))
        }) {
            comment.html_url.clone()
        } else {
            let body = format!("{}\n\n{}", payload.value.body.trim(), marker);
            let id = github
                .post_issue_comment(
                    &session.context.workspace.path,
                    &session.policy.repository,
                    issue,
                    &body,
                    &cancellation,
                )
                .await?;
            format!("issue-comment:{id}")
        };
        Ok((Some(external_ref), "issue comment applied"))
    }
    .await;
    finish_effect(session, effect.id, operation)
}

async fn task_block(
    session: &mut AgentSession,
    payload: PreparedPayload<BlockPayload>,
) -> Result<Value> {
    validate_version(payload.value.version)?;
    validate_text("block reason", &payload.value.reason, MAX_BODY_BYTES)?;
    let issue = source_issue(session)?;
    let effect = reserve(session, "task.block", &payload)?;
    if let Some(value) = replay(&effect) {
        return Ok(value);
    }
    let marker = marker("block", session, &payload.idempotency_key);
    let github = GitHubClient::default();
    let cancellation = CancellationToken::new();
    let operation = async {
        let comments = github
            .issue_comments(
                &session.context.workspace.path,
                &session.policy.repository,
                issue,
                &cancellation,
            )
            .await?;
        let external_ref = if let Some(comment) = comments.iter().find(|comment| {
            comment
                .body
                .as_deref()
                .is_some_and(|body| body.contains(&marker))
        }) {
            comment.html_url.clone()
        } else {
            let body = format!("{}\n\n{}", payload.value.reason.trim(), marker);
            let id = github
                .post_issue_comment(
                    &session.context.workspace.path,
                    &session.policy.repository,
                    issue,
                    &body,
                    &cancellation,
                )
                .await?;
            format!("issue-comment:{id}")
        };
        github
            .edit_issue_label(
                &session.context.workspace.path,
                issue,
                &session.policy.needs_review_label,
                true,
                &cancellation,
            )
            .await?;
        session
            .ledger
            .record_run_disposition(session.context.run.id, "blocked", &payload.raw)?;
        Ok((Some(external_ref), "task blocked"))
    }
    .await;
    finish_effect(session, effect.id, operation)
}

async fn proposal_create(
    session: &mut AgentSession,
    payload: PreparedPayload<ProposalPayload>,
) -> Result<Value> {
    require_effect(session, "proposal")?;
    validate_version(payload.value.version)?;
    validate_title(&payload.value.title)?;
    validate_text("proposal problem", &payload.value.problem, MAX_BODY_BYTES)?;
    validate_list("proposal evidence", &payload.value.evidence)?;
    validate_list(
        "proposal acceptance criteria",
        &payload.value.acceptance_criteria,
    )?;
    validate_list("proposal verification", &payload.value.verification)?;
    if payload.value.acceptance_criteria.is_empty() {
        bail!("proposal acceptance criteria must not be empty");
    }
    let effect = reserve(session, "proposal.create", &payload)?;
    if let Some(value) = replay(&effect) {
        return Ok(value);
    }
    let marker = marker("proposal", session, &payload.idempotency_key);
    let body = render_proposal(&payload.value);
    let operation = async {
        let proposal = GitHubClient::default()
            .find_or_create_proposal(
                &session.context.workspace.path,
                &session.policy.repository,
                ProposalIssueRequest {
                    title: &payload.value.title,
                    body: &body,
                    proposed_label: &session.policy.proposed_label,
                    marker: &marker,
                },
                &CancellationToken::new(),
            )
            .await?;
        Ok((
            Some(proposal.url),
            if proposal.created {
                "proposal issue created"
            } else {
                "proposal issue reused"
            },
        ))
    }
    .await;
    finish_effect(session, effect.id, operation)
}

async fn change_publish(
    session: &mut AgentSession,
    payload: PreparedPayload<PublishPayload>,
) -> Result<Value> {
    require_effect(session, "delivery")?;
    validate_version(payload.value.version)?;
    validate_title(&payload.value.title)?;
    validate_text("change summary", &payload.value.summary, MAX_BODY_BYTES)?;
    validate_list("change tests", &payload.value.tests)?;
    let branch = session
        .context
        .workspace
        .factory_branch
        .clone()
        .context("delivery run has no recorded Factory branch")?;
    let effect = reserve(session, "change.publish", &payload)?;
    if let Some(value) = replay(&effect) {
        return Ok(value);
    }
    let body = render_change(session, &payload.value);
    let operation = async {
        let github = GitHubClient::default();
        let cancellation = CancellationToken::new();
        github
            .validate_draft_pull_request_target(
                &session.context.workspace.path,
                &session.policy.repository,
                &branch,
                &cancellation,
            )
            .await?;
        let manager = WorkspaceManager::new(
            &session.policy.canonical_repository,
            &session.policy.workspace_root,
        )?;
        manager.push_recorded_branch(&session.context.workspace.path, &branch)?;
        let pull = github
            .publish_draft_pull_request(
                &session.context.workspace.path,
                &session.policy.repository,
                DraftPullRequestRequest {
                    head_branch: &branch,
                    base_branch: &session.context.workspace.base_branch,
                    title: &payload.value.title,
                    body: &body,
                },
                &cancellation,
            )
            .await?;
        session.ledger.observe_run(
            session.context.run.id,
            None,
            None,
            None,
            Some(&pull.url),
            Some("Factory draft change published"),
        )?;
        Ok((
            Some(pull.url),
            if pull.created {
                "draft pull request created"
            } else {
                "draft pull request updated"
            },
        ))
    }
    .await;
    finish_effect(session, effect.id, operation)
}

fn run_complete(
    session: &mut AgentSession,
    payload: PreparedPayload<CompletePayload>,
) -> Result<Value> {
    validate_version(payload.value.version)?;
    validate_text("completion summary", &payload.value.summary, MAX_BODY_BYTES)?;
    validate_list("completion checks", &payload.value.checks)?;
    if session.policy.effect == "delivery" {
        if session.context.run.pull_request.is_none() {
            bail!("delivery run cannot complete before change publication");
        }
        let branch = session
            .context
            .workspace
            .factory_branch
            .as_deref()
            .context("delivery run has no recorded Factory branch")?;
        let manager = WorkspaceManager::new(
            &session.policy.canonical_repository,
            &session.policy.workspace_root,
        )?;
        if manager
            .preview_cleanup(&session.context.workspace.path)?
            .dirty
        {
            bail!("delivery run cannot complete with uncommitted changes");
        }
        if !manager.branch_is_pushed(branch)? {
            bail!("delivery run cannot complete until its exact Factory branch is pushed");
        }
    }
    let effect = reserve(session, "run.complete", &payload)?;
    if let Some(value) = replay(&effect) {
        return Ok(value);
    }
    let operation = session
        .ledger
        .record_run_disposition(session.context.run.id, "completed", &payload.raw)
        .map(|()| (None, "run completion recorded"));
    finish_effect(session, effect.id, operation)
}

fn reserve<T>(
    session: &mut AgentSession,
    action: &str,
    payload: &PreparedPayload<T>,
) -> Result<RunEffect> {
    match session.ledger.reserve_run_effect(
        session.context.run.id,
        action,
        &session.policy.effect,
        &payload.idempotency_key,
        payload.version,
        &payload.hash,
    )? {
        EffectReservation::Reserved(effect) => Ok(effect),
        EffectReservation::Existing(effect) if effect.outcome == "applied" => Ok(effect),
        EffectReservation::Existing(_) => {
            bail!("an effect with this idempotency key is already in progress")
        }
    }
}

fn replay(effect: &RunEffect) -> Option<Value> {
    (effect.outcome == "applied").then(|| effect_response(effect))
}

fn finish_effect(
    session: &mut AgentSession,
    effect_id: i64,
    operation: Result<(Option<String>, &'static str)>,
) -> Result<Value> {
    let (external_ref, detail) = match operation {
        Ok(result) => result,
        Err(error) => return Err(record_effect_failure(session, effect_id, error)),
    };
    match session
        .ledger
        .complete_run_effect(effect_id, external_ref.as_deref(), detail)
    {
        Ok(effect) => Ok(effect_response(&effect)),
        Err(error) => Err(record_effect_failure(session, effect_id, error)),
    }
}

fn record_effect_failure(
    session: &mut AgentSession,
    effect_id: i64,
    error: anyhow::Error,
) -> anyhow::Error {
    let detail = bounded_error(&error);
    match session.ledger.fail_run_effect(effect_id, &detail) {
        Ok(_) => error.context(RecordedEffectFailure),
        Err(record_error) => error
            .context(format!("failed to record effect failure: {record_error:#}"))
            .context(RecordedEffectFailure),
    }
}

fn effect_response(effect: &RunEffect) -> Value {
    json!({
        "version": 1,
        "action": effect.action,
        "outcome": effect.outcome,
        "external_ref": effect.external_ref,
        "detail": effect.detail,
    })
}

fn source_issue(session: &AgentSession) -> Result<u64> {
    session
        .context
        .task
        .source_item
        .as_deref()
        .context("active task has no source issue")?
        .parse()
        .context("active task source issue is invalid")
}

fn require_effect(session: &AgentSession, expected: &str) -> Result<()> {
    if session.policy.effect != expected {
        bail!(
            "{} workflow cannot perform an operation requiring {expected} effect",
            session.policy.effect
        );
    }
    Ok(())
}

fn validate_session_scope(context: &ActiveRunContext, policy: &RunPolicy) -> Result<()> {
    if policy.version != 1 {
        bail!("unsupported run policy version {}", policy.version);
    }
    if context.run.effect.as_deref() != Some(policy.effect.as_str())
        || context.task.repository != policy.repository
        || context.workspace.path != policy.worktree
    {
        bail!("active run does not match its durable policy");
    }
    let current = std::env::current_dir()?.canonicalize()?;
    let worktree = policy.worktree.canonicalize()?;
    if current != worktree {
        bail!("task command must run from the active Factory worktree root");
    }
    let top = git_output(&current, &["rev-parse", "--show-toplevel"])?;
    if PathBuf::from(top.trim()).canonicalize()? != worktree {
        bail!("current Git worktree does not match the active run");
    }
    let common = git_output(&current, &["rev-parse", "--git-common-dir"])?;
    let common = PathBuf::from(common.trim()).canonicalize()?;
    let expected_common = policy.canonical_repository.join(".git").canonicalize()?;
    if common != expected_common {
        bail!("current Git worktree belongs to an unrelated repository");
    }
    if repository_remote_identity(&current)? != policy.repository {
        bail!("current Git origin does not match the active run repository");
    }
    Ok(())
}

fn read_payload<T: DeserializeOwned>(
    path: &Path,
    metadata: impl FnOnce(&T) -> (&str, u8),
) -> Result<PreparedPayload<T>> {
    let bytes = read_bounded(path)?;
    let raw = String::from_utf8(bytes).context("effect payload must be UTF-8 JSON")?;
    let value: T = serde_json::from_str(&raw).context("effect payload does not match v1 schema")?;
    let (idempotency_key, version) = metadata(&value);
    validate_idempotency_key(idempotency_key)?;
    Ok(PreparedPayload {
        hash: hash_text(&raw),
        idempotency_key: idempotency_key.to_owned(),
        version: u32::from(version),
        value,
        raw,
    })
}

fn read_bounded(path: &Path) -> Result<Vec<u8>> {
    let mut reader: Box<dyn Read> = if path == Path::new("-") {
        Box::new(stdin())
    } else {
        let metadata = std::fs::symlink_metadata(path)
            .with_context(|| format!("failed to inspect payload {}", path.display()))?;
        if !metadata.file_type().is_file() || metadata.file_type().is_symlink() {
            bail!("effect payload must be a regular file and not a symlink");
        }
        #[cfg(unix)]
        let file = {
            use rustix::fs::{Mode, OFlags, open};
            File::from(open(
                path,
                OFlags::RDONLY | OFlags::CLOEXEC | OFlags::NOFOLLOW,
                Mode::empty(),
            )?)
        };
        Box::new(file)
    };
    let mut bytes = Vec::new();
    reader
        .by_ref()
        .take((MAX_PAYLOAD_BYTES + 1) as u64)
        .read_to_end(&mut bytes)?;
    if bytes.len() > MAX_PAYLOAD_BYTES {
        bail!("effect payload exceeds {MAX_PAYLOAD_BYTES} bytes");
    }
    Ok(bytes)
}

fn validate_version(version: u8) -> Result<()> {
    if version != 1 {
        bail!("effect payload version must be 1");
    }
    Ok(())
}

fn validate_idempotency_key(value: &str) -> Result<()> {
    if value.is_empty()
        || value.len() > 128
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.'))
    {
        bail!("idempotency_key must be 1-128 ASCII letters, digits, dot, dash, or underscore");
    }
    Ok(())
}

fn validate_title(value: &str) -> Result<()> {
    validate_text("title", value, MAX_TITLE_BYTES)
}

fn validate_text(name: &str, value: &str, maximum: usize) -> Result<()> {
    if value.trim().is_empty() {
        bail!("{name} must not be empty");
    }
    if value.len() > maximum {
        bail!("{name} exceeds {maximum} bytes");
    }
    if value.chars().any(|character| character == '\0') {
        bail!("{name} contains a NUL byte");
    }
    Ok(())
}

fn validate_list(name: &str, values: &[String]) -> Result<()> {
    if values.len() > MAX_LIST_ITEMS {
        bail!("{name} exceeds {MAX_LIST_ITEMS} items");
    }
    for value in values {
        validate_text(name, value, MAX_ITEM_BYTES)?;
    }
    Ok(())
}

fn render_proposal(payload: &ProposalPayload) -> String {
    let mut body = format!("## Problem\n\n{}", payload.problem.trim());
    append_list(&mut body, "Evidence", &payload.evidence);
    append_list(
        &mut body,
        "Acceptance criteria",
        &payload.acceptance_criteria,
    );
    append_list(&mut body, "Verification", &payload.verification);
    body
}

fn render_change(session: &AgentSession, payload: &PublishPayload) -> String {
    let mut body = format!("## Summary\n\n{}", payload.summary.trim());
    append_list(&mut body, "Tests", &payload.tests);
    body.push_str(&format!(
        "\n\n<!-- factory-change:v1:task-{} -->",
        session.context.task.id
    ));
    body
}

fn append_list(body: &mut String, heading: &str, values: &[String]) {
    if values.is_empty() {
        return;
    }
    body.push_str(&format!("\n\n## {heading}\n"));
    for value in values {
        body.push_str(&format!("\n- {}", value.trim()));
    }
}

fn marker(kind: &str, session: &AgentSession, key: &str) -> String {
    let digest = hash_text(&format!(
        "{}:{}:{}:{}",
        session.policy.repository, session.context.task.id, kind, key
    ));
    format!("<!-- factory-{kind}:v1:{digest} -->")
}

fn required_environment(name: &str) -> Result<String> {
    std::env::var(name).with_context(|| format!("{name} is required inside an active Factory run"))
}

fn hash_text(value: &str) -> String {
    format!("sha256:{:x}", Sha256::digest(value.as_bytes()))
}

fn git_output(directory: &Path, arguments: &[&str]) -> Result<String> {
    let output = Command::new("git")
        .args(arguments)
        .current_dir(directory)
        .output()?;
    if !output.status.success() {
        bail!(
            "git {} failed: {}",
            arguments.join(" "),
            String::from_utf8_lossy(&output.stderr).trim()
        );
    }
    String::from_utf8(output.stdout).context("git returned non-UTF-8 output")
}

fn audit_context_rejection(action: &str, error: &anyhow::Error) {
    let Ok(path) = std::env::var("FACTORY_LEDGER_PATH") else {
        return;
    };
    let requested = std::env::var("FACTORY_RUN_ID")
        .ok()
        .and_then(|value| value.parse::<i64>().ok());
    let Ok(mut ledger) = Ledger::open(Path::new(&path)) else {
        return;
    };
    let run_id = requested.filter(|id| ledger.run(*id).ok().flatten().is_some());
    let _ = ledger.reject_run_effect(
        requested,
        run_id,
        action,
        None,
        None,
        None,
        None,
        &bounded_error(error),
    );
}

fn bounded_error(error: &anyhow::Error) -> String {
    let mut value = format!("{error:#}");
    if value.len() > 4096 {
        let mut end = 4096;
        while !value.is_char_boundary(end) {
            end -= 1;
        }
        value.truncate(end);
    }
    value
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strict_payload_rejects_merge_and_label_fields() {
        let publish = serde_json::from_str::<PublishPayload>(
            r#"{"version":1,"idempotency_key":"one","title":"Change","summary":"Summary","tests":[],"merge":true}"#,
        );
        let proposal = serde_json::from_str::<ProposalPayload>(
            r#"{"version":1,"idempotency_key":"one","title":"Proposal","problem":"Problem","acceptance_criteria":["Done"],"labels":["factory:ready"]}"#,
        );
        assert!(publish.is_err());
        assert!(proposal.is_err());
    }

    #[test]
    fn markers_are_stable_and_bounded() {
        let digest = hash_text("repo:1:proposal:key");
        let marker = format!("<!-- factory-proposal:v1:{digest} -->");
        assert!(marker.len() <= 256);
        assert!(!marker.contains('\n'));
    }

    #[test]
    fn bounded_errors_do_not_split_utf8() {
        let message = format!("{}é", "a".repeat(4095));
        let error = anyhow::anyhow!(message);
        let bounded = bounded_error(&error);
        assert_eq!(bounded.len(), 4095);
        assert!(bounded.chars().all(|character| character == 'a'));
    }
}
