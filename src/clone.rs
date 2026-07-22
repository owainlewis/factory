use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

use anyhow::{Context, Result, bail};

use crate::workspace::{CleanupPreview, WorkspaceManager};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StandaloneClone {
    pub path: PathBuf,
    pub branch: Option<String>,
    pub base_branch: String,
    pub base_sha: String,
}

#[derive(Debug, Clone)]
pub struct CloneManager {
    root: PathBuf,
    gh_executable: PathBuf,
    git_executable: PathBuf,
}

impl CloneManager {
    pub fn new(root: &Path) -> Result<Self> {
        let root = root
            .canonicalize()
            .with_context(|| format!("failed to resolve clone root {}", root.display()))?;
        if !root.is_dir() {
            bail!("clone root {} is not a directory", root.display());
        }
        Ok(Self {
            root,
            gh_executable: PathBuf::from("gh"),
            git_executable: PathBuf::from("git"),
        })
    }

    #[allow(clippy::too_many_arguments)]
    pub fn prepare(
        &self,
        repository: &str,
        task_id: i64,
        issue: u64,
        title: &str,
        base_branch: &str,
        base_sha: &str,
        implementation: bool,
        github_token_env: &str,
    ) -> Result<StandaloneClone> {
        if task_id <= 0 || issue == 0 {
            bail!("clone task and issue IDs must be positive");
        }
        validate_repository(repository)?;
        validate_revision(base_sha)?;
        let path = if implementation {
            self.root.join(format!("issue-{issue}"))
        } else {
            self.root.join(format!("triage-{task_id}"))
        };
        self.ensure_managed_path(&path)?;
        let token = std::env::var(github_token_env)
            .with_context(|| format!("GitHub token environment {github_token_env:?} is missing"))?;
        if token.trim().is_empty() {
            bail!("GitHub token environment {github_token_env:?} is empty");
        }
        if !path.exists() {
            let staging = tempfile::Builder::new()
                .prefix(".factory-clone-")
                .tempdir_in(&self.root)
                .context("failed to create clone staging directory")?;
            let staged_clone = staging.path().join("repository");
            let clone_url = format!("https://github.com/{repository}.git");
            let status = Command::new(&self.gh_executable)
                .args(["repo", "clone", &clone_url])
                .arg(&staged_clone)
                .args(["--", "--no-checkout", "--no-tags"])
                .env("GH_TOKEN", &token)
                .status()
                .context("failed to start gh repo clone")?;
            if !status.success() {
                bail!("gh repo clone failed with {status}");
            }
            if !staged_clone.join(".git").is_dir() {
                bail!("gh repo clone did not produce standalone Git metadata");
            }
            fs::rename(&staged_clone, &path).with_context(|| {
                format!("failed to publish completed clone at {}", path.display())
            })?;
        }
        self.require_standalone_clone(&path, repository)?;
        git(
            &self.git_executable,
            &path,
            [
                "config",
                "credential.https://github.com.helper",
                "!gh auth git-credential",
            ],
            &token,
        )?;
        let remote_ref = format!("refs/remotes/origin/{base_branch}");
        let refspec = format!("+refs/heads/{base_branch}:{remote_ref}");
        git(
            &self.git_executable,
            &path,
            ["fetch", "--no-tags", "origin", &refspec],
            &token,
        )?;
        let revision = format!("{base_sha}^{{commit}}");
        let resolved = git_output(
            &self.git_executable,
            &path,
            ["rev-parse", "--verify", &revision],
            &token,
        )?;
        if resolved.trim() != base_sha {
            bail!("clone base commit does not match the reserved source commit");
        }
        let branch = implementation.then(|| WorkspaceManager::delivery_branch(issue, title));
        if let Some(branch) = &branch {
            let remote_branch = format!("refs/remotes/origin/{branch}");
            let source_branch = format!("refs/heads/{branch}");
            let local_branch = Command::new(&self.git_executable)
                .args(["show-ref", "--verify", "--quiet", &source_branch])
                .current_dir(&path)
                .env("GH_TOKEN", &token)
                .status()
                .context("failed to inspect local implementation branch")?
                .code();
            if local_branch == Some(0) {
                git(&self.git_executable, &path, ["checkout", branch], &token)?;
                return Ok(StandaloneClone {
                    path,
                    branch: Some(branch.clone()),
                    base_branch: base_branch.to_owned(),
                    base_sha: base_sha.to_owned(),
                });
            }
            if local_branch != Some(1) {
                bail!("failed to inspect local implementation branch");
            }
            let remote = Command::new(&self.git_executable)
                .args([
                    "ls-remote",
                    "--exit-code",
                    "--heads",
                    "origin",
                    &source_branch,
                ])
                .current_dir(&path)
                .env("GH_TOKEN", &token)
                .status()
                .context("failed to inspect implementation branch")?
                .code();
            if remote == Some(0) {
                let branch_refspec = format!("+refs/heads/{branch}:{remote_branch}");
                git(
                    &self.git_executable,
                    &path,
                    ["fetch", "--no-tags", "origin", &branch_refspec],
                    &token,
                )?;
                git(
                    &self.git_executable,
                    &path,
                    ["checkout", "-B", branch, &remote_branch],
                    &token,
                )?;
            } else if remote == Some(2) {
                git(
                    &self.git_executable,
                    &path,
                    ["checkout", "-B", branch, base_sha],
                    &token,
                )?;
            } else {
                bail!("failed to inspect remote implementation branch");
            }
        } else {
            git(
                &self.git_executable,
                &path,
                ["checkout", "--detach", base_sha],
                &token,
            )?;
        }
        Ok(StandaloneClone {
            path,
            branch,
            base_branch: base_branch.to_owned(),
            base_sha: base_sha.to_owned(),
        })
    }

