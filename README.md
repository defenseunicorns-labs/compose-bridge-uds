# uds-compose-bridge

`uds-compose-bridge` is a Docker Compose Bridge transformation that converts a Docker Compose application into a manifest-first UDS package layout.

The transformation takes the canonical Compose model from `/in/compose.yaml` and writes a Zarf package definition plus Kubernetes manifests to `/out`.

Current output shape:

```text
/out
  zarf.yaml
  manifests/
    namespace.yaml
    pvc-*.yaml
    configmap-*.yaml
    secret-*.yaml
    deployment-*.yaml
    service-*.yaml
    uds-package.yaml
```

The generated `zarf.yaml` uses per-service components.

## How It Works

Compose Bridge resolves the Compose project first, then invokes this transformation image.

At runtime the transformer:

1. reads the canonical Compose model from `/in/compose.yaml`
2. parses it into a normalized internal model
3. validates unsupported behavior early
4. generates plain Kubernetes manifests into `/out/manifests`
5. generates a `zarf.yaml` in `/out` that references those manifests

The generated package is manifest-first, not Helm-chart based.

## Output Model

The generated package follows these rules:

- one Zarf component per Compose service
- components are ordered by `depends_on`
- shared manifests like PVCs, ConfigMaps, and Secrets are deduplicated so only one component owns them
- a single `uds-package.yaml` is generated for the package and attached to the last ordered service component

## Exposure Rules

`uds-package.yaml` exposure is generated with this policy:

1. services with published Compose `ports:` are auto-exposed
2. services with only internal Compose `expose:` are not auto-exposed
3. `x-uds.expose` overrides/customizes the generated exposure for a service
4. `x-uds.expose: false` suppresses default exposure for that service

This keeps frontend-style services exposed by default while keeping internal services like databases private.

## Supported Input

Currently supported well:

- `image:` services
- `build:` services only after `docker compose build`, with an explicit pullable image reference
- named volumes -> PVCs
- Compose `secrets`
- Compose `configs`
- `environment`
- resolved `env_file`
- `depends_on`
- `healthcheck`
- `deploy.resources`
- `profiles`
- `x-uds.package.*`
- `x-uds.allow` and `x-uds.network.allow`
- `x-uds.expose`

Currently rejected:

- bind mounts
- unsupported volume types
- local-only build images such as `*.local/...`
- `build:` without an explicit image name

## Important Limitations

This project intentionally does not try to infer every UDS concept from plain Compose.

Today it does not yet implement:

- `x-uds.monitor`
- `x-uds.sso`
- `x-uds.caBundle`
- local image archive generation for non-pullable built images
- broader Compose coverage beyond the current support set

The current contract for `build:` is:

1. declare an explicit pullable `image:`
2. run `docker compose build`
3. run `docker compose bridge convert --transformations ...`

## Custom Compose Metadata

Use `x-uds` to express UDS-specific intent that cannot be safely inferred from Compose alone.

Example:

```yaml
name: wordpress

services:
  wordpress:
    image: wordpress:latest
    ports:
      - "8000:80"
    x-uds:
      expose:
        host: wordpress
        path: /
```

Currently recognized keys:

- `x-uds.package.name`
- `x-uds.package.namespace`
- `x-uds.package.version`
- `x-uds.expose`
- `x-uds.allow`
- `x-uds.network.allow`

## Build The Transformation Image

Build a local image:

```bash
docker build -t uds-compose-bridge:dev .
```

## Use With Compose Bridge

From a Compose project directory:

```bash
docker compose bridge convert --transformations uds-compose-bridge:dev
```

Or against a specific file:

```bash
docker compose -f path/to/compose.yaml bridge convert --transformations uds-compose-bridge:dev
```

Compose Bridge will invoke the transformation and write generated output into that project's `out/` directory.

## Build A Deployable Package

After conversion, create a Zarf package from the generated output:

```bash
zarf package create out
```

Then deploy it:

```bash
zarf package deploy out/zarf-package-*.tar.zst
```

## Build Services With `build:`

If your Compose file uses `build:`:

```bash
docker compose build
docker compose bridge convert --transformations uds-compose-bridge:dev
```

This works only when the service also has an explicit pullable `image:`. The transformer does not currently create `imageArchives` for local-only images.

## Local Development

Run the test suite:

```bash
go test ./...
```

Run the transformer directly against a canonical Compose model:

```bash
docker compose -f testdata/basic/compose.yaml config > /tmp/basic.compose.yaml
go run . -in /tmp/basic.compose.yaml -out /tmp/uds-out
```

## Example Fixtures

Useful fixtures in this repo:

- `testdata/basic/compose.yaml`
- `testdata/full/compose.yaml`

The `basic` example demonstrates:

- frontend + database service split
- Compose secret handling
- named volumes
- per-service Zarf components
- explicit `x-uds.expose` for the frontend

The `full` example is useful for validating current rejection behavior, especially bind mounts.

## What Is Left To Do

The project is usable, but it is still an MVP. The main remaining work is:

1. add support for local built images via `imageArchives` or a small outer wrapper
2. expand UDS support beyond `network.expose` and `allow`
3. add more Compose feature coverage and clearer validation for unsupported fields
4. improve end-to-end tests around real `docker compose bridge convert` runs
5. validate generated packages with `zarf package create` and `zarf dev lint` as part of normal development

