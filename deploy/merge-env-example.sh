#!/usr/bin/env bash
# Usage: merge-env-example.sh /absolute/path/.env.example /absolute/path/.env
# Every supported writer must hold the sibling .<env-name>.merge.lock. Do not
# edit the environment file without that lock while deployment is running.
set -Eeuo pipefail

fail() {
    printf 'merge environment failed: %s\n' "$1" >&2
    exit 1
}

[[ "$#" -eq 2 ]] || fail 'expected example and env paths'
example_path="$1"
env_path="$2"
[[ "$example_path" == /* && "$env_path" == /* ]] || fail 'paths must be absolute'

resolve_system_command() {
    local name="$1" directory candidate
    if [[ "${MERGE_ENV_EXAMPLE_TEST_MODE:-}" == 1 && -n "${MERGE_ENV_EXAMPLE_TEST_SYSTEM_DIR:-}" ]]; then
        candidate="$MERGE_ENV_EXAMPLE_TEST_SYSTEM_DIR/$name"
        [[ "$candidate" == /* && -x "$candidate" ]] && { printf '%s\n' "$candidate"; return 0; }
    fi
    for directory in /usr/bin /bin /usr/sbin /sbin; do
        candidate="$directory/$name"
        [[ -x "$candidate" ]] && { printf '%s\n' "$candidate"; return 0; }
    done
    return 1
}

cp_command="$(resolve_system_command cp)" || fail 'system cp is unavailable'
mv_command="$(resolve_system_command mv)" || fail 'system mv is unavailable'
stat_command="$(resolve_system_command stat)" || fail 'system stat is unavailable'
cmp_command="$(resolve_system_command cmp)" || fail 'system cmp is unavailable'
flock_command="$(resolve_system_command flock)" || fail 'system flock is unavailable'
getfacl_command="$(resolve_system_command getfacl || true)"
getfattr_command="$(resolve_system_command getfattr || true)"
xattr_command="$(resolve_system_command xattr || true)"
[[ "$cp_command" == /* && -x "$cp_command" && "$mv_command" == /* && -x "$mv_command" ]] || fail 'resolved system command is invalid'
[[ "$stat_command" == /* && -x "$stat_command" && "$cmp_command" == /* && -x "$cmp_command" ]] || fail 'resolved system command is invalid'
[[ "$flock_command" == /* && -x "$flock_command" ]] || fail 'resolved system command is invalid'

env_dir="$(cd -- "$(dirname -- "$env_path")" && pwd)" || fail 'env directory is unavailable'
env_name="$(basename -- "$env_path")"
lock_path="$env_dir/.$env_name.merge.lock"
umask 077
exec 8>"$lock_path" || fail 'cannot open merge lock'
"$flock_command" -n -x 8 || fail 'cannot acquire merge lock'

[[ -f "$example_path" && ! -L "$example_path" ]] || fail 'example must be a regular file'
[[ -f "$env_path" && ! -L "$env_path" ]] || fail 'env must be a regular file'
exec 7<"$env_path" || fail 'cannot open env file'
"$flock_command" -n -x 7 || fail 'cannot lock env file'
cp_version="$("$cp_command" --version 2>/dev/null)" || fail 'full metadata copy is unavailable'
[[ "$cp_version" == *'GNU coreutils'* ]] || fail 'full metadata copy is unavailable'

file_identity() {
    "$stat_command" -f '%d:%i:%u:%g:%Lp' "$1" 2>/dev/null || "$stat_command" -c '%d:%i:%u:%g:%a' "$1" 2>/dev/null
}

digest_stream() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 | awk '{print $1}'
    else
        fail 'metadata digest is unavailable'
    fi
}

metadata_fingerprint() {
    local path="$1" attribute attributes
    {
        "$stat_command" -f '%u:%g:%Lp' "$path" 2>/dev/null || "$stat_command" -c '%u:%g:%a' "$path" 2>/dev/null
        if [[ -n "$getfacl_command" ]]; then
            "$getfacl_command" -cp -- "$path" 2>/dev/null || return 1
        fi
        if [[ -n "$getfattr_command" ]]; then
            "$getfattr_command" -d -m- --absolute-names -- "$path" 2>/dev/null | sed '1d' || return 1
        elif [[ -n "$xattr_command" ]]; then
            attributes="$("$xattr_command" "$path" 2>/dev/null)" || return 1
            if [[ -n "$attributes" ]]; then
                while IFS= read -r attribute; do
                    printf '%s\0' "$attribute"
                    "$xattr_command" -px "$attribute" "$path" 2>/dev/null || return 1
                done <<<"$attributes"
            fi
        fi
    } | digest_stream
}

initial_identity="$(file_identity "$env_path")" || fail 'cannot inspect env metadata'
snapshot_path="$(mktemp "$env_dir/$env_name.snapshot.XXXXXX")"
temp_path="$(mktemp "$env_dir/$env_name.merge.XXXXXX")"
copy_error_path=''
cleanup() {
    [[ -z "${snapshot_path:-}" ]] || rm -f -- "$snapshot_path"
    [[ -z "${temp_path:-}" ]] || rm -f -- "$temp_path"
    [[ -z "${copy_error_path:-}" ]] || rm -f -- "$copy_error_path"
}
trap cleanup EXIT HUP INT TERM

copy_preserving_metadata() {
    local source="$1" destination="$2"
    copy_error_path="$(mktemp "$env_dir/$env_name.copy-error.XXXXXX")"
    if ! "$cp_command" --preserve=all --no-dereference -- "$source" "$destination" 2>"$copy_error_path"; then
        fail 'cannot preserve env metadata'
    fi
    [[ ! -s "$copy_error_path" ]] || fail 'cannot confirm env metadata preservation'
    rm -f -- "$copy_error_path"
    copy_error_path=''
}

copy_preserving_metadata "$env_path" "$snapshot_path"
snapshot_metadata="$(metadata_fingerprint "$snapshot_path")" || fail 'cannot fingerprint env metadata'

declare -a example_keys=() example_values=() env_keys=()
commented_empty_pattern='^# ([A-Z][A-Z0-9_]*)=$'

contains_key() {
    local wanted="$1" candidate
    shift
    for candidate in "$@"; do
        [[ "$candidate" == "$wanted" ]] && return 0
    done
    return 1
}

while IFS= read -r line || [[ -n "$line" ]]; do
    key=''
    value=''
    if [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?([A-Z][A-Z0-9_]*)[[:space:]]*=(.*)$ ]]; then
        key="${BASH_REMATCH[2]}"
        value="${BASH_REMATCH[3]}"
    elif [[ "$line" =~ $commented_empty_pattern ]]; then
        key="${BASH_REMATCH[1]}"
    elif [[ "$line" =~ ^[[:space:]]*$ || "$line" =~ ^[[:space:]]*# ]]; then
        continue
    else
        fail 'malformed example assignment'
    fi

    ! contains_key "$key" ${example_keys[@]+"${example_keys[@]}"} || fail "duplicate example key: $key"
    example_keys+=("$key")
    example_values+=("$value")
done <"$example_path"

while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?([A-Z][A-Z0-9_]*)[[:space:]]*= ]]; then
        env_keys+=("${BASH_REMATCH[2]}")
    fi
done <"$snapshot_path"

declare -a added_keys=()
for index in "${!example_keys[@]}"; do
    key="${example_keys[$index]}"
    contains_key "$key" ${env_keys[@]+"${env_keys[@]}"} || added_keys+=("$key")
done

(( ${#added_keys[@]} > 0 )) || exit 0

copy_preserving_metadata "$snapshot_path" "$temp_path"

if [[ -s "$snapshot_path" ]] && [[ "$(tail -c 1 "$snapshot_path" | wc -l | tr -d ' ')" == 0 ]]; then
    printf '\n' >>"$temp_path"
fi

for index in "${!example_keys[@]}"; do
    key="${example_keys[$index]}"
    if ! contains_key "$key" ${env_keys[@]+"${env_keys[@]}"}; then
        printf '%s=%s\n' "$key" "${example_values[$index]}" >>"$temp_path"
    fi
done

[[ "$(metadata_fingerprint "$temp_path")" == "$snapshot_metadata" ]] || fail 'candidate metadata changed'
[[ -f "$env_path" && ! -L "$env_path" ]] || fail 'env path changed during merge'
[[ "$(file_identity "$env_path")" == "$initial_identity" ]] || fail 'env identity changed during merge'
"$cmp_command" -s -- "$env_path" "$snapshot_path" || fail 'env content changed during merge'
[[ "$(metadata_fingerprint "$env_path")" == "$snapshot_metadata" ]] || fail 'env metadata changed during merge'
"$mv_command" -f -- "$temp_path" "$env_path"
temp_path=''
rm -f -- "$snapshot_path"
snapshot_path=''

for key in "${added_keys[@]}"; do
    printf 'added environment key: %s\n' "$key"
done