    pub fn remove(&self, path: &Path) -> Result<()> {
        self.ensure_managed_path(path)?;
        if path.exists() {
            fs::remove_dir_all(path)
                .with_context(|| format!("failed to remove clone {}", path.display()))?;
        }
        Ok(())
    }

    pub fn preview_cleanup(&self, path: &Path) -> Result<CleanupPreview> {
        self.ensure_managed_path(path)?;
        if !path.join(".git").is_dir() {
            bail!("{} is not a standalone Git clone", path.display());
        }
        let dirty = !git_output(&self.git_executable, path, ["status", "--porcelain"], "")?
            .trim()
            .is_empty();
        let branch = git_output(&self.git_executable, path, ["branch", "--show-current"], "")?;
        Ok(CleanupPreview {
            path: path.to_owned(),
            branch: (!branch.trim().is_empty()).then(|| branch.trim().to_owned()),
            dirty,
        })
    }

    pub fn branch_is_pushed(
        &self,
        path: &Path,
        branch: &str,
        github_token_env: &str,
    ) -> Result<bool> {
        self.ensure_managed_path(path)?;
        let token = std::env::var(github_token_env)
            .with_context(|| format!("GitHub token environment {github_token_env:?} is missing"))?;
        let local = git_output(&self.git_executable, path, ["rev-parse", "HEAD"], &token)?;
        let remote_ref = format!("refs/heads/{branch}");
        let remote = git_output(
            &self.git_executable,
            path,
            ["ls-remote", "--heads", "origin", &remote_ref],
            &token,
        )?;
        Ok(remote.split_whitespace().next() == Some(local.trim()))
    }

    fn ensure_managed_path(&self, path: &Path) -> Result<()> {
        let parent = path
            .parent()
            .context("clone path has no parent")?
            .canonicalize()
            .context("failed to resolve clone parent")?;
        if parent != self.root || path == self.root {
            bail!(
                "clone path {} is outside {}",
                path.display(),
                self.root.display()
            );
        }
        if let Ok(metadata) = fs::symlink_metadata(path)
            && metadata.file_type().is_symlink()
        {
            bail!("clone path {} must not be a symlink", path.display());
        }
        Ok(())
    }

    fn require_standalone_clone(&self, path: &Path, repository: &str) -> Result<()> {
        if !path.join(".git").is_dir() {
            bail!(
                "clone {} does not contain standalone Git metadata",
                path.display()
            );
        }
        let origin = git_output(
            &self.git_executable,
            path,
            ["remote", "get-url", "origin"],
            "",
        )?;
        let expected_suffix = format!("/{repository}.git");
        if !origin
            .trim()
            .trim_end_matches('/')
            .ends_with(&expected_suffix)
        {
            bail!(
                "clone origin {:?} does not match {repository}",
                origin.trim()
            );
        }
        Ok(())
    }
}

fn validate_repository(repository: &str) -> Result<()> {
    let mut parts = repository.split('/');
    if !matches!(
        (parts.next(), parts.next(), parts.next()),
        (Some(owner), Some(repo), None) if !owner.is_empty() && !repo.is_empty()
    ) {
        bail!("invalid GitHub repository identity {repository:?}");
    }
    Ok(())
}

