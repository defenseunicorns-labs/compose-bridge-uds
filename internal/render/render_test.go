package render_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"defenseunicorns/uds-compose-bridge/internal/compose"
	"defenseunicorns/uds-compose-bridge/internal/model"
	"defenseunicorns/uds-compose-bridge/internal/render"

	yamlv3 "gopkg.in/yaml.v3"
)

func TestLoadCanonicalInternalizesBuildImage(t *testing.T) {
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
	if app.Services[0].Image != "zarf.internal/demo-api:0.1.0" {
		t.Fatalf("expected build image reference to be internalized, got %q", app.Services[0].Image)
	}
	if app.Services[0].Build == nil {
		t.Fatalf("expected service to retain build metadata")
	}
	if app.Services[0].Build.Config["context"] != "/workspace" {
		t.Fatalf("expected canonical build context, got %#v", app.Services[0].Build.Config)
	}
	if got := app.Services[0].Build.ReadPaths; len(got) != 1 || got[0] != "/workspace" {
		t.Fatalf("expected exact local build read path, got %#v", got)
	}
}

func TestLoadCanonicalIgnoresContainerName(t *testing.T) {
	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    container_name: custom-api
`)

	originalStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = writer
	app, err := compose.LoadCanonicalYAML(input)
	os.Stderr = originalStderr
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatalf("close stderr writer: %v", closeErr)
	}
	warning, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatalf("read stderr: %v", readErr)
	}
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatalf("close stderr reader: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("expected container_name to be ignored, got %v", err)
	}
	if got := app.Services[0].Name; got != "api" {
		t.Fatalf("expected Compose service name to be preserved, got %q", got)
	}
	if want := `service "api" container_name "custom-api" ignored`; !strings.Contains(string(warning), want) {
		t.Fatalf("expected warning %q, got %q", want, warning)
	}
}

func TestLoadCanonicalSkipsBindMounts(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    volumes:
      - ./data:/app/data
      - app-data:/app/state
volumes:
  app-data:
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("expected bind mount to be skipped, got error: %v", err)
	}
	if len(app.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(app.Services))
	}
	if len(app.Services[0].Volumes) != 1 || app.Services[0].Volumes[0].Target != "/app/state" {
		t.Fatalf("expected named volume to remain and bind mount to be skipped, got %#v", app.Services[0].Volumes)
	}
}

func TestLoadCanonicalFileSkipsBindMountsAfterEnvFileNormalization(t *testing.T) {
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
	if err != nil {
		t.Fatalf("expected bind mount to be skipped after env_file normalization, got error: %v", err)
	}
}

func TestLoadCanonicalAcceptsBuildWithoutExplicitImage(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    build:
      context: /workspace
      dockerfile: Dockerfile
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("expected build without image to be supported, got %v", err)
	}
	if got := app.Services[0].Image; got != "zarf.internal/demo-api:0.1.0" {
		t.Fatalf("expected generated internal image, got %q", got)
	}
}

func TestWritePackageGeneratesBuildWorkspaceAndImageArchives(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:dev
    build:
      context: /workspace/api
      dockerfile: Containerfile
      args:
        MODE: release
      tags:
        - ghcr.io/acme/extra:dev
      additional_contexts:
        base: service:base_image
      secrets:
        - build_token
  base_image:
    build:
      context: /workspace/base
      platforms:
        - linux/arm64
  cache:
    image: redis:7.4-alpine
secrets:
  build_token:
    file: /workspace/build-token.txt
x-uds:
  package:
    version: 1.2.3
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	buildCompose := readYAMLMap(t, filepath.Join(outDir, "build.compose.yaml"))
	services := mustMap(t, buildCompose["services"])
	api := mustMap(t, services["api"])
	if got := api["image"]; got != "zarf.internal/demo-api:1.2.3" {
		t.Fatalf("expected internal API image, got %#v", got)
	}
	apiBuild := mustMap(t, api["build"])
	if got := apiBuild["context"]; got != "/workspace/api" {
		t.Fatalf("expected canonical context, got %#v", got)
	}
	if _, exists := apiBuild["tags"]; exists {
		t.Fatalf("did not expect user-controlled build tags in generated build file: %#v", apiBuild)
	}
	contexts := mustMap(t, apiBuild["additional_contexts"])
	if got := contexts["base"]; got != "service:base-image" {
		t.Fatalf("expected normalized service build context, got %#v", got)
	}
	if _, exists := mustMap(t, buildCompose["secrets"])["build_token"]; !exists {
		t.Fatalf("expected referenced build secret in generated build file")
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"imageArchives:",
		"path: image-archives/api.tar",
		"zarf.internal/demo-api:1.2.3",
		"path: image-archives/base-image.tar",
		"zarf.internal/demo-base-image:1.2.3",
		"redis:7.4-alpine",
		"docker buildx bake",
		"fs.read=/workspace",
		"fs.read=/workspace/api",
		"fs.read=/workspace/base",
		"api.platform+=linux/amd64",
		"api.platform+=linux/arm64",
		"api.output=type=oci,dest=image-archives/api.tar",
		"base-image.output=type=oci,dest=image-archives/base-image.tar",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q\n%s", want, zarfConfig)
		}
	}
	if strings.Contains(zarfConfig, "base-image.platform+=") {
		t.Fatalf("did not expect default platforms to override declared build platforms\n%s", zarfConfig)
	}
	for _, want := range []string{
		`docker_context="$(docker context show)"`,
		`builder_name='compose-bridge-uds-'"$builder_context"`,
		`docker buildx create --name "$builder_name" --driver docker-container "$docker_context"`,
		`--builder "$builder_name"`,
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected context-scoped Buildx builder command %q\n%s", want, zarfConfig)
		}
	}

	deployment := readFile(t, filepath.Join(outDir, "chart", "templates", "deployment-api.yaml"))
	if !strings.Contains(deployment, "imagePullPolicy: Always") {
		t.Fatalf("expected built image to always pull from the Zarf registry\n%s", deployment)
	}
}

func TestWritePackageWithoutBuildOmitsBuildWorkspace(t *testing.T) {
	t.Parallel()

	app, err := compose.LoadCanonicalYAML([]byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
`))
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "build.compose.yaml")); !os.IsNotExist(err) {
		t.Fatalf("did not expect build.compose.yaml without build services")
	}
	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, unexpected := range []string{"imageArchives:", "actions:", "docker buildx"} {
		if strings.Contains(zarfConfig, unexpected) {
			t.Fatalf("did not expect %q without build services\n%s", unexpected, zarfConfig)
		}
	}
}

func TestWritePackageSeparatesBuildServicesFromImageServices(t *testing.T) {
	t.Parallel()

	app, err := compose.LoadCanonicalYAML([]byte(`name: demo
services:
  server:
    build:
      context: /workspace/server
  cache:
    image: redis:7-alpine
`))
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}

	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	zarfConfig := readYAMLMap(t, filepath.Join(outDir, "zarf.yaml"))
	components, ok := zarfConfig["components"].([]any)
	if !ok || len(components) != 1 {
		t.Fatalf("expected one Zarf component, got %#v", zarfConfig["components"])
	}
	component := mustMap(t, components[0])

	images, ok := component["images"].([]any)
	if !ok || len(images) != 1 || images[0] != "redis:7-alpine" {
		t.Fatalf("expected only the prebuilt image under images, got %#v", component["images"])
	}

	archives, ok := component["imageArchives"].([]any)
	if !ok || len(archives) != 1 {
		t.Fatalf("expected one image archive, got %#v", component["imageArchives"])
	}
	archive := mustMap(t, archives[0])
	if archive["path"] != "image-archives/server.tar" {
		t.Fatalf("expected server image archive path, got %#v", archive["path"])
	}
	archiveImages, ok := archive["images"].([]any)
	if !ok || len(archiveImages) != 1 || archiveImages[0] != "zarf.internal/demo-server:0.1.0" {
		t.Fatalf("expected only the built image under imageArchives, got %#v", archive["images"])
	}

	buildCompose := readYAMLMap(t, filepath.Join(outDir, "build.compose.yaml"))
	buildServices := mustMap(t, buildCompose["services"])
	if len(buildServices) != 1 {
		t.Fatalf("expected only one service in the build workspace, got %#v", buildServices)
	}
	if _, ok := buildServices["server"]; !ok {
		t.Fatalf("expected server in the build workspace, got %#v", buildServices)
	}
	if _, ok := buildServices["cache"]; ok {
		t.Fatalf("did not expect the prebuilt cache service in the build workspace")
	}
}

