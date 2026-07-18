use std::fs;
use std::path::{Path, PathBuf};
use std::time::Duration;

use assert_cmd::Command;
use factory::config::Config;
use factory::workflow::{Trigger, WorkflowCatalog};
use predicates::prelude::*;

struct Fixture {
    _temp: tempfile::TempDir,
    repository: PathBuf,
    config: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repository");
        let workflows = repository.join(".factory/workflows");
        let workspace = temp.path().join("worktrees");
        fs::create_dir_all(&workflows).unwrap();
        fs::create_dir(&workspace).unwrap();
        let config = temp.path().join("config.toml");
        write_config(&config, &[&repository], &workspace);
        Self {
            _temp: temp,
            repository,
            config,
        }
    }

    fn workflow(&self, name: &str, contents: &str) -> PathBuf {
        let path = self.repository.join(".factory/workflows").join(name);
        fs::write(&path, contents).unwrap();
        path
    }

    fn catalog(&self) -> WorkflowCatalog {
        let config = Config::load(&self.config).unwrap();
        WorkflowCatalog::load(&config).unwrap()
    }
}

fn write_config(path: &Path, repositories: &[&Path], workspace: &Path) {
    let repositories = repositories
        .iter()
        .map(|repository| format!("\"{}\"", repository.display()))
        .collect::<Vec<_>>()
        .join(", ");
    fs::write(
        path,
        format!(
            r#"repositories = [{repositories}]
poll_every = "30s"
default_runtime = "codex"
default_timeout = "2h"
maximum_timeout = "8h"
max_concurrent_runs = 2
workspace_root = "{}"
"#,
            workspace.display()
        ),
    )
    .unwrap();
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
        "not schedule and label",
    ] {
        assert!(
            output.contains(expected),
            "missing {expected:?} in {output}"
        );
    }
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
    let fixture = Fixture::new();
    fixture.workflow("same.md", &label_workflow("Prompt."));
    let workspace = fixture._temp.path().join("worktrees");
    write_config(
        &fixture.config,
        &[&fixture.repository, &fixture.repository],
        &workspace,
    );

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
    let config_path = temp.path().join("config.toml");
    write_config(
        &config_path,
        &[&valid_repository, &unsafe_repository],
        &workspace,
    );

    let config = Config::load(&config_path).unwrap();
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

#[test]
fn workflows_command_lists_resolved_catalog_and_fails_for_invalid_entries() {
    let fixture = Fixture::new();
    fixture.workflow("find-bugs.md", &scheduled_workflow("Review the code."));
    fixture.workflow("broken.md", "+++\nlabel = \"factory:ready\"\n+++\n");

    Command::cargo_bin("factory")
        .unwrap()
        .args(["workflows", "--config", fixture.config.to_str().unwrap()])
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
