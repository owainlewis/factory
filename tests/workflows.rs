use std::fs;
use std::process::Command;
use std::time::Duration;

use factory::config::{Config, TriggerKind};
use factory::workflow::{Trigger, WorkflowCatalog, WorkflowEntry};

struct Fixture {
    _temp: tempfile::TempDir,
    repository: std::path::PathBuf,
}

impl Fixture {
    fn new(config: &str) -> Self {
        let temp = tempfile::tempdir().unwrap();
        let repository = temp.path().join("repository");
        fs::create_dir_all(repository.join(".factory/workflows")).unwrap();
        assert!(
            Command::new("git")
                .args(["init", "--quiet"])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        assert!(
            Command::new("git")
                .args([
                    "remote",
                    "add",
                    "origin",
                    "git@github.com:example/repository.git",
                ])
                .current_dir(&repository)
                .status()
                .unwrap()
                .success()
        );
        assert_cmd::Command::cargo_bin("factory")
            .unwrap()
            .current_dir(&repository)
            .arg("init")
            .assert()
            .success();
        fs::write(repository.join(".factory/config.toml"), config).unwrap();
        Self {
            _temp: temp,
            repository,
        }
    }

    fn config(&self) -> anyhow::Result<Config> {
        Config::load(&self.repository.join(".factory/config.toml"))
    }
}

fn config_with(triggers: &str) -> String {
    format!(
        r#"version = 1
poll_every = "30s"

[worker]
runtime = "codex"
sandbox = "worktree"
timeout = "2h"
maximum_timeout = "8h"
max_concurrent = 1

[source]
type = "github"
project_owner = "owainlewis"
project_number = 16
status_field = "Status"
trusted_users = ["owainlewis"]

{triggers}
"#
    )
}

#[test]
fn catalog_display_groups_the_repository_and_aligns_workflows() {
    let repository = std::path::PathBuf::from("/tmp/a-long-repository-path");
    let entries = vec![
        WorkflowEntry {
            repository: repository.clone(),
            path: repository.join("implement.md"),
            id: "implement".to_owned(),
            trigger: Some(Trigger::Source {
                state: "open".to_owned(),
                labels: vec!["factory:ready-to-implement".to_owned()],
            }),
            runtime: Some("codex".to_owned()),
            timeout: Some(Duration::from_secs(4 * 60 * 60)),
            prompt: Some("Implement it.".to_owned()),
            errors: Vec::new(),
        },
        WorkflowEntry {
            repository,
            path: std::path::PathBuf::from("/tmp/pr-review.md"),
            id: "pr-review".to_owned(),
            trigger: Some(Trigger::Schedule {
                expression: "*/10 * * * *".to_owned(),
                timezone: chrono_tz::Europe::London,
            }),
            runtime: Some("codex".to_owned()),
            timeout: Some(Duration::from_secs(30 * 60)),
            prompt: Some("Review it.".to_owned()),
            errors: Vec::new(),
        },
    ];

    assert_eq!(
        WorkflowCatalog { entries }.to_string(),
        "Repository: /tmp/a-long-repository-path\n\
         \n\
         WORKFLOW   TRIGGER                                                    RUNTIME  TIMEOUT  VALIDITY\n\
         ─────────  ─────────────────────────────────────────────────────────  ───────  ───────  ────────\n\
         implement  source state \"open\" labels [\"factory:ready-to-implement\"]  codex    4h       valid\n\
         pr-review  schedule \"*/10 * * * *\" (Europe/London)                    codex    30m      valid\n"
    );
}

#[test]
fn loads_explicit_tagged_triggers_and_plain_workflows() {
    let config = config_with(
        r#"[trigger.triage]
type = "status"
status = "Ready For Spec"
workflow = ".factory/workflows/triage.md"

[trigger.implement]
type = "label"
label = "agent:ready"
workflow = ".factory/workflows/implement.md"
timeout = "45m"

[trigger.maintenance]
type = "schedule"
schedule = "*/10 * * * *"
timezone = "Europe/London"
workflow = ".factory/workflows/maintenance.md"
"#,
    );
    let fixture = Fixture::new(&config);
    for name in ["triage", "implement", "maintenance"] {
        fs::write(
            fixture
                .repository
                .join(format!(".factory/workflows/{name}.md")),
            format!("Run the {name} workflow.\n"),
        )
        .unwrap();
    }

    let config = fixture.config().unwrap();
    assert!(matches!(config.triggers[0].kind, TriggerKind::Label(_)));
    assert!(matches!(
        config.triggers[1].kind,
        TriggerKind::Schedule { .. }
    ));
    assert!(matches!(config.triggers[2].kind, TriggerKind::Status(_)));
    let catalog = WorkflowCatalog::load(&config).unwrap();
    catalog.validate_all().unwrap();
    let triage = catalog
        .entries
        .iter()
        .find(|entry| entry.id == "triage")
        .unwrap();
    let implement = catalog
        .entries
        .iter()
        .find(|entry| entry.id == "implement")
        .unwrap();
    assert!(matches!(triage.trigger, Some(Trigger::Status(_))));
    assert_eq!(
        implement.prompt.as_deref(),
        Some("Run the implement workflow.\n")
    );
}

#[test]
fn rejects_mixed_trigger_fields() {
    let fixture = Fixture::new(&config_with(
        r#"[trigger.bad]
type = "label"
label = "agent:ready"
status = "Ready"
workflow = ".factory/workflows/bad.md"
"#,
    ));
    let error = fixture.config().unwrap_err();
    assert!(format!("{error:#}").contains("unknown field `status`"));
}

#[test]
fn rejects_unsupported_runtime_and_source() {
    let config = config_with(
        r#"[trigger.triage]
type = "status"
status = "Ready"
workflow = ".factory/workflows/triage.md"
"#,
    )
    .replace("runtime = \"codex\"", "runtime = \"claude\"");
    let fixture = Fixture::new(&config);
    assert!(format!("{:#}", fixture.config().unwrap_err()).contains("must be \"codex\""));

    let config = config
        .replace("runtime = \"claude\"", "runtime = \"codex\"")
        .replace("type = \"github\"", "type = \"jira\"");
    let fixture = Fixture::new(&config);
    assert!(format!("{:#}", fixture.config().unwrap_err()).contains("use source.command"));
}

#[test]
fn catalog_rejects_frontmatter_and_missing_files() {
    let fixture = Fixture::new(&config_with(
        r#"[trigger.triage]
type = "status"
status = "Ready"
workflow = ".factory/workflows/triage.md"
"#,
    ));
    fs::write(
        fixture.repository.join(".factory/workflows/triage.md"),
        "+++\nlabel = \"old\"\n+++\nPrompt\n",
    )
    .unwrap();
    let catalog = WorkflowCatalog::load(&fixture.config().unwrap()).unwrap();
    assert!(catalog.validate_all().is_err());

    fs::remove_file(fixture.repository.join(".factory/workflows/triage.md")).unwrap();
    let catalog = WorkflowCatalog::load(&fixture.config().unwrap()).unwrap();
    assert!(catalog.validate_all().is_err());
}
