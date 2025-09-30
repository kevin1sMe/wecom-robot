#!/usr/bin/env sh
set -eu

# Simple local build helper for wecom-robot
# Usage:
#   build/local-build.sh [tag]
# Examples:
#   build/local-build.sh wecom-robot:local

TAG=${1:-wecom-robot:local}

docker buildx build \
  --platform linux/amd64 \
  --load \
  -f build/wecom-robot/Dockerfile \
  -t "$TAG" \
  .

echo "Built image: $TAG"
