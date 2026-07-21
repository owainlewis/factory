use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use tokio::process::Command;
use tokio_util::sync::CancellationToken;

use crate::approval::{
    ClaimArtifact, approved_content_hash, parse as parse_approval, parse_claim, render_claim,
};
use crate::config::{Config, GitHubConfig};
use crate::storage::{ApprovalEvidence, Ledger, ObservedTicket, Task};
use crate::workflow::{Trigger, WorkflowCatalog, WorkflowEntry, workflow_content_hash};

const DEFAULT_COMMAND_TIMEOUT: Duration = Duration::from_secs(30);

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TicketContext {
    pub number: u64,
    pub url: String,
    pub title: String,
    pub body: String,
    pub observed_revision: String,
    pub approval: TicketApproval,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TicketApproval {
    pub artifact_id: u64,
    pub label_event_id: u64,
    pub approver_id: u64,
    pub workflow_hash: String,
    pub approved_content_hash: String,
    pub nonce: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct GitHubUser {
    pub id: u64,
    pub login: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct IssueSnapshot {
    pub number: u64,
    pub url: String,
    pub title: String,
    pub body: String,
    pub state: String,
    pub labels: Vec<String>,
    pub updated_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProposalIssue {
    pub number: u64,
    pub url: String,
    pub created: bool,
}

#[derive(Debug, Clone, Copy)]
pub struct ProposalIssueRequest<'a> {
    pub title: &'a str,
    pub body: &'a str,
    pub proposed_label: &'a str,
    pub marker: &'a str,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DraftPullRequest {
    pub number: u64,
    pub url: String,
    pub created: bool,
}

#[derive(Debug, Clone, Copy)]
pub struct DraftPullRequestRequest<'a> {
    pub head_branch: &'a str,
    pub base_branch: &'a str,
    pub title: &'a str,
    pub body: &'a str,
}

#[derive(Debug, Deserialize)]
struct ApiRepository {
    default_branch: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RepositoryPoll {
    pub repository: PathBuf,
    pub name_with_owner: Option<String>,
    pub issues_seen: usize,
    pub tasks_created: usize,
    pub error: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PollReport {
    pub repositories: Vec<RepositoryPoll>,
}

impl PollReport {
    pub fn tasks_created(&self) -> usize {
        self.repositories
            .iter()
            .map(|item| item.tasks_created)
            .sum()
    }

    pub fn failures(&self) -> usize {
        self.repositories
            .iter()
            .filter(|item| item.error.is_some())
            .count()
    }
}

#[derive(Debug, Clone)]
pub struct GitHubClient {
    executable: PathBuf,
    command_timeout: Duration,
}

impl Default for GitHubClient {
    fn default() -> Self {
        Self::new("gh")
    }
}

impl GitHubClient {
    pub fn new(executable: impl Into<PathBuf>) -> Self {
        Self {
            executable: executable.into(),
            command_timeout: DEFAULT_COMMAND_TIMEOUT,
        }
    }

    pub fn with_command_timeout(mut self, command_timeout: Duration) -> Self {
        self.command_timeout = command_timeout;
        self
    }

    pub async fn validate_global(&self, cancellation: &CancellationToken) -> Result<()> {
        self.run(None, &["--version"], cancellation)
            .await
            .context("GitHub CLI is unavailable; install gh and ensure it is executable")?;
        self.run(None, &["auth", "status"], cancellation)
            .await
            .context("GitHub CLI is not authenticated; run gh auth login")?;
        Ok(())
    }

    pub async fn validate_repository(
        &self,
        repository: &Path,
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let output = self
            .run(
                Some(repository),
                &[
                    "repo",
                    "view",
                    "--json",
                    "nameWithOwner",
                    "--jq",
                    ".nameWithOwner",
                ],
                cancellation,
            )
            .await
            .with_context(|| {
                format!(
                    "GitHub CLI cannot read configured repository {}",
                    repository.display()
                )
            })?;
        let name = output.trim();
        if !valid_name_with_owner(name) {
            bail!("gh returned invalid repository identity {name:?}");
        }
        Ok(name.to_owned())
    }

    pub async fn labels(
        &self,
        repository: &Path,
        cancellation: &CancellationToken,
    ) -> Result<Vec<String>> {
        let output = self
            .run(
                Some(repository),
                &[
                    "api",
                    "repos/{owner}/{repo}/labels",
                    "--paginate",
                    "--jq",
                    ".[].name",
                ],
                cancellation,
            )
            .await
            .with_context(|| {
                format!(
                    "GitHub CLI cannot list all labels for {}",
                    repository.display()
                )
            })?;
        Ok(output.lines().map(str::to_owned).collect())
    }

    pub async fn create_label(
        &self,
        repository: &Path,
        name: &str,
        description: &str,
        color: &str,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.run(
            Some(repository),
            &[
                "label",
                "create",
                name,
                "--description",
                description,
                "--color",
                color,
            ],
            cancellation,
        )
        .await
        .with_context(|| format!("failed to create GitHub label {name:?}"))?;
        Ok(())
    }

    pub async fn current_user(&self, cancellation: &CancellationToken) -> Result<GitHubUser> {
        self.api_json(None, "user", cancellation)
            .await
            .context("failed to resolve authenticated GitHub user")
    }

    pub async fn user(
        &self,
        repository: &Path,
        login: &str,
        cancellation: &CancellationToken,
    ) -> Result<GitHubUser> {
        self.api_json(Some(repository), &format!("users/{login}"), cancellation)
            .await
            .with_context(|| format!("failed to resolve trusted GitHub user {login:?}"))
    }

    pub async fn issue(
        &self,
        repository: &Path,
        name: &str,
        issue: u64,
        cancellation: &CancellationToken,
    ) -> Result<IssueSnapshot> {
        let issue: ApiIssue = self
            .api_json(
                Some(repository),
                &format!("repos/{name}/issues/{issue}"),
                cancellation,
            )
            .await?;
        Ok(IssueSnapshot::from(issue))
    }

    pub async fn default_branch(
        &self,
        repository: &Path,
        name: &str,
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let response: ApiRepository = self
            .api_json(Some(repository), &format!("repos/{name}"), cancellation)
            .await?;
        let branch = response.default_branch.trim();
        if branch.is_empty()
            || branch.starts_with('-')
            || branch
                .chars()
                .any(|character| matches!(character, '\0' | '\n' | '\r'))
        {
            bail!("GitHub returned invalid default branch {branch:?}");
        }
        Ok(branch.to_owned())
    }

    pub async fn repository_default_branch(
        &self,
        repository: &Path,
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let output = self
            .run(
                Some(repository),
                &[
                    "repo",
                    "view",
                    "--json",
                    "defaultBranchRef",
                    "--jq",
                    ".defaultBranchRef.name",
                ],
                cancellation,
            )
            .await?;
        let branch = output.trim();
        if branch.is_empty()
            || branch.starts_with('-')
            || branch
                .chars()
                .any(|character| matches!(character, '\0' | '\n' | '\r'))
        {
            bail!("GitHub returned invalid default branch {branch:?}");
        }
        Ok(branch.to_owned())
    }

    pub async fn issue_comments(
        &self,
        repository: &Path,
        name: &str,
        issue: u64,
        cancellation: &CancellationToken,
    ) -> Result<Vec<ApiComment>> {
        self.api_pages(
            repository,
            &format!("repos/{name}/issues/{issue}/comments?per_page=100"),
            cancellation,
        )
        .await
    }

    pub async fn issue_timeline(
        &self,
        repository: &Path,
        name: &str,
        issue: u64,
        cancellation: &CancellationToken,
    ) -> Result<Vec<ApiTimelineEvent>> {
        self.api_pages(
            repository,
            &format!("repos/{name}/issues/{issue}/timeline?per_page=100"),
            cancellation,
        )
        .await
    }

    pub async fn post_issue_comment(
        &self,
        repository: &Path,
        name: &str,
        issue: u64,
        body: &str,
        cancellation: &CancellationToken,
    ) -> Result<u64> {
        let endpoint = format!("repos/{name}/issues/{issue}/comments");
        let output = self
            .run(
                Some(repository),
                &[
                    "api",
                    "--method",
                    "POST",
                    &endpoint,
                    "-f",
                    &format!("body={body}"),
                    "--jq",
                    ".id",
                ],
                cancellation,
            )
            .await?;
        output
            .trim()
            .parse()
            .context("GitHub returned invalid issue comment ID")
    }

    pub async fn edit_issue_label(
        &self,
        repository: &Path,
        issue: u64,
        label: &str,
        add: bool,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let issue = issue.to_string();
        let operation = if add { "--add-label" } else { "--remove-label" };
        self.run(
            Some(repository),
            &["issue", "edit", &issue, operation, label],
            cancellation,
        )
        .await?;
        Ok(())
    }

    pub async fn find_or_create_proposal(
        &self,
        repository: &Path,
        name: &str,
        request: ProposalIssueRequest<'_>,
        cancellation: &CancellationToken,
    ) -> Result<ProposalIssue> {
        let ProposalIssueRequest {
            title,
            body,
            proposed_label,
            marker,
        } = request;
        validate_label(proposed_label)?;
        validate_proposal_marker(marker)?;
        let labels = self.labels(repository, cancellation).await?;
        if !labels
            .iter()
            .any(|label| label.eq_ignore_ascii_case(proposed_label))
        {
            self.create_label(
                repository,
                proposed_label,
                "Proposed by Factory for human review",
                "5319E7",
                cancellation,
            )
            .await?;
        }
        let issues: Vec<ApiProposalIssue> = self
            .api_pages(
                repository,
                &format!("repos/{name}/issues?state=all&per_page=100"),
                cancellation,
            )
            .await?;
        let mut matches = issues.into_iter().filter(|issue| {
            issue.pull_request.is_none()
                && issue
                    .body
                    .as_deref()
                    .is_some_and(|body| body.contains(marker))
        });
        if let Some(issue) = matches.next() {
            if matches.next().is_some() {
                bail!("multiple GitHub issues contain proposal marker {marker:?}");
            }
            if !issue
                .labels
                .iter()
                .any(|label| label.name.eq_ignore_ascii_case(proposed_label))
            {
                self.edit_issue_label(repository, issue.number, proposed_label, true, cancellation)
                    .await?;
            }
            return Ok(ProposalIssue {
                number: issue.number,
                url: issue.html_url,
                created: false,
            });
        }

        let proposal_body = if body.ends_with('\n') {
            format!("{body}\n{marker}")
        } else {
            format!("{body}\n\n{marker}")
        };
        let endpoint = format!("repos/{name}/issues");
        let response: ApiCreatedIssue = self
            .run_json(
                Some(repository),
                &[
                    "api",
                    "--method",
                    "POST",
                    &endpoint,
                    "-f",
                    &format!("title={title}"),
                    "-f",
                    &format!("body={proposal_body}"),
                    "-f",
                    &format!("labels[]={proposed_label}"),
                ],
                cancellation,
            )
            .await
            .context("failed to create GitHub proposal issue")?;
        if !response
            .labels
            .iter()
            .any(|label| label.name.eq_ignore_ascii_case(proposed_label))
        {
            bail!(
                "GitHub created proposal issue #{} without configured label {proposed_label:?}",
                response.number
            );
        }
        Ok(ProposalIssue {
            number: response.number,
            url: response.html_url,
            created: true,
        })
    }

    pub async fn publish_draft_pull_request(
        &self,
        repository: &Path,
        name: &str,
        request: DraftPullRequestRequest<'_>,
        cancellation: &CancellationToken,
    ) -> Result<DraftPullRequest> {
        let DraftPullRequestRequest {
            head_branch,
            base_branch,
            title,
            body,
        } = request;
        validate_branch_name(head_branch, "head")?;
        validate_branch_name(base_branch, "base")?;
        if let Some(pull) = self
            .draft_pull_request_for_head(repository, name, head_branch, cancellation)
            .await?
        {
            let endpoint = format!("repos/{name}/pulls/{}", pull.number);
            let response: ApiPullRequest = self
                .run_json(
                    Some(repository),
                    &[
                        "api",
                        "--method",
                        "PATCH",
                        &endpoint,
                        "-f",
                        &format!("title={title}"),
                        "-f",
                        &format!("body={body}"),
                        "-f",
                        &format!("base={base_branch}"),
                    ],
                    cancellation,
                )
                .await
                .context("failed to update GitHub draft pull request")?;
            validate_published_pull_request(&response, head_branch)?;
            return Ok(DraftPullRequest {
                number: response.number,
                url: response.html_url,
                created: false,
            });
        }

        let endpoint = format!("repos/{name}/pulls");
        let response: ApiPullRequest = self
            .run_json(
                Some(repository),
                &[
                    "api",
                    "--method",
                    "POST",
                    &endpoint,
                    "-f",
                    &format!("title={title}"),
                    "-f",
                    &format!("body={body}"),
                    "-f",
                    &format!("head={head_branch}"),
                    "-f",
                    &format!("base={base_branch}"),
                    "-F",
                    "draft=true",
                ],
                cancellation,
            )
            .await
            .context("failed to create GitHub draft pull request")?;
        validate_published_pull_request(&response, head_branch)?;
        Ok(DraftPullRequest {
            number: response.number,
            url: response.html_url,
            created: true,
        })
    }

    pub async fn validate_draft_pull_request_target(
        &self,
        repository: &Path,
        name: &str,
        head_branch: &str,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        validate_branch_name(head_branch, "head")?;
        self.draft_pull_request_for_head(repository, name, head_branch, cancellation)
            .await?;
        Ok(())
    }

    async fn draft_pull_request_for_head(
        &self,
        repository: &Path,
        name: &str,
        head_branch: &str,
        cancellation: &CancellationToken,
    ) -> Result<Option<ApiPullRequest>> {
        let pulls: Vec<ApiPullRequest> = self
            .api_pages(
                repository,
                &format!("repos/{name}/pulls?state=all&per_page=100"),
                cancellation,
            )
            .await?;
        let mut matches = pulls.into_iter().filter(|pull| {
            pull.head.reference == head_branch
                && pull
                    .head
                    .repository
                    .as_ref()
                    .is_some_and(|repository| repository.full_name == name)
        });
        let Some(pull) = matches.next() else {
            return Ok(None);
        };
        if matches.next().is_some() {
            bail!("multiple pull requests use head branch {head_branch:?}");
        }
        if pull.merged_at.is_some() {
            bail!(
                "pull request #{} for head branch {head_branch:?} is already merged",
                pull.number
            );
        }
        if pull.state != "open" {
            bail!(
                "pull request #{} for head branch {head_branch:?} is closed",
                pull.number
            );
        }
        if !pull.draft {
            bail!(
                "pull request #{} for head branch {head_branch:?} is not a draft",
                pull.number
            );
        }
        Ok(Some(pull))
    }

    pub async fn authorize_claim(
        &self,
        repository: &Path,
        config: &GitHubConfig,
        task: &Task,
        workflow_hash: &str,
        ledger: &mut Ledger,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let payload = task
            .payload
            .as_deref()
            .context("ticket task has no approved source payload")?;
        let approved: TicketContext =
            serde_json::from_str(payload).context("ticket task approval payload is invalid")?;
        let issue_number = task
            .source_item
            .as_deref()
            .context("ticket task has no issue number")?
            .parse::<u64>()
            .context("ticket task issue number is invalid")?;
        if approved.number != issue_number || approved.approval.workflow_hash != workflow_hash {
            bail!("ticket task approval does not match its issue or workflow revision");
        }
        if ledger.issue_approval_is_reserved(&task.repository, issue_number)? {
            bail!("issue approval is being replaced; refusing to launch the task");
        }
        let locally_consumed = ledger.task_has_consumed_approval(task.id)?;
        let trusted = self
            .trusted_approver_ids(repository, config, cancellation)
            .await?;
        let actor = self.current_user(cancellation).await?;
        if !trusted.contains_key(&actor.id) {
            bail!("authenticated GitHub user is not trusted to claim approved work");
        }
        if !trusted.contains_key(&approved.approval.approver_id) {
            bail!("ticket approval actor is no longer trusted");
        }
        let issue = self
            .issue(repository, &task.repository, issue_number, cancellation)
            .await?;
        if issue.state != "open" {
            bail!("approved issue is no longer open");
        }
        let queued_content_hash = ticket_context_hash(&approved, &task.workflow, workflow_hash)?;
        if queued_content_hash != approved.approval.approved_content_hash
            || approved.url != issue.url
        {
            bail!("queued ticket context does not match its approval artifact");
        }
        if !locally_consumed && !issue.labels.contains(&config.ready_label) {
            bail!("approved issue no longer has the ready label");
        }
        let comments = self
            .issue_comments(repository, &task.repository, issue_number, cancellation)
            .await?;
        let comment = comments
            .iter()
            .find(|comment| comment.id == approved.approval.artifact_id)
            .context("approval artifact no longer exists")?;
        let artifact = parse_approval(comment.body.as_deref().unwrap_or_default())
            .context("approval artifact is malformed")?;
        if comment.user.id != approved.approval.approver_id
            || artifact.approver_id != approved.approval.approver_id
            || artifact.issue != issue_number
            || artifact.workflow_id != task.workflow
            || artifact.workflow_hash != workflow_hash
            || artifact.approved_content_hash != approved.approval.approved_content_hash
            || artifact.nonce != approved.approval.nonce
        {
            bail!("approval artifact does not match the claimed task");
        }
        let remotely_claimed = matching_claim(
            &comments,
            approved.approval.artifact_id,
            approved.approval.label_event_id,
            task.id,
            &trusted,
        );
        let conflicting_claim = conflicting_claim(
            &comments,
            approved.approval.artifact_id,
            approved.approval.label_event_id,
            &trusted,
        );
        if conflicting_claim.is_some() && remotely_claimed.is_none() {
            bail!("approval artifact or label event has a conflicting GitHub claim record");
        }
        if !locally_consumed && conflicting_claim.is_some() {
            bail!("approval artifact or label event was already claimed on GitHub");
        }
        let timeline = self
            .issue_timeline(repository, &task.repository, issue_number, cancellation)
            .await?;
        let latest_label_event = latest_ready_event(&timeline, &config.ready_label)
            .context("ready-label approval event no longer exists")?;
        if latest_label_event.id != approved.approval.label_event_id
            || latest_label_event.actor.id != approved.approval.approver_id
            || !trusted.contains_key(&latest_label_event.actor.id)
            || latest_label_event.created_at < comment.created_at
        {
            bail!("ready-label event does not match the claimed approval");
        }
        let content_hash = approved_content_hash(
            issue.number,
            &issue.title,
            &issue.body,
            &task.workflow,
            workflow_hash,
        )?;
        if content_hash != approved.approval.approved_content_hash {
            bail!("issue title or body changed after approval");
        }
        let evidence = ApprovalEvidence {
            artifact_id: approved.approval.artifact_id,
            label_event_id: approved.approval.label_event_id,
            approver_id: approved.approval.approver_id,
            content_hash: &content_hash,
            workflow_hash,
            source_revision: &issue.updated_at,
        };
        if locally_consumed {
            if !ledger.task_consumed_exact_approval(task.id, &evidence)? {
                bail!("durable task approval does not match its queued evidence");
            }
        } else {
            ledger.consume_task_approval(task.id, &evidence)?;
        }
        if issue.labels.contains(&config.ready_label) {
            self.edit_issue_label(
                repository,
                issue_number,
                &config.ready_label,
                false,
                cancellation,
            )
            .await?;
        }
        if remotely_claimed.is_none() {
            let claim = ClaimArtifact {
                version: 1,
                task_id: task.id,
                approval_artifact_id: approved.approval.artifact_id,
                label_event_id: approved.approval.label_event_id,
            };
            self.post_issue_comment(
                repository,
                &task.repository,
                issue_number,
                &render_claim(&claim)?,
                cancellation,
            )
            .await?;
        }
        let verified = self
            .issue(repository, &task.repository, issue_number, cancellation)
            .await?;
        if verified.state != "open"
            || verified.labels.contains(&config.ready_label)
            || approved_content_hash(
                verified.number,
                &verified.title,
                &verified.body,
                &task.workflow,
                workflow_hash,
            )? != content_hash
        {
            bail!("issue changed concurrently while Factory claimed its approval");
        }
        let timeline = self
            .issue_timeline(repository, &task.repository, issue_number, cancellation)
            .await?;
        if latest_ready_event(&timeline, &config.ready_label).map(|event| event.id)
            != Some(approved.approval.label_event_id)
        {
            bail!("a concurrent ready-label approval superseded the claimed task");
        }
        let comments = self
            .issue_comments(repository, &task.repository, issue_number, cancellation)
            .await?;
        let latest_approval = comments
            .iter()
            .rev()
            .find(|comment| {
                comment
                    .body
                    .as_deref()
                    .is_some_and(|body| body.contains("<!-- factory-approval:"))
            })
            .and_then(|comment| parse_approval(comment.body.as_deref()?).map(|_| comment.id));
        if latest_approval != Some(approved.approval.artifact_id) {
            bail!("a concurrent approval artifact superseded the claimed task");
        }
        if matching_claim(
            &comments,
            approved.approval.artifact_id,
            approved.approval.label_event_id,
            task.id,
            &trusted,
        )
        .is_none()
        {
            bail!("durable GitHub claim record was not visible after claiming");
        }
        Ok(())
    }

    pub async fn poll_once(
        &self,
        config: &Config,
        catalog: &WorkflowCatalog,
        ledger: &mut Ledger,
    ) -> Result<PollReport> {
        self.poll_once_with_cancellation(config, catalog, ledger, CancellationToken::new())
            .await
    }

    pub async fn poll_once_with_cancellation(
        &self,
        config: &Config,
        catalog: &WorkflowCatalog,
        ledger: &mut Ledger,
        cancellation: CancellationToken,
    ) -> Result<PollReport> {
        self.validate_global(&cancellation).await?;
        let workflows = label_workflows(catalog, &config.github.ready_label);
        let mut repositories = Vec::with_capacity(config.repositories.len());
        for repository in &config.repositories {
            let result = self
                .poll_repository(
                    repository,
                    workflows.get(repository),
                    &config.github,
                    ledger,
                    &cancellation,
                )
                .await;
            repositories.push(match result {
                Ok(report) => report,
                Err(error) => RepositoryPoll {
                    repository: repository.clone(),
                    name_with_owner: None,
                    issues_seen: 0,
                    tasks_created: 0,
                    error: Some(format!("{error:#}")),
                },
            });
        }
        Ok(PollReport { repositories })
    }

    pub async fn poll_until_cancelled<F>(
        &self,
        config: &Config,
        catalog: &WorkflowCatalog,
        ledger: &mut Ledger,
        cancellation: CancellationToken,
        mut on_poll: F,
    ) -> Result<()>
    where
        F: FnMut(&PollReport),
    {
        loop {
            if cancellation.is_cancelled() {
                return Ok(());
            }
            let report = match self
                .poll_once_with_cancellation(config, catalog, ledger, cancellation.clone())
                .await
            {
                Ok(report) => report,
                Err(_) if cancellation.is_cancelled() => return Ok(()),
                Err(error) => return Err(error),
            };
            on_poll(&report);
            tokio::select! {
                _ = cancellation.cancelled() => return Ok(()),
                _ = tokio::time::sleep(config.poll_every) => {}
            }
        }
    }

    async fn poll_repository(
        &self,
        repository: &Path,
        workflows: Option<&Vec<&WorkflowEntry>>,
        github_config: &GitHubConfig,
        ledger: &mut Ledger,
        cancellation: &CancellationToken,
    ) -> Result<RepositoryPoll> {
        let name = self.validate_repository(repository, cancellation).await?;
        let issues: Vec<ApiIssue> = self
            .api_pages(
                repository,
                &format!("repos/{name}/issues?state=open&per_page=100"),
                cancellation,
            )
            .await?;
        let issues = issues
            .into_iter()
            .filter(|issue| issue.pull_request.is_none())
            .collect::<Vec<_>>();
        let trusted_approvers = self
            .trusted_approver_ids(repository, github_config, cancellation)
            .await?;
        let mut tasks_created = 0;
        for workflow in workflows.into_iter().flatten() {
            let Trigger::Label(label) = workflow.trigger.as_ref().expect("filtered workflow")
            else {
                unreachable!();
            };
            let mut observations = Vec::with_capacity(issues.len());
            for issue in &issues {
                let label_present = issue.labels.iter().any(|item| item.name == *label);
                let approval_reserved = ledger.issue_approval_is_reserved(&name, issue.number)?;
                let approved = if label_present && !approval_reserved {
                    let comments = self
                        .issue_comments(repository, &name, issue.number, cancellation)
                        .await?;
                    let timeline = self
                        .issue_timeline(repository, &name, issue.number, cancellation)
                        .await?;
                    approved_ticket(
                        issue,
                        workflow,
                        label,
                        &comments,
                        &timeline,
                        &trusted_approvers,
                        ledger,
                    )?
                } else {
                    None
                };
                let (eligible, revision, payload) = approved.map_or_else(
                    || (false, issue.updated_at.clone(), None),
                    |(revision, context)| (true, revision, Some(context)),
                );
                observations.push(ObservedTicket {
                    source_item: issue.number.to_string(),
                    revision,
                    eligible,
                    payload: payload
                        .map(|context| serde_json::to_string(&context))
                        .transpose()
                        .context("failed to serialize ticket context")?
                        .unwrap_or_else(|| "{}".to_owned()),
                });
            }
            tasks_created += ledger
                .reconcile_ticket_poll(&name, &workflow.id, &observations)?
                .into_iter()
                .filter(|task| task.created)
                .count();
        }
        Ok(RepositoryPoll {
            repository: repository.to_owned(),
            name_with_owner: Some(name),
            issues_seen: issues.len(),
            tasks_created,
            error: None,
        })
    }

    async fn trusted_approver_ids(
        &self,
        repository: &Path,
        config: &GitHubConfig,
        cancellation: &CancellationToken,
    ) -> Result<HashMap<u64, String>> {
        let mut users = HashMap::new();
        for login in &config.trusted_approvers {
            let user = self.user(repository, login, cancellation).await?;
            users.insert(user.id, user.login);
        }
        Ok(users)
    }

    async fn api_pages<T: DeserializeOwned>(
        &self,
        repository: &Path,
        endpoint: &str,
        cancellation: &CancellationToken,
    ) -> Result<Vec<T>> {
        let output = self
            .run(
                Some(repository),
                &["api", "--paginate", "--slurp", endpoint],
                cancellation,
            )
            .await?;
        let pages: Vec<Vec<T>> = serde_json::from_str(&output)
            .with_context(|| format!("gh returned malformed paginated JSON for {endpoint}"))?;
        Ok(pages.into_iter().flatten().collect())
    }

    async fn api_json<T: DeserializeOwned>(
        &self,
        repository: Option<&Path>,
        endpoint: &str,
        cancellation: &CancellationToken,
    ) -> Result<T> {
        let output = self
            .run(repository, &["api", endpoint], cancellation)
            .await?;
        serde_json::from_str(&output)
            .with_context(|| format!("gh returned malformed JSON for {endpoint}"))
    }

    async fn run_json<T: DeserializeOwned>(
        &self,
        repository: Option<&Path>,
        arguments: &[&str],
        cancellation: &CancellationToken,
    ) -> Result<T> {
        let output = self.run(repository, arguments, cancellation).await?;
        serde_json::from_str(&output).context("gh returned malformed JSON")
    }

    async fn run(
        &self,
        repository: Option<&Path>,
        arguments: &[&str],
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let mut command = Command::new(&self.executable);
        command.args(arguments).env_remove("GH_REPO");
        if let Some(repository) = repository {
            command.current_dir(repository);
        }
        command
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        let child = command.spawn().with_context(|| {
            format!(
                "failed to start GitHub CLI at {}",
                self.executable.display()
            )
        })?;
        let output = tokio::select! {
            _ = cancellation.cancelled() => bail!("GitHub CLI command cancelled"),
            result = tokio::time::timeout(self.command_timeout, child.wait_with_output()) => {
                match result {
                    Ok(output) => output.context("failed to wait for GitHub CLI")?,
                    Err(_) => bail!(
                        "gh {} timed out after {}",
                        arguments.join(" "),
                        humantime::format_duration(self.command_timeout)
                    ),
                }
            }
        };
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            bail!(
                "gh {} failed with status {}: {}",
                arguments.join(" "),
                output.status,
                stderr.trim()
            );
        }
        String::from_utf8(output.stdout).context("gh output was not valid UTF-8")
    }
}

fn label_workflows<'a>(
    catalog: &'a WorkflowCatalog,
    ready_label: &str,
) -> HashMap<PathBuf, Vec<&'a WorkflowEntry>> {
    let mut workflows = HashMap::<PathBuf, Vec<&WorkflowEntry>>::new();
    for workflow in &catalog.entries {
        if workflow.errors.is_empty()
            && matches!(workflow.trigger.as_ref(), Some(Trigger::Label(label)) if label == ready_label)
        {
            workflows
                .entry(workflow.repository.clone())
                .or_default()
                .push(workflow);
        }
    }
    workflows
}

fn approved_ticket(
    issue: &ApiIssue,
    workflow: &WorkflowEntry,
    ready_label: &str,
    comments: &[ApiComment],
    timeline: &[ApiTimelineEvent],
    trusted_approvers: &HashMap<u64, String>,
    ledger: &Ledger,
) -> Result<Option<(String, TicketContext)>> {
    let workflow_hash = workflow_content_hash(workflow)?;
    let Some(comment) = comments.iter().rev().find(|comment| {
        comment
            .body
            .as_deref()
            .is_some_and(|body| body.contains("<!-- factory-approval:"))
    }) else {
        return Ok(None);
    };
    let Some(artifact) = parse_approval(comment.body.as_deref().unwrap_or_default()) else {
        return Ok(None);
    };
    if artifact.issue != issue.number
        || artifact.workflow_id != workflow.id
        || artifact.workflow_hash != workflow_hash
        || artifact.approver_id != comment.user.id
        || !trusted_approvers.contains_key(&comment.user.id)
    {
        return Ok(None);
    }
    if ledger.approval_is_consumed(comment.id)? {
        return Ok(None);
    }
    let Some(event) = latest_ready_event(timeline, ready_label) else {
        return Ok(None);
    };
    if event.actor.id != comment.user.id
        || !trusted_approvers.contains_key(&event.actor.id)
        || event.created_at < comment.created_at
    {
        return Ok(None);
    }
    if conflicting_claim(comments, comment.id, event.id, trusted_approvers).is_some() {
        return Ok(None);
    }
    let body = issue.body.as_deref().unwrap_or_default();
    let content_hash = approved_content_hash(
        issue.number,
        &issue.title,
        body,
        &workflow.id,
        &workflow_hash,
    )?;
    if content_hash != artifact.approved_content_hash {
        return Ok(None);
    }
    let revision = format!("approval:{}:label-event:{}", comment.id, event.id);
    Ok(Some((
        revision,
        TicketContext {
            number: issue.number,
            url: issue.html_url.clone(),
            title: issue.title.clone(),
            body: body.to_owned(),
            observed_revision: issue.updated_at.clone(),
            approval: TicketApproval {
                artifact_id: comment.id,
                label_event_id: event.id,
                approver_id: artifact.approver_id,
                workflow_hash: artifact.workflow_hash,
                approved_content_hash: artifact.approved_content_hash,
                nonce: artifact.nonce,
            },
        },
    )))
}

fn matching_claim<'a>(
    comments: &'a [ApiComment],
    artifact_id: u64,
    label_event_id: u64,
    task_id: i64,
    trusted_approvers: &HashMap<u64, String>,
) -> Option<&'a ApiComment> {
    comments.iter().rev().find(|comment| {
        trusted_approvers.contains_key(&comment.user.id)
            && parse_claim(comment.body.as_deref().unwrap_or_default()).is_some_and(|claim| {
                claim.approval_artifact_id == artifact_id
                    && claim.label_event_id == label_event_id
                    && claim.task_id == task_id
            })
    })
}

