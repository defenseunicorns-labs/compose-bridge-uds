# uds-compose-bridge

A [Docker Compose Bridge](https://docs.docker.com/compose/bridge/) transformation that converts a Compose application into a deployable [UDS](https://uds.defenseunicorns.com/) package. It receives the fully-resolved Compose model as input and produces a Zarf package definition with Kubernetes manifests and a UDS Package CR, ready for `zarf package create` and deploy.

## Quickstart

The following example deploys UDS Core Slim Dev on k3d, and packages an example `compose.yaml`:

```sh
# (optionally) deploy UDS Core Slim Dev
uds deploy k3d-core-slim-dev

# build the example
cd examples/full
docker compose build

# run the transformation
docker compose bridge convert -t ghcr.io/defenseunicorns-dashdays/compose-bridge-uds:latest

# build and deploy the package
zarf package create out/
zarf package deploy zarf-package-hello-world-*.tar.zst
```

## Compose structure

The transformation maps the Compose model (as defined by the [Compose Specification](https://compose-spec.io/)) to Kubernetes resources (Deployments, Services, PVCs, ConfigMaps, Secrets) and uses `x-uds` [extension](https://docs.docker.com/reference/compose-file/extension/) keys to generate a UDS Package CR for network policy and SSO.

### Supported Compose configuration

- `image:` — container image reference
- `build:` — build from a Dockerfile; run `docker compose build` before conversion
- Named `volumes:` — converted to PersistentVolumeClaims (1Gi, ReadWriteOnce by default)
- `secrets:` and `configs:` — converted to Kubernetes Secrets and ConfigMaps; configs must use inline `content:` (not external references)
- `environment:` and `env_file:` — resolved by `docker compose config` and injected as container environment variables
- `depends_on:` — converted to init-container wait logic using `netcat` (busybox); requires the dependency to declare a port
- `healthcheck:` (`CMD` and `CMD-SHELL`) — converted to Kubernetes liveness probes
- `deploy.resources` (limits and reservations) — mapped to Pod resource requests/limits
- `ports:` — services with published ports are auto-exposed via the UDS tenant gateway; `expose:` (internal-only) ports are not

### Unsupported Compose configuration

- **Bind mounts** — use Compose `configs:`, `secrets:`, or named `volumes:` instead
- **External configs** — configs must define inline `content:`; referencing external files is not supported

### UDS Package CR inference and overrides

The bridge automatically generates a [UDS Package CR](https://docs.defenseunicorns.com/core/reference/operator--crds/packages-v1alpha1-cr/) from the Compose model. Most configuration is inferred — use [`x-uds`](https://docs.docker.com/reference/compose-file/extension/) at the top level of your `compose.yaml` only to override defaults.

#### What is auto-generated

- **Expose**: services with published `ports:` are exposed on the tenant gateway (`host` = service name, `gateway` = `tenant`, `selector`/`podLabels` = `app.kubernetes.io/name: <service>`)
- **Network allow**: intra-namespace ingress/egress rules are always included; `depends_on:` relationships generate egress rules to the dependency's primary port
- **SSO**: a Keycloak client is generated for the first exposed service (`clientId` = project name, `redirectUris` = `https://<host>.uds.dev/*`); omitted when no services are exposed

#### Overriding defaults with `x-uds`

| Key | Purpose |
|---|---|
| `x-uds.package.name` | Package name (default: Compose project name) |
| `x-uds.package.namespace` | Kubernetes namespace (default: Compose project name) |
| `x-uds.package.version` | Package version (default: `0.1.0`) |
| `x-uds.network.expose[]` | Override expose rules; replaces auto-generation when present. Missing fields (`gateway`, `port`, `selector`, `podLabels`) are inferred from the service. |
| `x-uds.network.allow[]` | Additional network allow rules; merged with (and deduplicated against) auto-generated rules |
| `x-uds.sso[]` | Override SSO clients; missing fields (`clientId`, `name`, `redirectUris`, `enableAuthserviceSelector`) are inferred |

Example — override only the host for an exposed service:

```yaml
x-uds:
  network:
    expose:
      - service: server
        host: hello-world    # override host (default would be "server")
```

See [`examples/full/compose.yaml`](examples/full/compose.yaml) for a complete working example.
