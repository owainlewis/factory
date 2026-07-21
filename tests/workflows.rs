use std::fs;
use std::path::PathBuf;
use std::time::Duration;

use assert_cmd::Command;
use chrono_tz::UTC;
use factory::config::{Config, GitHubConfig};
use factory::workflow::{
    Trigger, WorkflowCatalog, WorkflowEffect, scheduled_workflow_fingerprint, workflow_content_hash,
};
use predicates::prelude::*;

struct Fixture {
    _temp: tempfile::TempDir,
    repository: PathBuf,
    config_path: PathBuf,
    config: Config,
    data_home: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repository");
        let workflows = repository.join(".factory/workflows");
        let workspace = temp.path().join("worktrees");
        let data_home = temp.path().join("factory-data");
        fs::create_dir_all(&workflows).unwrap();
        fs::create_dir(&workspace).unwrap();
        assert!(
            std::process::Command::new("git")
                .args(["init", "--quiet"])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        assert!(
            std::process::Command::new("git")
                .args([
                    "remote",
                    "add",
                    "origin",
                    "git@github.com:example/repository.git"
                ])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        Command::cargo_bin("factory")
            .unwrap()
            .current_dir(&repository)
            .env("FACTORY_DATA_HOME", &data_home)
            .arg("init")
            .assert()
            .success();
        let config_path = repository.join(".factory/config.toml");
        let config = test_config(
            vec![repository.canonicalize().unwrap()],
            workspace,
            temp.path().join("data"),
        );
        Self {
            _temp: temp,
            repository,
            config_path,
            config,
            data_home,
        }
    }

    fn workflow(&self, name: &str, contents: &str) -> PathBuf {
        let path = self.repository.join(".factory/workflows").join(name);
        fs::write(&path, contents).unwrap();
        path
    }

    fn catalog(&self) -> WorkflowCatalog {
        WorkflowCatalog::load(&self.config).unwrap()
    }
}

fn test_config(repositories: Vec<PathBuf>, workspace: PathBuf, data: PathBuf) -> Config {
    Config {
        repositories,
        poll_every: Duration::from_secs(30),
        default_runtime: "codex".into(),
        default_timeout: Duration::from_secs(2 * 60 * 60),
        maximum_timeout: Duration::from_secs(8 * 60 * 60),
        max_concurrent_runs: 2,
        max_concurrent_runs_per_repository: 2,
        workspace_root: workspace,
        data_directory: data,
        github: GitHubConfig {
            trusted_approvers: vec!["owainlewis".into()],
            ready_label: "factory:ready".into(),
            proposed_label: "factory:proposed".into(),
            needs_review_label: "factory:needs-review".into(),
        },
    }
}

fn scheduled_workflow(prompt: &str) -> String {
    format!(
        r#"+++
schedule = "0 9 * * 1"
timezone = "Europe/London"
effect = "proposal"
+++

{prompt}
"#
    )
}

fn label_workflow(prompt: &str) -> String {
    format!(
        r#"+++
label = "factory:ready"
effect = "delivery"
+++

{prompt}
"#
    )
}

#[test]
fn loads_valid_schedule_and_label_workflows_with_resolved_defaults() {
    let fixture = Fixture::new();
    fixture.workflow(
        "Find-Bugs.md",
        r#"+++
schedule = "0 9 * * 1"
timezone = "Europe/London"
effect = "proposal"
runtime = "claude"
timeout = "45m"
+++

Review the code for verified bugs.
"#,
    );
    fixture.workflow(
        "implement-ready-ticket.md",
        &label_workflow("Take the ticket to a green pull request."),
    );

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_count(), 0);
    assert!(catalog.validate_delivery_workflows(&fixture.config).is_ok());
    assert_eq!(catalog.entries.len(), 2);
    let scheduled = &catalog.entries[0];
    assert_eq!(scheduled.id, "find-bugs");
    assert_eq!(scheduled.effect, Some(WorkflowEffect::Proposal));
    assert_eq!(scheduled.runtime.as_deref(), Some("claude"));
    assert_eq!(scheduled.timeout, Some(Duration::from_secs(45 * 60)));
    assert!(matches!(
        scheduled.trigger,
        Some(Trigger::Schedule { ref expression, timezone })
            if expression == "0 9 * * 1" && timezone.name() == "Europe/London"
    ));
    let labelled = &catalog.entries[1];
    assert_eq!(labelled.runtime.as_deref(), Some("codex"));
    assert_eq!(labelled.effect, Some(WorkflowEffect::Delivery));
    assert_eq!(labelled.timeout, Some(Duration::from_secs(2 * 60 * 60)));
    assert_eq!(
        labelled.trigger,
        Some(Trigger::Label("factory:ready".to_owned()))
    );
}

