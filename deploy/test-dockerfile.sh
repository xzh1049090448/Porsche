#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
dockerfile="$script_dir/../Dockerfile"

test -f "$dockerfile"
if grep -Eq '^COPY[[:space:]]+config[[:space:]]+\.?/?config/?$' "$dockerfile"; then
  echo "Dockerfile must not copy the removed config directory" >&2
  exit 1
fi

if ! grep -Eq '^[[:space:]]*&& CGO_ENABLED=0 GOOS=linux go build -o /out/check-config \./cmd/check-config$' "$dockerfile"; then
  echo "Dockerfile must build /out/check-config from ./cmd/check-config" >&2
  exit 1
fi

if ! grep -Fxq 'COPY --from=builder /out/server /out/bootstrap-root /out/check-config ./' "$dockerfile"; then
  echo "Dockerfile must copy check-config beside the production binaries" >&2
  exit 1
fi
