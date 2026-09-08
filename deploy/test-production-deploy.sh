#!/usr/bin/env bash
set -Eeuo pipefail
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source_script="$script_dir/production-deploy.sh"
source_repo="$(cd -- "$script_dir/.." && pwd)"
test -f "$source_script"
test ! -e "$source_repo/.env" || { echo 'test must not create source .env' >&2; exit 1; }
! grep -Eq 'docker[[:space:]].*compose[[:space:]]+down|docker[[:space:]].*prune|docker[[:space:]]+volume[[:space:]]+rm|docker[[:space:]]+network[[:space:]]+rm|docker[[:space:]]+image[[:space:]]+rm|(^|[[:space:]])mysql([[:space:]]|$)' "$source_script" || { echo 'deployment script contains a forbidden operation' >&2; exit 1; }
fixture="$(mktemp -d "${TMPDIR:-/tmp}/production-deploy-test.XXXXXX")"; trap 'rm -rf -- "$fixture"' EXIT
repo="$fixture/repo"; bin="$fixture/bin"; log="$fixture/log"; mkdir -p "$repo/deploy" "$repo/.deploy-locks" "$bin"
repo="$(cd "$repo" && pwd)"
ln -s "$source_script" "$repo/deploy/production-deploy.sh"
printf 'ALLOWED_HOSTS=example.com\nACTION_SECURITY_HMAC_KEY=fixture-secret-never-log\n' >"$repo/.env"

image_id="sha256:$(printf 'a%.0s' {1..64})"
cat >"$bin/git" <<'EOF_M'
#!/usr/bin/env bash
printf 'git %s\n' "$*" >>"$COMMAND_LOG"
case "${1:-}" in rev-parse) [[ "${2:-}" == --is-inside-work-tree ]] && printf 'true\n' || printf 'revision\n';; diff) exit "${MOCK_GIT_DIRTY:-0}";; esac
EOF_M
cat >"$bin/docker" <<'EOF_M'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$COMMAND_LOG"
case "${1:-}" in
 image) [[ "${2:-}" == inspect ]] || exit 90; printf '%s\n' "${MOCK_INSPECT_ID:-$EXPECTED_IMAGE_ID}";;
 container) [[ "${MOCK_OLD_CONTAINER:-present}" == present ]];;
 run)
   if [[ " $* " == *' --entrypoint ./check-config '* ]]; then [[ "${MOCK_CONFIG_RESULT:-success}" == success ]] || exit 81; printf 'configuration valid\n';
   else [[ "${MOCK_RUN_RESULT:-success}" == success ]] || exit 82; printf 'container-id\n'; fi;;
 start) :;;
