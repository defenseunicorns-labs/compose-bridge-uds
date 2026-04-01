package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"defenseunicorns/uds-compose-bridge/internal/compose"
	"defenseunicorns/uds-compose-bridge/internal/render"
	yamlv3 "gopkg.in/yaml.v3"
)

func TestLoadCanonicalAcceptsExplicitLocalBuildImage(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    build:
      context: /workspace
      dockerfile: Dockerfile
    image: keel.local/api:latest
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	if len(app.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(app.Services))
	}
	if app.Services[0].Image != "keel.local/api:latest" {
		t.Fatalf("expected local image reference to be preserved, got %q", app.Services[0].Image)
	}
	if !app.Services[0].UsesBuild {
		t.Fatalf("expected service to retain build metadata")
	}
}

func TestLoadCanonicalRejectsBindMounts(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    volumes:
      - ./data:/app/data
`)

	_, err := compose.LoadCanonicalYAML(input)
	if err == nil {
		t.Fatalf("expected bind mount to fail")
	}
	if !strings.Contains(err.Error(), "bind mount") {
		t.Fatalf("expected bind mount error, got %v", err)
	}
}

func TestLoadCanonicalFileRejectsBindMountsAfterEnvFileNormalization(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "app-config.yml")
	if err := os.WriteFile(configPath, []byte("message: hello\n"), 0o644); err != nil {
		t.Fatalf("write bind source file: %v", err)
	}
	composePath := filepath.Join(dir, "compose.yaml")
	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    env_file: .env
    volumes:
      - ./app-config.yml:/app/config.yml:ro
`)
	if err := os.WriteFile(composePath, input, 0o644); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	_, err := compose.LoadCanonicalFile(composePath)
	if err == nil {
		t.Fatalf("expected bind mount fixture to fail")
	}
	if !strings.Contains(err.Error(), "bind mount") {
		t.Fatalf("expected bind mount error after env_file normalization, got %v", err)
	}
}

