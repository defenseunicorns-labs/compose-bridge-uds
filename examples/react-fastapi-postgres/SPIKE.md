# Compose Bridge Packaging Spike

Status: investigation in progress

This document records findings from comparing the package generated under
`out/` with the hand-authored Hello World package in the Mission Application
Template. It separates confirmed bridge behavior from proposals and pending
architecture decisions.

## Scope and assumptions

- This example represents a deliberately small application.
- It does not need to expose every possible Helm or Kubernetes setting.
- The generated package is expected to run on UDS Core, so the security
  mutations UDS Core applies by default are part of the runtime contract.
- Runtime correctness and a simple build/deploy workflow matter more than
  reproducing every feature of a hand-authored Helm chart.
- The desired build flow remains:

  ```text
  Compose source -> Compose Bridge workspace -> Zarf package
  ```

## Confirmed bridge behavior

### Package structure

The bridge currently generates one Helm chart and one required Zarf component.
Every service visible in the canonical Compose model is placed in that chart.
There is currently no service-to-component partitioning.

### Compose profiles

The bridge parses and retains the `profiles` attached to each service, but the
renderer does not currently use them.

Docker Compose removes inactive profiled services before the transformer sees
the canonical model:

- Conversion without `--profile dev` excludes a service in the `dev` profile.
- Conversion with `--profile dev` includes the service and preserves its
  profile metadata.

This can produce separate application-only and development package variants
today. It cannot produce one package containing an optional development
component without additional bridge work.

A required service cannot retain `depends_on` for an inactive profiled service;
Docker Compose rejects that model before conversion.

### Helm values and legacy Zarf variables

The bridge currently uses Helm values only to transport generated Compose
secret values:

```text
Compose secret
  -> sensitive legacy Zarf variable
  -> values/values.yaml
  -> Helm .Values.secrets.NAME
  -> Kubernetes Secret
```

The bridge does not currently expose arbitrary Helm values or user-defined Zarf
variables through `x-uds`. Environment variables, domains, replicas, and other
settings are rendered as literals.

### Zarf Package Values

The bridge does not currently generate first-class Zarf Package Values. It does
not emit top-level `values.files`, a values schema, or chart `sourcePath` to
`targetPath` mappings.

Zarf Package Values provide structured YAML configuration, deploy-time values
files, `--set-values`, JSON Schema validation, and mappings into Helm chart
values. Packaged defaults are overridden by deploy-time values files and then
by `--set-values`.

The locally installed Zarf `v0.77.0` accepts Package Values when the
`values=true` feature is enabled. This was verified with `zarf dev lint`:

```sh
uds zarf dev lint <package-directory> --features values=true
```

Reference: <https://docs.zarf.dev/ref/package-values/>

### External Compose secrets

An external Compose secret is referenced by the generated Deployment but is not
created by the generated chart. A local Compose override can replace an
external secret with a development file.

There is a bridge bug in the current Zarf generation: external secrets are
correctly skipped when rendering Kubernetes Secret resources, but they are not
skipped when generating promptable legacy Zarf variables. This can cause a
prompt for an unused value.

## Resolved findings

### Unnamed ports use neutral generated names

The bridge previously named the first unnamed TCP port `http`, which mislabeled
the generated PostgreSQL container and Service port. Unnamed ports now use the
neutral `port-<number>-<protocol>` form, so PostgreSQL port `5432` becomes
`port-5432-tcp`. Explicit Compose long-syntax port names are still preserved.

## Confirmed packaging gaps

### Health checks become liveness probes only

Compose health checks are rendered as Kubernetes liveness probes. No readiness
probe is generated, so Kubernetes may route traffic as soon as the container
starts.

The correct translation still needs to be decided. Compose health semantics are
generally closer to readiness, while startup and liveness behavior require care
to avoid restart loops during slow initialization.

Priority: high.

### Package and application image versions are static

The generated package, chart, and application image references currently use
the literal `x-uds.package.version`, which defaults to `0.1.0`. Rebuilding the
same version produces mutable application image tags.

The Mission Application Template instead supplies the package build version to
the package, chart, and image reference. The bridge needs a similarly simple
way to produce immutable references.

Priority: high; previously deferred.

### Empty generated secrets do not fail fast

Generated chart values default secret values to an empty string. A deployment
using `--confirm` can therefore create an empty Kubernetes Secret and fail only
after the workload starts.

Generated Secret templates should reject empty required values during Helm
rendering, or an equivalent validation mechanism should prevent deployment.

Priority: high.

### External secrets generate unused variable prompts

Legacy Zarf variables should be generated only for secrets the package creates.
Secrets marked `external: true` should not produce a prompt or a values entry.

Priority: medium; narrow bridge bug.

### Dependency init containers check only TCP

Generated `depends_on` init containers use `nc -z` to wait for a dependency
port. This verifies that a socket is open, not that the dependency is initialized
or usable. It can also conflict with dependencies whose host is made
deploy-time-configurable.

For services that can start without immediately using their dependency,
removing the Compose startup dependency may be simpler than generating an init
container.

Priority: medium.

### Package metadata and documentation are minimal

The generated package includes a basic name, description, and version but no UDS
title/icon annotations or packaged operator documentation. This does not affect
runtime correctness, but it makes package inspection less useful.

Priority: low.

## Pending: database packaging architecture

The bundled PostgreSQL database is intended for local development and local
in-cluster testing. A persistent environment is expected to use a database
provisioned externally, commonly by an operator that creates an RDS instance
and a Kubernetes credentials Secret.

The local workflow must still be able to deploy PostgreSQL inside the test
cluster. The architecture has not yet been selected.

Options under consideration:

1. Generate two package variants with the current bridge:
   application-only and application-with-development-database.
2. Deploy PostgreSQL separately through a local UDS test bundle.
3. Translate Compose profiles into optional Zarf components so one package can
   optionally install its development database.

The third option best preserves the single-package workflow, but requires the
bridge to partition services and their images, configs, secrets, and volumes
across multiple charts/components.

Reference: <https://docs.zarf.dev/ref/packages/>

## Pending: general Package Values support

General Package Values are useful independently of the database decision. A
small first use case is externalizing the deployment domain instead of baking
`uds.dev` into generated SSO redirect URIs.

A possible generated values structure is:

```yaml
uds:
  domain: uds.dev
```

This would allow deployment overrides such as:

```sh
uds zarf package deploy package.tar.zst \
  --set-values uds.domain=example.mil
```

Open design questions include:

- How defaults are declared in `x-uds`.
- Which generated settings should become Package Values.
- How literal SSO redirect URIs differ from domain-relative redirect paths.
- Whether the bridge should generate and package a JSON Schema.
- Which minimum Zarf version the bridge should target.

## Deliberate non-goals for this example

The following differences from the Mission Application Template are not
currently considered bridge gaps for this example:

- Horizontal pod autoscaling.
- Configurable replicas, affinity, tolerations, or node selection.
- Broad resource and scheduling customization.
- A general-purpose Helm override surface for every field.
- Repeating security settings that UDS Core supplies through mutation.
- Configurable SSO groups; this example intentionally permits any authenticated
  UDS user.
- Production database HA, backups, and lifecycle management for the bundled
  development database.
- CI and Playwright scaffolding, which are outside the generated package
  workspace.

## Suggested order when investigation resumes

1. Decide how Compose health checks map to readiness, startup, and liveness.
2. Add immutable package and application image versioning.
3. Make generated secret configuration fail fast.
4. Stop generating variables for external secrets.
5. Decide the Package Values input and rendering model.
6. Decide the database component and credential-injection architecture.
