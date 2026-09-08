# A14 Task 18 exact cleanup manifest

Status: `CLEANED` at `2026-09-06T12:59:42+08:00`.

The previous suffix `20260905125101-20c53025`, PIDs `90334`/`90407`, and
listeners `57178`/`55792` disappeared during a host restart together with their
AutoRemove containers and `/tmp` files. That unexpected historical absence is
not Step 6 cleanup evidence and is not an accepted cleanup target.

The first replacement process holders, PIDs `4020`/`4045`/`4084`, later ended
with their agent lifetime. This second unexpected absence also earns no Step 6
cleanup credit.

Only the replacement resources below were accepted Task 18 cleanup objects.
They remained live through independent re-review and were then cleaned:

- root-held backend exec-session PID `10318`, listener `127.0.0.1:57181`;
- root-held route-bridge exec-session PID `10331`, listener
  `127.0.0.1:8000`;
- root-held frontend exec-session PID `10376`, listener `127.0.0.1:55795`;
- MySQL ID
  `d1e96a2f780a4c604168c320e690f4685e467ace39edd9d701974903ebd807a3`,
  name `a14-user-delete-mysql-20260906031001-95b2aeb3`;
- Redis ID
  `365aa2d572c00f438db7c234273d82388b70d8de186d60b724174e7fa526596d`,
  name `a14-user-delete-redis-20260906031001-95b2aeb3`;
- task label `codex.task=a14-user-delete-20260906031001-95b2aeb3`, retention
  label `codex.retention=task-18-rerun`, namespace
  `a14_user_delete_20260906031001-95b2aeb3`, child
  `a14_user_delete_20260906031001-95b2aeb3_test`, and Redis DB 8.

Before cleanup, verify the exact PID/listener ownership and both full container
IDs, names, pinned image IDs, labels, `AutoRemove=true`, read-only roots,
tmpfs, loopback bindings and health. Stop immediately on a mismatch. Do not
use wildcard deletion, broad process termination, Docker prune, volume
removal, or an unrelated database operation.

The manifest currently enumerates all `60` exact Task 18 `/tmp` files: `45`
suffix-owned files and `15` Playwright/bridge helper files. There is no glob in
the pending deletion commands.

## Exact cleanup commands

