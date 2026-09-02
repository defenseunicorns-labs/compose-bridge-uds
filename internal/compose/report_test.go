package compose

import (
	"os"
	"path/filepath"
	"testing"

	"defenseunicorns/uds-compose-bridge/internal/model"
)

func TestConvertCanonicalYAMLReportsStructuredInvalidUDSSetting(t *testing.T) {
	t.Parallel()

	conversion, err := ConvertCanonicalYAML([]byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.2.3
x-uds:
  metadata:
    version:
      bad: value
`))
	if err == nil {
		t.Fatal("ConvertCanonicalYAML() error = nil, want invalid x-uds.metadata.version")
	}

	rejected := requireDecision(t, conversion.Report.Rejected, "x-uds.metadata.version")
	if rejected.Code != "invalid-setting" {
		t.Fatalf("rejected code = %q, want invalid-setting", rejected.Code)
	}
	if rejected.Remediation == "" {
		t.Fatalf("rejected remediation = %q, want non-empty remediation", rejected.Remediation)
	}
	if hasDecision(conversion.Report.Translated, "x-uds.metadata.version") {
		t.Fatalf("translated decisions unexpectedly include rejected path: %#v", conversion.Report.Translated)
	}
	if hasDecision(conversion.Report.Rejected, "compose") {
		t.Fatalf("rejected decisions unexpectedly include generic compose failure: %#v", conversion.Report.Rejected)
	}
}

func TestConvertCanonicalFileAlignsReportWithConsumedSettings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	if err := os.WriteFile(inputPath, []byte(`name: demo
services:
  my_api:
    build:
      context: .
    image: ghcr.io/acme/api:1.2.3
    deploy:
      resources:
        limits:
          cpus: "0.50"
          memory: 512M
          pids: 50
        reservations:
          cpus: "0.25"
          memory: 256M
        devices:
          - capabilities: ["gpu"]
x-uds:
  metadata:
    name: uds-package
    version: 1.2.3
    description: ignored
  spec:
    foo: ignored
    network:
      expose:
        - service: my_api
      foo: ignored
    caBundle:
      configMap:
        name: bundle
        key: ca.pem
`), 0o644); err != nil {
		t.Fatalf("write compose.yaml: %v", err)
	}

	conversion, err := ConvertCanonicalFile(inputPath)
	if err != nil {
		t.Fatalf("ConvertCanonicalFile() error = %v", err)
	}

	requireDecision(t, conversion.Report.Ignored, "name")
	requireDecision(t, conversion.Report.Ignored, "services.my_api.image")
	imageDecision := requireDecision(t, conversion.Report.Inferred, "services.my_api.image")
	if imageDecision.Value != "zarf.internal/uds-package-my-api:1.2.3-uds.0" {
		t.Fatalf("inferred image value = %q", imageDecision.Value)
	}
	if hasDecision(conversion.Report.Inferred, "services.my-api.image") {
		t.Fatalf("inferred decisions unexpectedly use normalized raw service path: %#v", conversion.Report.Inferred)
	}
	if hasDecision(conversion.Report.Translated, "services.my_api.image") {
		t.Fatalf("translated decisions unexpectedly include overridden build image: %#v", conversion.Report.Translated)
	}

	for _, path := range []string{
		"services.my_api.deploy.resources.limits.cpus",
		"services.my_api.deploy.resources.limits.memory",
		"services.my_api.deploy.resources.reservations.cpus",
		"services.my_api.deploy.resources.reservations.memory",
		"x-uds.metadata.name",
		"x-uds.metadata.version",
		"x-uds.spec.network.expose",
		"x-uds.spec.caBundle",
	} {
		requireDecision(t, conversion.Report.Translated, path)
	}
	if hasDecision(conversion.Report.Translated, "services.my_api.deploy.resources") {
		t.Fatalf("translated decisions unexpectedly include broad deploy.resources entry: %#v", conversion.Report.Translated)
	}

	for _, path := range []string{
		"services.my_api.deploy.resources.limits.pids",
		"services.my_api.deploy.resources.devices",
		"x-uds.metadata.description",
		"x-uds.spec.foo",
		"x-uds.spec.network.foo",
	} {
		requireDecision(t, conversion.Report.Ignored, path)
	}
}

func hasDecision(decisions []model.ConversionDecision, path string) bool {
	for _, decision := range decisions {
		if decision.Path == path {
			return true
		}
	}
	return false
}

func requireDecision(t *testing.T, decisions []model.ConversionDecision, path string) model.ConversionDecision {
	t.Helper()
	for _, decision := range decisions {
		if decision.Path == path {
			return decision
		}
	}
	t.Fatalf("decision path %q not found in %#v", path, decisions)
	return model.ConversionDecision{}
}
