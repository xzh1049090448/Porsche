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
    selected_checks=(bootstrap migration deploy rollback)
fi
for selected_check in "${selected_checks[@]}"; do
    case "$selected_check" in bootstrap|migration|deploy|rollback) ;; *) fail "unknown check: $selected_check" ;; esac
done

script_for_check() {
    case "$1" in
        bootstrap) printf '%s\n' bootstrap-auth-redis.sh ;;
        migration) printf '%s\n' auth-acceptance-migrate.sh ;;
        deploy) printf '%s\n' auth-acceptance-deploy.sh ;;
        rollback) printf '%s\n' auth-acceptance-rollback.sh ;;
    esac
}

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

# This is a metadata-only regression probe, not an entrypoint root. It proves
# that an existing source-like .env is not rejected or read by this fixture.
if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_SOURCE_ENV_REGRESSION:-0}" == 1 ]]; then
    source_env_probe="$(mktemp -d "${TMPDIR:-/tmp}/auth-acceptance-source-env.XXXXXX")"
    printf 'fixture-only-source-env\n' >"$source_env_probe/.env"
    probe_before="$(env_state_for "$source_env_probe/.env")"
    [[ "$probe_before" == present:* ]] || fail 'source-env metadata probe did not observe .env'
    [[ "$(env_state_for "$source_env_probe/.env")" == "$probe_before" ]] || fail 'source-env metadata probe changed .env'
fi

# Existing target scripts are linked into the fake checkout. Future calls use
# these paths exclusively, so BASH_SOURCE-based root lookup sees $backend_dir.
for check in bootstrap migration deploy rollback; do
    script_name="$(script_for_check "$check")"
    source_script="$script_dir/$script_name"
    fixture_script="$backend_dir/deploy/$script_name"
    [[ ! -x "$source_script" ]] || ln -s "$source_script" "$fixture_script"
done

token_is_assignment() {
    [[ "$1" =~ ^[A-Za-z_][A-Za-z0-9_]*= ]]
}

token_is_forbidden_command() {
    local token="$1"
    case "$token" in
        source|.|command|env|exec|time|nice|nohup|xargs|sudo|bash|sh|zsh|dash|fish|eval|builtin|/*|*/*|*'$'*|*'`'*) return 0 ;;
    esac
    return 1
}

