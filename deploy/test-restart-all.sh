#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source_script="$script_dir/restart-all.sh"

if [[ ! -f "$source_script" ]]; then
    echo "FAIL: restart-all.sh is missing: $source_script" >&2
    exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf -- "$tmp_dir"' EXIT
backend_dir="$tmp_dir/Porsche"
frontend_dir="$tmp_dir/Porsche-Web"
frontend_root="$tmp_dir/www/porsche-web"
mock_bin="$tmp_dir/bin"
command_log="$tmp_dir/commands.log"
lock_file="$tmp_dir/porsche-full-stack.deploy.lock"
mkdir -p "$backend_dir/deploy" "$frontend_dir/dist" "$frontend_root" "$mock_bin"
ln -s "$source_script" "$backend_dir/deploy/restart-all.sh"
printf 'DATABASE_URL=mysql://test:secret@db/platform\n' >"$backend_dir/.env"
printf '{"scripts":{"build":"vite build"}}\n' >"$frontend_dir/package.json"
printf '<!doctype html><title>fixture</title>\n' >"$frontend_dir/dist/index.html"
touch "$backend_dir/.git" "$frontend_dir/.git"

cat >"$backend_dir/deploy/production-deploy.sh" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf 'backend deploy/production-deploy.sh APP_DOCKER_NETWORK=%s\n' "${APP_DOCKER_NETWORK:-}" >>"$COMMAND_LOG"
[[ "${APP_DOCKER_NETWORK:-}" == 'porsche-app' ]]
[[ "${MOCK_BACKEND_DEPLOY_RESULT:-success}" != failure ]]
EOF
chmod +x "$backend_dir/deploy/production-deploy.sh"

cat >"$mock_bin/git" <<'EOF'
#!/usr/bin/env bash
printf 'git %s\n' "$*" >>"$COMMAND_LOG"
case "${1:-}" in
  rev-parse) printf 'true\n' ;;
  diff|fetch|switch|reset) ;;
  *) ;;
esac
EOF
cat >"$mock_bin/npm" <<'EOF'
#!/usr/bin/env bash
printf 'npm %s\n' "$*" >>"$COMMAND_LOG"
[[ "${MOCK_NPM_RESULT:-success}" != failure ]]
EOF
cat >"$mock_bin/rsync" <<'EOF'
#!/usr/bin/env bash
printf 'rsync %s\n' "$*" >>"$COMMAND_LOG"
if [[ "${MOCK_RSYNC_COPY:-0}" == 1 ]]; then
  source="${@: -2:1}"
  destination="${@: -1}"
  /bin/cp -a "${source%/}/." "${destination%/}/"
