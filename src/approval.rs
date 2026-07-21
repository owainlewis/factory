use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

const PREFIX: &str = "<!-- factory-approval:v1 ";
const SUFFIX: &str = " -->";
const CLAIM_PREFIX: &str = "<!-- factory-claim:v1 ";
static NONCE_SEQUENCE: AtomicU64 = AtomicU64::new(0);

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ApprovalArtifact {
    pub version: u8,
    pub issue: u64,
    pub workflow_id: String,
    pub workflow_hash: String,
    pub approved_content_hash: String,
    pub approver_id: u64,
    pub nonce: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ClaimArtifact {
    pub version: u8,
    pub task_id: i64,
    pub approval_artifact_id: u64,
    pub label_event_id: u64,
}

pub fn approved_content_hash(
    issue: u64,
    title: &str,
    body: &str,
    workflow_id: &str,
    workflow_hash: &str,
) -> Result<String> {
    let canonical = serde_json::to_vec(&(
        "factory-approved-content-v1",
        issue,
        title,
        body,
        workflow_id,
        workflow_hash,
    ))?;
    Ok(format!("v1:{:x}", Sha256::digest(canonical)))
}

pub fn nonce(issue: u64, approver_id: u64) -> Result<String> {
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .context("system clock is before the Unix epoch")?
        .as_nanos();
    let sequence = NONCE_SEQUENCE.fetch_add(1, Ordering::Relaxed);
    let value = serde_json::to_vec(&(now, std::process::id(), sequence, issue, approver_id))?;
    Ok(format!("{:x}", Sha256::digest(value)))
}

pub fn render(artifact: &ApprovalArtifact) -> Result<String> {
    Ok(format!(
        "{PREFIX}{}{SUFFIX}",
        serde_json::to_string(artifact)?
    ))
}

pub fn parse(body: &str) -> Option<ApprovalArtifact> {
    let json = body.trim().strip_prefix(PREFIX)?.strip_suffix(SUFFIX)?;
    let artifact: ApprovalArtifact = serde_json::from_str(json).ok()?;
    (artifact.version == 1).then_some(artifact)
}

pub fn render_claim(claim: &ClaimArtifact) -> Result<String> {
    Ok(format!(
        "{CLAIM_PREFIX}{}{SUFFIX}",
        serde_json::to_string(claim)?
    ))
}

pub fn parse_claim(body: &str) -> Option<ClaimArtifact> {
    let json = body
        .trim()
        .strip_prefix(CLAIM_PREFIX)?
        .strip_suffix(SUFFIX)?;
    let claim: ClaimArtifact = serde_json::from_str(json).ok()?;
    (claim.version == 1).then_some(claim)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn artifact_round_trips_and_hash_binds_every_approved_field() {
        let hash = approved_content_hash(7, "Title", "Body", "deliver", "workflow-v1").unwrap();
        let artifact = ApprovalArtifact {
            version: 1,
            issue: 7,
            workflow_id: "deliver".into(),
            workflow_hash: "workflow-v1".into(),
            approved_content_hash: hash.clone(),
            approver_id: 42,
            nonce: "nonce".into(),
        };
        assert_eq!(parse(&render(&artifact).unwrap()), Some(artifact));
        let claim = ClaimArtifact {
            version: 1,
            task_id: 9,
            approval_artifact_id: 7,
            label_event_id: 8,
        };
        assert_eq!(parse_claim(&render_claim(&claim).unwrap()), Some(claim));
        assert_ne!(
            hash,
            approved_content_hash(7, "Changed", "Body", "deliver", "workflow-v1").unwrap()
        );
    }
}
