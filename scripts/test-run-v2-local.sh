#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
mkdir -p "$temporary/bin"

cat >"$temporary/bin/factory-server" <<'EOF'
#!/bin/sh
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
      workers.push({id:"new-unhealthy-worker",health:"unhealthy",online:true});
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
    FACTORY_V2_WORKER_READY_SECONDS=1 \
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

echo "Factory V2 launcher rejects unhealthy workers, V1 writes, server loss, and stalled readiness responses."
