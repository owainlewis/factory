#!/usr/bin/env bash
set -euo pipefail

repository="owainlewis/factory"
ready_label="factory:ready-for-spec"

usage() {
  echo "Usage: $0 <idea-title> [idea-description]" >&2
  echo "Creates a real demo issue in ${repository} labelled ${ready_label}." >&2
}

if [[ $# -lt 1 || $# -gt 2 ]]; then
  usage
  exit 2
fi

title=$1
body=${2:-"This is an early idea. Investigate the repository and turn it into a clear, bounded task with testable acceptance criteria."}

if [[ -z ${title//[[:space:]]/} ]]; then
  echo "The idea title must not be empty." >&2
  exit 2
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "GitHub CLI is required. Install gh and retry." >&2
  exit 1
fi

gh auth status >/dev/null

issue_url=$(gh issue create \
  --repo "$repository" \
  --title "$title" \
  --body "$body" \
  --label "$ready_label")

echo "Demo issue: ${issue_url}"
echo "Label: ${ready_label}"
echo
echo "Next:"
echo "  1. Run: cargo run -- run"
echo "  2. Wait for the agent to refine the ticket and remove ${ready_label}."
echo "  3. Review the ticket and add factory:ready-to-implement."
echo "  4. Watch the implementation agent open a PR."
