#!/usr/bin/env bash
# Deploy only the application container. Database services are intentionally out of scope.
set -Eeuo pipefail

APP_NAME="${APP_NAME:-ai-gateway-go}"
IMAGE_NAME="${IMAGE_NAME:-ai-gateway-go:main}"
HOST_PORT="${HOST_PORT:-8000}"
APP_DOCKER_NETWORK="${APP_DOCKER_NETWORK:-}"

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-$repo_root/.env}"

cd "$repo_root"
command -v git >/dev/null
command -v docker >/dev/null
command -v curl >/dev/null

if [[ ! "$HOST_PORT" =~ ^[1-9][0-9]{0,4}$ ]] || (( HOST_PORT > 65535 )); then
    echo 'HOST_PORT must be an integer from 1 through 65535' >&2
    exit 1
fi

if [[ ! -f "$ENV_FILE" ]]; then
    echo 'ENV_FILE must name an existing regular file' >&2
    exit 1
fi

# Deliberately exclude untracked files so operators can retain local credentials.
git diff --quiet
git diff --cached --quiet

git fetch origin main
git switch main
git reset --hard origin/main

if [[ -n "$APP_DOCKER_NETWORK" ]]; then
    docker network inspect "$APP_DOCKER_NETWORK" >/dev/null
fi

candidate_name="${APP_NAME}-candidate-$$"
candidate_started=false
cleanup_candidate() {
    if [[ "$candidate_started" == true ]]; then
        docker rm -f "$candidate_name" >/dev/null 2>&1 || true
    fi
}
trap cleanup_candidate EXIT

docker build --tag "$IMAGE_NAME" .
if [[ -n "$APP_DOCKER_NETWORK" ]]; then
    docker run -d --name "$candidate_name" --env-file "$ENV_FILE" \
        --publish "127.0.0.1:${HOST_PORT}:8000" --network "$APP_DOCKER_NETWORK" \
        "$IMAGE_NAME" >/dev/null
else
    docker run -d --name "$candidate_name" --env-file "$ENV_FILE" \
        --publish "127.0.0.1:${HOST_PORT}:8000" "$IMAGE_NAME" >/dev/null
fi
candidate_started=true

healthy=false
for _ in {1..30}; do
    if curl -fsS "http://127.0.0.1:${HOST_PORT}/health" >/dev/null; then
        healthy=true
        break
    fi
    sleep 1
done
if [[ "$healthy" != true ]]; then
    echo 'candidate health check did not succeed within 30 seconds' >&2
    exit 1
fi

docker rm -f "$APP_NAME" >/dev/null 2>&1 || true
docker rename "$candidate_name" "$APP_NAME"
candidate_started=false
trap - EXIT
