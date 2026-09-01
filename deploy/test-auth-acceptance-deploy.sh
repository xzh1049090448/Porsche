#!/usr/bin/env bash
# Behavioural RED contract for isolated auth acceptance deployment tools.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source_repo="$(cd -- "$script_dir/.." && pwd)"
fail() { echo "FAIL: $*" >&2; exit 1; }

env_state_for() {
    local env_path="$1"
    if [[ ! -e "$env_path" ]]; then
        printf '%s\n' absent
    elif stat -f '%i:%m:%z' "$env_path" >/dev/null 2>&1; then
        printf 'present:%s\n' "$(stat -f '%i:%m:%z' "$env_path")"
    else
        printf 'present:%s\n' "$(stat -c '%i:%Y:%s' "$env_path")"
    fi
}

# The source checkout is audit-only: it is never an entrypoint root or an env
# source. The fixture records only source .env metadata, never its contents.
source_env_before="$(env_state_for "$source_repo/.env")"
fixture_dir=''
source_env_probe=''
cleanup() {
    local exit_status=$? source_env_after
    source_env_after="$(env_state_for "$source_repo/.env")"
    if [[ "$source_env_after" != "$source_env_before" ]]; then
        echo 'FAIL: fixture changed source-repository .env metadata' >&2
        exit_status=1
    fi
    [[ -z "$fixture_dir" ]] || rm -rf -- "$fixture_dir"
    [[ -z "$source_env_probe" ]] || rm -rf -- "$source_env_probe"
    trap - EXIT
    exit "$exit_status"
}
trap cleanup EXIT

selected_checks=("$@")
if (( ${#selected_checks[@]} == 0 )); then
    selected_checks=(bootstrap migration deploy rollback root-bootstrap docs)
fi
for selected_check in "${selected_checks[@]}"; do
    case "$selected_check" in bootstrap|migration|deploy|rollback|root-bootstrap|docs) ;; *) fail "unknown check: $selected_check" ;; esac
done

script_for_check() {
    case "$1" in
        bootstrap) printf '%s\n' bootstrap-auth-redis.sh ;;
        migration) printf '%s\n' auth-acceptance-migrate.sh ;;
        deploy) printf '%s\n' auth-acceptance-deploy.sh ;;
        rollback) printf '%s\n' auth-acceptance-rollback.sh ;;
        root-bootstrap) printf '%s\n' auth-acceptance-bootstrap-root.sh ;;
    esac
}

fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/auth-acceptance-deploy-test.XXXXXX")"
backend_dir="$fixture_dir/Porsche"
frontend_dir="$fixture_dir/Porsche-Web"
frontend_root="$fixture_dir/www/porsche-web"
manifest_dir="$fixture_dir/manifests"
mock_dir="$fixture_dir/bin"
command_log="$fixture_dir/commands.nul"
fixture_image_id="sha256:$(printf '%064d' 0)"
fixture_lock_file="$fixture_dir/auth-acceptance.lock"
fixture_tmp_dir="$fixture_dir/tmp"
fixture_untrusted_tmp_dir="$fixture_dir/untrusted"
run_count=0
mkdir -p "$backend_dir/deploy" "$backend_dir/cmd/bootstrap-root" "$backend_dir/vendor/fixture-malicious" "$frontend_dir/.git" "$frontend_dir/dist" "$frontend_root" "$manifest_dir" "$mock_dir" "$fixture_tmp_dir" "$fixture_untrusted_tmp_dir"
printf 'APP_ENV=production\nREDIS_URL=redis://:fixture-only-password@porsche-redis:6379/0\nALLOWED_HOSTS=aiportcloud.com\nAUTH_TRUSTED_ORIGINS=https://aiportcloud.com\n' >"$backend_dir/.env"
printf 'APP_ENV=production\nREDIS_URL=redis://:fixture-only-password@porsche-redis:6379/0\nALLOWED_HOSTS=aiportcloud.com\nAUTH_TRUSTED_ORIGINS=https://aiportcloud.com\nROOT_BOOTSTRAP_USERNAME=env-root\n' >"$backend_dir/.env.root-username"
printf 'APP_ENV=production\nREDIS_URL=redis://:fixture-only-password@porsche-redis:6379/0\nALLOWED_HOSTS=aiportcloud.com\nAUTH_TRUSTED_ORIGINS=https://aiportcloud.com\nROOT_BOOTSTRAP_PASSWORD=env-password\n' >"$backend_dir/.env.root-password"
cp "$backend_dir/.env" "$backend_dir/.env.clean"
printf 'ROOT_BOOTSTRAP_USERNAME=\nROOT_BOOTSTRAP_PASSWORD=\n' >>"$backend_dir/.env.clean"
printf 'APP_ENV=production\nREDIS_URL=redis://:fixture-only-password@porsche-redis:6379/0\nALLOWED_HOSTS=aiportcloud.com\nAUTH_TRUSTED_ORIGINS=https://aiportcloud.com\nROOT_BOOTSTRAP_USERNAME=root_admin\n' >"$backend_dir/.env.deploy-root-username"
printf 'APP_ENV=production\nREDIS_URL=redis://:fixture-only-password@porsche-redis:6379/0\nALLOWED_HOSTS=aiportcloud.com\nAUTH_TRUSTED_ORIGINS=https://aiportcloud.com\nROOT_BOOTSTRAP_PASSWORD=Aa1@fixture-secret\n' >"$backend_dir/.env.deploy-root-password"
printf 'APP_ENV=production\n ROOT_BOOTSTRAP_USERNAME=root_admin\n' >"$backend_dir/.env.deploy-root-leading-whitespace"
printf 'APP_ENV=production\nexport ROOT_BOOTSTRAP_PASSWORD=Aa1@fixture-secret\n' >"$backend_dir/.env.deploy-root-export"
printf 'APP_ENV=production\nROOT_BOOTSTRAP_USERNAME = root_admin\n' >"$backend_dir/.env.deploy-root-spaced"
printf 'APP_ENV=production\nROOT_BOOTSTRAP_PASSWORD: Aa1@fixture-secret\n' >"$backend_dir/.env.deploy-root-colon"
printf 'APP_ENV=production\n# ROOT_BOOTSTRAP_USERNAME=root_admin\n' >"$backend_dir/.env.deploy-root-comment"
printf 'APP_ENV=production\nROOT_BOOTSTRAP_USERNAME=\nexport ROOT_BOOTSTRAP_USERNAME=root_admin\n' >"$backend_dir/.env.deploy-root-mixed-duplicate"
printf 'APP_ENV=production\n ROOT_BOOTSTRAP_USERNAME=env-root\n' >"$backend_dir/.env.root-leading-whitespace"
printf 'APP_ENV=production\nexport ROOT_BOOTSTRAP_PASSWORD=env-password\n' >"$backend_dir/.env.root-export"
printf 'APP_ENV=production\nROOT_BOOTSTRAP_USERNAME = env-root\n' >"$backend_dir/.env.root-spaced"
printf 'APP_ENV=production\nROOT_BOOTSTRAP_PASSWORD: env-password\n' >"$backend_dir/.env.root-colon"
printf 'APP_ENV=production\nROOT_BOOTSTRAP_USERNAME=\nexport ROOT_BOOTSTRAP_USERNAME=env-root\n' >"$backend_dir/.env.root-mixed-duplicate"
chmod 600 "$backend_dir"/.env*
printf 'package main\n\nfunc fixtureOverride() {}\n' >"$backend_dir/cmd/bootstrap-root/override.go"
printf 'malicious untracked build input\n' >"$backend_dir/vendor/fixture-malicious/payload"
printf '<!doctype html><title>fixture</title>\n' >"$frontend_dir/dist/index.html"
printf 'fixture-static\n' >"$frontend_root/index.html"
printf '' >"$fixture_dir/redis-password-empty"
printf 'too-short\n' >"$fixture_dir/redis-password-short"
printf 'fixture-only-password-that-is-longer-than-thirty-two-bytes\n' >"$fixture_dir/redis-password-valid"
printf 'username=root_admin\npassword=Aa1@fixture-secret\n' >"$fixture_dir/root-acceptance-credentials"
printf 'username root_admin\npassword=Aa1@fixture-secret\n' >"$fixture_dir/root-credentials-malformed"
printf 'username=root_admin\nusername=second\npassword=Aa1@fixture-secret\n' >"$fixture_dir/root-credentials-duplicate"
printf 'username=root_admin\npassword=Aa1@fixture-secret\nrole=root\n' >"$fixture_dir/root-credentials-unknown"
printf 'username=root_admin\n' >"$fixture_dir/root-credentials-missing-password"
printf 'password=Aa1@fixture-secret\n' >"$fixture_dir/root-credentials-missing-username"
printf 'username=\npassword=Aa1@fixture-secret\n' >"$fixture_dir/root-credentials-empty-username"
printf 'username=root_admin\npassword=\n' >"$fixture_dir/root-credentials-empty-password"
printf 'username= root_admin\npassword=Aa1@fixture-secret\n' >"$fixture_dir/root-credentials-leading-space"
printf 'username=root_admin\npassword=Aa1@fixture-secret \n' >"$fixture_dir/root-credentials-trailing-space"
printf 'username=root_admin\n\npassword=Aa1@fixture-secret\n' >"$fixture_dir/root-credentials-blank-line"
printf 'username=root_admin\npassword=Aa1@fixture-secret\n' >"$fixture_dir/root-credentials-valid-restore"
mkdir "$fixture_dir/root-credentials-directory"
for credential_fixture in "$fixture_dir"/root-acceptance-credentials "$fixture_dir"/root-credentials-*; do
    chmod 600 "$credential_fixture"