fn conflicting_claim<'a>(
    comments: &'a [ApiComment],
    artifact_id: u64,
    label_event_id: u64,
    trusted_approvers: &HashMap<u64, String>,
) -> Option<&'a ApiComment> {
    comments.iter().rev().find(|comment| {
        trusted_approvers.contains_key(&comment.user.id)
            && parse_claim(comment.body.as_deref().unwrap_or_default()).is_some_and(|claim| {
                claim.approval_artifact_id == artifact_id || claim.label_event_id == label_event_id
            })
    })
}

fn latest_ready_event<'a>(
    timeline: &'a [ApiTimelineEvent],
    ready_label: &str,
) -> Option<&'a ApiTimelineEvent> {
    timeline.iter().rev().find(|event| {
        event.event == "labeled"
            && event
                .label
                .as_ref()
                .is_some_and(|label| label.name == ready_label)
    })
}

fn ticket_context_hash(
    context: &TicketContext,
    workflow_id: &str,
    workflow_hash: &str,
) -> Result<String> {
    approved_content_hash(
        context.number,
        &context.title,
        &context.body,
        workflow_id,
        workflow_hash,
    )
}

fn valid_name_with_owner(value: &str) -> bool {
    let mut parts = value.split('/');
    matches!((parts.next(), parts.next(), parts.next()), (Some(owner), Some(repository), None) if !owner.is_empty() && !repository.is_empty())
}

