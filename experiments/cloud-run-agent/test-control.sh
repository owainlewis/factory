#!/usr/bin/env bash

set -Eeuo pipefail

readonly script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT

readonly source_repo="${temp_root}/source"
mkdir -p "$source_repo"
git -C "$source_repo" init --quiet --initial-branch main
printf 'fixture\n' > "${source_repo}/README.md"
git -C "$source_repo" add README.md
git -C "$source_repo" -c user.name=Factory -c user.email=factory@example.invalid \
    commit --quiet -m fixture
readonly source_commit="$(git -C "$source_repo" rev-parse HEAD)"
git -C "$source_repo" -c user.name=Factory -c user.email=factory@example.invalid \
    tag -a annotated -m annotated
readonly tag_object="$(git -C "$source_repo" rev-parse annotated)"

resolved_commit="$("${script_dir}/resolve-git-ref.sh" "$source_repo" annotated)"
[[ "$resolved_commit" == "$source_commit" ]]
[[ "$resolved_commit" != "$tag_object" ]]

readonly fake_bin="${temp_root}/bin"
readonly output_root="${temp_root}/results"
readonly fake_archive="${temp_root}/attempt-result.tar.gz"
mkdir -p "$fake_bin"

cat > "${fake_bin}/gcloud" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "$1 $2 $3" == "auth print-access-token " ]]; then
    printf 'test-token\n'
    exit 0
fi
if [[ "$1 $2" == "storage cp" ]]; then
    if [[ "$3" == gs://* ]]; then
        [[ "${FAKE_ARTIFACT_MISSING:-}" != 1 ]] || exit 1
        cp "${FAKE_RESULT_ARCHIVE:?}" "$4"
    fi
    exit 0
fi
printf 'unexpected gcloud call: %s\n' "$*" >&2
exit 1
EOF
chmod 0755 "${fake_bin}/gcloud"

cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
for argument in "$@"; do
    if [[ "$argument" == *':run' ]]; then
        printf '%s\n' '{"metadata":{"name":"projects/factory-505220/locations/europe-west1/jobs/factory-agent-experiment/executions/execution-test"}}'
        exit 0
    fi
done
printf 'simulated polling interruption\n' >&2
exit 22
EOF
chmod 0755 "${fake_bin}/curl"

printf 'inspect this repository\n' > "${temp_root}/prompt.txt"
set +e
PATH="${fake_bin}:$PATH" \
PROJECT_ID=factory-505220 \
GIT_COMMIT="$source_commit" \
ATTEMPT_ID=attempt-launch-record \
OUTPUT_ROOT="$output_root" \
    "${script_dir}/execute.sh" "${temp_root}/prompt.txt" > "${temp_root}/execute-output" 2>&1
execute_exit="$?"
set -e

[[ "$execute_exit" -ne 0 ]]
readonly launch_path="${output_root}/attempt-launch-record/launch.json"
[[ -s "$launch_path" ]]
[[ "$(jq -r '.attempt' "$launch_path")" == attempt-launch-record ]]
[[ "$(jq -r '.execution' "$launch_path")" == execution-test ]]
[[ "$(jq -r '.commit' "$launch_path")" == "$source_commit" ]]
grep -F -- 'Resume:' "${temp_root}/execute-output" >/dev/null

readonly result_fixture="${temp_root}/result-fixture"
mkdir -p "$result_fixture"
printf '%s\n' '{"attempt_id":"attempt-launch-record"}' > "${result_fixture}/result.json"
: > "${result_fixture}/changes.patch"
: > "${result_fixture}/status.txt"
: > "${result_fixture}/events.jsonl"
digest_file() { shasum -a 256 "$1" | awk '{print $1}'; }
jq -nc \
    --arg attempt_id attempt-launch-record \
    --arg commit "$source_commit" \
    --arg result "$(digest_file "${result_fixture}/result.json")" \
    --arg patch "$(digest_file "${result_fixture}/changes.patch")" \
    --arg status "$(digest_file "${result_fixture}/status.txt")" \
    --arg events "$(digest_file "${result_fixture}/events.jsonl")" \
    '{version:1,attempt_id:$attempt_id,commit:$commit,files:{"result.json":$result,"changes.patch":$patch,"status.txt":$status,"events.jsonl":$events}}' \
    > "${result_fixture}/manifest.json"
tar -czf "$fake_archive" -C "$result_fixture" \
    manifest.json result.json changes.patch status.txt events.jsonl

cat > "${fake_bin}/curl" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
counter_path="${FAKE_CURL_COUNTER:?}"
counter=0
[[ ! -f "$counter_path" ]] || counter="$(cat "$counter_path")"
counter=$((counter + 1))
printf '%s' "$counter" > "$counter_path"
if (( counter == 1 )); then
    printf '%s\n' '{"name":"projects/factory-505220/locations/europe-west1/jobs/factory-agent-experiment/executions/execution-test","conditions":[{"type":"Completed","state":"CONDITION_RECONCILING"}]}'
else
    printf '%s\n' '{"name":"projects/factory-505220/locations/europe-west1/jobs/factory-agent-experiment/executions/execution-test","conditions":[{"type":"Completed","state":"CONDITION_SUCCEEDED"}]}'
fi
EOF
chmod 0755 "${fake_bin}/curl"

set +e
PATH="${fake_bin}:$PATH" \
PROJECT_ID=factory-505220 \
OUTPUT_ROOT="$output_root" \
WAIT_SECONDS=10 \
DELETE_EXECUTION_ON_TERMINAL=false \
FAKE_CURL_COUNTER="${temp_root}/curl-counter" \
FAKE_RESULT_ARCHIVE="$fake_archive" \
    "${script_dir}/inspect.sh" attempt-launch-record > "${temp_root}/inspect-output" 2>&1
inspect_exit="$?"
set -e
[[ "$inspect_exit" -eq 0 ]]
[[ "$(cat "${temp_root}/curl-counter")" -eq 2 ]]
grep -F -- 'Verified result:' "${temp_root}/inspect-output" >/dev/null
[[ "$(jq -r '.state' "${output_root}/attempt-launch-record/execution.json")" == CONDITION_SUCCEEDED ]]

rm -f "${output_root}/attempt-launch-record/attempt-result.tar.gz" \
    "${output_root}/attempt-launch-record/manifest.json" \
    "${output_root}/attempt-launch-record/result.json" \
    "${output_root}/attempt-launch-record/changes.patch" \
    "${output_root}/attempt-launch-record/status.txt" \
    "${output_root}/attempt-launch-record/events.jsonl"
: > "${temp_root}/curl-counter"
set +e
PATH="${fake_bin}:$PATH" \
PROJECT_ID=factory-505220 \
OUTPUT_ROOT="$output_root" \
WAIT_SECONDS=10 \
DELETE_EXECUTION_ON_TERMINAL=true \
FAKE_CURL_COUNTER="${temp_root}/curl-counter" \
FAKE_RESULT_ARCHIVE="$fake_archive" \
FAKE_ARTIFACT_MISSING=1 \
    "${script_dir}/inspect.sh" attempt-launch-record > "${temp_root}/missing-artifact-output" 2>&1
missing_artifact_exit="$?"
set -e
[[ "$missing_artifact_exit" -eq 1 ]]
grep -F -- 'Execution retained:' "${temp_root}/missing-artifact-output" >/dev/null
grep -F -- 'execution succeeded without a result artifact' "${temp_root}/missing-artifact-output" >/dev/null

printf 'cloud-run control tests passed\n'
