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
		filepath.Join(outDir, "chart", "Chart.yaml"),
		filepath.Join(outDir, "chart", "values.yaml"),
		filepath.Join(outDir, "chart", "templates", "namespace.yaml"),
		filepath.Join(outDir, "chart", "templates", "deployment-db.yaml"),
		filepath.Join(outDir, "chart", "templates", "deployment-wordpress.yaml"),
		filepath.Join(outDir, "chart", "templates", "service-db.yaml"),
		filepath.Join(outDir, "chart", "templates", "service-wordpress.yaml"),
		filepath.Join(outDir, "chart", "templates", "pvc-db-data.yaml"),
		filepath.Join(outDir, "chart", "templates", "pvc-wordpress-data.yaml"),
		filepath.Join(outDir, "chart", "templates", "secret-db-password.yaml"),
		filepath.Join(outDir, "chart", "templates", "uds-package.yaml"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("expected generated file %s: %v", file, err)
		}
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"kind: ZarfPackageConfig",
		"name: wordpress",
		"name: DB_PASSWORD",
		"prompt: true",
		"sensitive: true",
		"mysql:8.0",
		"wordpress:latest",
		"busybox:1.36",
		"localPath: chart",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q", want)
		}
	}

	secretManifest := readFile(t, filepath.Join(outDir, "chart", "templates", "secret-db-password.yaml"))
	if !strings.Contains(secretManifest, "###ZARF_VAR_DB_PASSWORD###") {
		t.Fatalf("expected secret manifest to contain templated zarf variable")
	}

	wordpressDeployment := readFile(t, filepath.Join(outDir, "chart", "templates", "deployment-wordpress.yaml"))
	for _, want := range []string{
		"wait-db",
		"/run/secrets/db_password",
		"claimName: wordpress-data",
	} {
		if !strings.Contains(wordpressDeployment, want) {
			t.Fatalf("expected wordpress deployment to contain %q", want)
		}
	}

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	for _, want := range []string{
		"kind: Package",
		"mode: ambient",
		"remoteGenerated: IntraNamespace",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected uds-package.yaml to contain %q", want)
		}
	}
	if strings.Contains(udsPackage, "service: wordpress") {
		t.Fatalf("did not expect implicit network expose entries without x-uds.expose")
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

func TestLoadCanonicalRejectsNonPullableBuildImage(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    build:
      context: /workspace
      dockerfile: Dockerfile
    image: keel.local/api:latest
`)

	_, err := compose.LoadCanonicalYAML(input)
	if err == nil {
		t.Fatalf("expected non-pullable build image to fail")
	}
	if !strings.Contains(err.Error(), "non-pullable local image") {
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

	configMap := readFile(t, filepath.Join(outDir, "chart", "templates", "configmap-app-config.yaml"))
	if !strings.Contains(configMap, "key: value") {
		t.Fatalf("expected configmap to contain rendered config content")
	}

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	for _, want := range []string{
		"namespace: '{{ .Release.Namespace }}'",
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
	if !strings.Contains(zarfConfig, "namespace: demo-ns") {
		t.Fatalf("expected zarf chart namespace override in zarf.yaml")
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
	path := filepath.Join("..", "..", "testdata", "canonical", name)
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
