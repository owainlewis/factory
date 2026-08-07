import {
  chmod,
  mkdir,
  mkdtemp,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, join, resolve } from "node:path";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";

const root = resolve(import.meta.dirname, "../..");
const temporary = await mkdtemp(join(tmpdir(), "factory-ui-e2e-"));
const serverBinary = join(temporary, "factory-server");
const workerBinary = join(temporary, "factory-worker");
const database = join(temporary, "server", "factory.sqlite3");
const workerData = join(temporary, "worker");
const workerConfig = join(temporary, "worker.toml");
const fakeBin = join(temporary, "bin");
const legacyRoot = resolve(import.meta.dirname, "../test-results/legacy-poller");
const legacyConfig = join(legacyRoot, "poller.toml");
const legacyLedger = join(legacyRoot, "poller", "poller.sqlite3");
const workerID = "11111111-1111-4111-8111-111111111111";

function run(command, args, options = {}) {
  return new Promise((resolveRun, rejectRun) => {
    const child = spawn(command, args, {
      cwd: options.cwd ?? root,
      env: options.env ?? process.env,
      stdio: options.stdio ?? "inherit",
    });
    child.once("error", rejectRun);
    child.once("exit", (code, signal) => {
      if (code === 0) resolveRun();
      else rejectRun(new Error(`${command} exited with ${code ?? signal}`));
    });
  });
}

async function createRepository(name) {
  const origin = join(temporary, `${name}-origin.git`);
  const checkout = join(temporary, name);
  await run("git", ["init", "--bare", "--initial-branch=main", origin]);
  await run("git", ["clone", origin, checkout]);
  await run("git", ["config", "user.name", "Factory browser test"], { cwd: checkout });
  await run("git", ["config", "user.email", "factory-browser@example.test"], { cwd: checkout });
  await writeFile(join(checkout, "README.md"), `# ${name}\n`);
  await run("git", ["add", "README.md"], { cwd: checkout });
  await run("git", ["commit", "-m", "test: initialize repository"], { cwd: checkout });
  await run("git", ["push", "--set-upstream", "origin", "main"], { cwd: checkout });
  return checkout;
}

async function createFakeCodex() {
  await mkdir(fakeBin, { recursive: true });
  const executable = join(fakeBin, "codex");
  await writeFile(
    executable,
    `#!/bin/sh
set -eu

if [ "\${1:-}" = "--version" ]; then
  echo "codex-cli 0.0.0-factory-e2e"
  exit 0
fi

if [ "\${1:-}" = "login" ] && [ "\${2:-}" = "status" ]; then
  echo "Logged in for deterministic Factory browser tests"
  exit 0
fi

if [ "\${1:-}" != "exec" ]; then
  echo "unexpected fake Codex arguments: $*" >&2
  exit 2
fi

result_path=
previous=
for argument in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then
    result_path=$argument
    break
  fi
  previous=$argument
done
if [ -z "$result_path" ]; then
  echo "fake Codex did not receive --output-last-message" >&2
  exit 2
fi

prompt=$(cat)
branch=$(git branch --show-current)
printf '%s\\n' '{"type":"progress","message":"Inspected the assigned repository."}'

case "$prompt" in
  *FACTORY_E2E_FAIL*)
    echo "Deterministic fake Codex failure." >&2
    exit 42
    ;;
  *FACTORY_E2E_WAIT*)
    printf '%s\\n' '{"type":"progress","message":"Waiting for operator cancellation."}'
    trap 'exit 143' TERM INT
    while :; do sleep 1; done
    ;;
esac

printf '%s\\n' "Created by the Factory browser proof." > factory-proof.txt
printf '%s\\n' '{"type":"progress","message":"Created deterministic worktree evidence."}'
{
  printf '%s\\n' "Completed by deterministic fake Codex."
  printf 'Branch: %s\\n' "$branch"
  printf 'Worktree: %s\\n' "$PWD"
} > "$result_path"
`,
  );
  await chmod(executable, 0o755);
}

async function createFakeGH() {
  await mkdir(fakeBin, { recursive: true });
  const executable = join(fakeBin, "gh");
  await writeFile(
    executable,
    `#!/bin/sh
set -eu

if [ "\${1:-}" = "--version" ]; then
  echo "gh version 2.94.0 (Factory browser fixture)"
  exit 0
fi

if [ "\${1:-}" = "auth" ] && [ "\${2:-}" = "status" ]; then
  echo "Logged in to github.com for deterministic Factory browser tests"
  exit 0
fi

if [ "\${1:-}" = "issue" ] && [ "\${2:-}" = "list" ]; then
  printf '%s\n' '[{"number":184,"title":"Typed Automation browser fixture","url":"https://github.com/example/automation-fixture/issues/184","state":"OPEN","labels":[{"id":"label-ready","name":"needs-agent","description":"","color":"ffffff"}]}]'
  exit 0
fi

if [ "\${1:-}" = "pr" ] && [ "\${2:-}" = "list" ]; then
  printf '%s\n' '[{"number":185,"title":"Typed pull-request Automation browser fixture","url":"https://github.com/example/automation-fixture/pull/185","state":"OPEN","isDraft":false,"baseRefName":"main","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","labels":[{"id":"label-review","name":"needs-agent","description":"","color":"ffffff"}]}]'
  exit 0
fi

echo "unexpected fake gh arguments: $*" >&2
exit 2
`,
  );
  await chmod(executable, 0o755);
}

