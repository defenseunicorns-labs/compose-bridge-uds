# React, FastAPI, and Postgres on UDS

This example is being built in passes. The current application contains a React
UI and a FastAPI service and follows the end-to-end vendor workflow:

```text
Compose source -> Docker images -> Compose Bridge -> Zarf package -> UDS deployment
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
- Helm
- UDS CLI
- A local UDS cluster, such as UDS Core Slim Dev on k3d
- `kubectl` and `curl`

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

The override is deliberately not named `compose.override.yaml`, so normal image
builds and Compose Bridge conversion continue to use only the production
configuration. Always pass both files when starting the local identity flow,
and stop it with the corresponding command:

```sh
docker compose -f compose.yaml -f compose.dev.yaml down
```

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
7. Authenticated Tenant Gateway Smoke Test

The generated Helm chart is written to `out/`. The Zarf archive is written to
`packages/`, and phase logs are written to `logs/`. These are generated
artifacts and are ignored by Git.

The final smoke test makes an anonymous request without following redirects and
verifies that Authservice sends it to the UDS SSO endpoint. It does not automate
a browser login or create a Keycloak user.

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

Each build uses unique UI and API image tags by default. This avoids stale
images being selected by Kubernetes when the generated Deployments use
`imagePullPolicy: IfNotPresent`. To provide specific image references:

```sh
UI_IMAGE=react-fastapi-postgres-ui:dev \
API_IMAGE=react-fastapi-postgres-api:dev \
  ./build.sh
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
- `api/` contains the FastAPI service, API tests, and its non-root image build.
- `ui/` contains the React and NGINX source used to build the image.
- `out/` is disposable Compose Bridge output.
- `packages/` contains the local distribution archive.
- `logs/` contains generated phase diagnostics.
