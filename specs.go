package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CurrentSpecSchemaVersion is the schema version written by this build of
// Podder. A spec is only considered safely replayable when its
// SchemaVersion is <= this value; a spec written by a newer, unknown schema
// must never be guessed at or partially replayed.
const CurrentSpecSchemaVersion = 1

// BindMountSpec models a host-to-container filesystem mount.
type BindMountSpec struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath"`
	ReadOnly      bool   `json:"readOnly"`
}

// ContainerSpec models the declarative specification for a Podder-managed
// service. It is the sole source of truth for recreating a container: every
// field here is replayed by BuildRunArgsFromSpec, and no other code path may
// construct run arguments for a Podder-managed workload from scratch.
type ContainerSpec struct {
	SchemaVersion int    `json:"schemaVersion"`
	Name          string `json:"name"`
	Image         string `json:"image"`
	// Managed marks whether this spec is (or, for a candidate, is intended
	// to become) the authoritative source of truth for a container carrying
	// io.podder.managed=true. It is set explicitly by the caller — never
	// inferred from the presence of port mappings or any other field.
	Managed      bool              `json:"managed"`
	PortMappings []PortMapping     `json:"portMappings"`
	Binds        []BindMountSpec   `json:"binds,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Entrypoint   []string          `json:"entrypoint,omitempty"`
	Command      CommandArgv       `json:"command,omitempty"`
	CreatedAt    string            `json:"createdAt,omitempty"`
	UpdatedAt    string            `json:"updatedAt,omitempty"`
}

// ValidateSpec checks that a ContainerSpec is well-formed and safe to
// replay. It is pure and touches neither the network nor the container
// runtime; PodmanService.validateMappingsForCreate performs the additional
// backend port checks (registry/runtime collisions) that must run
// immediately before applying any create or mutation.
func ValidateSpec(spec ContainerSpec) []string {
	var errs []string

	if strings.TrimSpace(spec.Image) == "" {
		errs = append(errs, "image must not be empty")
	}
	if spec.Managed && strings.TrimSpace(spec.Name) == "" {
		errs = append(errs, "a managed spec must have a non-empty name")
	}
	if spec.SchemaVersion > CurrentSpecSchemaVersion {
		errs = append(errs, fmt.Sprintf("spec schema version %d is newer than this build of Podder supports (max %d); refusing to guess at its meaning", spec.SchemaVersion, CurrentSpecSchemaVersion))
	}

	seenPorts := make(map[string]bool)
	for i, m := range spec.PortMappings {
		if m.ContainerPort == 0 {
			errs = append(errs, fmt.Sprintf("port mapping #%d: container port must be between 1 and 65535", i+1))
		}
		proto := NormalizeProtocol(m.Protocol)
		if proto != "tcp" && proto != "udp" {
			errs = append(errs, fmt.Sprintf("port mapping #%d: protocol must be tcp or udp", i+1))
		}
		if m.HostPort != 0 {
			key := fmt.Sprintf("%s|%d|%s", NormalizeAddress(m.HostIP), m.HostPort, proto)
			if seenPorts[key] {
				errs = append(errs, fmt.Sprintf("port mapping #%d duplicates another mapping in the same spec (%s)", i+1, m.DisplayString()))
			}
			seenPorts[key] = true
		}
	}

	for i, b := range spec.Binds {
		if strings.TrimSpace(b.HostPath) == "" || strings.TrimSpace(b.ContainerPath) == "" {
			errs = append(errs, fmt.Sprintf("bind mount #%d: both host and container paths are required", i+1))
		}
	}

	for _, e := range spec.Entrypoint {
		if strings.TrimSpace(e) == "" {
			errs = append(errs, "entrypoint must not contain empty arguments")
			break
		}
	}

	return errs
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

// isCandidateSpecFile reports whether a services-dir entry is a not-yet-committed
// candidate spec (see writeCandidateSpec) rather than an authoritative one.
func isCandidateSpecFile(name string) bool {
	return strings.Contains(name, ".candidate-")
}

// SaveSpec stores a declarative container specification atomically, as the
// immediately-authoritative spec for its name. Callers running a multi-step
// transaction (create, mutate, adopt) that must not let a half-finished
// operation appear authoritative should use writeCandidateSpec +
// commitCandidateSpec instead.
func (p *PodmanService) SaveSpec(spec ContainerSpec) error {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return fmt.Errorf("service name cannot be empty")
	}
	spec.Image = strings.TrimSpace(spec.Image)
	if spec.Image == "" {
		return fmt.Errorf("image name cannot be empty")
	}
	if spec.SchemaVersion == 0 {
		spec.SchemaVersion = CurrentSpecSchemaVersion
	}

	servicesDir := getServicesDir()
	if err := os.MkdirAll(servicesDir, 0o700); err != nil {
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

	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temporary spec: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to commit spec file: %w", err)
	}

	return nil
}

// writeCandidateSpec persists a not-yet-authoritative draft of spec to the
// services directory under a random, distinct filename that GetSpec/
// ListSpecs never treat as authoritative. It is the "prepare" half of the
// prepare/commit pattern used by managed container creation, port mutation,
// and adoption: a candidate spec can be safely discarded (os.Remove) if the
// transaction fails, and the transaction only becomes visible to the rest of
// Podder once commitCandidateSpec renames it into place.
func writeCandidateSpec(spec ContainerSpec) (string, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return "", fmt.Errorf("service name cannot be empty")
	}
	spec.SchemaVersion = CurrentSpecSchemaVersion

	now := time.Now().UTC().Format(time.RFC3339)
	if spec.CreatedAt == "" {
		spec.CreatedAt = now
	}
	spec.UpdatedAt = now

	servicesDir := getServicesDir()
	if err := os.MkdirAll(servicesDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create services directory %s: %w", servicesDir, err)
	}

	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to serialize candidate spec: %w", err)
	}

	safeName := sanitizeSpecName(spec.Name)
	f, err := os.CreateTemp(servicesDir, safeName+".candidate-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create candidate spec file: %w", err)
	}
	candidatePath := f.Name()

	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(candidatePath)
		return "", fmt.Errorf("failed to write candidate spec: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(candidatePath)
		return "", fmt.Errorf("failed to finalize candidate spec: %w", err)
	}
	if err := os.Chmod(candidatePath, 0o600); err != nil {
		_ = os.Remove(candidatePath)
		return "", fmt.Errorf("failed to secure candidate spec file permissions: %w", err)
	}

	return candidatePath, nil
}

// commitCandidateSpec atomically promotes a candidate spec written by
// writeCandidateSpec into the authoritative spec file for spec.Name. This
// must only be called after the container/workload it describes has been
// created and verified — never before.
func commitCandidateSpec(candidatePath string, spec ContainerSpec) error {
	if candidatePath == "" {
		return fmt.Errorf("no candidate spec to commit")
	}
	finalPath := getSpecFilePath(spec.Name)
	if err := os.Rename(candidatePath, finalPath); err != nil {
		return fmt.Errorf("failed to commit candidate spec: %w", err)
	}
	return nil
}

// discardCandidateSpec best-effort removes a candidate spec file. It is safe
// to call with an empty path (no-op) or after the file is already gone.
func discardCandidateSpec(candidatePath string) {
	if candidatePath == "" {
		return
	}
	_ = os.Remove(candidatePath)
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
	if spec.SchemaVersion == 0 {
		spec.SchemaVersion = CurrentSpecSchemaVersion
	}

	return &spec, nil
}

// ListSpecs returns all stored, committed declarative container
// specifications. Candidate (not-yet-committed) specs are never included.
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || isCandidateSpecFile(entry.Name()) {
			continue
		}

		filePath := filepath.Join(servicesDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var spec ContainerSpec
		if err := json.Unmarshal(data, &spec); err == nil {
			if spec.SchemaVersion == 0 {
				spec.SchemaVersion = CurrentSpecSchemaVersion
			}
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

// DeploySpec recreates and runs a container from its stored declarative
// specification, using the single authoritative BuildRunArgsFromSpec
// builder so replay is exact: every bind, every environment variable, the
// full command argv, and all published ports are applied — not just the
// first bind, and not a spec with Env silently ignored.
func (p *PodmanService) DeploySpec(name string) (string, error) {
	spec, err := p.GetSpec(name)
	if err != nil {
		return "", err
	}

	if errs := ValidateSpec(*spec); len(errs) > 0 {
		return "", fmt.Errorf("stored spec for %s failed validation, refusing to deploy: %s", name, strings.Join(errs, "; "))
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

	// 2. Build run command arguments from the complete spec
	args, err := BuildRunArgsFromSpec(*spec)
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
