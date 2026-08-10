# React, FastAPI, and Postgres on UDS

This example contains a React UI, a FastAPI service, and PostgreSQL and follows
the end-to-end vendor workflow:

```text
Compose source -> Compose Bridge workspace -> Zarf package -> UDS deployment
```

UDS Authservice protects the UI, NGINX forwards same-origin `/api/` requests and
the Authservice-provided ID token to FastAPI, and PostgreSQL persists messages
with the authenticated sender identity. PostgreSQL runs as part of local
Compose development but is deliberately excluded from the generated package.

```text
Browser -> UDS Authservice -> UI NGINX -> FastAPI -> PostgreSQL
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

Open <http://localhost:8080/>. React uses the same NGINX-to-FastAPI path used in
UDS, displays `Local Developer` as the signed-in user, and stores messages in
the local PostgreSQL volume.

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

That command preserves the database volume. Add `--volumes` when the local
messages should also be deleted. `postgres-username.dev.txt` and
`postgres-password.dev.txt` contain only fixed local development credentials;
they are not used as the deployed credentials.

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

Deploy the one archive in `packages/` after separately providing a compatible,
initialized PostgreSQL service reachable as `db` in the application namespace:

```sh
./scripts/deploy.sh
```

Deployment does not build or clean anything. Supply the name and key of the
existing Kubernetes Secret behind each shared Compose secret through Zarf
configuration or `--set-variables` arguments. These infrastructure references
do not prompt interactively. Both names can point to one Postgres-operator
credential Secret, with keys such as `username` and `password`. This phase does
not yet provide the development bundle or deploy-time database host
configuration; package creation and inspection are the supported end-to-end
workflow until those follow-on phases are complete.

Delete the application namespace and all generated workspace artifacts:

```sh
kubectl config current-context
./scripts/clean.sh
```

Cleanup stops the local Compose project, deletes its PostgreSQL volume, targets
the `react-fastapi-postgres` namespace in the active Kubernetes context, and
removes `.tmp/`, `logs/`, `out/`, and `packages/`.

The API and UI declare `build:` and receive package-owned references such as
`zarf.internal/react-fastapi-postgres-api:0.1.0`. PostgreSQL declares
`x-uds-exclude: true`, so its workload, image, volume, initialization config,
and dependency wait are omitted from the package. Zarf package creation builds
both application images for `linux/amd64` and `linux/arm64`. Update
`x-uds.package.version` when preparing a new application release.

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
from `name`, `preferred_username`, `email`, or `sub`. The same fallback order is
stored with each message alongside the stable token subject.

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
because this deployment trusts Authservice to validate every public request.
Add independent issuer, audience, expiration, and signature validation before
making the API reachable through any path that bypasses Authservice.

## Messages API

Both message endpoints require the bearer ID token inserted by Authservice.
The sender is always derived by FastAPI; clients cannot submit or override it.

Create a message:

```http
POST /api/messages
Content-Type: application/json

{"text":"Hello from UDS"}
```

The API trims the text, accepts between 1 and 500 characters, and responds with
`201 Created`:

```json
{
  "id": 1,
  "text": "Hello from UDS",
  "sender": {
    "sub": "user-id",
    "name": "Example User"
  },
  "created_at": "2026-08-05T16:00:00Z"
}
```

`GET /api/messages` returns all messages newest-first. Database connection
failures return `503 Database unavailable` without exposing credentials.

## Database lifecycle

In local Compose, PostgreSQL uses the `postgres-data` named volume. The initial
messages table is defined as an inline Compose config mounted into
`/docker-entrypoint-initdb.d/001-messages.sql`. PostgreSQL runs that file only
when initializing an empty volume. This keeps the first database pass small;
schema changes against existing data will require a migration workflow later.

PostgreSQL and FastAPI mount the same `postgres-username` and
`postgres-password` Compose secrets. Local Compose reads the development-only
files. Because each secret crosses from the included FastAPI service to the
excluded PostgreSQL service, the bridge treats both as package-external. The
package contains no credential values and creates no credential Secret.

At deployment, `POSTGRES_USERNAME_SECRET_NAME` and
`POSTGRES_PASSWORD_SECRET_NAME` identify existing Kubernetes Secrets in the
application namespace. Their corresponding `_SECRET_KEY` variables select the
entries to mount. Both names may identify one operator-created Secret while the
keys select `username` and `password`. Kubernetes projects those entries into
`/run/secrets/postgres-username` and `/run/secrets/postgres-password`; FastAPI
receives only the file paths and reads them with normal file I/O. The
database-only volume and initialization config are excluded from the package.

## Inspect the package

`scripts/inspect.sh` reads the one Zarf archive in `packages/` directly. It
does not read `out/` and can be run after the generated chart directory has
been discarded:

```sh
./scripts/inspect.sh
```

The report includes the package definition, bundled images, packaged chart
values files, and rendered Kubernetes/UDS manifests. Inspection substitutes an
obvious `inspection-only` Secret name plus `username` and `password` keys so it
remains non-interactive and never needs deployment credentials. The rendered
Deployment shows exactly how those external references become mounted files.

## Directory ownership

- `compose.yaml` and `compose.bridge.yaml` are the conversion source.
- `scripts/` contains the focused build, deploy, clean, and inspect commands.
- `postgres-username.dev.txt` and `postgres-password.dev.txt` are non-secret local Compose credentials.
- `api/` contains the FastAPI service, API tests, and its non-root image build.
- `ui/` contains the React and NGINX source used to build the image.
- `out/` is disposable Compose Bridge output.
- `packages/` contains the local distribution archive.