#[test]
fn checked_in_implementation_workflow_is_valid_and_requires_human_merge() {
    let fixture = Fixture::new();
    fixture.workflow(
        "implement-ready-ticket.md",
        include_str!("../examples/implement-ready-ticket.md"),
    );

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_count(), 0);
    let workflow = &catalog.entries[0];
    assert_eq!(
        workflow.trigger,
        Some(Trigger::Label("factory:ready".to_owned()))
    );
    assert_eq!(workflow.effect, Some(WorkflowEffect::Delivery));
    assert_eq!(workflow.runtime.as_deref(), Some("codex"));
    assert_eq!(workflow.timeout, Some(Duration::from_secs(4 * 60 * 60)));
    let prompt = workflow.prompt.as_deref().unwrap();
    assert!(prompt.contains("Never merge"));
    assert!(prompt.contains("`factory task block --file PATH`"));
    assert!(prompt.contains("`factory change publish --file PATH`"));
    assert!(prompt.contains("already consumed the exact approval"));
    assert!(prompt.contains("`factory approve ISSUE_NUMBER` again"));
    assert!(prompt.contains("fresh subagent"));
    let step_headings: Vec<_> = prompt
        .lines()
        .filter(|line| line.starts_with("## Step "))
        .collect();
    assert_eq!(
        step_headings,
        [
            "## Step 1: Establish scope and safety",
            "## Step 2: Inspect the issue and existing work",
            "## Step 3: Confirm the claim or report a blocker",
            "## Step 4: Implement the ticket",
            "## Step 5: Verify the implementation",
            "## Step 6: Review and publish the change",
            "## Step 7: Resolve CI and review feedback",
            "## Step 8: Hand off for human review",
        ]
    );
    assert_eq!(
        include_str!("../examples/implement-ready-ticket.md"),
        include_str!("../.factory/workflows/implement-ready-ticket.md")
    );
}

#[test]
fn requires_a_known_effect_and_enforces_effect_trigger_policy() {
    let fixture = Fixture::new();
    fixture.workflow(
        "missing-effect.md",
        "+++\nlabel = \"triage\"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "unknown-effect.md",
        "+++\nlabel = \"triage\"\neffect = \"mutation\"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "proposal-label.md",
        "+++\nlabel = \"triage\"\neffect = \"proposal\"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "scheduled-delivery.md",
        "+++\nschedule = \"0 9 * * 1\"\ntimezone = \"UTC\"\neffect = \"delivery\"\n+++\nPrompt.\n",
    );

    let output = fixture.catalog().to_string();

    assert!(output.contains("must declare effect"));
    assert!(output.contains("unknown variant `mutation`"));
    assert!(output.contains("proposal workflows must use a schedule trigger in v1"));
    assert!(output.contains("delivery workflow must use configured ready label"));
}

#[test]
fn rejects_more_than_one_valid_delivery_workflow_per_repository() {
    let fixture = Fixture::new();
    fixture.workflow("deliver-one.md", &label_workflow("First."));
    fixture.workflow("deliver-two.md", &label_workflow("Second."));

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_count(), 2);
    assert!(catalog.entries.iter().all(|entry| {
        entry
            .errors
            .iter()
            .any(|error| error.contains("exactly one valid delivery workflow"))
    }));
    assert_eq!(
        catalog
            .repositories_without_ready_workflow(&fixture.config)
            .count(),
        1
    );
    assert!(
        catalog
            .validate_delivery_workflows(&fixture.config)
            .is_err()
    );
}

#[test]
fn repository_without_delivery_workflow_fails_delivery_validation() {
    let fixture = Fixture::new();
    fixture.workflow("triage.md", &scheduled_workflow("Review the code."));

    let error = fixture
        .catalog()
        .validate_delivery_workflows(&fixture.config)
        .unwrap_err();

    assert!(
        error
            .to_string()
            .contains("exactly one valid delivery workflow")
    );
    assert!(
        error
            .to_string()
            .contains(&fixture.repository.display().to_string())
    );
}