fn validate_proposal_marker(marker: &str) -> Result<()> {
    if marker.len() > 256
        || !marker.starts_with("<!-- factory-proposal:")
        || !marker.ends_with(" -->")
        || marker
            .chars()
            .any(|character| matches!(character, '\0' | '\n' | '\r'))
    {
        bail!("invalid Factory proposal marker");
    }
    Ok(())
}

fn validate_label(label: &str) -> Result<()> {
    if label.is_empty()
        || label.len() > 50
        || label
            .chars()
            .any(|character| matches!(character, '\0' | '\n' | '\r'))
    {
        bail!("invalid GitHub proposal label {label:?}");
    }
    Ok(())
}

fn validate_branch_name(branch: &str, role: &str) -> Result<()> {
    if branch.is_empty()
        || branch.starts_with('-')
        || branch.len() > 255
        || branch
            .chars()
            .any(|character| matches!(character, '\0' | '\n' | '\r'))
    {
        bail!("invalid {role} branch {branch:?}");
    }
    Ok(())
}

fn validate_published_pull_request(pull: &ApiPullRequest, head_branch: &str) -> Result<()> {
    if !pull.draft {
        bail!("GitHub returned pull request #{} as ready", pull.number);
    }
    if pull.head.reference != head_branch {
        bail!(
            "GitHub returned pull request #{} for unexpected head branch {:?}",
            pull.number,
            pull.head.reference
        );
    }
    Ok(())
}

