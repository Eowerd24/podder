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
		Command: "nginx -g 'daemon off;'",
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

func TestBuildRunContainerArgsWithPodderLabels(t *testing.T) {
	mappings := []PortMapping{
		{
			HostIP:        "127.0.0.1",
			HostPort:      3000,
			ContainerPort: 3000,
			Protocol:      "tcp",
		},
	}

	args, err := buildRunContainerArgsWithMappings("alpine:latest", "flowise-service", mappings, "", "", "", false)
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