func TestLoadCanonicalAggregatesCompatibilityIssues(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    network_mode: host
    platform: wasi/wasm
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
`)

	_, err := compose.LoadCanonicalYAML(input)
	if err == nil {
		t.Fatal("expected compatibility validation to fail")
	}
	for _, code := range []string{"[service-field] services.api.network_mode", "[service-field] services.api.platform"} {
		if !strings.Contains(err.Error(), code) {
			t.Fatalf("expected %q in aggregated error, got %v", code, err)
		}
	}
	if strings.Contains(err.Error(), "[bind-mount]") {
		t.Fatalf("expected bind mount not to be a compatibility error, got %v", err)
	}
}

func TestLoadCanonicalPreservesNetworkMemberships(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  frontend:
    image: ghcr.io/acme/frontend:1.0.0
    networks: [front_end]
  api:
    image: ghcr.io/acme/api:1.0.0
    networks: [front_end, back]
  db:
    image: postgres:16
    networks: [back]
networks:
  front_end:
  back:
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("expected different network memberships to be supported, got %v", err)
	}
	memberships := map[string]string{}
	for _, svc := range app.Services {
		memberships[svc.Name] = strings.Join(svc.Networks, ",")
	}
	for service, want := range map[string]string{
		"frontend": "front-end",
		"api":      "back,front-end",
		"db":       "back",
	} {
		if got := memberships[service]; got != want {
			t.Fatalf("expected %s networks %q, got %q", service, want, got)
		}
	}
}

func TestLoadCanonicalAcceptsBridgeNetworkDriver(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    networks: [app]
networks:
  app:
    driver: bridge
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("expected ordinary bridge network to be supported, got %v", err)
	}
	if got := strings.Join(app.Services[0].Networks, ","); got != "app" {
		t.Fatalf("expected bridge network membership to be preserved, got %q", got)
	}
}

func TestLoadCanonicalRejectsUnsupportedTopLevelNetworkOptions(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"custom driver": `driver: overlay`,
		"bridge options": `driver: bridge
    driver_opts:
      com.example.option: enabled`,
	}
	for name, network := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := []byte("name: demo\nservices:\n  api:\n    image: ghcr.io/acme/api:1.0.0\n    networks: [app]\nnetworks:\n  app:\n    " + network + "\n")
			_, err := compose.LoadCanonicalYAML(input)
			if err == nil || !strings.Contains(err.Error(), "[network-options] networks.app") {
				t.Fatalf("expected unsupported network options error, got %v", err)
			}
		})
	}
}

func TestLoadCanonicalExcludesOptionalDependencyAndPrunesResources(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    depends_on:
      db:
        condition: service_healthy
        required: false
    volumes:
      - shared-data:/var/lib/shared
    secrets:
      - shared-password
    configs:
      - shared-config
  db:
    image: postgres:18
    platform: wasi/wasm
    user: "0"
    networks: [development]
    expose:
      - "5432"
    volumes:
      - shared-data:/var/lib/shared
      - db-data:/var/lib/postgresql/data
    secrets:
      - shared-password
      - db-password
    configs:
      - shared-config
      - db-schema
volumes:
  shared-data: {}
  db-data: {}
secrets:
  shared-password:
    file: ./shared-password.txt
  db-password:
    file: ./db-password.txt
configs:
  shared-config:
    content: shared
  db-schema:
    external: true
networks:
  development:
    driver: overlay
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	if len(app.Services) != 1 || app.Services[0].Name != "api" {
		t.Fatalf("expected only api to remain, got %#v", app.Services)
	}
	if len(app.Services[0].DependsOn) != 0 {
		t.Fatalf("expected dependency on excluded db to be removed, got %#v", app.Services[0].DependsOn)
	}
	if len(app.Volumes) != 1 || app.Volumes["shared-data"].Name != "shared-data" {
		t.Fatalf("expected only shared volume to remain, got %#v", app.Volumes)
	}
	if len(app.Secrets) != 1 || app.Secrets["shared-password"].Name != "shared-password" {
		t.Fatalf("expected only shared secret to remain, got %#v", app.Secrets)
	}
	if !app.Secrets["shared-password"].External {
		t.Fatalf("expected secret shared with excluded db to become external, got %#v", app.Secrets)
	}
	if len(app.Configs) != 1 || app.Configs["shared-config"].Name != "shared-config" {
		t.Fatalf("expected only shared config to remain, got %#v", app.Configs)
	}

	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}
	for _, path := range []string{
		"chart/templates/deployment-db.yaml",
		"chart/templates/service-db.yaml",
		"chart/templates/pvc-db-data.yaml",
		"chart/templates/configmap-db-schema.yaml",
		"chart/templates/secret-db-password.yaml",
		"chart/templates/uds-exemption.yaml",
	} {
		if _, err := os.Stat(filepath.Join(outDir, path)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be omitted, stat err = %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "chart", "templates", "secret-shared-password.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected external shared secret not to be created, stat err = %v", err)
	}
	for _, path := range []string{
		"chart/templates/pvc-shared-data.yaml",
		"chart/templates/configmap-shared-config.yaml",
	} {
		if _, err := os.Stat(filepath.Join(outDir, path)); err != nil {
			t.Fatalf("expected shared resource %s to remain: %v", path, err)
		}
	}
	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	if strings.Contains(zarfConfig, "postgres:18") || strings.Contains(zarfConfig, model.DependencyInitImage) {
		t.Fatalf("did not expect excluded image or dependency init image\n%s", zarfConfig)
	}
	for _, want := range []string{
		"name: SHARED_PASSWORD_SECRET_NAME",
		"name: SHARED_PASSWORD_SECRET_KEY",
		"default: shared-password",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected external secret variable %q\n%s", want, zarfConfig)
		}
	}
	if strings.Contains(zarfConfig, "sensitive: true") || strings.Contains(zarfConfig, "prompt: true") {
		t.Fatalf("external secret references must not prompt or be sensitive\n%s", zarfConfig)
	}

	deployment := readFile(t, filepath.Join(outDir, "chart", "templates", "deployment-api.yaml"))
	for _, want := range []string{
		"external compose secret shared-password requires a Kubernetes Secret name",
		".Values.externalSecrets.SHARED_PASSWORD.name",
		".Values.externalSecrets.SHARED_PASSWORD.key",
		"mountPath: /run/secrets/shared-password",
		"subPath: shared-password",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("expected deployment to contain %q\n%s", want, deployment)
		}
	}
}

func TestLoadCanonicalKeepsServiceWhenAnyDependencyReferenceIsRequired(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    depends_on:
      db:
        condition: service_started
        required: false
  worker:
    image: ghcr.io/acme/worker:1.0.0
    depends_on:
      db:
        condition: service_started
        required: true
  db:
    image: postgres:18
    expose: ["5432"]
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	if len(app.Services) != 3 {
		t.Fatalf("expected db to remain because one reference is required, got %#v", app.Services)
	}
	for _, service := range app.Services {
		if service.Name == "db" {
			return
		}
	}
	t.Fatalf("expected db to remain, got %#v", app.Services)
}

func TestLoadCanonicalKeepsShortSyntaxDependencyRequired(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    depends_on: [db]
  db:
    image: postgres:18
    expose: ["5432"]
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	if len(app.Services) != 2 {
		t.Fatalf("expected short-syntax dependency to remain required, got %#v", app.Services)
	}
}

func TestLoadCanonicalRejectsXUDSReferenceToExcludedService(t *testing.T) {
	t.Parallel()

	for _, extension := range []string{
		"network:\n    expose:\n      - service: db",
		"monitor:\n    - service: db",
	} {
		input := []byte(fmt.Sprintf(`name: demo
x-uds:
  %s
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    depends_on:
      db:
        required: false
  db:
    image: postgres:18
`, extension))
		_, err := compose.LoadCanonicalYAML(input)
		if err == nil || !strings.Contains(err.Error(), `references excluded service "db"`) {
			t.Fatalf("expected excluded service reference error, got %v", err)
		}
	}
}

func TestLoadCanonicalRejectsBuildContextFromExcludedService(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    build:
      context: .
      additional_contexts:
        base: service:base
    depends_on:
      base:
        required: false
  base:
    build:
      context: ./base
`)

	_, err := compose.LoadCanonicalYAML(input)
	if err == nil || !strings.Contains(err.Error(), `build references excluded service "base"`) {
		t.Fatalf("expected excluded build context error, got %v", err)
	}
}

