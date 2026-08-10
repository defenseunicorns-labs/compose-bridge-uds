# Awesome Compose Compatibility Matrix

This document records a manually performed compatibility matrix against the corpus of examples in
[Docker Awesome Compose](https://github.com/docker/awesome-compose).

## Perform matrix test

Run the matrix script from any directory with Python 3.9+, Git, Go, and Docker
Compose installed. It tests the current checkout and updates the Results table
below.

```bash
python3 scripts/awesome_compose_matrix.py
```

## Tested baseline

| Input | Value |
|---|---|
| Tested | 2026-08-07 |
| Compose Bridge | `de5d420c2ceba7ca6a35bad4f2aff1bf1344e731` |
| Awesome Compose | [`30f4b7f6a6c3b0c0ecf4d4efb0de203c48d11562`](https://github.com/docker/awesome-compose/commit/30f4b7f6a6c3b0c0ecf4d4efb0de203c48d11562) |
| Docker Compose | `v5.3.1` |
| Go | `go1.25.8 linux/arm64` |

All 39 files canonicalized. The bridge supported and rendered 27 models and
rejected 12 with diagnostics. Rendering only verified generation of
`zarf.yaml`, `chart/Chart.yaml`, and `chart/templates/uds-package.yaml`; it did
not build images, run Helm or Zarf validation, or deploy workloads.

## Results

| Sample | Static result | Diagnostics |
|---|---|---|
| `angular` | Supported |  |
| `apache-php` | Supported |  |
| `aspnet-mssql` | Supported |  |
| `django` | Supported |  |
| `elasticsearch-logstash-kibana` | Rejected | `container-name-alias: services.elasticsearch.container_name`, `container-name-alias: services.kibana.container_name`, `container-name-alias: services.logstash.container_name`, `network-options: networks.elastic` |
| `fastapi` | Rejected | `container-name-alias: services.api.container_name` |
| `flask` | Rejected | `service-field: services.web.stop_signal` |
| `flask-redis` | Rejected | `service-field: services.web.stop_signal` |
| `gitea-postgres` | Supported |  |
| `minecraft` | Supported |  |
| `nextcloud-postgres` | Supported |  |
| `nextcloud-redis-mariadb` | Supported |  |
| `nginx-aspnet-mysql` | Supported |  |
| `nginx-flask-mongo` | Rejected | `service-field: services.backend.stop_signal` |
| `nginx-flask-mysql` | Supported |  |
| `nginx-golang` | Supported |  |
| `nginx-golang-mysql` | Supported |  |
| `nginx-golang-postgres` | Supported |  |
| `nginx-nodejs-redis` | Supported |  |
| `nginx-wsgi-flask` | Supported |  |
| `pihole-cloudflared-DoH` | Rejected | `network-options: networks.dns-net`, `network-options: services.cloudflared.networks.dns-net` |
| `plex` | Rejected | `service-field: services.plex.network_mode` |
| `portainer` | Supported |  |
| `postgresql-pgadmin` | Supported |  |
| `prometheus-grafana` | Supported |  |
| `react-express-mongodb` | Rejected | `service-field: services.frontend.stdin_open` |
| `react-express-mysql` | Supported |  |
| `react-java-mysql` | Supported |  |
| `react-nginx` | Supported |  |
| `react-rust-postgres` | Supported |  |
| `sparkjava` | Supported |  |
| `sparkjava-mysql` | Supported |  |
| `spring-postgres` | Supported |  |
| `traefik-golang` | Rejected | `service-field: services.backend.labels` |
| `vuejs` | Supported |  |
| `wasmedge-kafka-mysql` | Rejected | `service-field: services.etl.platform`, `service-field: services.etl.runtime` |
| `wasmedge-mysql-nginx` | Rejected | `service-field: services.backend.platform`, `service-field: services.backend.runtime` |
| `wireguard` | Rejected | `service-field: services.wireguard.sysctls` |
| `wordpress-mysql` | Supported |  |

## Diagnostics

| Diagnostic | Affected samples | Disposition |
|---|---|---|
| `container_name` | `elasticsearch-logstash-kibana`, `fastapi` | The proposed bridge change makes this non-fatal and retains the Compose service name for Kubernetes resources. |
| `network.driver` | `elasticsearch-logstash-kibana` | The proposed bridge change accepts an otherwise ordinary `bridge` network. |
| `stop_signal` | `flask`, `flask-redis`, `nginx-flask-mongo` | Defer to broader Kubernetes/UDS support; define `STOPSIGNAL` in the image. |
| `network.ipv4_address` | `pihole-cloudflared-DoH` | Keep unsupported; use service DNS and cluster-assigned addresses. |
| `network.ipam` | `pihole-cloudflared-DoH` | Keep unsupported; do not reproduce Docker address management in Kubernetes. |
| `network_mode` | `plex` | Keep host networking unsupported; use explicit Kubernetes and UDS networking. |
| `stdin_open` | `react-express-mongodb` | The proposed bridge change maps this to the Kubernetes container `stdin` field. |
| `labels` | `traefik-golang` | Keep Docker discovery unsupported; use Kubernetes Services and UDS exposure. |
| `runtime` | `wasmedge-kafka-mysql`, `wasmedge-mysql-nginx` | Defer to broader RuntimeClass support in Kubernetes and UDS. |
| `sysctls` | `wireguard` | Keep the unsafe sysctl and accompanying host kernel dependencies unsupported. |

RuntimeClass, sysctl, and stop-signal mappings should move only with broader
platform support. Kubernetes stop signals still depend on the alpha
[`ContainerStopSignals`](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#defining-custom-stop-signals)
feature as of Kubernetes 1.36. The WASI runtime names are not portable
[RuntimeClass](https://kubernetes.io/docs/concepts/containers/runtime-class/)
names, and the WireGuard setting is not a
[safe sysctl](https://kubernetes.io/docs/tasks/administer-cluster/sysctl-cluster/).
