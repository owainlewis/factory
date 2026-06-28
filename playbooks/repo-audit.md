# Repo Audit Playbook

Use this to inspect one repo deeply.

## Checklist

- Identify repo role.
- Read README.
- Read AGENTS.md.
- Detect language, package manager, and test command.
- Check required files.
- Check CI status.
- Check open issues and PRs.
- Check recent commits.
- Check docs for commands that do not exist.
- Check examples for files that do not exist.
- Check links.
- Check obvious TODOs.

## Report Shape

```text
Repo:
Role:
Policy:
Status:
Top risks:
Fast wins:
Needs human:
Suggested next issue:
Suggested next PR:
```

## Decision Rule

If the fix is obvious and low risk, open a PR.

If the problem is real but the fix is not obvious, open an issue.

If the problem is strategic, report it for human review.