func TestLoadCanonicalRejectsCollidingNormalizedNetworkNames(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    networks: [front_end]
networks:
  front_end:
  front-end:
`)

	_, err := compose.LoadCanonicalYAML(input)
	if err == nil || !strings.Contains(err.Error(), `duplicate normalized top-level network name "front-end"`) {
		t.Fatalf("expected normalized network collision, got %v", err)
	}
}

func TestLoadCanonicalPreservesLocalMembershipForExternalNetworks(t *testing.T) {
	input := []byte(`name: demo
services:
  ui:
    image: ghcr.io/acme/ui:1.0.0
    networks: [ui-api]
  api:
    image: ghcr.io/acme/api:1.0.0
    networks: [ui-api, api-db]
  db:
    image: postgres:16
    networks: [api-db]
networks:
  ui-api:
    external: true
  api-db:
`)

	originalStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = writer
	app, err := compose.LoadCanonicalYAML(input)
	os.Stderr = originalStderr
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatalf("close stderr writer: %v", closeErr)
	}
	warning, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatalf("read stderr: %v", readErr)
	}
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatalf("close stderr reader: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("expected external network to warn without stopping conversion, got %v", err)
	}
	for _, want := range []string{
		`external Compose network "ui-api" treated as package-local`,
		"declare access to external workloads with x-uds.network.allow",
	} {
		if !strings.Contains(string(warning), want) {
			t.Fatalf("expected external network warning to contain %q, got %q", want, warning)
		}
	}
	memberships := map[string]string{}
	for _, svc := range app.Services {
		memberships[svc.Name] = strings.Join(svc.Networks, ",")
	}
	for service, want := range map[string]string{
		"ui":  "ui-api",
		"api": "api-db,ui-api",
		"db":  "api-db",
	} {
		if got := memberships[service]; got != want {
			t.Fatalf("expected %s networks %q, got %q", service, want, got)
		}
	}
}

func TestWritePackagePreservesHostname(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    hostname: api-node
`)
	out := t.TempDir()
	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	if err := render.WritePackage(out, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}
	deployment := readFile(t, filepath.Join(out, "chart", "templates", "deployment-api.yaml"))
	if !strings.Contains(deployment, "hostname: api-node") {
		t.Fatalf("expected hostname in deployment\n%s", deployment)
	}
}

func TestWritePackageMapsStdinOpen(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    stdin_open: true
`)
	out := t.TempDir()
	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	if err := render.WritePackage(out, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}
	deployment := readFile(t, filepath.Join(out, "chart", "templates", "deployment-api.yaml"))
	if !strings.Contains(deployment, "stdin: true") {
		t.Fatalf("expected stdin to be enabled in deployment\n%s", deployment)
	}
}

func TestWritePackagePreservesDifferentNetworkMemberships(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
x-uds:
  package:
    namespace: demo-ns
  network:
    allow:
      - description: external-api
        direction: Egress
        remoteHost: api.example.com
services:
  frontend:
    image: ghcr.io/acme/frontend:1.0.0
    networks: [front]
  api:
    image: ghcr.io/acme/api:1.0.0
    networks: [front, back]
  db:
    image: postgres:16
    networks: [back]
networks:
  front:
  back:
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	deploymentLabels := func(service string) map[string]any {
		deployment := readYAMLMap(t, filepath.Join(outDir, "chart", "templates", "deployment-"+service+".yaml"))
		spec := mustMap(t, deployment["spec"])
		template := mustMap(t, spec["template"])
		metadata := mustMap(t, template["metadata"])
		return mustMap(t, metadata["labels"])
	}

	frontendLabels := deploymentLabels("frontend")
	if got := frontendLabels["network.compose.bridge.uds.dev/front"]; got != "true" {
		t.Fatalf("expected frontend membership label, got %#v", frontendLabels)
	}
	if _, exists := frontendLabels["network.compose.bridge.uds.dev/back"]; exists {
		t.Fatalf("did not expect frontend on back network: %#v", frontendLabels)
	}
	apiLabels := deploymentLabels("api")
	for _, key := range []string{"network.compose.bridge.uds.dev/front", "network.compose.bridge.uds.dev/back"} {
		if got := apiLabels[key]; got != "true" {
			t.Fatalf("expected api membership label %q, got %#v", key, apiLabels)
		}
	}

	udsPackage := readUDSPackageYAMLMap(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	spec := mustMap(t, udsPackage["spec"])
	network := mustMap(t, spec["network"])
	allows, ok := network["allow"].([]any)
	if !ok || len(allows) != 5 {
		t.Fatalf("expected four Compose network rules plus one explicit rule, got %#v", network["allow"])
	}
	rules := map[string]map[string]any{}
	for _, value := range allows {
		rule := mustMap(t, value)
		if description, _ := rule["description"].(string); description != "" {
			rules[description] = rule
		}
	}
	for _, networkName := range []string{"back", "front"} {
		labelKey := "network.compose.bridge.uds.dev/" + networkName
		for _, direction := range []string{"Ingress", "Egress"} {
			description := "compose-" + networkName + "-" + strings.ToLower(direction)
			rule, exists := rules[description]
			if !exists {
				t.Fatalf("expected rule %q in %#v", description, rules)
			}
			if got := rule["direction"]; got != direction {
				t.Fatalf("expected %s direction %s, got %#v", description, direction, got)
			}
			if got := rule["remoteNamespace"]; got != "demo-ns" {
				t.Fatalf("expected %s to stay in package namespace, got %#v", description, got)
			}
			if got := mustMap(t, rule["selector"])[labelKey]; got != "true" {
				t.Fatalf("expected %s local selector, got %#v", description, rule["selector"])
			}
			if got := mustMap(t, rule["remoteSelector"])[labelKey]; got != "true" {
				t.Fatalf("expected %s remote selector, got %#v", description, rule["remoteSelector"])
			}
			if _, exists := rule["remoteGenerated"]; exists {
				t.Fatalf("did not expect broad generated remote for %s", description)
			}
		}
	}
	if _, exists := rules["external-api"]; !exists {
		t.Fatalf("expected explicit x-uds allow rule to be preserved, got %#v", rules)
	}
}

func TestWritePackageKeepsBroadRulesForSharedNetworkMembership(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
  db:
    image: postgres:16
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackage := readUDSPackageYAMLMap(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	spec := mustMap(t, udsPackage["spec"])
	network := mustMap(t, spec["network"])
	allows, ok := network["allow"].([]any)
	if !ok || len(allows) != 2 {
		t.Fatalf("expected existing ingress and egress defaults, got %#v", network["allow"])
	}
	for index, direction := range []string{"Ingress", "Egress"} {
		rule := mustMap(t, allows[index])
		if rule["direction"] != direction || rule["remoteGenerated"] != "IntraNamespace" {
			t.Fatalf("expected %s IntraNamespace rule, got %#v", direction, rule)
		}
	}

	deployment := readFile(t, filepath.Join(outDir, "chart", "templates", "deployment-api.yaml"))
	if strings.Contains(deployment, "network.compose.bridge.uds.dev/") {
		t.Fatalf("did not expect membership labels for a shared network\n%s", deployment)
	}
}

func TestWritePackageAddsDeployTimeNetworkAllow(t *testing.T) {
	t.Parallel()

	input := []byte(`name: demo
x-uds:
  network:
    allow:
      - direction: Egress
        selector:
          app.kubernetes.io/name: api
        remoteNamespace: platform-service
        ports:
          - 8443
services:
  api:
    image: ghcr.io/acme/api:1.0.0
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	udsPackagePath := filepath.Join(outDir, "chart", "templates", "uds-package.yaml")
	udsPackage := readFile(t, udsPackagePath)
	for _, want := range []string{
		"remoteNamespace: platform-service",
		"app.kubernetes.io/name: api",
		"{{- with .Values.additionalNetworkAllow }}",
		"{{ toYaml . | nindent 12 }}",
	} {
		if !strings.Contains(udsPackage, want) {
			t.Fatalf("expected UDS Package template to contain %q\n%s", want, udsPackage)
		}
	}
	staticIndex := strings.Index(udsPackage, "remoteNamespace: platform-service")
	templateIndex := strings.Index(udsPackage, "{{- with .Values.additionalNetworkAllow }}")
	if staticIndex < 0 || templateIndex < 0 || staticIndex >= templateIndex {
		t.Fatalf("expected deploy-time rules after static rules\n%s", udsPackage)
	}

	parsedPackage := readUDSPackageYAMLMap(t, udsPackagePath)
	network := mustMap(t, mustMap(t, parsedPackage["spec"])["network"])
	allows, ok := network["allow"].([]any)
	if !ok || len(allows) != 3 {
		t.Fatalf("expected two inferred rules and one static rule, got %#v", network["allow"])
	}

	chartValues := readYAMLMap(t, filepath.Join(outDir, "chart", "values.yaml"))
	additionalAllow, ok := chartValues["additionalNetworkAllow"].([]any)
	if !ok || len(additionalAllow) != 0 {
		t.Fatalf("expected empty additionalNetworkAllow chart default, got %#v", chartValues["additionalNetworkAllow"])
	}

	zarfValues := readFile(t, filepath.Join(outDir, "values", "values.yaml"))
	variableToken := "###ZARF_VAR_ADDITIONAL_NETWORK_ALLOW###"
	if !strings.Contains(zarfValues, "additionalNetworkAllow:\n  "+variableToken) {
		t.Fatalf("expected raw structured Zarf substitution\n%s", zarfValues)
	}
	if strings.Contains(zarfValues, "'"+variableToken+"'") {
		t.Fatalf("additional network allow substitution must not be quoted\n%s", zarfValues)
	}

	for name, substituted := range map[string]string{
		"empty": strings.Replace(zarfValues, variableToken, "[]", 1),
		"multiline": strings.Replace(
			zarfValues,
			"  "+variableToken,
			"  - direction: Egress\n    remoteNamespace: logging\n    ports:\n      - 443",
			1,
		),
	} {
		var values map[string]any
		if err := yamlv3.Unmarshal([]byte(substituted), &values); err != nil {
			t.Fatalf("expected %s network allow substitution to be valid YAML: %v\n%s", name, err, substituted)
		}
		if _, ok := values["additionalNetworkAllow"].([]any); !ok {
			t.Fatalf("expected %s network allow substitution to remain a list, got %#v", name, values["additionalNetworkAllow"])
		}
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"name: ADDITIONAL_NETWORK_ALLOW",
		"default: '[]'",
		"autoIndent: true",
		"values/values.yaml",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q\n%s", want, zarfConfig)
		}
	}
}

