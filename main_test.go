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

func TestRunWritesStructuredRenderFailureForInvalidMonitorSetting(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "compose.yaml")
	outputPath := filepath.Join(dir, "out")
	input := []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.2.3
    ports:
      - "8080:8080"
x-uds:
  spec:
    monitor:
      - service: missing
`)
	if err := os.WriteFile(inputPath, input, 0o644); err != nil {
		t.Fatalf("write Compose input: %v", err)
	}

	err := run(inputPath, outputPath)
	if err == nil {
		t.Fatal("run() error = nil, want monitor validation error")
	}

	report := readConversionReport(t, filepath.Join(outputPath, "conversion.json"))
	assertDecisionPath(t, report.Rejected, "x-uds.spec.monitor")
	for _, decision := range report.Rejected {
		if decision.Path != "x-uds.spec.monitor" {
			continue
		}
		if decision.Code != "invalid-setting" || decision.Remediation == "" {
			t.Fatalf("unexpected rejected decision = %#v", decision)
		}
	}
	for _, decision := range report.Translated {
		if decision.Path == "x-uds.spec.monitor" {
			t.Fatalf("translated decisions unexpectedly include invalid monitor path: %#v", report.Translated)
		}
	}
}

func TestRunWritesStructuredSourceDecisionsForRenderFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantPath string
		wantCode string
	}{
		{
			name: "duplicate port name",
			input: `name: demo
services:
  api:
    image: ghcr.io/acme/api:1.2.3
    ports:
      - name: web_api
        target: 8080
        published: "8080"
      - name: web-api
        target: 9090
        published: "9090"
`,
			wantPath: "services.api.ports",
			wantCode: "duplicate-port-name",
		},
		{
			name: "invalid environment name preserves raw service path",
			input: `name: demo
services:
  my_api:
    image: ghcr.io/acme/api:1.2.3
    environment:
      DATABASE/URL: postgres://db
`,
			wantPath: "services.my_api.environment",
			wantCode: "environment-name",
		},
		{
			name: "generated variable collision",
			input: `name: demo
services:
  api:
    image: ghcr.io/acme/api:1.2.3
    environment:
      CPU_LIMIT: custom
`,
			wantPath: "services.api.environment",
			wantCode: "zarf-variable-conflict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			inputPath := filepath.Join(dir, "compose.yaml")
			outputPath := filepath.Join(dir, "out")
			if err := os.WriteFile(inputPath, []byte(tt.input), 0o644); err != nil {
				t.Fatalf("write Compose input: %v", err)
			}

			if err := run(inputPath, outputPath); err == nil {
				t.Fatal("run() error = nil, want render validation error")
			}
			report := readConversionReport(t, filepath.Join(outputPath, "conversion.json"))
			decision := findDecision(t, report.Rejected, tt.wantPath)
			if decision.Code != tt.wantCode || decision.Remediation == "" {
				t.Fatalf("rejected decision = %#v, want code %q with remediation", decision, tt.wantCode)
			}
			for _, translated := range report.Translated {
				if translated.Path == tt.wantPath {
					t.Fatalf("translated decisions unexpectedly include rejected path: %#v", report.Translated)
				}
			}
		})
	}
}

func TestRunWritesFallbackPackageGenerationRemediation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "compose.yaml")
	outputPath := filepath.Join(dir, "out")
	if err := os.WriteFile(inputPath, []byte(`name: demo
services:
  api:
    image: ghcr.io/acme/api:1.2.3
`), 0o644); err != nil {
		t.Fatalf("write Compose input: %v", err)
	}
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputPath, "chart"), []byte("not-a-directory"), 0o644); err != nil {
		t.Fatalf("create chart file: %v", err)
	}

	if err := run(inputPath, outputPath); err == nil {
		t.Fatal("run() error = nil, want package generation error")
	}

	report := readConversionReport(t, filepath.Join(outputPath, "conversion.json"))
	decision := findDecision(t, report.Rejected, "package")
	if decision.Code != "package-generation" || decision.Remediation == "" {
		t.Fatalf("fallback rejected decision = %#v, want package-generation with remediation", decision)
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

func findDecision(t *testing.T, decisions []model.ConversionDecision, path string) model.ConversionDecision {
	t.Helper()
	for _, decision := range decisions {
		if decision.Path == path {
			return decision
		}
	}
	t.Fatalf("decision path %q not found in %#v", path, decisions)
	return model.ConversionDecision{}
}
