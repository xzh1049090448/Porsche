# B1-D H3 independent QA ledger

Date: 2026-09-04. Scope: retained task-only MySQL and Redis fixture; source review and verification only.

- Exact containers were re-inspected before use. IDs, names, task label, frozen image IDs, running state, AutoRemove, tmpfs paths, loopback-only publishing, and the absence of named volumes matched the retained resource ledger.
- Candidate baseline matched before runtime work: manifest SHA256 `26c063e0d31a4aa10bbd41df3396e965cdb9d186a88eecb9e0d23577aa58e2a5`; H3 implementation SHA256 `7a20df0cb257feb393f930b0d006fc88795acbc0b9dead2784f5ae3b61d4f715`; immutable 0004 checksum `44b5caba0473162c239e6b3035e6d9067e3af0b35e998a79c827a1424621494e`.
- Every mutable phase will reset only the task database and task Redis, migrate 0001--0004 using the private fixture values, and run serially. Private URLs and credentials are loaded silently and never copied to this directory.
- No production source, migration, test assertion, retained writer evidence, container, private directory, or cleanup state is modified by this QA run.
