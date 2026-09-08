#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
merge_script="$script_dir/merge-env-example.sh"
fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/merge-env-example-test.XXXXXX")"
trap 'rm -rf -- "$fixture_dir"' EXIT
tool_bin="$fixture_dir/bin"
mkdir "$tool_bin"

cat >"$tool_bin/flock" <<'EOF'
#!/usr/bin/env python3
import fcntl, sys
args = sys.argv[1:]
fd = int(args[-1])
fcntl.flock(fd, fcntl.LOCK_EX)
EOF
cat >"$tool_bin/cp" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "${1:-}" == --version ]]; then printf 'cp (GNU coreutils) test compatibility wrapper\n'; exit 0; fi
source="${@: -2:1}"
destination="${@: -1}"
/bin/cp -p "$source" "$destination"
if [[ -n "${MERGE_TEST_MUTATION:-}" && "$source" == "${MERGE_TEST_ENV:-}" && ! -e "${MERGE_TEST_STATE:-}" ]]; then
    : >"$MERGE_TEST_STATE"
    case "$MERGE_TEST_MUTATION" in
        content) printf 'CONCURRENT=must-survive\n' >>"$MERGE_TEST_ENV" ;;
        path-swap) mv "$MERGE_TEST_ENV" "$MERGE_TEST_ENV.old"; printf 'CONCURRENT=swapped-path\n' >"$MERGE_TEST_ENV" ;;
        metadata) chmod 600 "$MERGE_TEST_ENV" ;;
        pause) sleep 1 ;;
    esac
fi
EOF
chmod +x "$tool_bin/flock" "$tool_bin/cp"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
hash_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}
mode_for() { stat -f '%Lp' "$1" 2>/dev/null || stat -c '%a' "$1"; }
owner_for() { stat -f '%u:%g' "$1" 2>/dev/null || stat -c '%u:%g' "$1"; }
run_merge() { PATH="$tool_bin:$PATH" "$merge_script" "$@"; }

[[ -x "$merge_script" ]] || fail "merge script is missing or not executable: $merge_script"

example="$fixture_dir/example"
env_file="$fixture_dir/env"
cat >"$example" <<'EOF'
EXISTING=template-secret-must-not-print
EMPTY_EXISTING=template-empty-must-not-print
  export SPACED =template-spaced-must-not-print
NEW_ONE=first-new-secret-must-not-print
# NEW_EMPTY=
NEW_TWO="quoted-new-secret-must-not-print"
# ordinary comment
# IGNORED=value
EOF
cat >"$env_file" <<'EOF'
# preserve this comment and all bytes
EXISTING=operator-secret-must-not-print
 export EMPTY_EXISTING =
EOF
chmod 640 "$env_file"
before_owner="$(owner_for "$env_file")"
output="$(run_merge "$example" "$env_file" 2>"$fixture_dir/happy.stderr")"
[[ ! -s "$fixture_dir/happy.stderr" ]] || fail 'successful merge wrote stderr'
[[ "$output" == $'added environment key: SPACED\nadded environment key: NEW_ONE\nadded environment key: NEW_EMPTY\nadded environment key: NEW_TWO' ]] || fail "unexpected success output: $output"
[[ "$output" != *secret* ]] || fail 'success output disclosed a value'
cat >"$fixture_dir/expected" <<'EOF'
# preserve this comment and all bytes
EXISTING=operator-secret-must-not-print
 export EMPTY_EXISTING =
SPACED=template-spaced-must-not-print
NEW_ONE=first-new-secret-must-not-print
NEW_EMPTY=
NEW_TWO="quoted-new-secret-must-not-print"
EOF
cmp -s "$fixture_dir/expected" "$env_file" || fail 'merge did not preserve existing bytes or append expected keys in order'
[[ "$(mode_for "$env_file")" == 640 ]] || fail 'merge changed numeric mode'
[[ "$(owner_for "$env_file")" == "$before_owner" ]] || fail 'merge changed owner'

first_hash="$(hash_file "$env_file")"
second_output="$(run_merge "$example" "$env_file")"
[[ -z "$second_output" ]] || fail 'idempotent merge emitted output'
[[ "$(hash_file "$env_file")" == "$first_hash" ]] || fail 'second merge changed bytes'

cat >"$fixture_dir/comment-placeholders" <<'EOF'
# EXACT_EMPTY=
  # LEADING_SPACE=
#NO_SPACE=
#  TWO_SPACES=
EOF
printf '#\tTAB_SPACE=\n# TRAILING_SPACE= \n' >>"$fixture_dir/comment-placeholders"
printf 'BASE=unchanged\n' >"$fixture_dir/comment-env"
comment_output="$(run_merge "$fixture_dir/comment-placeholders" "$fixture_dir/comment-env")"
[[ "$comment_output" == 'added environment key: EXACT_EMPTY' ]] || fail 'non-exact commented placeholders were recognized'
[[ "$(cat "$fixture_dir/comment-env")" == $'BASE=unchanged\nEXACT_EMPTY=' ]] || fail 'comment placeholder parsing was not exact'

