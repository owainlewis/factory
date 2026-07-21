use std::ffi::{OsStr, OsString};
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

use anyhow::{Context, Result, bail};

pub const MAX_RETAINED_WORKSPACES: usize = 10;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum WorkspaceKind {
    Delivery,
    Proposal,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum WorkspaceDisposition {
    Created,
    Reused,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DeliveryReuse {
    Reject,
    ExactBase,
    Owned,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Workspace {
    pub kind: WorkspaceKind,
    pub path: PathBuf,
    pub branch: Option<String>,
    pub base_branch: String,
    pub base_sha: String,
    pub disposition: WorkspaceDisposition,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CleanupPreview {
    pub path: PathBuf,
    pub branch: Option<String>,
    pub dirty: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CleanupOutcome {
    Previewed,
    Removed,
}

#[derive(Debug, Clone)]
pub struct WorkspaceManager {
    repository: PathBuf,
    workspace_root: PathBuf,
}

impl WorkspaceManager {
    pub fn new(repository: &Path, workspace_root: &Path) -> Result<Self> {
        let repository = canonical_directory("repository", repository)?;
        let workspace_root = canonical_directory("workspace root", workspace_root)?;
        if repository == workspace_root
            || repository.starts_with(&workspace_root)
            || workspace_root.starts_with(&repository)
        {
            bail!(
                "workspace root {} must not overlap repository {}",
                workspace_root.display(),
                repository.display()
            );
        }
        let manager = Self {
            repository,
            workspace_root,
        };
        let actual = PathBuf::from(manager.git(["rev-parse", "--show-toplevel"])?.trim())
            .canonicalize()
            .context("failed to canonicalize Git repository root")?;
        if actual != manager.repository {
            bail!(
                "configured repository {} is not Git's primary checkout {}",
                manager.repository.display(),
                actual.display()
            );
        }
        Ok(manager)
    }

    pub fn delivery_branch(issue: u64, title: &str) -> String {
        format!("factory/{issue}-{}", slug(title))
    }

    pub fn reconcile_startup(&self) -> Result<()> {
        self.prune()
    }

    /// Fetch the provider's live default branch without consulting either local
    /// HEAD or origin/HEAD, then return its exact commit SHA.
    pub fn fetch_default_branch(&self, base_branch: &str) -> Result<String> {
        self.validate_branch(base_branch)?;
        let source = format!("refs/heads/{base_branch}");
        let remote = format!("refs/remotes/origin/{base_branch}");
        let refspec = format!("+{source}:{remote}");
        self.git(["fetch", "--no-tags", "origin", &refspec])
            .with_context(|| format!("failed to fetch origin default branch {base_branch}"))?;
        self.resolve_commit(&remote)
    }

    pub fn prepare_delivery(
        &self,
        issue: u64,
        title: &str,
        base_branch: &str,
        base_sha: &str,
        reuse: DeliveryReuse,
    ) -> Result<Workspace> {
        if issue == 0 {
            bail!("issue number must be greater than zero");
        }
        self.validate_branch(base_branch)?;
        let base_sha = self.resolve_commit(base_sha)?;
        self.prune()?;
        let branch = Self::delivery_branch(issue, title);
        self.validate_branch(&branch)?;

        if let Some(path) = self.worktree_for_branch(&branch)? {
            if reuse == DeliveryReuse::Reject {
                bail!(
                    "Factory branch {branch} already has a worktree; a new task cannot adopt prior or unowned Git state"
                );
            }
            self.ensure_managed_path(&path)?;
            if reuse == DeliveryReuse::ExactBase {
                self.require_clean_exact_base(&path, &base_sha)?;
            }
            return Ok(Workspace {
                kind: WorkspaceKind::Delivery,
                path,
                branch: Some(branch),
                base_branch: base_branch.to_owned(),
                base_sha,
                disposition: WorkspaceDisposition::Reused,
            });
        }

        self.require_capacity()?;
        let path = self.workspace_root.join(format!("issue-{issue}"));
        self.ensure_available_target(&path)?;
        if self.branch_exists(&branch)? {
            if reuse == DeliveryReuse::Reject {
                bail!(
                    "Factory branch {branch} already exists; inspect or remove it before approving a new task"
                );
            }
            if reuse == DeliveryReuse::ExactBase
                && self.resolve_commit(&format!("refs/heads/{branch}"))? != base_sha
            {
                bail!(
                    "Factory branch {branch} does not match the reserved base; refusing stale or unowned recovery state"
                );
            }
            // Preserve an owned recovery branch exactly as it is.
            self.git_os([
                OsStr::new("worktree"),
                OsStr::new("add"),
                path.as_os_str(),
                OsStr::new(&branch),
            ])?;
        } else {
            self.git_os([
                OsStr::new("worktree"),
                OsStr::new("add"),
                OsStr::new("-b"),
                OsStr::new(&branch),
                path.as_os_str(),
                OsStr::new(&base_sha),
            ])?;
        }
        Ok(Workspace {
            kind: WorkspaceKind::Delivery,
            path,
            branch: Some(branch),
            base_branch: base_branch.to_owned(),
            base_sha,
            disposition: WorkspaceDisposition::Created,
        })
    }

    pub fn prepare_proposal(
        &self,
        run_id: i64,
        base_branch: &str,
        base_sha: &str,
        reuse: DeliveryReuse,
    ) -> Result<Workspace> {
        if run_id <= 0 {
            bail!("run id must be greater than zero");
        }
        self.validate_branch(base_branch)?;
        let base_sha = self.resolve_commit(base_sha)?;
        self.prune()?;
        let path = self.workspace_root.join(format!("proposal-{run_id}"));
        if let Some(existing) = self.registered_worktree(&path)? {
            if reuse == DeliveryReuse::Reject {
                bail!(
                    "proposal workspace {} already exists; a new task cannot adopt prior or unowned Git state",
                    path.display()
                );
            }
            self.ensure_managed_path(&existing.path)?;
            if existing.branch.is_some() {
                bail!("proposal workspace {} is not detached", path.display());
            }
            if reuse == DeliveryReuse::ExactBase {
                self.require_clean_exact_base(&existing.path, &base_sha)?;
            }
            return Ok(Workspace {
                kind: WorkspaceKind::Proposal,
                path: existing.path,
                branch: None,
                base_branch: base_branch.to_owned(),
                base_sha,
                disposition: WorkspaceDisposition::Reused,
            });
        }
        self.ensure_available_target(&path)?;
        self.git_os([
            OsStr::new("worktree"),
            OsStr::new("add"),
            OsStr::new("--detach"),
            path.as_os_str(),
            OsStr::new(&base_sha),
        ])?;
        Ok(Workspace {
            kind: WorkspaceKind::Proposal,
            path,
            branch: None,
            base_branch: base_branch.to_owned(),
            base_sha,
            disposition: WorkspaceDisposition::Created,
        })
    }

    pub fn preview_cleanup(&self, path: &Path) -> Result<CleanupPreview> {
        self.ensure_managed_path(path)?;
        let registered = self.registered_worktree(path)?.ok_or_else(|| {
            anyhow::anyhow!("{} is not a registered Git worktree", path.display())
        })?;
        let dirty = !self
            .git_in(&registered.path, ["status", "--porcelain"])?
            .trim()
            .is_empty();
        Ok(CleanupPreview {
            path: registered.path,
            branch: registered.branch,
            dirty,
        })
    }

    pub fn cleanup(&self, path: &Path, confirm: bool) -> Result<(CleanupOutcome, CleanupPreview)> {
        let preview = self.preview_cleanup(path)?;
        if !confirm {
            return Ok((CleanupOutcome::Previewed, preview));
        }
        if preview.dirty {
            self.git_os([
                OsStr::new("worktree"),
                OsStr::new("remove"),
                OsStr::new("--force"),
                preview.path.as_os_str(),
            ])?;
        } else {
            self.git_os([
                OsStr::new("worktree"),
                OsStr::new("remove"),
                preview.path.as_os_str(),
            ])?;
        }
        Ok((CleanupOutcome::Removed, preview))
    }

    pub fn cleanup_clean(&self, path: &Path) -> Result<CleanupPreview> {
        let preview = self.preview_cleanup(path)?;
        if preview.dirty {
            bail!(
                "refusing automatic cleanup of dirty delivery workspace {}",
                preview.path.display()
            );
        }
        self.git_os([
            OsStr::new("worktree"),
            OsStr::new("remove"),
            preview.path.as_os_str(),
        ])?;
        Ok(preview)
    }

    pub fn cleanup_disposable(&self, path: &Path) -> Result<CleanupPreview> {
        let preview = self.preview_cleanup(path)?;
        self.git_os([
            OsStr::new("worktree"),
            OsStr::new("remove"),
            OsStr::new("--force"),
            preview.path.as_os_str(),
        ])?;
        Ok(preview)
    }

    pub fn branch_is_pushed(&self, branch: &str) -> Result<bool> {
        self.validate_branch(branch)?;
        let local = self.resolve_commit(&format!("refs/heads/{branch}"))?;
        let remote_ref = format!("refs/heads/{branch}");
        let output = self.git(["ls-remote", "--heads", "origin", &remote_ref])?;
        let remote = output.split_whitespace().next();
        Ok(remote == Some(local.as_str()))
    }

    pub fn retained_count(&self) -> Result<usize> {
        let entries = fs::read_dir(&self.workspace_root)
            .with_context(|| format!("failed to read {}", self.workspace_root.display()))?;
        let mut count = 0;
        for entry in entries {
            let entry = entry?;
            let name = entry.file_name();
            let name = name.to_string_lossy();
            let is_delivery = name.strip_prefix("issue-").is_some_and(|issue| {
                !issue.is_empty() && issue.bytes().all(|byte| byte.is_ascii_digit())
            });
            if is_delivery && entry.file_type()?.is_dir() {
                count += 1;
            }
        }
        Ok(count)
    }

    fn worktree_for_branch(&self, branch: &str) -> Result<Option<PathBuf>> {
        let mut found = self
            .worktrees()?
            .into_iter()
            .filter(|item| item.branch.as_deref() == Some(branch));
        let path = found.next().map(|item| item.path);
        if found.next().is_some() {
            bail!("branch {branch} is checked out in multiple worktrees");
        }
        Ok(path)
    }

    fn registered_worktree(&self, path: &Path) -> Result<Option<RegisteredWorktree>> {
        let path = absolute_lexical(path)?;
        Ok(self
            .worktrees()?
            .into_iter()
            .find(|item| absolute_lexical(&item.path).is_ok_and(|candidate| candidate == path)))
    }

    fn worktrees(&self) -> Result<Vec<RegisteredWorktree>> {
        parse_worktrees(&self.git(["worktree", "list", "--porcelain"])?)
    }

    fn ensure_managed_path(&self, path: &Path) -> Result<()> {
        let path = absolute_lexical(path)?;
        if path == self.repository {
            bail!("refusing to target canonical checkout {}", path.display());
        }
        if path.parent() != Some(self.workspace_root.as_path()) {
            bail!(
                "workspace {} is outside managed root {}",
                path.display(),
                self.workspace_root.display()
            );
        }
        Ok(())
    }

    fn ensure_available_target(&self, path: &Path) -> Result<()> {
        self.ensure_managed_path(path)?;
        if path.exists() {
            bail!(
                "workspace target {} exists but is not reusable; inspect it before cleanup",
                path.display()
            );
        }
        Ok(())
    }

    fn require_clean_exact_base(&self, path: &Path, base_sha: &str) -> Result<()> {
        let head = self.resolve_commit_in(path, "HEAD")?;
        let dirty = !self
            .git_in(path, ["status", "--porcelain"])?
            .trim()
            .is_empty();
        if head != base_sha || dirty {
            bail!(
                "preparing workspace {} is not a clean checkout of reserved base {}; refusing stale or unowned recovery state",
                path.display(),
                base_sha
            );
        }
        Ok(())
    }

    fn require_capacity(&self) -> Result<()> {
        let retained = self.retained_count()?;
        if retained >= MAX_RETAINED_WORKSPACES {
            bail!(
                "workspace retention limit reached ({retained}/{MAX_RETAINED_WORKSPACES}); cleanup is required"
            );
        }
        Ok(())
    }

    fn branch_exists(&self, branch: &str) -> Result<bool> {
        let status = Command::new("git")
            .current_dir(&self.repository)
            .args(["show-ref", "--verify", "--quiet"])
            .arg(format!("refs/heads/{branch}"))
            .status()
            .context("failed to inspect Git branch")?;
        match status.code() {
            Some(0) => Ok(true),
            Some(1) => Ok(false),
            _ => bail!("git show-ref failed with status {status}"),
        }
    }

    fn resolve_commit(&self, sha: &str) -> Result<String> {
        if sha.trim().is_empty() || sha.starts_with('-') {
            bail!("base SHA must be a non-empty object name");
        }
        let expression = format!("{sha}^{{commit}}");
        Ok(self
            .git(["rev-parse", "--verify", &expression])?
            .trim()
            .to_owned())
    }

    fn resolve_commit_in(&self, directory: &Path, sha: &str) -> Result<String> {
        if sha.trim().is_empty() || sha.starts_with('-') {
            bail!("commit must be a non-empty object name");
        }
        let expression = format!("{sha}^{{commit}}");
        Ok(self
            .git_in(directory, ["rev-parse", "--verify", &expression])?
            .trim()
            .to_owned())
    }

    fn validate_branch(&self, branch: &str) -> Result<()> {
        if branch.trim().is_empty() || branch.starts_with('-') {
            bail!("base branch is invalid");
        }
        self.git(["check-ref-format", "--branch", branch])
            .with_context(|| format!("invalid base branch {branch}"))?;
        Ok(())
    }

    fn prune(&self) -> Result<()> {
        self.git(["worktree", "prune"])?;
        Ok(())
    }

    fn git<const N: usize>(&self, args: [&str; N]) -> Result<String> {
        self.git_os(args.map(OsStr::new))
    }

    fn git_os<I, S>(&self, args: I) -> Result<String>
    where
        I: IntoIterator<Item = S>,
        S: AsRef<OsStr>,
    {
        git_output(&self.repository, args)
    }

    fn git_in<const N: usize>(&self, directory: &Path, args: [&str; N]) -> Result<String> {
        git_output(directory, args.map(OsStr::new))
    }
}

#[derive(Debug)]
struct RegisteredWorktree {
    path: PathBuf,
    branch: Option<String>,
}

fn parse_worktrees(output: &str) -> Result<Vec<RegisteredWorktree>> {
    let mut result = Vec::new();
    let mut path = None;
    let mut branch = None;
    for line in output.lines().chain(std::iter::once("")) {
        if line.is_empty() {
            if let Some(path) = path.take() {
                result.push(RegisteredWorktree {
                    path,
                    branch: branch.take(),
                });
            }
        } else if let Some(value) = line.strip_prefix("worktree ") {
            path = Some(PathBuf::from(value));
        } else if let Some(value) = line.strip_prefix("branch refs/heads/") {
            branch = Some(value.to_owned());
        }
    }
    if result.is_empty() {
        bail!("Git reported no worktrees");
    }
    Ok(result)
}

fn canonical_directory(name: &str, path: &Path) -> Result<PathBuf> {
    if !path.is_absolute() || !path.is_dir() {
        bail!(
            "{name} must be an existing absolute directory: {}",
            path.display()
        );
    }
    path.canonicalize()
        .with_context(|| format!("failed to canonicalize {name} {}", path.display()))
}

fn absolute_lexical(path: &Path) -> Result<PathBuf> {
    if !path.is_absolute() {
        bail!("workspace path must be absolute: {}", path.display());
    }
    if path
        .components()
        .any(|part| matches!(part, std::path::Component::ParentDir))
    {
        bail!("workspace path must not contain '..': {}", path.display());
    }
    Ok(path.to_owned())
}

fn slug(title: &str) -> String {
    let mut result = String::new();
    let mut separator = false;
    for character in title.chars() {
        if character.is_ascii_alphanumeric() {
            if separator && !result.is_empty() && result.len() < 48 {
                result.push('-');
            }
            separator = false;
            if result.len() < 48 {
                result.push(character.to_ascii_lowercase());
            }
        } else {
            separator = true;
        }
    }
    while result.ends_with('-') {
        result.pop();
    }
    if result.is_empty() {
        "task".to_owned()
    } else {
        result
    }
}

fn git_output<I, S>(directory: &Path, args: I) -> Result<String>
where
    I: IntoIterator<Item = S>,
    S: AsRef<OsStr>,
{
    let args = args
        .into_iter()
        .map(|arg| arg.as_ref().to_owned())
        .collect::<Vec<OsString>>();
    let output = Command::new("git")
        .current_dir(directory)
        .args(&args)
        .output()
        .with_context(|| format!("failed to run git in {}", directory.display()))?;
    if !output.status.success() {
        let command = args
            .iter()
            .map(|arg| arg.to_string_lossy())
            .collect::<Vec<_>>()
            .join(" ");
        bail!(
            "git {command} failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        );
    }
    String::from_utf8(output.stdout).context("Git returned non-UTF-8 output")
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    struct Fixture {
        _temp: TempDir,
        repository: PathBuf,
        root: PathBuf,
        head: String,
    }

    impl Fixture {
        fn new() -> Self {
            let temp = tempfile::tempdir().unwrap();
            let repository = temp.path().join("repo");
            let root = temp.path().join("workspaces");
            fs::create_dir(&repository).unwrap();
            fs::create_dir(&root).unwrap();
            run(&repository, ["init", "-b", "main"]);
            run(&repository, ["config", "user.email", "factory@example.com"]);
            run(&repository, ["config", "user.name", "Factory"]);
            fs::write(repository.join("README.md"), "factory\n").unwrap();
            run(&repository, ["add", "README.md"]);
            run(&repository, ["commit", "-m", "initial"]);
            let head = run(&repository, ["rev-parse", "HEAD"]).trim().to_owned();
            let remote = temp.path().join("origin.git");
            let output = Command::new("git")
                .current_dir(temp.path())
                .args(["clone", "--bare"])
                .arg(&repository)
                .arg(&remote)
                .output()
                .unwrap();
            assert!(output.status.success());
            run(
                &repository,
                ["remote", "add", "origin", remote.to_str().unwrap()],
            );
            Self {
                _temp: temp,
                repository: repository.canonicalize().unwrap(),
                root: root.canonicalize().unwrap(),
                head,
            }
        }

        fn manager(&self) -> WorkspaceManager {
            WorkspaceManager::new(&self.repository, &self.root).unwrap()
        }
    }

    #[test]
    fn delivery_uses_sha_and_reuses_stable_issue_branch() {
        let fixture = Fixture::new();
        let manager = fixture.manager();
        let created = manager
            .prepare_delivery(
                39,
                "Own worktrees safely!",
                "main",
                &fixture.head,
                DeliveryReuse::Reject,
            )
            .unwrap();
        assert_eq!(
            created.branch.as_deref(),
            Some("factory/39-own-worktrees-safely")
        );
        assert_eq!(
            run(&created.path, ["rev-parse", "HEAD"]).trim(),
            fixture.head
        );
        let reused = manager
            .prepare_delivery(
                39,
                "Own worktrees safely!",
                "main",
                &fixture.head,
                DeliveryReuse::Owned,
            )
            .unwrap();
        assert_eq!(reused.path, created.path);
        assert_eq!(reused.branch, created.branch);
        assert_eq!(reused.disposition, WorkspaceDisposition::Reused);
    }

    #[test]
    fn preparing_recovery_only_adopts_a_clean_exact_base() {
        let fixture = Fixture::new();
        let manager = fixture.manager();
        let workspace = manager
            .prepare_delivery(
                41,
                "Crash before ready",
                "main",
                &fixture.head,
                DeliveryReuse::Reject,
            )
            .unwrap();
        assert_eq!(
            manager
                .prepare_delivery(
                    41,
                    "Crash before ready",
                    "main",
                    &fixture.head,
                    DeliveryReuse::ExactBase,
                )
                .unwrap()
                .path,
            workspace.path
        );
        fs::write(workspace.path.join("unexpected.txt"), "unowned\n").unwrap();
        assert!(
            manager
                .prepare_delivery(
                    41,
                    "Crash before ready",
                    "main",
                    &fixture.head,
                    DeliveryReuse::ExactBase,
                )
                .unwrap_err()
                .to_string()
                .contains("not a clean checkout")
        );
    }

    #[test]
    fn fetch_default_branch_returns_exact_remote_commit() {
        let fixture = Fixture::new();
        assert_eq!(
            fixture.manager().fetch_default_branch("main").unwrap(),
            fixture.head
        );
    }

    #[test]
    fn delivery_ignores_operator_branch_and_stale_origin_head() {
        let fixture = Fixture::new();
        run(&fixture.repository, ["checkout", "-b", "operator-work"]);
        fs::write(fixture.repository.join("operator.txt"), "local\n").unwrap();
        run(&fixture.repository, ["add", "operator.txt"]);
        run(&fixture.repository, ["commit", "-m", "operator work"]);
        run(
            &fixture.repository,
            [
                "symbolic-ref",
                "refs/remotes/origin/HEAD",
                "refs/remotes/origin/not-the-default",
            ],
        );
        let manager = fixture.manager();
        let fetched = manager.fetch_default_branch("main").unwrap();
        let workspace = manager
            .prepare_delivery(
                40,
                "Use remote default",
                "main",
                &fetched,
                DeliveryReuse::Reject,
            )
            .unwrap();

        assert_eq!(fetched, fixture.head);
        assert_eq!(
            run(&workspace.path, ["rev-parse", "HEAD"]).trim(),
            fixture.head
        );
        assert_eq!(
            run(&fixture.repository, ["branch", "--show-current"]).trim(),
            "operator-work"
        );
        assert!(fixture.repository.join("operator.txt").exists());
        assert!(!workspace.path.join("operator.txt").exists());
    }

    #[test]
    fn dirty_cleanup_requires_confirmation() {
        let fixture = Fixture::new();
        let manager = fixture.manager();
        let workspace = manager
            .prepare_delivery(
                7,
                "Keep changes",
                "main",
                &fixture.head,
                DeliveryReuse::Reject,
            )
            .unwrap();
        fs::write(workspace.path.join("new.txt"), "unpublished\n").unwrap();
        let (outcome, preview) = manager.cleanup(&workspace.path, false).unwrap();
        assert_eq!(outcome, CleanupOutcome::Previewed);
        assert!(preview.dirty);
        assert_eq!(
            manager.cleanup(&workspace.path, true).unwrap().0,
            CleanupOutcome::Removed
        );
        assert!(!workspace.path.exists());
    }

    #[test]
    fn confirmed_cleanup_removes_clean_worktree_and_preserves_branch() {
        let fixture = Fixture::new();
        let manager = fixture.manager();
        let workspace = manager
            .prepare_delivery(8, "Clean", "main", &fixture.head, DeliveryReuse::Reject)
            .unwrap();
        let branch = workspace.branch.clone().unwrap();
        assert_eq!(
            manager.cleanup(&workspace.path, true).unwrap().0,
            CleanupOutcome::Removed
        );
        assert!(!workspace.path.exists());
        assert!(manager.branch_exists(&branch).unwrap());
    }

    #[test]
    fn new_task_cannot_adopt_a_preserved_branch_from_an_older_task() {
        let fixture = Fixture::new();
        let manager = fixture.manager();
        let first = manager
            .prepare_delivery(
                9,
                "Stable task",
                "main",
                &fixture.head,
                DeliveryReuse::Reject,
            )
            .unwrap();
        manager.cleanup(&first.path, true).unwrap();
        fs::write(fixture.repository.join("new-base.txt"), "new\n").unwrap();
        run(&fixture.repository, ["add", "new-base.txt"]);
        run(&fixture.repository, ["commit", "-m", "new default base"]);
        run(&fixture.repository, ["push", "origin", "main"]);
        let new_base = manager.fetch_default_branch("main").unwrap();

        let error = manager
            .prepare_delivery(9, "Stable task", "main", &new_base, DeliveryReuse::Reject)
            .unwrap_err();

        assert!(error.to_string().contains("already exists"));
        assert_ne!(new_base, fixture.head);
        assert!(!first.path.exists());
    }

    #[test]
    fn canonical_checkout_is_never_a_cleanup_target() {
        let fixture = Fixture::new();
        let manager = fixture.manager();
        assert!(manager.preview_cleanup(&fixture.repository).is_err());
        assert!(fixture.repository.exists());
    }

    #[test]
    fn retention_limit_blocks_new_but_allows_reuse() {
        let fixture = Fixture::new();
        let manager = fixture.manager();
        let existing = manager
            .prepare_delivery(1, "Existing", "main", &fixture.head, DeliveryReuse::Reject)
            .unwrap();
        for index in 1..MAX_RETAINED_WORKSPACES {
            fs::create_dir(fixture.root.join(format!("issue-{}", index + 10))).unwrap();
        }
        assert!(
            manager
                .prepare_delivery(2, "Blocked", "main", &fixture.head, DeliveryReuse::Reject,)
                .is_err()
        );
        assert_eq!(
            manager
                .prepare_delivery(1, "Existing", "main", &fixture.head, DeliveryReuse::Owned,)
                .unwrap()
                .path,
            existing.path
        );
        let proposal = manager
            .prepare_proposal(99, "main", &fixture.head, DeliveryReuse::Reject)
            .unwrap();
        assert_eq!(proposal.kind, WorkspaceKind::Proposal);
        manager.cleanup_disposable(&proposal.path).unwrap();
    }

    #[test]
    fn proposal_is_detached_and_removable() {
        let fixture = Fixture::new();
        let manager = fixture.manager();
        let workspace = manager
            .prepare_proposal(91, "main", &fixture.head, DeliveryReuse::Reject)
            .unwrap();
        assert_eq!(
            run(&workspace.path, ["rev-parse", "--abbrev-ref", "HEAD"]).trim(),
            "HEAD"
        );
        manager.cleanup(&workspace.path, true).unwrap();
        assert!(!workspace.path.exists());
    }

    #[test]
    fn preparing_proposal_recovery_requires_a_clean_exact_base() {
        let fixture = Fixture::new();
        let manager = fixture.manager();
        let workspace = manager
            .prepare_proposal(92, "main", &fixture.head, DeliveryReuse::Reject)
            .unwrap();
        fs::write(workspace.path.join("unexpected.txt"), "stale state\n").unwrap();

        let error = manager
            .prepare_proposal(92, "main", &fixture.head, DeliveryReuse::ExactBase)
            .unwrap_err();

        assert!(error.to_string().contains("not a clean checkout"));
        assert_eq!(
            manager
                .prepare_proposal(92, "main", &fixture.head, DeliveryReuse::Owned)
                .unwrap()
                .path,
            workspace.path
        );
    }

    fn run<const N: usize>(directory: &Path, args: [&str; N]) -> String {
        let output = Command::new("git")
            .current_dir(directory)
            .args(args)
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "git failed: {}",
            String::from_utf8_lossy(&output.stderr)
        );
        String::from_utf8(output.stdout).unwrap()
    }
}
