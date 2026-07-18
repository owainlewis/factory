use std::fs;
use std::path::{Path, PathBuf};
use std::time::Duration;

use anyhow::{Context, Result, bail};

use crate::config::Config;
use crate::workflow::{WorkflowCatalog, WorkflowEntry};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ResolvedWorkflow {
    pub id: String,
    pub repository: PathBuf,
    pub working_directory: PathBuf,
    pub runtime: String,
    pub timeout: Duration,
    pub prompt: String,
}

impl ResolvedWorkflow {
    pub fn resolve(
        config: &Config,
        catalog: &WorkflowCatalog,
        id: &str,
        repository: &Path,
    ) -> Result<Self> {
        let repository = fs::canonicalize(repository).with_context(|| {
            format!(
                "repository path does not exist or cannot be resolved: {}",
                repository.display()
            )
        })?;
        if !config.repositories.contains(&repository) {
            bail!(
                "repository {} is not listed in Factory configuration",
                repository.display()
            );
        }
        let matches = catalog
            .entries
            .iter()
            .filter(|entry| entry.repository == repository && entry.id == id)
            .collect::<Vec<_>>();
        let entry = match matches.as_slice() {
            [] => bail!(
                "workflow {id:?} was not found for repository {}",
                repository.display()
            ),
            [entry] => *entry,
            _ => bail!(
                "workflow {id:?} is duplicated for repository {}",
                repository.display()
            ),
        };
        Self::from_entry(entry)
    }

    fn from_entry(entry: &WorkflowEntry) -> Result<Self> {
        if !entry.errors.is_empty() {
            bail!(
                "workflow {:?} is invalid: {}",
                entry.id,
                entry.errors.join("; ")
            );
        }
        let runtime = entry
            .runtime
            .clone()
            .context("validated workflow has no resolved runtime")?;
        let timeout = entry
            .timeout
            .context("validated workflow has no resolved timeout")?;
        let workflow_prompt = entry
            .prompt
            .as_deref()
            .context("validated workflow has no prompt body")?;
        let working_directory = entry.repository.clone();
        let prompt = compose_prompt(&entry.id, workflow_prompt, &entry.repository);

        Ok(Self {
            id: entry.id.clone(),
            repository: entry.repository.clone(),
            working_directory,
            runtime,
            timeout,
            prompt,
        })
    }
}

fn compose_prompt(id: &str, workflow_prompt: &str, repository: &Path) -> String {
    format!(
        "# Factory workflow\n\n{workflow_prompt}\n\n\
         # Resolved target\n\n\
         Workflow: {id}\n\
         Repository: {}\n\
         Working directory: {}\n",
        repository.display(),
        repository.display()
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn prompt_contains_only_workflow_and_target_context() {
        let prompt = compose_prompt(
            "read-only",
            "Inspect the repository without modifying it.",
            Path::new("/code/project"),
        );

        assert!(prompt.contains("Inspect the repository without modifying it."));
        assert!(prompt.contains("Workflow: read-only"));
        assert!(prompt.contains("Repository: /code/project"));
        assert!(!prompt.contains("max_concurrent_runs"));
        assert!(!prompt.contains("workspace_root"));
    }
}