printf 'NO_NEWLINE=old-bytes' >"$fixture_dir/no-newline-env"
printf 'APPENDED=value\n' >"$fixture_dir/no-newline-example"
run_merge "$fixture_dir/no-newline-example" "$fixture_dir/no-newline-env" >"$fixture_dir/no-newline.stdout"
printf 'NO_NEWLINE=old-bytes\nAPPENDED=value\n' >"$fixture_dir/no-newline-expected"
cmp -s "$fixture_dir/no-newline-expected" "$fixture_dir/no-newline-env" || fail 'missing-final-newline merge did not add exactly one separator newline'

assert_rejected_unchanged() {
    local label="$1" bad_example="$2" before
    before="$(hash_file "$env_file")"
    if run_merge "$bad_example" "$env_file" >"$fixture_dir/$label.stdout" 2>"$fixture_dir/$label.stderr"; then
        fail "$label unexpectedly succeeded"
    fi
    [[ "$(hash_file "$env_file")" == "$before" ]] || fail "$label changed env"
    ! grep -Fq 'secret-must-not-print' "$fixture_dir/$label.stdout" "$fixture_dir/$label.stderr" || fail "$label disclosed a value"
}

printf 'DUPLICATE=one-secret-must-not-print\nDUPLICATE=two-secret-must-not-print\n' >"$fixture_dir/duplicate"
assert_rejected_unchanged duplicate "$fixture_dir/duplicate"
printf 'VALID=ok\nBAD-KEY=value-secret-must-not-print\n' >"$fixture_dir/malformed"
assert_rejected_unchanged malformed "$fixture_dir/malformed"
ln -s "$example" "$fixture_dir/example-link"
assert_rejected_unchanged example-symlink "$fixture_dir/example-link"
mkdir "$fixture_dir/example-directory"
assert_rejected_unchanged example-directory "$fixture_dir/example-directory"
mkfifo "$fixture_dir/example-fifo"
assert_rejected_unchanged example-fifo "$fixture_dir/example-fifo"
ln -s "$env_file" "$fixture_dir/env-link"
before="$(hash_file "$env_file")"
if run_merge "$example" "$fixture_dir/env-link" >"$fixture_dir/env-link.stdout" 2>"$fixture_dir/env-link.stderr"; then fail 'env symlink unexpectedly succeeded'; fi
[[ "$(hash_file "$env_file")" == "$before" ]] || fail 'env symlink rejection changed target'
for nonregular_env in env-directory env-fifo; do
    [[ "$nonregular_env" == env-directory ]] && mkdir "$fixture_dir/$nonregular_env" || mkfifo "$fixture_dir/$nonregular_env"
    before="$(hash_file "$env_file")"
    if run_merge "$example" "$fixture_dir/$nonregular_env" >"$fixture_dir/$nonregular_env.stdout" 2>"$fixture_dir/$nonregular_env.stderr"; then fail "$nonregular_env unexpectedly succeeded"; fi
    [[ "$(hash_file "$env_file")" == "$before" ]] || fail "$nonregular_env rejection changed an existing env"
done

printf 'WRITE_FAILURE_KEY=write-failure-secret-must-not-print\n' >"$fixture_dir/write-failure-example"
mkdir "$fixture_dir/mock-bin"
cat >"$fixture_dir/mock-bin/mv" <<'EOF'
#!/usr/bin/env bash
exit 73
EOF
chmod +x "$fixture_dir/mock-bin/mv"
before="$(hash_file "$env_file")"
if PATH="$fixture_dir/mock-bin:$PATH" run_merge "$fixture_dir/write-failure-example" "$env_file" >"$fixture_dir/write-failure.stdout" 2>"$fixture_dir/write-failure.stderr"; then fail 'simulated rename failure unexpectedly succeeded'; fi
[[ "$(hash_file "$env_file")" == "$before" ]] || fail 'simulated rename failure changed env'
! grep -Fq 'write-failure-secret-must-not-print' "$fixture_dir/write-failure.stdout" "$fixture_dir/write-failure.stderr" || fail 'write failure disclosed a value'
[[ -z "$(find "$fixture_dir" -maxdepth 1 \( -name '*.merge.??????' -o -name '*.snapshot.??????' \) -print -quit)" ]] || fail 'temporary or snapshot file remained after failure'

