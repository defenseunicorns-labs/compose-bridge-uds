# Full example

A complete walkthrough of `compose-bridge-uds` covering more of the Compose surface area than [`examples/simple`](../simple). This compose file exercises `build:`, `configs`, `secrets`, named `volumes`, `depends_on`, and `healthcheck`, alongside `x-uds` overrides for tenant-gateway exposure and Prometheus monitoring.

> [!IMPORTANT]
> This example uses `build:` for the `server` service. **Run `docker compose build` before `docker compose bridge convert`** — otherwise conversion fails with `pull access denied for hello-world-server`.

## Why the build step is required

`docker compose bridge convert` inspects every service's image to merge `EXPOSE` directives into the rendered Kubernetes service. When a service uses `build:` with no `image:` key, Compose synthesizes a default name (`<project>-<service>`, here `hello-world-server`) and tries to inspect it. On a fresh clone that image only exists after `docker compose build` tags it locally; without a local copy, the bridge falls through to a registry pull and Docker Hub returns `pull access denied`. Building first puts the image where `docker image inspect` can find it, and the pull branch is skipped. See [#20](https://github.com/defenseunicorns-labs/compose-bridge-uds/issues/20) for context.

## Quickstart

From this directory:

    docker compose build
    docker compose bridge convert -t ghcr.io/defenseunicorns-labs/compose-bridge-uds
    zarf package create out/
    zarf package deploy zarf-package-hello-world-*.tar.zst

The deploy step assumes a cluster with UDS Core Slim Dev (or equivalent) is already running. If not, see the [top-level Quickstart](../../README.md#quickstart).

## What this example demonstrates

| Compose feature | Bridge output |
| --- | --- |
| `server.build` | Locally-built image, rendered as a Deployment. |
| `db.image: redis:7-alpine` | Pulled from Docker Hub, rendered as a Deployment. |
| `configs:` (inline `content:`) | ConfigMap mounted into `server`. |
| `secrets:` (file source) | Secret mounted into `server`. |
| Named `volumes:` | PersistentVolumeClaims (`1Gi`, `ReadWriteOnce`). |
| `depends_on: db` | Init-container `netcat` wait on `db:6379`. |
| `healthcheck:` | Liveness probe on `server`. |
| `ports.target: 8080` | Auto-exposed via the UDS tenant gateway. Host overridden to `hello-world` via `x-uds.network.expose[].host`. |
| `x-uds.monitor[]` | Prometheus scrape configuration for `server`. |

For the full list of supported keys and `x-uds` extensions, see the [top-level README](../../README.md).
