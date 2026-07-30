#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
mkdir -p "$temporary/bin"

cat >"$temporary/bin/factory-server" <<'EOF'
#!/bin/sh
if [ -n "${FACTORY_V2_TEST_SERVER_PID_FILE:-}" ]; then
  echo "$$" >"$FACTORY_V2_TEST_SERVER_PID_FILE"
fi
exec node -e '
  const { existsSync } = require("node:fs");
  const { createServer } = require("node:http");
  const port = Number(process.argv[1].split(":").at(-1));
  createServer((request, response) => {
    response.setHeader("content-type", "application/json");
    if (request.url === "/healthz") {
      response.end("{\"status\":\"ok\"}");
      if (process.env.FACTORY_V2_TEST_SERVER_EXIT_AFTER_HEALTH) {
        response.on("finish", () => setTimeout(() => process.exit(0), 25));
      }
      return;
    }
    if (process.env.FACTORY_V2_TEST_HANG_WORKERS) {
      setTimeout(() => process.exit(0), 3000);
      return;
    }
    const workers = [{id:"existing-healthy-worker",health:"healthy",online:true}];
    if (existsSync(process.env.FACTORY_V2_TEST_WORKER_MARKER)) {
      workers.push({
        id:"new-unhealthy-worker",
        health:process.env.FACTORY_V2_TEST_HEALTHY_WORKER ? "healthy" : "unhealthy",
        online:true
      });
    }
    response.end(JSON.stringify({workers}));
  }).listen(port, "127.0.0.1");
' "$2"
EOF

cat >"$temporary/bin/factory-worker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "identity" ]; then
  echo "new-unhealthy-worker"
  exit 0
fi
if [ -n "${FACTORY_V2_TEST_WORKER_PID_FILE:-}" ]; then
  echo "$$" >"$FACTORY_V2_TEST_WORKER_PID_FILE"
fi
: >"$FACTORY_V2_TEST_WORKER_MARKER"
trap 'exit 0' INT TERM
while :; do sleep 1; done
EOF

chmod +x "$temporary/bin/factory-server" "$temporary/bin/factory-worker"
: >"$temporary/worker.toml"
mkdir "$temporary/v1"
: >"$temporary/v1/factory.sqlite3"
missing_v2_root="$temporary/v1/missing/v2"
port=$(node -e '
  const server = require("node:net").createServer();
  server.listen(0, "127.0.0.1", () => {
    console.log(server.address().port);
    server.close();
  });
')

set +e
output=$(
  FACTORY_V2_BUILD_DIR="$temporary/bin" \
    FACTORY_V2_DATA_HOME="$missing_v2_root" \
    FACTORY_V2_LISTEN="127.0.0.1:$port" \
    FACTORY_V2_SKIP_BUILD=1 \
    FACTORY_V2_TEST_WORKER_MARKER="$temporary/worker-started" \
    FACTORY_V2_WORKER_READY_SECONDS=1 \
    "$root/scripts/run-v2-local.sh" "$temporary/worker.toml" 2>&1
)
status=$?
set -e

if [ "$status" -ne 1 ]; then
  echo "launcher exit status = $status, want 1" >&2
  echo "$output" >&2
  exit 1
fi
if ! printf '%s\n' "$output" |
  grep -q "Factory V2 worker did not register as healthy within 1 seconds"; then
  echo "launcher did not explain unhealthy readiness failure" >&2
  echo "$output" >&2
  exit 1
fi
if printf '%s\n' "$output" | grep -q "Factory V2 is ready"; then
  echo "launcher reported an unhealthy worker as ready" >&2
  echo "$output" >&2
  exit 1
fi
if [ -e "$temporary/v1/missing" ]; then
  echo "launcher created a V2 data root below V1 state before validation" >&2
  exit 1
fi

rm -f "$temporary/worker-started"
port=$(node -e '
  const server = require("node:net").createServer();
  server.listen(0, "127.0.0.1", () => {
    console.log(server.address().port);
    server.close();
  });
')
set +e
output=$(
  FACTORY_V2_BUILD_DIR="$temporary/bin" \
    FACTORY_V2_DATA_HOME="$temporary/data" \
    FACTORY_V2_LISTEN="127.0.0.1:$port" \
    FACTORY_V2_SKIP_BUILD=1 \
    FACTORY_V2_TEST_SERVER_EXIT_AFTER_HEALTH=1 \
    FACTORY_V2_TEST_WORKER_MARKER="$temporary/worker-started" \
    FACTORY_V2_WORKER_READY_SECONDS=2 \
    "$root/scripts/run-v2-local.sh" "$temporary/worker.toml" 2>&1
)
status=$?
set -e
if [ "$status" -ne 1 ] ||
  ! printf '%s\n' "$output" | grep -q "Factory V2 server stopped during worker startup"; then
  echo "launcher did not report its server exiting during worker startup" >&2
  echo "$output" >&2
  exit 1
fi

rm -f "$temporary/worker-started"
port=$(node -e '
  const server = require("node:net").createServer();
  server.listen(0, "127.0.0.1", () => {
    console.log(server.address().port);
    server.close();
  });
')
set +e
output=$(
  FACTORY_V2_BUILD_DIR="$temporary/bin" \
    FACTORY_V2_DATA_HOME="$temporary/data" \
    FACTORY_V2_LISTEN="127.0.0.1:$port" \
    FACTORY_V2_SKIP_BUILD=1 \
    FACTORY_V2_TEST_HANG_WORKERS=1 \
    FACTORY_V2_TEST_WORKER_MARKER="$temporary/worker-started" \
    FACTORY_V2_WORKER_READY_SECONDS=1 \
    "$root/scripts/run-v2-local.sh" "$temporary/worker.toml" 2>&1
)
status=$?
set -e
if [ "$status" -ne 1 ] ||
  ! printf '%s\n' "$output" |
    grep -q "Factory V2 worker did not register as healthy within 1 seconds"; then
  echo "launcher did not bound a stalled readiness response" >&2
  echo "$output" >&2
  exit 1
fi

node - "$root" "$temporary" "$temporary/worker.toml" <<'EOF'
const { existsSync, readFileSync, rmSync } = require("node:fs");
const { createServer } = require("node:net");
const { spawn } = require("node:child_process");
const { join } = require("node:path");

function availablePort() {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const port = server.address().port;
      server.close((error) => error ? reject(error) : resolve(port));
    });
  });
}

