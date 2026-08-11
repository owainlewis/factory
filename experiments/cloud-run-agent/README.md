# Cloud Run agent experiment

This experiment proves that one Cloud Run Job can check out a Git repository,
run Pi with DeepSeek V4 Flash through OpenRouter from that checkout, report JSON
events and cost, and exit. It is intentionally separate from Factory's durable
Worker protocol.

The experiment trusts the selected repository and prompt. Pi can execute code
with the same environment and credentials as the container. Use `read-only`
mode unless a write-capable run is required.

Cloud Logging receives sanitized assistant events and the final summary. Pi's
raw JSON stream, including reasoning events, exists only on the Job's ephemeral
filesystem and disappears with the container. The base64 prompt is not secret:
it is visible in Cloud Run execution metadata to users who can inspect the Job.

## Files

- `Dockerfile` builds a pinned Node and Pi image.
- `run-agent.sh` checks out the requested Git ref and runs Pi.
- `deploy.sh` creates or updates the Artifact Registry repository, runtime
  service account, image, and Cloud Run Job.
- `execute.sh` starts one Job execution from a prompt file.
- `test-run-agent.sh` verifies checkout, invocation, patch capture, cost capture,
  failure propagation, and mode validation without using an API key.

## Local checks

Run the shell and behavior checks:

```sh
bash -n experiments/cloud-run-agent/*.sh
experiments/cloud-run-agent/test-run-agent.sh
```

With a Docker daemon running, build and run the real image:

```sh
docker build --platform linux/amd64 \
  --tag factory-agent-experiment \
  experiments/cloud-run-agent

PROMPT_B64="$(base64 < experiments/cloud-run-agent/smoke-prompt.txt | tr -d '\n')"

docker run --rm \
  --env OPENROUTER_API_KEY \
  --env REPOSITORY_URL=https://github.com/owainlewis/factory.git \
  --env GIT_REF=main \
  --env PROMPT_B64="$PROMPT_B64" \
  --env AGENT_MODE=read-only \
  factory-agent-experiment
```

## Google Cloud setup

The deploy script enables the required APIs, grants the default Cloud Build
service account the standard Cloud Build builder role, and creates the
non-secret resources. Create the OpenRouter secret once without placing the
value in shell history:

```sh
export PROJECT_ID=factory-505220
printf '%s' "$OPENROUTER_API_KEY" | \
  gcloud secrets create openrouter-api-key \
    --data-file=- \
    --replication-policy=automatic \
    --project "$PROJECT_ID"
```

If the secret already exists, add a new version instead:

```sh
printf '%s' "$OPENROUTER_API_KEY" | \
  gcloud secrets versions add openrouter-api-key \
    --data-file=- \
    --project "$PROJECT_ID"
```

Build the image and create or update the Job:

```sh
PROJECT_ID=factory-505220 \
REGION=europe-west1 \
  experiments/cloud-run-agent/deploy.sh
```

By default, deploys require a clean experiment directory and tag the image with
the containing Git commit. For an intentional development build, set a unique
`IMAGE_TAG`. The Job itself is updated with the resolved image digest.

Execute the read-only smoke test:

```sh
PROJECT_ID=factory-505220 \
REGION=europe-west1 \
  experiments/cloud-run-agent/execute.sh \
  experiments/cloud-run-agent/smoke-prompt.txt
```

`execute.sh` calls the Cloud Run v2 API directly and uses `gcloud` only for the
current access token. This avoids client-version-specific execution overrides.
It waits for the execution and prints its final state and Cloud Logging URL.

The first live read-only run against Factory completed in 1 minute 40 seconds
with zero retries. OpenRouter reported a model cost of `$0.013663` for that run.

Use `AGENT_MODE=write` to enable Pi's Bash and file mutation tools. The runner
prints a binary Git patch after Pi exits. The patch is diagnostic output only;
the experiment does not upload it or push a branch.

## Inputs

| Name | Default | Meaning |
| --- | --- | --- |
| `REPOSITORY_URL` | none | Git remote fetched by the Job. |
| `GIT_REF` | none | Branch, tag, or reachable commit to fetch. |
| `PROMPT_B64` | none | Base64-encoded task prompt. |
| `AGENT_MODE` | `read-only` | `read-only` or `write`. |
| `MODEL` | `deepseek/deepseek-v4-flash` | OpenRouter model ID. |
| `THINKING` | `low` | Pi thinking level. |
| `OPENROUTER_API_KEY` | none | Secret used by Pi's OpenRouter provider. |
| `WORKSPACE_ROOT` | `/workspace` | Checkout and result root. |

## Deliberate limits

- One repository and one agent process per Job execution.
- Public HTTPS repositories are the supported deployment path.
- No Factory task, lease, event, or cancellation integration.
- No automatic retries.
- No branch push, pull request, or durable patch storage.
- No Codex or Claude Code installation.
- No private-repository authentication or secret-prompt transport.
