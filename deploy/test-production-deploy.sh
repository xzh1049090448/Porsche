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
chmod 600 "$repo/.env"
printf 'ALLOWED_HOSTS=example.com\n# ACTION_SECURITY_HMAC_KEY=\n' >"$repo/.env.example"
cat >"$repo/deploy/merge-env-example.sh" <<'EOF_M'
#!/usr/bin/env bash
printf 'merge-env-example.sh %s %s\n' "$1" "$2" >>"$COMMAND_LOG"
EOF_M
chmod +x "$repo/deploy/merge-env-example.sh"

image_id="sha256:$(printf 'a%.0s' {1..64})"; revision="$(printf 'd%.0s' {1..40})"
cat >"$bin/git" <<'EOF_M'
#!/usr/bin/env bash
printf 'git %s\n' "$*" >>"$COMMAND_LOG"
case "${1:-}" in
 rev-parse) [[ "${2:-}" == --is-inside-work-tree ]] && printf 'true\n' || { [[ ! -e "$STATE_DIR/remote-advanced" ]] && printf '%s\n' "$EXPECTED_REVISION" || printf 'f%.0s' {1..40}; printf '\n'; };;
 diff) exit "${MOCK_GIT_DIRTY:-0}";;
 fetch) [[ "${MOCK_REMOTE_ADVANCE:-0}" != 1 ]] || : >"$STATE_DIR/remote-advanced";;
esac
EOF_M
cat >"$bin/docker" <<'EOF_M'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$COMMAND_LOG"
case "${1:-}" in
 image) [[ "${2:-}" == inspect ]] || exit 90; printf '%s\n' "${MOCK_INSPECT_ID:-$EXPECTED_IMAGE_ID}";;
 container) [[ "${MOCK_OLD_CONTAINER:-present}" == present ]];;
 run)
   if [[ " $* " == *' --entrypoint /app/check-config '* ]]; then [[ "${MOCK_CONFIG_RESULT:-success}" == success ]] || exit 81; printf 'configuration valid\n';
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
[[ "${USE_REAL_FLOCK:-0}" != 1 ]] || exec "$REAL_FLOCK" "$@"
[[ "${MOCK_FLOCK_RESULT:-success}" == success ]]
EOF_M
cat >"$bin/stat" <<'EOF_M'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "${1:-}" == --version ]]; then printf 'stat (GNU coreutils) test compatibility wrapper\n'; exit 0; fi
if [[ "${1:-}" == -L && "${2:-}" == -f ]]; then
    printf 'File: %s\n' "${4:-missing}"
    exit 1
fi
if [[ "${1:-}" == -L && "${2:-}" == -c ]]; then
    case "${3:-}" in
        '%i') exec /usr/bin/stat -f '%i' "${4}" ;;
        '%a') exec /usr/bin/stat -f '%Lp' "${4}" ;;
    esac
fi
if [[ "${1:-}" == -f ]]; then
    printf 'File: %s\n' "${3:-missing}"
    exit 1
fi
if [[ "${1:-}" == -c && "${2:-}" == '%a' ]]; then
    exec /usr/bin/stat -f '%Lp' "${3}"
fi
exit 64
EOF_M
cat >"$bin/kernel-flock" <<'EOF_M'
#!/usr/bin/env python3
import fcntl, os, sys
args=sys.argv[1:]; fd=int(args[-1]); nonblocking='-n' in args
try: fcntl.flock(fd, fcntl.LOCK_EX | (fcntl.LOCK_NB if nonblocking else 0))
except BlockingIOError: sys.exit(75)
EOF_M
chmod +x "$bin"/*
fail(){ echo "FAIL: $*" >&2; exit 1; }
run(){ : >"$log"; rm -f "$fixture/remote-advanced"; (cd "$repo" && PATH="$bin:$PATH" COMMAND_LOG="$log" STATE_DIR="$fixture" EXPECTED_IMAGE_ID="$image_id" EXPECTED_REVISION="$revision" LOCK_FILE="$repo/.deploy-locks/porsche-full-stack.deploy.lock" APP_NAME=app IMAGE_NAME=test-image HOST_PORT="${TEST_HOST_PORT:-18000}" PREBUILT_SOURCE_REVISION="${PREBUILT_SOURCE_REVISION:-$revision}" ENV_SNAPSHOT="${ENV_SNAPSHOT:-$repo/.env}" "$repo/deploy/production-deploy.sh"); }
line(){ grep -Fn -- "$1" "$log" | head -1 | cut -d: -f1; }
require(){ grep -Fq -- "$1" "$log" || { cat "$log" >&2; fail "missing: $1"; }; }
forbid(){ ! grep -Fq -- "$1" "$log" || fail "unexpected: $1"; }
before(){ local a b; a="$(line "$1")"; b="$(line "$2")"; [[ -n "$a" && -n "$b" && "$a" -lt "$b" ]] || fail "order: $1 before $2"; }

run >"$fixture/out"
require "merge-env-example.sh $repo/.env.example $repo/.env"
[[ "$(grep -Fc 'docker build --tag test-image .' "$log")" == 1 ]] || fail 'standalone must build exactly once'
before 'merge-env-example.sh' 'docker build --tag test-image .'
require "docker image inspect --format {{.Id}} test-image"
require "--entrypoint /app/check-config $image_id"
before '--entrypoint /app/check-config' 'docker stop -- app'
require "--publish 127.0.0.1:18000:8000 $image_id"
config_snapshot="$(grep -F 'docker run --rm --env-file ' "$log" | head -1 | sed -E 's/.*--env-file ([^ ]+).*/\1/')"
app_snapshot="$(grep -F 'docker run -d --name app --env-file ' "$log" | head -1 | sed -E 's/.*--env-file ([^ ]+).*/\1/')"
[[ -n "$config_snapshot" && "$config_snapshot" == "$app_snapshot" ]] || fail 'check-config and application did not share one snapshot'
[[ ! -e "$config_snapshot" ]] || fail 'standalone environment snapshot was not cleaned'