#[test]
fn workflow_hashes_and_schedule_fingerprints_bind_the_effect() {
    let fixture = Fixture::new();
    fixture.workflow("triage.md", &scheduled_workflow("Review the code."));
    let catalog = fixture.catalog();
    let proposal = &catalog.entries[0];
    let mut delivery = proposal.clone();
    delivery.effect = Some(WorkflowEffect::Delivery);

    assert_ne!(
        workflow_content_hash(proposal).unwrap(),
        workflow_content_hash(&delivery).unwrap()
    );
    assert_ne!(
        scheduled_workflow_fingerprint(
            "0 9 * * 1",
            UTC,
            WorkflowEffect::Proposal,
            "codex",
            Duration::from_secs(60),
            "Review the code.",
        )
        .unwrap(),
        scheduled_workflow_fingerprint(
            "0 9 * * 1",
            UTC,
            WorkflowEffect::Delivery,
            "codex",
            Duration::from_secs(60),
            "Review the code.",
        )
        .unwrap()
    );
}

#[test]
fn reports_all_invalid_workflows_without_hiding_valid_entries() {
    let fixture = Fixture::new();
    fixture.workflow("valid.md", &label_workflow("A useful prompt."));
    fixture.workflow(
        "missing-prompt.md",
        "+++\nlabel = \"factory:ready\"\neffect = \"delivery\"\n+++\n",
    );
    fixture.workflow(
        "unknown-field.md",
        "+++\nlabel = \"factory:ready\"\neffect = \"delivery\"\nsurprise = true\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "invalid-cron.md",
        &scheduled_workflow("Prompt.").replace("0 9 * * 1", "eventually"),
    );
    fixture.workflow(
        "invalid-timezone.md",
        &scheduled_workflow("Prompt.").replace("Europe/London", "Middle/Earth"),
    );
    fixture.workflow(
        "invalid-label.md",
        "+++\nlabel = \" factory:ready \"\neffect = \"proposal\"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "two-triggers.md",
        "+++\nschedule = \"0 9 * * 1\"\ntimezone = \"UTC\"\nlabel = \"factory:ready\"\neffect = \"proposal\"\n+++\nPrompt.\n",
    );

    let catalog = fixture.catalog();

    assert_eq!(catalog.entries.len(), 7);
    assert_eq!(catalog.invalid_count(), 6);
    assert!(catalog.entries.iter().any(|entry| entry.id == "valid"));
    let output = catalog.to_string();
    for expected in [
        "prompt body must not be empty",
        "unknown field `surprise`",
        "valid five-field cron expression",
        "timezone is invalid",
        "label must be 1-50 characters",
        "not schedule and label",
    ] {
        assert!(
            output.contains(expected),
            "missing {expected:?} in {output}"
        );
    }
}

#[test]
fn missing_schedule_frontmatter_delimiter_stays_isolated_from_tickets() {
    let fixture = Fixture::new();
    fixture.workflow(
        "broken-schedule.md",
        "+++\nschedule = \"0 9 * * 1\"\ntimezone = \"UTC\"\ntimeout = O(n) before maintenance.\n",
    );
    fixture.workflow("valid-ticket.md", &label_workflow("Implement the ticket."));

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_scheduled_entries().count(), 1);
    assert!(catalog.validate_ticket_workflows().is_ok());
    assert!(catalog.entries.iter().any(|entry| {
        entry.id == "broken-schedule"
            && entry
                .errors
                .iter()
                .any(|error| error.contains("missing its closing +++ delimiter"))
    }));
}

#[test]
fn malformed_schedule_isolation_is_independent_of_key_order() {
    let fixture = Fixture::new();
    fixture.workflow(
        "broken-schedule.md",
        "+++\ntimezone = \"UTC\"\nruntime = \"codex\"\nschedule = \"unterminated\n+++\nPrompt.\n",
    );
    fixture.workflow("valid-ticket.md", &label_workflow("Implement the ticket."));

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_scheduled_entries().count(), 1);
    assert!(catalog.validate_ticket_workflows().is_ok());
}