func TestWritePackageAddsDomainPackageInterface(t *testing.T) {
	t.Parallel()

	input := []byte(`name: worker
x-uds:
  sso: []
services:
  worker:
    image: ghcr.io/acme/worker:1.0.0
    environment:
      DOMAIN: internal.example
    secrets:
      - api_key
secrets:
  api_key:
    file: ./api_key.txt
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	chartValues := readYAMLMap(t, filepath.Join(outDir, "chart", "values.yaml"))
	udsValues := mustMap(t, chartValues["uds"])
	if got := udsValues["domain"]; got != "uds.dev" {
		t.Fatalf("expected default UDS domain, got %#v", got)
	}
	workerEnvironment := mustMap(t, mustMap(t, chartValues["environment"])["worker"])
	if got := workerEnvironment["DOMAIN"]; got != "internal.example" {
		t.Fatalf("expected Compose DOMAIN environment value to remain ordinary configuration, got %#v", got)
	}
	if _, ok := chartValues["additionalNetworkAllow"].([]any); !ok {
		t.Fatalf("expected additional network allow values to coexist with uds.domain, got %#v", chartValues)
	}
	if _, ok := mustMap(t, chartValues["secrets"])["API_KEY"]; !ok {
		t.Fatalf("expected secret values to coexist with uds.domain, got %#v", chartValues)
	}

	zarfValues := readFile(t, filepath.Join(outDir, "values", "values.yaml"))
	for _, want := range []string{
		"uds:\n    domain: \"###ZARF_VAR_DOMAIN###\"",
		"###ZARF_VAR_WORKER_DOMAIN###",
		"###ZARF_VAR_API_KEY###",
		"###ZARF_VAR_ADDITIONAL_NETWORK_ALLOW###",
	} {
		if !strings.Contains(zarfValues, want) {
			t.Fatalf("expected Zarf values to contain %q\n%s", want, zarfValues)
		}
	}

	zarfConfig := readYAMLMap(t, filepath.Join(outDir, "zarf.yaml"))
	variables, ok := zarfConfig["variables"].([]any)
	if !ok {
		t.Fatalf("expected Zarf variables, got %#v", zarfConfig["variables"])
	}
	variablesByName := map[string]map[string]any{}
	for _, raw := range variables {
		variable := mustMap(t, raw)
		variablesByName[variable["name"].(string)] = variable
	}
	domain := variablesByName["DOMAIN"]
	if domain == nil || domain["default"] != "uds.dev" || domain["description"] != "The domain for accessing endpoints" {
		t.Fatalf("expected non-sensitive DOMAIN package variable, got %#v", domain)
	}
	if _, exists := domain["sensitive"]; exists {
		t.Fatalf("DOMAIN must not be sensitive, got %#v", domain)
	}
	if _, exists := domain["prompt"]; exists {
		t.Fatalf("DOMAIN must not prompt, got %#v", domain)
	}
	if variablesByName["WORKER_DOMAIN"] == nil || variablesByName["ADDITIONAL_NETWORK_ALLOW"] == nil || variablesByName["API_KEY"] == nil {
		t.Fatalf("expected automatic, environment, and secret variables to coexist, got %#v", variablesByName)
	}

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	if strings.Contains(udsPackage, "clientId:") {
		t.Fatalf("explicitly empty SSO must remain disabled\n%s", udsPackage)
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

	configMap := readFile(t, filepath.Join(outDir, "chart", "templates", "configmap-app-config.yaml"))
	for _, want := range []string{"key: value", `uds.dev/pod-reload: "true"`} {
		if !strings.Contains(configMap, want) {
			t.Fatalf("expected configmap to contain %q\n%s", want, configMap)
		}
	}

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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
		"name: demo",
		"namespace: demo-ns",
		"localPath: chart",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q\n%s", want, zarfConfig)
		}
	}
	if strings.Contains(zarfConfig, "manifests:") {
		t.Fatalf("did not expect manifest-based zarf config\n%s", zarfConfig)
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	if !strings.Contains(udsPackage, "service: web") {
		t.Fatalf("expected published web service to be auto-exposed")
	}
	if strings.Contains(udsPackage, "service: db") {
		t.Fatalf("did not expect internal-only db service to be auto-exposed")
	}
}

func TestWritePackageAutoExposeUsesComposePortDeclarationOrder(t *testing.T) {
	t.Parallel()

	input := []byte(`name: homelab
services:
  gitea:
    image: docker.gitea.com/gitea:1.25.5
    ports:
      - "3000:3000"
      - "222:22"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	expose := firstExposeRule(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	if got := expose["port"]; got != 3000 {
		t.Fatalf("expected first declared published port 3000 to be auto-exposed, got %#v", got)
	}
}

func TestWritePackageAutoExposePrefersWebAppProtocolHint(t *testing.T) {
	t.Parallel()

	input := []byte(`name: homelab
services:
  gitea:
    image: docker.gitea.com/gitea:1.25.5
    ports:
      - name: ssh
        target: 22
        published: "222"
        protocol: tcp
        app_protocol: ssh
      - name: web
        target: 3000
        published: "3000"
        protocol: tcp
        app_protocol: http
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	expose := firstExposeRule(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	if got := expose["port"]; got != 3000 {
		t.Fatalf("expected app_protocol http port 3000 to be auto-exposed instead of SSH, got %#v", got)
	}

	service := readFile(t, filepath.Join(outDir, "chart", "templates", "service-gitea.yaml"))
	if !strings.Contains(service, "appProtocol: http") {
		t.Fatalf("expected Service port to preserve app_protocol hint\n%s", service)
	}
}

func TestWritePackageAutoExposePrefersWebPortNameHint(t *testing.T) {
	t.Parallel()

	input := []byte(`name: homelab
services:
  gitea:
    image: docker.gitea.com/gitea:1.25.5
    ports:
      - name: ssh
        target: 22
        published: "222"
        protocol: tcp
      - name: web
        target: 3000
        published: "3000"
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

	expose := firstExposeRule(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	if got := expose["port"]; got != 3000 {
		t.Fatalf("expected web-named port 3000 to be auto-exposed instead of SSH, got %#v", got)
	}
}

func TestWritePackageAutoExposeAllowsNonHTTPPortsWithoutFiltering(t *testing.T) {
	t.Parallel()

	input := []byte(`name: ssh-only
services:
  bastion:
    image: ghcr.io/acme/bastion:1.0.0
    ports:
      - "222:22"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	expose := firstExposeRule(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	if got := expose["port"]; got != 22 {
		t.Fatalf("expected first declared published port 22 to remain eligible for auto-expose, got %#v", got)
	}
}

func TestWritePackageAutoExposeAllowsUDPPublishedPortsWithoutFiltering(t *testing.T) {
	t.Parallel()

	input := []byte(`name: dns-demo
services:
  dns:
    image: ghcr.io/acme/dns:1.0.0
    ports:
      - "1053:53/udp"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	expose := firstExposeRule(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	if got := expose["port"]; got != 53 {
		t.Fatalf("expected UDP port 53 to remain eligible for auto-expose, got %#v", got)
	}

	service := readFile(t, filepath.Join(outDir, "chart", "templates", "service-dns.yaml"))
	if !strings.Contains(service, "protocol: UDP") {
		t.Fatalf("expected Service port to preserve UDP protocol\n%s", service)
	}
}

func TestWritePackageSanitizesComposePortNames(t *testing.T) {
	t.Parallel()

	input := []byte(`name: port-name-demo
x-uds:
  monitor:
    - service: web
      targetPort: 8080
services:
  web:
    image: ghcr.io/acme/web:1.0.0
    ports:
      - name: WEB__UI
        target: 8080
        published: "8080"
        protocol: tcp
      - name: "123"
        target: 9090
        published: "9090"
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

	deployment := readFile(t, filepath.Join(outDir, "chart", "templates", "deployment-web.yaml"))
	service := readFile(t, filepath.Join(outDir, "chart", "templates", "service-web.yaml"))
	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	if !strings.Contains(deployment, "name: web-ui") {
		t.Fatalf("expected deployment port name to preserve the sanitized Compose name\n%s", deployment)
	}
	for _, want := range []string{
		"name: web-ui",
		"name: port-9090-tcp",
	} {
		if !strings.Contains(service, want) {
			t.Fatalf("expected service port name %q\n%s", want, service)
		}
	}
	if !strings.Contains(udsPackage, "portName: web-ui") {
		t.Fatalf("expected monitor port name to preserve the sanitized Compose name\n%s", udsPackage)
	}
}

func TestWritePackageUsesNeutralUnnamedPortNames(t *testing.T) {
	t.Parallel()

	input := []byte(`name: unnamed-port-demo
services:
  db:
    image: postgres:18.4-bookworm
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

	service := readFile(t, filepath.Join(outDir, "chart", "templates", "service-db.yaml"))
	if !strings.Contains(service, "name: port-5432-tcp") {
		t.Fatalf("expected neutral service port name\n%s", service)
	}
}

func TestWritePackageRejectsDuplicateResolvedPortNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		ports        string
		resolvedName string
		firstPort    string
		secondPort   string
	}{
		{
			name: "generated and explicit",
			ports: `
      - name: port-9090-tcp
        target: 8080
        published: "8080"
        protocol: tcp
      - target: 9090
        published: "9090"
        protocol: tcp`,
			resolvedName: "port-9090-tcp",
			firstPort:    `8080/tcp (name "port-9090-tcp")`,
			secondPort:   "9090/tcp (unnamed)",
		},
		{
			name: "sanitized explicit names",
			ports: `
      - name: WEB__UI
        target: 8080
        published: "8080"
        protocol: tcp
      - name: web-ui
        target: 9090
        published: "9090"
        protocol: tcp`,
			resolvedName: "web-ui",
			firstPort:    `8080/tcp (name "WEB__UI")`,
			secondPort:   `9090/tcp (name "web-ui")`,
		},
		{
			name: "truncated explicit names",
			ports: `
      - name: abcdefghijklmno-one
        target: 8080
        published: "8080"
        protocol: tcp
      - name: abcdefghijklmno-two
        target: 9090
        published: "9090"
        protocol: tcp`,
			resolvedName: "abcdefghijklmno",
			firstPort:    `8080/tcp (name "abcdefghijklmno-one")`,
			secondPort:   `9090/tcp (name "abcdefghijklmno-two")`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := []byte(`name: collision-demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    ports:` + tt.ports + "\n")
			app, err := compose.LoadCanonicalYAML(input)
			if err != nil {
				t.Fatalf("LoadCanonicalYAML() error = %v", err)
			}

			outDir := filepath.Join(t.TempDir(), "out")
			err = render.WritePackage(outDir, app)
			if err == nil {
				t.Fatal("expected WritePackage() to reject duplicate resolved port names")
			}
			for _, want := range []string{`service "api"`, tt.resolvedName, tt.firstPort, tt.secondPort} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("expected error to contain %q, got %v", want, err)
				}
			}
			if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
				t.Fatalf("expected validation before output creation, os.Stat() error = %v", statErr)
			}
		})
	}
}

func TestWritePackageScopesPortNamesToService(t *testing.T) {
	t.Parallel()

	input := []byte(`name: shared-port-name-demo
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "8080"
  admin:
    image: ghcr.io/acme/admin:1.0.0
    expose:
      - "8080"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	if err := render.WritePackage(t.TempDir(), app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}
}

func TestWritePackageAllowsSamePortNumberWithDifferentProtocols(t *testing.T) {
	t.Parallel()

	input := []byte(`name: protocol-port-name-demo
services:
  dns:
    image: ghcr.io/acme/dns:1.0.0
    ports:
      - target: 53
        published: "53"
        protocol: tcp
      - target: 53
        published: "53"
        protocol: udp
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	service := readFile(t, filepath.Join(outDir, "chart", "templates", "service-dns.yaml"))
	for _, want := range []string{"name: port-53-tcp", "name: port-53-udp"} {
		if !strings.Contains(service, want) {
			t.Fatalf("expected service to contain %q\n%s", want, service)
		}
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	for _, want := range []string{
		"clientId: myapp",
		"name: Myapp",
		"https://web.{{ .Values.uds.domain }}/*",
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	if !strings.Contains(udsPackage, "clientId: custom-id") {
		t.Fatalf("expected user-provided clientId to be preserved\n%s", udsPackage)
	}
	if !strings.Contains(udsPackage, "name: Myapp") {
		t.Fatalf("expected inferred name\n%s", udsPackage)
	}
	if !strings.Contains(udsPackage, "https://web.{{ .Values.uds.domain }}/*") {
		t.Fatalf("expected inferred redirectUris\n%s", udsPackage)
	}
}

func TestSSOExplicitRedirectURIsRemainLiteralAndOrdered(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
x-uds:
  sso:
    - redirectUris:
        - https://first.example/callback
        - "https://custom.{{ .Values.uds.domain }}/callback"
        - https://last.uds.dev/callback
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	redirects := []string{
		"https://first.example/callback",
		"https://custom.{{ .Values.uds.domain }}/callback",
		"https://last.uds.dev/callback",
	}
	previous := -1
	for _, redirect := range redirects {
		index := strings.Index(udsPackage, redirect)
		if index < 0 {
			t.Fatalf("expected explicit redirect URI %q to remain unchanged\n%s", redirect, udsPackage)
		}
		if index <= previous {
			t.Fatalf("expected explicit redirect URI declaration order to be preserved\n%s", udsPackage)
		}
		previous = index
	}
	if strings.Contains(udsPackage, "https://web.{{ .Values.uds.domain }}/*") {
		t.Fatalf("did not expect inferred redirect URI when redirectUris is supplied\n%s", udsPackage)
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	for _, want := range []string{
		"monitor:",
		"description: API metrics",
		"selector:",
		"podSelector:",
		"app.kubernetes.io/name: api",
		"portName: port-9090-tcp",
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
	for _, want := range []string{
		"kind: PodMonitor",
		"path: /custom/metrics",
		"portName: port-9090-tcp",
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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
      portName: port-8080-tcp
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
	if !strings.Contains(err.Error(), `portName "port-8080-tcp" resolves to 8080, but targetPort is 9090`) {
		t.Fatalf("expected mismatched port error, got %v", err)
	}
}

func TestWritePackageRendersCABundleConfigMap(t *testing.T) {
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

	udsPackage := readUDSPackageYAMLMap(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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

	if _, err := os.Stat(filepath.Join(outDir, "uds-config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected uds-config.yaml to be omitted, stat err = %v", err)
	}
}

func TestLoadCanonicalRejectsRemovedCABundleFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		field   string
		value   string
		message string
	}{
		{name: "certs", field: "CA_BUNDLE_CERTS", value: `"LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0t"`, message: "invalid x-uds.caBundle.CA_BUNDLE_CERTS"},
		{name: "dod", field: "CA_BUNDLE_INCLUDE_DOD_CERTS", value: "true", message: "invalid x-uds.caBundle.CA_BUNDLE_INCLUDE_DOD_CERTS"},
		{name: "public", field: "CA_BUNDLE_INCLUDE_PUBLIC_CERTS", value: "true", message: "invalid x-uds.caBundle.CA_BUNDLE_INCLUDE_PUBLIC_CERTS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte("name: trust-demo\nx-uds:\n  caBundle:\n    " + tt.field + ": " + tt.value + "\nservices:\n  api:\n    image: ghcr.io/acme/api:1.0.0\n")

			_, err := compose.LoadCanonicalYAML(input)
			if err == nil {
				t.Fatalf("expected removed caBundle field %s to fail", tt.field)
			}
			if !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("expected removed caBundle field error, got %v", err)
			}
		})
	}
}

func TestLoadCanonicalRejectsUnsupportedCABundleConfigMapField(t *testing.T) {
	t.Parallel()

	input := []byte(`name: trust-demo
x-uds:
  caBundle:
    configMap:
      target: custom-trust-bundle
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    expose:
      - "8080"
`)

	_, err := compose.LoadCanonicalYAML(input)
	if err == nil {
		t.Fatalf("expected unsupported caBundle configMap field to fail")
	}
	if !strings.Contains(err.Error(), "invalid x-uds.caBundle.configMap.target") {
		t.Fatalf("expected unsupported caBundle configMap field error, got %v", err)
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

func TestExemptionForRootUser(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		user string
	}{
		{"named root", "root"},
		{"uid zero", "0"},
		{"uid:gid zero", "0:0"},
		{"root:root", "root:root"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := []byte("name: myapp\nservices:\n  gitea:\n    image: gitea:latest\n    user: " + tc.user + "\n")
			app, err := compose.LoadCanonicalYAML(input)
			if err != nil {
				t.Fatalf("LoadCanonicalYAML() error = %v", err)
			}
			outDir := t.TempDir()
			if err := render.WritePackage(outDir, app); err != nil {
				t.Fatalf("WritePackage() error = %v", err)
			}

			exemption := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-exemption.yaml"))
			for _, want := range []string{
				"kind: Exemption",
				"namespace: uds-policy-exemptions",
				"RequireNonRootUser",
				"^gitea-.*",
			} {
				if !strings.Contains(exemption, want) {
					t.Fatalf("expected uds-exemption.yaml to contain %q\n%s", want, exemption)
				}
			}
			if strings.Contains(exemption, "DisallowPrivileged") {
				t.Fatalf("did not expect DisallowPrivileged in root-user exemption\n%s", exemption)
			}
		})
	}
}

func TestExemptionForPrivileged(t *testing.T) {
	t.Parallel()

	input := []byte(`name: homelab
services:
  runner:
    image: gitea/act_runner:latest
    privileged: true
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	exemption := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-exemption.yaml"))
	for _, want := range []string{
		"kind: Exemption",
		"namespace: uds-policy-exemptions",
		"DisallowPrivileged",
		"^runner-.*",
		"privileged policy exemption for homelab runner",
	} {
		if !strings.Contains(exemption, want) {
			t.Fatalf("expected uds-exemption.yaml to contain %q\n%s", want, exemption)
		}
	}
	if strings.Contains(exemption, "RequireNonRootUser") {
		t.Fatalf("did not expect RequireNonRootUser for non-root privileged service\n%s", exemption)
	}
}

func TestExemptionForCapAdd(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
services:
  net:
    image: myapp:latest
    cap_add:
      - NET_ADMIN
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	exemption := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-exemption.yaml"))
	for _, want := range []string{
		"DropAllCapabilities",
		"RestrictCapabilities",
		"^net-.*",
	} {
		if !strings.Contains(exemption, want) {
			t.Fatalf("expected uds-exemption.yaml to contain %q\n%s", want, exemption)
		}
	}
}

func TestExemptionForSeccompUnconfined(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
services:
  svc:
    image: myapp:latest
    security_opt:
      - seccomp:unconfined
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	exemption := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-exemption.yaml"))
	if !strings.Contains(exemption, "RestrictSeccomp") {
		t.Fatalf("expected RestrictSeccomp in exemption\n%s", exemption)
	}
}

func TestNoExemptionForSecureService(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
services:
  api:
    image: myapp:latest
    user: "1000"
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "chart", "templates", "uds-exemption.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected uds-exemption.yaml to be absent for a secure service, stat err = %v", err)
	}
}

func TestNoExemptionForCapDropOnly(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
services:
  api:
    image: myapp:latest
    cap_drop:
      - ALL
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "chart", "templates", "uds-exemption.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected uds-exemption.yaml to be absent for cap_drop-only service, stat err = %v", err)
	}
}

func TestExemptionMultipleServicesInZarf(t *testing.T) {
	t.Parallel()

	input := []byte(`name: homelab
services:
  gitea:
    image: gitea:latest
    user: root
    ports:
      - mode: ingress
        target: 3000
        published: "3000"
        protocol: tcp
  runner:
    image: gitea/act_runner:latest
    privileged: true
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	exemption := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-exemption.yaml"))
	for _, want := range []string{
		"RequireNonRootUser",
		"^gitea-.*",
		"root user policy exemption for homelab gitea",
		"DisallowPrivileged",
		"^runner-.*",
		"privileged policy exemption for homelab runner",
		"namespace: uds-policy-exemptions",
	} {
		if !strings.Contains(exemption, want) {
			t.Fatalf("expected uds-exemption.yaml to contain %q\n%s", want, exemption)
		}
	}

	// The exemption is rendered as a chart template (not a path-referenced
	// manifest), so the Zarf package references the generated chart instead.
	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	if !strings.Contains(zarfConfig, "localPath: chart") {
		t.Fatalf("expected zarf.yaml to reference the generated chart\n%s", zarfConfig)
	}
}

func TestCapDropUpdatesSecurityContext(t *testing.T) {
	t.Parallel()

	input := []byte(`name: myapp
services:
  api:
    image: myapp:latest
    cap_drop:
      - ALL
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	deployment := readFile(t, filepath.Join(outDir, "chart", "templates", "deployment-api.yaml"))
	for _, want := range []string{
		"capabilities:",
		"drop:",
		"- ALL",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("expected deployment to contain %q\n%s", want, deployment)
		}
	}
}

func TestWritePackageGeneratesHelmChart(t *testing.T) {
	t.Parallel()

	input := []byte(`name: shop
services:
  api:
    image: ghcr.io/acme/api:1.0.0
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

	chartMeta := readFile(t, filepath.Join(outDir, "chart", "Chart.yaml"))
	for _, want := range []string{
		"apiVersion: v2",
		"name: shop",
		"version: 0.1.0",
	} {
		if !strings.Contains(chartMeta, want) {
			t.Fatalf("expected Chart.yaml to contain %q\n%s", want, chartMeta)
		}
	}

	// Resources are rendered as chart templates, not raw manifests.
	for _, rel := range []string{"namespace.yaml", "deployment-api.yaml", "service-api.yaml", "uds-package.yaml"} {
		if _, err := os.Stat(filepath.Join(outDir, "chart", "templates", rel)); err != nil {
			t.Fatalf("expected chart template %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "manifests")); !os.IsNotExist(err) {
		t.Fatalf("did not expect legacy manifests/ directory")
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"charts:",
		"localPath: chart",
		"name: shop",
		"version: 0.1.0",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q\n%s", want, zarfConfig)
		}
	}
	if strings.Contains(zarfConfig, "manifests:") {
		t.Fatalf("did not expect manifest-based zarf config\n%s", zarfConfig)
	}
	// Every package has a values file for deploy-time additional network rules.
	zarfValues := readFile(t, filepath.Join(outDir, "values", "values.yaml"))
	if !strings.Contains(zarfValues, "additionalNetworkAllow:\n  ###ZARF_VAR_ADDITIONAL_NETWORK_ALLOW###") {
		t.Fatalf("expected deploy-time network allow placeholder\n%s", zarfValues)
	}
	for _, want := range []string{"name: ADDITIONAL_NETWORK_ALLOW", "default: '[]'", "autoIndent: true", "valuesFiles:"} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q\n%s", want, zarfConfig)
		}
	}
}

func TestWritePackageGeneratesPackageDocumentation(t *testing.T) {
	t.Parallel()

	input := []byte(`name: shop
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    environment:
      LOG_LEVEL: info
    secrets:
      - api_key
    depends_on:
      database:
        condition: service_healthy
  database:
    image: postgres:17
secrets:
  api_key:
    file: ./api_key.txt
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	zarfConfig := readYAMLMap(t, filepath.Join(outDir, "zarf.yaml"))
	documentation := mustMap(t, zarfConfig["documentation"])
	wantDocumentation := map[string]string{
		"readme":        "docs/README.md",
		"configuration": "docs/configuration.md",
		"dependencies":  "docs/dependencies.md",
	}
	for key, want := range wantDocumentation {
		if got := documentation[key]; got != want {
			t.Fatalf("documentation[%q] = %#v, want %q", key, got, want)
		}
	}

	readme := readFile(t, filepath.Join(outDir, "docs", "README.md"))
	for _, want := range []string{"# shop", "deploys the shop application to the shop namespace", "[Configuration](configuration.md)", "[Dependencies](dependencies.md)"} {
		if !strings.Contains(readme, want) {
			t.Fatalf("expected generated readme to contain %q\n%s", want, readme)
		}
	}

	configuration := readFile(t, filepath.Join(outDir, "docs", "configuration.md"))
	for _, want := range []string{
		"| `DOMAIN` | Cluster domain used by generated application endpoints | uds.dev | false |",
		"| `ADDITIONAL_NETWORK_ALLOW` | Additional UDS network allow rules supplied as a YAML array | [] | false |",
		"| `API_KEY` | Value for Compose secret api-key | — | true |",
		"| `API_LOG_LEVEL` | Value for LOG_LEVEL environment variable on Compose service api | info | false |",
	} {
		if !strings.Contains(configuration, want) {
			t.Fatalf("expected generated configuration documentation to contain %q\n%s", want, configuration)
		}
	}

	dependencies := readFile(t, filepath.Join(outDir, "docs", "dependencies.md"))
	for _, want := range []string{
		"| `api` | `ghcr.io/acme/api:1.0.0` | database (service_healthy) |",
		"| `database` | `postgres:17` | — |",
	} {
		if !strings.Contains(dependencies, want) {
			t.Fatalf("expected generated dependency documentation to contain %q\n%s", want, dependencies)
		}
	}
}

func TestWritePackageSecretFlowsThroughChartValues(t *testing.T) {
	t.Parallel()

	input := []byte(`name: shop
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    secrets:
      - api_key
secrets:
  api_key:
    file: ./api_key.txt
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	// The Secret template reads from chart values, not a static literal.
	secret := readFile(t, filepath.Join(outDir, "chart", "templates", "secret-api-key.yaml"))
	if !strings.Contains(secret, "{{ .Values.secrets.API_KEY | quote }}") {
		t.Fatalf("expected secret template to read from chart values\n%s", secret)
	}
	if strings.Contains(secret, "###ZARF_VAR_") {
		t.Fatalf("did not expect Zarf placeholder inside chart template\n%s", secret)
	}

	// The Zarf variable placeholder lives in the Zarf-templated values file.
	zarfValues := readFile(t, filepath.Join(outDir, "values", "values.yaml"))
	if !strings.Contains(zarfValues, "API_KEY: '###ZARF_VAR_API_KEY###'") {
		t.Fatalf("expected values.yaml to carry the variable placeholder\n%s", zarfValues)
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"valuesFiles:",
		"values/values.yaml",
		"name: API_KEY",
		"prompt: true",
		"sensitive: true",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q\n%s", want, zarfConfig)
		}
	}

	deployment := readFile(t, filepath.Join(outDir, "chart", "templates", "deployment-api.yaml"))
	for _, want := range []string{
		"name: secret-api-key",
		"secretName: api-key",
		"key: api-key",
		"path: api-key",
		"mountPath: /run/secrets/api_key",
		"subPath: api-key",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("expected package-owned secret file mount %q\n%s", want, deployment)
		}
	}
	if strings.Contains(deployment, "mountPath: /run/secrets\n") {
		t.Fatalf("secret must not replace the service-account parent directory\n%s", deployment)
	}
}

func TestWritePackageNativeExternalSecretUsesNameAndKeyVariables(t *testing.T) {
	t.Parallel()

	input := []byte(`name: shop
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    secrets:
      - source: operator-credential
        target: /etc/shop/database-password
secrets:
  operator-credential:
    external: true
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	if !app.Secrets["operator-credential"].External {
		t.Fatalf("expected native external Compose secret to remain external, got %#v", app.Secrets)
	}

	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "chart", "templates", "secret-operator-credential.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected no chart-owned Secret for native external secret, stat err = %v", err)
	}

	chartValues := readFile(t, filepath.Join(outDir, "chart", "values.yaml"))
	for _, want := range []string{
		"externalSecrets:",
		"OPERATOR_CREDENTIAL:",
		"name: \"\"",
		"key: operator-credential",
	} {
		if !strings.Contains(chartValues, want) {
			t.Fatalf("expected chart values to contain %q\n%s", want, chartValues)
		}
	}

	zarfValues := readFile(t, filepath.Join(outDir, "values", "values.yaml"))
	for _, want := range []string{
		"###ZARF_VAR_OPERATOR_CREDENTIAL_SECRET_NAME###",
		"###ZARF_VAR_OPERATOR_CREDENTIAL_SECRET_KEY###",
	} {
		if !strings.Contains(zarfValues, want) {
			t.Fatalf("expected Zarf values to contain %q\n%s", want, zarfValues)
		}
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"name: OPERATOR_CREDENTIAL_SECRET_NAME",
		"name: OPERATOR_CREDENTIAL_SECRET_KEY",
		"default: operator-credential",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected external secret variable %q\n%s", want, zarfConfig)
		}
	}
	if strings.Contains(zarfConfig, "sensitive: true") || strings.Contains(zarfConfig, "prompt: true") {
		t.Fatalf("external secret references must not prompt or be sensitive\n%s", zarfConfig)
	}

	deployment := readFile(t, filepath.Join(outDir, "chart", "templates", "deployment-api.yaml"))
	for _, want := range []string{
		".Values.externalSecrets.OPERATOR_CREDENTIAL.name",
		".Values.externalSecrets.OPERATOR_CREDENTIAL.key",
		"mountPath: /etc/shop/database-password",
		"subPath: operator-credential",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("expected custom external secret file mount %q\n%s", want, deployment)
		}
	}
}

func TestWritePackageExternalizesServiceEnvironmentThroughConfigMap(t *testing.T) {
	t.Parallel()

	input := []byte(`name: shop
services:
  api:
    image: ghcr.io/acme/api:1.0.0
    environment:
      OPTIONAL_SETTING:
      POSTGRES_HOST: db
      POSTGRES_PORT: "5432"
      POSTGRES_PASSWORD_FILE: /run/secrets/postgres-password
  ui:
    image: ghcr.io/acme/ui:1.0.0
  worker:
    image: ghcr.io/acme/worker:1.0.0
    environment:
      LOG_LEVEL: info
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	templatesDir := filepath.Join(outDir, "chart", "templates")
	configMap := readFile(t, filepath.Join(templatesDir, "configmap-api-environment.yaml"))
	for _, want := range []string{
		"name: api-environment",
		"uds.dev/pod-reload: \"true\"",
		`POSTGRES_HOST: {{ index .Values.environment "api" "POSTGRES_HOST" | quote }}`,
		`OPTIONAL_SETTING: {{ index .Values.environment "api" "OPTIONAL_SETTING" | quote }}`,
		`POSTGRES_PASSWORD_FILE: {{ index .Values.environment "api" "POSTGRES_PASSWORD_FILE" | quote }}`,
	} {
		if !strings.Contains(configMap, want) {
			t.Fatalf("expected environment ConfigMap to contain %q\n%s", want, configMap)
		}
	}
	if _, err := os.Stat(filepath.Join(templatesDir, "configmap-ui-environment.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected no environment ConfigMap for ui, stat err = %v", err)
	}
	workerConfigMap := readFile(t, filepath.Join(templatesDir, "configmap-worker-environment.yaml"))
	for _, want := range []string{
		"name: worker-environment",
		`LOG_LEVEL: {{ index .Values.environment "worker" "LOG_LEVEL" | quote }}`,
	} {
		if !strings.Contains(workerConfigMap, want) {
			t.Fatalf("expected worker environment ConfigMap to contain %q\n%s", want, workerConfigMap)
		}
	}

	deployment := readFile(t, filepath.Join(templatesDir, "deployment-api.yaml"))
	for _, want := range []string{"envFrom:", "configMapRef:", "name: api-environment"} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("expected deployment to contain %q\n%s", want, deployment)
		}
	}
	if strings.Contains(deployment, "POSTGRES_HOST") {
		t.Fatalf("did not expect literal environment entries on the deployment\n%s", deployment)
	}

	chartValues := readYAMLMap(t, filepath.Join(outDir, "chart", "values.yaml"))
	apiValues := mustMap(t, mustMap(t, chartValues["environment"])["api"])
	if got := apiValues["POSTGRES_HOST"]; got != "db" {
		t.Fatalf("expected Compose host default, got %#v", got)
	}
	if got := apiValues["POSTGRES_PORT"]; got != "5432" {
		t.Fatalf("expected Compose port default to remain a string, got %#v", got)
	}
	if got := apiValues["OPTIONAL_SETTING"]; got != "" {
		t.Fatalf("expected unset Compose value to default to an empty string, got %#v", got)
	}

	zarfValues := readFile(t, filepath.Join(outDir, "values", "values.yaml"))
	for _, want := range []string{
		"###ZARF_VAR_API_POSTGRES_HOST###",
		"###ZARF_VAR_API_OPTIONAL_SETTING###",
		"###ZARF_VAR_API_POSTGRES_PORT###",
		"###ZARF_VAR_API_POSTGRES_PASSWORD_FILE###",
		"###ZARF_VAR_WORKER_LOG_LEVEL###",
	} {
		if !strings.Contains(zarfValues, want) {
			t.Fatalf("expected Zarf values to contain %q\n%s", want, zarfValues)
		}
	}

	zarfConfig := readFile(t, filepath.Join(outDir, "zarf.yaml"))
	for _, want := range []string{
		"name: API_POSTGRES_HOST",
		"default: db",
		"name: API_OPTIONAL_SETTING",
		"default: \"\"",
		"name: API_POSTGRES_PORT",
		"default: \"5432\"",
		"name: API_POSTGRES_PASSWORD_FILE",
		"default: /run/secrets/postgres-password",
		"name: WORKER_LOG_LEVEL",
		"valuesFiles:",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q\n%s", want, zarfConfig)
		}
	}
	if strings.Contains(zarfConfig, "prompt: true") || strings.Contains(zarfConfig, "sensitive: true") {
		t.Fatalf("ordinary environment variables must not prompt or be sensitive\n%s", zarfConfig)
	}
}

func TestWritePackageExternalizesKubernetesEnvironmentNames(t *testing.T) {
	t.Parallel()

	input := []byte(`name: elk
services:
  elasticsearch:
    image: elasticsearch:7.16.1
    environment:
      discovery.type: single-node
  logstash:
    image: logstash:7.16.1
    environment:
      discovery.seed_hosts: logstash
`)

	app, err := compose.LoadCanonicalYAML(input)
	if err != nil {
		t.Fatalf("LoadCanonicalYAML() error = %v", err)
	}
	outDir := t.TempDir()
	if err := render.WritePackage(outDir, app); err != nil {
		t.Fatalf("WritePackage() error = %v", err)
	}

	templatesDir := filepath.Join(outDir, "chart", "templates")
	configMaps := map[string][]string{
		"configmap-elasticsearch-environment.yaml": {
			`discovery.type: {{ index .Values.environment "elasticsearch" "discovery.type" | quote }}`,
		},
		"configmap-logstash-environment.yaml": {
			`discovery.seed_hosts: {{ index .Values.environment "logstash" "discovery.seed_hosts" | quote }}`,
		},
	}
	for name, expected := range configMaps {
		configMap := readFile(t, filepath.Join(templatesDir, name))
		for _, want := range expected {
			if !strings.Contains(configMap, want) {
				t.Fatalf("expected environment ConfigMap to contain %q\n%s", want, configMap)
			}
		}
	}

	zarfValues := readFile(t, filepath.Join(outDir, "values", "values.yaml"))
	for _, want := range []string{
		"###ZARF_VAR_ELASTICSEARCH_DISCOVERY_TYPE###",
		"###ZARF_VAR_LOGSTASH_DISCOVERY_SEED_HOSTS###",
	} {
		if !strings.Contains(zarfValues, want) {
			t.Fatalf("expected Zarf values to contain %q\n%s", want, zarfValues)
		}
	}
}

func TestWritePackageRejectsInvalidEnvironmentExternalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		app     model.App
		wantErr string
	}{
		{
			name: "invalid Kubernetes environment name",
			app: model.App{
				Package: model.Package{Name: "shop", Namespace: "shop", Version: "0.1.0"},
				Services: []model.Service{{
					Name: "api", Image: "ghcr.io/acme/api:1.0.0",
					Env: []model.EnvVar{{Name: "DATABASE/URL", Value: "postgres://db"}},
				}},
			},
			wantErr: `invalid environment variable "DATABASE/URL" on service "api"`,
		},
		{
			name: "normalized environment variable collision",
			app: model.App{
				Package: model.Package{Name: "shop", Namespace: "shop", Version: "0.1.0"},
				Services: []model.Service{{
					Name: "api", Image: "ghcr.io/acme/api:1.0.0",
					Env: []model.EnvVar{
						{Name: "LOG_LEVEL", Value: "info"},
						{Name: "log_level", Value: "debug"},
					},
				}},
			},
			wantErr: `generates Zarf variable "API_LOG_LEVEL", which conflicts with environment variable "LOG_LEVEL" on service "api"`,
		},
		{
			name: "secret variable collision",
			app: model.App{
				Package: model.Package{Name: "shop", Namespace: "shop", Version: "0.1.0"},
				Services: []model.Service{{
					Name: "api", Image: "ghcr.io/acme/api:1.0.0",
					Env: []model.EnvVar{{Name: "TOKEN", Value: "not-a-secret"}},
				}},
				Secrets: map[string]model.Secret{"api-token": {Name: "api-token"}},
			},
			wantErr: `generates Zarf variable "API_TOKEN", which conflicts with compose secret "api-token"`,
		},
		{
			name: "external secret reference variable collision",
			app: model.App{
				Package: model.Package{Name: "shop", Namespace: "shop", Version: "0.1.0"},
				Services: []model.Service{{
					Name: "api-token-secret", Image: "ghcr.io/acme/api:1.0.0",
					Env: []model.EnvVar{{Name: "NAME", Value: "not-a-secret-reference"}},
				}},
				Secrets: map[string]model.Secret{
					"api-token": {Name: "api-token", External: true},
				},
			},
			wantErr: `generates Zarf variable "API_TOKEN_SECRET_NAME", which conflicts with compose secret "api-token"`,
		},
		{
			name: "cross-service variable collision",
			app: model.App{
				Package: model.Package{Name: "shop", Namespace: "shop", Version: "0.1.0"},
				Services: []model.Service{
					{
						Name: "api", Image: "ghcr.io/acme/api:1.0.0",
						Env: []model.EnvVar{{Name: "WORKER_LOG_LEVEL", Value: "info"}},
					},
					{
						Name: "api-worker", Image: "ghcr.io/acme/worker:1.0.0",
						Env: []model.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}},
					},
				},
			},
			wantErr: `generates Zarf variable "API_WORKER_LOG_LEVEL", which conflicts with environment variable "WORKER_LOG_LEVEL" on service "api"`,
		},
		{
			name: "reserved environment variable collision",
			app: model.App{
				Package: model.Package{Name: "shop", Namespace: "shop", Version: "0.1.0"},
				Services: []model.Service{{
					Name: "additional", Image: "ghcr.io/acme/api:1.0.0",
					Env: []model.EnvVar{{Name: "NETWORK_ALLOW", Value: "custom"}},
				}},
			},
			wantErr: `generates Zarf variable "ADDITIONAL_NETWORK_ALLOW", which conflicts with automatic package variable "ADDITIONAL_NETWORK_ALLOW"`,
		},
		{
			name: "reserved secret variable collision",
			app: model.App{
				Package:  model.Package{Name: "shop", Namespace: "shop", Version: "0.1.0"},
				Services: []model.Service{{Name: "api", Image: "ghcr.io/acme/api:1.0.0"}},
				Secrets: map[string]model.Secret{
					"additional-network-allow": {Name: "additional-network-allow"},
				},
			},
			wantErr: `compose secret "additional-network-allow" generates Zarf variable "ADDITIONAL_NETWORK_ALLOW", which conflicts with automatic package variable "ADDITIONAL_NETWORK_ALLOW"`,
		},
		{
			name: "domain secret variable collision",
			app: model.App{
				Package:  model.Package{Name: "shop", Namespace: "shop", Version: "0.1.0"},
				Services: []model.Service{{Name: "api", Image: "ghcr.io/acme/api:1.0.0"}},
				Secrets: map[string]model.Secret{
					"domain": {Name: "domain"},
				},
			},
			wantErr: `compose secret "domain" generates Zarf variable "DOMAIN", which conflicts with automatic package variable "DOMAIN"`,
		},
		{
			name: "compose config map collision",
			app: model.App{
				Package: model.Package{Name: "shop", Namespace: "shop", Version: "0.1.0"},
				Services: []model.Service{{
					Name: "api", Image: "ghcr.io/acme/api:1.0.0",
					Env: []model.EnvVar{{Name: "LOG_LEVEL", Value: "info"}},
				}},
				Configs: map[string]model.Config{
					"api-environment": {Name: "api-environment", Content: "config"},
				},
			},
			wantErr: `environment ConfigMap "api-environment" for service "api" conflicts with compose config "api-environment"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := render.WritePackage(t.TempDir(), tt.app)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("WritePackage() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func firstExposeRule(t *testing.T, path string) map[string]any {
	t.Helper()
	udsPackage := readUDSPackageYAMLMap(t, path)
	spec := mustMap(t, udsPackage["spec"])
	network := mustMap(t, spec["network"])
	exposes, ok := network["expose"].([]any)
	if !ok || len(exposes) == 0 {
		t.Fatalf("expected at least one expose rule in %s, got %#v", path, network["expose"])
	}
	return mustMap(t, exposes[0])
}

func readUDSPackageYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	content := readFile(t, path)
	templateBlock := strings.Join([]string{
		"            {{- with .Values.additionalNetworkAllow }}",
		"            {{ toYaml . | nindent 12 }}",
		"            {{- end }}",
	}, "\n")
	if !strings.Contains(content, templateBlock) {
		t.Fatalf("expected additional network allow template in %s\n%s", path, content)
	}
	content = strings.Replace(content, templateBlock, "", 1)
	var out map[string]any
	if err := yamlv3.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("unmarshal Helm-stripped yaml %s: %v", path, err)
	}
	return out
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
