#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

# UDS reads deployment configuration separately from the bundle archive. Point
# it at the tracked config that connects the app to the bundled PostgreSQL.
UDS_CONFIG="$PWD/uds/bundle/uds-config.yaml" \
  uds deploy uds/bundle/uds-bundle-*.tar.zst "$@" --confirm
