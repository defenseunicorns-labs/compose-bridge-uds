# compose-bridge-uds

Convert a Docker Compose application into a deployable [UDS](https://uds.defenseunicorns.com/) package using [Docker Compose Bridge](https://docs.docker.com/compose/bridge/). The transformation consumes a fully-resolved Compose model and emits a Zarf package definition — Kubernetes manifests plus a UDS Package CR — ready for `zarf package create` and `zarf package deploy`.

> [!IMPORTANT]
> **This is not a supported product pathway.** It's an experimental transformation we're sharing to gather signal. If you find it useful — or hit rough edges — please open an issue. Your feedback is what tells us whether to invest further.

## Contents

- [Prerequisites](#prerequisites)
- [Quickstart](#quickstart)
- [Local Dockerfile builds](#local-dockerfile-builds)
- [How it works](#how-it-works)
- [Supported Compose configuration](#supported-compose-configuration)
- [Unsupported Compose configuration](#unsupported-compose-configuration)
- [UDS Package CR generation](#uds-package-cr-generation)
- [UDS Exemption generation](#uds-exemption-generation)
- [Feedback](#feedback)

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [Zarf](https://docs.zarf.dev/getting-started/install/)
- [UDS CLI](https://uds.defenseunicorns.com/reference/cli/quickstart-and-usage/)
- A Kubernetes cluster — the quickstart uses [k3d](https://k3d.io/)

## Quickstart

This walkthrough deploys UDS Core Slim Dev on k3d, then packages and deploys WordPress + MySQL from a Compose file.

**1. Stand up a local cluster with UDS Core Slim Dev** (skip if you already have one):

```sh
uds deploy k3d-core-slim-dev:latest
```

**2. Run the bridge transformation.** This writes Kubernetes manifests and a Zarf package definition to `out/`:

```sh
cd examples/simple
docker compose bridge convert -t ghcr.io/defenseunicorns-labs/compose-bridge-uds
```

**3. Build and deploy the Zarf package:**

```sh
zarf package create out/
zarf package deploy zarf-package-wordpress-*.tar.zst
```

## Local Dockerfile builds

Docker Compose Bridge does not run the Compose build step. If a service uses `build:` with a local Dockerfile, build the Compose project before running the bridge conversion so the image tag exists locally:

```sh
cd examples/full
docker compose build
docker compose bridge convert -t ghcr.io/defenseunicorns-labs/compose-bridge-uds
```

When `image:` is omitted, Compose assigns a default image name based on the project and service, such as `hello-world-server` for `examples/full`. To make the generated Kubernetes manifests and `zarf.yaml` use a stable or registry-backed reference, declare `image:` on the service before running `docker compose build`.

## How it works

The bridge maps the [Compose Specification](https://compose-spec.io/) to Kubernetes resources (Deployments, Services, PVCs, ConfigMaps, Secrets) and synthesizes a [UDS Package CR](https://docs.defenseunicorns.com/core/reference/operator--crds/packages-v1alpha1-cr/) for network policy, monitoring, SSO, and trust-bundle distribution. You can guide that synthesis with `x-uds` [extension keys](https://docs.docker.com/reference/compose-file/extension/) at the top level of your `compose.yaml`.

## Supported Compose configuration

| Compose key | Behavior |
|---|---|
| `image:` | Container image reference. |
| `build:` | Build from a Dockerfile. Run `docker compose build` before `docker compose bridge convert`; the bridge conversion does not build images. |
| Named `volumes:` | Converted to PersistentVolumeClaims (`1Gi`, `ReadWriteOnce` by default). |
| `secrets:` | Converted to Kubernetes Secrets. |
| `configs:` | Converted to ConfigMaps. Must use inline `content:` (no external file references). |
| `environment:`, `env_file:` | Resolved by `docker compose config` and injected as container env vars. |
| `depends_on:` | Converted to init-container wait logic using `netcat` (busybox). The dependency must declare a port. |
| `healthcheck:` | `CMD` and `CMD-SHELL` forms convert to Kubernetes liveness probes. |
| `deploy.resources` | `limits` and `reservations` map to Pod resource requests/limits. |
| `ports:` | Published ports are auto-exposed via the UDS tenant gateway. `expose:` (internal-only) ports are not exposed externally. Compose port declaration order is preserved, and long-syntax `name` / `app_protocol` hints are used to prefer web ports for multi-port services. |
| Bind mounts | Skipped during conversion with a warning because host paths do not have a portable Kubernetes equivalent. Use named `volumes:`, `configs:`, or `secrets:` for data that should be rendered into manifests. |
| `user:`, `privileged:`, `cap_add:`, `cap_drop:`, `security_opt:` | Reflected in the container security context where applicable. Settings that require UDS policy exceptions also generate a `manifests/uds-exemption.yaml`. |

## Unsupported Compose configuration

- **External configs** — `configs:` must define inline `content:`; external file references are not supported.

## UDS Package CR generation

Most of the UDS Package CR is inferred from your Compose model. Reach for `x-uds` only when you want to override defaults.

### Auto-generated

- **Expose** — services with published `ports:` are exposed on the tenant gateway (`host` = service name, `gateway` = `tenant`, `selector`/`podLabels` = `app.kubernetes.io/name: <service>`). For multi-port services, the bridge prefers ports whose Compose `app_protocol` or `name` indicates web traffic (`http`, `https`, `http2`, `h2c`, `grpc`, `grpcs`, `web`, `www`), then falls back to the first declared published port. Ports are not filtered by port number or transport protocol.
- **Network allow** — intra-namespace ingress/egress rules are always included so services in the namespace can communicate.
- **SSO** — a Keycloak client is generated for the first exposed service (`clientId` = project name, `redirectUris` = `https://<host>.uds.dev/*`). Omitted when no services are exposed.
- **Policy exemptions** — services that require UDS policy exceptions generate a `manifests/uds-exemption.yaml`, which is included in `zarf.yaml` with the rest of the rendered manifests.

### Opt-in

- **Monitoring** — declare `x-uds.monitor[]` for any metrics endpoints you want Prometheus to scrape.

### Overriding defaults with `x-uds`

| Key | Purpose |
|---|---|
| `x-uds.package.name` | Package name (default: Compose project name). |
| `x-uds.package.namespace` | Kubernetes namespace (default: Compose project name). |
| `x-uds.package.version` | Package version (default: `0.1.0`). |
| `x-uds.network.expose[]` | Override expose rules — replaces auto-generation when present. Missing fields (`gateway`, `port`, `selector`, `podLabels`) are inferred from the service. |
| `x-uds.network.allow[]` | Additional network allow rules — merged with (and deduplicated against) auto-generated rules. |
| `x-uds.monitor[]` | Monitor rules for Prometheus scraping. When `service` is set, missing `selector`, `podSelector`, `portName`, `targetPort`, `path`, and `kind` are inferred from the Compose service. |
| `x-uds.sso[]` | Override SSO clients — missing fields (`clientId`, `name`, `redirectUris`, `enableAuthserviceSelector`) are inferred. Set `x-uds.sso: []` to disable inferred SSO. |
| `x-uds.caBundle.configMap` | Customize the operator-managed trust bundle ConfigMap metadata for this package namespace. Renders to `spec.caBundle` in `manifests/uds-package.yaml`. |

Example — override only the host for an exposed service:

```yaml
x-uds:
  network:
    expose:
      - service: server
        host: hello-world    # override host (default would be "server")
```

See [`examples/full/compose.yaml`](examples/full/compose.yaml) for a complete working example.

### Notes on specific keys

- **Auto-expose port selection** — For services with multiple published ports, prefer Compose long syntax with `name` and/or `app_protocol` to identify the web-facing port, or use `x-uds.network.expose[].port` to pin the intended port if inference is ambiguous. The bridge does not automatically skip SSH, DNS, UDP, or other non-HTTP-looking ports.
- **Bind mounts** — Bind mounts are treated as local-development-only inputs and are omitted from the rendered manifests with a warning. Conversion continues, which lets Compose files with dev container mounts still produce a deployable package. Use a named volume when the data should become a PVC, or a config/secret when the mounted content should be materialized in Kubernetes.
- **`x-uds.sso`** — If omitted, the bridge infers an SSO client for the first exposed service. If explicitly set to an empty list (`x-uds.sso: []`), inferred SSO is disabled.
- **`x-uds.monitor[]`** — Each entry may be a raw UDS `spec.monitor[]` item, or may use the bridge-only `service` key to infer labels and port metadata. For multi-port services, set either `portName` or `targetPort` so the bridge picks the intended metrics port.
- **`x-uds.caBundle.configMap`** — Customizes the namespace trust-bundle ConfigMap created by UDS Core. The actual trust bundle contents are configured separately in UDS Core and are not part of the Package CR.

## UDS Exemption generation

When Compose security settings conflict with UDS secure-by-default policies, the bridge writes `manifests/uds-exemption.yaml` with an `Exemption` scoped to each matching service's namespace and pod name pattern. No exemption file is emitted when all services are compliant.

UDS Core enforces a restrictive baseline for workloads by default: containers are expected to run as non-root, avoid privileged mode, drop Linux capabilities, and use an approved seccomp profile. Those defaults are intentional defense-in-depth controls, but many Compose applications were written for a less constrained runtime. A container image that starts normally under Docker Compose may fail after package deploy if it expects root filesystem access, privileged host access, added capabilities, or an unconfined seccomp profile. In Kubernetes this often shows up as a pod stuck in `CrashLoopBackOff`, `CreateContainerConfigError`, or another startup failure caused by admission policy or missing permissions.

The generated exemption is the bridge's way of preserving that Compose intent while still making the security tradeoff visible in the rendered package. You need an exemption only when the application truly depends on one of these less-restricted settings. Prefer changing the image or Compose configuration to satisfy the UDS baseline when you can; keep the exemption when the workload cannot run correctly without it.

| Compose input | Generated UDS policy exemption |
|---|---|
| `user: root`, `user: 0`, or equivalent UID/GID forms | `RequireNonRootUser` |
| `privileged: true` | `DisallowPrivileged` |
| `cap_add:` | `DropAllCapabilities`, `RestrictCapabilities` |
| `security_opt: [seccomp:unconfined]` | `RestrictSeccomp` |
| `cap_drop: [ALL]` | No exemption; rendered into the container `securityContext.capabilities.drop` instead. |

Example:

```yaml
name: homelab
services:
  gitea:
    image: docker.gitea.com/gitea:1.25.5
    user: root
  runner:
    image: gitea/act_runner:latest
    privileged: true
```

Produces exemption entries for the `gitea` and `runner` pods:

```yaml
apiVersion: uds.dev/v1alpha1
kind: Exemption
metadata:
  name: homelab
  namespace: uds-policy-exemptions
spec:
  exemptions:
    - title: root user policy exemption for homelab gitea
      matcher:
        kind: pod
        namespace: homelab
        name: ^gitea-.*
      policies:
        - RequireNonRootUser
    - title: privileged policy exemption for homelab runner
      matcher:
        kind: pod
        namespace: homelab
        name: ^runner-.*
      policies:
        - DisallowPrivileged
```

## Feedback

This bridge isn't an officially supported pathway, but we genuinely want to know whether it's useful. Open an issue on this repo with your use case, your friction points, or anything you'd like to see work — that's the signal we'll use to decide where this goes next.
