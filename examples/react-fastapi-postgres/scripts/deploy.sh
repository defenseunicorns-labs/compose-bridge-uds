#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

# External Secret references must come from deployment configuration or
# --set-variables arguments. Missing Secret names fail during Helm rendering.
uds zarf package deploy packages/zarf-package-*.tar.zst "$@"
