# Merge one Dependabot update

Use the authenticated `gh` CLI to find open pull requests in this repository
authored by `dependabot[bot]`.

Choose at most one pull request that satisfies every condition:

- it is not a draft;
- GitHub reports it cleanly mergeable;
- all required checks have completed successfully;
- it is a patch-level dependency update;
- its diff contains only the expected dependency manifest or lockfile changes;
- it does not contain unexpected source-code changes.

Inspect the candidate with `gh pr view`, `gh pr checks`, and `gh pr diff` before
merging. Prefer the smallest, lowest-risk eligible update. Do not modify the
pull request branch or attempt to repair an ineligible update.

Merge exactly one eligible pull request with `gh pr merge <number> --squash`.
After the merge succeeds, use `gh pr comment <number> --body <message>` to state
that the scheduled Factory worker merged it after confirming the dependency
update, diff, merge state, and required checks. Then stop.

If no pull request qualifies, make no GitHub or repository changes and stop.
Never merge or comment on more than one pull request during this run.
