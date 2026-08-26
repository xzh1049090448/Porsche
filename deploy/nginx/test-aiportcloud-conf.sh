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

require_location_block() {
    location="$1"
    required_line="$2"
    block="$(awk -v location="$location" '
        $0 == "    " location " {" { in_block = 1 }
        in_block { print }
        in_block && $0 == "    }" { exit }
    ' "$config_file")"

    if [ -z "$block" ] || ! printf '%s\n' "$block" | grep -Fqx "$required_line"; then
        echo "missing required line in $location: $required_line" >&2
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

# The TLS virtual host must serve the SPA itself while retaining distinct
# application, gateway, admin, and health-check proxy routes.
require_line "$config_file" '    root /var/www/porsche-web;'
require_line "$config_file" '    index index.html;'
require_location_block 'location /' '        try_files $uri $uri/ /index.html;'

for location in \
    'location /api/' 'location = /api' \
    'location /v1/' 'location = /v1' \
    'location /admin/' 'location = /admin' \
    'location = /health'; do
    require_location_block "$location" '        proxy_pass http://127.0.0.1:8000;'
    require_location_block "$location" '        proxy_http_version 1.1;'
    require_location_block "$location" '        proxy_set_header Host $host;'
    require_location_block "$location" '        proxy_set_header X-Real-IP $remote_addr;'
    require_location_block "$location" '        proxy_set_header X-Forwarded-For $remote_addr;'
    require_location_block "$location" '        proxy_set_header X-Forwarded-Proto $scheme;'
    require_location_block "$location" '        proxy_buffering off;'
    require_location_block "$location" '        proxy_read_timeout 300s;'
    require_location_block "$location" '        proxy_send_timeout 300s;'
done

# Deployment documentation must make proxy trust an explicit topology decision.
grep -Fq 'TRUSTED_PROXY_CIDRS' "$readme_file"
grep -Fq 'TRUST_PROXY_HEADERS=true' "$readme_file"
grep -Fq 'actual source IP or CIDR' "$readme_file"
grep -Fq 'Docker gateway' "$readme_file"
grep -Fq 'actual source IP or CIDR' "$env_example_file"
