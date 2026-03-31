package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"defenseunicorns/uds-compose-bridge/internal/compose"
	"defenseunicorns/uds-compose-bridge/internal/model"
	"defenseunicorns/uds-compose-bridge/internal/render"
)

func TestWritePackageFromBasicFixture(t *testing.T) {
	t.Parallel()

	app := mustLoadFixture(t, filepath.Join("basic", "compose.yaml"))
	outDir := t.TempDir()

	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	for _, file := range []string{
		filepath.Join(outDir, "zarf.yaml"),
		filepath.Join(outDir, "manifests", "namespace.yaml"),
		filepath.Join(outDir, "manifests", "deployment-db.yaml"),
		filepath.Join(outDir, "manifests", "deployment-wordpress.yaml"),
		filepath.Join(outDir, "manifests", "service-db.yaml"),
		filepath.Join(outDir, "manifests", "service-wordpress.yaml"),
		filepath.Join(outDir, "manifests", "pvc-db-data.yaml"),
		filepath.Join(outDir, "manifests", "pvc-wordpress-data.yaml"),
		filepath.Join(outDir, "manifests", "secret-db-password.yaml"),
		filepath.Join(outDir, "manifests", "uds-package.yaml"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("expected generated file %s: %v", file, err)
		}
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"kind: ZarfPackageConfig",
		"name: wordpress",
		"name: db",
		"name: DB_PASSWORD",
		"prompt: true",
		"sensitive: true",
		"mysql:8.0",
		"wordpress:latest",
		"busybox:1.36",
		"manifests/namespace.yaml",
		"manifests/deployment-db.yaml",
		"manifests/deployment-wordpress.yaml",
		"manifests/uds-package.yaml",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q", want)
		}
	}
	if strings.Contains(zarfConfig, "localPath: chart") {
		t.Fatalf("did not expect chart-based zarf config")
	}
	if strings.Count(zarfConfig, "manifests/secret-db-password.yaml") != 1 {
		t.Fatalf("expected shared secret manifest to be owned by only one component")
	}
	if strings.Count(zarfConfig, "manifests/pvc-db-data.yaml") != 1 {
		t.Fatalf("expected shared pvc manifest references to be deduped")
	}
	if dbIdx, wordpressIdx := strings.Index(zarfConfig, "\n    - name: db\n"), strings.Index(zarfConfig, "\n    - name: wordpress\n"); dbIdx == -1 || wordpressIdx == -1 || dbIdx > wordpressIdx {
		t.Fatalf("expected db component to be ordered before wordpress component")
	}

	secretManifest := readFile(t, filepath.Join(outDir, "manifests", "secret-db-password.yaml"))
	if !strings.Contains(secretManifest, "###ZARF_VAR_DB_PASSWORD###") {
		t.Fatalf("expected secret manifest to contain templated zarf variable")
	}

	wordpressDeployment := readFile(t, filepath.Join(outDir, "manifests", "deployment-wordpress.yaml"))
	for _, want := range []string{
		"wait-db",
		"/run/secrets/db_password",
		"claimName: wordpress-data",
	} {
		if !strings.Contains(wordpressDeployment, want) {
			t.Fatalf("expected wordpress deployment to contain %q", want)
		}
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	for _, want := range []string{
		"kind: Package",
		"namespace: wordpress",
		"mode: ambient",
		"remoteGenerated: IntraNamespace",
		"service: wordpress",
		"host: wordpress",
		"- /",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q", want)
		}
	}
	if strings.Contains(udsPackage, "service: db") {
		t.Fatalf("did not expect internal-only db service to be auto-exposed")
	}
}

func TestWritePackageFromFullWorkingFixture(t *testing.T) {
	t.Parallel()

	app := mustLoadFixture(t, filepath.Join("full-working", "compose.yaml"))
	outDir := t.TempDir()

	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	for _, file := range []string{
		filepath.Join(outDir, "zarf.yaml"),
		filepath.Join(outDir, "manifests", "deployment-hello-world.yaml"),
		filepath.Join(outDir, "manifests", "deployment-redis.yaml"),
		filepath.Join(outDir, "manifests", "service-hello-world.yaml"),
		filepath.Join(outDir, "manifests", "service-redis.yaml"),
		filepath.Join(outDir, "manifests", "pvc-hello-data.yaml"),
		filepath.Join(outDir, "manifests", "pvc-redis-data.yaml"),
		filepath.Join(outDir, "manifests", "configmap-app-config.yaml"),
		filepath.Join(outDir, "manifests", "secret-app-secret.yaml"),
		filepath.Join(outDir, "manifests", "uds-package.yaml"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("expected generated file %s: %v", file, err)
		}
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"keel.local/hello:latest",
		"redis:7-alpine",
		"busybox:1.36",
		"manifests/configmap-app-config.yaml",
		"manifests/secret-app-secret.yaml",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q", want)
		}
	}
	if strings.Contains(zarfConfig, "name: debugger") {
		t.Fatalf("did not expect profiled debugger service to be rendered")
	}

	configMap := readFile(t, filepath.Join(outDir, "manifests", "configmap-app-config.yaml"))
	if !strings.Contains(configMap, "Hello from config map") {
		t.Fatalf("expected configmap to contain inline config content")
	}

	udsPackage := readFile(t, filepath.Join(outDir, "manifests", "uds-package.yaml"))
	if !strings.Contains(udsPackage, "service: hello-world") {
		t.Fatalf("expected hello-world service to be exposed")
	}
	if strings.Contains(udsPackage, "service: redis") {
		t.Fatalf("did not expect internal-only redis service to be exposed")
	}
}

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

	_, err := compose.LoadCanonicalYAML(mustReadFixture(t, filepath.Join("full", "compose.yaml")))
	if err == nil {
		t.Fatalf("expected bind mount fixture to fail")
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
    x-uds:
      expose:
        host: app
        path: /healthz
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
		"- /healthz",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q", want)
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

func mustLoadFixture(t *testing.T, name string) model.App {
	t.Helper()
	app, err := compose.LoadCanonicalYAML(mustReadFixture(t, name))
	if err != nil {
		t.Fatalf("LoadCanonicalYAML(%s) error = %v", name, err)
	}
	return app
}

func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", name)
	cmd := exec.Command("docker", "compose", "-f", path, "config")
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render canonical fixture %s: %v\n%s", name, err, string(data))
	}
	return data
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return string(data)
}
