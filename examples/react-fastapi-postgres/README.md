# React, FastAPI, and Postgres on UDS

This example is being built in passes. The current application contains a React
UI and a FastAPI service and follows the end-to-end vendor workflow:

```text
Compose source -> Compose Bridge workspace -> Zarf package -> UDS deployment
```

The first pass established plain tenant-gateway routing, and the second added
UDS Authservice in front of the UI. The current pass adds an internal FastAPI
service. NGINX forwards `/api/` requests and the Authservice-provided ID token to
FastAPI, which returns selected user claims for the React UI. A future pass will
add Postgres without changing the overall packaging workflow.

```text
Browser -> UDS Authservice -> UI NGINX -> FastAPI
```

## Prerequisites

- Docker with Docker Compose and the Compose Bridge plugin
- Docker Buildx
- UDS CLI
- A local UDS cluster, such as UDS Core Slim Dev on k3d
- `kubectl`

## Local Docker Compose development

Run the UI and API locally with a development-only identity injected by NGINX:

```sh
docker compose -f compose.yaml -f compose.dev.yaml up --build
```

Open <http://localhost:8080/>. React requests `/api/userinfo` through the same
NGINX-to-FastAPI path used in UDS and displays `Local Developer` as the signed-in
user.

The shared NGINX configuration loads a small authorization-header snippet inside
its `/api/` location. In production, `ui/nginx/api-auth.conf` forwards the token
supplied by UDS Authservice. `compose.dev.yaml` replaces only that snippet with
`ui/nginx/api-auth.dev.conf`, which supplies a fixed, unsigned JWT. The token is
not a secret and is intended only to simulate the deployed identity header. It
does not provide local login or access enforcement.

The development override is deliberately not named `compose.override.yaml`, so
normal Compose commands continue to use only the production configuration.
Always pass both files when starting the local identity flow, and stop it with
the corresponding command:

```sh
docker compose -f compose.yaml -f compose.dev.yaml down
```

## Build, deploy, and clean

Each script has one job and assumes its required tools and cluster context are
already configured.

Build the local development Compose Bridge image, convert the Compose project,
and create the Zarf package:

```sh
./scripts/build.sh
```

The build does not clean old output or touch the cluster. Run cleanup first
when rebuilding an existing workspace. The generated Helm chart, temporary
Buildx Bake definition, and OCI image archives are written under `out/`. The
Zarf archive is written to `packages/`.

The bridge phase uses `.tmp/` as its temporary directory. This keeps the
canonical Compose model on the project path, which is available to OrbStack's
Linux engine even when the host system temp directory is not. It is also
ignored by Git and is not part of the package.

The conversion also explicitly loads `compose.bridge.yaml`. Current Docker
Compose Bridge versions inspect or pull every service image before starting a
transformation, even when a service declares `build:`. The overlay temporarily
points API and UI at `compose-bridge-uds:dev` so conversion can reach the local
transformer without building either application image first. The transformer
replaces those placeholders with package-owned image references. Do not use
this overlay with `docker compose up` or `docker compose build`; it can be
removed once Docker Compose skips missing images for build services.

Deploy the one archive in `packages/`:

```sh
./scripts/deploy.sh
```

Deployment does not build or clean anything.

Delete the application namespace and all generated workspace artifacts:

```sh
kubectl config current-context
./scripts/clean.sh
```

Cleanup always targets the `react-fastapi-postgres` namespace in the active
Kubernetes context and removes `.tmp/`, `logs/`, `out/`, and `packages/`.

The Compose services declare only `build:`. The bridge assigns package-owned
references such as `zarf.internal/react-fastapi-postgres-api:0.1.0`, and the
generated Deployments use `imagePullPolicy: Always`. Zarf package creation
builds both `linux/amd64` and `linux/arm64` variants and packages their OCI
archives. Update `x-uds.package.version` when preparing a new application
release.

The build script always builds the repository's current source as
`compose-bridge-uds:dev` and uses that image for conversion. This keeps bridge
development in the same explicit build flow as package generation.

## Access the authenticated UI

After the build and deployment completes, open:

```text
https://react-fastapi-postgres.uds.dev/
```

Authservice redirects the browser to UDS SSO. Sign in with an existing UDS
account, then the browser returns to the React UI. The example does not restrict
access to a particular Keycloak group, so any authenticated UDS user is
accepted. React requests `/api/userinfo` and displays the first available value
from `name`, `preferred_username`, `email`, or `sub`.

Authentication is enforced by UDS Core at the deployed tenant gateway. Running
the containers directly with Docker does not put Authservice in front of them,
and the API intentionally has no fake local user fallback.

## User information API

The API is internal to the Compose and Kubernetes network. Only the UI service
is exposed through the tenant gateway; NGINX proxies same-origin `/api/`
requests to FastAPI.

`GET /api/userinfo` requires the bearer ID token inserted by Authservice and
returns only selected standard claims:

```json
{
  "sub": "user-id",
  "name": "Example User",
  "preferred_username": "example",
  "email": "example@uds.dev"
}
```

Optional claims are returned as `null`. Missing or malformed bearer tokens, and
tokens without a string `sub`, receive `401 Unauthorized`. Responses use
`Cache-Control: no-store`, and the raw token is never returned or logged.

This pass relies on Authservice to validate the token before it reaches NGINX.
FastAPI decodes the forwarded token without independently checking its signature
because the claims are used only to display identity. Add independent issuer,
audience, expiration, and signature validation before using the API for
authorization or protected data.

## Inspect the package

`scripts/inspect.sh` reads the one Zarf archive in `packages/` directly. It
does not read `out/` and can be run after the generated chart directory has
been discarded:

```sh
./scripts/inspect.sh
```

The report includes the package definition, bundled images, packaged chart
values files, and rendered Kubernetes/UDS manifests.

## Directory ownership

- `compose.yaml` and `compose.bridge.yaml` are the conversion source.
- `scripts/` contains the focused build, deploy, clean, and inspect commands.
- `api/` contains the FastAPI service, API tests, and its non-root image build.
- `ui/` contains the React and NGINX source used to build the image.
- `out/` is disposable Compose Bridge output.
- `packages/` contains the local distribution archive.
