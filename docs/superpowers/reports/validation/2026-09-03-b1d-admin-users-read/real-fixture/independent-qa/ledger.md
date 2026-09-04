# B1-D independent QA ledger

Date: 2026-09-04. Scope: retained, task-only MySQL/Redis fixture.

- Static baseline: all 9 frozen production SHA256 values matched `manifest.json`.
- Contract review: the only historical role expectation changes are Admin reading an equal-role Admin or Root through legacy reads, from 403 to hidden-target 404. This matches the frozen B1-D strict lower-target contract.
- Fixture identity: exact task IDs, names, label, images, AutoRemove, tmpfs, loopback-only ports, and lack of named volumes were inspected before reset. No conflicting resource was adopted.
- Reset/migration: only the task database was recreated and only the task Redis was flushed. Existing migrations 0001--0003 completed and their non-secret checksums were recorded in `migration-up.log` and `migration-status.log`.
- Environment: private `fixture.env` is loaded silently for each command. Application variables are unset; tests receive only `TEST_DATABASE_URL` and `TEST_REDIS_URL`. No URL or secret is written in this directory.
- Sandbox note: the sandbox denied loopback access before connection. Authorized out-of-sandbox processes are used for the fixture migration and tests; the failed sandbox migration log is deliberately not retained as QA evidence.

Raw command output is stored in this directory. Tests are serial while they can mutate the fixture.
