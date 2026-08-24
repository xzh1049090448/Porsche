#!/usr/bin/env bash
# Behavioural regression checks for the production deployment safety contract.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source_script="$script_dir/production-deploy.sh"
source_repo="$(cd -- "$script_dir/.." && pwd)"
test -f "$source_script"
test ! -e "$source_repo/.env" || { echo 'test must not create the source repository .env' >&2; exit 1; }
! grep -Eq 'docker[[:space:]].*compose[[:space:]]+down|docker[[:space:]].*prune|docker[[:space:]]+volume[[:space:]]+rm|docker[[:space:]]+network[[:space:]]+rm|docker[[:space:]]+image[[:space:]]+rm|(^|[[:space:]])mysql([[:space:]]|$)' "$source_script" || { echo 'deployment script contains a forbidden operation' >&2; exit 1; }

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/production-deploy-test.XXXXXX")"
repo_dir="$fixture_dir/repo"; mock_dir="$fixture_dir/bin"; command_log="$fixture_dir/commands.nul"
mkdir -p "$repo_dir/deploy" "$mock_dir"
repo_dir="$(cd -- "$repo_dir" && pwd)"
lock_dir="$repo_dir/.deploy-locks"
mkdir -p "$lock_dir"
ln -s "$source_script" "$repo_dir/deploy/production-deploy.sh"
printf 'DATABASE_URL=mysql://test:secret@db/test\n' >"$repo_dir/.env"
cleanup() { rm -rf -- "$fixture_dir"; }; trap cleanup EXIT

write_mock() {
    local command_name="$1"
    {
        printf '%s\n' '#!/usr/bin/env bash' 'set -Eeuo pipefail'
        printf '%s\n' 'printf "%s\\0" "'"$command_name"'" "$@" "__END__" >>"$COMMAND_LOG"'
        printf '%s\n' 'case "'"$command_name"'" in'
        printf '%s\n' '  git) case "${1:-}" in rev-parse) printf "%s\\n" "${MOCK_WORKTREE:-true}" ;; diff) exit "${MOCK_GIT_DIRTY:-0}" ;; esac ;;'
        printf '%s\n' '  docker) case "${1:-}" in container) [[ "${2:-}" != inspect ]] || [[ "${MOCK_OLD_CONTAINER:-present}" == present ]] ;; run) [[ "${MOCK_RUN_RESULT:-success}" == success ]] || exit 71; printf "new-container-id\\n" ;; start) [[ "${MOCK_ROLLBACK_START_RESULT:-success}" == success ]] || exit 72 ;; esac ;;'
        printf '%s\n' '  curl) [[ "${MOCK_HEALTH_RESULT:-success}" == success ]] || exit 28 ;;'
        printf '%s\n' '  flock) [[ "${1:-}" == -E && "${2:-}" == 75 && "${3:-}" == -n && "${4:-}" == 9 ]] || exit 76; [[ "${MOCK_LOCK_RESULT:-success}" == success ]] || exit 75 ;;'
        printf '%s\n' 'esac'
    } >"$mock_dir/$command_name"
    chmod +x "$mock_dir/$command_name"
}
write_mock git; write_mock docker; write_mock curl; write_mock sleep; write_mock flock

read_calls() { calls=(); local record current=''; while IFS= read -r -d '' record; do if [[ "$record" == '__END__' ]]; then calls+=("$current"); current=''; else current+="${current:+ }$record"; fi; done <"$command_log"; }
line_for() { local expression="$1" index; read_calls; for index in "${!calls[@]}"; do [[ "${calls[$index]}" == *"$expression"* ]] && { printf '%s\n' "$((index + 1))"; return; }; done; return 1; }
count_calls() { local expression="$1" count=0 call; read_calls; for call in "${calls[@]}"; do [[ "$call" == *"$expression"* ]] && ((count += 1)); done; printf '%s\n' "$count"; }
require_line() { local label="$1" expression="$2" line; line="$(line_for "$expression")" || { echo "missing expected command: $label" >&2; read_calls; printf 'calls: %s\n' "${calls[*]:-<none>}" >&2; exit 1; }; printf '%s\n' "$line"; }
assert_no_docker_writes() { read_calls; local call; for call in "${calls[@]-}"; do [[ "$call" == docker\ build* || "$call" == docker\ run* || "$call" == docker\ stop* || "$call" == docker\ rename* || "$call" == docker\ rm* ]] && { echo "unexpected Docker write: $call" >&2; exit 1; }; done; return 0; }
run_deploy() { : >"$command_log"; (cd "$repo_dir" && PATH="$mock_dir:$PATH" COMMAND_LOG="$command_log" LOCK_FILE="$lock_dir/existing-app.deploy.lock" APP_NAME="${TEST_APP_NAME:-existing-app}" IMAGE_NAME=test-image HOST_PORT="${TEST_HOST_PORT:-18000}" "$repo_dir/deploy/production-deploy.sh"); }