entrypoint_has_no_absolute_command_bypass() {
    local entrypoint="$1" line line_number=0 length index character next_character quote='' token='' command_expected=1 substitution
    while IFS= read -r line || [[ -n "$line" ]]; do
        ((line_number += 1))
        [[ "$line" =~ ^[[:space:]]*$ || "$line" =~ ^[[:space:]]*# ]] && continue
        length="${#line}"; quote=''; token=''; command_expected=1; index=0
        while (( index < length )); do
            character="${line:index:1}"
            if [[ -n "$quote" ]]; then
                if [[ "$character" == '\\' && "$quote" == '"' && $((index + 1)) -lt $length ]]; then
                    ((index += 1)); token+="${line:index:1}"
                elif [[ "$character" == "$quote" ]]; then
                    quote=''
                else
                    token+="$character"
                fi
                ((index += 1)); continue
            fi
            case "$character" in
                "'"|'"') quote="$character" ;;
                '\\')
                    if (( index + 1 < length )); then ((index += 1)); token+="${line:index:1}"; fi
                    ;;
                '#') [[ -z "$token" ]] && break; token+="$character" ;;
                '$')
                    next_character="${line:index+1:1}"
                    if [[ "$next_character" == '(' ]]; then
                        substitution="${line:index+2}"
                        while [[ "$substitution" == [[:space:]]* ]]; do substitution="${substitution:1}"; done
                        case "$substitution" in
                            /*|source[[:space:]]*|.[[:space:]]*)
                                echo "command-substitution bypass in $entrypoint:$line_number" >&2
                                return 1
                                ;;
                        esac
                    fi
                    token+="$character"
                    ;;
                ';'|'|'|'&'|'('|')')
                    if [[ -n "$token" ]]; then
                        [[ "$token" != PATH=* ]] || { echo "PATH override in $entrypoint:$line_number" >&2; return 1; }
                        if (( command_expected )); then
                            case "$token" in
                                if|then|elif|else|do|done|'!') ;;
                                *)
                                    token_is_forbidden_command "$token" && { echo "command bypass in $entrypoint:$line_number" >&2; return 1; }
                                    token_is_assignment "$token" || command_expected=0
                                    ;;
                            esac
                        fi
                        token=''
                    fi
                    command_expected=1
                    ;;
                '{'|'}')
                    # ${var} is an argument word, while a standalone brace is
                    # a shell command-group boundary.
                    if [[ -n "$token" ]]; then
                        token+="$character"
                    else
                        command_expected=1
                    fi
                    ;;
                [[:space:]])
                    if [[ -n "$token" ]]; then
                        [[ "$token" != PATH=* ]] || { echo "PATH override in $entrypoint:$line_number" >&2; return 1; }
                        if (( command_expected )); then
                            case "$token" in
                                if|then|elif|else|do|done|'!') ;;
                                *)
                                    token_is_forbidden_command "$token" && { echo "command bypass in $entrypoint:$line_number" >&2; return 1; }
                                    token_is_assignment "$token" || command_expected=0
                                    ;;
                            esac
                        fi
                        token=''
                    fi
                    ;;
                *) token+="$character" ;;
            esac
            ((index += 1))
        done
        if [[ -n "$token" ]]; then
            [[ "$token" != PATH=* ]] || { echo "PATH override in $entrypoint:$line_number" >&2; return 1; }
            if (( command_expected )); then
                case "$token" in
                    if|then|elif|else|do|done|'!') ;;
                    *) token_is_forbidden_command "$token" && { echo "command bypass in $entrypoint:$line_number" >&2; return 1; } ;;
                esac
            fi
        fi
    done <"$entrypoint"
}

assert_fixture_entrypoint() {
    local script_name="$1" fixture_script="$backend_dir/deploy/$1"
    [[ "$fixture_script" == "$backend_dir/deploy/"* ]] || fail "fixture entrypoint escapes fake backend: $fixture_script"
    [[ "$fixture_script" != "$source_repo/"* ]] || fail "fixture entrypoint uses source repository: $fixture_script"
    [[ "$backend_dir/.env" != "$source_repo/.env" ]] || fail 'fixture .env aliases source-repository .env'
    [[ -x "$fixture_script" ]] || fail "missing fixture entrypoint: $fixture_script"
    entrypoint_has_no_absolute_command_bypass "$fixture_script" || fail "fixture entrypoint bypasses command mocks: $fixture_script"
}

assert_static_bypass_guard() {
    local bypass_script="$backend_dir/deploy/fixture-absolute-bypass.sh" bypass_line
    : >"$command_log"
    for bypass_line in '/opt/homebrew/bin/docker run fixture' '$(/custom/bin/rsync --archive source destination)' '. ./helper.sh' '( /usr/bin/docker run fixture )' '{ /custom/bin/rsync --archive source destination; }' 'pattern) /srv/bin/docker run fixture ;;' 'command /custom/bin/docker run fixture' 'env X=1 /custom/bin/rsync --archive source destination' 'PATH=/usr/bin command docker inspect fixture' 'bash helper.sh' 'BIN=/custom/bin/docker; "$BIN" run fixture'; do
        printf '%s\n' '#!/usr/bin/env bash' '# comment: /opt/homebrew/bin/docker is ignored' "$bypass_line" >"$bypass_script"
        chmod +x "$bypass_script"
        if entrypoint_has_no_absolute_command_bypass "$bypass_script" >"$fixture_dir/bypass.stdout" 2>"$fixture_dir/bypass.stderr"; then
            fail "fixture command bypass was accepted: $bypass_line"
        fi
        [[ ! -s "$command_log" ]] || fail 'static bypass scan executed a command'
    done
    printf '%s\n' '#!/usr/bin/env bash' 'BIN=fixture docker --env-file /opt/Porsche/.env inspect "$BIN"' >"$bypass_script"
    entrypoint_has_no_absolute_command_bypass "$bypass_script" || fail 'guard rejected an absolute command argument'
    rm -f -- "$bypass_script"
}

# NUL command log protocol: BEGIN/call-id, ARG/value pairs, END/call-id.
# Both spaces and the literal __END__ are ordinary argument values.
write_mock() {
    local command_name="$1"
    printf '%s\n' '#!/usr/bin/env bash' 'set -Eeuo pipefail' \
        'command_name="${0##*/}"' \
        'call_id="${BASHPID:-$$}-${RANDOM}"' \
        'printf "BEGIN\\0%s\\0ARG\\0%s\\0" "$call_id" "$command_name" >>"$COMMAND_LOG"' \
        'for arg in "$@"; do printf "ARG\\0%s\\0" "$arg" >>"$COMMAND_LOG"; done' \
        'printf "END\\0%s\\0" "$call_id" >>"$COMMAND_LOG"' \
        'case "$command_name" in' \
        '  id) [[ "${1:-}" == "-u" ]] && printf "0\\n" ;;' \
        '  git) case "${1:-}" in branch) printf "%s\\n" "${MOCK_BRANCH:-feature/user-registration-management}" ;; rev-parse) printf "%s\\n" "${MOCK_GIT_SHA:-fixture-sha}" ;; diff|status) [[ "${MOCK_GIT_DIRTY:-0}" == 0 ]] ;; esac ;;' \
        '  docker) case "${1:-}" in network|container) [[ "${MOCK_DOCKER_INSPECT_RESULT:-success}" == success ]] ;; run) [[ "${MOCK_DOCKER_RUN_RESULT:-success}" == success ]] || exit 71; printf "fixture-container\\n" ;; esac ;;' \
        '  curl) [[ "${MOCK_HEALTH_RESULT:-success}" == success ]] || exit 72 ;;' \
        '  npm) [[ "${MOCK_NPM_RESULT:-success}" == success ]] || exit 73 ;;' \
        '  rsync) [[ "${MOCK_RSYNC_RESULT:-success}" == success ]] || exit 74 ;;' \
        '  nginx) [[ "${MOCK_NGINX_RESULT:-success}" == success ]] || exit 75 ;;' \
        '  systemctl) [[ "${MOCK_SYSTEMCTL_RESULT:-success}" == success ]] || exit 76 ;;' \
        '  flock) [[ "${MOCK_FLOCK_RESULT:-success}" == success ]] || exit 77 ;;' \
        'esac' >"$mock_dir/$command_name"
    chmod +x "$mock_dir/$command_name"
}
for command_name in git docker docker-compose mysql mariadb npm rsync nginx systemctl flock id curl sleep; do write_mock "$command_name"; done

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
}

