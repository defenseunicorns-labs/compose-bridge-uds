#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

mkdir -p .tmp packages

# Build the local Compose Bridge transformation image.
docker build --tag compose-bridge-uds:dev ../..

# Convert the Compose application into the generated package workspace.
# Include the conversion-only image overlay as a temporary workaround pending
# an upstream Docker Compose contribution. It can be removed when Compose Bridge
# stops resolving images for services that declare build:.
TMPDIR="$PWD/.tmp" docker compose \
  --file compose.yaml \
  --file compose.bridge.yaml \
  bridge convert \
  --transformation compose-bridge-uds:dev \
  --output "$PWD/out"

# Build the application Zarf package consumed by the bundle.
uds zarf package create out \
  --output packages \
  --confirm

# Build the complete development bundle containing PostgreSQL and the app.
uds create bundle \
  --output packages \
  --confirm
