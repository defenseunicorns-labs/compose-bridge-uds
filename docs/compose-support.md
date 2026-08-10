# Compose support

## Supported configuration

| Compose key                                                      | Behavior                                                                                                                                                                                                                                                                        |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `image:`                                                         | Container image reference.                                                                                                                                                                                                                                                      |
| `build:`                                                         | Preserved as a temporary Buildx Bake definition. `zarf package create` builds the image and adds its OCI archive to the package.                                                                                                                                                 |
| Named `volumes:`                                                 | Converted to PersistentVolumeClaims (`1Gi`, `ReadWriteOnce` by default).                                                                                                                                                                                                        |
| `secrets:`                                                       | Delivered to containers as read-only files. Secrets used only by packaged services become package-owned Kubernetes Secrets. Secrets shared with an `x-uds-exclude` service, and native external secrets, reference deployment-provided Kubernetes Secret names and keys. |
| `configs:`                                                       | Converted to ConfigMaps. Must use inline `content:` (no external file references).                                                                                                                                                                                              |
| `environment:`, `env_file:`                                      | Resolved by `docker compose config`, exposed as non-sensitive Zarf variables, and rendered into one reloadable ConfigMap per packaged service. The Deployment imports that ConfigMap with `envFrom`.                                                                              |
| `depends_on:`                                                    | Converted to init-container wait logic using `netcat` (busybox). The dependency must declare a port.                                                                                                                                                                            |
| `healthcheck:`                                                   | `CMD` and `CMD-SHELL` forms convert to Kubernetes liveness probes.                                                                                                                                                                                                              |
| `container_name:`                                                | Ignored with a warning. Kubernetes Service and Deployment names come from the Compose service name.                                                                                                                                                                             |
| `stdin_open:`                                                    | Maps to the Kubernetes container `stdin` field.                                                                                                                                                                                                                                 |
| `deploy.resources`                                               | `limits` and `reservations` map to Pod resource requests and limits.                                                                                                                                                                                                            |
| `ports:`                                                         | Published ports are auto-exposed through the UDS tenant gateway. `expose:` (internal-only) ports are not exposed externally. Compose port declaration order is preserved, and long-syntax `name` and `app_protocol` hints are used to prefer web ports for multi-port services. |
| `hostname:`                                                      | Preserved as the Kubernetes Pod hostname.                                                                                                                                                                                                                                       |
| `networks:`                                                      | Service membership is preserved with Pod labels and selector-scoped UDS ingress and egress rules when services use different network sets. The ordinary `bridge` driver is accepted. External networks warn because external peers cannot be inferred; aliases, addresses, and driver options remain unsupported. |
| `x-uds-exclude: true`                                            | Excludes a service from package generation while leaving ordinary Docker Compose behavior unchanged. Resources used only by excluded services are omitted; shared resources remain.                                                                                            |
| Bind mounts                                                      | Skipped during conversion with a warning because host paths do not have a portable Kubernetes equivalent. Use named `volumes:`, `configs:`, or `secrets:` for data that should be rendered into the chart.                                                                       |
| `user:`, `privileged:`, `cap_add:`, `cap_drop:`, `security_opt:` | Reflected in the container security context where applicable. Settings that require UDS policy exceptions also generate a `chart/templates/uds-exemption.yaml`.                                                                                                                 |

External configs referenced by an included service are not supported. A
packaged `configs:` entry must define inline `content:`.

## Excluding development services

Use the service-level `x-uds-exclude: true` extension for dependencies that are
useful in local Compose development but should not be carried into persistent
environments:

```yaml
services:
  api:
    image: example/api:1.0.0
    depends_on:
      - db

  db:
    image: postgres:18
    x-uds-exclude: true
```

Compose ignores extension fields, so both services still run with `docker
compose up`. The bridge omits the excluded service, its image, dependencies on
it, and volumes, configs, or secrets used only by it. A resource shared with an
included service remains relevant to the package. A shared secret becomes
package-external: local Compose still reads its declared source, while the
generated deployment mounts a key from an existing Kubernetes Secret. Explicit
`x-uds.network.expose` and `x-uds.monitor` entries must not refer to excluded
services.

## Runtime secrets

Compose grants a service access to secrets as read-only files, normally under
`/run/secrets`. The generated chart preserves that application interface.

- A secret consumed only by included services is package-owned. Zarf prompts
  for its sensitive value, and the chart creates the Kubernetes Secret.
- A secret consumed only by excluded services is omitted.
- A secret consumed by included and excluded services is package-external.
- A native Compose `external: true` secret consumed by an included service is
  also package-external.

For every package-external Compose secret, the generated Zarf package declares
`<SECRET>_SECRET_NAME` and `<SECRET>_SECRET_KEY` variables. The name has no
default and must identify an existing Kubernetes Secret in the package
namespace. The key defaults to the normalized Compose secret name. These
variables configure the generated Pod volume; they are not application
environment variables. The application continues reading the secret from its
Compose file target.

Different Compose secrets can select different keys in the same Kubernetes
Secret. This is useful for operators that publish one credential Secret with
keys such as `username` and `password`. Build secrets are unaffected by this
runtime-secret behavior.

## Runtime configuration

