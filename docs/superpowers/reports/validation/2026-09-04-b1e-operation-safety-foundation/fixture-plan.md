# B1-E Task 12 isolated fixture plan

- Frozen report date: `2026-09-04`
- Execution date: `2026-09-05`
- Baseline commit: `a0056a9013b4d2572058c50c356bc492862ae073`
- Task label: `porsche-b1e-260905-t12-a0056a9-c71f`
- MySQL container name: `porsche-b1e-260905-t12-a0056a9-c71f-mysql`
- Redis container name: `porsche-b1e-260905-t12-a0056a9-c71f-redis`
- Private directory template: `/private/tmp/porsche-b1e-260905-t12-a0056a9-c71f.XXXXXX`
- MySQL image ID and digest: `sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b`, `mysql@sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b`
- Redis image ID and digest: `sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`, `redis@sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`

The executor will create the private directory with mode `0700`, set `umask 077`, and keep every private file at mode `0600`. Secrets are generated and consumed only from that directory. They must not appear in command arguments, stdout, Git, reports, or captured public logs. The MySQL root password is supplied only through the image-supported password-file interface from one read-only bind mount. Redis is bound to loopback without authentication because its random host port and container are isolated and disposable; no Redis secret is generated.

Both containers use `--rm`, `--read-only`, task label `codex.task=porsche-b1e-260905-t12-a0056a9-c71f`, loopback-only random host ports, and no named volumes. MySQL uses a tmpfs at `/var/lib/mysql` with `rw,noexec,nosuid,size=768m`; Redis uses a tmpfs at `/data` with `rw,noexec,nosuid,size=128m`. The only bind mount is the MySQL password file mounted read-only at `/run/secrets/mysql-root-password`.

Immediately after creation, the executor stores complete container IDs in a private mode-`0600` identity file and verifies exact full ID, name, task label, immutable image ID, `AutoRemove=true`, mounts, tmpfs, loopback binding, and absence of named volumes. Any mismatch stops the workflow before migration or tests. If the immutable images cannot honor this isolation contract, the executor stops only the exact full IDs created by this task and reports the fixture blocked; isolation will not be weakened.

Only the two full container IDs recorded by this task may be stopped. Cleanup must first repeat the complete identity comparison, then stop those exact IDs and remove the one recorded private directory only after checking ownership, mode, non-symlink status, and its task marker. `docker prune`, globs, named volumes, `docker compose down -v`, `docker volume rm`, parent-directory recursion, partial container IDs, and broad process cleanup are forbidden.

The fixture will remain alive after Task 12 implementation verification for independent QA. It will not be used for production deployment, production migration, application traffic, model/chat/SSE, upstream requests, or frontend changes.

## Independent QA remediation retention

After the first independent `QA_FAIL`, the same exact full-ID containers remain retained. Remediation uses fresh task-only MySQL child databases and distinct Redis logical databases or unique canonical key identities. Before the second QA handoff, the executor verifies the private root realpath, owner and exact task marker without following symlinks, then enforces mode `0700` on every directory and `0600` on every regular file. All failed raw attempts remain in the private directory; reports contain only redacted counts and hashes. Cleanup remains forbidden until the fresh second QA verdict.

## Second independent QA

The fresh second independent review returned `QA_PASS` for the retained full-ID fixture and reviewed commit `9dbad0512f5c5fa69d7202ab3a3cc2132f0cdc41`. Its signed private result is represented in repository evidence only by SHA-256, verdict, redacted counts, and public fixture identity. Two non-product QA environment setup failures remain archived with the earlier failure history. The fixture remains alive; cleanup and Task 13 remain outside this evidence update.