done
touch "$backend_dir/.git"

needs_container_fixture=0
for selected_check in "${selected_checks[@]}"; do
    if [[ "$selected_check" != docs ]]; then
        needs_container_fixture=1
        break
    fi
done
if (( needs_container_fixture )) && ! docker image inspect bash:5.2 >/dev/null 2>&1; then
    fail 'Docker and the bash:5.2 test image are required; refusing to run deployment entrypoints on the host'
fi

# This is a metadata-only regression probe, not an entrypoint root. It proves
# that an existing source-like .env is not rejected or read by this fixture.
if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_SOURCE_ENV_REGRESSION:-0}" == 1 ]]; then
    source_env_probe="$(mktemp -d "${TMPDIR:-/tmp}/auth-acceptance-source-env.XXXXXX")"
    printf 'fixture-only-source-env\n' >"$source_env_probe/.env"
    probe_before="$(env_state_for "$source_env_probe/.env")"
    [[ "$probe_before" == present:* ]] || fail 'source-env metadata probe did not observe .env'
    [[ "$(env_state_for "$source_env_probe/.env")" == "$probe_before" ]] || fail 'source-env metadata probe changed .env'
fi

# Existing target scripts are copied into the fake checkout. The fixture is the
# only host path mounted into the disposable container, so source-repository
# paths and files are unreachable while an entrypoint runs.
for check in bootstrap migration deploy rollback root-bootstrap; do
    script_name="$(script_for_check "$check")"
    source_script="$script_dir/$script_name"
    fixture_script="$backend_dir/deploy/$script_name"
    if [[ -x "$source_script" ]]; then
        cp "$source_script" "$fixture_script"
        chmod +x "$fixture_script"
    fi
done

assert_fixture_entrypoint() {
    local script_name="$1" fixture_script="$backend_dir/deploy/$1"
    [[ "$fixture_script" == "$backend_dir/deploy/"* ]] || fail "fixture entrypoint escapes fake backend: $fixture_script"
    [[ "$fixture_script" != "$source_repo/"* ]] || fail "fixture entrypoint uses source repository: $fixture_script"
    [[ "$backend_dir/.env" != "$source_repo/.env" ]] || fail 'fixture .env aliases source-repository .env'
    [[ -x "$fixture_script" ]] || fail "missing fixture entrypoint: $fixture_script"
}

mocked_commands=(docker git npm rsync nginx systemctl flock id curl sleep chown stat cp)

# NUL command log protocol: BEGIN/call-id, ARG/value pairs, END/call-id.
# Both spaces and the literal __END__ are ordinary argument values.
write_mock() {
    local command_name="$1"
    printf '%s\n' '#!/usr/bin/env bash' 'set -Eeuo pipefail' \
        'command_name="${0##*/}"' \
        'call_id="${BASHPID:-$$}-${RANDOM}"' \
        'lock_dir="${COMMAND_LOG}.lock"' \
        'until mkdir "$lock_dir" 2>/dev/null; do :; done' \
        'printf "BEGIN\\0%s\\0ARG\\0%s\\0" "$call_id" "$command_name" >>"$COMMAND_LOG"' \
        'for arg in "$@"; do printf "ARG\\0%s\\0" "$arg" >>"$COMMAND_LOG"; done' \
        'printf "END\\0%s\\0" "$call_id" >>"$COMMAND_LOG"' \
        'rmdir "$lock_dir"' \
        'case "$command_name" in' \
        '  id) [[ "${1:-}" == "-u" ]] && printf "%s\\n" "${MOCK_ID_UID:-0}" ;;' \
        '  stat) if [[ "${MOCK_ENTRYPOINT:-}" != auth-acceptance-bootstrap-root.sh ]]; then exec /bin/stat "$@"; fi; path="${4:-}"; case "$path" in /fixture/root-acceptance-credentials) stat_uid="${MOCK_CREDENTIAL_UID:-0}"; stat_mode="${MOCK_CREDENTIAL_MODE:-600}" ;; /fixture) stat_uid="${MOCK_CREDENTIAL_PARENT_UID:-0}"; stat_mode="${MOCK_CREDENTIAL_PARENT_MODE:-700}" ;; /fixture/Porsche/.env) stat_uid="${MOCK_ENV_UID:-0}"; stat_mode="${MOCK_ENV_MODE:-600}" ;; /fixture/Porsche) stat_uid="${MOCK_BACKEND_UID:-0}"; stat_mode="${MOCK_BACKEND_MODE:-755}" ;; /tmp/porsche-auth-root-bootstrap.*\/root-bootstrap) stat_uid="${MOCK_SNAPSHOT_CREDENTIAL_UID:-0}"; stat_mode="${MOCK_SNAPSHOT_CREDENTIAL_MODE:-600}" ;; /tmp/porsche-auth-root-bootstrap.*\/.env) stat_uid="${MOCK_SNAPSHOT_ENV_UID:-0}"; stat_mode="${MOCK_SNAPSHOT_ENV_MODE:-600}" ;; *) exit 78 ;; esac; case "${1:-}:${2:-}" in "-c:%u") printf "%s\\n" "$stat_uid" ;; "-c:%a") printf "%s\\n" "$stat_mode" ;; *) exit 78 ;; esac ;;' \
        '  cp) if [[ "${MOCK_ENTRYPOINT:-}" == auth-acceptance-bootstrap-root.sh ]]; then [[ $# == 5 && "$1" == --preserve=mode,ownership && "$2" == --no-dereference && "$3" == -- ]] || exit 80; exec /bin/cp -pP -- "$4" "$5"; else exec /bin/cp "$@"; fi ;;' \
        '  git) case "${1:-}" in fetch) if [[ "${MOCK_ENV_MUTATE_ON_GIT_FETCH:-0}" == 1 ]]; then printf "APP_ENV=production\\nREDIS_URL=redis://:fixture-only-password@porsche-redis:6379/0\\nALLOWED_HOSTS=aiportcloud.com\\nAUTH_TRUSTED_ORIGINS=https://aiportcloud.com\\nROOT_BOOTSTRAP_PASSWORD=Aa1@fixture-secret\\n" >/fixture/Porsche/.env; fi ;; branch) if [[ "$PWD" == */Porsche-Web ]]; then printf "%s\\n" "${MOCK_FRONTEND_BRANCH:-feature/session-auth-frontend}"; else printf "%s\\n" "${MOCK_BRANCH:-feature/user-registration-management}"; fi ;; rev-parse) if [[ "${2:-}" == origin/* && "${MOCK_REMOTE_MISMATCH:-0}" == 1 ]]; then printf "remote-sha\\n"; else printf "%s\\n" "${MOCK_GIT_SHA:-fixture-sha}"; fi ;; status) [[ "${MOCK_GIT_STATUS_FAILURE:-0}" == 0 ]] || exit 79; [[ "${MOCK_GIT_DIRTY:-0}" == 0 ]] || printf " M tracked-fixture\\n" ;; diff) [[ "${MOCK_GIT_DIRTY:-0}" == 0 ]] ;; archive) : ;; esac ;;' \
        '  docker) case "${1:-}" in network) if [[ "${2:-}" == inspect && "${3:-}" == porsche-app ]]; then [[ "${MOCK_NETWORK_INSPECT_RESULT:-success}" == success ]]; else [[ "${MOCK_DOCKER_INSPECT_RESULT:-success}" == success ]]; fi ;; container) if [[ "${2:-}" == inspect && "${3:-}" == porsche-redis ]]; then [[ "${MOCK_REDIS_EXISTS:-0}" == 1 ]]; elif [[ "${2:-}" == inspect && "${3:-}" == porsche-mysql ]]; then [[ "${MOCK_MYSQL_EXISTS:-1}" == 1 && "${MOCK_MYSQL_INSPECT_RESULT:-success}" == success ]]; else [[ "${MOCK_DOCKER_INSPECT_RESULT:-success}" == success ]]; fi ;; build) [[ "${2:-}" != --quiet ]] || printf "%s\\n" "${MOCK_DOCKER_BUILD_IMAGE_ID:-}" ;; run) [[ "${MOCK_DOCKER_RUN_RESULT:-success}" == success ]] || exit 71; if [[ "${2:-}" == --rm && "${3:-}" == --entrypoint && "${4:-}" == id && "${5:-}" == redis:7-alpine && "${6:-}" == -u && "${7:-}" == redis ]]; then printf "999\\n"; elif [[ "${2:-}" == --rm && "${3:-}" == --entrypoint && "${4:-}" == id && "${5:-}" == redis:7-alpine && "${6:-}" == -g && "${7:-}" == redis ]]; then printf "1000\\n"; else printf "fixture-container\\n"; fi ;; esac ;;' \
        '  curl) [[ "${MOCK_HEALTH_RESULT:-success}" == success ]] || exit 72 ;;' \
        '  npm) [[ "${MOCK_NPM_RESULT:-success}" == success ]] || exit 73 ;;' \
        '  rsync) [[ "${MOCK_RSYNC_RESULT:-success}" == success ]] || exit 74 ;;' \
        '  nginx) [[ "${MOCK_NGINX_RESULT:-success}" == success ]] || exit 75 ;;' \
        '  systemctl) [[ "${MOCK_SYSTEMCTL_RESULT:-success}" == success ]] || exit 76 ;;' \
        '  flock) if [[ "${MOCK_ENV_MUTATE_ON_FLOCK:-0}" == 1 ]]; then printf "APP_ENV=production\\nREDIS_URL=redis://:fixture-only-password@porsche-redis:6379/0\\nALLOWED_HOSTS=aiportcloud.com\\nAUTH_TRUSTED_ORIGINS=https://aiportcloud.com\\nROOT_BOOTSTRAP_PASSWORD=Aa1@fixture-secret\\n" >/fixture/Porsche/.env; fi; [[ "${MOCK_FLOCK_RESULT:-success}" == success ]] || exit 77 ;;' \
        'esac' >"$mock_dir/$command_name"
    chmod +x "$mock_dir/$command_name"
}
for command_name in "${mocked_commands[@]}"; do write_mock "$command_name"; done

