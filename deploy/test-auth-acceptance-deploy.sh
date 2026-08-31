#!/usr/bin/env bash
# Behavioural RED contract for the isolated auth acceptance deployment tools.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source_repo="$(cd -- "$script_dir/.." && pwd)"
source_env_fixture=''
if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_SOURCE_ENV_REGRESSION:-0}" == 1 ]]; then
    source_env_fixture="$(mktemp -d "${TMPDIR:-/tmp}/auth-acceptance-source-env.XXXXXX")"
    source_repo="$source_env_fixture"
    printf 'fixture-only-source-env\n' >"$source_repo/.env"
fi
fail() { echo "FAIL: $*" >&2; exit 1; }

source_env_state() {
    local source_env="$source_repo/.env"
    if [[ ! -e "$source_env" ]]; then
        printf '%s\n' absent
    elif stat -f '%i:%m:%z' "$source_env" >/dev/null 2>&1; then
        printf 'present:%s\n' "$(stat -f '%i:%m:%z' "$source_env")"
    else
        printf 'present:%s\n' "$(stat -c '%i:%Y:%s' "$source_env")"
    fi
}

source_env_before="$(source_env_state)"
fixture_dir=''
cleanup() {
    local exit_status=$? source_env_after
    source_env_after="$(source_env_state)"
    if [[ "$source_env_after" != "$source_env_before" ]]; then
        echo 'FAIL: fixture changed source-repository .env metadata' >&2
        exit_status=1
    fi
    [[ -z "$fixture_dir" ]] || rm -rf -- "$fixture_dir"
    [[ -z "$source_env_fixture" ]] || rm -rf -- "$source_env_fixture"
    trap - EXIT
    exit "$exit_status"
}
trap cleanup EXIT

selected_checks=("$@")
if (( ${#selected_checks[@]} == 0 )); then
    selected_checks=(bootstrap migration deploy rollback)
fi
for selected_check in "${selected_checks[@]}"; do
    case "$selected_check" in
        bootstrap|migration|deploy|rollback) ;;
        *) fail "unknown check: $selected_check" ;;
    esac
done

script_for_check() {
    case "$1" in
        bootstrap) printf '%s\n' bootstrap-auth-redis.sh ;;
        migration) printf '%s\n' auth-acceptance-migrate.sh ;;
        deploy) printf '%s\n' auth-acceptance-deploy.sh ;;
        rollback) printf '%s\n' auth-acceptance-rollback.sh ;;
    esac
}

# Keep the first RED failure legible while none of the production entry points
# exists. Task 2 onward must make these contracts executable rather than change
# the fixture to manufacture a pass.
for selected_check in "${selected_checks[@]}"; do
    required_script="$(script_for_check "$selected_check")"
    [[ -x "$script_dir/$required_script" ]] || fail "missing required script: deploy/$required_script"
done

for selected_check in "${selected_checks[@]}"; do
    required_script="$(script_for_check "$selected_check")"
    ! grep -Eqi 'compose[[:space:]]+down|docker[[:space:]].*prune|volume[[:space:]]+rm|network[[:space:]]+rm|mysql[[:space:]].*DROP' "$script_dir/$required_script" || {
        fail "forbidden destructive operation in deploy/$required_script"
    }
done

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/auth-acceptance-deploy-test.XXXXXX")"
backend_dir="$fixture_dir/Porsche"
frontend_dir="$fixture_dir/Porsche-Web"
frontend_root="$fixture_dir/www/porsche-web"
manifest_dir="$fixture_dir/manifests"
mock_dir="$fixture_dir/bin"
command_log="$fixture_dir/commands.nul"
mkdir -p "$backend_dir/deploy" "$frontend_dir/.git" "$frontend_dir/dist" "$frontend_root" "$manifest_dir" "$mock_dir"
printf 'APP_ENV=production\nREDIS_URL=redis://:fixture-only-password@porsche-redis:6379/0\nALLOWED_HOSTS=aiportcloud.com\nAUTH_TRUSTED_ORIGINS=https://aiportcloud.com\n' >"$backend_dir/.env"
printf '<!doctype html><title>fixture</title>\n' >"$frontend_dir/dist/index.html"
printf 'fixture-static\n' >"$frontend_root/index.html"
printf 'fixture-only-password-that-is-longer-than-thirty-two-bytes\n' >"$fixture_dir/redis-password"
touch "$backend_dir/.git"

