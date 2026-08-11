#!/usr/bin/env bash

set -Eeuo pipefail

readonly project_id="${PROJECT_ID:?PROJECT_ID is required}"
readonly prompt_path="${1:?usage: execute.sh PROMPT_FILE}"
readonly region="${REGION:-europe-west1}"
readonly job_name="${JOB_NAME:-factory-agent-experiment}"
readonly repository_url="${REPOSITORY_URL:-https://github.com/owainlewis/factory.git}"
readonly git_ref="${GIT_REF:-main}"
readonly agent_mode="${AGENT_MODE:-read-only}"
readonly wait_seconds="${WAIT_SECONDS:-600}"

if [[ ! -f "$prompt_path" ]]; then
    printf 'prompt file does not exist: %s\n' "$prompt_path" >&2
    exit 2
fi

readonly prompt_base64="$(base64 < "$prompt_path" | tr -d '\n')"
if [[ -z "$prompt_base64" ]]; then
    printf 'prompt file must not be empty\n' >&2
    exit 2
fi

case "$agent_mode" in
    read-only | write) ;;
    *)
        printf 'AGENT_MODE must be read-only or write\n' >&2
        exit 2
        ;;
esac

if [[ ! "$wait_seconds" =~ ^[1-9][0-9]*$ ]]; then
    printf 'WAIT_SECONDS must be a positive integer\n' >&2
    exit 2
fi

readonly request_body="$(
    jq -nc \
        --arg repository_url "$repository_url" \
        --arg git_ref "$git_ref" \
        --arg prompt_base64 "$prompt_base64" \
        --arg agent_mode "$agent_mode" \
        '{
            overrides: {
                containerOverrides: [{
                    env: [
                        {name: "REPOSITORY_URL", value: $repository_url},
                        {name: "GIT_REF", value: $git_ref},
                        {name: "PROMPT_B64", value: $prompt_base64},
                        {name: "AGENT_MODE", value: $agent_mode}
                    ]
                }],
                taskCount: 1,
                timeout: "600s"
            }
        }'
)"
readonly access_token="$(gcloud auth print-access-token)"
readonly run_url="https://run.googleapis.com/v2/projects/${project_id}/locations/${region}/jobs/${job_name}:run"
readonly run_response="$(
    curl --fail-with-body --silent --show-error \
        --request POST \
        --header "Authorization: Bearer ${access_token}" \
        --header 'Content-Type: application/json' \
        --data "$request_body" \
        "$run_url"
)"
readonly execution_name="$(jq -er '.metadata.name' <<< "$run_response")"
readonly execution_id="${execution_name##*/}"
readonly execution_url="https://run.googleapis.com/v2/${execution_name}"

printf 'Execution started: %s\n' "$execution_id"

readonly poll_count="$((wait_seconds / 5 + 1))"
for _poll_index in $(seq 1 "$poll_count"); do
    execution_response="$(
        curl --fail-with-body --silent --show-error \
            --header "Authorization: Bearer ${access_token}" \
            "$execution_url"
    )"
    completion_state="$(
        jq -r '
            first(.conditions[]? | select(.type == "Completed") | .state)
            // "CONDITION_PENDING"
        ' <<< "$execution_response"
    )"
    case "$completion_state" in
        CONDITION_SUCCEEDED)
            jq -nc \
                --arg execution "$execution_id" \
                --arg log_uri "$(jq -r '.logUri // ""' <<< "$execution_response")" \
                '{execution: $execution, state: "succeeded", log_uri: $log_uri}'
            exit 0
            ;;
        CONDITION_FAILED)
            jq -c '{execution: .name, conditions: .conditions}' <<< "$execution_response" >&2
            exit 1
            ;;
    esac
    sleep 5
done

printf 'Execution did not finish within %s seconds: %s\n' "$wait_seconds" "$execution_id" >&2
exit 1
