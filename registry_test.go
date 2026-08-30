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

	// Never depend on whatever real podman/ss happens to be installed (or
	// not) on the machine running the tests — inject an empty fake runner.
	service := &PodmanService{runner: newFakeCommandRunner()}
	if err := service.SaveSettings(AppSettings{
		PortRegistry: PortRegistryConfig{
			Enabled: true,
			Path:    registryPath,
		},
		// The fixture's node-scoped entries (rig9-*) are only evaluated as
		// local once the local node identity matches.
		LocalNode: "rig9",
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

const multiNodeRegistryV1 = `
version: 1

ports:
  - id: rack1-service
    service: rack1-svc
    node: rack1
    protocol: tcp
    listener:
      address: 0.0.0.0
      port: 4000
    scope: lan
    state: active
    purpose: A service that runs on a different node entirely

  - id: local-service
    service: local-svc
    node: rig9
    protocol: tcp
    listener:
      address: 127.0.0.1
      port: 4100
    scope: loopback
    state: active
    purpose: A service that runs on this node
`

func TestReconciliation_RemoteNodeNeverReportedAsLocallyMissing(t *testing.T) {
	tempDir := t.TempDir()
	registryPath := filepath.Join(tempDir, "ports.yaml")
	if err := os.WriteFile(registryPath, []byte(multiNodeRegistryV1), 0o644); err != nil {
		t.Fatalf("failed to write fixture registry: %v", err)
	}

	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{runner: newFakeCommandRunner()}
	if err := service.SaveSettings(AppSettings{
		PortRegistry: PortRegistryConfig{Enabled: true, Path: registryPath},
		LocalNode:    "rig9",
	}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	overview, err := service.GetPortOverview()
	if err != nil {
		t.Fatalf("failed to get port overview: %v", err)
	}

	if overview.Summary.LocalNode != "rig9" {
		t.Errorf("expected summary to report the configured local node, got %q", overview.Summary.LocalNode)
	}

	var rack1Status, localStatus string
	for _, item := range overview.Items {
		if item.RegistryID == "rack1-service" {
			rack1Status = item.ReconciliationStatus
		}
		if item.RegistryID == "local-service" {
			localStatus = item.ReconciliationStatus
		}
	}

	if rack1Status != "REMOTE" {
		t.Errorf("expected the rack1-scoped entry to be classified REMOTE, not %q — a remote node's declaration must never be reported as locally missing", rack1Status)
	}
	if localStatus != "DECLARED_MISSING" {
		t.Errorf("expected the rig9-scoped entry (no running container) to be DECLARED_MISSING, got %q", localStatus)
	}
	if overview.Summary.RegistryMissing != 1 {
		t.Errorf("expected exactly 1 locally-missing entry (the remote one must not count), got %d", overview.Summary.RegistryMissing)
	}
	if overview.Summary.RegistryRemote != 1 {
		t.Errorf("expected exactly 1 remote entry counted, got %d", overview.Summary.RegistryRemote)
	}
}

func TestReconciliation_RemoteReservationDoesNotBlockLocalAllocation(t *testing.T) {
	tempDir := t.TempDir()
	registryPath := filepath.Join(tempDir, "ports.yaml")
	reservedElsewhere := `
version: 1
ports:
  - id: remote-reservation
    service: remote-svc
    node: rack1
    protocol: tcp
    listener:
      address: 0.0.0.0
      port: 7000
    scope: lan
    state: reserved
    purpose: Reserved on a different node
`
	if err := os.WriteFile(registryPath, []byte(reservedElsewhere), 0o644); err != nil {
		t.Fatalf("failed to write fixture registry: %v", err)
	}

	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{runner: newFakeCommandRunner()}
	if err := service.SaveSettings(AppSettings{
		PortRegistry: PortRegistryConfig{Enabled: true, Path: registryPath},
		LocalNode:    "rig9",
	}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	validation, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP:        "0.0.0.0",
		HostPort:      7000,
		ContainerPort: 7000,
		Protocol:      "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !validation.Valid {
		t.Errorf("expected a reservation scoped to a different node to NOT block local allocation, got: %+v", validation.Checks)
	}
}

func TestActiveRegistryDeclarationDoesNotBlockOwnService(t *testing.T) {
	tempDir := t.TempDir()
	registryPath := filepath.Join(tempDir, "ports.yaml")
	if err := os.WriteFile(registryPath, []byte(cleanRegistryV1), 0o644); err != nil {
		t.Fatalf("failed to write fixture registry: %v", err)
	}

	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{runner: newFakeCommandRunner()}
	if err := service.SaveSettings(AppSettings{
		PortRegistry: PortRegistryConfig{Enabled: true, Path: registryPath},
		LocalNode:    "rig9",
	}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	// rig9-open-webui is declared "active" at 127.0.0.1:3000. Deploying the
	// very service that owns that declaration (or redeploying it) must not
	// be blocked merely because the registry says it's "active" — active
	// is a declaration of intent, not a confirmed open socket.
	validation, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP:        "127.0.0.1",
		HostPort:      3000,
		ContainerPort: 8080,
		Protocol:      "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !validation.Valid {
		t.Errorf("expected an 'active' registry declaration to not block allocation, got: %+v", validation.Checks)
	}
}

func TestRegistryNotesAcceptsListAndScalar(t *testing.T) {
	listForm := `
version: 1
ports:
  - id: a
    service: a
    protocol: tcp
    listener:
      port: 80
    state: active
    notes:
      - first note
      - second note
`
	scalarForm := `
version: 1
ports:
  - id: a
    service: a
    protocol: tcp
    listener:
      port: 80
    state: active
    notes: a single scalar note
`
	r1 := ParseRegistryYAML([]byte(listForm), "/dummy.yaml")
	if !r1.Loaded || len(r1.Ports) != 1 || len(r1.Ports[0].Notes) != 2 {
		t.Fatalf("expected list-form notes to parse as 2 entries: %+v", r1)
	}

	r2 := ParseRegistryYAML([]byte(scalarForm), "/dummy.yaml")
	if !r2.Loaded || len(r2.Ports) != 1 || len(r2.Ports[0].Notes) != 1 || r2.Ports[0].Notes[0] != "a single scalar note" {
		t.Fatalf("expected scalar-form notes to parse as 1 entry: %+v", r2)
	}
}

func TestRegistryUnsupportedVersionRejected(t *testing.T) {
	r := ParseRegistryYAML([]byte("version: 99\nports: []\n"), "/dummy.yaml")
	if r.Loaded {
		t.Fatalf("expected an unsupported schema version to fail loading")
	}
	if r.Error == "" {
		t.Errorf("expected a descriptive error for the unsupported version")
	}
}

func TestRegistryDuplicateIDsAreDroppedWithWarning(t *testing.T) {
	dup := `
version: 1
ports:
  - id: dup
    service: a
    protocol: tcp
    listener:
      port: 80
    state: active
  - id: dup
    service: b
    protocol: tcp
    listener:
      port: 81
    state: active
`
	r := ParseRegistryYAML([]byte(dup), "/dummy.yaml")
	if !r.Loaded {
		t.Fatalf("expected registry to still load despite one duplicate entry")
	}
	if len(r.Ports) != 1 {
		t.Fatalf("expected only the first of the duplicate IDs to be kept, got %d entries", len(r.Ports))
	}
	if len(r.Warnings) == 0 {
		t.Errorf("expected a warning about the dropped duplicate")
	}
}

func TestRegistryInvalidProtocolAndStateDropped(t *testing.T) {
	bad := `
version: 1
ports:
  - id: bad-proto
    service: a
    protocol: sctp
    listener:
      port: 80
    state: active
  - id: bad-state
    service: b
    protocol: tcp
    listener:
      port: 81
    state: quantum
  - id: missing-port
    service: c
    protocol: tcp
    state: active
  - id: good
    service: d
    protocol: tcp
    listener:
      port: 82
    state: active
`
	r := ParseRegistryYAML([]byte(bad), "/dummy.yaml")
	if !r.Loaded {
		t.Fatalf("expected registry to still load with only the good entry kept")
	}
	if len(r.Ports) != 1 || r.Ports[0].ID != "good" {
		t.Fatalf("expected only the 'good' entry to survive validation, got: %+v", r.Ports)
	}
	if len(r.Warnings) != 3 {
		t.Errorf("expected 3 warnings (one per dropped entry), got %d: %v", len(r.Warnings), r.Warnings)
	}
}

func TestGetLocalNodeDefaultsToHostname(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{}
	node, err := service.GetLocalNode()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node == "" {
		t.Errorf("expected a non-empty default local node (hostname fallback)")
	}

	if err := service.SaveSettings(AppSettings{LocalNode: "explicit-override"}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}
	node, err = service.GetLocalNode()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node != "explicit-override" {
		t.Errorf("expected explicit LocalNode override to take precedence, got %q", node)
	}
}