async function createLegacyPollerFixture() {
  await rm(legacyRoot, { recursive: true, force: true });
  await mkdir(join(legacyRoot, "poller"), { recursive: true });
  await writeFile(
    legacyConfig,
    `server = "http://127.0.0.1:17437"
poll_every = "30s"
data_directory = "poller"

[[queues]]
name = "browser-ready"
source = "github"
project = "example/automation-fixture"
status = "open"
labels = ["needs-agent"]
prompt = "Implement the issue and open a reviewed pull request."
timeout_seconds = 3600
`,
    { mode: 0o600 },
  );
  const queueID = createHash("sha256")
    .update(["browser-ready", "github", "example/automation-fixture"].join("\0"))
    .digest("hex")
    .slice(0, 48);
  const requestBody = JSON.stringify({
    request_key: "legacy-browser-request-187",
    title: "Work on github ticket #187",
    description: "Resume the exact imported browser migration request.",
    timeout_seconds: 3600,
    route: {
      repository_remote_identity: "github.com/example/automation-fixture",
      source_access: { provider: "github", hostname: "github.com" },
    },
  });
  const helper = join(temporary, "create-legacy-ledger.go");
  await writeFile(helper, `package main

import (
  "database/sql"
  "os"
  _ "modernc.org/sqlite"
)

func main() {
  database, err := sql.Open("sqlite", os.Args[1])
  if err != nil { panic(err) }
  defer database.Close()
  _, err = database.Exec(\`CREATE TABLE observations (
    queue_id TEXT NOT NULL,
    issue_key TEXT NOT NULL,
    request_key TEXT NOT NULL UNIQUE,
    request_json BLOB NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('pending', 'submitted')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (queue_id, issue_key)
  );
  CREATE INDEX observations_pending ON observations(state, created_at, queue_id, issue_key);\`)
  if err != nil { panic(err) }
  _, err = database.Exec(\`INSERT INTO observations(
    queue_id, issue_key, request_key, request_json, task_id, state, created_at, updated_at
  ) VALUES (?, '#187', ?, ?, '', 'pending', 1, 1)\`, os.Args[2], os.Args[3], []byte(os.Args[4]))
  if err != nil { panic(err) }
}
`);
  await run("go", ["run", helper, legacyLedger, queueID, "legacy-browser-request-187", requestBody]);
}

await Promise.all([
  run("go", ["build", "-o", serverBinary, "./cmd/factory-server"]),
  run("go", ["build", "-o", workerBinary, "./cmd/factory-worker"]),
  createFakeCodex(),
  createFakeGH(),
  createLegacyPollerFixture(),
]);
const [factoryRepository, handbookRepository] = await Promise.all([
  createRepository("factory-demo"),
  createRepository("handbook-demo"),
]);

await mkdir(workerData, { recursive: true });
await writeFile(join(workerData, "worker-id"), `${workerID}\n`, { mode: 0o600 });
await writeFile(
  workerConfig,
  `server = "http://127.0.0.1:17437"
name = "Real local worker"
max_concurrent = 1
data_directory = ${JSON.stringify(workerData)}

[repositories.factory-demo]
path = ${JSON.stringify(factoryRepository)}

[repositories.handbook-demo]
path = ${JSON.stringify(handbookRepository)}
`,
);

const server = spawn(
  serverBinary,
  ["-listen", "127.0.0.1:17437", "-database", database],
  {
    cwd: root,
    env: {
      ...process.env,
      HOME: temporary,
      FACTORY_DATA_HOME: temporary,
      PATH: `${fakeBin}${delimiter}${process.env.PATH ?? ""}`,
    },
    stdio: "inherit",
  },
);
const worker = spawn(
  workerBinary,
  ["--config", workerConfig],
  {
    cwd: root,
    env: {
      ...process.env,
      HOME: temporary,
      FACTORY_DATA_HOME: temporary,
      PATH: `${fakeBin}${delimiter}${process.env.PATH ?? ""}`,
    },
    stdio: "inherit",
  },
);

let stopping = false;
async function stopChild(child, signal) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  await new Promise((resolveStop) => {
    child.once("exit", resolveStop);
    if (!child.kill(signal)) resolveStop();
  });
}

async function stop(signal = "SIGTERM", exitCode = 0) {
  if (stopping) return;
  stopping = true;
  await stopChild(worker, signal);
  await stopChild(server, signal);
  await rm(temporary, { recursive: true, force: true });
  await rm(legacyRoot, { recursive: true, force: true });
  process.exit(exitCode);
}

process.on("SIGINT", () => void stop("SIGINT"));
process.on("SIGTERM", () => void stop("SIGTERM"));
server.once("exit", (code) => {
  if (!stopping) void stop("SIGTERM", code ?? 1);
});
worker.once("exit", (code) => {
  if (!stopping) void stop("SIGTERM", code ?? 1);
});
