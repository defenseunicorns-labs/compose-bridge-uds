package main

import (
	"os"
	"testing"

	"defenseunicorns/uds-compose-bridge/internal/model"
	yamlv3 "gopkg.in/yaml.v3"
)

func TestWriteBridgeOverlayIncludesOnlyBuildServices(t *testing.T) {
	t.Parallel()

	app := model.App{Services: []model.Service{
		{
			Name: "api-service",
			Build: &model.BuildDefinition{
				ComposeService: "api_service",
				Target:         "api-service",
			},
		},
		{Name: "cache", Image: "redis:7-alpine"},
	}}
	path, err := writeBridgeOverlay(t.TempDir(), app, "compose-bridge-uds:test")
	if err != nil {
		t.Fatalf("writeBridgeOverlay() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	var overlay map[string]any
	if err := yamlv3.Unmarshal(data, &overlay); err != nil {
		t.Fatalf("decode overlay: %v", err)
	}
	services, ok := overlay["services"].(map[string]any)
	if !ok {
		t.Fatalf("expected services map, got %#v", overlay["services"])
	}
	if len(services) != 1 {
		t.Fatalf("expected one placeholder service, got %#v", services)
	}
	api, ok := services["api_service"].(map[string]any)
	if !ok || api["image"] != "compose-bridge-uds:test" {
		t.Fatalf("expected original Compose service key and transformation image, got %#v", services)
	}
}

func TestWriteBridgeOverlayOmitsFileWithoutBuildServices(t *testing.T) {
	t.Parallel()

	path, err := writeBridgeOverlay(t.TempDir(), model.App{Services: []model.Service{{
		Name:  "cache",
		Image: "redis:7-alpine",
	}}}, "compose-bridge-uds:test")
	if err != nil {
		t.Fatalf("writeBridgeOverlay() error = %v", err)
	}
	if path != "" {
		t.Fatalf("expected no overlay, got %q", path)
	}
}
