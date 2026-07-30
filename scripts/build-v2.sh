#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
data_home=${FACTORY_V2_DATA_HOME:-}
if [ -z "$data_home" ]; then
  if [ -z "${HOME:-}" ]; then
    echo "Factory V2 build requires HOME or FACTORY_V2_DATA_HOME." >&2
    exit 1
  fi
  data_home="$HOME/.factory"
fi
build_directory=${FACTORY_V2_BUILD_DIR:-"$data_home/bin"}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Factory V2 build requires $1 on PATH." >&2
    exit 1
  fi
}

require_command go

mkdir -p "$build_directory"

echo "Building Factory V2 Go binaries from committed embedded UI assets..."
(cd "$root" && go build -o "$build_directory/factory-server" ./cmd/factory-server)
(cd "$root" && go build -o "$build_directory/factory-worker" ./cmd/factory-worker)

echo "Factory V2 build complete:"
echo "  $build_directory/factory-server"
echo "  $build_directory/factory-worker"
