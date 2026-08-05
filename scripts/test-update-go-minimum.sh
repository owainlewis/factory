#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

mkdir -p "$fixture/scripts" "$fixture/docs" "$fixture/.github/ISSUE_TEMPLATE"
cp "$repository_root/go.mod" "$repository_root/README.md" \
  "$repository_root/CONTRIBUTING.md" "$repository_root/SECURITY.md" "$fixture/"
cp "$repository_root/docs/local.md" "$fixture/docs/local.md"
cp "$repository_root/.github/ISSUE_TEMPLATE/bug_report.yml" \
  "$fixture/.github/ISSUE_TEMPLATE/bug_report.yml"
cp "$repository_root/scripts/update-go-minimum.sh" "$fixture/scripts/update-go-minimum.sh"

current="$(awk '$1 == "go" { print $2; exit }' "$fixture/go.mod")"
current_patch="${current##*.}"
lower="${current%.*}.$((10#$current_patch - 1))"
higher="${current%.*}.$((10#$current_patch + 1))"

lower_output="$("$fixture/scripts/update-go-minimum.sh" "go$lower")"
grep -Fq "Refusing to lower Go minimum from $current to $lower" <<<"$lower_output"
grep -qx "go $current" "$fixture/go.mod"

"$fixture/scripts/update-go-minimum.sh" "go$current" >/dev/null
grep -qx "go $current" "$fixture/go.mod"

"$fixture/scripts/update-go-minimum.sh" "go$higher" >/dev/null
grep -qx "go $higher" "$fixture/go.mod"
if grep -R -Fq "$current" \
  "$fixture/README.md" "$fixture/CONTRIBUTING.md" "$fixture/SECURITY.md" \
  "$fixture/docs/local.md" "$fixture/.github/ISSUE_TEMPLATE/bug_report.yml"; then
  echo "Go minimum updater left stale documentation" >&2
  exit 1
fi

echo "Go minimum updater refuses downgrades and updates matching documentation."