#[test]
fn malformed_same_line_mixed_triggers_remain_fail_fast() {
    let fixture = Fixture::new();
    fixture.workflow(
        "ambiguous-workflow.md",
        "+++\nschedule = \"0 9 * * 1\" label = \"factory:ready\"\n+++\nPrompt.\n",
    );
    fixture.workflow("valid-ticket.md", &label_workflow("Implement the ticket."));

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_scheduled_entries().count(), 0);
    assert!(catalog.validate_ticket_workflows().is_err());
}

#[test]
fn rejects_missing_trigger_timezone_and_invalid_timeout() {
    let fixture = Fixture::new();
    fixture.workflow(
        "no-trigger.md",
        "+++\neffect = \"proposal\"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "no-timezone.md",
        "+++\nschedule = \"0 9 * * 1\"\neffect = \"proposal\"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "bad-timeout.md",
        "+++\nlabel = \"factory:ready\"\neffect = \"delivery\"\ntimeout = \"forever\"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "long-timeout.md",
        "+++\nlabel = \"factory:ready\"\neffect = \"delivery\"\ntimeout = \"9h\"\n+++\nPrompt.\n",
    );

    let output = fixture.catalog().to_string();

    assert!(output.contains("exactly one trigger"));
    assert!(output.contains("must declare timezone"));
    assert!(output.contains("timeout has invalid duration"));
    assert!(output.contains("exceeds maximum_timeout"));
}

#[test]
fn duplicate_ids_are_reported_for_every_duplicate_entry() {
    let mut fixture = Fixture::new();
    fixture.workflow("same.md", &label_workflow("Prompt."));
    fixture
        .config
        .repositories
        .push(fixture.repository.canonicalize().unwrap());

    let catalog = fixture.catalog();

    assert_eq!(catalog.entries.len(), 2);
    assert_eq!(catalog.invalid_count(), 2);
    assert!(catalog.entries.iter().all(|entry| {
        entry
            .errors
            .iter()
            .any(|error| error.contains("duplicate workflow ID"))
    }));
}

#[cfg(unix)]
#[test]
fn rejects_symlinked_workflow_files() {
    use std::os::unix::fs::symlink;

    let fixture = Fixture::new();
    let outside = fixture._temp.path().join("outside.md");
    fs::write(&outside, label_workflow("Do not load me.")).unwrap();
    let workflow = fixture.repository.join(".factory/workflows/unsafe.md");
    symlink(outside, workflow).unwrap();

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_count(), 1);
    assert!(catalog.entries[0].prompt.is_none());
    assert!(catalog.entries[0].errors[0].contains("not a symlink"));
}

#[cfg(unix)]
#[test]
fn unsafe_workflow_directory_does_not_hide_other_repositories() {
    use std::os::unix::fs::symlink;

    let temp = tempfile::tempdir().unwrap();
    let valid_repository = temp.path().join("valid-repository");
    let valid_workflows = valid_repository.join(".factory/workflows");
    fs::create_dir_all(&valid_workflows).unwrap();
    fs::write(
        valid_workflows.join("valid.md"),
        label_workflow("A valid prompt."),
    )
    .unwrap();

    let unsafe_repository = temp.path().join("unsafe-repository");
    let unsafe_factory = unsafe_repository.join(".factory");
    let outside = temp.path().join("outside-workflows");
    fs::create_dir_all(&unsafe_factory).unwrap();
    fs::create_dir(&outside).unwrap();
    symlink(&outside, unsafe_factory.join("workflows")).unwrap();

    let workspace = temp.path().join("worktrees");
    fs::create_dir(&workspace).unwrap();
    let config = test_config(
        vec![
            valid_repository.canonicalize().unwrap(),
            unsafe_repository.canonicalize().unwrap(),
        ],
        workspace,
        temp.path().join("data"),
    );
    let catalog = WorkflowCatalog::load(&config).unwrap();

    assert_eq!(catalog.entries.len(), 2);
    assert_eq!(catalog.invalid_count(), 1);
    assert!(
        catalog
            .entries
            .iter()
            .any(|entry| entry.id == "valid" && entry.errors.is_empty())
    );
    assert!(catalog.to_string().contains("not a symlink"));
}

