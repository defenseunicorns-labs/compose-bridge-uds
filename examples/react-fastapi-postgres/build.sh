#!/usr/bin/env bash

set -Eeuo pipefail

# This script is intentionally written as a linear, seven-phase walkthrough.
# It is both an executable build pipeline and a map of how Compose source
# becomes a running UDS package.

EXAMPLE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$EXAMPLE_DIR"

# Inputs that a vendor may want to override without editing this script.
TRANSFORM_IMAGE="${TRANSFORM_IMAGE:-ghcr.io/defenseunicorns-labs/compose-bridge-uds:edge}"
PUBLIC_HOST="${PUBLIC_HOST:-react-fastapi-postgres.uds.dev}"
CLEAN_DEPLOY="${CLEAN_DEPLOY:-true}"

PACKAGE_NAMESPACE="react-fastapi-postgres"

# New image tags on every build prevent Kubernetes from reusing older local
# images when the generated Deployments use imagePullPolicy: IfNotPresent.
BUILD_ID="${BUILD_ID:-$(date -u +%Y%m%d%H%M%S)-$$}"
UI_IMAGE="${UI_IMAGE:-react-fastapi-postgres-ui:$BUILD_ID}"
API_IMAGE="${API_IMAGE:-react-fastapi-postgres-api:$BUILD_ID}"
export API_IMAGE UI_IMAGE

# Generated files stay inside the example and are ignored by Git.
OUT_DIR="$EXAMPLE_DIR/out"
PACKAGE_DIR="$EXAMPLE_DIR/packages"
LOG_DIR="$EXAMPLE_DIR/logs"
BRIDGE_TMP_DIR="$EXAMPLE_DIR/.tmp"
SMOKE_HEADERS="$LOG_DIR/smoke-response-headers.txt"

CURRENT_PHASE="Setup"
CURRENT_LOG=""
PACKAGE_PATH=""

fail() {
  printf '\nERROR: %s\n' "$*" >&2
  printf 'Phase: %s\n' "$CURRENT_PHASE" >&2
  if [[ -n "$CURRENT_LOG" ]]; then
    printf 'Log: %s\n' "$CURRENT_LOG" >&2
  fi
  exit 1
}

# Run a command visibly, copy its output to the current phase log, and stop on
# failure. Keeping this small helper lets the phases below read like a recipe.
run() {
  {
    printf '$'
    printf ' %q' "$@"
    printf '\n'
  } | tee -a "$CURRENT_LOG"

  set +e
  "$@" 2>&1 | tee -a "$CURRENT_LOG"
  command_status=${PIPESTATUS[0]}
  set -e

  if [[ $command_status -ne 0 ]]; then
    fail "command failed with exit code $command_status"
  fi
}

# -----------------------------------------------------------------------------
# Clean the previous build
# clean.sh is also useful on its own and owns all destructive cleanup logic.
# -----------------------------------------------------------------------------

CLEAN_DEPLOY="$CLEAN_DEPLOY" "$EXAMPLE_DIR/clean.sh"
mkdir -p "$PACKAGE_DIR" "$LOG_DIR" "$BRIDGE_TMP_DIR"


# -----------------------------------------------------------------------------
# Phase 1: Preflight
# Prove that every required tool, Docker, and the target cluster are available.
# -----------------------------------------------------------------------------

CURRENT_PHASE="Preflight"
CURRENT_LOG="$LOG_DIR/01-preflight.log"
: > "$CURRENT_LOG"
printf '\n== [1/7] %s ==\n' "$CURRENT_PHASE" | tee -a "$CURRENT_LOG"
printf 'UI image: %s\n' "$UI_IMAGE" | tee -a "$CURRENT_LOG"
printf 'API image: %s\n' "$API_IMAGE" | tee -a "$CURRENT_LOG"

for required_command in docker helm uds kubectl curl tee; do
  if ! command -v "$required_command" >/dev/null 2>&1; then
    fail "required command not found: $required_command"
  fi
  printf 'found %s: %s\n' \
    "$required_command" \
    "$(command -v "$required_command")" | tee -a "$CURRENT_LOG"
done

run docker info
run docker compose version
run docker compose bridge --help
run helm version --short
run uds zarf --help
run kubectl cluster-info


# -----------------------------------------------------------------------------
# Phase 2: Application Build
# Build the React/NGINX and FastAPI images described by compose.yaml.
# -----------------------------------------------------------------------------

CURRENT_PHASE="Application Build"
CURRENT_LOG="$LOG_DIR/02-application-build.log"
: > "$CURRENT_LOG"
printf '\n== [2/7] %s ==\n' "$CURRENT_PHASE" | tee -a "$CURRENT_LOG"

run docker compose build


