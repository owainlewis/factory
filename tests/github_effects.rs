#![cfg(unix)]

use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;

use factory::github::{DraftPullRequestRequest, GitHubClient, ProposalIssueRequest};
use tokio_util::sync::CancellationToken;

struct Fixture {
    _temp: tempfile::TempDir,
    repository: PathBuf,
    gh: PathBuf,
}

impl Fixture {
    fn new(issue_pages: &str, pull_pages: &str) -> Self {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repo");
        fs::create_dir(&repository).unwrap();
        fs::write(repository.join("issue-pages.json"), issue_pages).unwrap();
        fs::write(repository.join("pull-pages.json"), pull_pages).unwrap();
        let gh = temp.path().join("gh");
        fs::write(
            &gh,
            r#"#!/bin/sh
set -eu
printf '%s\n' "$@" >> gh.log
if [ "$1" = "api" ] && [ "$2" = "repos/{owner}/{repo}/labels" ]; then
  cat labels.txt
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "--paginate" ]; then
  case "$4" in
    */issues*) cat issue-pages.json ;;
    */pulls*) cat pull-pages.json ;;
    *) exit 91 ;;
  esac
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "--method" ] && [ "$3" = "POST" ]; then
  case "$4" in
    */issues) printf '%s\n' '{"number":42,"html_url":"https://github.com/example/repo/issues/42","labels":[{"name":"factory:proposed"},{"name":"factory:suggested"}]}' ;;
    */pulls) printf '%s\n' '{"number":7,"html_url":"https://github.com/example/repo/pull/7","draft":true,"state":"open","merged_at":null,"head":{"ref":"factory/40-effects"}}' ;;
    *) exit 92 ;;
  esac
  exit 0
fi
if [ "$1" = "label" ] && [ "$2" = "create" ]; then
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "edit" ]; then
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "--method" ] && [ "$3" = "PATCH" ]; then
  printf '%s\n' '{"number":7,"html_url":"https://github.com/example/repo/pull/7","draft":true,"state":"open","merged_at":null,"head":{"ref":"factory/40-effects"}}'
  exit 0
fi
exit 93
"#,
        )
        .unwrap();
        fs::write(
            repository.join("labels.txt"),
            "factory:proposed\nfactory:suggested\n",
        )
        .unwrap();
        let mut permissions = fs::metadata(&gh).unwrap().permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(&gh, permissions).unwrap();
        Self {
            _temp: temp,
            repository,
            gh,
        }
    }

    fn client(&self) -> GitHubClient {
        GitHubClient::new(&self.gh)
    }

    fn log(&self) -> String {
        fs::read_to_string(self.repository.join("gh.log")).unwrap()
    }

    fn set_labels(&self, labels: &str) {
        fs::write(self.repository.join("labels.txt"), labels).unwrap();
    }
}

fn proposal(number: u64, marker: &str) -> String {
    format!(
        r#"[[{{"number":{number},"html_url":"https://github.com/example/repo/issues/{number}","body":"Proposal\n\n{marker}","labels":[{{"name":"factory:proposed"}}],"pull_request":null}}]]"#
    )
}

fn pull(number: u64, head: &str, draft: bool) -> String {
    format!(
        r#"[[{{"number":{number},"html_url":"https://github.com/example/repo/pull/{number}","draft":{draft},"state":"open","merged_at":null,"head":{{"ref":"{head}","repo":{{"full_name":"example/repo"}}}}}}]]"#
    )
}