require_call() {
    local call_index
    parse_calls
    for ((call_index = 0; call_index < ${#call_starts[@]}; call_index += 1)); do
        call_has_prefix "$call_index" "$@" && return 0
    done
    fail "missing expected mock invocation: $*"
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

assert_log_protocol_preserves_argv
assert_parser_detects_absolute_docker
assert_parser_detects_optioned_dangerous_calls
assert_parser_detects_optioned_write_calls
assert_static_bypass_guard

# A missing production script must now be reported at the fake checkout path.
for selected_check in "${selected_checks[@]}"; do
    assert_fixture_entrypoint "$(script_for_check "$selected_check")"
done

run_entrypoint() {
    local entrypoint="$1"
    shift
    assert_fixture_entrypoint "$entrypoint"
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
        "$backend_dir/deploy/$entrypoint" "$@"
}

run_bootstrap() { run_entrypoint bootstrap-auth-redis.sh; }
run_migration() {
    run_entrypoint auth-acceptance-migrate.sh --confirm-auth-schema-migration
    assert_no_dangerous_calls
}
run_deploy() { run_entrypoint auth-acceptance-deploy.sh; }
run_rollback() {
    run_entrypoint auth-acceptance-rollback.sh --confirm-auth-acceptance-rollback
    assert_no_dangerous_calls
}

assert_bootstrap_creates_internal_redis() {
    run_bootstrap
    require_call docker volume create porsche-redis-data
    require_call docker run -d --name porsche-redis --restart unless-stopped --network porsche-app
    assert_no_dangerous_calls
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
    require_call docker rename -- ai-gateway-go-acceptance-rollback-
    require_call docker start -- ai-gateway-go
    assert_no_dangerous_calls
}

# Unreachable until target scripts exist; later tasks turn these contracts green.
for selected_check in "${selected_checks[@]}"; do
    case "$selected_check" in
        bootstrap) assert_bootstrap_creates_internal_redis ;;
        migration) run_migration ;;
        deploy) assert_deploy_refuses_main_or_dirty_checkout_without_writes; assert_candidate_failure_restores_old_application ;;
        rollback) run_rollback ;;
    esac
done
echo "PASS: auth acceptance deployment regression checks (${selected_checks[*]})"
