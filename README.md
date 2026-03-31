# uds-compose-bridge

A [Docker Compose Bridge](https://docs.docker.com/compose/bridge/) transformation that converts a Docker Compose application into a deployable [UDS](https://uds.defenseunicorns.com/) package. It reads a `compose.yaml` and produces a Zarf package definition with Kubernetes manifests and a UDS Package CR, ready for `zarf package create` and deploy.

## Quickstart

### 1. Build the transformation image

```bash
docker build -t uds-compose-bridge:dev .
```

### 2. Prepare the example app

From the `examples/full` directory, build the app image and create the required `.env` file:

```bash
cd examples/full
docker compose build
```

### 3. Run the transformation

```bash
docker compose bridge convert --transformation uds-compose-bridge:dev
```

This writes the generated UDS package layout to `examples/full/out/`.

### 4. Create and deploy the Zarf package

```bash
zarf package create out/
zarf package deploy zarf-package-hello-world-*.tar.zst
```

## `x-uds` Extension

Use `x-uds` at the top level of your `compose.yaml` to express UDS-specific configuration that cannot be inferred from Compose alone. Two keys are supported under `x-uds`:

### `network`

Controls how services are exposed on Istio gateways and what network traffic is allowed.

```yaml
x-uds:
  network:
    expose:
      - service: server
        host: hello-world
        gateway: tenant
        port: 8080
        selector:
          app.kubernetes.io/name: server
        podLabels:
          app.kubernetes.io/name: server
    allow:
      - description: egress to redis
        direction: Egress
        selector:
          app.kubernetes.io/name: server
        remoteNamespace: hello-world
        remoteSelector:
          app.kubernetes.io/name: redis
        port: 6379
```

`expose` entries map directly to the [UDS Package `spec.network.expose`](https://uds.defenseunicorns.com/reference/configuration/custom-resources/packages-v1alpha1-cr/) fields. `allow` entries map to `spec.network.allow`.

When no `x-uds.network.expose` is provided, services with published Compose `ports:` are auto-exposed. Services with only internal `expose:` are never auto-exposed.

### `sso`

Configures Keycloak SSO clients. Entries map directly to [UDS Package `spec.sso`](https://uds.defenseunicorns.com/reference/configuration/custom-resources/packages-v1alpha1-cr/) fields. Use `enableAuthserviceSelector` to protect pods with AuthService.

```yaml
x-uds:
  sso:
    - clientId: hello-world
      name: Hello World
      redirectUris:
        - https://hello-world.uds.dev/*
      enableAuthserviceSelector:
        app.kubernetes.io/name: server
```

## Output Structure

```text
out/
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

- One Zarf component per Compose service, ordered by `depends_on`
- Shared manifests (PVCs, ConfigMaps, Secrets) are deduplicated across components
- A single `uds-package.yaml` is attached to the last component

## Supported Compose Features

- `image:` and `build:` services (with explicit `image:` after `docker compose build`)
- Named volumes (as PVCs)
- Compose `secrets` and `configs`
- `environment` and `env_file`
- `depends_on` (with init-container wait logic)
- `healthcheck` (as liveness probes)
- `deploy.resources`
- `profiles`

Bind mounts and `build:` without an explicit `image:` are rejected.

## Development

```bash
go test ./...
```

To run the transformer directly against a resolved Compose model:

```bash
docker compose -f examples/full/compose.yaml config > /tmp/compose.yaml
go run . -in /tmp/compose.yaml -out /tmp/uds-out
```