#[cfg(unix)]
#[test]
fn rejects_workflows_reached_through_symlinked_factory_ancestor() {
    use std::os::unix::fs::symlink;

    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository");
    let outside_factory = temp.path().join("outside-factory");
    let outside_workflows = outside_factory.join("workflows");
    let workspace = temp.path().join("worktrees");
    fs::create_dir(&repository).unwrap();
    fs::create_dir_all(&outside_workflows).unwrap();
    fs::create_dir(&workspace).unwrap();
    fs::write(
        outside_workflows.join("escaped.md"),
        label_workflow("This prompt is outside the repository."),
    )
    .unwrap();
    symlink(&outside_factory, repository.join(".factory")).unwrap();
    let config = test_config(
        vec![repository.canonicalize().unwrap()],
        workspace,
        temp.path().join("data"),
    );
    let catalog = WorkflowCatalog::load(&config).unwrap();

    assert_eq!(catalog.invalid_count(), 1);
    assert_eq!(catalog.entries[0].id, "<workflow-directory>");
    assert!(catalog.entries[0].prompt.is_none());
    assert!(catalog.entries[0].errors[0].contains("resolves outside the configured repository"));
}

#[test]
fn workflows_command_lists_resolved_catalog_and_fails_for_invalid_entries() {
    let fixture = Fixture::new();
    fixture.workflow("find-bugs.md", &scheduled_workflow("Review the code."));
    fixture.workflow(
        "broken.md",
        "+++\nlabel = \"factory:ready\"\neffect = \"delivery\"\n+++\n",
    );

    Command::cargo_bin("factory")
        .unwrap()
        .args([
            "workflows",
            "--config",
            fixture.config_path.to_str().unwrap(),
        ])
        .env("FACTORY_DATA_HOME", &fixture.data_home)
        .assert()
        .failure()
        .stdout(predicate::str::contains("REPOSITORY\tWORKFLOW"))
        .stdout(predicate::str::contains(
            fixture.repository.to_str().unwrap(),
        ))
        .stdout(predicate::str::contains("find-bugs"))
        .stdout(predicate::str::contains(
            "schedule \"0 9 * * 1\" (Europe/London)",
        ))
        .stdout(predicate::str::contains("codex\t2h\tvalid"))
        .stdout(predicate::str::contains("broken"))
        .stdout(predicate::str::contains("prompt body must not be empty"))
        .stderr(predicate::str::contains(
            "workflow catalog contains 1 invalid workflow(s)",
        ));
}

#[test]
fn loading_workflows_never_executes_prompt_text() {
    let fixture = Fixture::new();
    let marker = fixture._temp.path().join("must-not-exist");
    fixture.workflow(
        "safe.md",
        &label_workflow(&format!("Create the file {}.", marker.display())),
    );

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_count(), 0);
    assert!(!marker.exists());
}

#[test]
fn catalog_output_escapes_control_characters_in_every_dynamic_cell() {
    let temp = tempfile::tempdir().unwrap();
    let repository = temp.path().join("repository\u{1b}[2J");
    let workflows = repository.join(".factory/workflows");
    let workspace = temp.path().join("worktrees");
    fs::create_dir_all(&workflows).unwrap();
    fs::create_dir(&workspace).unwrap();
    fs::write(
        workflows.join("bad\u{1b}[31m.md"),
        "+++\nlabel = \"factory:ready\"\neffect = \"delivery\"\nruntime = \"\\u001b[32m\"\n+++\nPrompt.\n",
    )
    .unwrap();
    let config = Config {
        repositories: vec![repository.canonicalize().unwrap()],
        poll_every: Duration::from_secs(30),
        default_runtime: "codex".to_owned(),
        default_timeout: Duration::from_secs(2 * 60 * 60),
        maximum_timeout: Duration::from_secs(8 * 60 * 60),
        max_concurrent_runs: 1,
        max_concurrent_runs_per_repository: 1,
        workspace_root: workspace,
        data_directory: temp.path().join("data"),
        github: GitHubConfig {
            trusted_approvers: vec!["owainlewis".into()],
            ready_label: "factory:ready".into(),
            proposed_label: "factory:proposed".into(),
            needs_review_label: "factory:needs-review".into(),
        },
    };

    let output = WorkflowCatalog::load(&config).unwrap().to_string();

    assert!(!output.contains('\u{1b}'));
    assert!(output.matches("\\u{1b}").count() >= 3, "{output}");
}
