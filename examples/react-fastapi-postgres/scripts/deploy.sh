#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

# Keep the interactive variable prompt; --confirm would deploy an empty password.
uds zarf package deploy packages/zarf-package-*.tar.zst
