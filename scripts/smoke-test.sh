#!/usr/bin/env bash

# Copyright 2024-2026 Defense Unicorns
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
generated_dir="${repo_root}/examples/simple/out"
transformation_image="compose-bridge-uds:smoke-test"

cd "${repo_root}"

docker build --tag "${transformation_image}" "${repo_root}"

(
  cd "${repo_root}/examples/simple"
  docker compose bridge convert \
    --transformation "${transformation_image}" \
    --output "${generated_dir}"
)

uds run \
  --with "path=${generated_dir}" \
  --with "options=--output ${generated_dir} --skip-sbom --no-color" \
  --no-progress \
  create:package

uds run --no-progress setup:k3d-test-cluster

db_password=$(tr -d '\r\n' < "${repo_root}/examples/simple/db_password.txt")
uds run \
  --with "path=${generated_dir}" \
  --with "flavor=upstream" \
  --with "options=--set-variables DB_PASSWORD=${db_password} --no-color" \
  --no-progress \
  deploy:package

uds zarf tools kubectl wait \
  --namespace wordpress \
  --for condition=Available \
  --timeout 10m \
  deployment/db \
  deployment/wordpress

uds run \
  --no-progress \
  setup:keycloak-user

status=""
for _ in $(seq 1 60); do
  status=$(curl --location --silent --output /dev/null --write-out '%{http_code}' \
    https://wordpress.uds.dev || true)
  if [[ "${status}" == "200" ]]; then
    break
  fi
  sleep 5
done

if [[ "${status}" != "200" ]]; then
  echo "WordPress ingress did not become ready; last HTTP status was ${status:-unavailable}." >&2
  exit 1
fi

playwright_version=$(sed -nE 's/.*"@playwright\/test": "([^"]+)".*/\1/p' \
  "${repo_root}/tests/package.json")
if [[ -z "${playwright_version}" ]]; then
  echo "Unable to determine the Playwright version from tests/package.json." >&2
  exit 1
fi

docker run --rm \
  --init \
  --network host \
  --shm-size 1g \
  --security-opt seccomp=unconfined \
  --user "$(id -u):$(id -g)" \
  --env BASE_URL=https://wordpress.uds.dev \
  --env CI=true \
  --env HOME=/tmp \
  --env NPM_CONFIG_CACHE=/tmp/.npm \
  --volume "${repo_root}/tests:/app" \
  --workdir /app \
  "mcr.microsoft.com/playwright:v${playwright_version}" \
  bash -lc "npm ci && npx playwright test"
