#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

cd "${REPO_ROOT}"

GIT_TAG="${GIT_TAG:-v0.60.3-bt}"
GIT_SHA="$(git rev-parse --short HEAD)"
IMAGE="navidrome-bt:dev"

echo "==> Building image ${IMAGE} (${GIT_TAG} @ ${GIT_SHA})..."
docker buildx build \
  --platform linux/amd64 \
  --build-arg GIT_TAG="${GIT_TAG}" \
  --build-arg GIT_SHA="${GIT_SHA}" \
  --tag "${IMAGE}" \
  --load \
  .

echo "==> Stopping and removing existing containers..."
docker compose down --remove-orphans
# Also remove any standalone container with the same project name
CONTAINER_NAME="${COMPOSE_PROJECT_NAME:-$(basename "$(pwd)")}"
if docker ps -aq --filter "name=${CONTAINER_NAME}" | grep -q .; then
  docker rm -f $(docker ps -aq --filter "name=${CONTAINER_NAME}") 2>/dev/null || true
fi

echo "==> Starting container..."
docker compose up -d

echo "==> Done. Waiting for health..."
sleep 2
if curl -sf -o /dev/null http://localhost:4538/app/; then
  echo "==> Navidrome is up at http://localhost:4538"
else
  echo "==> Container started. Check logs: docker compose logs -f"
fi
echo ""
echo "NOTE: Clear browser Service Worker + hard refresh (Ctrl+Shift+R) to load new UI."