Every environment variable on a packaged service becomes a non-sensitive Zarf
variable named `<SERVICE>_<ENV_NAME>`. Its resolved Compose value is the
default, so an unchanged package behaves like `docker compose up`, while a UDS
bundle or day-two deployment can override the value. Variables ending in
`_FILE` follow the same rule; their referenced Compose secrets remain mounted
as files.

The generated chart groups these values into one `<service>-environment`
ConfigMap per service and imports it into the workload with `envFrom`. Each
ConfigMap has the `uds.dev/pod-reload: "true"` label, allowing UDS Core to roll
only the consuming workload when configuration changes. A service without
environment entries does not receive an empty ConfigMap. A direct Helm install
still consumes the ConfigMap, but without UDS Core it requires a manual workload
restart after configuration changes.

ConfigMaps are not secret storage. Put credentials and other sensitive values
in Compose `secrets:` rather than `environment:`. Environment names must use
the portable form `[A-Za-z_][A-Za-z0-9_]*`; conversion fails rather than
allowing Kubernetes to silently skip a key imported through `envFrom`.

## Deploy-time network access

Every generated package exposes the non-sensitive Zarf variable
`ADDITIONAL_NETWORK_ALLOW`, defaulting to `[]`. Its value is appended to the UDS
Package CR's `network.allow` list, allowing a bundle or day-two deployment to
provide environment-specific ingress or egress without modifying Compose or
rebuilding the package.

Rules under `x-uds.network.allow` remain static package defaults for invariant
connectivity that Compose cannot express. Compose-derived and static rules are
rendered first; `ADDITIONAL_NETWORK_ALLOW` entries are appended afterward.

## Local Dockerfile builds

For services with `build:`, conversion writes `out/build.compose.yaml` and adds a Zarf `onCreate` action. Package creation runs Buildx Bake and writes one temporary OCI archive per service under `out/image-archives/`.

Current Docker Compose Bridge versions inspect or pull every service image before invoking a transformation, including build services whose image does not exist yet. Until Docker Compose skips that lookup for `build:`, use an explicit conversion-only overlay that points each build service at the transformation image:

```yaml
# compose.bridge.yaml
services:
  server:
    image: ${TRANSFORM_IMAGE:-ghcr.io/defenseunicorns-labs/compose-bridge-uds:edge}
```

Apply the overlay only to conversion:

```sh
cd examples/full
TRANSFORM_IMAGE=ghcr.io/defenseunicorns-labs/compose-bridge-uds:edge \
  docker compose \
    -f compose.yaml \
    -f compose.bridge.yaml \
    bridge convert \
    -t ghcr.io/defenseunicorns-labs/compose-bridge-uds:edge
zarf package create out
```

The placeholder is used only by Compose Bridge's pre-transformation inspection. Because the service still has `build:`, this transformer discards the placeholder and emits its package-owned image reference. Do not use the overlay with `docker compose build` or `docker compose up`.

Built services always use `zarf.internal/<package>-<service>:<package-version>` in the generated chart and image archive. When a service declares both `build:` and `image:`, the build definition wins and the bridge-controlled reference replaces `image:` for packaging. Services with only `image:` continue to be pulled through Zarf's normal `images` list.

Builds target `linux/amd64` and `linux/arm64` by default. A non-empty Compose `build.platforms` list overrides that default. Canonical Compose build options—including contexts, Dockerfile selection, arguments, targets, secrets, SSH, and cache settings—flow into the temporary build definition. Because that definition lives under `out/`, the generated Bake command grants `fs.read` only to its canonical local contexts and other local build inputs.

Docker with the Buildx plugin is required during package creation. Conversion and package creation must run from the same workspace while the source paths referenced by `build:` still exist. The generated `out/` tree, its absolute source paths, and its OCI archives are disposable; the completed Zarf package is the portable artifact.

## Configuration notes

- **Auto-expose port selection:** For services with multiple published ports, prefer Compose long syntax with `name` or `app_protocol` to identify the web-facing port. Use `x-uds.network.expose[].port` to pin the intended port when inference is ambiguous. The bridge does not automatically skip SSH, DNS, UDP, or other non-HTTP-looking ports.
- **Bind mounts:** Bind mounts are treated as local-development-only inputs and omitted from the rendered chart with a warning. Use a named volume when the data should become a PVC, or a config or secret when the mounted content should be materialized in Kubernetes.
- **Network topology:** A shared Compose network maps to the package namespace. When services join different network sets, the bridge labels Pods by membership and allows communication only between services that share a network.
- **External networks:** Membership among converted services is preserved, but external peers cannot be inferred from the transformation input. Conversion emits a warning; declare cross-package traffic explicitly with `x-uds.network.allow`.
- **Generated port names:** Compose long-syntax `name` values are preserved after Kubernetes-compatible sanitization. Unnamed ports use the neutral `port-<number>-<protocol>` form; the bridge does not assume that the first TCP port carries HTTP traffic.
- **Docker runtime settings:** Host networking, alternate runtimes and platforms, sysctls, Docker discovery labels, and custom stop signals are rejected when they cannot be represented faithfully.

See the [full Compose example](../examples/full/compose.yaml) and [UDS package generation](uds-package.md) for extension keys and generated behavior.
