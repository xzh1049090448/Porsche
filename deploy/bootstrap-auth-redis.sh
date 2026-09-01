#!/usr/bin/env bash
set -Eeuo pipefail

(( $# == 0 )) || { echo 'bootstrap-auth-redis.sh accepts no arguments' >&2; exit 64; }
[[ "$(id -u)" == 0 ]] || { echo 'bootstrap-auth-redis.sh must run as root' >&2; exit 1; }

config_dir=/var/lib/porsche-auth-redis
password_file=''
if [[ "${PORSCHE_AUTH_ACCEPTANCE_TEST_MODE:-0}" == 1 ]]; then
    config_dir="${PORSCHE_AUTH_ACCEPTANCE_REDIS_CONFIG_DIR:?test Redis config directory is required}"
    password_file="${PORSCHE_AUTH_ACCEPTANCE_TEST_PASSWORD_FILE:?test password file is required}"
fi

docker network inspect porsche-app >/dev/null
if docker container inspect porsche-redis >/dev/null 2>&1; then
    echo 'porsche-redis already exists; refusing replacement' >&2
    exit 1
fi

redis_password=''
if [[ -n "$password_file" ]]; then
    IFS= read -r redis_password <"$password_file" || true
else
    read -rsp 'Redis password (32+ bytes): ' redis_password
    echo
fi
LC_ALL=C
export LC_ALL
[[ ${#redis_password} -ge 32 ]] || { echo 'Redis password must contain at least 32 bytes' >&2; exit 1; }

umask 077
mkdir -p "$config_dir"
config_file="$config_dir/redis.conf"
printf 'appendonly yes\nsave 60 1\nrequirepass %s\n' "$redis_password" >"$config_file"
chmod 600 "$config_file"
unset redis_password

redis_uid="$(docker run --rm --entrypoint id redis:7-alpine -u redis)"
redis_gid="$(docker run --rm --entrypoint id redis:7-alpine -g redis)"
[[ "$redis_uid" =~ ^[0-9]+$ && "$redis_gid" =~ ^[0-9]+$ ]] || {
    echo 'could not resolve the redis:7-alpine runtime user' >&2
    exit 1
}
chown "$redis_uid:$redis_gid" "$config_file"

docker volume create porsche-redis-data >/dev/null
if ! docker run -d --name porsche-redis --restart unless-stopped --network porsche-app \
    --mount type=volume,src=porsche-redis-data,dst=/data \
    --mount "type=bind,src=$config_file,dst=/usr/local/etc/redis/redis.conf,readonly" \
    redis:7-alpine redis-server /usr/local/etc/redis/redis.conf >/dev/null; then
    docker rm -f -- porsche-redis >/dev/null 2>&1 || true
    echo 'porsche-redis failed to start; retained volume and config for inspection' >&2
    exit 1
fi

echo 'porsche-redis started; configure REDIS_URL in /opt/Porsche/.env'