```sh
set -eu
backend_pid=10318
bridge_pid=10331
frontend_pid=10376
mysql_name=a14-user-delete-mysql-20260906031001-95b2aeb3
redis_name=a14-user-delete-redis-20260906031001-95b2aeb3
mysql_id=d1e96a2f780a4c604168c320e690f4685e467ace39edd9d701974903ebd807a3
redis_id=365aa2d572c00f438db7c234273d82388b70d8de186d60b724174e7fa526596d
mysql_image_id=sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b
redis_image_id=sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf
mysql_image_digest='["mysql@sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b"]'
redis_image_digest='["redis@sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf"]'
task_label=a14-user-delete-20260906031001-95b2aeb3
retention_label=task-18-rerun

# Every preflight check completes before the first mutation.
test "$(lsof -nP -t -iTCP:57181 -sTCP:LISTEN)" = "$backend_pid"
test "$(lsof -nP -t -iTCP:8000 -sTCP:LISTEN)" = "$bridge_pid"
test "$(lsof -nP -t -iTCP:55795 -sTCP:LISTEN)" = "$frontend_pid"
test "$(ps -p "$backend_pid" -o command=)" = "/tmp/a14-user-delete-20260906031001-95b2aeb3-server"
test "$(ps -p "$bridge_pid" -o command=)" = "node /tmp/playwright-test-a14-route-bridge.cjs"
test "$(ps -p "$frontend_pid" -o command=)" = "node /Users/xuzhihao/code/Porsche-Web/.worktrees/admin-public-260903/node_modules/.bin/vite --host 127.0.0.1 --port 55795 --strictPort"
test "$(docker inspect -f '{{.Id}}' "$mysql_name")" = "$mysql_id"
test "$(docker inspect -f '{{.Name}}' "$mysql_name")" = "/$mysql_name"
test "$(docker inspect -f '{{.Image}}' "$mysql_name")" = "$mysql_image_id"
test "$(docker image inspect -f '{{json .RepoDigests}}' "$mysql_image_id")" = "$mysql_image_digest"
test "$(docker inspect -f '{{index .Config.Labels "codex.task"}}' "$mysql_name")" = "$task_label"
test "$(docker inspect -f '{{index .Config.Labels "codex.retention"}}' "$mysql_name")" = "$retention_label"
test "$(docker inspect -f '{{.HostConfig.AutoRemove}}' "$mysql_name")" = true
test "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$mysql_name")" = true
test "$(docker inspect -f '{{.State.Running}}' "$mysql_name")" = true
test "$(docker inspect -f '{{.State.Health.Status}}' "$mysql_name")" = healthy
test "$(docker inspect -f '{{index .HostConfig.Tmpfs "/var/lib/mysql"}}' "$mysql_name")" = "rw,nosuid,size=1536m"
test "$(docker inspect -f '{{index .HostConfig.Tmpfs "/var/run/mysqld"}}' "$mysql_name")" = "rw,nosuid,noexec,size=32m"
test "$(docker inspect -f '{{index .HostConfig.Tmpfs "/var/lib/mysql-files"}}' "$mysql_name")" = "rw,nosuid,noexec,size=32m"
test "$(docker inspect -f '{{index .HostConfig.Tmpfs "/tmp"}}' "$mysql_name")" = "rw,nosuid,noexec,size=64m"
test "$(docker inspect -f '{{len .HostConfig.Tmpfs}}' "$mysql_name")" = 4
test "$(docker inspect -f '{{len .Mounts}}' "$mysql_name")" = 0
test "$(docker inspect -f '{{len .NetworkSettings.Ports}}' "$mysql_name")" = 1
test "$(docker inspect -f '{{(index (index .NetworkSettings.Ports "3306/tcp") 0).HostIp}}:{{(index (index .NetworkSettings.Ports "3306/tcp") 0).HostPort}}' "$mysql_name")" = "127.0.0.1:56060"
test "$(docker inspect -f '{{.Id}}' "$redis_name")" = "$redis_id"
test "$(docker inspect -f '{{.Name}}' "$redis_name")" = "/$redis_name"
test "$(docker inspect -f '{{.Image}}' "$redis_name")" = "$redis_image_id"
test "$(docker image inspect -f '{{json .RepoDigests}}' "$redis_image_id")" = "$redis_image_digest"
test "$(docker inspect -f '{{index .Config.Labels "codex.task"}}' "$redis_name")" = "$task_label"
test "$(docker inspect -f '{{index .Config.Labels "codex.retention"}}' "$redis_name")" = "$retention_label"
test "$(docker inspect -f '{{.HostConfig.AutoRemove}}' "$redis_name")" = true
test "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$redis_name")" = true
test "$(docker inspect -f '{{.State.Running}}' "$redis_name")" = true
test "$(docker inspect -f '{{.State.Health.Status}}' "$redis_name")" = healthy
test "$(docker inspect -f '{{index .HostConfig.Tmpfs "/data"}}' "$redis_name")" = "rw,nosuid,noexec,size=256m"
test "$(docker inspect -f '{{index .HostConfig.Tmpfs "/tmp"}}' "$redis_name")" = "rw,nosuid,noexec,size=32m"
test "$(docker inspect -f '{{len .HostConfig.Tmpfs}}' "$redis_name")" = 2
test "$(docker inspect -f '{{len .Mounts}}' "$redis_name")" = 0
test "$(docker inspect -f '{{len .NetworkSettings.Ports}}' "$redis_name")" = 1
test "$(docker inspect -f '{{(index (index .NetworkSettings.Ports "6379/tcp") 0).HostIp}}:{{(index (index .NetworkSettings.Ports "6379/tcp") 0).HostPort}}' "$redis_name")" = "127.0.0.1:56065"
test "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:57181/health)" = 200
test "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:55795/)" = 200

kill "$backend_pid"
kill "$bridge_pid"
kill "$frontend_pid"
docker stop "$mysql_name"
docker stop "$redis_name"
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-api-matrix.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-backend-build.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-backend-diff-check.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-backend-full-corrected.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-backend-full-final.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-backend-full-rerun.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-backend-race-corrected.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-backend-vet.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-backend.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-backend.pid
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-bootstrap-for-rereview.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-bootstrap.sh
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-create.sh
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-final-credential-rejections.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-final-migrate.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-final-readback-sanitized.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-final-role-seed.sh
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-final-sanitized-readback.sh
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-final-ui-successes.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-final-users.private.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-frontend-build.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-frontend-diff-check.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-frontend-test-corrected.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-frontend-test.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-migrate-before-full.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-migrate-before-race.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-migrate-for-rereview.log
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-plan.txt
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-readback-sanitized.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-rereview-smoke.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-reset.sh
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-role-seed.sh
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-root.credentials
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-run-full.sh
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-run-race.sh
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-sanitized-readback.sh
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-sensitive-ui-lifecycle.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-server
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-ui-eligibility.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-ui-recovery.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-ui-successes.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3-users.private.json
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3.env
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3.ids
rm -f /tmp/a14-user-delete-20260906031001-95b2aeb3.public
rm -f /tmp/playwright-test-a14-add-post-target.js
rm -f /tmp/playwright-test-a14-debug-list.js
rm -f /tmp/playwright-test-a14-debug-ui.js
rm -f /tmp/playwright-test-a14-extend-users.js
rm -f /tmp/playwright-test-a14-final-credential-rejection.js
rm -f /tmp/playwright-test-a14-final-seed.js
rm -f /tmp/playwright-test-a14-final-successes.js
rm -f /tmp/playwright-test-a14-real-api-matrix.js
rm -f /tmp/playwright-test-a14-real-sensitive-lifecycle.js
rm -f /tmp/playwright-test-a14-real-smoke.js
rm -f /tmp/playwright-test-a14-real-successes.js
rm -f /tmp/playwright-test-a14-route-bridge.cjs
rm -f /tmp/playwright-test-a14-seed-users.js
rm -f /tmp/playwright-test-a14-ui-eligibility.js
rm -f /tmp/playwright-test-a14-ui-recovery-matrix.js
```

