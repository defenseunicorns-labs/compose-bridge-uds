# React, FastAPI, and Postgres on UDS

This example is being built in passes. Pass 1 contains only a React UI and
focuses on the end-to-end vendor workflow:

```text
Compose source -> Docker image -> Compose Bridge -> Zarf package -> UDS deployment
```

The future passes will add the FastAPI API and Postgres service without
changing the overall workflow.

Pass 1 intentionally disables inferred UDS SSO so the Hello World can verify
plain tenant-gateway routing first. Authentication will be enabled deliberately
in a later pass.

## Prerequisites

- Docker with Docker Compose and the Compose Bridge plugin
- Helm
- UDS CLI
- A local UDS cluster, such as UDS Core Slim Dev on k3d
- `kubectl` and `curl`

## Build and deploy

Run the complete, phased workflow from this directory:

```sh
./build.sh
```

Before the phases begin, `build.sh` invokes `clean.sh` to remove the previous
generated artifacts and deployment. The build phases are intentionally visible
and independently logged:

1. Preflight
2. Application Build
3. Compose Bridge Conversion
4. Generated Package Validation
5. Zarf Package Creation
6. UDS Deployment
7. Tenant Gateway Smoke Test

The generated Helm chart is written to `out/`. The Zarf archive is written to
`packages/`, and phase logs are written to `logs/`. These are generated
artifacts and are ignored by Git.

The bridge phase uses `.tmp/` as its temporary directory. This keeps the
canonical Compose model on the project path, which is available to OrbStack's
Linux engine even when the host system temp directory is not. It is also
ignored by Git and is not part of the package.

Run the cleanup independently when needed:

```sh
./clean.sh
```

Cleanup removes the `out/`, `packages/`, `logs/`, and `.tmp/` trees. It also
removes an existing `react-fastapi-postgres` Zarf package so generated resources
are tested from a clean state. Preserve the deployed package while still
removing local artifacts with:

```sh
CLEAN_DEPLOY=false ./clean.sh
```

The same setting can be passed to `build.sh`, which forwards it to `clean.sh`.

Each build uses a unique UI image tag by default. This avoids stale images
being selected by Kubernetes when the generated Deployment uses
`imagePullPolicy: IfNotPresent`. To provide a specific image reference:

```sh
UI_IMAGE=react-fastapi-postgres-ui:dev ./build.sh
```

The conversion uses the published Compose Bridge transformation image, pulling
it automatically when it is not already local. The repository's current image
workflow publishes `edge` for `main`, so that is the default moving tag:

```sh
TRANSFORM_IMAGE=ghcr.io/defenseunicorns-labs/compose-bridge-uds:edge ./build.sh
```

This example intentionally consumes the published bridge image. Changes to
the bridge source should be tested through the bridge repository's own image
build workflow or by overriding `TRANSFORM_IMAGE` with another published tag,
including `:latest` if that tag is available in the registry.

## Inspect the package

`inspect.sh` reads the Zarf archive directly. It does not read `out/` and can
be run after the generated chart directory has been discarded:

```sh
./inspect.sh
```

You can also inspect a specific package:

```sh
./inspect.sh packages/zarf-package-react-fastapi-postgres-0.1.0.tar.zst
```

The report includes the package definition, bundled images, packaged chart
values files, and rendered Kubernetes/UDS manifests.

## Directory ownership

- `compose.yaml`, `build.sh`, `clean.sh`, and `inspect.sh` are the workflow source.
- `ui/` contains the React and NGINX source used to build the image.
- `out/` is disposable Compose Bridge output.
- `packages/` contains the local distribution archive.
- `logs/` contains generated phase diagnostics.
