use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::{Deserialize, Deserializer, Serialize, de::DeserializeOwned};
use tokio::process::Command;
use tokio_util::sync::CancellationToken;

use crate::approval::{
    ClaimArtifact, approved_content_hash, parse as parse_approval, parse_claim, render_claim,
};
use crate::config::{Config, GitHubConfig, SourceConfig};
pub use crate::source::{PollReport, RepositoryPoll};
use crate::storage::{ApprovalEvidence, Ledger, ObservedTicket, Task};
use crate::workflow::{Trigger, WorkflowCatalog, WorkflowEntry};

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

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ProjectTicketContext {
    pub project_id: String,
    pub project_item_id: String,
    pub status_field_id: String,
    pub issue_node_id: String,
    pub number: u64,
    pub url: String,
    pub title: String,
    pub author_id: String,
    pub author_login: String,
    pub repository: String,
    pub expected_status: String,
    pub expected_option_id: String,
    pub status_updated_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ResolvedProjectSource {
    pub project_id: String,
    pub status_field_id: String,
    pub options: HashMap<String, String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct GitHubUser {
    pub id: u64,
    pub login: String,
    #[serde(default)]
    pub node_id: Option<String>,
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
    pub author_id: u64,
    pub author_login: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct LabelTicketContext {
    pub number: u64,
    pub url: String,
    pub title: String,
    pub author_id: u64,
    pub author_login: String,
    pub repository: String,
    pub expected_label: String,
    pub label_event_id: u64,
}

#[derive(Debug, Deserialize)]
struct ApiRepository {
    default_branch: String,
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

    pub async fn validate_token_env(
        &self,
        environment: &str,
        cancellation: &CancellationToken,
    ) -> Result<GitHubUser> {
        let token = std::env::var(environment)
            .with_context(|| format!("GitHub token is missing; export {environment}"))?;
        if token.trim().is_empty() {
            bail!("{environment} must not be empty");
        }
        let output = self
            .run_with_token(None, &["api", "user"], Some(&token), cancellation)
            .await
            .with_context(|| format!("GitHub token from {environment} is not valid"))?;
        serde_json::from_str(&output)
            .context("GitHub token validation returned malformed user JSON")
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

    pub async fn validate_project_source(
        &self,
        repository: &Path,
        source: &SourceConfig,
        statuses: &[String],
        cancellation: &CancellationToken,
    ) -> Result<ResolvedProjectSource> {
        self.validate_repository(repository, cancellation).await?;
        self.trusted_source_user_ids(repository, source, cancellation)
            .await?;
        let resolved = self
            .resolve_project_source(repository, source, cancellation)
            .await?;
        for status in statuses {
            if !resolved.options.contains_key(status) {
                bail!(
                    "GitHub Project field {:?} has no option {:?} required by a status trigger",
                    source.status_field,
                    status
                );
            }
        }
        Ok(resolved)
    }

    pub async fn validate_issue_source(
        &self,
        repository: &Path,
        source: &SourceConfig,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.validate_repository(repository, cancellation).await?;
        self.trusted_source_numeric_user_ids(repository, source, cancellation)
            .await?;
        Ok(())
    }

    pub async fn authorize_project_claim(
        &self,
        repository: &Path,
        source: &SourceConfig,
        task: &Task,
        _ledger: &mut Ledger,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let payload = task
            .payload
            .as_deref()
            .context("project task has no source payload")?;
        let expected: ProjectTicketContext =
            serde_json::from_str(payload).context("project task source payload is invalid")?;
        let source_item = task
            .source_item
            .as_deref()
            .context("project task has no issue number")?;
        if source_item != expected.number.to_string() || task.repository != expected.repository {
            bail!("project task payload does not match its durable identity");
        }
        let repository_name = self.validate_repository(repository, cancellation).await?;
        if repository_name != task.repository {
            bail!("project task repository no longer matches the configured checkout");
        }
        let resolved = self
            .resolve_project_source(repository, source, cancellation)
            .await?;
        let trusted = self
            .trusted_source_user_ids(repository, source, cancellation)
            .await?;
        if !trusted.contains_key(&expected.author_id) {
            bail!("project issue author is no longer trusted");
        }
        let current_expected_option = resolved
            .options
            .get(&expected.expected_status)
            .context("queued expected state is no longer configured")?;
        if resolved.project_id != expected.project_id
            || resolved.status_field_id != expected.status_field_id
            || current_expected_option != &expected.expected_option_id
        {
            bail!("project source configuration changed after this task was queued");
        }
        let current = self
            .project_item(
                repository,
                &expected.project_item_id,
                &expected.status_field_id,
                cancellation,
            )
            .await?
            .context("project item no longer exists or is not an issue")?;
        validate_project_item(&current, &expected, &expected.expected_option_id)
    }

    pub async fn authorize_label_claim(
        &self,
        repository: &Path,
        source: &SourceConfig,
        expected_label: &str,
        task: &Task,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let payload = task
            .payload
            .as_deref()
            .context("label task has no source payload")?;
        let expected: LabelTicketContext =
            serde_json::from_str(payload).context("label task source payload is invalid")?;
        let source_item = task
            .source_item
            .as_deref()
            .context("label task has no issue number")?;
        if source_item != expected.number.to_string()
            || task.repository != expected.repository
            || expected.expected_label != expected_label
        {
            bail!("label task payload does not match its durable identity");
        }
        let repository_name = self.validate_repository(repository, cancellation).await?;
        if repository_name != task.repository {
            bail!("label task repository no longer matches the configured checkout");
        }
        let trusted = self
            .trusted_source_numeric_user_ids(repository, source, cancellation)
            .await?;
        let issue = self
            .issue(repository, &task.repository, expected.number, cancellation)
            .await?;
        if issue.state != "open"
            || issue.author_id != expected.author_id
            || issue.author_login != expected.author_login
            || issue.url != expected.url
            || !trusted.contains_key(&issue.author_id)
            || !issue.labels.iter().any(|label| label == expected_label)
        {
            bail!("issue author, state, URL, or label changed before claim");
        }
        let timeline = self
            .issue_timeline(repository, &task.repository, expected.number, cancellation)
            .await?;
        if latest_label_event_id(&timeline, expected_label) != Some(expected.label_event_id) {
            bail!("issue label entry changed before claim");
        }
        Ok(())
    }

    async fn resolve_project_source(
        &self,
        repository: &Path,
        source: &SourceConfig,
        cancellation: &CancellationToken,
    ) -> Result<ResolvedProjectSource> {
        let project_number = source.project_number.to_string();
        let project_output = self
            .run(
                Some(repository),
                &[
                    "project",
                    "view",
                    &project_number,
                    "--owner",
                    &source.owner,
                    "--format",
                    "json",
                ],
                cancellation,
            )
            .await
            .with_context(|| {
                format!(
                    "failed to read GitHub Project {} for owner {:?}",
                    source.project_number, source.owner
                )
            })?;
        let project: ProjectView = serde_json::from_str(&project_output)
            .context("gh project view returned malformed JSON")?;
        if project.id.trim().is_empty() {
            bail!("GitHub Project returned an empty node ID");
        }
        let fields_output = self
            .run(
                Some(repository),
                &[
                    "project",
                    "field-list",
                    &project_number,
                    "--owner",
                    &source.owner,
                    "--limit",
                    "1000",
                    "--format",
                    "json",
                ],
                cancellation,
            )
            .await
            .context("failed to list GitHub Project fields")?;
        let fields: ProjectFieldList = serde_json::from_str(&fields_output)
            .context("gh project field-list returned malformed JSON")?;
        let mut matching_fields = fields
            .fields
            .into_iter()
            .filter(|field| field.name == source.status_field);
        let field = matching_fields.next().with_context(|| {
            format!(
                "GitHub Project does not contain status field {:?}",
                source.status_field
            )
        })?;
        if matching_fields.next().is_some() {
            bail!(
                "GitHub Project contains more than one field named {:?}",
                source.status_field
            );
        }
        if field.kind != "ProjectV2SingleSelectField" {
            bail!(
                "GitHub Project field {:?} must be a single-select field",
                source.status_field
            );
        }
        let mut options = HashMap::new();
        let mut option_ids = HashMap::new();
        for option in field.options {
            if options
                .insert(option.name.clone(), option.id.clone())
                .is_some()
            {
                bail!(
                    "GitHub Project field {:?} contains more than one option named {:?}",
                    source.status_field,
                    option.name
                );
            }
            if let Some(previous_name) = option_ids.insert(option.id, option.name.clone()) {
                bail!(
                    "GitHub Project field {:?} must use a distinct project option ID for {:?} and {:?}",
                    source.status_field,
                    previous_name,
                    option.name
                );
            }
        }
        Ok(ResolvedProjectSource {
            project_id: project.id,
            status_field_id: field.id,
            options,
        })
    }

    async fn trusted_source_user_ids(
        &self,
        repository: &Path,
        source: &SourceConfig,
        cancellation: &CancellationToken,
    ) -> Result<HashMap<String, String>> {
        let mut users = HashMap::new();
        for login in &source.trusted_users {
            let user = self.user(repository, login, cancellation).await?;
            let node_id = user
                .node_id
                .with_context(|| format!("GitHub user {:?} has no stable node ID", user.login))?;
            users.insert(node_id, user.login);
        }
        Ok(users)
    }

    async fn trusted_source_numeric_user_ids(
        &self,
        repository: &Path,
        source: &SourceConfig,
        cancellation: &CancellationToken,
    ) -> Result<HashMap<u64, String>> {
        let mut users = HashMap::new();
        for login in &source.trusted_users {
            let user = self.user(repository, login, cancellation).await?;
            users.insert(user.id, user.login);
        }
        Ok(users)
    }

    async fn project_items(
        &self,
        repository: &Path,
        project_id: &str,
        status_field: &str,
        cancellation: &CancellationToken,
    ) -> Result<Vec<ProjectItem>> {
        const QUERY: &str = "query($project:ID!,$endCursor:String,$statusField:String!){node(id:$project){... on ProjectV2{items(first:100,after:$endCursor){pageInfo{hasNextPage endCursor} nodes{id updatedAt content{... on Issue{id number title url state updatedAt author{login ... on User{id}} repository{nameWithOwner}}} fieldValueByName(name:$statusField){... on ProjectV2ItemFieldSingleSelectValue{optionId name updatedAt}}}}}}}";
        let mut cursor: Option<String> = None;
        let mut items = Vec::new();
        loop {
            let mut arguments = vec![
                "api".to_owned(),
                "graphql".to_owned(),
                "-f".to_owned(),
                format!("query={QUERY}"),
                "-F".to_owned(),
                format!("project={project_id}"),
                "-f".to_owned(),
                format!("statusField={status_field}"),
            ];
            if let Some(cursor) = &cursor {
                arguments.extend(["-f".to_owned(), format!("endCursor={cursor}")]);
            }
            let refs = arguments.iter().map(String::as_str).collect::<Vec<_>>();
            let output = self.run(Some(repository), &refs, cancellation).await?;
            let response: ProjectItemsResponse = serde_json::from_str(&output)
                .context("gh api graphql returned malformed Project items JSON")?;
            let page = response
                .data
                .node
                .context("GitHub Project node was not found")?
                .items;
            items.extend(page.nodes);
            if !page.page_info.has_next_page {
                break;
            }
            cursor = Some(
                page.page_info
                    .end_cursor
                    .context("GitHub Project page has no end cursor")?,
            );
        }
        Ok(items)
    }

    async fn project_item(
        &self,
        repository: &Path,
        item_id: &str,
        status_field_id: &str,
        cancellation: &CancellationToken,
    ) -> Result<Option<ProjectItem>> {
        const QUERY: &str = "query($item:ID!){node(id:$item){... on ProjectV2Item{id updatedAt content{... on Issue{id number title url state updatedAt author{login ... on User{id}} repository{nameWithOwner}}} fieldValues(first:100){nodes{... on ProjectV2ItemFieldSingleSelectValue{optionId name updatedAt field{... on ProjectV2SingleSelectField{id}}}}}}}}";
        let query = format!("query={QUERY}");
        let item = format!("item={item_id}");
        let output = self
            .run(
                Some(repository),
                &["api", "graphql", "-f", &query, "-F", &item],
                cancellation,
            )
            .await?;
        let response: ProjectItemResponse = serde_json::from_str(&output)
            .context("gh api graphql returned malformed Project item JSON")?;
        Ok(response.data.node.map(|item| {
            let field_value = item
                .field_values
                .nodes
                .into_iter()
                .filter_map(|value| serde_json::from_value(value).ok())
                .find(|value: &ProjectStatusValueWithField| value.field.id == status_field_id)
                .map(|value| ProjectStatusValue {
                    option_id: value.option_id,
                    name: value.name,
                    updated_at: value.updated_at,
                });
            ProjectItem {
                id: item.id,
                updated_at: item.updated_at,
                content: item.content,
                field_value,
            }
        }))
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
        let label_workflows = label_workflows(catalog);
        let status_workflows = status_workflows(catalog);
        let mut repositories = Vec::with_capacity(config.repositories.len());
        for repository in &config.repositories {
            let result: Result<RepositoryPoll> = async {
                let source = config
                    .source
                    .as_ref()
                    .context("ticket triggers require a configured source")?;
                let mut report = RepositoryPoll {
                    repository: repository.clone(),
                    name_with_owner: None,
                    issues_seen: 0,
                    tasks_created: 0,
                    error: None,
                };
                if let Some(workflows) = status_workflows.get(repository) {
                    let status = self
                        .poll_project_repository(
                            repository,
                            Some(workflows),
                            source,
                            ledger,
                            &cancellation,
                        )
                        .await?;
                    report.name_with_owner = status.name_with_owner;
                    report.issues_seen = report.issues_seen.max(status.issues_seen);
                    report.tasks_created += status.tasks_created;
                }
                if let Some(workflows) = label_workflows.get(repository) {
                    let labels = self
                        .poll_label_repository(
                            repository,
                            Some(workflows),
                            source,
                            ledger,
                            &cancellation,
                        )
                        .await?;
                    report.name_with_owner = labels.name_with_owner;
                    report.issues_seen = report.issues_seen.max(labels.issues_seen);
                    report.tasks_created += labels.tasks_created;
                }
                if report.name_with_owner.is_none() {
                    report.name_with_owner =
                        Some(self.validate_repository(repository, &cancellation).await?);
                }
                Ok(report)
            }
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

    async fn poll_project_repository(
        &self,
        repository: &Path,
        workflows: Option<&Vec<&WorkflowEntry>>,
        source: &SourceConfig,
        ledger: &mut Ledger,
        cancellation: &CancellationToken,
    ) -> Result<RepositoryPoll> {
        let name = self.validate_repository(repository, cancellation).await?;
        let resolved = self
            .resolve_project_source(repository, source, cancellation)
            .await?;
        let trusted = self
            .trusted_source_user_ids(repository, source, cancellation)
            .await?;
        let items = self
            .project_items(
                repository,
                &resolved.project_id,
                &source.status_field,
                cancellation,
            )
            .await?;
        let issues_seen = items
            .iter()
            .filter(|item| {
                item.content
                    .as_ref()
                    .is_some_and(|issue| issue.repository.name_with_owner == name)
            })
            .count();
        let mut tasks_created = 0;
        for workflow in workflows.into_iter().flatten() {
            let Trigger::Status(expected_status) =
                workflow.trigger.as_ref().expect("filtered workflow")
            else {
                unreachable!();
            };
            let expected_option_id = resolved
                .options
                .get(expected_status)
                .context("trigger status was not found in the configured project field")?;
            let mut observations = Vec::new();
            for item in &items {
                let Some(issue) = &item.content else {
                    continue;
                };
                if issue.repository.name_with_owner != name || issue.state != "OPEN" {
                    continue;
                }
                let Some(author) = &issue.author else {
                    continue;
                };
                let Some(author_id) = author.id.as_deref() else {
                    continue;
                };
                let Some(status) = &item.field_value else {
                    continue;
                };
                let eligible =
                    status.option_id == *expected_option_id && trusted.contains_key(author_id);
                let revision =
                    project_source_revision(&item.id, expected_option_id, &status.updated_at);
                let payload = ProjectTicketContext {
                    project_id: resolved.project_id.clone(),
                    project_item_id: item.id.clone(),
                    status_field_id: resolved.status_field_id.clone(),
                    issue_node_id: issue.id.clone(),
                    number: issue.number,
                    url: issue.url.clone(),
                    title: issue.title.clone(),
                    author_id: author_id.to_owned(),
                    author_login: author.login.clone(),
                    repository: name.clone(),
                    expected_status: expected_status.clone(),
                    expected_option_id: expected_option_id.clone(),
                    status_updated_at: status.updated_at.clone(),
                };
                observations.push(ObservedTicket {
                    source_item: issue.number.to_string(),
                    revision,
                    eligible,
                    payload: serde_json::to_string(&payload)
                        .context("failed to serialize GitHub Project ticket context")?,
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
            issues_seen,
            tasks_created,
            error: None,
        })
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

    async fn poll_label_repository(
        &self,
        repository: &Path,
        workflows: Option<&Vec<&WorkflowEntry>>,
        source: &SourceConfig,
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
        let trusted_users = self
            .trusted_source_numeric_user_ids(repository, source, cancellation)
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
                let context = if label_present && trusted_users.contains_key(&issue.user.id) {
                    let timeline = self
                        .issue_timeline(repository, &name, issue.number, cancellation)
                        .await?;
                    latest_label_event_id(&timeline, label).map(|label_event_id| {
                        LabelTicketContext {
                            number: issue.number,
                            url: issue.html_url.clone(),
                            title: issue.title.clone(),
                            author_id: issue.user.id,
                            author_login: issue.user.login.clone(),
                            repository: name.clone(),
                            expected_label: label.clone(),
                            label_event_id,
                        }
                    })
                } else {
                    None
                };
                let revision = context.as_ref().map_or_else(
                    || format!("issue:{}:label:{label}:absent", issue.number),
                    |context| {
                        format!(
                            "issue:{}:label:{label}:event:{}",
                            issue.number, context.label_event_id
                        )
                    },
                );
                observations.push(ObservedTicket {
                    source_item: issue.number.to_string(),
                    revision,
                    eligible: context.is_some(),
                    payload: context
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

    async fn run(
        &self,
        repository: Option<&Path>,
        arguments: &[&str],
        cancellation: &CancellationToken,
    ) -> Result<String> {
        self.run_with_token(repository, arguments, None, cancellation)
            .await
    }

    async fn run_with_token(
        &self,
        repository: Option<&Path>,
        arguments: &[&str],
        token: Option<&str>,
        cancellation: &CancellationToken,
    ) -> Result<String> {
        let mut command = Command::new(&self.executable);
        command.args(arguments).env_remove("GH_REPO");
        if let Some(token) = token {
            command.env("GH_TOKEN", token);
        }
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
            let stderr = String::from_utf8_lossy(&output.stderr).into_owned();
            let stderr = if let Some(secret) = token {
                stderr.replace(secret, "[REDACTED]")
            } else {
                stderr
            };
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

fn label_workflows(catalog: &WorkflowCatalog) -> HashMap<PathBuf, Vec<&WorkflowEntry>> {
    let mut workflows = HashMap::<PathBuf, Vec<&WorkflowEntry>>::new();
    for workflow in &catalog.entries {
        if workflow.errors.is_empty() && matches!(workflow.trigger, Some(Trigger::Label(_))) {
            workflows
                .entry(workflow.repository.clone())
                .or_default()
                .push(workflow);
        }
    }
    workflows
}

fn latest_label_event_id(events: &[ApiTimelineEvent], label: &str) -> Option<u64> {
    events
        .iter()
        .filter(|event| {
            event.event == "labeled"
                && event
                    .label
                    .as_ref()
                    .is_some_and(|event_label| event_label.name == label)
        })
        .map(|event| event.id)
        .max()
}

fn status_workflows(catalog: &WorkflowCatalog) -> HashMap<PathBuf, Vec<&WorkflowEntry>> {
    let mut workflows = HashMap::<PathBuf, Vec<&WorkflowEntry>>::new();
    for workflow in &catalog.entries {
        if workflow.errors.is_empty()
            && matches!(workflow.trigger.as_ref(), Some(Trigger::Status(_)))
        {
            workflows
                .entry(workflow.repository.clone())
                .or_default()
                .push(workflow);
        }
    }
    workflows
}

fn project_source_revision(item_id: &str, option_id: &str, status_updated_at: &str) -> String {
    format!("project-item:{item_id}:option:{option_id}:at:{status_updated_at}")
}

fn validate_project_item(
    current: &ProjectItem,
    expected: &ProjectTicketContext,
    required_option_id: &str,
) -> Result<()> {
    let issue = current
        .content
        .as_ref()
        .context("project item is no longer an issue")?;
    let status = current
        .field_value
        .as_ref()
        .context("project item no longer has the configured status")?;
    if current.id != expected.project_item_id
        || issue.id != expected.issue_node_id
        || issue.number != expected.number
        || issue.url != expected.url
        || issue.repository.name_with_owner != expected.repository
        || issue.state != "OPEN"
    {
        bail!("project item no longer matches the queued issue");
    }
    let author = issue
        .author
        .as_ref()
        .context("project issue no longer has an author")?;
    if author.id.as_deref() != Some(expected.author_id.as_str())
        || status.option_id != required_option_id
    {
        bail!("project issue author or status changed before claim");
    }
    Ok(())
}

#[derive(Debug, Deserialize)]
struct ProjectView {
    id: String,
}

#[derive(Debug, Deserialize)]
struct ProjectFieldList {
    fields: Vec<ProjectField>,
}

#[derive(Debug, Deserialize)]
struct ProjectField {
    id: String,
    name: String,
    #[serde(rename = "type")]
    kind: String,
    #[serde(default)]
    options: Vec<ProjectOption>,
}

#[derive(Debug, Deserialize)]
struct ProjectOption {
    id: String,
    name: String,
}

#[derive(Debug, Deserialize)]
struct ProjectItemsResponse {
    data: ProjectItemsData,
}

#[derive(Debug, Deserialize)]
struct ProjectItemsData {
    node: Option<ProjectNode>,
}

#[derive(Debug, Deserialize)]
struct ProjectNode {
    items: ProjectItemsConnection,
}

#[derive(Debug, Deserialize)]
struct ProjectItemsConnection {
    #[serde(rename = "pageInfo")]
    page_info: PageInfo,
    nodes: Vec<ProjectItem>,
}

#[derive(Debug, Deserialize)]
struct PageInfo {
    #[serde(rename = "hasNextPage")]
    has_next_page: bool,
    #[serde(rename = "endCursor")]
    end_cursor: Option<String>,
}

#[derive(Debug, Clone, Deserialize)]
struct ProjectItem {
    id: String,
    #[allow(dead_code)]
    #[serde(rename = "updatedAt")]
    updated_at: String,
    #[serde(default, deserialize_with = "deserialize_project_issue")]
    content: Option<ProjectIssue>,
    #[serde(rename = "fieldValueByName", alias = "fieldValue")]
    field_value: Option<ProjectStatusValue>,
}

#[derive(Debug, Clone, Deserialize)]
struct ProjectIssue {
    id: String,
    number: u64,
    title: String,
    url: String,
    state: String,
    #[allow(dead_code)]
    #[serde(rename = "updatedAt")]
    updated_at: String,
    author: Option<ProjectAuthor>,
    repository: ProjectRepository,
}

#[derive(Debug, Clone, Deserialize)]
struct ProjectAuthor {
    id: Option<String>,
    login: String,
}

#[derive(Debug, Clone, Deserialize)]
struct ProjectRepository {
    #[serde(rename = "nameWithOwner")]
    name_with_owner: String,
}

fn deserialize_project_issue<'de, D>(
    deserializer: D,
) -> std::result::Result<Option<ProjectIssue>, D::Error>
where
    D: Deserializer<'de>,
{
    let value = Option::<serde_json::Value>::deserialize(deserializer)?;
    match value {
        Some(value) if value.get("id").is_some() => serde_json::from_value(value)
            .map(Some)
            .map_err(serde::de::Error::custom),
        _ => Ok(None),
    }
}

#[derive(Debug, Clone, Deserialize)]
struct ProjectStatusValue {
    #[serde(rename = "optionId")]
    option_id: String,
    #[allow(dead_code)]
    name: String,
    #[serde(rename = "updatedAt")]
    updated_at: String,
}

#[derive(Debug, Deserialize)]
struct ProjectItemResponse {
    data: ProjectItemData,
}

#[derive(Debug, Deserialize)]
struct ProjectItemData {
    node: Option<ProjectItemWithValues>,
}

#[derive(Debug, Deserialize)]
struct ProjectItemWithValues {
    id: String,
    #[serde(rename = "updatedAt")]
    updated_at: String,
    content: Option<ProjectIssue>,
    #[serde(rename = "fieldValues")]
    field_values: ProjectFieldValues,
}

#[derive(Debug, Deserialize)]
struct ProjectFieldValues {
    nodes: Vec<serde_json::Value>,
}

#[derive(Debug, Deserialize)]
struct ProjectStatusValueWithField {
    #[serde(rename = "optionId")]
    option_id: String,
    name: String,
    #[serde(rename = "updatedAt")]
    updated_at: String,
    field: ProjectValueField,
}

#[derive(Debug, Deserialize)]
struct ProjectValueField {
    id: String,
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
    user: ApiUser,
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
            author_id: issue.user.id,
            author_login: issue.user.login,
        }
    }
}

#[cfg(test)]
mod tests {
    #[cfg(unix)]
    use std::fs;
    #[cfg(unix)]
    use std::os::unix::fs::PermissionsExt;

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

    #[cfg(unix)]
    #[tokio::test]
    async fn worker_token_validation_redacts_a_rejected_secret() {
        const TOKEN_ENV: &str = "FACTORY_GITHUB_VALIDATION_TEST_TOKEN";
        let temp = tempfile::tempdir().unwrap();
        let gh = temp.path().join("gh");
        fs::write(&gh, "#!/bin/sh\nprintf '%s' \"$GH_TOKEN\" >&2\nexit 1\n").unwrap();
        let mut permissions = fs::metadata(&gh).unwrap().permissions();
        permissions.set_mode(0o700);
        fs::set_permissions(&gh, permissions).unwrap();
        unsafe { std::env::set_var(TOKEN_ENV, "worker-secret-value") };

        let error = GitHubClient::new(gh)
            .validate_token_env(TOKEN_ENV, &CancellationToken::new())
            .await
            .unwrap_err();
        let message = format!("{error:#}");
        assert!(message.contains("is not valid"));
        assert!(message.contains("[REDACTED]"));
        assert!(!message.contains("worker-secret-value"));
    }

    #[test]
    fn project_poll_ignores_non_issue_content() {
        let response: ProjectItemsResponse = serde_json::from_value(serde_json::json!({
            "data": {
                "node": {
                    "items": {
                        "pageInfo": {"hasNextPage": false, "endCursor": null},
                        "nodes": [{
                            "id": "PVTI_pull_request",
                            "updatedAt": "2026-07-21T00:00:00Z",
                            "content": {},
                            "fieldValueByName": null
                        }]
                    }
                }
            }
        }))
        .unwrap();

        assert!(response.data.node.unwrap().items.nodes[0].content.is_none());
    }
}
