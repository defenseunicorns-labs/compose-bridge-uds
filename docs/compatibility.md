# Compatibility testing

The bridge is tested against every Compose file in [Docker Awesome Compose](https://github.com/docker/awesome-compose). The corpus is pinned to a reviewed commit so pull request results are reproducible. A scheduled workflow reports upstream changes that need classification.

Each sample is classified as:

- **Supported:** the canonical Compose model converts without dropping declared behavior, and the generated chart passes Helm validation.
- **Unsupported:** conversion stops with stable diagnostic codes and remediation for settings without a portable UDS or Kubernetes equivalent.

The current baseline has 12 supported and 27 intentionally unsupported samples. The checked-in [`manifest.yaml`](../test/awesome-compose/manifest.yaml) is the source of truth for individual results.

## Test levels

Pull requests canonicalize all 39 samples and compare their results with the manifest. This path supplies the same default build image names that Compose Bridge adds after inspecting locally built images, without doing every expensive language build.

The scheduled full suite runs the real lifecycle for every supported sample:

```sh
docker compose build
docker compose bridge convert -t compose-bridge-uds:test
helm lint out/chart
helm template out/chart
uds zarf package create out --confirm
```

The runtime suite deploys WordPress/MySQL, NGINX/Node.js/Redis, and Spring/PostgreSQL to UDS Core Slim Dev. It waits for every Deployment and checks the tenant-gateway endpoint.

Run the fast suite locally with Docker Compose available:

```sh
go run ./test/awesome-compose -mode static
```

Use `-mode full` with Docker, Helm, and a locally built `compose-bridge-uds:test` transformation image. Use `-mode smoke -zarf-command "uds zarf"` only when a UDS cluster is already available.
