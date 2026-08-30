package main

import (
	"os"
	"testing"
)

func TestSpecsCRUD(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{}

	// 1. Initial list should be empty
	specs, err := service.ListSpecs()
	if err != nil {
		t.Fatalf("unexpected error listing specs: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected 0 specs, got %d", len(specs))
	}

	// 2. Save spec
	spec1 := ContainerSpec{
		Name:  "my-app",
		Image: "docker.io/library/nginx:alpine",
		PortMappings: []PortMapping{
			{
				HostIP:        "127.0.0.1",
				HostPort:      8080,
				ContainerPort: 80,
				Protocol:      "tcp",
			},
		},
		Binds: []BindMountSpec{
			{
				HostPath:      "/tmp/html",
				ContainerPath: "/usr/share/nginx/html",
				ReadOnly:      true,
			},
		},
		Command: []string{"nginx", "-g", "daemon off;"},
	}

	if err := service.SaveSpec(spec1); err != nil {
		t.Fatalf("failed to save spec: %v", err)
	}

	// 3. Get spec
	loaded, err := service.GetSpec("my-app")
	if err != nil {
		t.Fatalf("failed to get spec: %v", err)
	}
	if loaded.Name != "my-app" || loaded.Image != "docker.io/library/nginx:alpine" {
		t.Errorf("unexpected loaded spec: %+v", loaded)
	}
	if len(loaded.PortMappings) != 1 || loaded.PortMappings[0].HostPort != 8080 {
		t.Errorf("unexpected port mapping in loaded spec: %+v", loaded.PortMappings)
	}
	if len(loaded.Binds) != 1 || loaded.Binds[0].HostPath != "/tmp/html" {
		t.Errorf("unexpected binds in loaded spec: %+v", loaded.Binds)
	}

	// 4. List specs should return 1
	specs, err = service.ListSpecs()
	if err != nil {
		t.Fatalf("failed to list specs: %v", err)
	}
	if len(specs) != 1 {
		t.Errorf("expected 1 spec, got %d", len(specs))
	}

	// 5. Delete spec
	if err := service.DeleteSpec("my-app"); err != nil {
		t.Fatalf("failed to delete spec: %v", err)
	}

	// 6. Get deleted spec should error
	_, err = service.GetSpec("my-app")
	if err == nil {
		t.Errorf("expected error getting deleted spec, got nil")
	}
}

func TestLegacySpecWithoutSchemaVersionMigratesToManaged(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	// Simulate a spec file written by the pre-hardening prototype: no
	// schemaVersion, no managed field at all (both post-date this pass).
	// Every such file was, in practice, written for a container that DID
	// carry io.podder.managed=true — the prototype applied that label
	// unconditionally.
	legacyJSON := `{
		"name": "legacy-app",
		"image": "docker.io/library/nginx:alpine",
		"portMappings": [{"hostIP":"127.0.0.1","hostPort":8080,"containerPort":80,"protocol":"tcp"}],
		"command": "nginx -g \"daemon off;\""
	}`

	svc := &PodmanService{}
	servicesDir := getServicesDir()
	if err := os.MkdirAll(servicesDir, 0o700); err != nil {
		t.Fatalf("failed to create services dir: %v", err)
	}
	if err := os.WriteFile(getSpecFilePath("legacy-app"), []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("failed to write legacy fixture: %v", err)
	}

	spec, err := svc.GetSpec("legacy-app")
	if err != nil {
		t.Fatalf("unexpected error loading legacy spec: %v", err)
	}
	if !spec.Managed {
		t.Fatalf("expected a legacy (pre-schemaVersion) spec to migrate to Managed=true, since the prototype applied io.podder.managed=true unconditionally; got Managed=false — replaying this spec would silently strip the managed label from a container that currently carries it")
	}
	if spec.SchemaVersion != 0 {
		t.Errorf("expected legacy schema version to remain 0 and read-only, got %d", spec.SchemaVersion)
	}

	if errs := ValidateSpec(*spec); len(errs) == 0 {
		t.Fatal("expected legacy prototype spec to be blocked from destructive replay")
	}

	// A spec written by the CURRENT code (SchemaVersion already set) must
	// NOT be force-migrated — its explicit Managed value is authoritative.
	if err := svc.SaveSpec(ContainerSpec{Name: "explicit-unmanaged", Image: "alpine", Managed: false}); err != nil {
		t.Fatalf("failed to save spec: %v", err)
	}
	explicit, err := svc.GetSpec("explicit-unmanaged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if explicit.Managed {
		t.Errorf("expected an explicitly-unmanaged current-schema spec to stay unmanaged, got Managed=true")
	}
}