: >"$log"
PREBUILT_IMAGE_ID="$image_id" run >"$fixture/prebuilt-out"
forbid 'docker build'
forbid 'merge-env-example.sh'
require "docker image inspect --format {{.Id}} $image_id"
require "--entrypoint /app/check-config $image_id"
require "--publish 127.0.0.1:18000:8000 $image_id"
forbid 'git fetch'; forbid 'git switch'; forbid 'git reset'; forbid 'docker build'

: >"$log"
MOCK_REMOTE_ADVANCE=1 PREBUILT_IMAGE_ID="$image_id" run >"$fixture/remote-advance-out"
forbid 'git fetch'; forbid 'git switch'; forbid 'git reset'; forbid 'docker build'
[[ ! -e "$fixture/remote-advanced" ]] || fail 'prebuilt deployment observed a later remote revision'
grep -Fq "revision=$revision" "$fixture/remote-advance-out" || fail 'prebuilt deployment logged a drifting revision'

: >"$log"
APP_DOCKER_NETWORK=test-network PREBUILT_IMAGE_ID="$image_id" run >"$fixture/network-out"
require "docker run --rm --env-file $repo/.env --network test-network --entrypoint /app/check-config $image_id"
require "docker run -d --name app --env-file $repo/.env --publish 127.0.0.1:18000:8000 --network test-network $image_id"

: >"$log"
if PREBUILT_IMAGE_ID="$image_id" PREBUILT_SOURCE_REVISION="$(printf 'b%.0s' {1..40})" run >"$fixture/revision-out" 2>"$fixture/revision-err"; then fail 'mismatched prebuilt source revision accepted'; fi
forbid 'git fetch'; forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'

chmod 644 "$repo/.env"
: >"$log"
if PREBUILT_IMAGE_ID="$image_id" run >"$fixture/mode-out" 2>"$fixture/mode-err"; then fail 'insecure snapshot mode accepted'; fi
forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'
chmod 600 "$repo/.env"
: >"$log"
if PREBUILT_IMAGE_ID="$image_id" ENV_SNAPSHOT=relative.env run >"$fixture/path-out" 2>"$fixture/path-err"; then fail 'relative snapshot path accepted'; fi
forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'

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

system_flock="$bin/kernel-flock"
exec 7>"$repo/.deploy-locks/porsche-full-stack.deploy.lock"
"$system_flock" -x 7
USE_REAL_FLOCK=1 REAL_FLOCK="$system_flock" RELEASE_LOCK_FD=7 PREBUILT_IMAGE_ID="$image_id" run >"$fixture/inherited-lock-out"
exec 7>&-
forbid 'git fetch'; forbid 'docker build'

ready="$fixture/lock-ready"
( exec 7>"$repo/.deploy-locks/porsche-full-stack.deploy.lock"; python3 -c 'import fcntl,sys,time; fcntl.flock(7,fcntl.LOCK_EX); open(sys.argv[1],"w").close(); time.sleep(2)' "$ready" ) & holder_pid=$!
for _ in {1..50}; do [[ -e "$ready" ]] && break; /bin/sleep 0.02; done
if USE_REAL_FLOCK=1 REAL_FLOCK="$system_flock" run >"$fixture/contention-out" 2>"$fixture/contention-err"; then kill "$holder_pid" 2>/dev/null || true; wait "$holder_pid" 2>/dev/null || true; fail 'shared release lock contention accepted'; fi
kill "$holder_pid" 2>/dev/null || true; wait "$holder_pid" 2>/dev/null || true
forbid 'git fetch'; forbid 'docker build'; forbid 'docker stop'

writer_ready="$fixture/writer-ready"
( exec 6>"$repo/..env.merge.lock"; python3 -c 'import fcntl,sys,time; fcntl.flock(6,fcntl.LOCK_EX); open(sys.argv[1],"w").close(); time.sleep(2)' "$writer_ready" ) & writer_pid=$!
for _ in {1..50}; do [[ -e "$writer_ready" ]] && break; /bin/sleep 0.02; done
if USE_REAL_FLOCK=1 REAL_FLOCK="$system_flock" run >"$fixture/writer-out" 2>"$fixture/writer-err"; then kill "$writer_pid" 2>/dev/null || true; wait "$writer_pid" 2>/dev/null || true; fail 'environment writer lock contention accepted'; fi
kill "$writer_pid" 2>/dev/null || true; wait "$writer_pid" 2>/dev/null || true
forbid 'docker build'; forbid 'docker run'; forbid 'docker stop'

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
