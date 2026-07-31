#!/usr/bin/env bash

set -Eeuo pipefail

# This script demonstrates that the Zarf archive is the distribution artifact.
# Everything below is read directly from that archive; out/ is not required.

EXAMPLE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_DIR="$EXAMPLE_DIR/packages"


# -----------------------------------------------------------------------------
# Choose the package archive
# Use the path supplied by the caller, or discover the one archive in packages/.
# -----------------------------------------------------------------------------

if [[ $# -gt 1 ]]; then
  printf 'ERROR: usage: ./inspect.sh [path/to/zarf-package.tar.zst]\n' >&2
  exit 1
fi

if [[ $# -eq 1 ]]; then
  PACKAGE_PATH="$1"

  # A relative argument is relative to the directory where the user invoked
  # the script, which matches normal command-line behavior.
  if [[ "$PACKAGE_PATH" != /* ]]; then
    PACKAGE_PATH="$PWD/$PACKAGE_PATH"
  fi
else
  package_matches=()
  shopt -s nullglob
  package_matches=("$PACKAGE_DIR"/zarf-package-*.tar.zst)
  shopt -u nullglob

  if [[ ${#package_matches[@]} -ne 1 ]]; then
    printf 'ERROR: expected exactly one Zarf archive in %s\n' "$PACKAGE_DIR" >&2
    printf 'Pass the archive path explicitly: ./inspect.sh path/to/package.tar.zst\n' >&2
    exit 1
  fi

  PACKAGE_PATH="${package_matches[0]}"
fi

if [[ ! -f "$PACKAGE_PATH" ]]; then
  printf 'ERROR: Zarf archive not found: %s\n' "$PACKAGE_PATH" >&2
  exit 1
fi

if ! command -v uds >/dev/null 2>&1; then
  printf 'ERROR: required command not found: uds\n' >&2
  exit 1
fi

printf 'Inspecting package: %s\n' "$PACKAGE_PATH"


# -----------------------------------------------------------------------------
# Package definition
# Show the package metadata and the components Zarf will deploy.
# -----------------------------------------------------------------------------

printf '\n== Package Definition ==\n'
uds zarf package inspect definition "$PACKAGE_PATH"


# -----------------------------------------------------------------------------
# Bundled images
# Show the container images carried inside the air-gap package.
# -----------------------------------------------------------------------------

printf '\n== Bundled Images ==\n'
uds zarf package inspect images "$PACKAGE_PATH"


# -----------------------------------------------------------------------------
# Packaged chart values
# Show the values files bundled with generated Helm charts.
# -----------------------------------------------------------------------------

printf '\n== Packaged Chart Values Files ==\n'
uds zarf package inspect values-files "$PACKAGE_PATH"


# -----------------------------------------------------------------------------
# Rendered manifests
# Show the Kubernetes and UDS resources that the package will apply.
# -----------------------------------------------------------------------------

printf '\n== Rendered Kubernetes and UDS Manifests ==\n'
uds zarf package inspect manifests "$PACKAGE_PATH"
