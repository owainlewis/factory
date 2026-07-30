#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
build_directory=${FACTORY_V2_BUILD_DIR:-"$root/.factory-v2/bin"}
listen=${FACTORY_V2_LISTEN:-127.0.0.1:7337}
data_home=${FACTORY_V2_DATA_HOME:-"$root/.factory-v2/data"}
config=${1:-${FACTORY_V2_WORKER_CONFIG:-}}
worker_ready_seconds=${FACTORY_V2_WORKER_READY_SECONDS:-40}

if [ -z "$config" ]; then
  echo "Usage: ./scripts/run-v2-local.sh /path/to/worker.toml" >&2
  echo "Copy examples/v2-worker.toml and set both repository paths first." >&2
  exit 2
fi

case "$(uname -s)" in
  Darwin|DragonFly|FreeBSD|Linux|NetBSD|OpenBSD|SunOS) ;;
  *)
    echo "Factory V2 local workers are supported only on Unix." >&2
    exit 1
    ;;
esac

if [ ! -f "$config" ]; then
  echo "Factory V2 worker configuration does not exist: $config" >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "Factory V2 local startup requires curl on PATH." >&2
  exit 1
fi

if [ "${FACTORY_V2_SKIP_BUILD:-0}" != "1" ]; then
  "$root/scripts/build-v2.sh"
fi

server_binary="$build_directory/factory-server"
worker_binary="$build_directory/factory-worker"
if [ ! -x "$server_binary" ] || [ ! -x "$worker_binary" ]; then
  echo "Factory V2 binaries are missing. Run ./scripts/build-v2.sh first." >&2
  exit 1
fi

export FACTORY_V2_DATA_HOME="$data_home"

server_pid=
worker_pid=

curl_before_deadline() {
  endpoint=$1
  deadline=$2
  now=$(date +%s)
  remaining=$((deadline - now))
  if [ "$remaining" -le 0 ]; then
    return 1
  fi
  curl --silent --show-error --fail --max-time "$remaining" "$endpoint"
}

stop_processes() {
  trap - INT TERM EXIT
  if [ -n "$worker_pid" ] && kill -0 "$worker_pid" 2>/dev/null; then
    kill -TERM "$worker_pid" 2>/dev/null || true
  fi
  if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null; then
    kill -TERM "$server_pid" 2>/dev/null || true
  fi
  if [ -n "$worker_pid" ]; then
    wait "$worker_pid" 2>/dev/null || true
  fi
  if [ -n "$server_pid" ]; then
    wait "$server_pid" 2>/dev/null || true
  fi
}

stop_after_signal() {
  stop_processes
  exit 0
}

trap stop_after_signal INT TERM
trap stop_processes EXIT

echo "Starting Factory V2 server on http://$listen/ ..."
"$server_binary" -listen "$listen" &
server_pid=$!

ready=0
server_ready_seconds=10
server_ready_deadline=$(($(date +%s) + server_ready_seconds))
while [ "$(date +%s)" -lt "$server_ready_deadline" ]; do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    wait "$server_pid" || true
    echo "Factory V2 server exited before becoming ready. Check the error above." >&2
    exit 1
  fi
  if curl_before_deadline "http://$listen/healthz" "$server_ready_deadline" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.1
done
if [ "$ready" != "1" ]; then
  echo "Factory V2 server did not become healthy within $server_ready_seconds seconds." >&2
  exit 1
fi

echo "Starting Factory V2 worker with $config ..."
worker_id=$("$worker_binary" identity --config "$config")
"$worker_binary" --config "$config" &
worker_pid=$!

registered=0
worker_ready_deadline=$(($(date +%s) + worker_ready_seconds))
while [ "$(date +%s)" -lt "$worker_ready_deadline" ]; do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    wait "$server_pid" || true
    echo "Factory V2 server stopped during worker startup." >&2
    exit 1
  fi
  if ! kill -0 "$worker_pid" 2>/dev/null; then
    wait "$worker_pid" || true
    echo "Factory V2 worker exited during startup. Check the configuration and error above." >&2
    exit 1
  fi
  if curl_before_deadline "http://$listen/api/v1/workers" "$worker_ready_deadline" |
    grep -q "\"id\":\"$worker_id\"[^}]*\"health\":\"healthy\",\"online\":true"; then
    registered=1
    break
  fi
  sleep 0.1
done
if [ "$registered" != "1" ]; then
  echo "Factory V2 worker did not register as healthy within $worker_ready_seconds seconds. Check its health errors above." >&2
  exit 1
fi
if ! kill -0 "$server_pid" 2>/dev/null; then
  wait "$server_pid" || true
  echo "Factory V2 server stopped before startup completed." >&2
  exit 1
fi
if ! kill -0 "$worker_pid" 2>/dev/null; then
  wait "$worker_pid" || true
  echo "Factory V2 worker stopped before startup completed." >&2
  exit 1
fi

echo "Factory V2 is ready at http://$listen/"
echo "Press Ctrl-C to stop the server and worker. State remains in $data_home."

while :; do
  if ! kill -0 "$server_pid" 2>/dev/null; then
    wait "$server_pid" || true
    echo "Factory V2 server stopped unexpectedly." >&2
    exit 1
  fi
  if ! kill -0 "$worker_pid" 2>/dev/null; then
    wait "$worker_pid" || true
    echo "Factory V2 worker stopped unexpectedly." >&2
    exit 1
  fi
  sleep 1
done
