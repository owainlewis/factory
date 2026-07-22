#!/usr/bin/env bash
set -euo pipefail

repository="owainlewis/factory"
project_owner="owainlewis"
project_number="16"
status_field="Status"
ready_status="Ready For Spec"
trusted_user="owainlewis"

usage() {
  echo "Usage: $0 <idea-title> [idea-description]" >&2
  echo "Creates a real demo issue in ${repository} and places it in ${ready_status}." >&2
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
authenticated_user=$(gh api user --jq '.login')
if [[ "$authenticated_user" != "$trusted_user" ]]; then
  echo "The active GitHub user ${authenticated_user} is not trusted by this Factory demo. Log in as ${trusted_user}." >&2
  exit 1
fi

project_id=$(gh project view "$project_number" \
  --owner "$project_owner" \
  --format json \
  --jq '.id')

field_and_option=$(gh project field-list "$project_number" \
  --owner "$project_owner" \
  --limit 1000 \
  --format json \
  --jq '.fields[] | select(.name == "Status") | [.id, (.options[] | select(.name == "Ready For Spec") | .id)] | @tsv')

IFS=$'\t' read -r field_id option_id <<<"$field_and_option"
if [[ -z ${project_id:-} || -z ${field_id:-} || -z ${option_id:-} ]]; then
  echo "Could not resolve Project ${project_number}, field ${status_field}, or status ${ready_status}." >&2
  exit 1
fi

issue_url=$(gh issue create \
  --repo "$repository" \
  --title "$title" \
  --body "$body")

if ! item_id=$(gh project item-add "$project_number" \
  --owner "$project_owner" \
  --url "$issue_url" \
  --format json \
  --jq '.id'); then
  echo "The issue was created but could not be added to Project ${project_number}: ${issue_url}" >&2
  exit 1
fi

if ! gh project item-edit \
  --id "$item_id" \
  --project-id "$project_id" \
  --field-id "$field_id" \
  --single-select-option-id "$option_id" >/dev/null; then
  echo "The issue was added to the Project but its status could not be set: ${issue_url}" >&2
  exit 1
fi

echo "Demo issue: ${issue_url}"
echo "Project: https://github.com/users/${project_owner}/projects/${project_number}"
echo "Status: ${ready_status}"
echo
echo "Next:"
echo "  1. Run: cargo run -- run"
echo "  2. Wait for the agent to refine the ticket and leave it in Creating Spec."
echo "  3. Review the ticket and move it to Ready To Implement."
echo "  4. Watch the implementation agent open a PR and move it to Reviewing."
