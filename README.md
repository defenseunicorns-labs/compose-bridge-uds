# compose-bridge-uds

Convert a Docker Compose application into a deployable [UDS](https://uds.defenseunicorns.com/) package using [Docker Compose Bridge](https://docs.docker.com/compose/bridge/). The transformation consumes a fully-resolved Compose model and emits a Helm chart tailored for UDS, ready for `zarf package create` and `zarf package deploy`.

> [!IMPORTANT]
> **This is not a supported product pathway.** It's an experimental transformation we're sharing to gather feedback. Please enagage with our team on the project [discussions page](https://github.com/defenseunicorns-labs/compose-bridge-uds/discussions) with any questions or suggestions.

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [k3d](https://k3d.io/stable/#releases)
- [UDS CLI](https://uds.defenseunicorns.com/reference/cli/quickstart-and-usage/)

## Quickstart

This walkthrough deploys UDS Core Slim Dev on k3d, then packages and deploys WordPress and MySQL from a Compose file.

```sh
# 1. Create a local k3d cluster with UDS Core Slim Dev
uds zarf package deploy oci://ghcr.io/defenseunicorns/dev/uds/checkpoints/k3d-core-slim-dev:1.9.0

# 2. Transform the Compose application into a UDS Package
cd examples/simple
docker compose bridge convert -t ghcr.io/defenseunicorns-labs/compose-bridge-uds

# 3. Build and deploy the package
zarf package create out/
zarf package deploy zarf-package-wordpress-*.tar.zst
```

The transformation writes a Helm chart to `out/chart/`, a Zarf package definition to `out/zarf.yaml`, and `out/values/` when the Compose file declares secrets.

## Documentation

> [!TIP]
> **Is Compose Bridge a good fit for your application?** Use the [Awesome Compose compatibility matrix](docs/awesome-compose-compatibility-matrix.md) as a practical benchmark. If your application depends on unsupported Compose features or needs deeper package customization, start with the [UDS reference package](https://github.com/uds-packages/reference-package) and build the package directly.

- [Compose support](docs/compose-support.md) describes supported configuration, known limitations, and local Dockerfile builds.
- [UDS package generation](docs/uds-package.md) describes generated resources, `x-uds` extensions, secrets, and policy exemptions.
- [`examples/full/compose.yaml`](examples/full/compose.yaml) demonstrates the complete supported configuration.
