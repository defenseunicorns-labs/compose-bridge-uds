# uds-compose-bridge

A [Docker Compose Bridge](https://docs.docker.com/compose/bridge/) transformation that converts a Docker Compose application into a deployable [UDS](https://uds.defenseunicorns.com/) package. It reads a `compose.yaml` and produces a Zarf package definition with Kubernetes manifests and a UDS Package CR, ready for `zarf package create` and deploy.

## Quickstart

The following example deploys UDS Core Slim Dev on k3d, and packages an example compose file:

```sh
# deploy UDS Core Slim Dev
uds deploy k3d-core-demo

# build the example
cd examples/full
docker compose build

# run the transformation
docker compose bridge convert --transformation ghcr.io/defenseunicorns-dashdays/compose-bridge-uds:latest

# build and deploy the package
zarf package create out/
zarf package deploy zarf-package-hello-world-*.tar.zst
```

## Compose structure

### Supported Compose Specs

- `image:` and `build:` services (with explicit `image:` after `docker compose build`)
- Named volumes (as PVCs)
- Compose `secrets` and `configs`
- `environment` and `env_file`
- `depends_on` (with init-container wait logic)
- `healthcheck` (as liveness probes)
- `deploy.resources`
- `profiles`

### Unsupported Compose Specs

- Bind mounts

### UDS Compose Extensions

Use `x-uds` at the top level of your `compose.yaml` to express UDS-specific configuration that cannot be inferred from Compose alone.
