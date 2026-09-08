#!/usr/bin/env bash
set -Eeuo pipefail

fail() {
    printf 'merge environment failed: %s\n' "$1" >&2
    exit 1
}

[[ "$#" -eq 2 ]] || fail 'expected example and env paths'
example_path="$1"
env_path="$2"
[[ "$example_path" == /* && "$env_path" == /* ]] || fail 'paths must be absolute'

env_dir="$(cd -- "$(dirname -- "$env_path")" && pwd)" || fail 'env directory is unavailable'
env_name="$(basename -- "$env_path")"
lock_path="$env_dir/.$env_name.merge.lock"
umask 077
exec 8>"$lock_path" || fail 'cannot open merge lock'
flock -x 8 || fail 'cannot acquire merge lock'

[[ -f "$example_path" && ! -L "$example_path" ]] || fail 'example must be a regular file'
[[ -f "$env_path" && ! -L "$env_path" ]] || fail 'env must be a regular file'
cp --version 2>/dev/null | grep -Fq 'GNU coreutils' || fail 'full metadata copy is unavailable'

file_identity() {
    stat -f '%d:%i:%u:%g:%Lp' "$1" 2>/dev/null || stat -c '%d:%i:%u:%g:%a' "$1" 2>/dev/null
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
    local path="$1" attribute
    {
        stat -f '%u:%g:%Lp' "$path" 2>/dev/null || stat -c '%u:%g:%a' "$path" 2>/dev/null
        if command -v getfacl >/dev/null 2>&1; then
            getfacl -cp -- "$path" 2>/dev/null || return 1
        fi
        if command -v getfattr >/dev/null 2>&1; then
            getfattr -d -m- --absolute-names -- "$path" 2>/dev/null | sed '1d' || return 1
        elif command -v xattr >/dev/null 2>&1; then
            while IFS= read -r attribute; do
                printf '%s\0' "$attribute"
                xattr -px "$attribute" "$path" 2>/dev/null || return 1
            done < <(xattr "$path" 2>/dev/null) || return 1
        fi
    } | digest_stream
}

initial_identity="$(file_identity "$env_path")" || fail 'cannot inspect env metadata'
snapshot_path="$(mktemp "$env_dir/$env_name.snapshot.XXXXXX")"
temp_path="$(mktemp "$env_dir/$env_name.merge.XXXXXX")"
cleanup() {
    [[ -z "${snapshot_path:-}" ]] || rm -f -- "$snapshot_path"
    [[ -z "${temp_path:-}" ]] || rm -f -- "$temp_path"
}
trap cleanup EXIT HUP INT TERM
cp --preserve=all --no-dereference -- "$env_path" "$snapshot_path" || fail 'cannot preserve env snapshot metadata'
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

cp --preserve=all --no-dereference -- "$snapshot_path" "$temp_path" || fail 'cannot preserve candidate metadata'

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
cmp -s -- "$env_path" "$snapshot_path" || fail 'env content changed during merge'
[[ "$(metadata_fingerprint "$env_path")" == "$snapshot_metadata" ]] || fail 'env metadata changed during merge'
mv -f -- "$temp_path" "$env_path"
temp_path=''
rm -f -- "$snapshot_path"
snapshot_path=''

for key in "${added_keys[@]}"; do
    printf 'added environment key: %s\n' "$key"
done