parse_calls() {
    call_starts=() call_lengths=() call_ids=() call_argv=()
    local marker value call_id='' call_start=0 call_length=0 in_call=0
    while IFS= read -r -d '' marker; do
        case "$marker" in
            BEGIN)
                (( in_call == 0 )) || fail 'nested BEGIN in mock command log'
                IFS= read -r -d '' call_id || fail 'truncated BEGIN call id'
                call_start="${#call_argv[@]}"; call_length=0; in_call=1
                ;;
            ARG)
                (( in_call == 1 )) || fail 'ARG outside mock call'
                IFS= read -r -d '' value || fail 'truncated ARG value'
                call_argv+=("$value"); ((call_length += 1))
                ;;
            END)
                (( in_call == 1 )) || fail 'END outside mock call'
                IFS= read -r -d '' value || fail 'truncated END call id'
                [[ "$value" == "$call_id" ]] || fail 'mismatched END call id'
                call_ids+=("$call_id"); call_starts+=("$call_start"); call_lengths+=("$call_length"); in_call=0
                ;;
            *) fail "unknown mock log marker: $marker" ;;
        esac
    done <"$command_log"
    (( in_call == 0 )) || fail 'unterminated mock call'
}

call_has_prefix() {
    local call_index="$1" start length offset expected
    shift
    start="${call_starts[$call_index]}"; length="${call_lengths[$call_index]}"
    (( $# <= length )) || return 1
    offset=0
    for expected in "$@"; do
        [[ "${call_argv[$((start + offset))]}" == "$expected" ]] || return 1
        ((offset += 1))
    done
}

call_has_token() {
    local call_index="$1" expected="$2" start length offset
    start="${call_starts[$call_index]}"; length="${call_lengths[$call_index]}"
    for ((offset = 0; offset < length; offset += 1)); do
        [[ "${call_argv[$((start + offset))]}" == "$expected" ]] && return 0
    done
    return 1
}

call_has_sequence() {
    local call_index="$1" first="$2" second="$3" start length offset
    start="${call_starts[$call_index]}"; length="${call_lengths[$call_index]}"
    for ((offset = 0; offset + 1 < length; offset += 1)); do
        [[ "${call_argv[$((start + offset))]}" == "$first" && "${call_argv[$((start + offset + 1))]}" == "$second" ]] && return 0
    done
    return 1
}

call_command_basename() {
    local call_index="$1" start
    start="${call_starts[$call_index]}"
    printf '%s\n' "${call_argv[$start]##*/}"
}

call_is_dangerous() {
    local call_index="$1" command
    command="$(call_command_basename "$call_index")"
    [[ "$command" == mysql || "$command" == mariadb ]] && return 0
    [[ "$command" == docker || "$command" == docker-compose ]] || return 1
    call_has_token "$call_index" down && return 0
    call_has_sequence "$call_index" volume rm && return 0
    call_has_sequence "$call_index" network rm && return 0
    call_has_token "$call_index" prune && return 0
    return 1
}

assert_no_dangerous_calls() {
    local call_index
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_is_dangerous "$call_index" && fail "dangerous mocked invocation at call $call_index"
    done
    return 0
}

docker_call_writes() {
    local call_index="$1" command start length offset token
    command="$(call_command_basename "$call_index")"
    [[ "$command" == docker || "$command" == docker-compose ]] || return 1
    start="${call_starts[$call_index]}"; length="${call_lengths[$call_index]}"
    for ((offset = 1; offset < length; offset += 1)); do
        token="${call_argv[$((start + offset))]}"
        case "$token" in build|run|create|up|down|start|stop|restart|kill|pause|unpause|rename|rm|prune) return 0 ;; esac
    done
    return 1
}

assert_no_docker_or_rsync_writes() {
    local call_index start command
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        docker_call_writes "$call_index" && fail "unexpected Docker write after rejection at call $call_index"
        command="$(call_command_basename "$call_index")"
        [[ "$command" != rsync ]] || fail "unexpected rsync write after rejection at call $call_index"
    done
    return 0
}

assert_no_deploy_preparation_calls() {
    local call_index command
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        command="$(call_command_basename "$call_index")"
        [[ "$command" != npm && "$command" != nginx ]] || fail "unexpected deployment preparation invocation at call $call_index"
    done
    return 0
}

assert_no_git_fetch_calls() {
    local call_index
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_has_prefix "$call_index" git fetch && fail "unexpected git fetch after rejection at call $call_index"
    done
    return 0
}

require_deploy_snapshot_env_file() {
    local call_index start length offset env_file
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_has_prefix "$call_index" docker run -d --name ai-gateway-go || continue
        start="${call_starts[$call_index]}"; length="${call_lengths[$call_index]}"
        for ((offset = 0; offset + 1 < length; offset += 1)); do
            [[ "${call_argv[$((start + offset))]}" == --env-file ]] || continue
            env_file="${call_argv[$((start + offset + 1))]}"
            [[ "$env_file" != /fixture/untrusted/porsche-auth-env.* ]] || fail 'application environment snapshot used caller-controlled TMPDIR'
            [[ "$env_file" =~ ^/tmp/porsche-auth-env\.[A-Za-z0-9]+/\.env$ ]] || continue
            [[ "$env_file" != /fixture/Porsche/.env ]] || continue
            return 0
        done
    done
    fail 'missing application Docker run with a private environment snapshot'
}

assert_env_snapshots_cleaned_up() {
    if find "$fixture_tmp_dir" -mindepth 1 -maxdepth 1 -name 'porsche-auth-env.*' -print -quit | grep -q .; then
        fail 'application environment snapshot was not cleaned up'
    fi
}

require_call() {
    local call_index
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_has_prefix "$call_index" "$@" && return 0
    done
    fail "missing expected mock invocation: $*"
}

require_exact_call() {
    local call_index
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        [[ "${call_lengths[$call_index]}" == "$#" ]] || continue
        call_has_prefix "$call_index" "$@" && return 0
    done
    fail "missing expected exact mock invocation"
}

assert_no_call_token() {
    local forbidden="$1" call_index
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_has_token "$call_index" "$forbidden" && fail "forbidden mocked invocation option: $forbidden"
    done
    return 0
}

assert_no_sensitive_argv_fields() {
    local field sought field_index
    parse_calls
    for ((field_index = 0; field_index < ${#call_argv[@]}; field_index += 1)); do
        field="${call_argv[$field_index]}"
        for sought in "$@"; do
            [[ "$field" != *"$sought"* ]] || return 1
        done
    done
    return 0
}

assert_no_docker_run() {
    local call_index
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_has_prefix "$call_index" docker run && fail 'unexpected Docker run invocation'
    done
    return 0
}

require_root_bootstrap_snapshot_run() {
    local call_index start env_mount credential_mount env_snapshot_dir credential_snapshot_dir
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_has_prefix "$call_index" docker run --rm --network porsche-app || continue
        [[ "${call_lengths[$call_index]}" == 13 ]] || continue
        start="${call_starts[$call_index]}"
        [[ "${call_argv[$((start + 5))]}" == --mount && "${call_argv[$((start + 7))]}" == --mount ]] || continue
        env_mount="${call_argv[$((start + 6))]}"
        credential_mount="${call_argv[$((start + 8))]}"
        [[ "$env_mount" =~ ^type=bind,src=(/tmp/porsche-auth-root-bootstrap\.[^,]+)/\.env,dst=/app/\.env,readonly$ ]] || continue
        env_snapshot_dir="${BASH_REMATCH[1]}"
        [[ "$credential_mount" =~ ^type=bind,src=(/tmp/porsche-auth-root-bootstrap\.[^,]+)/root-bootstrap,dst=/run/secrets/root-bootstrap,readonly$ ]] || continue
        credential_snapshot_dir="${BASH_REMATCH[1]}"
        [[ "$env_snapshot_dir" == "$credential_snapshot_dir" ]] || continue
        [[ "$env_mount" != *'/fixture/Porsche/.env'* && "$credential_mount" != *'/fixture/root-acceptance-credentials'* ]] || continue
        [[ "${call_argv[$((start + 9))]}" == "$fixture_image_id" ]] || continue
        [[ "${call_argv[$((start + 10))]}" == /app/bootstrap-root ]] || continue
        [[ "${call_argv[$((start + 11))]}" == --credentials-file ]] || continue
        [[ "${call_argv[$((start + 12))]}" == /run/secrets/root-bootstrap ]] || continue
        return 0
    done
    fail 'missing root bootstrap run with private snapshot mounts and immutable image id'
}

require_root_bootstrap_snapshot_copies() {
    local call_index start source destination env_snapshot_dir='' credential_snapshot_dir=''
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        [[ "${call_lengths[$call_index]}" == 6 ]] || continue
        call_has_prefix "$call_index" cp --preserve=mode,ownership --no-dereference -- || continue
        start="${call_starts[$call_index]}"
        source="${call_argv[$((start + 4))]}"
        destination="${call_argv[$((start + 5))]}"
        case "$source" in
            /fixture/Porsche/.env)
                [[ "$destination" =~ ^(/tmp/porsche-auth-root-bootstrap\.[^/]+)/\.env$ ]] || continue
                env_snapshot_dir="${BASH_REMATCH[1]}"
                ;;
            /fixture/root-acceptance-credentials)
                [[ "$destination" =~ ^(/tmp/porsche-auth-root-bootstrap\.[^/]+)/root-bootstrap$ ]] || continue
                credential_snapshot_dir="${BASH_REMATCH[1]}"
                ;;
        esac
    done
    [[ -n "$env_snapshot_dir" && "$env_snapshot_dir" == "$credential_snapshot_dir" ]] || fail 'missing exact preserved copies into one private snapshot'
}

require_candidate_rollback_renames() {
    local call_index start target saw_save=0 saw_restore=0
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        start="${call_starts[$call_index]}"
        if call_has_prefix "$call_index" docker rename -- ai-gateway-go; then
            target="${call_argv[$((start + 4))]:-}"
            [[ "$target" =~ ^ai-gateway-go-acceptance-rollback-[0-9]+$ ]] && saw_save=1
        elif [[ "${call_lengths[$call_index]}" -ge 5 && "${call_argv[$start]}" == docker && "${call_argv[$((start + 1))]}" == rename && "${call_argv[$((start + 2))]}" == -- && "${call_argv[$((start + 3))]}" =~ ^ai-gateway-go-acceptance-rollback-[0-9]+$ && "${call_argv[$((start + 4))]}" == ai-gateway-go ]]; then
            saw_restore=1
        fi
    done
    (( saw_save == 1 && saw_restore == 1 )) || fail 'candidate failure did not save and restore the old application container'
}

call_index_with_prefix() {
    local call_index
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_has_prefix "$call_index" "$@" && { printf '%s\n' "$call_index"; return 0; }
    done
    return 1
}

assert_before() {
    local first second split=0 args=() first_args=() second_args=()
    for arg in "$@"; do
        if [[ "$arg" == ::: ]]; then split=1; continue; fi
        (( split )) && second_args+=("$arg") || first_args+=("$arg")
    done
    first="$(call_index_with_prefix "${first_args[@]}")" || fail "missing ordered call: ${first_args[*]}"
    second="$(call_index_with_prefix "${second_args[@]}")" || fail "missing ordered call: ${second_args[*]}"
    (( first < second )) || fail "wrong call order: ${first_args[*]} must precede ${second_args[*]}"
}

assert_log_protocol_preserves_argv() {
    : >"$command_log"
    COMMAND_LOG="$command_log" "$mock_dir/docker" run --label 'a b' '__END__' >/dev/null
    parse_calls
    [[ "${#call_starts[@]}" == 1 ]] || fail 'mock protocol did not produce one call'
    call_has_prefix 0 docker run --label 'a b' '__END__' || fail 'mock protocol flattened or terminated an argv value'
    [[ "${call_lengths[0]}" == 5 ]] || fail 'mock protocol lost argv boundaries'
}

assert_parser_detects_absolute_docker() {
    : >"$command_log"
    printf 'BEGIN\0synthetic\0ARG\0/usr/bin/docker\0ARG\0volume\0ARG\0rm\0ARG\0fixture\0END\0synthetic\0' >"$command_log"
    parse_calls
    call_is_dangerous 0 || fail 'parser did not flag absolute docker command'
}

assert_parser_detects_optioned_dangerous_calls() {
    : >"$command_log"
    printf 'BEGIN\0one\0ARG\0docker\0ARG\0--context\0ARG\0fixture\0ARG\0compose\0ARG\0down\0END\0one\0' >>"$command_log"
    printf 'BEGIN\0two\0ARG\0docker-compose\0ARG\0-p\0ARG\0fixture\0ARG\0down\0END\0two\0' >>"$command_log"
    printf 'BEGIN\0three\0ARG\0docker\0ARG\0--context\0ARG\0fixture\0ARG\0volume\0ARG\0rm\0END\0three\0' >>"$command_log"
    printf 'BEGIN\0four\0ARG\0docker\0ARG\0--context\0ARG\0fixture\0ARG\0prune\0END\0four\0' >>"$command_log"
    printf 'BEGIN\0five\0ARG\0mysql\0ARG\0-e\0ARG\0SELECT 1\0END\0five\0' >>"$command_log"
    parse_calls
    local call_index
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_is_dangerous "$call_index" || fail "parser missed optioned dangerous call $call_index"
    done
}

assert_parser_detects_optioned_write_calls() {
    : >"$command_log"
    printf 'BEGIN\0one\0ARG\0docker\0ARG\0--context\0ARG\0fixture\0ARG\0compose\0ARG\0up\0END\0one\0' >>"$command_log"
    printf 'BEGIN\0two\0ARG\0docker-compose\0ARG\0-p\0ARG\0fixture\0ARG\0down\0END\0two\0' >>"$command_log"
    printf 'BEGIN\0three\0ARG\0/usr/local/bin/rsync\0ARG\0--archive\0END\0three\0' >>"$command_log"
    parse_calls
    docker_call_writes 0 || fail 'write detector missed optioned docker compose'
    docker_call_writes 1 || fail 'write detector missed optioned docker-compose'
    [[ "$(call_command_basename 2)" == rsync ]] || fail 'write detector missed absolute rsync basename'
}

assert_structured_sensitive_argv_scan_contract() {
    : >"$command_log"
    printf 'BEGIN\0safe\0ARG\0docker\0ARG\0run\0ARG\0ordinary-value\0END\0safe\0' >"$command_log"
    assert_no_sensitive_argv_fields sensitive-probe
    : >"$command_log"
    printf 'BEGIN\0unsafe\0ARG\0docker\0ARG\0run\0ARG\0prefix-sensitive-probe-suffix\0END\0unsafe\0' >"$command_log"
    if assert_no_sensitive_argv_fields sensitive-probe; then
        fail 'structured argv scanner accepted a sensitive field'
    fi
}

assert_log_protocol_handles_pipeline_concurrency() {
    : >"$command_log"
    COMMAND_LOG="$command_log" "$mock_dir/git" archive --format=tar fixture-sha \
        | COMMAND_LOG="$command_log" MOCK_DOCKER_BUILD_IMAGE_ID="$fixture_image_id" "$mock_dir/docker" build --quiet --tag fixture - >/dev/null
    parse_calls
    require_exact_call git archive --format=tar fixture-sha
    require_exact_call docker build --quiet --tag fixture -
}

assert_log_protocol_preserves_argv
assert_parser_detects_absolute_docker
assert_parser_detects_optioned_dangerous_calls
assert_parser_detects_optioned_write_calls
assert_structured_sensitive_argv_scan_contract
assert_log_protocol_handles_pipeline_concurrency

assert_container_blocks_host_command_bypasses() {
    local host_probe="$source_repo/.auth-acceptance-host-probe"
    [[ ! -e "$host_probe" ]] || fail "host isolation probe already exists: $host_probe"
    : >"$command_log"
    printf '%s\n' '#!/usr/bin/env bash' "touch '$host_probe'" >"$fixture_dir/helper.sh"
    printf '%s\n' '#!/usr/bin/env bash' 'set +e' \
        '/opt/homebrew/bin/docker run fixture' \
        'command /custom/bin/rsync --archive /fixture /host' \
        'bash /fixture/helper.sh' \
        'exit 0' >"$fixture_dir/bypass-target.sh"
    chmod +x "$fixture_dir/helper.sh" "$fixture_dir/bypass-target.sh"
    docker run --rm --network none --read-only --cap-drop ALL \
        --security-opt no-new-privileges --tmpfs /tmp:rw,noexec,nosuid,nodev \
        --mount "type=bind,src=$fixture_dir,dst=/fixture" \
        bash:5.2 /fixture/bypass-target.sh >"$fixture_dir/bypass.stdout" 2>"$fixture_dir/bypass.stderr"
    [[ ! -e "$host_probe" ]] || fail 'containerized bypass target changed a host path outside the fixture'
    [[ ! -s "$command_log" ]] || fail 'containerized bypass target reached a fixture command mock unexpectedly'
}

if (( needs_container_fixture )); then
    assert_container_blocks_host_command_bypasses
fi

# A missing production script must now be reported at the fake checkout path.
for selected_check in "${selected_checks[@]}"; do
    [[ "$selected_check" == docs ]] && continue
    assert_fixture_entrypoint "$(script_for_check "$selected_check")"
done

run_entrypoint() {
    local entrypoint="$1" tmpdir_value=/tmp tmp_mount_option=--tmpfs tmp_mount_value=/tmp:rw,noexec,nosuid,nodev
    shift
    assert_fixture_entrypoint "$entrypoint"
    if [[ "$entrypoint" == auth-acceptance-deploy.sh ]]; then
        tmpdir_value=/fixture/untrusted
        tmp_mount_option=--mount
        tmp_mount_value="type=bind,src=$fixture_tmp_dir,dst=/tmp"
    fi
    ((run_count += 1))
    command_log="$fixture_dir/commands-$run_count.nul"
    : >"$command_log"
    docker run --rm --network none --read-only --cap-drop ALL \
        --security-opt no-new-privileges "$tmp_mount_option" "$tmp_mount_value" \
        --mount "type=bind,src=$fixture_dir,dst=/fixture" \
        --env PATH=/fixture/bin:/usr/local/bin:/usr/bin:/bin \
        --env "COMMAND_LOG=/fixture/commands-$run_count.nul" \
        --env "MOCK_ENTRYPOINT=$entrypoint" \
        --env PORSCHE_AUTH_ACCEPTANCE_TEST_MODE=1 \
        --env "MOCK_ID_UID=${MOCK_ID_UID:-0}" \
        --env PORSCHE_AUTH_ACCEPTANCE_BACKEND_DIR=/fixture/Porsche \
        --env PORSCHE_AUTH_ACCEPTANCE_ROOT_CREDENTIALS_FILE=/fixture/root-acceptance-credentials \
        --env PORSCHE_AUTH_ACCEPTANCE_FRONTEND_DIR=/fixture/Porsche-Web \
        --env PORSCHE_AUTH_ACCEPTANCE_FRONTEND_ROOT=/fixture/www/porsche-web \
        --env PORSCHE_AUTH_ACCEPTANCE_MANIFEST_DIR=/fixture/manifests \
        --env "TMPDIR=$tmpdir_value" \
        --env PORSCHE_AUTH_ACCEPTANCE_REDIS_CONFIG_DIR=/fixture/redis-config \
        --env PORSCHE_AUTH_ACCEPTANCE_LOCK_FILE=/fixture/auth-acceptance.lock \
        --env "PORSCHE_AUTH_ACCEPTANCE_TEST_PASSWORD_FILE=/fixture/${MOCK_PASSWORD_FILE_NAME:-redis-password-valid}" \
        --env "MOCK_BRANCH=${MOCK_BRANCH:-feature/user-registration-management}" \
        --env "MOCK_FRONTEND_BRANCH=${MOCK_FRONTEND_BRANCH:-feature/session-auth-frontend}" \
        --env "MOCK_REMOTE_MISMATCH=${MOCK_REMOTE_MISMATCH:-0}" \
        --env "MOCK_GIT_SHA=${MOCK_GIT_SHA:-fixture-sha}" \
        --env "MOCK_GIT_DIRTY=${MOCK_GIT_DIRTY:-0}" \
        --env "MOCK_GIT_STATUS_FAILURE=${MOCK_GIT_STATUS_FAILURE:-0}" \
        --env "MOCK_ENV_MUTATE_ON_FLOCK=${MOCK_ENV_MUTATE_ON_FLOCK:-0}" \
        --env "MOCK_ENV_MUTATE_ON_GIT_FETCH=${MOCK_ENV_MUTATE_ON_GIT_FETCH:-0}" \
        --env "MOCK_CREDENTIAL_UID=${MOCK_CREDENTIAL_UID:-0}" \
        --env "MOCK_CREDENTIAL_MODE=${MOCK_CREDENTIAL_MODE:-600}" \
        --env "MOCK_CREDENTIAL_PARENT_UID=${MOCK_CREDENTIAL_PARENT_UID:-0}" \
        --env "MOCK_CREDENTIAL_PARENT_MODE=${MOCK_CREDENTIAL_PARENT_MODE:-700}" \
        --env "MOCK_BACKEND_UID=${MOCK_BACKEND_UID:-0}" \
        --env "MOCK_BACKEND_MODE=${MOCK_BACKEND_MODE:-755}" \
        --env "MOCK_ENV_UID=${MOCK_ENV_UID:-0}" \
        --env "MOCK_ENV_MODE=${MOCK_ENV_MODE:-600}" \
        --env "MOCK_SNAPSHOT_CREDENTIAL_UID=${MOCK_SNAPSHOT_CREDENTIAL_UID:-0}" \
        --env "MOCK_SNAPSHOT_CREDENTIAL_MODE=${MOCK_SNAPSHOT_CREDENTIAL_MODE:-600}" \
        --env "MOCK_SNAPSHOT_ENV_UID=${MOCK_SNAPSHOT_ENV_UID:-0}" \
        --env "MOCK_SNAPSHOT_ENV_MODE=${MOCK_SNAPSHOT_ENV_MODE:-600}" \
        --env "MOCK_NETWORK_INSPECT_RESULT=${MOCK_NETWORK_INSPECT_RESULT:-success}" \
        --env "MOCK_MYSQL_EXISTS=${MOCK_MYSQL_EXISTS:-1}" \
        --env "MOCK_MYSQL_INSPECT_RESULT=${MOCK_MYSQL_INSPECT_RESULT:-success}" \
        --env "MOCK_DOCKER_INSPECT_RESULT=${MOCK_DOCKER_INSPECT_RESULT:-success}" \
        --env "MOCK_DOCKER_BUILD_IMAGE_ID=${MOCK_DOCKER_BUILD_IMAGE_ID:-$fixture_image_id}" \
        --env "MOCK_REDIS_EXISTS=${MOCK_REDIS_EXISTS:-0}" \
        --env "MOCK_DOCKER_RUN_RESULT=${MOCK_DOCKER_RUN_RESULT:-success}" \
        --env "MOCK_HEALTH_RESULT=${MOCK_HEALTH_RESULT:-success}" \
        --env "MOCK_NPM_RESULT=${MOCK_NPM_RESULT:-success}" \
        --env "MOCK_RSYNC_RESULT=${MOCK_RSYNC_RESULT:-success}" \
        --env "MOCK_NGINX_RESULT=${MOCK_NGINX_RESULT:-success}" \
        --env "MOCK_SYSTEMCTL_RESULT=${MOCK_SYSTEMCTL_RESULT:-success}" \
        --env "MOCK_FLOCK_RESULT=${MOCK_FLOCK_RESULT:-success}" \
        bash:5.2 "/fixture/Porsche/deploy/$entrypoint" "$@"
}

wait_for_fixture_file() {
    local host_path="$1" container_path="$2" expected attempt
    expected="$(cksum <"$host_path")"
    for attempt in $(seq 1 10); do
        if docker run --rm --network none --read-only --cap-drop ALL \
            --security-opt no-new-privileges --tmpfs /tmp:rw,noexec,nosuid,nodev \
            --mount "type=bind,src=$fixture_dir,dst=/fixture" \
            bash:5.2 sh -c 'actual="$(cksum <"$1")"; [ "$actual" = "$2" ]' sh "$container_path" "$expected" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.2
    done
    fail 'isolated fixture did not observe an updated test input'
}

wait_for_fixture_path_state() {
    local state="$1" container_path="$2" attempt
    for attempt in $(seq 1 10); do
        if docker run --rm --network none --read-only --cap-drop ALL \
            --security-opt no-new-privileges --tmpfs /tmp:rw,noexec,nosuid,nodev \
            --mount "type=bind,src=$fixture_dir,dst=/fixture" \
            bash:5.2 sh -c 'case "$1" in missing) [ ! -e "$2" ] && [ ! -L "$2" ] ;; directory) [ -d "$2" ] ;; symlink) [ -L "$2" ] ;; *) exit 2 ;; esac' sh "$state" "$container_path" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.2
    done
    fail 'isolated fixture did not observe an updated path state'
}

run_bootstrap() { run_entrypoint bootstrap-auth-redis.sh; }
run_migration() {
    run_entrypoint auth-acceptance-migrate.sh --confirm-auth-schema-migration
    assert_no_dangerous_calls
}
assert_migration_requires_confirmation_without_writes() {
    if run_entrypoint auth-acceptance-migrate.sh; then fail 'migration accepted no confirmation'; fi
    assert_no_docker_or_rsync_writes
    if run_entrypoint auth-acceptance-migrate.sh --wrong-confirmation; then fail 'migration accepted wrong confirmation'; fi
    assert_no_docker_or_rsync_writes
}
run_deploy() { MOCK_REDIS_EXISTS=1 run_entrypoint auth-acceptance-deploy.sh; }
run_deploy_with_captured_output() {
    local stdout_file="$fixture_dir/deploy-root-env.stdout" stderr_file="$fixture_dir/deploy-root-env.stderr" status
    : >"$stdout_file"
    : >"$stderr_file"
    if MOCK_REDIS_EXISTS=1 run_entrypoint auth-acceptance-deploy.sh >"$stdout_file" 2>"$stderr_file"; then
        status=0
    else
        status=$?
    fi
    return "$status"
}
run_root_bootstrap() {
    local stdout_file="$fixture_dir/root-bootstrap.stdout" stderr_file="$fixture_dir/root-bootstrap.stderr" status
    : >"$stdout_file"
    : >"$stderr_file"
    if run_entrypoint auth-acceptance-bootstrap-root.sh --confirm-auth-root-bootstrap >"$stdout_file" 2>"$stderr_file"; then
        status=0
    else
        status=$?
    fi
    assert_root_secret_absent
    return "$status"
}
run_rollback() {
    if [[ ! -f "$manifest_dir/rollback.env" ]]; then
        run_deploy
    fi
    run_entrypoint auth-acceptance-rollback.sh --confirm-auth-acceptance-rollback
    assert_no_dangerous_calls
}

assert_bootstrap_creates_internal_redis() {
    run_bootstrap
    require_call docker run --rm --entrypoint id redis:7-alpine -u redis
    require_call docker run --rm --entrypoint id redis:7-alpine -g redis
    require_call chown 999:1000 /fixture/redis-config/redis.conf
    require_call docker volume create porsche-redis-data
    require_call docker run -d --name porsche-redis --restart unless-stopped --network porsche-app
    assert_no_dangerous_calls
}

assert_bootstrap_rejects_invalid_passwords_and_existing_container() {
    if MOCK_PASSWORD_FILE_NAME=redis-password-empty run_bootstrap; then fail 'bootstrap accepted an empty Redis password'; fi
    assert_no_docker_or_rsync_writes
    if MOCK_PASSWORD_FILE_NAME=redis-password-short run_bootstrap; then fail 'bootstrap accepted a short Redis password'; fi
    assert_no_docker_or_rsync_writes
    if MOCK_REDIS_EXISTS=1 run_bootstrap; then fail 'bootstrap accepted an existing Redis container'; fi
    assert_no_docker_or_rsync_writes
}

assert_deploy_refuses_main_or_dirty_checkout_without_writes() {
    if MOCK_BRANCH=main run_deploy; then fail 'deployment accepted main'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_GIT_DIRTY=1 run_deploy; then fail 'deployment accepted dirty checkout'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
}

assert_candidate_failure_restores_old_application() {
    if MOCK_HEALTH_RESULT=failure run_deploy; then fail 'candidate unexpectedly healthy'; fi
    require_call docker rm -f -- ai-gateway-go
    require_candidate_rollback_renames
    require_call docker start -- ai-gateway-go
    assert_no_dangerous_calls
}

assert_successful_deploy_order_and_manifest() {
    run_deploy
    require_deploy_snapshot_env_file
    assert_env_snapshots_cleaned_up
    assert_before npm run build ::: docker build --tag ai-gateway-go:auth-acceptance
    assert_before nginx -t ::: docker stop -- ai-gateway-go
    assert_before docker run -d --name ai-gateway-go ::: rsync --archive --delete --delay-updates
    assert_before rsync --archive --delete --delay-updates ::: systemctl reload nginx
    [[ -f "$manifest_dir/rollback.env" ]] || fail 'successful deployment did not create rollback manifest'
    ! grep -Eq 'fixture-only-password|REDIS_URL|DATABASE_URL|JWT|SECRET' "$manifest_dir/rollback.env" || fail 'rollback manifest contains a secret value or key'
}

restore_clean_deploy_env() {
    cp "$backend_dir/.env.clean" "$backend_dir/.env"
    wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
}

assert_deploy_rejects_root_bootstrap_snapshot_race_without_writes() {
    local stdout_file="$fixture_dir/deploy-root-env.stdout" stderr_file="$fixture_dir/deploy-root-env.stderr"
    restore_clean_deploy_env
    rm -f -- "$fixture_lock_file"
    wait_for_fixture_path_state missing /fixture/auth-acceptance.lock
    if MOCK_ENV_MUTATE_ON_FLOCK=1 run_deploy_with_captured_output; then
        restore_clean_deploy_env
        fail 'deployment accepted a Root bootstrap declaration introduced after flock'
    fi
    grep -Fq ROOT_BOOTSTRAP_PASSWORD "$stderr_file" || {
        restore_clean_deploy_env
        fail 'snapshot rejection did not identify the Root bootstrap key'
    }
    assert_no_sensitive_argv_fields 'Aa1@fixture-secret' 'root_admin' || {
        restore_clean_deploy_env
        fail 'Root bootstrap value appeared in a structured argv field'
    }
    ! grep -Fq 'Aa1@fixture-secret' "$stdout_file" "$stderr_file" || {
        restore_clean_deploy_env
        fail 'Root bootstrap secret appeared in deployment output'
    }
    ! grep -Fq 'root_admin' "$stdout_file" "$stderr_file" || {
        restore_clean_deploy_env
        fail 'Root bootstrap username appeared in deployment output'
    }
    assert_no_git_fetch_calls
    assert_no_deploy_preparation_calls
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    assert_env_snapshots_cleaned_up
    restore_clean_deploy_env
}

assert_deploy_uses_snapshot_after_source_env_mutates() {
    restore_clean_deploy_env
    if ! MOCK_ENV_MUTATE_ON_GIT_FETCH=1 run_deploy; then
        restore_clean_deploy_env
        fail 'deployment did not retain the environment snapshot after source mutation'
    fi
    require_deploy_snapshot_env_file
    assert_no_sensitive_argv_fields 'Aa1@fixture-secret' 'root_admin' || {
        restore_clean_deploy_env
        fail 'post-snapshot Root bootstrap value appeared in a structured argv field'
    }
    assert_env_snapshots_cleaned_up
    restore_clean_deploy_env
}

assert_deploy_preflight_failures_do_not_write() {
    if MOCK_REMOTE_MISMATCH=1 run_deploy; then fail 'deployment accepted remote SHA mismatch'; fi
    assert_no_docker_or_rsync_writes
    if MOCK_REDIS_EXISTS=0 run_entrypoint auth-acceptance-deploy.sh; then fail 'deployment accepted missing Redis'; fi
    assert_no_docker_or_rsync_writes
    if MOCK_NGINX_RESULT=failure run_deploy; then fail 'deployment accepted invalid Nginx configuration'; fi
    assert_no_docker_or_rsync_writes
}

assert_deploy_rejects_root_bootstrap_env_without_writes() {
    local env_fixture key stdout_file="$fixture_dir/deploy-root-env.stdout" stderr_file="$fixture_dir/deploy-root-env.stderr"
    local root_env_fixtures=(
        '.env.deploy-root-username:ROOT_BOOTSTRAP_USERNAME'
        '.env.deploy-root-password:ROOT_BOOTSTRAP_PASSWORD'
        '.env.deploy-root-leading-whitespace:ROOT_BOOTSTRAP_USERNAME'
        '.env.deploy-root-export:ROOT_BOOTSTRAP_PASSWORD'
        '.env.deploy-root-spaced:ROOT_BOOTSTRAP_USERNAME'
        '.env.deploy-root-colon:ROOT_BOOTSTRAP_PASSWORD'
        '.env.deploy-root-comment:ROOT_BOOTSTRAP_USERNAME'
        '.env.deploy-root-mixed-duplicate:ROOT_BOOTSTRAP_USERNAME'
    )
    for env_fixture in "${root_env_fixtures[@]}"; do
        rm -f -- "$fixture_lock_file"
        wait_for_fixture_path_state missing /fixture/auth-acceptance.lock
        cp "$backend_dir/${env_fixture%%:*}" "$backend_dir/.env"
        wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
        key="${env_fixture#*:}"
        if run_deploy_with_captured_output; then
            cp "$backend_dir/.env.clean" "$backend_dir/.env"
            wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
            fail 'deployment accepted a Root bootstrap declaration from .env'
        fi
        grep -Fq "$key" "$stderr_file" || {
            cp "$backend_dir/.env.clean" "$backend_dir/.env"
            wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
            fail 'deployment rejection did not identify the Root bootstrap key'
        }
        assert_no_sensitive_argv_fields 'Aa1@fixture-secret' 'root_admin' || {
            cp "$backend_dir/.env.clean" "$backend_dir/.env"
            wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
            fail 'Root bootstrap value appeared in a structured argv field'
        }
        ! grep -Fq 'Aa1@fixture-secret' "$stdout_file" "$stderr_file" || {
            cp "$backend_dir/.env.clean" "$backend_dir/.env"
            wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
            fail 'Root bootstrap secret appeared in deployment output'
        }
        ! grep -Fq 'root_admin' "$stdout_file" "$stderr_file" || {
            cp "$backend_dir/.env.clean" "$backend_dir/.env"
            wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
            fail 'Root bootstrap username appeared in deployment output'
        }
        [[ ! -e "$fixture_lock_file" && ! -L "$fixture_lock_file" ]] || fail 'Root bootstrap rejection created the deployment lock file'
        assert_no_git_fetch_calls
        assert_no_deploy_preparation_calls
        assert_no_docker_or_rsync_writes
        assert_no_dangerous_calls
        cp "$backend_dir/.env.clean" "$backend_dir/.env"
        wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
    done
}

assert_publish_failures_restore_application() {
    if MOCK_RSYNC_RESULT=failure run_deploy; then fail 'deployment accepted static publish failure'; fi
    require_call docker rm -f -- ai-gateway-go
    require_candidate_rollback_renames
    if MOCK_SYSTEMCTL_RESULT=failure run_deploy; then fail 'deployment accepted Nginx reload failure'; fi
    require_call docker rm -f -- ai-gateway-go
    require_candidate_rollback_renames
}

assert_root_secret_absent() {
    local secret='Aa1@fixture-secret' credential_contents
    credential_contents="$(printf 'username=root_admin\npassword=Aa1@fixture-secret')"
    assert_no_sensitive_argv_fields "$secret" 'username=root_admin' "$credential_contents" || fail 'sensitive data appeared in a structured argv field'
    ! grep -Fq "$secret" "$fixture_dir/root-bootstrap.stdout" "$fixture_dir/root-bootstrap.stderr" || fail 'root credential secret appeared in output'
    ! grep -Fq 'username=root_admin' "$fixture_dir/root-bootstrap.stdout" "$fixture_dir/root-bootstrap.stderr" || fail 'root credential username appeared in output'
    ! grep -Fq "$credential_contents" "$fixture_dir/root-bootstrap.stdout" "$fixture_dir/root-bootstrap.stderr" || fail 'root credential file contents appeared in output'
}

assert_root_bootstrap_requires_confirmation_without_writes() {
    local stdout_file="$fixture_dir/root-bootstrap.stdout" stderr_file="$fixture_dir/root-bootstrap.stderr"
    : >"$stdout_file"; : >"$stderr_file"
    if run_entrypoint auth-acceptance-bootstrap-root.sh >"$stdout_file" 2>"$stderr_file"; then fail 'root bootstrap accepted no confirmation'; fi
    assert_root_secret_absent
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    : >"$stdout_file"; : >"$stderr_file"
    if run_entrypoint auth-acceptance-bootstrap-root.sh --wrong-confirmation >"$stdout_file" 2>"$stderr_file"; then fail 'root bootstrap accepted wrong confirmation'; fi
    assert_root_secret_absent
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    : >"$stdout_file"; : >"$stderr_file"
    if run_entrypoint auth-acceptance-bootstrap-root.sh --confirm-auth-root-bootstrap extra >"$stdout_file" 2>"$stderr_file"; then fail 'root bootstrap accepted extra arguments'; fi
    assert_root_secret_absent
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
}

assert_root_bootstrap_requires_root_without_writes() {
    if MOCK_ID_UID=1000 run_root_bootstrap; then fail 'root bootstrap accepted a non-root operator'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
}

assert_root_bootstrap_rejects_invalid_checkout_without_writes() {
    if MOCK_BRANCH=main run_root_bootstrap; then fail 'root bootstrap accepted wrong branch'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_GIT_DIRTY=1 run_root_bootstrap; then fail 'root bootstrap accepted dirty checkout'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_GIT_STATUS_FAILURE=1 run_root_bootstrap; then fail 'root bootstrap accepted failed Git status inspection'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_REMOTE_MISMATCH=1 run_root_bootstrap; then fail 'root bootstrap accepted remote SHA mismatch'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
}

assert_root_bootstrap_rejects_unsafe_source_metadata_without_writes() {
    if MOCK_CREDENTIAL_PARENT_MODE=770 run_root_bootstrap; then fail 'root bootstrap accepted group-writable credential parent'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_CREDENTIAL_PARENT_MODE=707 run_root_bootstrap; then fail 'root bootstrap accepted world-writable credential parent'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_BACKEND_UID=1000 run_root_bootstrap; then fail 'root bootstrap accepted non-root backend directory owner'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_BACKEND_MODE=775 run_root_bootstrap; then fail 'root bootstrap accepted writable backend directory'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_ENV_UID=1000 run_root_bootstrap; then fail 'root bootstrap accepted non-root backend .env owner'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_ENV_MODE=640 run_root_bootstrap; then fail 'root bootstrap accepted non-600 backend .env mode'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
}

assert_root_bootstrap_rejects_unsafe_snapshot_metadata_without_writes() {
    if MOCK_SNAPSHOT_CREDENTIAL_UID=1000 run_root_bootstrap; then fail 'root bootstrap accepted non-root credential snapshot owner'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_SNAPSHOT_CREDENTIAL_MODE=640 run_root_bootstrap; then fail 'root bootstrap accepted non-600 credential snapshot mode'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_SNAPSHOT_ENV_UID=1000 run_root_bootstrap; then fail 'root bootstrap accepted non-root .env snapshot owner'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_SNAPSHOT_ENV_MODE=640 run_root_bootstrap; then fail 'root bootstrap accepted non-600 .env snapshot mode'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
}

assert_root_bootstrap_rejects_invalid_credentials_without_writes() {
    local credential_file="$fixture_dir/root-acceptance-credentials"
    mv "$credential_file" "$credential_file.real"
    wait_for_fixture_path_state missing /fixture/root-acceptance-credentials
    if run_root_bootstrap; then fail 'root bootstrap accepted missing credentials'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    mv "$credential_file.real" "$credential_file"
    wait_for_fixture_file "$credential_file" /fixture/root-acceptance-credentials

    mv "$credential_file" "$credential_file.real"
    mv "$fixture_dir/root-credentials-directory" "$credential_file"
    wait_for_fixture_path_state directory /fixture/root-acceptance-credentials
    if run_root_bootstrap; then fail 'root bootstrap accepted credential directory'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    rmdir "$credential_file"
    mv "$credential_file.real" "$credential_file"
    wait_for_fixture_file "$credential_file" /fixture/root-acceptance-credentials

    mv "$credential_file" "$credential_file.real"
    ln -s root-acceptance-credentials.real "$credential_file"
    wait_for_fixture_path_state symlink /fixture/root-acceptance-credentials
    if run_root_bootstrap; then fail 'root bootstrap accepted credential symlink'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    rm "$credential_file"
    mv "$credential_file.real" "$credential_file"
    wait_for_fixture_file "$credential_file" /fixture/root-acceptance-credentials

    if MOCK_CREDENTIAL_MODE=640 run_root_bootstrap; then fail 'root bootstrap accepted non-600 credential mode'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_CREDENTIAL_UID=1000 run_root_bootstrap; then fail 'root bootstrap accepted non-root credential owner'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls

    local invalid_credentials=(
        root-credentials-malformed
        root-credentials-duplicate
        root-credentials-unknown
        root-credentials-missing-password
        root-credentials-missing-username
        root-credentials-empty-username
        root-credentials-empty-password
        root-credentials-leading-space
        root-credentials-trailing-space
        root-credentials-blank-line
    )
    local invalid
    for invalid in "${invalid_credentials[@]}"; do
        mv "$fixture_dir/$invalid" "$credential_file"
        wait_for_fixture_file "$credential_file" /fixture/root-acceptance-credentials
        if run_root_bootstrap; then fail 'root bootstrap accepted malformed credentials'; fi
        assert_no_docker_or_rsync_writes
        assert_no_dangerous_calls
    done
    mv "$fixture_dir/root-credentials-valid-restore" "$credential_file"
    wait_for_fixture_file "$credential_file" /fixture/root-acceptance-credentials
}

assert_root_bootstrap_rejects_env_credentials_without_writes() {
    mv "$backend_dir/.env" "$backend_dir/.env.real"
    wait_for_fixture_path_state missing /fixture/Porsche/.env
    if run_root_bootstrap; then fail 'root bootstrap accepted missing backend .env'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    mv "$backend_dir/.env.real" "$backend_dir/.env"
    wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env

    mv "$backend_dir/.env" "$backend_dir/.env.real"
    ln -s .env.real "$backend_dir/.env"
    wait_for_fixture_path_state symlink /fixture/Porsche/.env
    if run_root_bootstrap; then fail 'root bootstrap accepted backend .env symlink'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    rm "$backend_dir/.env"
    mv "$backend_dir/.env.real" "$backend_dir/.env"
    wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env

    mv "$backend_dir/.env.root-username" "$backend_dir/.env"
    grep -Fqx 'ROOT_BOOTSTRAP_USERNAME=env-root' "$backend_dir/.env" || fail 'fixture did not write ROOT_BOOTSTRAP_USERNAME test input'
    wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
    if run_root_bootstrap; then fail 'root bootstrap accepted ROOT_BOOTSTRAP_USERNAME from .env'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    mv "$backend_dir/.env.root-password" "$backend_dir/.env"
    grep -Fqx 'ROOT_BOOTSTRAP_PASSWORD=env-password' "$backend_dir/.env" || fail 'fixture did not write ROOT_BOOTSTRAP_PASSWORD test input'
    wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
    if run_root_bootstrap; then fail 'root bootstrap accepted ROOT_BOOTSTRAP_PASSWORD from .env'; fi
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls

    local alternate_envs=(
        .env.root-leading-whitespace
        .env.root-export
        .env.root-spaced
        .env.root-colon
        .env.root-mixed-duplicate
    )
    local alternate_env
    for alternate_env in "${alternate_envs[@]}"; do
        mv "$backend_dir/$alternate_env" "$backend_dir/.env"
        wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
        if run_root_bootstrap; then fail 'root bootstrap accepted alternate Root declaration syntax'; fi
        assert_no_docker_or_rsync_writes
        assert_no_dangerous_calls
    done
    mv "$backend_dir/.env.clean" "$backend_dir/.env"
    wait_for_fixture_file "$backend_dir/.env" /fixture/Porsche/.env
}

assert_root_bootstrap_rejects_missing_docker_dependencies_without_writes() {
    if MOCK_NETWORK_INSPECT_RESULT=failure run_root_bootstrap; then fail 'root bootstrap accepted missing Docker network'; fi
    require_call docker network inspect porsche-app
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_MYSQL_INSPECT_RESULT=failure run_root_bootstrap; then fail 'root bootstrap accepted missing MySQL container'; fi
    require_call docker network inspect porsche-app
    require_call docker container inspect porsche-mysql
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
    if MOCK_MYSQL_EXISTS=0 run_root_bootstrap; then fail 'root bootstrap accepted absent MySQL container'; fi
    require_call docker network inspect porsche-app
    require_call docker container inspect porsche-mysql
    assert_no_docker_or_rsync_writes
    assert_no_dangerous_calls
}

assert_root_bootstrap_rejects_invalid_image_id_without_run() {
    if MOCK_DOCKER_BUILD_IMAGE_ID=not-an-image-id run_root_bootstrap; then fail 'root bootstrap accepted invalid Docker build image id'; fi
    require_exact_call git archive --format=tar fixture-sha
    require_exact_call docker build --quiet --tag ai-gateway-go:auth-acceptance -
    assert_no_docker_run
    assert_no_dangerous_calls
}

assert_root_bootstrap_uses_readonly_secret_mounts() {
    if ! run_root_bootstrap; then
        fail "root bootstrap happy path failed: $(<"$fixture_dir/root-bootstrap.stderr")"
    fi
    require_call docker network inspect porsche-app
    require_call docker container inspect porsche-mysql
    require_exact_call git archive --format=tar fixture-sha
    require_exact_call docker build --quiet --tag ai-gateway-go:auth-acceptance -
    require_root_bootstrap_snapshot_copies
    require_root_bootstrap_snapshot_run
    assert_no_call_token --env
    assert_no_call_token --env-file
    assert_root_secret_absent
    assert_no_dangerous_calls
}

assert_operator_documentation() {
    local command
    for command in \
        'sudo bash deploy/bootstrap-auth-redis.sh' \
        'sudo bash deploy/auth-acceptance-migrate.sh --confirm-auth-schema-migration' \
        'sudo bash deploy/auth-acceptance-bootstrap-root.sh --confirm-auth-root-bootstrap' \
        'sudo bash deploy/auth-acceptance-deploy.sh' \
        'sudo bash deploy/auth-acceptance-rollback.sh --confirm-auth-acceptance-rollback'; do
        grep -Fq "$command" "$source_repo/README.md" || fail "README missing operator command: $command"
    done
    grep -Fq 'ROOT_BOOTSTRAP_USERNAME and ROOT_BOOTSTRAP_PASSWORD must be empty before deployment' "$source_repo/README.md" || fail 'README does not require empty Root bootstrap keys before deployment'
    grep -Fq 'docker inspect of the long-running application must not contain ROOT_BOOTSTRAP_' "$source_repo/README.md" || fail 'README does not require inspection for Root bootstrap keys in the long-running application'
    grep -Fq 'username=<Root-username>' "$source_repo/README.md" || fail 'README missing Root credential schema username field'
    grep -Fq 'password=<Root-password>' "$source_repo/README.md" || fail 'README missing Root credential schema password field'
    grep -Fq 'exactly two lines' "$source_repo/README.md" || fail 'README missing Root credential line-count requirement'
    grep -Fq 'no blank, unknown, or duplicate lines' "$source_repo/README.md" || fail 'README missing Root credential structural restrictions'
    grep -Fq '12–20 characters' "$source_repo/README.md" || fail 'README missing Root password length requirement'
    grep -Fq 'tracked tree has no modifications' "$source_repo/README.md" || fail 'README missing tracked checkout requirement'
    grep -Fq 'untracked and ignored files do not enter the image' "$source_repo/README.md" || fail 'README missing verified archive build-input boundary'
    grep -Fq '/opt/Porsche` must be root-owned and not writable by group or other' "$source_repo/README.md" || fail 'README missing backend directory permission requirement'
    grep -Fq 'docker container inspect ai-gateway-go --format' "$source_repo/README.md" || fail 'README missing safe long-running application inspect command'
    grep -Fq "'{{range .Config.Env}}{{println .}}{{end}}'" "$source_repo/README.md" || fail 'README missing inspect format template'
    grep -Fq "grep -Eq '^ROOT_BOOTSTRAP_(USERNAME|PASSWORD)='" "$source_repo/README.md" || fail 'README missing Root bootstrap environment check pattern'
    grep -Fq 'PIPESTATUS' "$source_repo/README.md" || fail 'README does not capture pipeline statuses for Root bootstrap environment check'
    grep -Fq 'PASS: long-running application has no ROOT_BOOTSTRAP_ environment keys' "$source_repo/README.md" || fail 'README missing pass condition for Root bootstrap environment check'
    grep -Fq 'Application rollback does not automatically roll back the database migration' "$source_repo/README.md" || fail 'README does not state that application rollback leaves the migration applied'
}

# Unreachable until target scripts exist; later tasks turn these contracts green.
for selected_check in "${selected_checks[@]}"; do
    case "$selected_check" in
        bootstrap) assert_bootstrap_rejects_invalid_passwords_and_existing_container; assert_bootstrap_creates_internal_redis ;;
        migration) assert_migration_requires_confirmation_without_writes; run_migration ;;
        deploy) assert_deploy_refuses_main_or_dirty_checkout_without_writes; assert_deploy_preflight_failures_do_not_write; assert_deploy_rejects_root_bootstrap_env_without_writes; assert_deploy_rejects_root_bootstrap_snapshot_race_without_writes; assert_deploy_uses_snapshot_after_source_env_mutates; assert_candidate_failure_restores_old_application; assert_publish_failures_restore_application; assert_successful_deploy_order_and_manifest ;;
        rollback) run_rollback ;;
        root-bootstrap) assert_root_bootstrap_requires_confirmation_without_writes; assert_root_bootstrap_requires_root_without_writes; assert_root_bootstrap_rejects_invalid_checkout_without_writes; assert_root_bootstrap_rejects_unsafe_source_metadata_without_writes; assert_root_bootstrap_rejects_invalid_credentials_without_writes; assert_root_bootstrap_rejects_unsafe_snapshot_metadata_without_writes; assert_root_bootstrap_rejects_env_credentials_without_writes; assert_root_bootstrap_rejects_missing_docker_dependencies_without_writes; assert_root_bootstrap_rejects_invalid_image_id_without_run; assert_root_bootstrap_uses_readonly_secret_mounts ;;
        docs) assert_operator_documentation ;;
    esac
done
echo "PASS: auth acceptance deployment regression checks (${selected_checks[*]})"
