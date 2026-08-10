package render_test

import (
	"io"
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
	if !strings.Contains(err.Error(), "[build-image-unresolved]") {
		t.Fatalf("expected build image validation error, got %v", err)
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

	udsPackage := readYAMLMap(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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

	udsPackage := readYAMLMap(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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
	if !strings.Contains(configMap, "key: value") {
		t.Fatalf("expected configmap to contain rendered config content")
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

	service := readFile(t, filepath.Join(outDir, "chart", "templates", "service-web.yaml"))
	for _, want := range []string{
		"name: web-ui",
		"name: port-9090-tcp",
	} {
		if !strings.Contains(service, want) {
			t.Fatalf("expected service port name %q\n%s", want, service)
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

	udsPackage := readFile(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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

	udsPackage := readYAMLMap(t, filepath.Join(outDir, "chart", "templates", "uds-package.yaml"))
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
	// No secrets: no Zarf values file and no valuesFiles reference.
	if _, err := os.Stat(filepath.Join(outDir, "values")); !os.IsNotExist(err) {
		t.Fatalf("did not expect values/ directory for a package without secrets")
	}
	if strings.Contains(zarfConfig, "valuesFiles") {
		t.Fatalf("did not expect valuesFiles for a package without secrets\n%s", zarfConfig)
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
		"sensitive: true",
	} {
		if !strings.Contains(zarfConfig, want) {
			t.Fatalf("expected zarf.yaml to contain %q\n%s", want, zarfConfig)
		}
	}
}

func firstExposeRule(t *testing.T, path string) map[string]any {
	t.Helper()
	udsPackage := readYAMLMap(t, path)
	spec := mustMap(t, udsPackage["spec"])
	network := mustMap(t, spec["network"])
	exposes, ok := network["expose"].([]any)
	if !ok || len(exposes) == 0 {
		t.Fatalf("expected at least one expose rule in %s, got %#v", path, network["expose"])
	}
	return mustMap(t, exposes[0])
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