## Executed result

The full `42`-test read-only preflight above passed before the first cleanup
attempt. The first mutation-segment extractor omitted the variable assignments:
all three empty-value `kill` calls and both empty-name Docker stops failed
without touching a process or container, while the following 60 exact `rm`
commands completed. This harness error is retained here; it was not a product
failure and did not affect any non-task path.

Before retrying any process/container mutation, the complete 42-test preflight
passed a second time against PIDs `10318`/`10331`/`10376` and the two unchanged
full container identities. The three exact PIDs were then killed and only the
two exact names above were stopped. `AutoRemove=true` removed both containers.
Docker Desktop and every unrelated container remained untouched.

Final zero-residual proof passed:

- PIDs `10318`/`10331`/`10376` are absent and ports `57181`/`8000`/`55795`
  have no listener.
- Both replacement full IDs and exact names are absent. Containers, networks
  and volumes with task label
  `codex.task=a14-user-delete-20260906031001-95b2aeb3` each count zero.
- The tmpfs-backed namespace and child ceased with the exact MySQL container;
  no separately retained database or named volume exists.
- All 60 enumerated `/tmp` paths are absent, including env, credential, reset,
  helper and result artifacts. Playwright skill `.temp-execution-*` count is
  zero.
- The older suffix `20260905125101-20c53025` remains classified as host-restart
  unexpected absence; only suffix `20260906031001-95b2aeb3` receives this
  Step 6 cleanup result.
