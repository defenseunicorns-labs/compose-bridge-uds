#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

uds zarf package inspect definition packages/zarf-package-*.tar.zst
uds zarf package inspect images packages/zarf-package-*.tar.zst
uds zarf package inspect values-files packages/zarf-package-*.tar.zst \
  --set-variables POSTGRES_PASSWORD=INSPECTION_ONLY
uds zarf package inspect manifests packages/zarf-package-*.tar.zst \
  --set-variables POSTGRES_PASSWORD=INSPECTION_ONLY
