#!/usr/bin/env bash
set -Eeuo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; source_script="$script_dir/restart-all.sh"
tmp="$(mktemp -d)"; trap '[[ "${KEEP_FIXTURE:-0}" == 1 ]] || rm -rf -- "$tmp"' EXIT
backend="$tmp/Porsche"; frontend="$tmp/Porsche-Web"; root="$tmp/www"; bin="$tmp/bin"; log="$tmp/log"; mkdir -p "$backend/deploy" "$frontend/dist" "$root" "$bin"
ln -s "$source_script" "$backend/deploy/restart-all.sh"; touch "$backend/.git" "$frontend/.git"
printf 'BASE=backend\n' >"$backend/.env.example"; printf 'EXISTING=preserved\n' >"$backend/.env"
printf 'VITE_USE_MOCK=false\n' >"$frontend/.env.example"; printf 'VITE_EXISTING=preserved\n' >"$frontend/.env"
printf '{}\n' >"$frontend/package.json"; printf '{}\n' >"$frontend/package-lock.json"; printf '<html>ok</html>\n' >"$frontend/dist/index.html"
cat >"$backend/deploy/merge-env-example.sh" <<'EOF_M'
#!/usr/bin/env bash
printf 'merge-env-example.sh %s %s\n' "$1" "$2" >>"$COMMAND_LOG"
[[ "${MOCK_MERGE_RESULT:-success}" == success ]] || exit 83
EOF_M
cat >"$backend/deploy/production-deploy.sh" <<'EOF_M'
#!/usr/bin/env bash
printf 'production-deploy.sh PREBUILT_IMAGE_ID=%s APP_DOCKER_NETWORK=%s\n' "${PREBUILT_IMAGE_ID:-}" "${APP_DOCKER_NETWORK:-}" >>"$COMMAND_LOG"
[[ "${MOCK_DEPLOY_RESULT:-success}" == success ]] || exit 84
EOF_M
cat >"$bin/git" <<'EOF_M'
#!/usr/bin/env bash
printf 'git %s\n' "$*" >>"$COMMAND_LOG"
[[ "${1:-}" == -C && "${3:-}" == rev-parse ]] && { printf 'true\n'; exit; }
[[ "${1:-}" == rev-parse ]] && printf 'true\n'
exit 0
EOF_M
cat >"$bin/docker" <<'EOF_M'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$COMMAND_LOG"
case "${1:-}" in image) printf '%s\n' "${MOCK_INSPECT_ID:-$EXPECTED_IMAGE_ID}";; run) [[ "${MOCK_CONFIG_RESULT:-success}" == success ]] || exit 85;; esac
EOF_M
cat >"$bin/npm" <<'EOF_M'
#!/usr/bin/env bash
printf 'npm %s\n' "$*" >>"$COMMAND_LOG"
case "${1:-}" in ci) [[ "${MOCK_NPM_CI_RESULT:-success}" == success ]];; run) [[ "${MOCK_BUILD_RESULT:-success}" == success ]];; esac
EOF_M
cat >"$bin/nginx" <<'EOF_M'
#!/usr/bin/env bash
printf 'nginx %s\n' "$*" >>"$COMMAND_LOG"; [[ "${MOCK_NGINX_RESULT:-success}" == success ]]
EOF_M
cat >"$bin/rsync" <<'EOF_M'
#!/usr/bin/env bash
printf 'rsync %s\n' "$*" >>"$COMMAND_LOG"
EOF_M
cat >"$bin/systemctl" <<'EOF_M'
#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >>"$COMMAND_LOG"
EOF_M
cat >"$bin/flock" <<'EOF_M'
#!/usr/bin/env bash
printf 'flock %s\n' "$*" >>"$COMMAND_LOG"; [[ "${MOCK_FLOCK_RESULT:-success}" == success ]]
EOF_M
cat >"$bin/id" <<'EOF_M'
#!/usr/bin/env bash
[[ "${1:-}" == -u ]] && printf '0\n'
EOF_M
chmod +x "$backend/deploy/"*.sh "$bin"/*
image_id="sha256:$(printf 'c%.0s' {1..64})"
fail(){ echo "FAIL: $*" >&2; exit 1; }
run(){ : >"$log"; PATH="$bin:$PATH" COMMAND_LOG="$log" EXPECTED_IMAGE_ID="$image_id" PORSCHE_RESTART_TEST_MODE=1 PORSCHE_RESTART_BACKEND_DIR="$backend" PORSCHE_RESTART_FRONTEND_DIR="$frontend" PORSCHE_RESTART_FRONTEND_ROOT="$root" PORSCHE_RESTART_LOCK_FILE="$tmp/full.lock" PORSCHE_RESTART_STAGE_PARENT="$tmp" "$backend/deploy/restart-all.sh"; }
line(){ grep -Fn -- "$1" "$log" | head -1 | cut -d: -f1; }; require(){ grep -Fq -- "$1" "$log" || fail "missing $1"; }; forbid(){ ! grep -Fq -- "$1" "$log" || fail "unexpected $1"; }; before(){ local a b; a="$(line "$1")"; b="$(line "$2")"; [[ -n "$a" && -n "$b" && "$a" -lt "$b" ]] || fail "expected $1 before $2"; }
assert_unchanged(){ grep -Fqx 'EXISTING=preserved' "$backend/.env"; grep -Fqx 'VITE_EXISTING=preserved' "$frontend/.env"; }
run >"$tmp/out"
require 'flock -E 75 -n 9'; require "merge-env-example.sh $backend/.env.example $backend/.env"; require "merge-env-example.sh $frontend/.env.example $frontend/.env"
require "docker build --tag ai-gateway-go:release-candidate $backend"; require 'docker image inspect --format {{.Id}} ai-gateway-go:release-candidate'; require "--entrypoint ./check-config $image_id"
require 'npm ci'; require 'npm run build'; forbid 'npm install'; require "production-deploy.sh PREBUILT_IMAGE_ID=$image_id APP_DOCKER_NETWORK=porsche-app"
before 'flock -E 75 -n 9' 'merge-env-example.sh'; before 'git reset --hard origin/main' 'merge-env-example.sh'; before 'merge-env-example.sh' 'docker build'; before '--entrypoint ./check-config' 'npm ci'; before 'npm ci' 'npm run build'; before 'npm run build' 'nginx -t'; before 'nginx -t' 'production-deploy.sh'; before 'production-deploy.sh' 'rsync --archive'; before 'rsync --archive' 'systemctl reload nginx'; assert_unchanged

negative(){ local variable="$1" value="$2"; export "$variable=$value"; if run >"$tmp/fail-out" 2>"$tmp/fail-err"; then unset "$variable"; fail "$variable failure accepted"; fi; unset "$variable"; forbid 'production-deploy.sh'; forbid 'rsync --archive'; forbid 'systemctl reload nginx'; forbid 'docker stop'; forbid 'docker rm'; assert_unchanged; }
negative MOCK_MERGE_RESULT failure
negative MOCK_CONFIG_RESULT failure
negative MOCK_NPM_CI_RESULT failure
negative MOCK_BUILD_RESULT failure
negative MOCK_NGINX_RESULT failure
negative MOCK_INSPECT_ID invalid-image-id
mv "$frontend/package-lock.json" "$frontend/package-lock.missing"
if run >"$tmp/missing-out" 2>"$tmp/missing-err"; then fail 'missing lockfile accepted'; fi
forbid 'git fetch'; forbid 'git reset'; forbid 'merge-env-example.sh'; forbid 'docker build'; forbid 'production-deploy.sh'; forbid 'rsync --archive'; forbid 'systemctl reload nginx'
mv "$frontend/package-lock.missing" "$frontend/package-lock.json"
negative MOCK_FLOCK_RESULT failure
! grep -Fq 'fixture-secret-never-log' "$tmp/out" "$tmp/fail-out" "$tmp/fail-err" "$log" 2>/dev/null || fail 'environment value leaked'
for forbidden in 'npm install' mysql 'compose down' prune 'volume rm' 'network rm' 'docker rm'; do ! grep -Fqi "$forbidden" "$source_script" || fail "forbidden operation in restart-all: $forbidden"; done
bash -n "$source_script"
echo 'PASS: restart-all immutable release orchestration checks'
