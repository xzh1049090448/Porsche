#!/usr/bin/env bash
set -Eeuo pipefail

if (($# != 0)); then
    echo 'usage: restart-all.sh' >&2
    exit 64
fi

if [[ "$(id -u)" != '0' ]]; then
    echo 'restart-all.sh must be run as root' >&2
    exit 1
fi

# Production values are deliberately fixed. Test-only overrides make the shell
# regression fixture possible without allowing deployment configuration drift.
BACKEND_DIR=/opt/Porsche
FRONTEND_DIR=/opt/Porsche-Web
FRONTEND_ROOT=/var/www/porsche-web
APP_DOCKER_NETWORK=porsche-app
LOCK_FILE=/var/lock/porsche-full-stack.deploy.lock
STAGE_PARENT=/var/www
if [[ "${PORSCHE_RESTART_TEST_MODE:-}" == '1' ]]; then
    BACKEND_DIR="${PORSCHE_RESTART_BACKEND_DIR:-$BACKEND_DIR}"
    FRONTEND_DIR="${PORSCHE_RESTART_FRONTEND_DIR:-$FRONTEND_DIR}"
    FRONTEND_ROOT="${PORSCHE_RESTART_FRONTEND_ROOT:-$FRONTEND_ROOT}"
    LOCK_FILE="${PORSCHE_RESTART_LOCK_FILE:-$LOCK_FILE}"
    STAGE_PARENT="${PORSCHE_RESTART_STAGE_PARENT:-$STAGE_PARENT}"
fi

for command_name in git docker npm rsync nginx systemctl flock; do
    command -v "$command_name" >/dev/null 2>&1 || {
        echo "required command is unavailable: $command_name" >&2
        exit 1
    }
done

for repository in "$BACKEND_DIR" "$FRONTEND_DIR"; do
    git -C "$repository" rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
        echo "not a git work tree: $repository" >&2
        exit 1
    }
done

[[ -f "$BACKEND_DIR/.env" ]] || { echo "missing backend environment file: $BACKEND_DIR/.env" >&2; exit 1; }
[[ -f "$FRONTEND_DIR/package.json" ]] || { echo "missing frontend package.json: $FRONTEND_DIR/package.json" >&2; exit 1; }
[[ -x "$BACKEND_DIR/deploy/production-deploy.sh" ]] || { echo "missing backend deployment script" >&2; exit 1; }
docker network inspect "$APP_DOCKER_NETWORK" >/dev/null

exec 9>"$LOCK_FILE"
flock -E 75 -n 9

(
    cd "$FRONTEND_DIR"
    git diff --quiet
    git diff --cached --quiet
    git fetch origin main
    git switch main
    git reset --hard origin/main
    npm install --package-lock=false
    npm run build
)

(
    cd "$BACKEND_DIR"
    APP_DOCKER_NETWORK="$APP_DOCKER_NETWORK" ./deploy/production-deploy.sh
)

nginx -t

stage_dir="$(mktemp -d "$STAGE_PARENT/.porsche-web-stage.XXXXXX")"
cleanup_stage() {
    if [[ -n "${stage_dir:-}" && -d "$stage_dir" ]]; then
        rm -rf -- "$stage_dir"
    fi
}
trap cleanup_stage EXIT

rsync --archive --delete --delay-updates "$FRONTEND_DIR/dist/" "$stage_dir/"
# Do not publish caller/build-tool umask into the Nginx document root.
find "$stage_dir" -type d -exec chmod 0755 {} +
find "$stage_dir" -type f -exec chmod 0644 {} +
rsync --archive --delete --delay-updates "$stage_dir/" "$FRONTEND_ROOT/"

systemctl reload nginx

backend_revision="$(git -C "$BACKEND_DIR" rev-parse HEAD)"
frontend_revision="$(git -C "$FRONTEND_DIR" rev-parse HEAD)"
printf 'full stack deployment succeeded: backend=%s frontend=%s\n' "$backend_revision" "$frontend_revision"