assert_successful_deploy() {
    run_deploy >"$fixture_dir/success-stdout"
    require_line lock 'flock -E 75 -n 9' >/dev/null
    require_line env "--env-file $repo_dir/.env" >/dev/null
    require_line health 'curl -fsS --connect-timeout 2 --max-time 3 http://127.0.0.1:18000/health' >/dev/null
    require_line container-id 'docker run -d --name existing-app' >/dev/null
}
assert_timeout_rolls_back() {
    if MOCK_HEALTH_RESULT=timeout run_deploy >"$fixture_dir/timeout-stdout" 2>"$fixture_dir/timeout-stderr"; then echo 'deployment must fail after bounded health timeouts' >&2; exit 1; fi
    require_line health 'curl -fsS --connect-timeout 2 --max-time 3 http://127.0.0.1:18000/health' >/dev/null
    require_line candidate-removal 'docker rm -f -- existing-app' >/dev/null
    require_line restore-name 'docker rename -- existing-app-rollback-' >/dev/null
    require_line restore-start 'docker start -- existing-app' >/dev/null
    [[ "$(count_calls 'curl -fsS --connect-timeout 2 --max-time 3 http://127.0.0.1:18000/health')" == 30 ]] || { echo 'health timeout must make exactly 30 bounded attempts' >&2; exit 1; }
}
assert_lock_contention_is_safe() {
    local stderr_file="$fixture_dir/lock-stderr"
    if MOCK_LOCK_RESULT=busy run_deploy >"$fixture_dir/lock-stdout" 2>"$stderr_file"; then echo 'deployment must fail while another deployment holds its lock' >&2; exit 1; fi
    grep -Fq 'another deployment is already running for existing-app' "$stderr_file" || { echo 'lock contention error is not readable' >&2; exit 1; }
    require_line lock 'flock -E 75 -n 9' >/dev/null
    assert_no_docker_writes
}
assert_start_failure_rolls_back() {
    if MOCK_RUN_RESULT=failure run_deploy >"$fixture_dir/startup-stdout" 2>"$fixture_dir/startup-stderr"; then echo 'deployment must fail when candidate startup fails' >&2; exit 1; fi
    require_line candidate-removal 'docker rm -f -- existing-app' >/dev/null
    require_line restore-name 'docker rename -- existing-app-rollback-' >/dev/null
    require_line restore-start 'docker start -- existing-app' >/dev/null
    [[ -z "$(line_for 'curl -fsS' || true)" ]] || { echo 'health check ran after candidate startup failure' >&2; exit 1; }
}
assert_invalid_input_has_no_docker_write() {
    if TEST_HOST_PORT=invalid run_deploy >"$fixture_dir/invalid-port-stdout" 2>"$fixture_dir/invalid-port-stderr"; then echo 'deployment accepted invalid port' >&2; exit 1; fi
    assert_no_docker_writes
    if MOCK_GIT_DIRTY=1 run_deploy; then echo 'deployment accepted dirty tracked worktree' >&2; exit 1; fi
    [[ -z "$(line_for 'git fetch origin main' || true)" ]] || { echo 'deployment fetched after dirty check' >&2; exit 1; }
}

assert_successful_deploy
if ENV_FILE="$fixture_dir/attempted-override.env" run_deploy >"$fixture_dir/override-stdout"; then :; else echo 'ENV_FILE must not alter the deployment environment file' >&2; exit 1; fi
require_line env-after-override "--env-file $repo_dir/.env" >/dev/null
[[ -z "$(line_for "$fixture_dir/attempted-override.env" || true)" ]] || { echo 'deployment honored ENV_FILE override' >&2; exit 1; }
assert_timeout_rolls_back
assert_start_failure_rolls_back
assert_lock_contention_is_safe
assert_invalid_input_has_no_docker_write
test ! -e "$source_repo/.env" || { echo 'test created the source repository .env' >&2; exit 1; }
grep -Fq '.env' "$source_repo/.dockerignore" || { echo '.dockerignore must exclude .env' >&2; exit 1; }
grep -Fq '!.env.example' "$source_repo/.dockerignore" || { echo '.dockerignore must retain .env.example' >&2; exit 1; }
for pattern in .git .worktrees .claw .superpowers data coverage test-results playwright-report node_modules .idea .vscode; do grep -Fqx "$pattern" "$source_repo/.dockerignore" || { echo ".dockerignore must exclude $pattern" >&2; exit 1; }; done
for required in Dockerfile go.mod go.sum cmd internal config; do if test -e "$source_repo/$required" && grep -Fqx "$required" "$source_repo/.dockerignore"; then echo ".dockerignore must retain required build input: $required" >&2; exit 1; fi; done
grep -Fq 'sudo bash deploy/production-deploy.sh' "$source_repo/README.md" || { echo 'README must document sudo bash deployment' >&2; exit 1; }
! grep -Fq 'sudo -E bash deploy/production-deploy.sh' "$source_repo/README.md" || { echo 'README must not recommend sudo -E deployment' >&2; exit 1; }
