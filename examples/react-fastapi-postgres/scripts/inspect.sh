#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

printf '\n== Package Definition ==\n\n'
uds zarf package inspect definition uds/packages/zarf-package-*.tar.zst

printf '\n== Packaged Images ==\n\n'
uds zarf package inspect images uds/packages/zarf-package-*.tar.zst

printf '\n== Packaged Values Files ==\n\n'
uds zarf package inspect values-files uds/packages/zarf-package-*.tar.zst \
  --set-variables POSTGRES_USERNAME_SECRET_NAME=inspection-only \
  --set-variables POSTGRES_USERNAME_SECRET_KEY=username \
  --set-variables POSTGRES_PASSWORD_SECRET_NAME=inspection-only \
  --set-variables POSTGRES_PASSWORD_SECRET_KEY=password

printf '\n== Rendered Kubernetes and UDS Manifests ==\n\n'
uds zarf package inspect manifests uds/packages/zarf-package-*.tar.zst \
  --set-variables POSTGRES_USERNAME_SECRET_NAME=inspection-only \
  --set-variables POSTGRES_USERNAME_SECRET_KEY=username \
  --set-variables POSTGRES_PASSWORD_SECRET_NAME=inspection-only \
  --set-variables POSTGRES_PASSWORD_SECRET_KEY=password
