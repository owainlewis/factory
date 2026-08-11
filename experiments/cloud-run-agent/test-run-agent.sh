#!/usr/bin/env bash

set -Eeuo pipefail

readonly script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT

readonly source_repo="${temp_root}/source"
readonly fake_bin="${temp_root}/bin"
mkdir -p "$source_repo" "$fake_bin"

git -C "$source_repo" init --quiet --initial-branch main
printf 'fixture\n' > "${source_repo}/README.md"
git -C "$source_repo" add README.md
git -C "$source_repo" \
    -c user.name=Factory \
    -c user.email=factory@example.invalid \
    commit --quiet -m fixture
readonly source_commit="$(git -C "$source_repo" rev-parse HEAD)"

cat > "${fake_bin}/pi" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$PWD" > "${FAKE_PI_CAPTURE}/cwd"
printf '%s\n' "$@" > "${FAKE_PI_CAPTURE}/arguments"
printf 'CLOUD_RUN_AGENT_OK\n' > cloud-run-smoke.txt
printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"PRIVATE_REASONING"}}'
printf '%s\n' '{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"PRIVATE_PROMPT"}]}}'
printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"PRIVATE_THOUGHT"},{"type":"text","text":"finished"}],"usage":{"cost":{"total":0.0123}}}}'
exit "${FAKE_PI_EXIT_CODE:-0}"
EOF
chmod 0755 "${fake_bin}/pi"

run_success_test() {
    local workspace="${temp_root}/success"
    local prompt
    prompt="$(printf 'make the smoke change' | base64 | tr -d '\n')"
    mkdir -p "$workspace"

    PATH="${fake_bin}:$PATH" \
    WORKSPACE_ROOT="$workspace" \
    REPOSITORY_URL="$source_repo" \
    GIT_REF=main \
    PROMPT_B64="$prompt" \
    OPENROUTER_API_KEY=test-key \
    AGENT_MODE=write \
    FAKE_PI_CAPTURE="$workspace" \
        "$script_dir/run-agent.sh" > "${workspace}/output"

    [[ "$(cat "${workspace}/cwd")" == "${workspace}/repo" ]]
    [[ "$(git -C "${workspace}/repo" rev-parse HEAD)" == "$source_commit" ]]
    grep -Fx -- '--provider' "${workspace}/arguments" >/dev/null
    grep -Fx -- 'openrouter' "${workspace}/arguments" >/dev/null
    grep -Fx -- 'deepseek/deepseek-v4-flash' "${workspace}/arguments" >/dev/null
    grep -F -- 'CLOUD_RUN_AGENT_OK' "${workspace}/result/changes.patch" >/dev/null
    grep -F -- '"cost_usd":0.0123' "${workspace}/output" >/dev/null
    grep -F -- '"exit_code":0' "${workspace}/output" >/dev/null
    grep -F -- '"text":"finished"' "${workspace}/output" >/dev/null
    ! grep -F -- 'PRIVATE_REASONING' "${workspace}/output" >/dev/null
    ! grep -F -- 'PRIVATE_PROMPT' "${workspace}/output" >/dev/null
    ! grep -F -- 'PRIVATE_THOUGHT' "${workspace}/output" >/dev/null
    grep -F -- 'PRIVATE_REASONING' "${workspace}/result/events.jsonl" >/dev/null
}

run_agent_failure_test() {
    local workspace="${temp_root}/failure"
    local prompt
    prompt="$(printf 'fail after running' | base64 | tr -d '\n')"
    mkdir -p "$workspace"

    set +e
    PATH="${fake_bin}:$PATH" \
    WORKSPACE_ROOT="$workspace" \
    REPOSITORY_URL="$source_repo" \
    GIT_REF=main \
    PROMPT_B64="$prompt" \
    OPENROUTER_API_KEY=test-key \
    AGENT_MODE=read-only \
    FAKE_PI_CAPTURE="$workspace" \
    FAKE_PI_EXIT_CODE=7 \
        "$script_dir/run-agent.sh" > "${workspace}/output"
    local exit_code="$?"
    set -e

    [[ "$exit_code" -eq 7 ]]
    grep -F -- '"exit_code":7' "${workspace}/output" >/dev/null
}

run_invalid_mode_test() {
    local workspace="${temp_root}/invalid-mode"
    local prompt
    prompt="$(printf test | base64 | tr -d '\n')"
    mkdir -p "$workspace"

    set +e
    PATH="${fake_bin}:$PATH" \
    WORKSPACE_ROOT="$workspace" \
    REPOSITORY_URL="$source_repo" \
    GIT_REF=main \
    PROMPT_B64="$prompt" \
    OPENROUTER_API_KEY=test-key \
    AGENT_MODE=unsafe \
    FAKE_PI_CAPTURE="$workspace" \
        "$script_dir/run-agent.sh" > "${workspace}/output" 2>&1
    local exit_code="$?"
    set -e

    [[ "$exit_code" -eq 2 ]]
    grep -F -- 'AGENT_MODE must be read-only or write' "${workspace}/output" >/dev/null
}

run_malformed_prompt_test() {
    local encoded_prompt="$1"
    local case_name="$2"
    local workspace="${temp_root}/${case_name}"
    mkdir -p "$workspace"

    set +e
    PATH="${fake_bin}:$PATH" \
    WORKSPACE_ROOT="$workspace" \
    REPOSITORY_URL="$source_repo" \
    GIT_REF=main \
    PROMPT_B64="$encoded_prompt" \
    OPENROUTER_API_KEY=test-key \
    AGENT_MODE=write \
    FAKE_PI_CAPTURE="$workspace" \
        "$script_dir/run-agent.sh" > "${workspace}/output" 2>&1
    local exit_code="$?"
    set -e

    [[ "$exit_code" -eq 2 ]]
    grep -F -- 'PROMPT_B64 must contain valid base64' "${workspace}/output" >/dev/null
    [[ ! -e "${workspace}/cwd" ]]
}

run_success_test
run_agent_failure_test
run_invalid_mode_test
run_malformed_prompt_test 'bWFrZSBhIGNoYW5nZQ==!!!' malformed-characters
run_malformed_prompt_test 'bWFrZSBhIGNoYW5nZQ' truncated-alphabet

printf 'cloud-run agent tests passed\n'
