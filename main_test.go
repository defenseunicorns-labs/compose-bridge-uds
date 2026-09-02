package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"defenseunicorns/uds-compose-bridge/internal/model"
)

func TestRunWritesConversionReports(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "compose.yaml")
	outputPath := filepath.Join(dir, "out")
	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.2.3
    container_name: custom-api
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
`)
	if err := os.WriteFile(inputPath, input, 0o644); err != nil {
		t.Fatalf("write Compose input: %v", err)
	}

	if err := run(inputPath, outputPath); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	report := readConversionReport(t, filepath.Join(outputPath, "conversion.json"))
	assertDecisionPath(t, report.Translated, "services.api.image")
	assertDecisionPath(t, report.Inferred, "x-uds.metadata.name")
	assertDecisionPath(t, report.Inferred, "x-uds.metadata.version")
	assertDecisionPath(t, report.Ignored, "services.api.container_name")
	assertDecisionPath(t, report.Ignored, "services.api.volumes[0]")
	for _, decision := range report.Inferred {
		if !strings.HasPrefix(decision.Path, "services.") &&
			!strings.HasPrefix(decision.Path, "networks.") &&
			!strings.HasPrefix(decision.Path, "x-uds.") {
			t.Fatalf("inferred path %q is not part of the Compose or x-uds configuration shape", decision.Path)
		}
		if decision.Path == "x-uds.metadata.name" && !strings.Contains(decision.Message, "Kubernetes namespace") {
			t.Fatalf("inferred package name does not explain namespace derivation: %#v", decision)
		}
	}
	if len(report.Rejected) != 0 {
		t.Fatalf("rejected decisions = %#v, want none", report.Rejected)
	}
	zarfConfig, err := os.ReadFile(filepath.Join(outputPath, "zarf.yaml"))
	if err != nil {
		t.Fatalf("read zarf.yaml: %v", err)
	}
	if want := "conversion: docs/conversion.md"; !strings.Contains(string(zarfConfig), want) {
		t.Fatalf("zarf.yaml does not contain %q\n%s", want, zarfConfig)
	}
	if unexpected := "conversion-json:"; strings.Contains(string(zarfConfig), unexpected) {
		t.Fatalf("zarf.yaml unexpectedly contains %q\n%s", unexpected, zarfConfig)
	}

	markdown, err := os.ReadFile(filepath.Join(outputPath, "docs", "conversion.md"))
	if err != nil {
		t.Fatalf("read Markdown conversion report: %v", err)
	}
	for _, heading := range []string{
		"## Translated settings",
		"## Inferred settings",
		"## Ignored settings",
		"## Rejected settings",
	} {
		if !strings.Contains(string(markdown), heading) {
			t.Fatalf("conversion.md does not contain %q\n%s", heading, markdown)
		}
	}
}

func TestRunWritesRejectedSettingsBeforeReturningConversionError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "compose.yaml")
	outputPath := filepath.Join(dir, "out")
	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.2.3
    network_mode: host
`)
	if err := os.WriteFile(inputPath, input, 0o644); err != nil {
		t.Fatalf("write Compose input: %v", err)
	}

	err := run(inputPath, outputPath)
	if err == nil {
		t.Fatal("run() error = nil, want compatibility error")
	}
	report := readConversionReport(t, filepath.Join(outputPath, "conversion.json"))
	if len(report.Rejected) != 1 {
		t.Fatalf("rejected decisions = %#v, want one", report.Rejected)
	}
	rejected := report.Rejected[0]
	if rejected.Path != "services.api.network_mode" || rejected.Code != "service-field" {
		t.Fatalf("rejected decision = %#v", rejected)
	}
	if rejected.Message == "" || rejected.Remediation == "" {
		t.Fatalf("rejected decision must include a message and remediation: %#v", rejected)
	}

	markdown, readErr := os.ReadFile(filepath.Join(outputPath, "docs", "conversion.md"))
	if readErr != nil {
		t.Fatalf("read Markdown conversion report: %v", readErr)
	}
	for _, want := range []string{"services.api.network_mode", "ordinary Compose service networking"} {
		if !strings.Contains(string(markdown), want) {
			t.Fatalf("conversion.md does not contain %q\n%s", want, markdown)
		}
	}
	if _, statErr := os.Stat(filepath.Join(outputPath, "zarf.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("zarf.yaml should not be generated after rejected settings; stat error = %v", statErr)
	}
}

func readConversionReport(t *testing.T, path string) model.ConversionReport {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read conversion report: %v", err)
	}
	var report model.ConversionReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode conversion report: %v", err)
	}
	return report
}

func assertDecisionPath(t *testing.T, decisions []model.ConversionDecision, path string) {
	t.Helper()
	for _, decision := range decisions {
		if decision.Path == path {
			return
		}
	}
	t.Fatalf("decision path %q not found in %#v", path, decisions)
}
