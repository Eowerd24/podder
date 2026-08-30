package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CurrentSpecSchemaVersion is the schema version written by this build of
// Podder. A spec is only considered safely replayable when its
// SchemaVersion is <= this value; a spec written by a newer, unknown schema
// must never be guessed at or partially replayed.
const CurrentSpecSchemaVersion = 2

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
	// ResolvedImage is the immutable image ID/digest used for replay. Image is
	// retained as the operator-facing reference. A mutable tag alone is not an
	// authoritative recreation source because it may point at different bytes.
	ResolvedImage string `json:"resolvedImage,omitempty"`
	// ReplayComplete is only written by a complete creation/adoption path.
	// Prototype specs remain useful for observation but are read-only.
	ReplayComplete bool `json:"replayComplete"`
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
	} else if strings.ContainsRune(spec.Image, '\x00') {
		errs = append(errs, "image must not contain NUL")
	}
	if strings.ContainsRune(spec.Name, '\x00') {
		errs = append(errs, "container name must not contain NUL")
	}
	if spec.Managed && strings.TrimSpace(spec.Name) == "" {
		errs = append(errs, "a managed spec must have a non-empty name")
	}
	if spec.Managed {
		if spec.SchemaVersion < CurrentSpecSchemaVersion || !spec.ReplayComplete {
			errs = append(errs, "managed spec predates the authoritative replay schema; destructive replay is blocked until the workload is safely re-adopted")
		}
		if strings.TrimSpace(spec.ResolvedImage) == "" {
			errs = append(errs, "managed spec is missing an immutable resolved image ID/digest")
		}
	}
	if spec.SchemaVersion > CurrentSpecSchemaVersion {
		errs = append(errs, fmt.Sprintf("spec schema version %d is newer than this build of Podder supports (max %d); refusing to guess at its meaning", spec.SchemaVersion, CurrentSpecSchemaVersion))
	}

	var seenClaims []PortClaim
	for i, m := range spec.PortMappings {
		if m.ContainerPort == 0 {
			errs = append(errs, fmt.Sprintf("port mapping #%d: container port must be between 1 and 65535", i+1))
		}
		proto := NormalizeProtocol(m.Protocol)
		if proto != "tcp" && proto != "udp" {
			errs = append(errs, fmt.Sprintf("port mapping #%d: protocol must be tcp or udp", i+1))
		}
		if m.RangeSize < 0 {
			errs = append(errs, fmt.Sprintf("port mapping #%d: range size cannot be negative", i+1))
		}
		if strings.TrimSpace(m.HostIP) != "" && net.ParseIP(strings.TrimSpace(m.HostIP)) == nil {
			errs = append(errs, fmt.Sprintf("port mapping #%d: invalid host IP %q", i+1, m.HostIP))
		}
		if m.RangeSize > 1 {
			if int(m.ContainerPort)+m.RangeSize-1 > 65535 {
				errs = append(errs, fmt.Sprintf("port mapping #%d: container port range overflows past 65535", i+1))
			}
			if m.HostPort != 0 && int(m.HostPort)+m.RangeSize-1 > 65535 {
				errs = append(errs, fmt.Sprintf("port mapping #%d: host port range overflows past 65535", i+1))
			}
		}
		if m.HostPort != 0 {
			claim := PortClaim{Address: m.HostIP, Port: m.HostPort, Protocol: proto, RangeSize: m.RangeSize}
			if FindConflict(seenClaims, claim, "") != nil {
				errs = append(errs, fmt.Sprintf("port mapping #%d conflicts with another mapping in the same spec (%s)", i+1, m.DisplayString()))
			}
			seenClaims = append(seenClaims, claim)
		}
	}

	for i, b := range spec.Binds {
		hostPath := strings.TrimSpace(b.HostPath)
		containerPath := strings.TrimSpace(b.ContainerPath)
		if hostPath == "" || containerPath == "" {
			errs = append(errs, fmt.Sprintf("bind mount #%d: both host and container paths are required", i+1))
			continue
		}
		if !filepath.IsAbs(hostPath) || !filepath.IsAbs(containerPath) {
			errs = append(errs, fmt.Sprintf("bind mount #%d: host and container paths must be absolute", i+1))
		}
		if strings.ContainsAny(hostPath, ",\x00") || strings.ContainsAny(containerPath, ",\x00") {
			errs = append(errs, fmt.Sprintf("bind mount #%d: paths containing commas or NUL cannot be represented safely", i+1))
		}
	}

	for key, value := range spec.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			errs = append(errs, fmt.Sprintf("environment variable name %q is invalid", key))
		}
		if strings.ContainsRune(value, '\x00') {
			errs = append(errs, fmt.Sprintf("environment variable %q contains NUL", key))
		}
	}

	for _, arg := range append(append([]string{}, spec.Entrypoint...), []string(spec.Command)...) {
		if strings.ContainsRune(arg, '\x00') {
			errs = append(errs, "entrypoint and command arguments must not contain NUL")
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
	if spec.Managed {
		return fmt.Errorf("directly saving a managed spec is disabled: managed ownership may only be committed by a verified create or adoption transaction")
	}
	return saveSpec(spec)
}

// saveSpec is intentionally unexported so the Wails bridge cannot fabricate
// managed authority. Managed transactions use candidate promotion instead.
func saveSpec(spec ContainerSpec) error {
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
	if err := ensurePrivateDir(servicesDir); err != nil {
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

	return writePrivateFileAtomic(getSpecFilePath(spec.Name), data)
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
	if err := ensurePrivateDir(servicesDir); err != nil {
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

	if err := f.Chmod(0o600); err != nil {
		f.Close()
		_ = os.Remove(candidatePath)
		return "", fmt.Errorf("failed to secure candidate spec file permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(candidatePath)
		return "", fmt.Errorf("failed to write candidate spec: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(candidatePath)
		return "", fmt.Errorf("failed to sync candidate spec: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(candidatePath)
		return "", fmt.Errorf("failed to finalize candidate spec: %w", err)
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
	migrateLegacySpec(&spec)

	return &spec, nil
}

// migrateLegacySpec upgrades a spec loaded from a pre-hardening-pass file
// (SchemaVersion 0, meaning the field didn't exist yet) in place.
//
// Every spec that predates SchemaVersion/Managed was written by a code path
// that unconditionally applied io.podder.managed=true to the container it
// described (both the pre-hardening RunContainerWithPortMappings and
// AdoptContainer did this regardless of any UI checkbox — that blanket
// behavior is exactly what this hardening pass fixed). So a legacy spec
// with no explicit Managed value does NOT mean "this was an unmanaged
// container that happened to get saved" — it means "Managed was true, but
// the field didn't exist yet to record it". Defaulting it to false here
// would be actively dangerous: a mutation or DeploySpec replay would then
// build the replacement container WITHOUT io.podder.managed=true, silently
// stripping managed status (and the label a mutation's own PREFLIGHT relies
// on to recognize the container next time) from a workload that is, right
// now, actually running with that label.
func migrateLegacySpec(spec *ContainerSpec) {
	if spec.SchemaVersion != 0 {
		return
	}
	// Preserve schemaVersion=0. Prototype capture was incomplete, so merely
	// loading a file must not make it eligible for destructive replay.
	spec.Managed = true
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
			migrateLegacySpec(&spec)
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
	return "", fmt.Errorf("DeploySpec is disabled: the prototype path deleted an existing workload without verified rollback; use verified managed creation for a new workload or the transactional mutation path for an existing authoritative workload")
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func writePrivateFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".podder-private-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