# Every mock appends command name, argv, and an end marker as NUL fields. The
# fixture never invokes a real Docker/Git mutation, nor reads any real secret.
write_mock() {
    local command_name="$1"
    printf '%s\n' '#!/usr/bin/env bash' 'set -Eeuo pipefail' \
        'command_name="$(basename -- "$0")"' \
        'printf "%s\\0" "$command_name" "$@" "__END__" >>"$COMMAND_LOG"' \
        'case "$command_name" in' \
        '  id) [[ "${1:-}" == "-u" ]] && printf "0\\n" ;;' \
        '  git)' \
        '    case "${1:-}" in' \
        '      branch) printf "%s\\n" "${MOCK_BRANCH:-feature/user-registration-management}" ;;' \
        '      rev-parse) printf "%s\\n" "${MOCK_GIT_SHA:-fixture-sha}" ;;' \
        '      diff|status) [[ "${MOCK_GIT_DIRTY:-0}" == 0 ]] ;;' \
        '    esac ;;' \
        '  docker)' \
        '    case "${1:-}" in' \
        '      network|container) [[ "${MOCK_DOCKER_INSPECT_RESULT:-success}" == success ]] ;;' \
        '      run) [[ "${MOCK_DOCKER_RUN_RESULT:-success}" == success ]] || exit 71; printf "fixture-container\\n" ;;' \
        '    esac ;;' \
        '  curl) [[ "${MOCK_HEALTH_RESULT:-success}" == success ]] || exit 28 ;;' \
        '  npm) [[ "${MOCK_NPM_RESULT:-success}" == success ]] || exit 72 ;;' \
        '  rsync) [[ "${MOCK_RSYNC_RESULT:-success}" == success ]] || exit 73 ;;' \
        '  nginx) [[ "${MOCK_NGINX_RESULT:-success}" == success ]] || exit 74 ;;' \
        '  systemctl) [[ "${MOCK_SYSTEMCTL_RESULT:-success}" == success ]] || exit 75 ;;' \
        '  flock) [[ "${MOCK_FLOCK_RESULT:-success}" == success ]] || exit 76 ;;' \
        'esac' >"$mock_dir/$command_name"
    chmod +x "$mock_dir/$command_name"
}
for command_name in git docker npm rsync nginx systemctl flock id curl sleep; do
    write_mock "$command_name"
done

read_calls() {
    calls=()
    local field current=''
    while IFS= read -r -d '' field; do
        if [[ "$field" == '__END__' ]]; then
            calls+=("$current")
            current=''
        else
            current+="${current:+ }$field"
        fi
    done <"$command_log"
}

require_call() {
    local expected="$1" call
    read_calls
    for call in "${calls[@]-}"; do
        [[ "$call" == *"$expected"* ]] && return 0
    done
    fail "missing expected mock invocation: $expected"
}

forbid_call() {
    local forbidden="$1" call
    read_calls
    for call in "${calls[@]-}"; do
        [[ "$call" == *"$forbidden"* ]] && fail "forbidden mock invocation: $call"
    done
}

assert_no_docker_or_rsync_writes() {
    local call
    read_calls
    for call in "${calls[@]-}"; do
        [[ "$call" == docker\ build* || "$call" == docker\ run* || "$call" == docker\ stop* || "$call" == docker\ rename* || "$call" == docker\ rm* || "$call" == rsync\ * ]] && {
            fail "unexpected write after rejected deployment: $call"
        }
    done
}

run_entrypoint() {
    local entrypoint="$1"
    shift
    : >"$command_log"
    PATH="$mock_dir:$PATH" COMMAND_LOG="$command_log" \
        PORSCHE_AUTH_ACCEPTANCE_TEST_MODE=1 \
        PORSCHE_AUTH_ACCEPTANCE_BACKEND_DIR="$backend_dir" \
        PORSCHE_AUTH_ACCEPTANCE_FRONTEND_DIR="$frontend_dir" \
        PORSCHE_AUTH_ACCEPTANCE_FRONTEND_ROOT="$frontend_root" \
        PORSCHE_AUTH_ACCEPTANCE_MANIFEST_DIR="$manifest_dir" \
        PORSCHE_AUTH_ACCEPTANCE_REDIS_CONFIG_DIR="$fixture_dir/redis-config" \
        PORSCHE_AUTH_ACCEPTANCE_LOCK_FILE="$fixture_dir/auth-acceptance.lock" \
        PORSCHE_AUTH_ACCEPTANCE_TEST_PASSWORD_FILE="$fixture_dir/redis-password" \
        "$script_dir/$entrypoint" "$@"
}

run_bootstrap() { run_entrypoint bootstrap-auth-redis.sh; }
run_migration() { run_entrypoint auth-acceptance-migrate.sh --confirm-auth-schema-migration; }
run_deploy() { run_entrypoint auth-acceptance-deploy.sh; }
run_rollback() { run_entrypoint auth-acceptance-rollback.sh --confirm-auth-acceptance-rollback; }

assert_bootstrap_creates_internal_redis() {
    run_bootstrap
    require_call 'docker volume create porsche-redis-data'
    require_call 'docker run -d --name porsche-redis --restart unless-stopped --network porsche-app'
    forbid_call 'docker run -p'
    forbid_call 'docker run --publish'
}

assert_deploy_refuses_main_or_dirty_checkout_without_writes() {
    if MOCK_BRANCH=main run_deploy; then fail 'deployment accepted main'; fi
    assert_no_docker_or_rsync_writes
    if MOCK_GIT_DIRTY=1 run_deploy; then fail 'deployment accepted dirty checkout'; fi
    assert_no_docker_or_rsync_writes
}

assert_candidate_failure_restores_old_application() {
    if MOCK_HEALTH_RESULT=failure run_deploy; then fail 'candidate unexpectedly healthy'; fi
    require_call 'docker rm -f -- ai-gateway-go'
    require_call 'docker rename -- ai-gateway-go-acceptance-rollback-'
    require_call 'docker start -- ai-gateway-go'
    forbid_call 'rsync --archive --delete --delay-updates'
}

# These are intentionally unreachable until the target production entry point
# exists. They are the behavioral requirements that later tasks must turn green.
for selected_check in "${selected_checks[@]}"; do
    case "$selected_check" in
        bootstrap) assert_bootstrap_creates_internal_redis ;;
        migration) run_migration ;;
        deploy)
            assert_deploy_refuses_main_or_dirty_checkout_without_writes
            assert_candidate_failure_restores_old_application
            ;;
        rollback) run_rollback ;;
    esac
done
echo "PASS: auth acceptance deployment regression checks (${selected_checks[*]})"