function pidFrom(path) {
  if (!existsSync(path)) return null;
  return Number(readFileSync(path, "utf8").trim());
}

function isAlive(pid) {
  if (!pid) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error.code === "ESRCH") return false;
    throw error;
  }
}

function terminateFrom(path) {
  const pid = pidFrom(path);
  if (isAlive(pid)) process.kill(pid, "SIGTERM");
}

async function verifySignal(signal, suffix) {
  const root = process.argv[2];
  const temporary = process.argv[3];
  const config = process.argv[4];
  const marker = join(temporary, `worker-started-${suffix}`);
  const serverPidFile = join(temporary, `server-${suffix}.pid`);
  const workerPidFile = join(temporary, `worker-${suffix}.pid`);
  rmSync(marker, { force: true });
  const port = await availablePort();
  let output = "";
  let signalled = false;
  let timedOut = false;

  const launcher = spawn(join(root, "scripts/run-v2-local.sh"), [config], {
    env: {
      ...process.env,
      FACTORY_V2_BUILD_DIR: join(temporary, "bin"),
      FACTORY_V2_DATA_HOME: join(temporary, "data"),
      FACTORY_V2_LISTEN: `127.0.0.1:${port}`,
      FACTORY_V2_SKIP_BUILD: "1",
      FACTORY_V2_TEST_HEALTHY_WORKER: "1",
      FACTORY_V2_TEST_SERVER_PID_FILE: serverPidFile,
      FACTORY_V2_TEST_WORKER_MARKER: marker,
      FACTORY_V2_TEST_WORKER_PID_FILE: workerPidFile,
      FACTORY_V2_WORKER_READY_SECONDS: "2"
    },
    stdio: ["ignore", "pipe", "pipe"]
  });

  const capture = (chunk) => {
    output += chunk;
    if (!signalled && output.includes("Factory V2 is ready")) {
      signalled = true;
      launcher.kill(signal);
    }
  };
  launcher.stdout.on("data", capture);
  launcher.stderr.on("data", capture);

  const timeout = setTimeout(() => {
    timedOut = true;
    launcher.kill("SIGKILL");
    terminateFrom(workerPidFile);
    terminateFrom(serverPidFile);
  }, 5000);

  const result = await new Promise((resolve, reject) => {
    launcher.once("error", reject);
    launcher.once("close", (code, exitSignal) => resolve({ code, exitSignal }));
  });
  clearTimeout(timeout);

  const serverPid = pidFrom(serverPidFile);
  const workerPid = pidFrom(workerPidFile);
  const leakedServer = isAlive(serverPid);
  const leakedWorker = isAlive(workerPid);
  if (leakedWorker) process.kill(workerPid, "SIGTERM");
  if (leakedServer) process.kill(serverPid, "SIGTERM");

  if (timedOut) throw new Error(`${signal} shutdown exceeded 5 seconds\n${output}`);
  if (!signalled) throw new Error(`launcher never became ready for ${signal}\n${output}`);
  if (result.code !== 0 || result.exitSignal !== null) {
    throw new Error(`${signal} shutdown returned code=${result.code} signal=${result.exitSignal}\n${output}`);
  }
  if (output.includes("stopped unexpectedly")) {
    throw new Error(`${signal} shutdown was reported as unexpected\n${output}`);
  }
  if (leakedServer || leakedWorker) {
    throw new Error(`${signal} leaked server=${leakedServer} worker=${leakedWorker}\n${output}`);
  }
}

(async () => {
  await verifySignal("SIGTERM", "term");
  await verifySignal("SIGINT", "int");
})().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
EOF

echo "Factory V2 launcher rejects unhealthy workers, V1 writes, server loss, and stalled readiness responses; signals stop cleanly."
