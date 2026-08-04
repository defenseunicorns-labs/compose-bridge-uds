# Compose support

## Supported configuration

| Compose key                                                      | Behavior                                                                                                                                                                                                                                                                        |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `image:`                                                         | Container image reference.                                                                                                                                                                                                                                                      |
| `build:`                                                         | Build from a Dockerfile. Run `docker compose build` before `docker compose bridge convert`; the bridge conversion does not build images.                                                                                                                                        |
| Named `volumes:`                                                 | Converted to PersistentVolumeClaims (`1Gi`, `ReadWriteOnce` by default).                                                                                                                                                                                                        |
| `secrets:`                                                       | Converted to Kubernetes Secrets.                                                                                                                                                                                                                                                |
| `configs:`                                                       | Converted to ConfigMaps. Must use inline `content:` (no external file references).                                                                                                                                                                                              |
| `environment:`, `env_file:`                                      | Resolved by `docker compose config` and injected as container environment variables.                                                                                                                                                                                            |
| `depends_on:`                                                    | Converted to init-container wait logic using `netcat` (busybox). The dependency must declare a port.                                                                                                                                                                            |
| `healthcheck:`                                                   | `CMD` and `CMD-SHELL` forms convert to Kubernetes liveness probes.                                                                                                                                                                                                              |
| `deploy.resources`                                               | `limits` and `reservations` map to Pod resource requests and limits.                                                                                                                                                                                                            |
| `ports:`                                                         | Published ports are auto-exposed through the UDS tenant gateway. `expose:` (internal-only) ports are not exposed externally. Compose port declaration order is preserved, and long-syntax `name` and `app_protocol` hints are used to prefer web ports for multi-port services. |
| `hostname:`                                                      | Preserved as the Kubernetes Pod hostname.                                                                                                                                                                                                                                       |
| Bind mounts                                                      | Skipped during conversion with a warning because host paths do not have a portable Kubernetes equivalent. Use named `volumes:`, `configs:`, or `secrets:` for data that should be rendered into the chart.                                                                       |
| `user:`, `privileged:`, `cap_add:`, `cap_drop:`, `security_opt:` | Reflected in the container security context where applicable. Settings that require UDS policy exceptions also generate a `chart/templates/uds-exemption.yaml`.                                                                                                                 |

External configs are not supported. A `configs:` entry must define inline `content:`.

## Local Dockerfile builds

Docker Compose Bridge does not run the Compose build step. If a service uses `build:` with a local Dockerfile, build the Compose project before running the bridge conversion so the image exists locally:

```sh
cd examples/full
docker compose build
docker compose bridge convert -t ghcr.io/defenseunicorns-labs/compose-bridge-uds
```

When `image:` is omitted, Compose assigns a default image name based on the project and service, such as `hello-world-server` for `examples/full`. Compose Bridge resolves that built image before invoking the transformation. Declare `image:` when the generated Helm chart and `zarf.yaml` need a stable or registry-backed reference.

## Configuration notes

- **Auto-expose port selection:** For services with multiple published ports, prefer Compose long syntax with `name` or `app_protocol` to identify the web-facing port. Use `x-uds.network.expose[].port` to pin the intended port when inference is ambiguous. The bridge does not automatically skip SSH, DNS, UDP, or other non-HTTP-looking ports.
- **Bind mounts:** Bind mounts are treated as local-development-only inputs and omitted from the rendered chart with a warning. Use a named volume when the data should become a PVC, or a config or secret when the mounted content should be materialized in Kubernetes.
- **Network topology:** One shared Compose network maps to the package namespace. Different service network memberships are rejected because flattening them would remove the isolation expressed by the Compose model.
- **Docker runtime settings:** Host networking, alternate runtimes and platforms, sysctls, Docker discovery labels, and custom stop signals are rejected when they cannot be represented faithfully.

See the [full Compose example](../examples/full/compose.yaml) and [UDS package generation](uds-package.md) for extension keys and generated behavior.
