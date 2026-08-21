# Compose support

## Supported configuration

| Compose key                                                      | Behavior                                                                                                                                                                                                                                                                        |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `image:`                                                         | Container image reference.                                                                                                                                                                                                                                                      |
| `build:`                                                         | Preserved as a generated Buildx Bake definition. `zarf package create` builds the image and adds its OCI archive to the package.                                                                                                                                                |
| Named `volumes:`                                                 | Converted to PersistentVolumeClaims (`1Gi`, `ReadWriteOnce` by default).                                                                                                                                                                                                        |
| `secrets:`                                                       | Delivered as read-only files. Secrets used only by packaged services become package-owned Kubernetes Secrets. Native external secrets and secrets shared with excluded services reference deployment-provided Kubernetes Secret names and keys.                                                                                               |
| `configs:`                                                       | Converted to reloadable ConfigMaps. Must use inline `content:` (no external file references).                                                                                                                                                                                   |
| `environment:`, `env_file:`                                      | Resolved by `docker compose config`, exposed as non-sensitive Zarf variables, and rendered into a reloadable ConfigMap consumed by the service through `envFrom`.                                                                                                               |
| `depends_on:`                                                    | Required dependencies become init-container wait logic using `netcat` (busybox). A service referenced only through long-syntax dependencies with `required: false` is excluded as a development-only service. Included dependencies must declare a port.                                                                                       |
| `healthcheck:`                                                   | `CMD` and `CMD-SHELL` forms convert to Kubernetes liveness probes.                                                                                                                                                                                                              |
| `container_name:`                                                | Ignored with a warning. Kubernetes Service and Deployment names come from the Compose service name.                                                                                                                                                                             |
| `stdin_open:`                                                    | Maps to the Kubernetes container `stdin` field.                                                                                                                                                                                                                                 |
| `deploy.resources`                                               | `limits` and `reservations` map to Pod resource requests and limits.                                                                                                                                                                                                            |
| `ports:`                                                         | Published ports are auto-exposed through the UDS tenant gateway. `expose:` (internal-only) ports are not exposed externally. Compose port declaration order is preserved, and long-syntax `name` and `app_protocol` hints are used to prefer web ports for multi-port services. |
| `hostname:`                                                      | Preserved as the Kubernetes Pod hostname.                                                                                                                                                                                                                                       |
| `networks:`                                                      | Service membership is preserved with Pod labels and selector-scoped UDS ingress and egress rules when services use different network sets. The ordinary `bridge` driver is accepted. External networks warn because external peers cannot be inferred; aliases, addresses, and driver options remain unsupported. |
| Bind mounts                                                      | Skipped during conversion with a warning because host paths do not have a portable Kubernetes equivalent. Use named `volumes:`, `configs:`, or `secrets:` for data that should be rendered into the chart.                                                                       |
| `user:`, `privileged:`, `cap_add:`, `cap_drop:`, `security_opt:` | Reflected in the container security context where applicable. Settings that require UDS policy exceptions also generate a `chart/templates/uds-exemption.yaml`.                                                                                                                 |

External configs referenced by an included service are not supported. A
packaged `configs:` entry must define inline `content:`.

## Excluding development services

Use long-syntax `depends_on` with `required: false` for a dependency that is
useful during local Compose development but should not be included in the
generated package:

```yaml
services:
  api:
    image: example/api:1.0.0
    depends_on:
      db:
        condition: service_healthy
        required: false

  db:
    image: postgres:18
```

Compose still starts `db` during `docker compose up`. The bridge excludes it
when every `depends_on` reference to `db` has `required: false`. Any required
reference keeps the service in the package, and services that are never
referenced remain included.

Excluded services do not generate workloads, Services, images, dependency
waits, policy exemptions, or other package content. Volumes, configs, and
secrets used only by excluded services are pruned; resources shared with an
included service remain. Explicit `x-uds.network.expose`, `x-uds.monitor`, and
build `additional_contexts` entries must not reference an excluded service.

## Runtime secrets

Compose grants services access to secrets as read-only files, normally under
`/run/secrets`. The generated chart preserves each secret's target file path.

- A secret consumed only by included services is package-owned. Zarf prompts
  for its sensitive value, and the chart creates the Kubernetes Secret.
- A secret consumed only by excluded services is omitted.
- A secret consumed by included and excluded services becomes package-external.
- A native Compose `external: true` secret consumed by an included service is
  also package-external.

For each package-external Compose secret, the generated Zarf package declares
`<SECRET>_SECRET_NAME` and `<SECRET>_SECRET_KEY` variables. The name has no
default and must identify an existing Kubernetes Secret in the package
namespace. The key defaults to the normalized Compose secret name. These
variables are non-sensitive references and do not expose the secret value.

