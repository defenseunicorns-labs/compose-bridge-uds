#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

uds zarf package inspect definition packages/zarf-package-*.tar.zst
uds zarf package inspect images packages/zarf-package-*.tar.zst
uds zarf package inspect values-files packages/zarf-package-*.tar.zst \
  --set-variables POSTGRES_USERNAME_SECRET_NAME=inspection-only \
  --set-variables POSTGRES_USERNAME_SECRET_KEY=username \
  --set-variables POSTGRES_PASSWORD_SECRET_NAME=inspection-only \
  --set-variables POSTGRES_PASSWORD_SECRET_KEY=password
uds zarf package inspect manifests packages/zarf-package-*.tar.zst \
  --set-variables POSTGRES_USERNAME_SECRET_NAME=inspection-only \
  --set-variables POSTGRES_USERNAME_SECRET_KEY=username \
  --set-variables POSTGRES_PASSWORD_SECRET_NAME=inspection-only \
  --set-variables POSTGRES_PASSWORD_SECRET_KEY=password
