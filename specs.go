package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BindMountSpec models a host-to-container filesystem mount.
type BindMountSpec struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath"`
	ReadOnly      bool   `json:"readOnly"`
}

// ContainerSpec models the declarative specification for a Podder-managed service.
type ContainerSpec struct {
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	PortMappings []PortMapping     `json:"portMappings"`
	Binds        []BindMountSpec   `json:"binds,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Command      string            `json:"command,omitempty"`
	CreatedAt    string            `json:"createdAt,omitempty"`
	UpdatedAt    string            `json:"updatedAt,omitempty"`
}

func getServicesDir() string {
	return filepath.Join(getConfigDir(), "services")
}

func sanitizeSpecName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "..", "")
	return name
}

func getSpecFilePath(name string) string {
	safeName := sanitizeSpecName(name)
	return filepath.Join(getServicesDir(), safeName+".json")
}

// SaveSpec stores a declarative container specification atomically.
func (p *PodmanService) SaveSpec(spec ContainerSpec) error {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return fmt.Errorf("service name cannot be empty")
	}
	spec.Image = strings.TrimSpace(spec.Image)
	if spec.Image == "" {
		return fmt.Errorf("image name cannot be empty")
	}

	servicesDir := getServicesDir()
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create services directory %s: %w", servicesDir, err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if spec.CreatedAt == "" {
		spec.CreatedAt = now
	}
	spec.UpdatedAt = now

	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize container spec: %w", err)
	}

	filePath := getSpecFilePath(spec.Name)
	tmpFile := filePath + ".tmp"

	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return fmt.Errorf("failed to write temporary spec: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		return fmt.Errorf("failed to commit spec file: %w", err)
	}

	return nil
}

// GetSpec loads a declarative container specification by service name.
func (p *PodmanService) GetSpec(name string) (*ContainerSpec, error) {
	filePath := getSpecFilePath(name)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("spec not found for service %s: %w", name, err)
	}

	var spec ContainerSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("corrupted spec file for service %s: %w", name, err)
	}

	return &spec, nil
}

// ListSpecs returns all stored declarative container specifications.
func (p *PodmanService) ListSpecs() ([]ContainerSpec, error) {
	servicesDir := getServicesDir()
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ContainerSpec{}, nil
		}
		return nil, fmt.Errorf("failed to read services directory: %w", err)
	}

	var specs []ContainerSpec
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(servicesDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var spec ContainerSpec
		if err := json.Unmarshal(data, &spec); err == nil {
			specs = append(specs, spec)
		}
	}

	return specs, nil
}

// DeleteSpec removes a declarative container specification from storage.
func (p *PodmanService) DeleteSpec(name string) error {
	filePath := getSpecFilePath(name)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete spec for %s: %w", name, err)
	}
	return nil
}

// DeploySpec recreates and runs a container from its stored declarative specification.
func (p *PodmanService) DeploySpec(name string) (string, error) {
	spec, err := p.GetSpec(name)
	if err != nil {
		return "", err
	}

	// 1. If an existing container with this name is running, stop and remove it
	containers, _ := p.ListContainers(true)
	for _, c := range containers {
		for _, cName := range c.Names {
			if strings.TrimPrefix(cName, "/") == spec.Name {
				_ = p.StopContainer(c.Id)
				_ = p.RemoveContainer(c.Id)
				break
			}
		}
	}

	// 2. Build run command arguments
	hostPath := ""
	containerPath := ""
	readOnly := false
	if len(spec.Binds) > 0 {
		hostPath = spec.Binds[0].HostPath
		containerPath = spec.Binds[0].ContainerPath
		readOnly = spec.Binds[0].ReadOnly
	}

	args, err := buildRunContainerArgsWithMappings(
		spec.Image,
		spec.Name,
		spec.PortMappings,
		spec.Command,
		hostPath,
		containerPath,
		readOnly,
	)
	if err != nil {
		return "", fmt.Errorf("failed to build run arguments from spec: %w", err)
	}

	// 3. Execute container run
	stdout, stderr, err := p.runCommand(args...)
	if err != nil {
		return "", fmt.Errorf("failed to run container from spec: %v, stderr: %s", err, strings.TrimSpace(stderr))
	}

	containerID := strings.TrimSpace(stdout)
	return containerID, nil
}