Different Compose secrets can select different keys from the same Kubernetes
Secret. Build secrets are unaffected by this runtime-secret behavior.

## Runtime configuration

Every resolved service environment value becomes a non-sensitive Zarf variable named `<SERVICE>_<ENVIRONMENT_VARIABLE>`. The value resolved by `docker compose config`, including an empty value, is retained as its deployment default in `zarf.yaml`.

The bridge renders one `<service>-environment` ConfigMap for each service with environment values and attaches it to that service through `envFrom`. Empty environment ConfigMaps are omitted. Environment and Compose configuration ConfigMaps carry the `uds.dev/pod-reload: "true"` label so UDS can restart dependent Pods when their data changes. Direct Helm deployments do not provide that UDS reload behavior.

ConfigMaps do not protect sensitive data; use Compose `secrets:` for credentials and other confidential values. Environment names must use the portable `[A-Za-z_][A-Za-z0-9_]*` form. Generated Zarf variable names must also be unique across all services, secrets, and reserved package variables such as `ADDITIONAL_NETWORK_ALLOW`; conversion fails rather than emitting an ambiguous package when names collide.

## Deploy-time network access

Every generated package exposes the non-sensitive Zarf variable `ADDITIONAL_NETWORK_ALLOW`, defaulting to `[]`. Its value must be a YAML array of UDS network allow rules. The rules are appended to the generated UDS Package resource so operators can provide environment-specific ingress or egress without modifying the Compose application or rebuilding the package.

Rules declared under `x-uds.network.allow` remain static package defaults for connectivity that Compose cannot express. Compose-derived and static rules are rendered first; `ADDITIONAL_NETWORK_ALLOW` entries are appended afterward.

## Local Dockerfile builds

Docker Compose v5.5.0 or later can pass a build-only service through Compose Bridge without trying to pull Compose's default image name. For earlier Compose versions, run `docker compose build` before conversion so that default image exists locally:

### Docker Compose versions earlier than v5.5.0

```sh
cd examples/full
docker compose build
docker compose bridge convert -t ghcr.io/defenseunicorns-labs/compose-bridge-uds
zarf package create out
```

### Docker Compose versions v5.5.0 or later

```sh
cd examples/full
docker compose bridge convert -t ghcr.io/defenseunicorns-labs/compose-bridge-uds
zarf package create out
```

With Docker Compose versions earlier than v5.5.0, the `docker compose build` step is required to unblock conversion; Zarf still runs the generated Buildx Bake build during `zarf package create`.

The bridge assigns each build service a package-local image reference, writes the canonical build configuration to `out/build.compose.yaml`, and adds Zarf `onCreate` actions that run `docker buildx bake`. The build produces OCI archives under `out/image-archives/`, which Zarf includes through `imageArchives`. User-provided build tags do not become additional package image references.

Local contexts, Dockerfiles, additional contexts, build-secret files, and SSH paths are passed to Buildx as explicit filesystem read allowances. 

Build services default to `linux/amd64` and `linux/arm64`; a Compose `build.platforms` declaration overrides that default for the service.

Deferred builds cannot automatically determine what to expose by examining the Dockerfile because the image is built after Compose Bridge conversion.

## Configuration notes

- **Auto-expose port selection:** For services with multiple published ports, prefer Compose long syntax with `name` or `app_protocol` to identify the web-facing port. Use `x-uds.network.expose[].port` to pin the intended port when inference is ambiguous. The bridge does not automatically skip SSH, DNS, UDP, or other non-HTTP-looking ports.
- **Bind mounts:** Bind mounts are treated as local-development-only inputs and omitted from the rendered chart with a warning. Use a named volume when the data should become a PVC, or a config or secret when the mounted content should be materialized in Kubernetes.
- **Network topology:** A shared Compose network maps to the package namespace. When services join different network sets, the bridge labels Pods by membership and allows communication only between services that share a network.
- **External networks:** Membership among converted services is preserved, but external peers cannot be inferred from the transformation input. Conversion emits a warning; declare cross-package traffic explicitly with `x-uds.network.allow`.
- **Port names:** The bridge preserves explicit Compose port names after Kubernetes-compatible sanitization. For unnamed ports, it generates `port-<number>-<protocol>` (for example, port-5432-tcp) instead of assuming an HTTP port. Conversion fails when two ports in the same service resolve to the same Kubernetes port name.
- **Docker runtime settings:** Host networking, alternate runtimes and platforms, sysctls, Docker discovery labels, and custom stop signals are rejected when they cannot be represented faithfully.

See the [full Compose example](../examples/full/compose.yaml) and [UDS package generation](uds-package.md) for extension keys and generated behavior.
