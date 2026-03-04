#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

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

echo "==> Restarting container..."
docker compose down
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
