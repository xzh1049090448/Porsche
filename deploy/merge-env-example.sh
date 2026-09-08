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
[[ -f "$example_path" && ! -L "$example_path" ]] || fail 'example must be a regular file'
[[ -f "$env_path" && ! -L "$env_path" ]] || fail 'env must be a regular file'

declare -a example_keys=() example_values=() env_keys=()

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
    elif [[ "$line" =~ ^[[:space:]]*#[[:space:]]([A-Z][A-Z0-9_]*)=$ ]]; then
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
done <"$env_path"

declare -a added_keys=()
for index in "${!example_keys[@]}"; do
    key="${example_keys[$index]}"
    contains_key "$key" ${env_keys[@]+"${env_keys[@]}"} || added_keys+=("$key")
done

(( ${#added_keys[@]} > 0 )) || exit 0

env_dir="$(cd -- "$(dirname -- "$env_path")" && pwd)"
env_name="$(basename -- "$env_path")"
temp_path="$(mktemp "$env_dir/$env_name.merge.XXXXXX")"
cleanup() { [[ -z "${temp_path:-}" ]] || rm -f -- "$temp_path"; }
trap cleanup EXIT HUP INT TERM

mode="$(stat -f '%Lp' "$env_path" 2>/dev/null || stat -c '%a' "$env_path")"
owner="$(stat -f '%u:%g' "$env_path" 2>/dev/null || stat -c '%u:%g' "$env_path")"
cat -- "$env_path" >"$temp_path"

if [[ -s "$env_path" ]] && [[ "$(tail -c 1 "$env_path" | wc -l | tr -d ' ')" == 0 ]]; then
    printf '\n' >>"$temp_path"
fi

for index in "${!example_keys[@]}"; do
    key="${example_keys[$index]}"
    if ! contains_key "$key" ${env_keys[@]+"${env_keys[@]}"}; then
        printf '%s=%s\n' "$key" "${example_values[$index]}" >>"$temp_path"
    fi
done

chmod "$mode" "$temp_path"
temp_owner="$(stat -f '%u:%g' "$temp_path" 2>/dev/null || stat -c '%u:%g' "$temp_path")"
[[ "$temp_owner" == "$owner" ]] || chown "$owner" "$temp_path"
mv -f -- "$temp_path" "$env_path"
temp_path=''

for key in "${added_keys[@]}"; do
    printf 'added environment key: %s\n' "$key"
done
