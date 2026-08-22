# UDS package generation

The bridge maps the [Compose Specification](https://compose-spec.io/) to Kubernetes resources and synthesizes a [UDS Package CR](https://docs.defenseunicorns.com/core/reference/operator--crds/packages-v1alpha1-cr/) for network policy, monitoring, SSO, and trust-bundle distribution. It renders these resources as a Helm chart under `out/chart/`, referenced by `out/zarf.yaml` with `localPath: chart`.

For services with `build:`, the bridge also writes `out/build.compose.yaml`. Zarf `onCreate` actions use Buildx Bake to build those services into OCI archives under `out/image-archives/`, and the component's `imageArchives` entries add them to the package.

Package-owned secrets are rendered from chart values rather than baked into templates. Package-external secrets carry only non-sensitive Kubernetes Secret name and key variables; the chart neither includes their values nor creates their Secret objects. Service environment values are exposed as non-sensitive Zarf variables and rendered into per-service ConfigMaps. Every package also exposes `DOMAIN` for generated endpoints and `ADDITIONAL_NETWORK_ALLOW` for deploy-time UDS network rules. The bridge writes `out/values/values.yaml` with `###ZARF_VAR_*###` placeholders for these deploy-time values, references it through `charts[].valuesFiles`, and retains their defaults, prompts, indentation, and sensitivity settings in the Zarf package's `variables:`.

## Inferred behavior

- **Expose:** Services with published `ports:` are exposed on the tenant gateway. For multi-port services, the bridge prefers Compose `app_protocol` or `name` values indicating web traffic, then falls back to the first published port.
- **Network allow:** Intra-namespace ingress and egress rules are always included so services in the namespace can communicate. Static `x-uds.network.allow` entries follow inferred rules, and deploy-time `ADDITIONAL_NETWORK_ALLOW` entries are appended last.
- **SSO:** A Keycloak client is generated for the first exposed service and omitted when no services are exposed. Inferred redirect URIs use the package's deploy-time `DOMAIN`, which defaults to `uds.dev`.
- **Policy exemptions:** Services requiring UDS policy exceptions produce `chart/templates/uds-exemption.yaml`.
- **Monitoring:** Monitoring is opt-in through `x-uds.monitor[]`.
- **Development dependencies:** Services referenced only by `depends_on` entries with `required: false` are omitted along with resources used exclusively by them.

## Extension keys

Use `x-uds` [Compose extension keys](https://docs.docker.com/reference/compose-file/extension/) only when inferred behavior needs to be overridden.

| Key | Purpose |
|---|---|
| `x-uds.package.name` | Package name (default: Compose project name). |
| `x-uds.package.namespace` | Package namespace (default: Compose project name). |
| `x-uds.package.version` | Package version (default: `0.1.0`). |
| `x-uds.network.expose[]` | Replace inferred expose rules. Missing fields are inferred from the service. |
| `x-uds.network.allow[]` | Add network allow rules, deduplicated against inferred rules. |
| `x-uds.monitor[]` | Add Prometheus monitoring rules, with service metadata inferred when possible. |
| `x-uds.sso[]` | Replace inferred SSO clients. Set `x-uds.sso: []` to disable inferred SSO. |
| `x-uds.caBundle.configMap` | Customize the operator-managed trust bundle ConfigMap metadata. |

For example, override only the host for an exposed service:

```yaml
x-uds:
  network:
    expose:
      - service: server
        host: hello-world
```

### Extension notes

- **`x-uds.sso`:** Missing `clientId`, `name`, `redirectUris`, and `enableAuthserviceSelector` fields are inferred. Inferred redirect URIs use `DOMAIN`; explicitly supplied redirect URIs remain unchanged and in declaration order. An explicitly empty list disables inferred SSO without removing `DOMAIN` from the package interface.
- **`x-uds.monitor[]`:** Entries may be raw UDS `spec.monitor[]` items or use the bridge-only `service` key to infer labels and port metadata. Set `portName` or `targetPort` for multi-port services.
- **`x-uds.caBundle.configMap`:** This customizes the namespace trust-bundle ConfigMap. Trust bundle contents are configured separately in UDS Core.

See the [full Compose example](../examples/full/compose.yaml) for all supported extensions.

## Policy exemptions

UDS Core expects containers to run as non-root, avoid privileged mode, drop Linux capabilities, and use an approved seccomp profile. When Compose security settings conflict with those policies, the bridge generates an `Exemption` scoped to each matching service. No exemption is emitted when all services comply.

Prefer changing the image or Compose configuration to meet the UDS baseline. Keep an exemption when the workload cannot run correctly without the less-restricted setting.

| Compose input | Generated UDS policy exemption |
|---|---|
| `user: root`, `user: 0`, or equivalent UID/GID forms | `RequireNonRootUser` |
| `privileged: true` | `DisallowPrivileged` |
| `cap_add:` | `DropAllCapabilities`, `RestrictCapabilities` |
| `security_opt: [seccomp:unconfined]` | `RestrictSeccomp` |
| `cap_drop: [ALL]` | No exemption; rendered into the container `securityContext.capabilities.drop` instead. |

Example Compose input:

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

Generated exemption:

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
