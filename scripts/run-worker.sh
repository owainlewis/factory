#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
data_home=${FACTORY_DATA_HOME:-${FACTORY_V2_DATA_HOME:-}}
if [ -z "$data_home" ]; then
  if [ -z "${HOME:-}" ]; then
    echo "Factory startup requires HOME or FACTORY_DATA_HOME." >&2
    exit 1
  fi
  legacy_data_home=
  legacy_config=
  if [ -f "$HOME/.factory/worker.toml" ] ||
    [ -f "$HOME/.factory/server/factory.sqlite3" ] ||
    [ -f "$HOME/.factory/server/factory.sqlite3.v2-control-plane" ] ||
    [ -d "$HOME/.factory/workers" ]; then
    data_home="$HOME/.factory"
  elif [ -f "$root/.factory-v2/worker.toml" ] ||
    [ -f "$root/.factory-v2/data/server/factory.sqlite3" ] ||
    [ -f "$root/.factory-v2/data/server/factory.sqlite3.v2-control-plane" ] ||
    [ -d "$root/.factory-v2/data/workers" ]; then
    legacy_data_home="$root/.factory-v2/data"
    legacy_config="$root/.factory-v2/worker.toml"
  elif [ -f "$HOME/.factory-v2/worker.toml" ] ||
    [ -f "$HOME/.factory-v2/server/factory.sqlite3" ] ||
    [ -f "$HOME/.factory-v2/server/factory.sqlite3.v2-control-plane" ] ||
    [ -d "$HOME/.factory-v2/workers" ]; then
    legacy_data_home="$HOME/.factory-v2"
    legacy_config="$HOME/.factory-v2/worker.toml"
  fi
  if [ -n "$legacy_data_home" ]; then
    echo "Factory found preview state and refused to replace it with an empty ~/.factory home." >&2
    if [ -f "$legacy_config" ]; then
      echo "Resolve its work by running:" >&2
      echo "  FACTORY_DATA_HOME=\"$legacy_data_home\" \"$root/scripts/run-worker.sh\" \"$legacy_config\"" >&2
    else
      echo "The matching worker configuration was not found at $legacy_config." >&2
      echo "Set FACTORY_DATA_HOME=$legacy_data_home when reopening factory-server, and restore the matching worker configuration before starting a worker." >&2
    fi
    echo "Archive the old state after its attempts and retained worktrees are resolved." >&2
    exit 1
  fi
  if [ -z "$data_home" ]; then
    data_home="$HOME/.factory"
  fi
fi
build_directory=${FACTORY_BUILD_DIR:-${FACTORY_V2_BUILD_DIR:-"$data_home/bin"}}
config=${1:-${FACTORY_WORKER_CONFIG:-${FACTORY_V2_WORKER_CONFIG:-"$data_home/worker.toml"}}}
worker_ready_seconds=${FACTORY_WORKER_READY_SECONDS:-${FACTORY_V2_WORKER_READY_SECONDS:-40}}
skip_build=${FACTORY_SKIP_BUILD:-${FACTORY_V2_SKIP_BUILD:-0}}

case "$(uname -s)" in
  Darwin|DragonFly|FreeBSD|Linux|NetBSD|OpenBSD|SunOS) ;;
  *)
    echo "Factory local workers are supported only on Unix." >&2
    exit 1
    ;;
esac

if [ ! -f "$config" ]; then
  echo "Factory worker configuration does not exist: $config" >&2
  echo "Copy examples/worker.toml there and set the repository paths first." >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "Factory local startup requires curl on PATH." >&2
  exit 1
fi

# The worker configuration is the only authority for the control plane this
# worker joins. Read its top-level keys, which precede the first table header.
# An absent key takes its default. A key that is present but unreadable is an
# error, because defaulting there would silently join the wrong control plane.
read_config_key() {
  key=$1
  line=$(sed -n "/^[[:space:]]*\[/q; /^[[:space:]]*$key[[:space:]]*=/p" "$config" | head -1)
  if [ -z "$line" ]; then
    return 0
  fi
  value=$(
    printf '%s\n' "$line" | sed -n \
      -e "s/^[^=]*=[[:space:]]*\"\([^\"]*\)\"[[:space:]]*\$/\1/p" \
      -e "s/^[^=]*=[[:space:]]*\"\([^\"]*\)\"[[:space:]]*#.*\$/\1/p" \
      -e "s/^[^=]*=[[:space:]]*'\([^']*\)'[[:space:]]*\$/\1/p" \
      -e "s/^[^=]*=[[:space:]]*'\([^']*\)'[[:space:]]*#.*\$/\1/p" |
      head -1
  )
  if [ -z "$value" ]; then
    echo "Factory could not read the $key value in $config." >&2
    echo "Write it as a single-line quoted TOML string, such as $key = \"value\"." >&2
    return 1
  fi
  printf '%s' "$value"
}