esac
EOF_M
cat >"$bin/curl" <<'EOF_M'
#!/usr/bin/env bash
printf 'curl %s\n' "$*" >>"$COMMAND_LOG"
[[ "${MOCK_CURL_RESULT:-success}" == success ]]
EOF_M
cat >"$bin/sleep" <<'EOF_M'
#!/usr/bin/env bash
printf 'sleep %s\n' "$*" >>"$COMMAND_LOG"
EOF_M
cat >"$bin/flock" <<'EOF_M'
#!/usr/bin/env bash
printf 'flock %s\n' "$*" >>"$COMMAND_LOG"
[[ "${MOCK_FLOCK_RESULT:-success}" == success ]]
EOF_M
chmod +x "$bin"/*
fail(){ echo "FAIL: $*" >&2; exit 1; }
run(){ : >"$log"; (cd "$repo" && PATH="$bin:$PATH" COMMAND_LOG="$log" EXPECTED_IMAGE_ID="$image_id" LOCK_FILE="$repo/.deploy-locks/app.deploy.lock" APP_NAME=app IMAGE_NAME=test-image HOST_PORT="${TEST_HOST_PORT:-18000}" "$repo/deploy/production-deploy.sh"); }
line(){ grep -Fn -- "$1" "$log" | head -1 | cut -d: -f1; }
require(){ grep -Fq -- "$1" "$log" || fail "missing: $1"; }
forbid(){ ! grep -Fq -- "$1" "$log" || fail "unexpected: $1"; }
before(){ local a b; a="$(line "$1")"; b="$(line "$2")"; [[ -n "$a" && -n "$b" && "$a" -lt "$b" ]] || fail "order: $1 before $2"; }

run >"$fixture/out"
[[ "$(grep -Fc 'docker build --tag test-image .' "$log")" == 1 ]] || fail 'standalone must build exactly once'
require "docker image inspect --format {{.Id}} test-image"
require "docker run --rm --env-file $repo/.env --entrypoint ./check-config $image_id"
before '--entrypoint ./check-config' 'docker stop -- app'
require "--publish 127.0.0.1:18000:8000 $image_id"

: >"$log"
PREBUILT_IMAGE_ID="$image_id" run >"$fixture/prebuilt-out"
forbid 'docker build'
require "docker image inspect --format {{.Id}} $image_id"
require "--entrypoint ./check-config $image_id"
require "--publish 127.0.0.1:18000:8000 $image_id"

: >"$log"
if PREBUILT_IMAGE_ID=latest run >"$fixture/bad-out" 2>"$fixture/bad-err"; then fail 'invalid prebuilt ID accepted'; fi
forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'

: >"$log"
if PREBUILT_IMAGE_ID="$image_id" MOCK_INSPECT_ID="sha256:$(printf 'b%.0s' {1..64})" run >"$fixture/mismatch-out" 2>"$fixture/mismatch-err"; then fail 'mismatched ID accepted'; fi
forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'

: >"$log"
if MOCK_CONFIG_RESULT=failure run >"$fixture/config-out" 2>"$fixture/config-err"; then fail 'config failure accepted'; fi
forbid 'docker stop'; forbid 'docker rename'; forbid 'docker rm'
! grep -Fq 'fixture-secret-never-log' "$fixture/config-out" "$fixture/config-err" "$log" || fail 'secret leaked'

: >"$log"
if MOCK_CURL_RESULT=failure run >"$fixture/health-out" 2>"$fixture/health-err"; then fail 'health failure accepted'; fi
require 'docker rm -f -- app'; require 'docker rename -- app-rollback-'; require 'docker start -- app'
[[ "$(grep -Fc 'curl -fsS' "$log")" == 30 ]] || fail 'health check must remain bounded to 30 attempts'

: >"$log"
if MOCK_FLOCK_RESULT=failure run >"$fixture/lock-out" 2>"$fixture/lock-err"; then fail 'lock contention accepted'; fi
forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'

printf 'ACTION_SECURITY_HMAC_KEY=fixture-secret-never-log\n' >"$repo/.env"
: >"$log"
if run >"$fixture/hosts-out" 2>"$fixture/hosts-err"; then fail 'missing ALLOWED_HOSTS accepted'; fi
forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'
printf 'ALLOWED_HOSTS=example.com\nACTION_SECURITY_HMAC_KEY=fixture-secret-never-log\n' >"$repo/.env"

: >"$log"
if TEST_HOST_PORT=invalid run >"$fixture/port-out" 2>"$fixture/port-err"; then fail 'invalid port accepted'; fi
forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'

: >"$log"
if MOCK_GIT_DIRTY=1 run >"$fixture/dirty-out" 2>"$fixture/dirty-err"; then fail 'dirty checkout accepted'; fi
forbid 'git fetch'; forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'

for env_line in 'ALLOWED_HOSTS="example.com,127.0.0.1"' "ALLOWED_HOSTS='example.com,127.0.0.1'" ' export ALLOWED_HOSTS=example.com,127.0.0.1'; do
    printf '%s\nACTION_SECURITY_HMAC_KEY=fixture-secret-never-log\n' "$env_line" >"$repo/.env"
    run >"$fixture/quoted-out"
    require 'curl -fsS -H Host: example.com'
done
printf 'ALLOWED_HOSTS=example.com\nACTION_SECURITY_HMAC_KEY=fixture-secret-never-log\n' >"$repo/.env"

bash -n "$source_script"
grep -Fq '.env' "$source_repo/.dockerignore"
grep -Fq '!.env.example' "$source_repo/.dockerignore"
echo 'PASS: production deployment immutable preflight checks'
