#!/usr/bin/env bash

set -Eeuo pipefail

readonly workspace_root="${WORKSPACE_ROOT:-/workspace}"
readonly checkout_dir="${workspace_root}/repo"
readonly result_dir="${workspace_root}/result"
readonly events_path="${result_dir}/events.jsonl"
readonly patch_path="${result_dir}/changes.patch"
readonly status_path="${result_dir}/status.txt"
readonly decoded_prompt_path="${result_dir}/prompt.txt"
readonly model="${MODEL:-deepseek/deepseek-v4-flash}"
readonly thinking="${THINKING:-low}"
readonly agent_mode="${AGENT_MODE:-read-only}"

emit_error() {
    local exit_code="$?"
    jq -nc \
        --arg type "factory_agent_error" \
        --arg execution "${CLOUD_RUN_EXECUTION:-local}" \
        --arg message "agent job failed before completion" \
        --argjson exit_code "$exit_code" \
        '{type: $type, execution: $execution, exit_code: $exit_code, message: $message}'
    exit "$exit_code"
}
trap emit_error ERR

require_value() {
    local name="$1"
    if [[ -z "${!name:-}" ]]; then
        printf '%s is required\n' "$name" >&2
        return 1
    fi
}

decode_prompt() {
    if base64 --help 2>&1 | grep -q -- '--decode'; then
        printf '%s' "$PROMPT_B64" | base64 --decode
    else
        printf '%s' "$PROMPT_B64" | base64 -D
    fi
}

require_value REPOSITORY_URL
require_value GIT_REF
require_value PROMPT_B64
require_value OPENROUTER_API_KEY

if [[ "$REPOSITORY_URL" == -* || "$GIT_REF" == -* ]]; then
    printf 'REPOSITORY_URL and GIT_REF must not begin with a dash\n' >&2
    exit 2
fi

case "$agent_mode" in
    read-only)
        readonly agent_tools="read,grep,find,ls"
        ;;
    write)
        readonly agent_tools="read,grep,find,ls,bash,edit,write"
        ;;
    *)
        printf 'AGENT_MODE must be read-only or write\n' >&2
        exit 2
        ;;
esac

if [[ -e "$checkout_dir" ]]; then
    printf 'checkout path already exists: %s\n' "$checkout_dir" >&2
    exit 2
fi

mkdir -p "$checkout_dir" "$result_dir"

if (( ${#PROMPT_B64} % 4 != 0 )) \
    || [[ ! "$PROMPT_B64" =~ ^[A-Za-z0-9+/]*={0,2}$ ]]; then
    printf 'PROMPT_B64 must contain valid base64\n' >&2
    exit 2
fi

if ! decode_prompt > "$decoded_prompt_path"; then
    printf 'PROMPT_B64 must contain valid base64\n' >&2
    exit 2
fi

canonical_prompt="$(base64 < "$decoded_prompt_path" | tr -d '\n')"
readonly canonical_prompt
if [[ "$canonical_prompt" != "$PROMPT_B64" ]]; then
    printf 'PROMPT_B64 must contain canonical base64\n' >&2
    exit 2
fi

prompt="$(< "$decoded_prompt_path")"
readonly prompt
rm -f "$decoded_prompt_path"
if [[ -z "$prompt" ]]; then
    printf 'decoded prompt must not be empty\n' >&2
    exit 2
fi

git -C "$checkout_dir" init --quiet
git -C "$checkout_dir" remote add origin "$REPOSITORY_URL"
git -C "$checkout_dir" fetch --quiet --depth=1 origin "$GIT_REF"
git -C "$checkout_dir" checkout --quiet --detach FETCH_HEAD
readonly base_commit="$(git -C "$checkout_dir" rev-parse HEAD)"

jq -nc \
    --arg type "factory_agent_start" \
    --arg execution "${CLOUD_RUN_EXECUTION:-local}" \
    --arg repository "$REPOSITORY_URL" \
    --arg ref "$GIT_REF" \
    --arg commit "$base_commit" \
    --arg model "$model" \
    --arg mode "$agent_mode" \
    '{type: $type, execution: $execution, repository: $repository, ref: $ref, commit: $commit, model: $model, mode: $mode}'

set +e
(
    cd "$checkout_dir"
    pi \
        --mode json \
        --no-session \
        --no-approve \
        --no-extensions \
        --no-skills \
        --no-prompt-templates \
        --provider openrouter \
        --model "$model" \
        --thinking "$thinking" \
        --tools "$agent_tools" \
        "$prompt"
) | tee "$events_path" | jq --unbuffered -c '
    if .type == "message_end" and .message.role == "assistant" then
        .message.content |= map(select(.type != "thinking"))
    else
        empty
    end
'
readonly agent_exit_code="${PIPESTATUS[0]}"
set -e

git -C "$checkout_dir" add --intent-to-add .
git -C "$checkout_dir" status --short > "$status_path"
git -C "$checkout_dir" diff --binary "$base_commit" > "$patch_path"

readonly cost="$(
    jq -s '
        [
            .[]
            | select(.type == "message_end" and .message.role == "assistant")
            | (.message.usage.cost.total // 0)
        ]
        | add // 0
    ' "$events_path"
)"

jq -nc \
    --arg type "factory_agent_summary" \
    --arg execution "${CLOUD_RUN_EXECUTION:-local}" \
    --arg commit "$base_commit" \
    --arg model "$model" \
    --arg mode "$agent_mode" \
    --arg status "$(cat "$status_path")" \
    --arg patch "$patch_path" \
    --argjson cost "$cost" \
    --argjson exit_code "$agent_exit_code" \
    '{type: $type, execution: $execution, commit: $commit, model: $model, mode: $mode, exit_code: $exit_code, cost_usd: $cost, git_status: $status, patch_path: $patch}'

if [[ -s "$patch_path" ]]; then
    printf '%s\n' '----- BEGIN FACTORY AGENT PATCH -----'
    cat "$patch_path"
    printf '%s\n' '----- END FACTORY AGENT PATCH -----'
fi

exit "$agent_exit_code"
