#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
mkdir -p "$temporary/bin"

cat >"$temporary/bin/go" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$FACTORY_V2_TEST_GO_LOG"
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    output=$1
    break
  fi
  shift
done
if [ -z "$output" ]; then
  echo "fake go did not receive -o" >&2
  exit 1
fi
mkdir -p "$(dirname -- "$output")"
case "$output" in
  *factory-server)
    cat >"$output" <<'SERVER'
#!/bin/sh
exit 0
SERVER
    ;;
  *)
    cat >"$output" <<'WORKER'
#!/bin/sh
exit 0
WORKER
    ;;
esac
chmod +x "$output"
EOF

for command in node npm; do
  cat >"$temporary/bin/$command" <<'EOF'
#!/bin/sh
echo "$0 must not run during the operator build" >&2
exit 99
EOF
done
chmod +x "$temporary/bin/go" "$temporary/bin/node" "$temporary/bin/npm"

mkdir -p "$temporary/home"
(
  unset FACTORY_V2_BUILD_DIR FACTORY_V2_DATA_HOME
  HOME="$temporary/home" \
  FACTORY_V2_TEST_GO_LOG="$temporary/default-go.log" \
  PATH="$temporary/bin:/usr/bin:/bin" \
    "$root/scripts/build-v2.sh"
)

test -x "$temporary/home/.factory/bin/factory-server"
test -x "$temporary/home/.factory/bin/factory-worker"
test "$(wc -l <"$temporary/default-go.log" | tr -d ' ')" = "2"

set +e
output=$(
  unset FACTORY_V2_BUILD_DIR FACTORY_V2_DATA_HOME FACTORY_V2_WORKER_CONFIG
  HOME="$temporary/home-without-config" \
    FACTORY_V2_SKIP_BUILD=1 \
    PATH="$temporary/bin:/usr/bin:/bin" \
    "$root/scripts/run-v2-local.sh" 2>&1
)
status=$?
set -e

if [ "$status" -ne 1 ]; then
  echo "launcher without the default config exited $status, want 1" >&2
  echo "$output" >&2
  exit 1
fi
if ! printf '%s\n' "$output" |
  grep -q "$temporary/home-without-config/.factory/worker.toml"; then
  echo "launcher did not select ~/.factory/worker.toml by default" >&2
  echo "$output" >&2
  exit 1
fi

legacy_checkout="$temporary/legacy-checkout"
mkdir -p "$legacy_checkout/scripts" "$legacy_checkout/.factory-v2/data/server"
cp "$root/scripts/run-v2-local.sh" "$legacy_checkout/scripts/run-v2-local.sh"
: >"$legacy_checkout/.factory-v2/worker.toml"
: >"$legacy_checkout/.factory-v2/data/server/factory.sqlite3"

set +e
output=$(
  unset FACTORY_V2_BUILD_DIR FACTORY_V2_DATA_HOME FACTORY_V2_WORKER_CONFIG
  HOME="$temporary/legacy-home-unused" \
    "$legacy_checkout/scripts/run-v2-local.sh" 2>&1
)
status=$?
set -e

if [ "$status" -ne 1 ]; then
  echo "launcher with legacy state exited $status, want 1" >&2
  echo "$output" >&2
  exit 1
fi
if ! printf '%s\n' "$output" |
  grep -Fq "FACTORY_V2_DATA_HOME=\"$legacy_checkout/.factory-v2/data\""; then
  echo "launcher did not fail closed with the legacy recovery command" >&2
  echo "$output" >&2
  exit 1
fi
if ! printf '%s\n' "$output" |
  grep -Fq "\"$legacy_checkout/scripts/run-v2-local.sh\""; then
  echo "launcher recovery command did not use its absolute script path" >&2
  echo "$output" >&2
  exit 1
fi
if [ -e "$temporary/legacy-home-unused/.factory" ]; then
  echo "launcher created the new Factory home despite legacy state" >&2
  exit 1
fi

rm "$legacy_checkout/.factory-v2/worker.toml"
set +e
output=$(
  unset FACTORY_V2_BUILD_DIR FACTORY_V2_DATA_HOME FACTORY_V2_WORKER_CONFIG
  HOME="$temporary/legacy-home-unused" \
    "$legacy_checkout/scripts/run-v2-local.sh" 2>&1
)
status=$?
set -e
if [ "$status" -ne 1 ] ||
  ! printf '%s\n' "$output" | grep -Fq "matching worker configuration was not found"; then
  echo "launcher did not give safe server-only legacy recovery guidance" >&2
  echo "$output" >&2
  exit 1
fi

FACTORY_V2_BUILD_DIR="$temporary/output" \
FACTORY_V2_TEST_GO_LOG="$temporary/go.log" \
PATH="$temporary/bin:/usr/bin:/bin" \
  "$root/scripts/build-v2.sh"

test -x "$temporary/output/factory-server"
test -x "$temporary/output/factory-worker"
test "$(wc -l <"$temporary/go.log" | tr -d ' ')" = "2"
grep -q 'build -o .*factory-server ./cmd/factory-server' "$temporary/go.log"
grep -q 'build -o .*factory-worker ./cmd/factory-worker' "$temporary/go.log"

rm -rf "$temporary/output"
: >"$temporary/go.log"
: >"$temporary/worker.toml"

set +e
output=$(
  FACTORY_V2_BUILD_DIR="$temporary/output" \
    FACTORY_V2_DATA_HOME="$temporary/data" \
    FACTORY_V2_LISTEN="127.0.0.1:1" \
    FACTORY_V2_TEST_GO_LOG="$temporary/go.log" \
    PATH="$temporary/bin:/usr/bin:/bin" \
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
  grep -q "Factory V2 server exited before becoming ready"; then
  echo "launcher did not reach the controlled server readiness failure" >&2
  echo "$output" >&2
  exit 1
fi
test "$(wc -l <"$temporary/go.log" | tr -d ' ')" = "2"

echo "Factory V2 operator build and default launcher route require only Go."