#[derive(Debug, Deserialize)]
struct ApiIssue {
    number: u64,
    html_url: String,
    title: String,
    body: Option<String>,
    labels: Vec<ApiLabel>,
    updated_at: String,
    #[serde(default = "open_state")]
    state: String,
    pull_request: Option<serde_json::Value>,
}

#[derive(Debug, Deserialize)]
struct ApiProposalIssue {
    number: u64,
    html_url: String,
    body: Option<String>,
    labels: Vec<ApiLabel>,
    pull_request: Option<serde_json::Value>,
}

#[derive(Debug, Deserialize)]
struct ApiCreatedIssue {
    number: u64,
    html_url: String,
    labels: Vec<ApiLabel>,
}

#[derive(Debug, Deserialize)]
struct ApiPullRequest {
    number: u64,
    html_url: String,
    draft: bool,
    state: String,
    merged_at: Option<String>,
    head: ApiPullRequestHead,
}

#[derive(Debug, Deserialize)]
struct ApiPullRequestHead {
    #[serde(rename = "ref")]
    reference: String,
    #[serde(rename = "repo")]
    repository: Option<ApiPullRequestRepository>,
}

#[derive(Debug, Deserialize)]
struct ApiPullRequestRepository {
    full_name: String,
}

fn open_state() -> String {
    "open".to_owned()
}

