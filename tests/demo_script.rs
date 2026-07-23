#[cfg(unix)]
mod unix {
    use std::fs;
    use std::os::unix::fs::PermissionsExt;
    use std::path::Path;
    use std::process::Command;

    fn write_fake_gh(bin: &Path) {
        let gh = bin.join("gh");
        fs::write(
            &gh,
            r#"#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$GH_LOG"
case "$1 $2" in
  "auth status") exit 0 ;;
  "issue create") printf '%s\n' 'https://github.com/owainlewis/factory/issues/123' ;;
  *) exit 64 ;;
esac
"#,
        )
        .unwrap();
        let mut permissions = fs::metadata(&gh).unwrap().permissions();
        permissions.set_mode(0o755);
        fs::set_permissions(gh, permissions).unwrap();
    }

    #[test]
    fn creates_a_demo_issue() {
        let temp = tempfile::tempdir().unwrap();
        let bin = temp.path().join("bin");
        let log = temp.path().join("gh.log");
        fs::create_dir(&bin).unwrap();
        write_fake_gh(&bin);
        let path = format!(
            "{}:{}",
            bin.display(),
            std::env::var("PATH").unwrap_or_default()
        );
        let script = Path::new(env!("CARGO_MANIFEST_DIR")).join("scripts/create-demo-issue.sh");

        let output = Command::new(script)
            .arg("A rough idea")
            .arg("Please turn this into a task.")
            .env("PATH", path)
            .env("GH_LOG", &log)
            .output()
            .unwrap();

        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        let stdout = String::from_utf8_lossy(&output.stdout);
        assert!(stdout.contains("https://github.com/owainlewis/factory/issues/123"));
        assert!(stdout.contains("factory:ready-for-spec"));
        assert!(stdout.contains("cargo run -- run"));

        let calls = fs::read_to_string(log).unwrap();
        assert!(calls.contains("issue create --repo owainlewis/factory"));
        assert!(calls.contains("--label factory:ready-for-spec"));
    }

    #[test]
    fn rejects_a_missing_idea_without_calling_github() {
        let script = Path::new(env!("CARGO_MANIFEST_DIR")).join("scripts/create-demo-issue.sh");

        let output = Command::new(script).output().unwrap();

        assert_eq!(output.status.code(), Some(2));
        assert!(String::from_utf8_lossy(&output.stderr).contains("Usage:"));
    }
}