server=$(read_config_key server) || exit 1
if [ -z "$server" ]; then
  server="http://127.0.0.1:7337"
fi
server=${server%/}
ca_certificate=$(read_config_key ca_certificate) || exit 1
case "$ca_certificate" in
  "" | /*) ;;
  *) ca_certificate="$(CDPATH= cd -- "$(dirname -- "$config")" && pwd)/$ca_certificate" ;;
esac

worker_binary="$build_directory/factory-worker"

if [ "$skip_build" != "1" ] && [ ! -x "$worker_binary" ]; then
  if ! command -v just >/dev/null 2>&1; then
    echo "Factory local startup requires just on PATH." >&2
    exit 1
  fi
  FACTORY_BUILD_DIR="$build_directory" \
    just --justfile "$root/Justfile" --working-directory "$root" build-worker
fi

if [ ! -x "$worker_binary" ]; then
  echo "The Factory worker binary is missing. Run just build-worker first." >&2
  exit 1
fi

export FACTORY_DATA_HOME="$data_home"

response_body=$(mktemp)
worker_pid=

curl_control_plane() {
  timeout=$1
  endpoint=$2
  if [ -n "$ca_certificate" ]; then
    curl --silent --show-error --fail --max-time "$timeout" --cacert "$ca_certificate" "$endpoint"
  else
    curl --silent --show-error --fail --max-time "$timeout" "$endpoint"
  fi
}

curl_control_plane_status() {
  timeout=$1
  endpoint=$2
  if [ -n "$ca_certificate" ]; then
    curl --silent --output "$response_body" --write-out '%{http_code}' \
      --max-time "$timeout" --cacert "$ca_certificate" "$endpoint"
  else
    curl --silent --output "$response_body" --write-out '%{http_code}' \
      --max-time "$timeout" "$endpoint"
  fi
}

stop_processes() {
  trap - INT TERM EXIT
  if [ -n "$worker_pid" ] && kill -0 "$worker_pid" 2>/dev/null; then
    kill -TERM "$worker_pid" 2>/dev/null || true
  fi
  if [ -n "$worker_pid" ]; then
    wait "$worker_pid" 2>/dev/null || true
  fi
  rm -f "$response_body"
}

stop_after_signal() {
  stop_processes
  exit 0
}

trap stop_after_signal INT TERM
trap stop_processes EXIT

if ! curl_control_plane 5 "$server/healthz" >/dev/null 2>&1; then
  echo "Factory control plane did not answer $server/healthz." >&2
  echo "Start it with just run, or correct the server key in $config." >&2
  exit 1
fi

echo "Starting Factory worker with $config ..."
worker_id=$("$worker_binary" identity --config "$config")
"$worker_binary" --config "$config" &
worker_pid=$!

registered=0
observable=1
worker_ready_deadline=$(($(date +%s) + worker_ready_seconds))
while [ "$(date +%s)" -lt "$worker_ready_deadline" ]; do
  if ! kill -0 "$worker_pid" 2>/dev/null; then
    wait "$worker_pid" || true
    echo "Factory worker exited during startup. Check the configuration and error above." >&2
    exit 1
  fi
  remaining=$((worker_ready_deadline - $(date +%s)))
  if [ "$remaining" -le 0 ]; then
    break
  fi
  status=$(curl_control_plane_status "$remaining" "$server/api/v1/workers/$worker_id" || echo 000)
  case "$status" in
    200)
      if grep -q "\"health\":\"healthy\",\"online\":true" "$response_body"; then
        registered=1
        break
      fi
      ;;
    401 | 403 | 404)
      # A remote control plane publishes only the worker lifecycle over TLS, so
      # its worker status is unavailable here. Keep the worker running.
      observable=0
      break
      ;;
  esac
  sleep 0.1
done
if [ "$observable" = "0" ]; then
  echo "Factory worker status is not published at $server/api/v1/workers/$worker_id, so its registration was not confirmed."
elif [ "$registered" != "1" ]; then
  echo "Factory worker did not register as healthy within $worker_ready_seconds seconds. Check its health errors above." >&2
  exit 1
fi
if ! kill -0 "$worker_pid" 2>/dev/null; then
  wait "$worker_pid" || true
  echo "Factory worker stopped before startup completed." >&2
  exit 1
fi

echo "Factory worker $worker_id joined $server/"
echo "Press Ctrl-C to stop the worker. The control plane keeps running. State remains in $data_home."

while :; do
  if ! kill -0 "$worker_pid" 2>/dev/null; then
    wait "$worker_pid" || true
    echo "Factory worker stopped unexpectedly." >&2
    exit 1
  fi
  sleep 1
done