#[derive(Debug, Deserialize)]
pub struct ApiLabel {
    pub name: String,
}

#[derive(Debug, Deserialize)]
pub struct ApiComment {
    pub id: u64,
    pub html_url: String,
    pub user: ApiUser,
    pub body: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Deserialize)]
pub struct ApiUser {
    pub id: u64,
    pub login: String,
}

#[derive(Debug, Deserialize)]
pub struct ApiTimelineEvent {
    pub id: u64,
    pub event: String,
    pub actor: ApiUser,
    pub label: Option<ApiLabel>,
    pub created_at: String,
}

impl From<ApiIssue> for IssueSnapshot {
    fn from(issue: ApiIssue) -> Self {
        Self {
            number: issue.number,
            url: issue.html_url,
            title: issue.title,
            body: issue.body.unwrap_or_default(),
            state: issue.state,
            labels: issue.labels.into_iter().map(|label| label.name).collect(),
            updated_at: issue.updated_at,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::approval::{ClaimArtifact, render_claim};

    #[test]
    fn exact_claim_requires_the_same_task_while_replay_detection_is_global() {
        let claim = render_claim(&ClaimArtifact {
            version: 1,
            task_id: 99,
            approval_artifact_id: 10,
            label_event_id: 20,
        })
        .unwrap();
        let comments: Vec<ApiComment> = serde_json::from_value(serde_json::json!([{
            "id": 30,
            "html_url": "https://example/comment/30",
            "user": {"id": 42, "login": "trusted"},
            "body": claim,
            "created_at": "2026-01-01T00:00:00Z",
            "updated_at": "2026-01-01T00:00:00Z"
        }]))
        .unwrap();
        let trusted = HashMap::from([(42, "trusted".to_owned())]);

        assert!(matching_claim(&comments, 10, 20, 1, &trusted).is_none());
        assert!(conflicting_claim(&comments, 10, 20, &trusted).is_some());
    }

    #[test]
    fn agent_visible_ticket_context_is_bound_to_the_approval_hash() {
        let expected = approved_content_hash(7, "Title", "Body", "deliver", "workflow").unwrap();
        let mut context = TicketContext {
            number: 7,
            url: "https://example/issues/7".into(),
            title: "Title".into(),
            body: "Body".into(),
            observed_revision: "revision".into(),
            approval: TicketApproval {
                artifact_id: 10,
                label_event_id: 20,
                approver_id: 42,
                workflow_hash: "workflow".into(),
                approved_content_hash: expected.clone(),
                nonce: "nonce".into(),
            },
        };
        assert_eq!(
            ticket_context_hash(&context, "deliver", "workflow").unwrap(),
            expected
        );
        context.title = "Corrupted title".into();
        assert_ne!(
            ticket_context_hash(&context, "deliver", "workflow").unwrap(),
            context.approval.approved_content_hash
        );
    }
}