# -----------------------------------------------------------------------------
# Phase 3: Compose Bridge Conversion
# Treat compose.yaml as source and generate out/ from scratch.
# Compose Bridge pulls the transformation image if it is not already local.
# -----------------------------------------------------------------------------

CURRENT_PHASE="Compose Bridge Conversion"
CURRENT_LOG="$LOG_DIR/03-bridge-conversion.log"
: > "$CURRENT_LOG"
printf '\n== [3/7] %s ==\n' "$CURRENT_PHASE" | tee -a "$CURRENT_LOG"

# A project-local temp directory is visible to host-managed Docker engines such
# as OrbStack. Some host operating-system temp directories are not.
printf 'using repo-local Compose Bridge temp directory: %s\n' \
  "$BRIDGE_TMP_DIR" | tee -a "$CURRENT_LOG"
run env TMPDIR="$BRIDGE_TMP_DIR" \
  docker compose bridge convert \
  --transformation "$TRANSFORM_IMAGE" \
  --output "$OUT_DIR"


# -----------------------------------------------------------------------------
# Phase 4: Generated Package Validation
# Check the generated Helm chart before spending time assembling a Zarf package.
# -----------------------------------------------------------------------------

CURRENT_PHASE="Generated Package Validation"
CURRENT_LOG="$LOG_DIR/04-chart-validation.log"
: > "$CURRENT_LOG"
printf '\n== [4/7] %s ==\n' "$CURRENT_PHASE" | tee -a "$CURRENT_LOG"

run helm lint "$OUT_DIR/chart"
run helm template "$OUT_DIR/chart"


# -----------------------------------------------------------------------------
# Phase 5: Zarf Package Creation
# Turn the disposable generated output into the distributable package archive.
# -----------------------------------------------------------------------------

CURRENT_PHASE="Zarf Package Creation"
CURRENT_LOG="$LOG_DIR/05-package-creation.log"
: > "$CURRENT_LOG"
printf '\n== [5/7] %s ==\n' "$CURRENT_PHASE" | tee -a "$CURRENT_LOG"

run uds zarf package create "$OUT_DIR" --output "$PACKAGE_DIR" --confirm

package_matches=()
shopt -s nullglob
package_matches=("$PACKAGE_DIR"/zarf-package-*.tar.zst)
shopt -u nullglob

if [[ ${#package_matches[@]} -ne 1 ]]; then
  fail "expected exactly one Zarf archive in $PACKAGE_DIR; found ${#package_matches[@]}"
fi

PACKAGE_PATH="${package_matches[0]}"
printf 'created package: %s\n' "$PACKAGE_PATH" | tee -a "$CURRENT_LOG"


# -----------------------------------------------------------------------------
# Phase 6: UDS Deployment
# Deploy the package archive and wait for both generated Deployments to roll out.
# -----------------------------------------------------------------------------

CURRENT_PHASE="UDS Deployment"
CURRENT_LOG="$LOG_DIR/06-uds-deployment.log"
: > "$CURRENT_LOG"
printf '\n== [6/7] %s ==\n' "$CURRENT_PHASE" | tee -a "$CURRENT_LOG"

run uds zarf package deploy "$PACKAGE_PATH" --confirm
run kubectl rollout status \
  deployment/api \
  --namespace "$PACKAGE_NAMESPACE" \
  --timeout 5m
run kubectl rollout status \
  deployment/ui \
  --namespace "$PACKAGE_NAMESPACE" \
  --timeout 5m


# -----------------------------------------------------------------------------
# Phase 7: Tenant Gateway Smoke Test
# Verify that Authservice intercepts an anonymous request and redirects it to
# the UDS SSO endpoint instead of allowing it to reach the UI.
# -----------------------------------------------------------------------------

CURRENT_PHASE="Tenant Gateway Smoke Test"
CURRENT_LOG="$LOG_DIR/07-smoke-test.log"
: > "$CURRENT_LOG"
printf '\n== [7/7] %s ==\n' "$CURRENT_PHASE" | tee -a "$CURRENT_LOG"
printf 'checking https://%s/\n' "$PUBLIC_HOST" | tee -a "$CURRENT_LOG"

run curl \
  --fail \
  --silent \
  --show-error \
  --insecure \
  --retry 12 \
  --retry-all-errors \
  --retry-delay 5 \
  --max-time 20 \
  --dump-header "$SMOKE_HEADERS" \
  --output /dev/null \
  "https://$PUBLIC_HOST/"

run grep \
  --quiet \
  --ignore-case \
  --extended-regexp \
  '^location: https://sso\.uds\.dev/' \
  "$SMOKE_HEADERS"

printf '\nBuild complete.\nPackage: %s\nLogs: %s\n\n' \
  "$PACKAGE_PATH" \
  "$LOG_DIR"
