#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
dockerfile="$script_dir/../Dockerfile"

validate_dockerfile() {
  local candidate="$1"
  local final_stage
  local copy_line

  test -f "$candidate" || return 1
  if grep -Eq '^COPY[[:space:]]+config[[:space:]]+\.?/?config/?$' "$candidate"; then
    echo "Dockerfile must not copy the removed config directory" >&2
    return 1
  fi
  if ! grep -Eq '^[[:space:]]*&& CGO_ENABLED=0 GOOS=linux go build -o /out/check-config \./cmd/check-config$' "$candidate"; then
    echo "Dockerfile must build /out/check-config from ./cmd/check-config" >&2
    return 1
  fi

  final_stage="$(awk '
    /^[[:space:]]*[Ff][Rr][Oo][Mm][[:space:]]/ { stage = "" }
    { stage = stage $0 ORS }
    END { printf "%s", stage }
  ' "$candidate")"
  copy_line='COPY --from=builder /out/check-config /app/check-config'
  if [[ "$(grep -Fxc "$copy_line" <<<"$final_stage")" != 1 ]]; then
    echo "final Docker stage must copy check-config exactly to /app/check-config" >&2
    return 1
  fi
  if ! awk -v copy="$copy_line" '
    $0 == copy { found = 1; next }
    found && toupper($1) ~ /^(ADD|COPY|RUN|VOLUME|WORKDIR)$/ { invalid = 1 }
    END { exit found && !invalid ? 0 : 1 }
  ' <<<"$final_stage"; then
    echo "final Docker stage must not overwrite, remove, or alter check-config after its copy" >&2
    return 1
  fi
}

validate_dockerfile "$dockerfile"

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/test-dockerfile.XXXXXX")"
trap 'rm -rf "$fixture_dir"' EXIT

cp "$dockerfile" "$fixture_dir/extra-final-stage.Dockerfile"
printf '\nFROM alpine:3.20\nCMD ["true"]\n' >>"$fixture_dir/extra-final-stage.Dockerfile"
if validate_dockerfile "$fixture_dir/extra-final-stage.Dockerfile" >/dev/null 2>&1; then
  echo "Dockerfile validation accepted an appended final stage without check-config" >&2
  exit 1
fi

cp "$dockerfile" "$fixture_dir/permission-damage.Dockerfile"
printf '\nRUN chmod 000 /app/check-config\n' >>"$fixture_dir/permission-damage.Dockerfile"
if validate_dockerfile "$fixture_dir/permission-damage.Dockerfile" >/dev/null 2>&1; then
  echo "Dockerfile validation accepted a later check-config permission change" >&2
  exit 1
fi