assert_concurrent_change_survives() {
    local kind="$1" race_env state before
    race_env="$fixture_dir/race-$kind.env"
    state="$fixture_dir/race-$kind.state"
    printf 'BASE=original\n' >"$race_env"
    chmod 640 "$race_env"
    before="$(hash_file "$race_env")"
    if MERGE_TEST_MUTATION="$kind" MERGE_TEST_ENV="$race_env" MERGE_TEST_STATE="$state" run_merge "$fixture_dir/write-failure-example" "$race_env" >"$fixture_dir/race-$kind.stdout" 2>"$fixture_dir/race-$kind.stderr"; then
        fail "$kind concurrent mutation unexpectedly succeeded"
    fi
    if [[ "$kind" == metadata ]]; then
        [[ "$(mode_for "$race_env")" == 600 ]] || fail 'metadata mutation did not occur after snapshot'
    else
        [[ "$(hash_file "$race_env")" != "$before" ]] || fail "$kind mutation did not occur after snapshot"
    fi
    grep -Fq 'CONCURRENT=' "$race_env" || { [[ "$kind" == metadata ]] || fail "$kind concurrent content was lost"; }
    ! grep -Fq 'WRITE_FAILURE_KEY=' "$race_env" || fail "$kind race replaced the concurrently changed env"
}
assert_concurrent_change_survives content
assert_concurrent_change_survives path-swap
assert_concurrent_change_survives metadata

printf 'FIRST_KEY=one\n' >"$fixture_dir/concurrent-first.example"
printf 'SECOND_KEY=two\n' >"$fixture_dir/concurrent-second.example"
printf 'BASE=original\n' >"$fixture_dir/concurrent.env"
MERGE_TEST_MUTATION=pause MERGE_TEST_ENV="$fixture_dir/concurrent.env" MERGE_TEST_STATE="$fixture_dir/concurrent.state" \
    run_merge "$fixture_dir/concurrent-first.example" "$fixture_dir/concurrent.env" >"$fixture_dir/concurrent-first.stdout" 2>"$fixture_dir/concurrent-first.stderr" &
first_pid=$!
sleep 0.1
run_merge "$fixture_dir/concurrent-second.example" "$fixture_dir/concurrent.env" >"$fixture_dir/concurrent-second.stdout" 2>"$fixture_dir/concurrent-second.stderr" &
second_pid=$!
wait "$first_pid" || fail 'first concurrent merge failed'
wait "$second_pid" || fail 'second concurrent merge failed'
grep -Fqx 'FIRST_KEY=one' "$fixture_dir/concurrent.env" || fail 'first concurrent key was lost'
grep -Fqx 'SECOND_KEY=two' "$fixture_dir/concurrent.env" || fail 'second concurrent key was lost'

if command -v xattr >/dev/null 2>&1 && xattr -w com.porsche.merge-test preserved "$fixture_dir/concurrent.env" 2>/dev/null; then
    printf 'XATTR_KEY=value\n' >"$fixture_dir/xattr.example"
    run_merge "$fixture_dir/xattr.example" "$fixture_dir/concurrent.env" >"$fixture_dir/xattr.stdout"
    [[ "$(xattr -p com.porsche.merge-test "$fixture_dir/concurrent.env")" == preserved ]] || fail 'merge lost xattr metadata'
else
    printf 'SKIP: xattr preservation is not supported on this platform\n'
fi

if command -v getfacl >/dev/null 2>&1 && command -v setfacl >/dev/null 2>&1; then
    printf 'ACL_KEY=value\n' >"$fixture_dir/acl.example"
    acl_before="$(getfacl -cp "$fixture_dir/concurrent.env")"
    run_merge "$fixture_dir/acl.example" "$fixture_dir/concurrent.env" >"$fixture_dir/acl.stdout"
    [[ "$(getfacl -cp "$fixture_dir/concurrent.env")" == "$acl_before" ]] || fail 'merge lost ACL metadata'
else
    printf 'SKIP: ACL preservation tools are not available\n'
fi
! grep -Fq 'must-survive' "$fixture_dir"/race-*.stdout "$fixture_dir"/race-*.stderr || fail 'race diagnostics disclosed a value'
[[ -z "$(find "$fixture_dir" -maxdepth 1 \( -name '*.merge.??????' -o -name '*.snapshot.??????' \) -print -quit)" ]] || fail 'temporary or snapshot file remained after race checks'

for invalid_args in zero one three relative; do
    case "$invalid_args" in
        zero) args=() ;;
        one) args=("$example") ;;
        three) args=("$example" "$env_file" "$env_file") ;;
        relative) args=("example" "$env_file") ;;
    esac
    before="$(hash_file "$env_file")"
    if run_merge ${args[@]+"${args[@]}"} >"$fixture_dir/args.stdout" 2>"$fixture_dir/args.stderr"; then fail "$invalid_args arguments unexpectedly succeeded"; fi
    [[ "$(hash_file "$env_file")" == "$before" ]] || fail "$invalid_args arguments changed env"
done

printf 'PASS: additive environment merge checks\n'