func TestSpecFilePermissionsAreRestrictive(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{}
	spec := ContainerSpec{Name: "secret-app", Image: "alpine", Env: map[string]string{"TOKEN": "s3cr3t"}}
	if err := service.SaveSpec(spec); err != nil {
		t.Fatalf("failed to save spec: %v", err)
	}

	servicesDir := getServicesDir()
	dirInfo, err := os.Stat(servicesDir)
	if err != nil {
		t.Fatalf("failed to stat services dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("expected services dir mode 0700, got %o", perm)
	}

	fileInfo, err := os.Stat(getSpecFilePath("secret-app"))
	if err != nil {
		t.Fatalf("failed to stat spec file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected spec file mode 0600, got %o", perm)
	}
}

func TestCandidateSpecCommitAndDiscard(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	spec := ContainerSpec{Name: "candidate-app", Image: "alpine"}
	candidatePath, err := writeCandidateSpec(spec)
	if err != nil {
		t.Fatalf("failed to write candidate spec: %v", err)
	}

	info, err := os.Stat(candidatePath)
	if err != nil {
		t.Fatalf("candidate spec file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected candidate spec mode 0600, got %o", perm)
	}

	// A candidate must never be visible to ListSpecs/GetSpec before commit.
	svc := &PodmanService{}
	if _, err := svc.GetSpec("candidate-app"); err == nil {
		t.Errorf("expected candidate spec to not be authoritative before commit")
	}
	specs, err := svc.ListSpecs()
	if err != nil {
		t.Fatalf("unexpected error listing specs: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected ListSpecs to ignore uncommitted candidates, got %d", len(specs))
	}

	if err := commitCandidateSpec(candidatePath, spec); err != nil {
		t.Fatalf("failed to commit candidate spec: %v", err)
	}
	if _, err := svc.GetSpec("candidate-app"); err != nil {
		t.Errorf("expected committed spec to be authoritative: %v", err)
	}

	// discardCandidateSpec on an already-committed (moved) path must not panic.
	discardCandidateSpec("")
}

func TestBuildRunArgsFromSpecWithPodderLabels(t *testing.T) {
	spec := ContainerSpec{
		Name:           "flowise-service",
		Image:          "alpine:latest",
		Managed:        true,
		SchemaVersion:  CurrentSpecSchemaVersion,
		ResolvedImage:  "sha256:test-image",
		ReplayComplete: true,
		PortMappings: []PortMapping{
			{HostIP: "127.0.0.1", HostPort: 3000, ContainerPort: 3000, Protocol: "tcp"},
		},
	}

	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasManagedLabel := false
	hasServiceLabel := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--label" && args[i+1] == "io.podder.managed=true" {
			hasManagedLabel = true
		}
		if args[i] == "--label" && args[i+1] == "io.podder.service=flowise-service" {
			hasServiceLabel = true
		}
	}

	if !hasManagedLabel || !hasServiceLabel {
		t.Errorf("expected Podder labels in args: %v", args)
	}
}

func TestBuildRunArgsFromSpecUnmanagedHasNoLabels(t *testing.T) {
	spec := ContainerSpec{
		Name:  "scratch",
		Image: "alpine:latest",
		PortMappings: []PortMapping{
			{HostPort: 3001, ContainerPort: 3000, Protocol: "tcp"},
		},
	}

	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--label" {
			t.Errorf("expected no Podder labels for an unmanaged spec, got: %v", args)
		}
	}
}
