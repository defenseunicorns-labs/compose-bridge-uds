#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

uds zarf package deploy packages/zarf-package-*.tar.zst --confirm