func TestLoadCanonicalRejectsBuildWithoutExplicitImage(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    build:
      context: /workspace
      dockerfile: Dockerfile
`)

	_, err := compose.LoadCanonicalYAML(input)
	if err == nil {
		t.Fatalf("expected build without image to fail")
	}
	if !strings.Contains(err.Error(), "does not declare an explicit image") {
		t.Fatalf("expected build image validation error, got %v", err)
	}
}

func TestWritePackageWithConfigAndExpose(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
x-uds:
  package:
    namespace: demo-ns
  network:
    expose:
      - service: api
        host: app
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    ports:
      - mode: ingress
        target: 8080
        published: "8080"
        protocol: tcp
    configs:
      - source: app_config
        target: /app/config.yml
configs:
  app_config:
    name: demo_app_config
    file: /workspace/app-config.yml
    content: |
      key: value
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	configMap := readFile(t, filepath.Join(outDir, "manifests", "configmap-app-config.yaml"))
	if !strings.Contains(configMap, "key: value") {
		t.Fatalf("expected configmap to contain rendered config content")
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	for _, want := range []string{
		"namespace: demo-ns",
		"service: api",
		"host: app",
		"port: 8080",
		"gateway: tenant",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q\n%s", want, udsPackage)
		}
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"name: api",
		"namespace: demo-ns",
		"manifests/uds-package.yaml",
		"manifests/configmap-app-config.yaml",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q", want)
		}
	}
	if strings.Contains(zarfConfig, "localPath: chart") {
		t.Fatalf("did not expect chart-based zarf config")
	}
}

func TestWritePackageAutoExposesPublishedPortsOnly(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  web:
    image: ghcr.io/acme/web:1.0.0
    ports:
      - mode: ingress
        target: 8080
        published: "8080"
        protocol: tcp
  db:
    image: postgres:16
    expose:
      - "5432"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	if !strings.Contains(udsPackage, "service: web") {
		t.Fatalf("expected published web service to be auto-exposed")
	}
	if strings.Contains(udsPackage, "service: db") {
		t.Fatalf("did not expect internal-only db service to be auto-exposed")
	}
}

func TestExposeEnrichmentFillsDefaults(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
x-uds:
  network:
    expose:
      - service: web
services:
  web:
    image: nginx:latest
    ports:
      - mode: ingress
        target: 8080
        published: "8080"
        protocol: tcp
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	for _, want := range []string{
		"service: web",
		"host: web",
		"gateway: tenant",
		"port: 8080",
		"app.kubernetes.io/name: web",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q\n%s", want, udsPackage)
		}
	}
}

func TestSSOAutoGeneration(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
services:
  web:
    image: nginx:latest
    ports:
      - mode: ingress
        target: 8080
        published: "8080"
        protocol: tcp
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	for _, want := range []string{
		"clientId: myapp",
		"name: Myapp",
		"https://web.uds.dev/*",
		"enableAuthserviceSelector",
		"app.kubernetes.io/name: web",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q\n%s", want, udsPackage)
		}
	}
}

func TestExplicitEmptySSODisablesAutoGeneration(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
x-uds:
  network:
    expose:
      - service: web
        host: app
  sso: []
services:
  web:
    image: nginx:latest
    ports:
      - mode: ingress
        target: 8080
        published: "8080"
        protocol: tcp
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	if strings.Contains(udsPackage, "clientId:") {
		t.Fatalf("did not expect SSO when x-uds.sso is explicitly empty\n%s", udsPackage)
	}
	if strings.Contains(udsPackage, "enableAuthserviceSelector") {
		t.Fatalf("did not expect authservice selector when x-uds.sso is explicitly empty\n%s", udsPackage)
	}
	for _, want := range []string{
		"service: web",
		"host: app",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q\n%s", want, udsPackage)
		}
	}
}

func TestSSOEnrichment(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
x-uds:
  sso:
    - clientId: custom-id
services:
  web:
    image: nginx:latest
    ports:
      - mode: ingress
        target: 8080
        published: "8080"
        protocol: tcp
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	if !strings.Contains(udsPackage, "clientId: custom-id") {
		t.Fatalf("expected user-provided clientId to be preserved\n%s", udsPackage)
	}
	if !strings.Contains(udsPackage, "name: Myapp") {
		t.Fatalf("expected inferred name\n%s", udsPackage)
	}
	if !strings.Contains(udsPackage, "https://web.uds.dev/*") {
		t.Fatalf("expected inferred redirectUris\n%s", udsPackage)
	}
}

func TestNoSSOWhenNoExposedServices(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
services:
  db:
    image: postgres:16
    expose:
      - "5432"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	if strings.Contains(udsPackage, "clientId") {
		t.Fatalf("did not expect SSO when no services have published ports\n%s", udsPackage)
	}
}

func TestMonitorEnrichment(t *testing.T) {
	t.Parallel()

	input := []byte(`name: metrics-demo
x-uds:
  monitor:
    - service: api
      description: API metrics
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "9090"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	for _, want := range []string{
		"monitor:",
		"description: API metrics",
		"selector:",
		"podSelector:",
		"app.kubernetes.io/name: api",
		"portName: http",
		"targetPort: 9090",
		"path: /metrics",
		"kind: ServiceMonitor",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q\n%s", want, udsPackage)
		}
	}
	if strings.Contains(udsPackage, "service: api") {
		t.Fatalf("did not expect bridge-only service field in rendered monitor\n%s", udsPackage)
	}
}

func TestMonitorAuthorizationDefaultsAndExplicitFields(t *testing.T) {
	t.Parallel()

	input := []byte(`name: metrics-demo
x-uds:
  monitor:
    - service: api
      kind: PodMonitor
      path: /custom/metrics
      authorization:
        credentials:
          name: metrics-auth-secret
          key: token
          optional: false
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "9090"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	for _, want := range []string{
		"kind: PodMonitor",
		"path: /custom/metrics",
		"portName: http",
		"targetPort: 9090",
		"authorization:",
		"type: Bearer",
		"name: metrics-auth-secret",
		"key: token",
		"optional: false",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q\n%s", want, udsPackage)
		}
	}
}

func TestMonitorResolvesPortNameFromTargetPort(t *testing.T) {
	t.Parallel()

	input := []byte(`name: metrics-demo
x-uds:
  monitor:
    - service: api
      targetPort: 9090
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "8080"
      - "9090"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	for _, want := range []string{
		"targetPort: 9090",
		"portName: port-9090-tcp",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q\n%s", want, udsPackage)
		}
	}
}

