use std::fs;
use std::path::PathBuf;
use std::time::Duration;

use assert_cmd::Command;
use factory::config::{Config, GitHubConfig, PipelineState, SourceConfig, SourceStates};
use factory::workflow::{Trigger, WorkflowCatalog};
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
        source: None,
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
+++

{prompt}
"#
    )
}

fn label_workflow(prompt: &str) -> String {
    format!(
        r#"+++
label = "factory:ready"
+++

{prompt}
"#
    )
}

fn state_workflow(state: &str, prompt: &str) -> String {
    format!("+++\nstate = {state:?}\n+++\n\n{prompt}\n")
}

fn source_config() -> SourceConfig {
    SourceConfig {
        owner: "owainlewis".into(),
        project_number: 16,
        status_field: "Status".into(),
        trusted_users: vec!["owainlewis".into()],
        states: SourceStates {
            ready_for_spec: "Ready for spec".into(),
            creating_spec: "Creating spec".into(),
            ready_to_implement: "Ready to implement".into(),
            implementing: "Implementing".into(),
            ready_to_review: "Ready to review".into(),
            done: "Done".into(),
        },
    }
}

#[test]
fn loads_valid_schedule_and_label_workflows_with_resolved_defaults() {
    let fixture = Fixture::new();
    fixture.workflow(
        "Find-Bugs.md",
        r#"+++
schedule = "0 9 * * 1"
timezone = "Europe/London"
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
    assert_eq!(catalog.entries.len(), 2);
    let scheduled = &catalog.entries[0];
    assert_eq!(scheduled.id, "find-bugs");
    assert_eq!(scheduled.runtime.as_deref(), Some("claude"));
    assert_eq!(scheduled.timeout, Some(Duration::from_secs(45 * 60)));
    assert!(matches!(
        scheduled.trigger,
        Some(Trigger::Schedule { ref expression, timezone })
            if expression == "0 9 * * 1" && timezone.name() == "Europe/London"
    ));
    let labelled = &catalog.entries[1];
    assert_eq!(labelled.runtime.as_deref(), Some("codex"));
    assert_eq!(labelled.timeout, Some(Duration::from_secs(2 * 60 * 60)));
    assert_eq!(
        labelled.trigger,
        Some(Trigger::Label("factory:ready".to_owned()))
    );
}

#[test]
fn checked_in_implementation_workflow_is_valid_and_requires_human_merge() {
    let mut fixture = Fixture::new();
    fixture.config.source = Some(source_config());
    fixture.workflow(
        "triage-ticket.md",
        include_str!("../examples/triage-ticket.md"),
    );
    fixture.workflow(
        "implement-ready-ticket.md",
        include_str!("../examples/implement-ready-ticket.md"),
    );

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_count(), 0);
    let workflow = catalog
        .entries
        .iter()
        .find(|entry| entry.id == "implement-ready-ticket")
        .unwrap();
    assert_eq!(
        workflow.trigger,
        Some(Trigger::State(PipelineState::ReadyToImplement))
    );
    assert_eq!(workflow.runtime.as_deref(), Some("codex"));
    assert_eq!(workflow.timeout, Some(Duration::from_secs(4 * 60 * 60)));
    let prompt = workflow.prompt.as_deref().unwrap();
    assert!(prompt.contains("Use the authenticated\n`gh` and `git` commands directly"));
    assert!(prompt.contains("supplied working directory"));
    assert!(prompt.contains("Do not merge or enable auto-merge"));
    assert!(prompt.contains("`ready_to_review`"));
    let triage = catalog
        .entries
        .iter()
        .find(|entry| entry.id == "triage-ticket")
        .unwrap();
    assert_eq!(
        triage.trigger,
        Some(Trigger::State(PipelineState::ReadyForSpec))
    );
    assert!(
        triage
            .prompt
            .as_deref()
            .unwrap()
            .contains("Do not invent requirements")
    );
    assert_eq!(
        include_str!("../examples/implement-ready-ticket.md"),
        include_str!("../.factory/workflows/implement-ready-ticket.md")
    );
    assert_eq!(
        include_str!("../examples/triage-ticket.md"),
        include_str!("../.factory/workflows/triage-ticket.md")
    );
}

#[test]
fn source_mode_requires_one_workflow_for_each_ready_state() {
    let mut fixture = Fixture::new();
    fixture.config.source = Some(source_config());
    fixture.workflow(
        "triage.md",
        &state_workflow("ready_for_spec", "Clarify the task."),
    );
    fixture.workflow(
        "implement.md",
        &state_workflow("ready_to_implement", "Implement the task."),
    );

    let catalog = fixture.catalog();

    assert_eq!(catalog.invalid_count(), 0);
    assert!(catalog.validate_ticket_workflows().is_ok());
}

#[test]
fn source_mode_rejects_label_and_output_state_triggers() {
    let mut fixture = Fixture::new();
    fixture.config.source = Some(source_config());
    fixture.workflow("label.md", &label_workflow("Do work."));
    fixture.workflow("active.md", &state_workflow("implementing", "Do work."));

    let catalog = fixture.catalog();
    let rendered = catalog.to_string();

    assert!(rendered.contains("label triggers are not supported"));
    assert!(rendered.contains("is an output state"));
    assert!(rendered.contains("missing-ready_for_spec-workflow"));
    assert!(rendered.contains("missing-ready_to_implement-workflow"));
}

#[test]
fn reports_all_invalid_workflows_without_hiding_valid_entries() {
    let fixture = Fixture::new();
    fixture.workflow("valid.md", &label_workflow("A useful prompt."));
    fixture.workflow("missing-prompt.md", "+++\nlabel = \"factory:ready\"\n+++\n");
    fixture.workflow(
        "unknown-field.md",
        "+++\nlabel = \"factory:ready\"\nsurprise = true\n+++\nPrompt.\n",
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
        "+++\nlabel = \" factory:ready \"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "two-triggers.md",
        "+++\nschedule = \"0 9 * * 1\"\ntimezone = \"UTC\"\nlabel = \"factory:ready\"\n+++\nPrompt.\n",
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
        "exactly one trigger: schedule, label, or state",
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
    fixture.workflow("no-trigger.md", "+++\n+++\nPrompt.\n");
    fixture.workflow(
        "no-timezone.md",
        "+++\nschedule = \"0 9 * * 1\"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "bad-timeout.md",
        "+++\nlabel = \"factory:ready\"\ntimeout = \"forever\"\n+++\nPrompt.\n",
    );
    fixture.workflow(
        "long-timeout.md",
        "+++\nlabel = \"factory:ready\"\ntimeout = \"9h\"\n+++\nPrompt.\n",
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
        "triage-ticket.md",
        &state_workflow("ready_for_spec", "Triage the issue."),
    );
    fixture.workflow(
        "implement-ready-ticket.md",
        &state_workflow("ready_to_implement", "Implement the issue."),
    );
    fixture.workflow(
        "broken.md",
        "+++\nschedule = \"0 8 * * *\"\ntimezone = \"UTC\"\n+++\n",
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
        "+++\nlabel = \"factory:ready\"\nruntime = \"\\u001b[32m\"\n+++\nPrompt.\n",
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
        source: None,
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