fi
EOF
cat >"$mock_bin/nginx" <<'EOF'
#!/usr/bin/env bash
printf 'nginx %s\n' "$*" >>"$COMMAND_LOG"
[[ "${MOCK_NGINX_RESULT:-success}" != failure ]]
EOF
cat >"$mock_bin/systemctl" <<'EOF'
#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >>"$COMMAND_LOG"
EOF
cat >"$mock_bin/docker" <<'EOF'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$COMMAND_LOG"
[[ "$*" == 'network inspect porsche-app' ]]
EOF
cat >"$mock_bin/flock" <<'EOF'
#!/usr/bin/env bash
printf 'flock %s\n' "$*" >>"$COMMAND_LOG"
EOF
cat >"$mock_bin/id" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == -u ]]; then printf '0\n'; else /usr/bin/id "$@"; fi
EOF
chmod +x "$mock_bin"/*

fail() { echo "FAIL: $*" >&2; exit 1; }
require_line() { grep -Fqx "$2" "$command_log" || fail "missing command: $2"; }
require_contains() { grep -Fq "$2" "$command_log" || fail "missing command containing: $2"; }
forbid_line() { ! grep -Fq "$2" "$command_log" || fail "unexpected command: $2"; }
line_number() { grep -Fn "$2" "$command_log" | head -n1 | cut -d: -f1; }
assert_before() {
    local first second first_line second_line
    first="$1"; second="$2"
    first_line="$(line_number '' "$first")"
    second_line="$(line_number '' "$second")"
    [[ -n "$first_line" && -n "$second_line" && "$first_line" -lt "$second_line" ]] || fail "expected '$first' before '$second'"
}

run_script() {
    : >"$command_log"
    PATH="$mock_bin:$PATH" COMMAND_LOG="$command_log" \
      PORSCHE_RESTART_TEST_MODE=1 \
      PORSCHE_RESTART_BACKEND_DIR="$backend_dir" \
      PORSCHE_RESTART_FRONTEND_DIR="$frontend_dir" \
      PORSCHE_RESTART_FRONTEND_ROOT="$frontend_root" \
      PORSCHE_RESTART_LOCK_FILE="$lock_file" \
      PORSCHE_RESTART_STAGE_PARENT="$tmp_dir" \
      "$backend_dir/deploy/restart-all.sh"
}

assert_happy_path_ordering() {
    run_script
    require_line command 'npm install --package-lock=false'
    require_line command 'npm run build'
    require_line command 'backend deploy/production-deploy.sh APP_DOCKER_NETWORK=porsche-app'
    require_contains command 'rsync --archive --delete --delay-updates'
    assert_before 'npm run build' 'backend deploy/production-deploy.sh APP_DOCKER_NETWORK=porsche-app'
    assert_before 'backend deploy/production-deploy.sh APP_DOCKER_NETWORK=porsche-app' 'rsync --archive --delete --delay-updates'
    assert_before 'nginx -t' 'systemctl reload nginx'
}

assert_frontend_permissions_are_normalized() {
    local path
    mode_for() {
        path="$1"
        stat -f '%Lp' "$path" 2>/dev/null || stat -c '%a' "$path"
    }

    mkdir -p "$frontend_dir/dist/assets"
    printf 'fixture-asset\n' >"$frontend_dir/dist/assets/app.js"
    chmod 700 "$frontend_dir/dist" "$frontend_dir/dist/assets"
    chmod 600 "$frontend_dir/dist/index.html" "$frontend_dir/dist/assets/app.js"

    MOCK_RSYNC_COPY=1 run_script

    [[ "$(mode_for "$frontend_root")" == 755 ]] || fail 'restart retained a restrictive frontend root mode'
    [[ "$(mode_for "$frontend_root/index.html")" == 644 ]] || fail 'restart retained a restrictive index.html mode'
    [[ "$(mode_for "$frontend_root/assets")" == 755 ]] || fail 'restart retained a restrictive asset directory mode'
    [[ "$(mode_for "$frontend_root/assets/app.js")" == 644 ]] || fail 'restart retained a restrictive asset file mode'

    chmod 755 "$frontend_dir/dist" "$frontend_dir/dist/assets" "$frontend_root" "$frontend_root/assets"
    chmod 644 "$frontend_dir/dist/index.html" "$frontend_dir/dist/assets/app.js" "$frontend_root/index.html" "$frontend_root/assets/app.js"
}

assert_frontend_failure_stops_before_backend() {
    if MOCK_NPM_RESULT=failure run_script; then fail 'npm failure unexpectedly succeeded'; fi
    forbid_line command 'backend deploy/production-deploy.sh'
    forbid_line command 'rsync --archive --delete --delay-updates'
}

assert_backend_failure_stops_before_publish() {
    if MOCK_BACKEND_DEPLOY_RESULT=failure run_script; then fail 'backend failure unexpectedly succeeded'; fi
    forbid_line command 'rsync --archive --delete --delay-updates'
    forbid_line command 'systemctl reload nginx'
}

assert_nginx_failure_does_not_reload() {
    if MOCK_NGINX_RESULT=failure run_script; then fail 'nginx failure unexpectedly succeeded'; fi
    forbid_line command 'rsync --archive --delete --delay-updates'
    forbid_line command 'systemctl reload nginx'
}

assert_no_forbidden_operations() {
    run_script
    for forbidden in mysql 'compose down' prune 'volume rm' 'network rm' 'docker rm'; do
        ! grep -Fqi "$forbidden" "$command_log" || fail "forbidden operation logged: $forbidden"
    done
}

assert_happy_path_ordering
assert_frontend_permissions_are_normalized
assert_frontend_failure_stops_before_backend
assert_backend_failure_stops_before_publish
assert_nginx_failure_does_not_reload
assert_no_forbidden_operations
echo 'PASS: restart-all deployment regression checks'