fn validate_revision(revision: &str) -> Result<()> {
    if revision.len() != 40 || !revision.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        bail!("base commit must be a full Git SHA");
    }
    Ok(())
}

fn git<const N: usize>(
    executable: &Path,
    repository: &Path,
    arguments: [&str; N],
    token: &str,
) -> Result<()> {
    let status = Command::new(executable)
        .args(arguments)
        .current_dir(repository)
        .env("GH_TOKEN", token)
        .status()
        .context("failed to start Git")?;
    if !status.success() {
        bail!("Git command failed with {status}");
    }
    Ok(())
}

fn git_output<const N: usize>(
    executable: &Path,
    repository: &Path,
    arguments: [&str; N],
    token: &str,
) -> Result<String> {
    let output = Command::new(executable)
        .args(arguments)
        .current_dir(repository)
        .env("GH_TOKEN", token)
        .output()
        .context("failed to start Git")?;
    if !output.status.success() {
        bail!(
            "Git command failed with {}: {}",
            output.status,
            String::from_utf8_lossy(&output.stderr).trim()
        );
    }
    String::from_utf8(output.stdout).context("Git output was not UTF-8")
}

#[cfg(all(test, unix))]
mod tests {
    use std::fs;
    use std::os::unix::fs::PermissionsExt;

    use super::*;

    const BASE_SHA: &str = "0123456789abcdef0123456789abcdef01234567";
    const TOKEN_ENV: &str = "FACTORY_CLONE_TEST_TOKEN";

    fn executable(path: &Path, contents: &str) {
        fs::write(path, contents).unwrap();
        let mut permissions = fs::metadata(path).unwrap().permissions();
        permissions.set_mode(0o700);
        fs::set_permissions(path, permissions).unwrap();
    }

    #[test]
    fn prepares_standalone_triage_and_stable_implementation_clones() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("clones");
        fs::create_dir(&root).unwrap();
        let root = root.canonicalize().unwrap();
        let log = temp.path().join("commands.log");
        let gh = temp.path().join("gh");
        let git = temp.path().join("git");
        executable(
            &gh,
            &format!(
                r#"#!/bin/sh
set -eu
printf 'gh|%s\n' "$*" >> '{}'
test "$GH_TOKEN" = 'clone-test-token'
test "$1 $2" = 'repo clone'
mkdir -p "$4/.git"
"#,
                log.display()
            ),
        );
        executable(
            &git,
            &format!(
                r#"#!/bin/sh
set -eu
printf '%s|%s\n' "$PWD" "$*" >> '{}'
case "$1 ${{2:-}}" in
  'remote get-url') printf '%s\n' 'https://github.com/acme/widgets.git' ;;
  'rev-parse --verify') printf '%s\n' '{}' ;;
  'show-ref --verify')
    if test -f .fake-local-branch && test "$(cat .fake-local-branch)" = "${{4#refs/heads/}}"; then exit 0; else exit 1; fi
    ;;
  'ls-remote --exit-code') exit 2 ;;
  'checkout -B') printf '%s' "$3" > .fake-local-branch ;;
  'checkout factory/'*) printf '%s' "$2" > .fake-local-branch ;;
