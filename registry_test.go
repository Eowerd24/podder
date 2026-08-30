package main

import (
	"os"
	"path/filepath"
	"testing"
)

const cleanRegistryV1 = `
version: 1

ports:
  - id: rig9-open-webui
    service: open-webui
    node: rig9
    protocol: tcp
    application_protocol: http
    listener:
      address: 127.0.0.1
      port: 3000
    container:
      port: 8080
    scope: loopback
    class: application
    state: active
    verification: confirmed
    purpose: Open WebUI local frontend

  - id: rig9-flowise-web
    service: flowise
    node: rig9
    protocol: tcp
    application_protocol: http
    listener:
      address: 127.0.0.1
      port: 3100
    container:
      port: 3000
    scope: loopback
    class: application
    state: active
    verification: confirmed
    purpose: Flowise local frontend

  - id: witness1-relp
    service: witness1-relp
    provider: witness1
    protocol: tcp
    application_protocol: relp
    listener:
      port: 2514
    scope: lan
    class: observability
    state: reserved
    verification: confirmed
    purpose: RELP ingestion reservation

  - id: planned-service
    service: analytics
    protocol: tcp
    listener:
      address: 127.0.0.1
      port: 8899
    scope: loopback
    state: planned
    purpose: Future local analytics service
`

const malformedRegistryYAML = `
version: 1
ports:
  - - id: invalid-double-dash
    service: broken
    listener
      port: 9999
`

func TestParseRegistryYAML_Clean(t *testing.T) {
	result := ParseRegistryYAML([]byte(cleanRegistryV1), "/dummy/ports.yaml")
	if !result.Loaded {
		t.Fatalf("expected registry to load successfully, got error: %s", result.Error)
	}

	if result.Version != 1 {
		t.Errorf("expected version 1, got %d", result.Version)
	}

	if len(result.Ports) != 4 {
		t.Fatalf("expected 4 ports, got %d", len(result.Ports))
	}

	p1 := result.Ports[0]
	if p1.ID != "rig9-open-webui" || p1.Service != "open-webui" || p1.Listener.Port != 3000 || p1.Container.Port != 8080 {
		t.Errorf("unexpected port 0: %+v", p1)
	}

	p3 := result.Ports[2]
	if p3.ID != "witness1-relp" || p3.State != "reserved" || p3.Listener.Port != 2514 {
		t.Errorf("unexpected port 2 (reserved): %+v", p3)
	}

	p4 := result.Ports[3]
	if p4.ID != "planned-service" || p4.State != "planned" {
		t.Errorf("unexpected port 3 (planned): %+v", p4)
	}
}

func TestParseRegistryYAML_Malformed(t *testing.T) {
	result := ParseRegistryYAML([]byte(malformedRegistryYAML), "/dummy/malformed.yaml")
	if result.Loaded {
		t.Fatal("expected malformed registry to fail loading")
	}

	if result.Error == "" {
		t.Fatal("expected descriptive error message for malformed registry")
	}
}

func TestParseRegistryYAML_Empty(t *testing.T) {
	result := ParseRegistryYAML([]byte(""), "/dummy/empty.yaml")
	if result.Loaded {
		t.Fatal("expected empty registry to fail loading")
	}
}

func TestSettingsPersistence(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{}

	// Test default settings
	settings, err := service.GetSettings()
	if err != nil {
		t.Fatalf("failed to get default settings: %v", err)
	}
	if settings.PortRegistry.Enabled {
		t.Error("expected default portRegistry.enabled to be false")
	}

	// Test saving settings
	newSettings := AppSettings{
		PortRegistry: PortRegistryConfig{
			Enabled: true,
			Path:    filepath.Join(tempDir, "ports.yaml"),
		},
	}
	if err := service.SaveSettings(newSettings); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	// Test reading saved settings
	readBack, err := service.GetSettings()
	if err != nil {
		t.Fatalf("failed to read back saved settings: %v", err)
	}
	if !readBack.PortRegistry.Enabled || readBack.PortRegistry.Path != filepath.Join(tempDir, "ports.yaml") {
		t.Errorf("saved settings mismatch: %+v", readBack)
	}
}

func TestRegistryReservationConflict(t *testing.T) {
	tempDir := t.TempDir()
	registryPath := filepath.Join(tempDir, "ports.yaml")
	if err := os.WriteFile(registryPath, []byte(cleanRegistryV1), 0o644); err != nil {
		t.Fatalf("failed to write fixture registry: %v", err)
	}

	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{}
	if err := service.SaveSettings(AppSettings{
		PortRegistry: PortRegistryConfig{
			Enabled: true,
			Path:    registryPath,
		},
	}); err != nil {
		t.Fatalf("failed to save registry settings: %v", err)
	}

	// Validate attempting to bind to reserved port 2514
	validation, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP:        "0.0.0.0",
		HostPort:      2514,
		ContainerPort: 2514,
		Protocol:      "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error validating mapping: %v", err)
	}

	if validation.Valid {
		t.Error("expected validation to fail for reserved port 2514")
	}

	foundReservationCheck := false
	for _, check := range validation.Checks {
		if !check.Passed && check.Name == "Registry Reservation" {
			foundReservationCheck = true
			break
		}
	}
	if !foundReservationCheck {
		t.Errorf("expected Registry Reservation failure check in: %+v", validation.Checks)
	}
}

func TestReconciliationStates(t *testing.T) {
	tempDir := t.TempDir()
	registryPath := filepath.Join(tempDir, "ports.yaml")
	if err := os.WriteFile(registryPath, []byte(cleanRegistryV1), 0o644); err != nil {
		t.Fatalf("failed to write fixture registry: %v", err)
	}

	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{}
	if err := service.SaveSettings(AppSettings{
		PortRegistry: PortRegistryConfig{
			Enabled: true,
			Path:    registryPath,
		},
	}); err != nil {
		t.Fatalf("failed to save registry settings: %v", err)
	}

	overview, err := service.GetPortOverview()
	if err != nil {
		t.Fatalf("failed to get port overview: %v", err)
	}

	if !overview.Summary.RegistryLoaded {
		t.Errorf("expected registry to be marked as loaded")
	}

	// Unmatched declared entries should be present
	foundMissing := false
	foundReserved := false
	foundPlanned := false

	for _, item := range overview.Items {
		if item.RegistryID == "rig9-flowise-web" && item.ReconciliationStatus == "DECLARED_MISSING" {
			foundMissing = true
		}
		if item.RegistryID == "witness1-relp" && (item.ReconciliationStatus == "RESERVED_FREE" || item.ReconciliationStatus == "RESERVED_IN_USE") {
			foundReserved = true
		}
		if item.RegistryID == "planned-service" && item.ReconciliationStatus == "PLANNED" {
			foundPlanned = true
		}
	}

	if !foundMissing {
		t.Errorf("expected DECLARED_MISSING item for flowise in overview: %+v", overview.Items)
	}
	if !foundReserved {
		t.Errorf("expected RESERVED item for witness1-relp in overview: %+v", overview.Items)
	}
	if !foundPlanned {
		t.Errorf("expected PLANNED item for planned-service in overview: %+v", overview.Items)
	}
}