func TestMonitorPassthroughWithoutService(t *testing.T) {
	t.Parallel()

	input := []byte(`name: metrics-demo
x-uds:
  monitor:
    - selector:
        app: api
      podSelector:
        app: api
      portName: metrics
      targetPort: 9090
      path: /stats/metrics
      kind: PodMonitor
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "9090"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	for _, want := range []string{
		"selector:",
		"podSelector:",
		"app: api",
		"portName: metrics",
		"targetPort: 9090",
		"path: /stats/metrics",
		"kind: PodMonitor",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q\n%s", want, udsPackage)
		}
	}
}

func TestMonitorRejectsAmbiguousServicePorts(t *testing.T) {
	t.Parallel()

	input := []byte(`name: metrics-demo
x-uds:
  monitor:
    - service: api
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "8080"
      - "9090"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	err = render.WritePackage(outDir, app)
	if err == nil {
		t.Fatalf("expected WritePackage() to fail for ambiguous monitor ports")
	}
	if !strings.Contains(err.Error(), "multiple declared TCP ports") {
		t.Fatalf("expected ambiguous port error, got %v", err)
	}
}

func TestMonitorRejectsUnknownService(t *testing.T) {
	t.Parallel()

	input := []byte(`name: metrics-demo
x-uds:
  monitor:
    - service: missing
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "9090"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	err = render.WritePackage(outDir, app)
	if err == nil {
		t.Fatalf("expected WritePackage() to fail for unknown monitor service")
	}
	if !strings.Contains(err.Error(), `x-uds.monitor service "missing"`) {
		t.Fatalf("expected unknown service error, got %v", err)
	}
}

func TestMonitorRejectsMismatchedPortNameAndTargetPort(t *testing.T) {
	t.Parallel()

	input := []byte(`name: metrics-demo
x-uds:
  monitor:
    - service: api
      portName: http
      targetPort: 9090
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "8080"
      - "9090"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	err = render.WritePackage(outDir, app)
	if err == nil {
		t.Fatalf("expected WritePackage() to fail for mismatched monitor port fields")
	}
	if !strings.Contains(err.Error(), `portName "http" resolves to 8080, but targetPort is 9090`) {
		t.Fatalf("expected mismatched port error, got %v", err)
	}
}

func TestWritePackageRendersCABundleAndUDSConfig(t *testing.T) {
	t.Parallel()

	input := []byte(`name: trust-demo
x-uds:
  caBundle:
    configMap:
      name: custom-trust-bundle
      key: roots.pem
      labels:
        uds.dev/pod-reload: "true"
      annotations:
        uds.dev/pod-reload-selector: app=api
    CA_BUNDLE_CERTS: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0t
    CA_BUNDLE_INCLUDE_DOD_CERTS: true
    CA_BUNDLE_INCLUDE_PUBLIC_CERTS: "false"
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "8080"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readYAMLMap(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	spec := mustMap(t, udsPackage["spec"])
	caBundle := mustMap(t, spec["caBundle"])
	configMap := mustMap(t, caBundle["configMap"])
	if got := configMap["name"]; got != "custom-trust-bundle" {
		t.Fatalf("expected caBundle configMap name, got %#v", got)
	}
	if got := configMap["key"]; got != "roots.pem" {
		t.Fatalf("expected caBundle configMap key, got %#v", got)
	}
	labels := mustMap(t, configMap["labels"])
	if got := labels["uds.dev/pod-reload"]; got != "true" {
		t.Fatalf("expected caBundle label, got %#v", got)
	}
	annotations := mustMap(t, configMap["annotations"])
	if got := annotations["uds.dev/pod-reload-selector"]; got != "app=api" {
		t.Fatalf("expected caBundle annotation, got %#v", got)
	}

	udsConfig := readYAMLMap(t, filepath.Join(outDir, "uds-config.yaml"))
	variables := mustMap(t, udsConfig["variables"])
	core := mustMap(t, variables["core"])
	if got := core["CA_BUNDLE_CERTS"]; got != "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0t" {
		t.Fatalf("expected CA_BUNDLE_CERTS, got %#v", got)
	}
	if got := core["CA_BUNDLE_INCLUDE_DOD_CERTS"]; got != "true" {
		t.Fatalf("expected CA_BUNDLE_INCLUDE_DOD_CERTS to be normalized to string, got %#v", got)
	}
	if got := core["CA_BUNDLE_INCLUDE_PUBLIC_CERTS"]; got != "false" {
		t.Fatalf("expected CA_BUNDLE_INCLUDE_PUBLIC_CERTS to be normalized to string, got %#v", got)
	}
}

func TestWritePackageRendersUDSConfigWithoutPackageCABundle(t *testing.T) {
	t.Parallel()

	input := []byte(`name: trust-demo
x-uds:
  caBundle:
    CA_BUNDLE_INCLUDE_PUBLIC_CERTS: true
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "8080"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	if strings.Contains(udsPackage, "caBundle:") {
		t.Fatalf("did not expect package caBundle without configMap override\n%s", udsPackage)
	}

	udsConfig := readYAMLMap(t, filepath.Join(outDir, "uds-config.yaml"))
	variables := mustMap(t, udsConfig["variables"])
	core := mustMap(t, variables["core"])
	if got := core["CA_BUNDLE_INCLUDE_PUBLIC_CERTS"]; got != "true" {
		t.Fatalf("expected CA_BUNDLE_INCLUDE_PUBLIC_CERTS to be rendered, got %#v", got)
	}
}

func TestWritePackageOmitsUDSConfigWithoutCABundleVariables(t *testing.T) {
	t.Parallel()

	input := []byte(`name: trust-demo
x-uds:
  caBundle:
    configMap:
      name: custom-trust-bundle
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "8080"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "uds-config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected uds-config.yaml to be omitted, stat err = %v", err)
	}
}

func TestLoadCanonicalRejectsInvalidCABundleConfigMapType(t *testing.T) {
	t.Parallel()

	input := []byte(`name: trust-demo
x-uds:
  caBundle:
    configMap: nope
services:
  api:
    image: ghcr.io/acme/api:1.0.0
`)

	_, err := compose.LoadCanonicalYAML(input)
	if err == nil {
		t.Fatalf("expected invalid caBundle configMap type to fail")
	}
	if !strings.Contains(err.Error(), "invalid x-uds.caBundle.configMap") {
		t.Fatalf("expected caBundle configMap validation error, got %v", err)
	}
}

func TestLoadCanonicalRejectsInvalidCABundleBoolType(t *testing.T) {
	t.Parallel()

	input := []byte(`name: trust-demo
x-uds:
  caBundle:
    CA_BUNDLE_INCLUDE_DOD_CERTS:
      nope: true
services:
  api:
    image: ghcr.io/acme/api:1.0.0
`)

	_, err := compose.LoadCanonicalYAML(input)
	if err == nil {
		t.Fatalf("expected invalid caBundle bool type to fail")
	}
	if !strings.Contains(err.Error(), "invalid x-uds.caBundle.CA_BUNDLE_INCLUDE_DOD_CERTS") {
		t.Fatalf("expected caBundle boolean validation error, got %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return string(data)
}

func readYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	content := readFile(t, path)
	var out map[string]any
	if err := yamlv3.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("unmarshal yaml %s: %v", path, err)
	}
	return out
}

func mustMap(t *testing.T, value any) map[string]any {
	t.Helper()
	m, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", value)
	}
	return m
}
