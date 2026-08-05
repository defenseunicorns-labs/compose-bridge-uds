#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

kubectl delete namespace react-fastapi-postgres --ignore-not-found

rm -rf -- .tmp logs out packages
