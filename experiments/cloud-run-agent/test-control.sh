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
mkdir -p "$fake_bin"

cat > "${fake_bin}/gcloud" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "$1 $2 $3" == "auth print-access-token " ]]; then
    printf 'test-token\n'
    exit 0
fi
if [[ "$1 $2" == "storage cp" ]]; then
    [[ "$3" != gs://* ]] || exit 1
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
    printf '%s\n' '{"name":"projects/factory-505220/locations/europe-west1/jobs/factory-agent-experiment/executions/execution-test","conditions":[{"type":"Completed","state":"CONDITION_FAILED"}]}'
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
    "${script_dir}/inspect.sh" attempt-launch-record > "${temp_root}/inspect-output" 2>&1
inspect_exit="$?"
set -e
[[ "$inspect_exit" -eq 1 ]]
[[ "$(cat "${temp_root}/curl-counter")" -eq 2 ]]
grep -F -- 'CONDITION_FAILED' "${temp_root}/inspect-output" >/dev/null

printf 'cloud-run control tests passed\n'
