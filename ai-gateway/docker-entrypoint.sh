#!/bin/sh
set -e
# Named volume mounts at /app/data are root-owned; ensure appuser can write chroma/uploads.
mkdir -p /app/data/chroma /app/data/uploads
chown -R appuser:appuser /app/data
exec gosu appuser "$@"
