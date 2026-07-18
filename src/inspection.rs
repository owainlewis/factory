use std::sync::OnceLock;

use regex::Regex;
use serde::Serialize;

use crate::storage::{Run, Task, TaskState};

const MAX_SUMMARY_BYTES: usize = 240;
const MAX_DETAIL_BYTES: usize = 16 * 1024;

#[derive(Debug, Serialize)]
pub struct TaskView {
    pub id: i64,
    pub repository: String,
    pub workflow: String,
    pub source_item: Option<String>,
    pub state: &'static str,
    pub created_at: i64,
    pub updated_at: i64,
}

impl From<&Task> for TaskView {
    fn from(task: &Task) -> Self {
        Self {
            id: task.id,
            repository: task.repository.clone(),
            workflow: task.workflow.clone(),
            source_item: task.source_item.clone(),
            state: task_state(task.state),
            created_at: task.created_at,
            updated_at: task.updated_at,
        }
    }
}

#[derive(Debug, Serialize)]
pub struct RunView {
    pub id: i64,
    pub task_id: i64,
    pub repository: String,
    pub workflow: String,
    pub source_item: Option<String>,
    pub runtime: String,
    pub outcome: String,
    pub started_at: i64,
    pub finished_at: Option<i64>,
    pub duration_ms: Option<i64>,
    pub summary: Option<String>,
    pub cancellation_requested_at: Option<i64>,
    pub owner_pid: Option<u32>,
    pub owner_id: Option<String>,
}

impl From<&Run> for RunView {
    fn from(run: &Run) -> Self {
        let summary = run
            .error
            .as_deref()
            .or(run.result.as_deref())
            .map(sanitize)
            .map(|value| truncate(&value, MAX_SUMMARY_BYTES).0);
        Self {
            id: run.id,
            task_id: run.task_id,
            repository: run.repository.clone(),
            workflow: run.workflow.clone(),
            source_item: run.source_item.clone(),
            runtime: run.runtime.clone(),
            outcome: run.outcome.clone(),
            started_at: run.started_at,
            finished_at: run.finished_at,
            duration_ms: run
                .finished_at
                .map(|finished| finished.saturating_sub(run.started_at)),
            summary,
            cancellation_requested_at: run.cancellation_requested_at,
            owner_pid: run.owner_pid,
            owner_id: run.owner_id.clone(),
        }
    }
}

#[derive(Debug, Serialize)]
pub struct BoundedText {
    pub value: String,
    pub truncated: bool,
}

impl BoundedText {
    fn new(value: &str) -> Self {
        let value = sanitize(value);
        let (value, truncated) = truncate(&value, MAX_DETAIL_BYTES);
        Self { value, truncated }
    }
}

#[derive(Debug, Serialize)]
pub struct RunInspection {
    pub run: RunView,
    pub task: TaskView,
    pub session_id: Option<String>,
    pub result: Option<BoundedText>,
    pub error: Option<BoundedText>,
}

impl RunInspection {
    pub fn new(run: &Run, task: &Task) -> Self {
        Self {
            run: RunView::from(run),
            task: TaskView::from(task),
            session_id: run.session_id.clone(),
            result: run.result.as_deref().map(BoundedText::new),
            error: run.error.as_deref().map(BoundedText::new),
        }
    }
}

pub fn print_tasks(tasks: &[Task]) {
    println!("ID\tSTATE\tREPOSITORY\tWORKFLOW\tSOURCE\tCREATED\tUPDATED");
    for task in tasks {
        println!(
            "{}\t{}\t{}\t{}\t{}\t{}\t{}",
            task.id,
            task_state(task.state),
            safe_column(&task.repository, 48),
            safe_column(&task.workflow, 36),
            safe_column(task.source_item.as_deref().unwrap_or("-"), 24),
            task.created_at,
            task.updated_at,
        );
    }
}

pub fn print_runs(runs: &[Run]) {
    println!("ID\tOUTCOME\tRUNTIME\tWORKFLOW\tREPOSITORY\tSOURCE\tDURATION_MS\tSUMMARY");
    for run in runs {
        let view = RunView::from(run);
        println!(
            "{}\t{}\t{}\t{}\t{}\t{}\t{}\t{}",
            view.id,
            safe_column(&view.outcome, 16),
            safe_column(&view.runtime, 16),
            safe_column(&view.workflow, 36),
            safe_column(&view.repository, 48),
            safe_column(view.source_item.as_deref().unwrap_or("-"), 24),
            view.duration_ms
                .map_or_else(|| "-".to_owned(), |value| value.to_string()),
            safe_column(view.summary.as_deref().unwrap_or("-"), MAX_SUMMARY_BYTES),
        );
    }
}

