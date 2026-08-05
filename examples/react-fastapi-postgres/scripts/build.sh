#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p .tmp packages

docker build --tag compose-bridge-uds:dev ../..

# Include the conversion-only image overlay as a temporary workaround pending
# an upstream Docker Compose contribution. It can be removed when Compose Bridge
# stops resolving images for services that declare build:.
TMPDIR="$PWD/.tmp" docker compose \
  --file compose.yaml \
  --file compose.bridge.yaml \
  bridge convert \
  --transformation compose-bridge-uds:dev \
  --output "$PWD/out"

uds zarf package create out \
  --output packages \
  --confirm