esac
"#,
                log.display(),
                BASE_SHA
            ),
        );
        // SAFETY: this test uses a process-unique environment variable name and
        // no other test in this crate reads or writes it.
        unsafe { std::env::set_var(TOKEN_ENV, "clone-test-token") };
        let manager = CloneManager {
            root: root.clone(),
            gh_executable: gh,
            git_executable: git,
        };

        let triage = manager
            .prepare(
                "acme/widgets",
                7,
                42,
                "Fix login timeout",
                "main",
                BASE_SHA,
                false,
                TOKEN_ENV,
            )
            .unwrap();
        assert_eq!(triage.path, root.join("triage-7"));
        assert_eq!(triage.branch, None);
        assert!(triage.path.join(".git").is_dir());

        let implementation = manager
            .prepare(
                "acme/widgets",
                8,
                42,
                "Fix login timeout",
                "main",
                BASE_SHA,
                true,
                TOKEN_ENV,
            )
            .unwrap();
        assert_eq!(implementation.path, root.join("issue-42"));
        assert_eq!(implementation.branch.as_deref(), Some("factory/42"));
        assert!(implementation.path.join(".git").is_dir());

        fs::write(implementation.path.join("local-work"), "preserve me").unwrap();
        manager
            .prepare(
                "acme/widgets",
                9,
                42,
                "Fix login timeout",
                "main",
                BASE_SHA,
                true,
                TOKEN_ENV,
            )
            .unwrap();
        assert_eq!(
            fs::read_to_string(implementation.path.join("local-work")).unwrap(),
            "preserve me"
        );

        let commands = fs::read_to_string(log).unwrap();
        assert!(commands.contains("gh|repo clone https://github.com/acme/widgets.git"));
        assert!(commands.contains("checkout --detach"));
        assert_eq!(commands.matches("checkout -B factory/42").count(), 1);
        assert!(commands.contains("checkout factory/42"));
        assert!(!commands.contains("clone-test-token"));
    }

    #[test]
    fn fresh_clone_checks_out_existing_remote_branch_after_title_change() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("clones");
        fs::create_dir(&root).unwrap();
        let root = root.canonicalize().unwrap();
        let log = temp.path().join("commands.log");
        let remote_branch = temp.path().join("remote-branch");
        let gh = temp.path().join("gh");
        let git = temp.path().join("git");
        executable(
            &gh,
            &format!(
                r#"#!/bin/sh
set -eu
printf 'gh|%s\n' "$*" >> '{}'
test "$GH_TOKEN" = 'clone-test-token'
mkdir -p "$4/.git"
"#,
                log.display()
            ),
        );
        executable(
            &git,
            &format!(
                r#"#!/bin/sh
set -eu
printf '%s|%s\n' "$PWD" "$*" >> '{}'
case "$1 ${{2:-}}" in
  'remote get-url') printf '%s\n' 'https://github.com/acme/widgets.git' ;;
  'rev-parse --verify') printf '%s\n' '{}' ;;
  'show-ref --verify') exit 1 ;;
  'ls-remote --exit-code') if test -f '{}'; then exit 0; else exit 2; fi ;;
  'checkout -B') printf '%s' "$3" > .fake-local-branch ;;
esac
"#,
                log.display(),
                BASE_SHA,
                remote_branch.display()
            ),
        );
        unsafe { std::env::set_var(TOKEN_ENV, "clone-test-token") };
        let manager = CloneManager {
            root: root.clone(),
            gh_executable: gh,
            git_executable: git,
        };

        let first = manager
            .prepare(
                "acme/widgets",
                8,
                42,
                "Original title",
                "main",
                BASE_SHA,
                true,
                TOKEN_ENV,
            )
            .unwrap();
        assert_eq!(first.branch.as_deref(), Some("factory/42"));

        fs::write(&remote_branch, "factory/42").unwrap();
        manager.remove(&first.path).unwrap();
        let continued = manager
            .prepare(
                "acme/widgets",
                9,
                42,
                "A renamed issue title",
                "main",
                BASE_SHA,
                true,
                TOKEN_ENV,
            )
            .unwrap();

        assert_eq!(continued.path, root.join("issue-42"));
        assert_eq!(continued.branch.as_deref(), Some("factory/42"));
        let commands = fs::read_to_string(log).unwrap();
        assert_eq!(commands.matches("gh|repo clone").count(), 2);
        assert!(commands.contains("ls-remote --exit-code --heads origin refs/heads/factory/42"));
        assert!(commands.contains(
            "fetch --no-tags origin +refs/heads/factory/42:refs/remotes/origin/factory/42"
        ));
        assert!(commands.contains("checkout -B factory/42 refs/remotes/origin/factory/42"));
    }

    #[test]
    fn failed_initial_clone_does_not_poison_the_stable_destination() {
        let temp = tempfile::tempdir().unwrap();
        let root = temp.path().join("clones");
        fs::create_dir(&root).unwrap();
        let gh = temp.path().join("gh");
        let git = temp.path().join("git");
        executable(&gh, "#!/bin/sh\nmkdir -p \"$4/.git\"\nexit 1\n");
        executable(&git, "#!/bin/sh\nexit 64\n");
        unsafe { std::env::set_var(TOKEN_ENV, "clone-test-token") };
        let manager = CloneManager {
            root: root.canonicalize().unwrap(),
            gh_executable: gh,
            git_executable: git,
        };

        assert!(
            manager
                .prepare(
                    "acme/widgets",
                    99,
                    42,
                    "Fix login timeout",
                    "main",
                    BASE_SHA,
                    true,
                    TOKEN_ENV,
                )
                .is_err()
        );
        assert!(!root.join("issue-42").exists());
        assert_eq!(fs::read_dir(root).unwrap().count(), 0);
    }
}
