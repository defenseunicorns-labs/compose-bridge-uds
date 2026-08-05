#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

docker compose --file compose.yaml --file compose.dev.yaml \
  down --volumes --remove-orphans

kubectl delete namespace react-fastapi-postgres --ignore-not-found

rm -rf -- .tmp logs out packages
