#!/bin/sh

set -eu

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: $0 VERSION COMMIT [OUTPUT]" >&2
  exit 2
fi

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$1
commit=$2
output=${3:-dist}
toolchain=$(tr -d '[:space:]' < "$root/.release/go-version")

if [ -z "$toolchain" ]; then
  echo "release Go version is empty" >&2
  exit 1
fi

cd "$root"
GO111MODULE=on GOARCH= GOAMD64=v1 GOARM64=v8.0 GODEBUG= GOENV=off \
  GOEXPERIMENT= GOFIPS140=off GOFLAGS= GOOS= GOWORK=off \
  GOTOOLCHAIN="go$toolchain" \
  exec go run ./cmd/release-artifacts \
  -root "$root" -output "$output" -version "$version" -commit "$commit"
