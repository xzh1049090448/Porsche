# B1-D resource ledger (non-secret)

- Task label: `codex.task=b1d-admin-users-read-260904`
- MySQL container name: `b1d-admin-users-read-260904-mysql`
- MySQL container ID: `94e8463c0411eeceb4b31d3a5ff78f9da715eec937e7d3cd4e48b5fd25680fcd`
- MySQL image ID: `sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b`
- MySQL version: `8.0.46`
- MySQL binding: `127.0.0.1:60366 -> 3306/tcp`
- Database: `porsche_b1d_admin_users_260904_test`
- Redis container name: `b1d-admin-users-read-260904-redis`
- Redis container ID: `2711acfbc9aa7b3784b0b7b81854645b5c8f740e170bf7cb5c7290eaff3ebad0`
- Redis image ID: `sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf`
- Redis version: `7.4.11`
- Redis binding: `127.0.0.1:60488 -> 6379/tcp`
- AutoRemove: `true` for both containers
- Named volumes: none
- tmpfs: MySQL `/var/lib/mysql` and `/tmp`; Redis `/data`
- Per-container CPU/memory limit: none (`NanoCpus=0`, `Memory=0`, `CpuQuota=0`)
- Historical retained state before final QA: both containers running; migration rows `3`; users `100013`; Redis DB keys `7`.
- Final state: independent H3 QA PASS后按授权精确清理；两个容器ID/名称均不存在、同task label残留0、私密任务目录不存在。无named volume、prune或其它容器清理操作。
- Private task path and credential contents are intentionally excluded; Root receives the path out of band.

The retained 100013 rows are 100000 performance users plus 13 focused/actor
fixture users. They exist only inside the task MySQL tmpfs and are removed with
the exact task container.
