#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
web_directory="$root/web"
build_directory=${FACTORY_V2_BUILD_DIR:-"$root/.factory-v2/bin"}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Factory V2 build requires $1 on PATH." >&2
    exit 1
  fi
}

require_command go
require_command node
require_command npm

mkdir -p "$build_directory"

if [ "${FACTORY_V2_SKIP_INSTALL:-0}" != "1" ]; then
  echo "Installing pinned UI dependencies..."
  (cd "$web_directory" && npm ci)
fi

echo "Building the embedded Factory V2 UI..."
(cd "$web_directory" && npm run build)

echo "Building Factory V2 Go binaries..."
(cd "$root" && go build -o "$build_directory/factory-server" ./cmd/factory-server)
(cd "$root" && go build -o "$build_directory/factory-worker" ./cmd/factory-worker)

echo "Factory V2 build complete:"
echo "  $build_directory/factory-server"
echo "  $build_directory/factory-worker"
