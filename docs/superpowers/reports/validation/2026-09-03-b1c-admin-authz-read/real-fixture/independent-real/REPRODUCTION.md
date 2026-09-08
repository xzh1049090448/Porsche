# Reproducing the independent real-fixture probe

The original report, JSON logs and overlay retain historical source paths. The
fixture credentials have a deliberately limited lifecycle and must not be reused
after cleanup. Reproduction requires a newly authorized disposable fixture.

Copy `adversarial-http-probe/admin_authz_overlay.go.txt` to a private temporary
`.go` file, regenerate the overlay mapping for the current
`internal/handler/admin_authz_test.go`, and run the exact test expression documented
in `security-report.txt` against explicit TEST_DATABASE_URL/TEST_REDIS_URL.
The `.go.txt` suffix keeps archived evidence outside Go package scanning; source
bytes are preserved. Do not copy the probe into a live production source directory.
