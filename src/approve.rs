use std::fmt;

use anyhow::{Context, Result, bail};
use tokio_util::sync::CancellationToken;

use crate::approval::{ApprovalArtifact, approved_content_hash, nonce, parse, render};
use crate::config::Config;
use crate::github::GitHubClient;
use crate::storage::Ledger;
use crate::workflow::{Trigger, WorkflowCatalog, WorkflowEntry, workflow_content_hash};

pub struct ApprovalReport {
    pub issue: u64,
    pub workflow: String,
    pub artifact_id: u64,
    pub label_event_id: u64,
    pub approver_id: u64,
}

impl fmt::Display for ApprovalReport {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        writeln!(
            formatter,
            "Approved issue #{} for workflow {}.",
            self.issue, self.workflow
        )?;
        writeln!(formatter, "approval_artifact: {}", self.artifact_id)?;
        writeln!(formatter, "ready_label_event: {}", self.label_event_id)?;
        writeln!(formatter, "approver_id: {}", self.approver_id)
    }
}

pub async fn approve_issue(
    config: &Config,
    catalog: &WorkflowCatalog,
    ledger: &mut Ledger,
    issue_number: u64,
    github: &GitHubClient,
) -> Result<ApprovalReport> {
    let cancellation = CancellationToken::new();
    github.validate_global(&cancellation).await?;
    let repository = &config.repositories[0];
    let name = github
        .validate_repository(repository, &cancellation)
        .await?;
    let workflow = delivery_workflow(config, catalog)?;
    let mut trusted_ids = Vec::new();
    for login in &config.github.trusted_approvers {
        trusted_ids.push(github.user(repository, login, &cancellation).await?.id);
    }
    let approver = github.current_user(&cancellation).await?;
    if !trusted_ids.contains(&approver.id) {
        bail!(
            "authenticated GitHub user {} ({}) is not a configured trusted approver",
            approver.login,
            approver.id
        );
    }
    let mut issue = github
        .issue(repository, &name, issue_number, &cancellation)
        .await?;
    if issue.state != "open" {
        bail!("issue #{issue_number} is not open");
    }
    let reservation_id = nonce(issue_number, approver.id)?;
    ledger.reserve_issue_approval(&name, issue_number, &reservation_id)?;
    let result = async {
        if issue.labels.contains(&config.github.ready_label) {
            github
                .edit_issue_label(
                    repository,
                    issue_number,
                    &config.github.ready_label,
                    false,
                    &cancellation,
                )
                .await?;
            issue = github
                .issue(repository, &name, issue_number, &cancellation)
                .await?;
            if issue.labels.contains(&config.github.ready_label) {
                bail!("GitHub did not confirm removal of the existing ready label");
            }
        }
        let workflow_hash = workflow_content_hash(workflow)?;
        let content_hash = approved_content_hash(
            issue.number,
            &issue.title,
            &issue.body,
            &workflow.id,
            &workflow_hash,
        )?;
        let artifact = ApprovalArtifact {
            version: 1,
            issue: issue.number,
            workflow_id: workflow.id.clone(),
            workflow_hash: workflow_hash.clone(),
            approved_content_hash: content_hash.clone(),
            approver_id: approver.id,
            nonce: nonce(issue.number, approver.id)?,
        };
        let artifact_id = github
            .post_issue_comment(
                repository,
                &name,
                issue_number,
                &render(&artifact)?,
                &cancellation,
            )
            .await?;
        github
            .edit_issue_label(
                repository,
                issue_number,
                &config.github.ready_label,
                true,
                &cancellation,
            )
            .await?;

        let verification = async {
            let verified = github
                .issue(repository, &name, issue_number, &cancellation)
                .await?;
            if verified.state != "open" || !verified.labels.contains(&config.github.ready_label) {
                bail!("issue is no longer open and ready after approval");
            }
            let verified_hash = approved_content_hash(
                verified.number,
                &verified.title,
                &verified.body,
                &workflow.id,
                &workflow_hash,
            )?;
            if verified_hash != content_hash {
                bail!("issue title or body changed while approval was being applied");
            }
            let comments = github
                .issue_comments(repository, &name, issue_number, &cancellation)
                .await?;
            let comment = comments
                .iter()
                .find(|comment| comment.id == artifact_id)
                .context("approval artifact was not visible after creation")?;
            if comment.user.id != approver.id {
                bail!("approval artifact author changed unexpectedly");
            }
            if parse(comment.body.as_deref().unwrap_or_default()).as_ref() != Some(&artifact) {
                bail!("approval artifact content changed unexpectedly");
            }
            let timeline = github
                .issue_timeline(repository, &name, issue_number, &cancellation)
                .await?;
            let event = timeline
                .iter()
                .rev()
                .find(|event| {
                    event.event == "labeled"
                        && event
                            .label
                            .as_ref()
                            .is_some_and(|label| label.name == config.github.ready_label)
                })
                .context("new ready-label event was not visible after approval")?;
            if event.actor.id != approver.id || event.created_at < comment.created_at {
                bail!("latest ready-label event was not created by the trusted approver");
            }
            Ok::<u64, anyhow::Error>(event.id)
        }
        .await;
        let label_event_id = match verification {
            Ok(event) => event,
            Err(error) => {
                let _ = github
                    .edit_issue_label(
                        repository,
                        issue_number,
                        &config.github.ready_label,
                        false,
                        &cancellation,
                    )
                    .await;
                Err(error).context("approval failed closed")?
            }
        };
        Ok(ApprovalReport {
            issue: issue_number,
            workflow: workflow.id.clone(),
            artifact_id,
            label_event_id,
            approver_id: approver.id,
        })
    }
    .await;
    let release = ledger.release_issue_approval(&name, issue_number, &reservation_id);
    match (result, release) {
        (Ok(report), Ok(())) => Ok(report),
        (Err(error), Ok(())) => Err(error),
        (Ok(_), Err(error)) => Err(error),
        (Err(error), Err(release_error)) => Err(error).context(format!(
            "also failed to release approval reservation: {release_error:#}"
        )),
    }
}

fn delivery_workflow<'a>(
    config: &Config,
    catalog: &'a WorkflowCatalog,
) -> Result<&'a WorkflowEntry> {
    let workflows = catalog
        .entries
        .iter()
        .filter(|entry| {
            entry.errors.is_empty()
                && matches!(entry.trigger.as_ref(), Some(Trigger::Label(label)) if label == &config.github.ready_label)
        })
        .collect::<Vec<_>>();
    match workflows.as_slice() {
        [workflow] => Ok(*workflow),
        [] => bail!(
            "no valid delivery workflow uses ready label {:?}",
            config.github.ready_label
        ),
        _ => bail!(
            "multiple delivery workflows use ready label {:?}; v1 requires exactly one",
            config.github.ready_label
        ),
    }
}
