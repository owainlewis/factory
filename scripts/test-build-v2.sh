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
