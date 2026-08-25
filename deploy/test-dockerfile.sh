#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
dockerfile="$script_dir/../Dockerfile"

test -f "$dockerfile"
if grep -Eq '^COPY[[:space:]]+config[[:space:]]+\.?/?config/?$' "$dockerfile"; then
  echo "Dockerfile must not copy the removed config directory" >&2
  exit 1
fi