#[tokio::test]
async fn proposal_marker_reuses_the_existing_labeled_issue() {
    let marker = "<!-- factory-proposal:v1:run-12 -->";
    let fixture = Fixture::new(&proposal(12, marker), "[[]]");

    let result = fixture
        .client()
        .find_or_create_proposal(
            &fixture.repository,
            "example/repo",
            ProposalIssueRequest {
                title: "A proposal",
                body: "Details",
                proposed_label: "factory:proposed",
                marker,
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(result.number, 12);
    assert!(!result.created);
    assert!(!fixture.log().contains("--method\nPOST"));
}

#[tokio::test]
async fn proposal_marker_reuses_an_existing_issue_after_its_label_is_removed() {
    let marker = "<!-- factory-proposal:v1:run-12 -->";
    let issues = proposal(12, marker).replace(
        r#""labels":[{"name":"factory:proposed"}]"#,
        r#""labels":[]"#,
    );
    let fixture = Fixture::new(&issues, "[[]]");

    let result = fixture
        .client()
        .find_or_create_proposal(
            &fixture.repository,
            "example/repo",
            ProposalIssueRequest {
                title: "A proposal",
                body: "Details",
                proposed_label: "factory:proposed",
                marker,
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(result.number, 12);
    assert!(!result.created);
    let log = fixture.log();
    assert!(log.contains("issue\nedit\n12\n--add-label\nfactory:proposed"));
    assert!(!log.contains("--method\nPOST"));
}

#[tokio::test]
async fn proposal_creation_creates_the_configured_label_when_missing() {
    let marker = "<!-- factory-proposal:v1:run-14 -->";
    let fixture = Fixture::new("[[]]", "[[]]");
    fixture.set_labels("");

    fixture
        .client()
        .find_or_create_proposal(
            &fixture.repository,
            "example/repo",
            ProposalIssueRequest {
                title: "A proposal",
                body: "Details",
                proposed_label: "factory:proposed",
                marker,
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap();

    let log = fixture.log();
    assert!(log.contains("label\ncreate\nfactory:proposed"));
    assert!(log.contains("labels[]=factory:proposed"));
}

#[tokio::test]
async fn proposal_creation_applies_the_proposed_label_and_marker() {
    let marker = "<!-- factory-proposal:v1:run-13 -->";
    let fixture = Fixture::new("[[]]", "[[]]");

    let result = fixture
        .client()
        .find_or_create_proposal(
            &fixture.repository,
            "example/repo",
            ProposalIssueRequest {
                title: "A proposal",
                body: "Details",
                proposed_label: "factory:suggested",
                marker,
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(result.number, 42);
    assert!(result.created);
    let log = fixture.log();
    assert!(log.contains("labels[]=factory:suggested"));
    assert!(log.contains(marker));
    assert!(!log.contains("factory:ready"));
}

#[tokio::test]
async fn proposal_rejects_a_marker_that_is_not_factory_scoped() {
    let fixture = Fixture::new("[[]]", "[[]]");

    let error = fixture
        .client()
        .find_or_create_proposal(
            &fixture.repository,
            "example/repo",
            ProposalIssueRequest {
                title: "A proposal",
                body: "Details",
                proposed_label: "factory:proposed",
                marker: "run-13",
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap_err();

    assert!(format!("{error:#}").contains("invalid Factory proposal marker"));
    assert!(!fixture.repository.join("gh.log").exists());
}

#[tokio::test]
async fn publication_creates_a_draft_for_the_exact_recorded_head() {
    let fixture = Fixture::new("[[]]", "[[]]");

    let result = fixture
        .client()
        .publish_draft_pull_request(
            &fixture.repository,
            "example/repo",
            DraftPullRequestRequest {
                head_branch: "factory/40-effects",
                base_branch: "main",
                title: "Effect commands",
                body: "Details",
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap();

    assert!(result.created);
    let log = fixture.log();
    assert!(log.contains("head=factory/40-effects"));
    assert!(log.contains("base=main"));
    assert!(log.contains("draft=true"));
    assert!(!log.contains("merge"));
}

#[tokio::test]
async fn repeated_publication_updates_the_one_existing_draft() {
    let fixture = Fixture::new("[[]]", &pull(7, "factory/40-effects", true));

    let result = fixture
        .client()
        .publish_draft_pull_request(
            &fixture.repository,
            "example/repo",
            DraftPullRequestRequest {
                head_branch: "factory/40-effects",
                base_branch: "main",
                title: "Updated title",
                body: "Updated body",
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap();

    assert_eq!(result.number, 7);
    assert!(!result.created);
    let log = fixture.log();
    assert!(log.contains("PATCH"));
    assert!(!log.contains("head=factory/40-effects"));
}

#[tokio::test]
async fn publication_refuses_an_existing_ready_pull_request() {
    let fixture = Fixture::new("[[]]", &pull(7, "factory/40-effects", false));

    let error = fixture
        .client()
        .publish_draft_pull_request(
            &fixture.repository,
            "example/repo",
            DraftPullRequestRequest {
                head_branch: "factory/40-effects",
                base_branch: "main",
                title: "Title",
                body: "Body",
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap_err();

    assert!(format!("{error:#}").contains("is not a draft"));
    assert!(!fixture.log().contains("PATCH"));
}

#[tokio::test]
async fn publication_refuses_a_closed_pull_request_for_the_recorded_head() {
    let closed =
        pull(7, "factory/40-effects", true).replace("\"state\":\"open\"", "\"state\":\"closed\"");
    let fixture = Fixture::new("[[]]", &closed);

    let error = fixture
        .client()
        .publish_draft_pull_request(
            &fixture.repository,
            "example/repo",
            DraftPullRequestRequest {
                head_branch: "factory/40-effects",
                base_branch: "main",
                title: "Title",
                body: "Body",
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap_err();

    assert!(format!("{error:#}").contains("is closed"));
    assert!(!fixture.log().contains("POST"));
}

#[tokio::test]
async fn publication_refuses_a_merged_pull_request_for_the_recorded_head() {
    let merged = pull(7, "factory/40-effects", false)
        .replace("\"state\":\"open\"", "\"state\":\"closed\"")
        .replace(
            "\"merged_at\":null",
            "\"merged_at\":\"2026-07-21T12:00:00Z\"",
        );
    let fixture = Fixture::new("[[]]", &merged);

    let error = fixture
        .client()
        .publish_draft_pull_request(
            &fixture.repository,
            "example/repo",
            DraftPullRequestRequest {
                head_branch: "factory/40-effects",
                base_branch: "main",
                title: "Title",
                body: "Body",
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap_err();

    assert!(format!("{error:#}").contains("is already merged"));
    assert!(!fixture.log().contains("POST"));
}

#[tokio::test]
async fn publication_does_not_adopt_a_same_named_branch_from_a_fork() {
    let fork_pull = r#"[[{"number":8,"html_url":"https://github.com/fork/repo/pull/8","draft":true,"state":"open","merged_at":null,"head":{"ref":"factory/40-effects","repo":{"full_name":"fork/repo"}}}]]"#;
    let fixture = Fixture::new("[[]]", fork_pull);

    let result = fixture
        .client()
        .publish_draft_pull_request(
            &fixture.repository,
            "example/repo",
            DraftPullRequestRequest {
                head_branch: "factory/40-effects",
                base_branch: "main",
                title: "Title",
                body: "Body",
            },
            &CancellationToken::new(),
        )
        .await
        .unwrap();

    assert!(result.created);
    assert!(fixture.log().contains("POST"));
}
