# Awesome Compose spike

This branch is a long-lived, manually maintained compatibility spike against
[Docker Awesome Compose](https://github.com/docker/awesome-compose). It keeps the
experiment out of project CI and records each reviewed corpus sweep here.

## Baseline

| Input | Value |
|---|---|
| Tested | 2026-08-07 |
| Compose Bridge | `de5d420c2ceba7ca6a35bad4f2aff1bf1344e731` |
| Awesome Compose | [`30f4b7f6a6c3b0c0ecf4d4efb0de203c48d11562`](https://github.com/docker/awesome-compose/commit/30f4b7f6a6c3b0c0ecf4d4efb0de203c48d11562) |
| Docker Compose | `v5.3.1` |
| Go | `go1.25.8 linux/arm64` |

All 39 Compose files canonicalized successfully. The canonical models were then
loaded by the bridge and checked against its compatibility rules. Supported
models also rendered successfully and produced `zarf.yaml`, `chart/Chart.yaml`,
and `chart/templates/uds-package.yaml`.

The refreshed static sweep found **27 supported** and **12 rejected with
actionable diagnostics**. Preserving Compose network membership moved five
samples to supported and removed `network-topology` from the remaining
`react-express-mongodb` diagnostic. Bind mounts affected 23 samples, but the
bridge skips them with a warning instead of rejecting the model. Thirteen
supported samples consequently have runtime caveats.

This sweep did not build images, run Helm validation, create Zarf packages, or
deploy workloads. A supported result is therefore not a runtime certification.

## Results

| Sample | Static result | Diagnostics |
|---|---|---|
| `angular` | Supported | |
| `apache-php` | Supported | |
| `aspnet-mssql` | Supported | |
| `django` | Supported | |
| `elasticsearch-logstash-kibana` | Rejected | `container-name-alias`, `network-options` |
| `fastapi` | Rejected | `container-name-alias` |
| `flask-redis` | Rejected | `service-field` |
| `flask` | Rejected | `service-field` |
| `gitea-postgres` | Supported | |
| `minecraft` | Supported | |
| `nextcloud-postgres` | Supported | |
| `nextcloud-redis-mariadb` | Supported | |
| `nginx-aspnet-mysql` | Supported | |
| `nginx-flask-mongo` | Rejected | `service-field` |
| `nginx-flask-mysql` | Supported | |
| `nginx-golang-mysql` | Supported | |
| `nginx-golang-postgres` | Supported | |
| `nginx-golang` | Supported | |
| `nginx-nodejs-redis` | Supported | |
| `nginx-wsgi-flask` | Supported | |
| `pihole-cloudflared-DoH` | Rejected | `network-options` |
| `plex` | Rejected | `service-field` |
| `portainer` | Supported | |
| `postgresql-pgadmin` | Supported | |
| `prometheus-grafana` | Supported | |
| `react-express-mongodb` | Rejected | `service-field` |
| `react-express-mysql` | Supported | |
| `react-java-mysql` | Supported | |
| `react-nginx` | Supported | |
| `react-rust-postgres` | Supported | |
| `sparkjava-mysql` | Supported | |
| `sparkjava` | Supported | |
| `spring-postgres` | Supported | |
| `traefik-golang` | Rejected | `service-field` |
| `vuejs` | Supported | |
| `wasmedge-kafka-mysql` | Rejected | `service-field` |
| `wasmedge-mysql-nginx` | Rejected | `service-field` |
| `wireguard` | Rejected | `service-field` |
| `wordpress-mysql` | Supported | |

## Gap analysis

The 12 rejected samples do not represent 12 equivalent bridge defects. The
corpus mixes deployable application definitions with local development mounts,
Docker-host management workloads, and runtime-specific examples. Conversely,
the 27 supported results do not all represent deployable workloads: a static
success can include bind mounts that the bridge intentionally omitted.

A Compose Bridge transformation receives the resolved model at
`/in/compose.yaml`; it does not receive the project files referenced by bind
mounts. This is an important boundary for the analysis below. See the
[Compose Bridge transformation contract](https://docs.docker.com/compose/bridge/)
for details.

### Gap summary

Counts overlap because several samples have more than one blocker.

| Gap | Samples | Assessment | Disposition |
|---|---:|---|---|
| Skipped bind mounts | 23 | The transform cannot read the source content, and a Kubernetes `hostPath` would refer to a cluster node rather than the Compose host. Static conversion omits the mount with a warning, which may also omit required application code, configuration, or data. | Keep the warning and skip behavior for local-development inputs, but require lifecycle validation before calling the output deployable. Use a production Compose override, bake content into the image, use an inline config, or use a named volume. |
| Unsupported service fields | 9 | This diagnostic combines portable fields, experimental Kubernetes fields, and Docker-specific behavior. It needs field-specific handling rather than one policy. | Split as described below. |
| `container_name` | 2 | This is a [Docker runtime naming constraint](https://docs.docker.com/reference/compose-file/services/#container_name), not the Compose service name. Kubernetes object identity already follows the service name. | Ignore with a documented warning instead of failing conversion. |
| Network options | 2 | The ordinary Compose `bridge` driver is compatible with the bridge's shared-network model. Static addresses and IPAM are not portable, especially when application configuration embeds the address. | Accept the plain `bridge` driver; continue rejecting aliases, addresses, IPAM, and driver options until individually mapped. |

### Recommended bridge backlog

Compose network membership was implemented in `de5d420`. Five topology-only
samples are now supported, while `react-express-mongodb` remains rejected only
because of `stdin_open`. The sweep no longer reports `network-topology` for any
sample.

1. **Treat `container_name` as non-fatal.** This immediately adds `fastapi` to
   the static supported set. `elasticsearch-logstash-kibana` also loses one of
   its two blocker classes. Preserve service names as Kubernetes Service and
   Deployment names; do not use the Docker container name as the workload name.

2. **Handle small portable cases separately.** Map `stdin_open` to the
   Kubernetes container `stdin` field and accept a top-level network whose only
   option is `driver: bridge`. The first adds `react-express-mongodb`; the second
   adds `elasticsearch-logstash-kibana` once item 1 also lands.

3. **Add platform extensions only when there is a UDS use case.** A future
   `x-uds.runtimeClassName` could select a preconfigured Kubernetes
   [RuntimeClass](https://kubernetes.io/docs/concepts/containers/runtime-class/),
   but the raw Compose runtime handler is not a portable RuntimeClass name.
   Likewise, [safe namespaced sysctls](https://kubernetes.io/docs/tasks/administer-cluster/sysctl-cluster/)
   could be mapped selectively. The WireGuard sysctl in this corpus is not on
   Kubernetes' safe list and also depends on host kernel mounts, so generic
   sysctl support would not make it deployable.

4. **Defer `stop_signal`.** Kubernetes exposes container stop signals only
   through the alpha
   [`ContainerStopSignals`](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#defining-custom-stop-signals)
   feature, which is disabled by default as of Kubernetes 1.36. Until the UDS
   Kubernetes baseline supports it, the portable remedy is to set `STOPSIGNAL`
   in the image. A synthetic `preStop` command would depend on utilities and
   process layout inside an arbitrary image and would not be a faithful
   translation.

Item 1 would move the unmodified corpus from 27 to **28 statically supported
samples**. Mapping `stdin_open` in item 2 would bring the estimate to 29; also
accepting an otherwise ordinary `bridge` network would bring the combined
estimate to 30. These totals still include samples whose bind mounts were
skipped, so they measure static bridge coverage rather than deployability.

### Statically supported after bind skipping

These thirteen samples no longer have a compatibility blocker, but their generated
packages omit content or host access declared by the Compose model.

| Sample | Omitted bind content | Recommended path before deployment |
|---|---|---|
| `angular` | Development source tree | Remove the mount in a production override; the selected image stage already copies the source. |
| `apache-php` | Application source tree | Update the image to copy the PHP application, then remove the mount. |
| `minecraft` | Host persistence path | Replace the host directory with a named volume/PVC and migrate the data separately. |
| `nginx-golang-mysql` | NGINX configuration | Bake the config into an image or declare it as an inline Compose config. |
| `nginx-golang-postgres` | NGINX configuration | Bake the config into an image or declare it as an inline Compose config. |
| `nginx-golang` | NGINX configuration | Bake the config into an image or declare it as an inline Compose config. |
| `nginx-wsgi-flask` | NGINX runtime configuration | Bake the config into the existing image or declare it as an inline config. |
| `portainer` | Docker socket | Use Portainer's Kubernetes deployment model; do not expose a node container-runtime socket. |
| `prometheus-grafana` | Provisioning directories | Package provisioning files in images or inline configs; retain the Prometheus named volume as a PVC. |
| `react-express-mysql` | Development backend and frontend source files | Use production image stages and remove the development binds. |
| `react-java-mysql` | Development frontend source tree | Select the production frontend stage and remove the bind. |
| `react-rust-postgres` | Development backend and frontend source trees | Select production image stages and remove the binds. |
| `vuejs` | Development source tree | Remove the mount in a production override; the image already copies the source. |

### Rejected sample disposition

| Sample | What is missing | Recommended path |
|---|---|---|
| `elasticsearch-logstash-kibana` | Custom container names and explicit `bridge` network; file binds are skipped | Put the Logstash files in the image or inline configs; make container names non-fatal and accept the ordinary bridge driver. |
| `fastapi` | Custom container name | Make `container_name` non-fatal in the bridge. |
| `flask-redis` | `stop_signal`; development source bind is skipped | Remove the redundant source mount and set `STOPSIGNAL SIGINT` in the image. |
| `flask` | `stop_signal` | Set `STOPSIGNAL SIGINT` in the image until Kubernetes stop signals are a supported baseline feature. |
| `nginx-flask-mongo` | `stop_signal`; NGINX config and development source binds are skipped | Bake or inline the NGINX config, remove the redundant source mount, and set the image stop signal. |
| `pihole-cloudflared-DoH` | Static container IP and IPAM; host state paths are skipped | Use named storage and service DNS, if Pi-hole accepts it. Do not emulate Docker IPAM or node paths in the bridge. |
| `plex` | Host networking; host media path is skipped | Use cluster storage and explicit Kubernetes/UDS networking. Treat discovery and media access as workload-specific migration work. |
| `react-express-mongodb` | `stdin_open`; development source binds are skipped | Use production image stages and map `stdin_open` explicitly. |
| `traefik-golang` | Docker-provider labels; Docker socket bind is skipped | Replace Docker discovery with the generated Kubernetes Service and UDS expose configuration. |
| `wasmedge-kafka-mysql` | WASI platform/runtime; source bind is skipped | Bake the mounted content and use an explicit, cluster-supported RuntimeClass extension if WASI is a UDS requirement. |
| `wasmedge-mysql-nginx` | WASI platform/runtime; static-content bind is skipped | Bake the frontend content and use an explicit, cluster-supported RuntimeClass extension if required. |
| `wireguard` | Unsafe sysctl; host kernel/module mounts are skipped | Keep unsupported. Use a cluster-native networking/VPN design with explicit node administration. |

### Explicit non-goals

The bridge should not generate `hostPath` volumes from Compose binds, enable
host networking by default, copy Docker discovery labels into Kubernetes, invent
cluster IP addresses, or assume alternate container runtimes exist. Kubernetes
can express some of these primitives, but UDS policy and cluster configuration
make them security and operations decisions rather than portable application
translations.

## Repeat the sweep

The one-off static harness was removed from the project in commit `53cb34b`. It
can be recovered into a temporary directory for a manual sweep without restoring
CI or permanent test machinery:

```sh
awesome_dir="$(mktemp -d)/awesome-compose"
report_dir="$(mktemp -d)"
harness_dir=".tmp-awesome-compose"

git clone https://github.com/docker/awesome-compose.git "$awesome_dir"
git -C "$awesome_dir" checkout 30f4b7f6a6c3b0c0ecf4d4efb0de203c48d11562

mkdir -p "$harness_dir"
git show 23559f9:test/awesome-compose/main.go > "$harness_dir/main.go"

go run "./$harness_dir" \
  -mode static \
  -manifest docs/awesome-compose-manifest.yaml \
  -repo "$awesome_dir" \
  -json "$report_dir/report.json" \
  -markdown "$report_dir/report.md"

rm -rf "$harness_dir"
```

Review `report.md` before accepting a changed result. When advancing the pinned
Awesome Compose revision, first confirm that every discovered Compose file has a
row in the results table, then update the baseline metadata and results together.

For deeper validation of a supported sample, run the real lifecycle from its
Awesome Compose directory:

```sh
docker build --tag compose-bridge-uds:test /path/to/compose-bridge-uds
docker compose build
docker compose bridge convert -t compose-bridge-uds:test
helm lint out/chart
helm template out/chart
uds zarf package create out --confirm
```

Deployment testing additionally requires a UDS cluster and workload-specific
health checks. Record those results separately from the static classification.
