#!/bin/sh
# Static regression checks for the production Nginx proxy contract.
set -eu

config_file="$(dirname "$0")/aiportcloud.conf"
readme_file="$(dirname "$0")/../../README.md"
env_example_file="$(dirname "$0")/../../.env.example"

require_line() {
    file="$1"
    line="$2"
    if ! grep -Fqx "$line" "$file"; then
        echo "missing required line in $file: $line" >&2
        exit 1
    fi
}

reject_line() {
    file="$1"
    line="$2"
    if grep -Fqx "$line" "$file"; then
        echo "unsafe line in $file: $line" >&2
        exit 1
    fi
}

# The Go service trusts this Nginx peer, so forwarding a client-supplied XFF
# chain would let the client choose the address evaluated by IP allowlists.
reject_line "$config_file" '        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;'
require_line "$config_file" '        proxy_set_header X-Forwarded-For $remote_addr;'

# Keep long-lived OpenAI-compatible SSE frames unbuffered and on HTTP/1.1.
require_line "$config_file" '        proxy_http_version 1.1;'
require_line "$config_file" '        proxy_buffering off;'
require_line "$config_file" '        proxy_read_timeout 300s;'
require_line "$config_file" '        proxy_send_timeout 300s;'

# Deployment documentation must make proxy trust an explicit topology decision.
grep -Fq 'TRUSTED_PROXY_CIDRS' "$readme_file"
grep -Fq 'TRUST_PROXY_HEADERS=true' "$readme_file"
grep -Fq 'actual source IP or CIDR' "$readme_file"
grep -Fq 'Docker gateway' "$readme_file"
grep -Fq 'actual source IP or CIDR' "$env_example_file"