pub fn print_inspection(inspection: &RunInspection) {
    println!("Run: {}", inspection.run.id);
    println!("Task: {}", inspection.task.id);
    println!("Repository: {}", safe_text(&inspection.run.repository));
    println!("Workflow: {}", safe_text(&inspection.run.workflow));
    println!(
        "Source: {}",
        safe_text(inspection.run.source_item.as_deref().unwrap_or("-"))
    );
    println!("Runtime: {}", safe_text(&inspection.run.runtime));
    println!("Outcome: {}", safe_text(&inspection.run.outcome));
    println!("Started: {}", inspection.run.started_at);
    println!(
        "Finished: {}",
        inspection
            .run
            .finished_at
            .map_or_else(|| "-".to_owned(), |value| value.to_string())
    );
    println!(
        "Duration ms: {}",
        inspection
            .run
            .duration_ms
            .map_or_else(|| "-".to_owned(), |value| value.to_string())
    );
    println!(
        "Session: {}",
        safe_text(inspection.session_id.as_deref().unwrap_or("-"))
    );
    print_detail("Result", inspection.result.as_ref());
    print_detail("Error", inspection.error.as_ref());
}

fn print_detail(label: &str, detail: Option<&BoundedText>) {
    match detail {
        Some(detail) => {
            let suffix = if detail.truncated { " [truncated]" } else { "" };
            println!("{label}{suffix}: {}", safe_text(&detail.value));
        }
        None => println!("{label}: -"),
    }
}

fn task_state(state: TaskState) -> &'static str {
    match state {
        TaskState::Queued => "queued",
        TaskState::Running => "running",
        TaskState::Succeeded => "succeeded",
        TaskState::Failed => "failed",
        TaskState::Cancelled => "cancelled",
    }
}

fn safe_column(value: &str, maximum: usize) -> String {
    let (value, truncated) = truncate(value, maximum);
    let mut value = safe_text(&value);
    if truncated {
        value.push('…');
    }
    value
}

fn safe_text(value: &str) -> String {
    value.chars().flat_map(char::escape_default).collect()
}

fn truncate(value: &str, maximum_bytes: usize) -> (String, bool) {
    if value.len() <= maximum_bytes {
        return (value.to_owned(), false);
    }
    let mut end = maximum_bytes;
    while !value.is_char_boundary(end) {
        end -= 1;
    }
    (value[..end].to_owned(), true)
}

fn sanitize(value: &str) -> String {
    static PRIVATE_KEY: OnceLock<Regex> = OnceLock::new();
    static ASSIGNMENT: OnceLock<Regex> = OnceLock::new();
    static BEARER: OnceLock<Regex> = OnceLock::new();
    static TOKEN_PREFIX: OnceLock<Regex> = OnceLock::new();
    static URI_USERINFO: OnceLock<Regex> = OnceLock::new();
    static AWS_ACCESS_KEY: OnceLock<Regex> = OnceLock::new();

    let value = PRIVATE_KEY
        .get_or_init(|| {
            Regex::new(
                r"(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)",
            )
            .expect("private-key redaction pattern is valid")
        })
        .replace_all(value, "[REDACTED PRIVATE KEY]");
    let value = BEARER
        .get_or_init(|| {
            Regex::new(r"(?i)\bbearer\s+[A-Za-z0-9._~+/-]+=*")
                .expect("bearer redaction pattern is valid")
        })
        .replace_all(&value, "Bearer [REDACTED]");
    let value = ASSIGNMENT
        .get_or_init(|| {
            Regex::new(
                r#"(?i)([\"']?(?:[A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|ACCESS_KEY|PRIVATE_KEY|CREDENTIAL|COOKIE)[A-Z0-9_]*|DATABASE_URL|AUTHORIZATION)[\"']?\s*[:=]\s*)(?:\"(?:\\.|[^\"\\])*(?:\"|\z)|'(?:\\.|[^'\\])*(?:'|\z)|[^\s,}]+)"#,
            )
            .expect("credential-assignment redaction pattern is valid")
        })
        .replace_all(&value, "${1}[REDACTED]");
    let value = URI_USERINFO
        .get_or_init(|| {
            Regex::new(r"(?i)\b([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^@/\s]+@")
                .expect("credential-URI redaction pattern is valid")
        })
        .replace_all(&value, "${1}[REDACTED]@");
    let value = AWS_ACCESS_KEY
        .get_or_init(|| {
            Regex::new(r"\b(?:AKIA|ASIA|AIDA|AROA|AIPA|ANPA|ANVA|ASCA)[A-Z0-9]{16}\b")
                .expect("AWS access-key redaction pattern is valid")
        })
        .replace_all(&value, "[REDACTED]");
    TOKEN_PREFIX
        .get_or_init(|| {
            Regex::new(
                r"\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|sk-[A-Za-z0-9_-]+)\b",
            )
            .expect("token-prefix redaction pattern is valid")
        })
        .replace_all(&value, "[REDACTED]")
        .into_owned()
}
