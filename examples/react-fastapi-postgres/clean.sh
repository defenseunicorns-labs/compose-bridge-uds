#!/usr/bin/env bash

set -Eeuo pipefail

# Remove generated files and, when explicitly requested, the deployed Zarf
# package. This script can be run directly or by build.sh.

EXAMPLE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$EXAMPLE_DIR"

PACKAGE_NAME="react-fastapi-postgres"
PACKAGE_NAMESPACE="react-fastapi-postgres"
CLEAN_DEPLOY="${CLEAN_DEPLOY:-false}"
# build.sh keeps its completed preflight log while cleaning older artifacts.
CLEAN_LOGS="${CLEAN_LOGS:-true}"

OUT_DIR="$EXAMPLE_DIR/out"
PACKAGE_DIR="$EXAMPLE_DIR/packages"
LOG_DIR="$EXAMPLE_DIR/logs"
BRIDGE_TMP_DIR="$EXAMPLE_DIR/.tmp"

for boolean_name in CLEAN_DEPLOY CLEAN_LOGS; do
  if [[ "${!boolean_name}" != "true" && "${!boolean_name}" != "false" ]]; then
    printf 'ERROR: %s must be true or false\n' "$boolean_name" >&2
    exit 1
  fi
done

DEPLOYED_PACKAGE_EXISTS=false
if [[ "$CLEAN_DEPLOY" == "true" ]]; then
  if ! command -v kubectl >/dev/null 2>&1; then
    printf 'ERROR: kubectl is required to check for the deployed package\n' >&2
    exit 1
  fi

  CURRENT_CONTEXT="$(kubectl config current-context)"
  printf 'Using Kubernetes context: %s\n' "$CURRENT_CONTEXT"

  if ! kubectl cluster-info >/dev/null; then
    printf 'ERROR: Kubernetes context is not reachable: %s\n' "$CURRENT_CONTEXT" >&2
    exit 1
  fi

  if kubectl get package "$PACKAGE_NAME" --namespace "$PACKAGE_NAMESPACE" >/dev/null 2>&1; then
    if ! command -v uds >/dev/null 2>&1; then
      printf 'ERROR: uds is required to remove the deployed package\n' >&2
      exit 1
    fi
    DEPLOYED_PACKAGE_EXISTS=true
  fi
fi

printf 'Removing generated build artifacts:\n'
artifact_dirs=("$OUT_DIR" "$PACKAGE_DIR" "$BRIDGE_TMP_DIR")
if [[ "$CLEAN_LOGS" == "true" ]]; then
  artifact_dirs+=("$LOG_DIR")
fi
printf '  %s\n' "${artifact_dirs[@]}"
rm -rf "${artifact_dirs[@]}"

if [[ "$CLEAN_DEPLOY" == "false" ]]; then
  printf 'Preserving the deployed package because CLEAN_DEPLOY=false\n'
elif [[ "$DEPLOYED_PACKAGE_EXISTS" == "false" ]]; then
  printf 'No deployed package found: %s\n' "$PACKAGE_NAME"
else
  printf 'Removing deployed package: %s\n' "$PACKAGE_NAME"
  uds zarf package remove "$PACKAGE_NAME" --confirm
fi

printf 'Clean complete.\n'
