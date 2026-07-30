#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
web_directory="$root/web"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Factory UI build requires $1 on PATH." >&2
    exit 1
  fi
}

require_command node
require_command npm

skip_install=${FACTORY_SKIP_INSTALL:-${FACTORY_V2_SKIP_INSTALL:-0}}
if [ "$skip_install" != "1" ]; then
  echo "Installing pinned UI dependencies..."
  (cd "$web_directory" && npm ci)
fi

echo "Rebuilding the embedded Factory UI assets..."
(cd "$web_directory" && npm run build)

echo "Factory UI assets are current in $web_directory/dist."
