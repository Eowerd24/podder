package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CurrentSpecSchemaVersion is the schema version written by this build of
// Podder. A spec is only considered safely replayable when its
// SchemaVersion is <= this value; a spec written by a newer, unknown schema
// must never be guessed at or partially replayed.
const CurrentSpecSchemaVersion = 3

// BindMountSpec models a host-to-container filesystem mount.
type BindMountSpec struct {
	HostPath        string `json:"hostPath"`
	ContainerPath   string `json:"containerPath"`
	ReadOnly        bool   `json:"readOnly"`
	ObjectIdentity  string `json:"objectIdentity,omitempty"`
	sourceIdentity  string
	sourceFile      *os.File
	podderGenerated bool
	approvedPath    string
}

// ContainerSpec models the declarative specification for a Podder-managed
// service. It is the sole source of truth for recreating a container: every
// field here is replayed by BuildRunArgsFromSpec, and no other code path may
// construct run arguments for a Podder-managed workload from scratch.
type UlimitSpec struct {
	Name string `json:"name"`
	Soft int64  `json:"soft"`
	Hard int64  `json:"hard"`
}

type ContainerSpec struct {
	Ulimits       []UlimitSpec `json:"ulimits,omitempty"`
	SchemaVersion int          `json:"schemaVersion"`
	Name          string       `json:"name"`
	Image         string       `json:"image"`
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
	Managed       bool              `json:"managed"`
	PortMappings  []PortMapping     `json:"portMappings"`
	Binds         []BindMountSpec   `json:"binds,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	EnvironmentID string            `json:"environmentId,omitempty"`
	RequiredEnv   []string          `json:"requiredEnv,omitempty"`
	// Labels contains ordinary workload metadata that must survive replay.
	// Podder ownership labels are generated internally and are never accepted
	// through this map.
	Labels     map[string]string `json:"labels,omitempty"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	Command    CommandArgv       `json:"command,omitempty"`
	CreatedAt  string            `json:"createdAt,omitempty"`
	UpdatedAt  string            `json:"updatedAt,omitempty"`
}

// ValidateSpec checks that a ContainerSpec is well-formed and safe to
// replay. It is pure and touches neither the network nor the container
// runtime; PodmanService.validateMappingsForCreate performs the additional
// backend port checks (registry/runtime collisions) that must run
// immediately before applying any create or mutation.
func ValidateSpec(spec ContainerSpec) []string {
	var errs []string
	if err := validatePodmanOperand(spec.Image); err != nil {
		errs = append(errs, err.Error())
	}
	if spec.Managed {
		if err := validatePodmanOperand(spec.ResolvedImage); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(spec.Ulimits) > 0 && spec.SchemaVersion < 3 {
		errs = append(errs, "ulimit replay requires spec schema 3 or later")
	}
	for _, limit := range spec.Ulimits {
		if _, err := adoptionUlimit(limit.Name, limit.Soft, limit.Hard); err != nil {
			errs = append(errs, err.Error())
		}
	}

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
	if spec.Managed && strings.TrimSpace(spec.Name) != "" {
		// A managed spec's Name becomes its on-disk filename stem
		// (getSpecFilePath) with no lossy transformation, so it must match
		// the canonical service-name grammar up front — before any file is
		// ever written — rather than being silently reduced to something
		// that could collide with a different logical name.
		if err := validateServiceName(strings.TrimSpace(spec.Name)); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if spec.Managed {
		if spec.SchemaVersion < 2 || !spec.ReplayComplete {
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
		if !b.podderGenerated {
			if err := rejectPrivateRootOverlap(hostPath); err != nil {
				errs = append(errs, fmt.Sprintf("bind mount #%d: %v", i+1, err))
			}
		}
	}

	for key, value := range spec.Env {
		if key == "" || strings.ContainsAny(key, "=\r\n\x00") || strings.TrimSpace(key) != key || strings.HasPrefix(key, "#") {
			errs = append(errs, fmt.Sprintf("environment variable name %q is invalid", key))
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			errs = append(errs, fmt.Sprintf("environment variable %q contains a line break or NUL", key))
		}
	}

	for key, value := range spec.Labels {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" || strings.ContainsAny(key, "=\x00") {
			errs = append(errs, fmt.Sprintf("label name %q is invalid", key))
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "io.podder.") {
			errs = append(errs, fmt.Sprintf("label %q is reserved for Podder ownership metadata", key))
		}
		if strings.ContainsRune(value, '\x00') {
			errs = append(errs, fmt.Sprintf("label %q contains NUL", key))
		}
	}

	for _, arg := range append(append([]string{}, spec.Entrypoint...), []string(spec.Command)...) {
		if strings.ContainsRune(arg, '\x00') {
			errs = append(errs, "entrypoint and command arguments must not contain NUL")
			break
		}
	}
	for _, e := range spec.Entrypoint {
		if strings.TrimSpace(e) == "" {
			// FormatEntrypointArg passes a single-element entrypoint
			// through verbatim; an empty element there becomes
			// `--entrypoint ""`, which silently discards the image's
			// built-in entrypoint instead of surfacing a validation error.
			errs = append(errs, "entrypoint must not contain empty arguments")
			break
		}
	}

	return errs
}

func getServicesDir() string {
	return filepath.Join(getConfigDir(), "services")
}

// serviceNamePattern is the canonical service-name grammar: it must start
// with a letter or digit and contain only letters, digits, '_', '.', or
// '-'. Notably it excludes '/' entirely, so a validated name can never
// escape the services directory or collide with another name via path
// separators.
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// validateServiceName enforces the canonical service-name grammar. A
// validated name is used AS THE FILENAME STEM DIRECTLY (see
// getSpecFilePath) rather than through any lossy transformation — the
// previous sanitizeSpecName approach (replacing '/' with '-', stripping
// "..") let distinct logical names collide on disk (e.g. "a/b" and "a-b"
// both became "a-b.json"), which is unacceptable for a service's
// authoritative on-disk identity.
func validateServiceName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("invalid service name %q: must start with a letter or digit and contain only letters, digits, '_', '.', or '-'", name)
	}
	return nil
}

// getSpecFilePath is the single source of truth for a service name's
// on-disk spec path, used identically by save/get/delete and by candidate
// prepare/commit (create and adopt). It refuses any name that does not
// match the canonical grammar rather than silently transforming it.
func getSpecFilePath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if err := validateServiceName(name); err != nil {
		return "", err
	}
	return filepath.Join(getServicesDir(), name+".json"), nil
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
	release, err := lockManagedTransaction()
	if err != nil {
		return err
	}
	defer release()
	if err := checkManagedRecovery(strings.TrimSpace(spec.Name)); err != nil {
		return err
	}

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
	if err := validateServiceName(spec.Name); err != nil {
		return err
	}
	spec.Image = strings.TrimSpace(spec.Image)
	if spec.Image == "" {
		return fmt.Errorf("image name cannot be empty")
	}
	if spec.SchemaVersion == 0 {
		spec.SchemaVersion = CurrentSpecSchemaVersion
	}
	if err := approveContainerSpecBinds(&spec); err != nil {
		return fmt.Errorf("invalid container bind: %w", err)
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

	stored, err := storedEnvironmentSpec(spec)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize container spec: %w", err)
	}

	filePath, err := getSpecFilePath(spec.Name)
	if err != nil {
		return err
	}
	return writePrivateFileAtomic(filePath, data)
}

// writeCandidateSpec persists a not-yet-authoritative draft of spec to the
// services directory under a random, distinct filename that GetSpec/
// ListSpecs never treat as authoritative. It is the "prepare" half of the
// prepare/commit pattern used by managed container creation, port mutation,
// and adoption: a candidate spec can be safely discarded (os.Remove) if the
// transaction fails, and the transaction only becomes visible to the rest of
// Podder once commitCandidateSpec renames it into place.
func writeCandidateSpec(spec ContainerSpec) (string, error) {
	path, _, err := writeCandidateSpecPrepared(spec)
	return path, err
}

// writeCandidateSpecPrepared returns the exact in-memory form that was
// serialized. Transaction code must carry this value forward; the staged
// pathname is never reread as authority because another same-UID process can
// replace it after staging.
func writeCandidateSpecPrepared(spec ContainerSpec) (string, ContainerSpec, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return "", spec, fmt.Errorf("service name cannot be empty")
	}
	if err := validateServiceName(spec.Name); err != nil {
		return "", spec, err
	}
	if spec.SchemaVersion == 0 {
		spec.SchemaVersion = CurrentSpecSchemaVersion
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if spec.CreatedAt == "" {
		spec.CreatedAt = now
	}
	spec.UpdatedAt = now

	servicesDir := getServicesDir()
	if err := ensurePrivateDir(servicesDir); err != nil {
		return "", spec, fmt.Errorf("failed to create services directory %s: %w", servicesDir, err)
	}

	stored, err := storedEnvironmentSpec(spec)
	if err != nil {
		return "", spec, err
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return "", spec, fmt.Errorf("failed to serialize candidate spec: %w", err)
	}

	root, err := privateRoot(servicesDir)
	if err != nil {
		return "", spec, err
	}
	defer root.Close()
	candidateName := spec.Name + ".candidate-" + newSessionID() + ".json"
	f, err := root.OpenFile(candidateName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", spec, fmt.Errorf("failed to create candidate spec file: %w", err)
	}
	candidatePath := filepath.Join(servicesDir, candidateName)

	if err := f.Chmod(0o600); err != nil {
		f.Close()
		_ = root.Remove(candidateName)
		return "", spec, fmt.Errorf("failed to secure candidate spec file permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = root.Remove(candidateName)
		return "", spec, fmt.Errorf("failed to write candidate spec: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = root.Remove(candidateName)
		return "", spec, fmt.Errorf("failed to sync candidate spec: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = root.Remove(candidateName)
		return "", spec, fmt.Errorf("failed to finalize candidate spec: %w", err)
	}

	if err := syncRootDirectory(root); err != nil {
		return "", spec, err
	}
	return candidatePath, stored, nil
}

// commitCandidateSpec atomically promotes a candidate spec written by
// writeCandidateSpec into the authoritative spec file for spec.Name. This
// must only be called after the container/workload it describes has been
// created and verified — never before.
func commitCandidateSpec(candidatePath string, spec ContainerSpec) error {
	if candidatePath == "" {
		return fmt.Errorf("no candidate spec to commit")
	}
	if filepath.Dir(candidatePath) != getServicesDir() || !isCandidateSpecFile(filepath.Base(candidatePath)) {
		return fmt.Errorf("candidate spec path is outside the private services directory")
	}
	finalPath, err := getSpecFilePath(spec.Name)
	if err != nil {
		return err
	}
	stored, err := storedEnvironmentSpec(spec)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if stored.CreatedAt == "" {
		stored.CreatedAt = now
	}
	if stored.UpdatedAt == "" {
		stored.UpdatedAt = now
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize verified candidate spec: %w", err)
	}
	// The staged pathname is deliberately not promoted. It is same-UID mutable
	// state, so commit writes freshly serialized bytes from the verified
	// in-memory spec and atomically replaces authority with those exact bytes.
	if err := writePrivateFileAtomic(finalPath, data); err != nil {
		return fmt.Errorf("failed to commit verified candidate spec: %w", err)
	}
	root, err := privateRoot(filepath.Dir(finalPath))
	if err != nil {
		return fmt.Errorf("failed to reopen committed spec: %w", err)
	}
	committed, readErr := root.ReadFile(filepath.Base(finalPath))
	root.Close()
	if readErr != nil || string(committed) != string(data) {
		return fmt.Errorf("committed spec bytes could not be verified")
	}
	discardCandidateSpec(candidatePath)
	return nil
}

// discardCandidateSpec best-effort removes a candidate spec file. It is safe
// to call with an empty path (no-op) or after the file is already gone.
func discardCandidateSpec(candidatePath string) {
	if candidatePath == "" {
		return
	}
	dir, name, err := privatePathParts(candidatePath)
	if err != nil || dir != getServicesDir() || !isCandidateSpecFile(name) {
		return
	}
	root, err := privateRoot(dir)
	if err != nil {
		return
	}
	defer root.Close()
	_ = root.Remove(name)
	_ = syncRootDirectory(root)
}

// GetSpec loads a declarative container specification by service name.
func (p *PodmanService) GetSpec(name string) (*ContainerSpec, error) {
	filePath, err := getSpecFilePath(name)
	if err != nil {
		return nil, fmt.Errorf("spec not found for service %s: %w", name, err)
	}
	root, err := privateRoot(filepath.Dir(filePath))
	if err != nil {
		return nil, fmt.Errorf("spec not found for service %s: %w", name, err)
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.Base(filePath))
	if err != nil {
		return nil, fmt.Errorf("spec not found for service %s: %w", name, err)
	}

	var spec ContainerSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("corrupted spec file for service %s: %w", name, err)
	}
	migrateLegacySpec(&spec)
	if spec.EnvironmentID != "" && len(spec.Env) == 0 {
		if env, e := specEnvironment(spec); e == nil {
			spec.Env = env
		}
	}

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
	root, err := privateRoot(servicesDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read services directory: %w", err)
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("failed to read services directory: %w", err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("failed to read services directory: %w", err)
	}

	var specs []ContainerSpec
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || isCandidateSpecFile(entry.Name()) {
			continue
		}

		data, err := root.ReadFile(entry.Name())
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
// This refuses while a live Podder-managed container still carries this
// service's ownership labels (io.podder.managed=true,
// io.podder.service=<name>): deleting the spec out from under a running
// managed workload would orphan it — a container claiming Podder ownership
// with no authoritative spec on disk to recreate/verify it from, violating
// the core managed-workload invariant. A deliberate "detach from Podder"
// feature, if added, must be its own explicit workflow that updates
// ownership labels safely; it must never happen implicitly via spec
// deletion.
func (p *PodmanService) DeleteSpec(name string) error {
	release, err := lockManagedTransaction()
	if err != nil {
		return err
	}
	defer release()
	if err := checkManagedRecovery(strings.TrimSpace(name)); err != nil {
		return err
	}

	name = strings.TrimSpace(name)
	containers, err := p.ListContainers(true)
	if err != nil {
		return fmt.Errorf("refusing to delete spec for %s: local containers could not be inspected, and deleting the spec without checking risks orphaning a live managed workload: %w", name, err)
	}
	for _, c := range containers {
		if c.Provenance.Type == "podder" && strings.EqualFold(c.Provenance.Service, name) {
			cName := c.Id
			if len(c.Names) > 0 {
				cName = strings.TrimPrefix(c.Names[0], "/")
			}
			return fmt.Errorf("refusing to delete spec for %s: a running Podder-managed container (%s) still carries this service's ownership labels; stop and remove that container (or migrate it to a different spec) before deleting its spec, so no managed workload is ever left without an authoritative spec", name, cName)
		}
	}

	filePath, err := getSpecFilePath(name)
	if err != nil {
		return err
	}
	root, err := privateRoot(filepath.Dir(filePath))
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(filepath.Base(filePath)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete spec for %s: %w", name, err)
	}
	return syncRootDirectory(root)
}

// DeploySpec recreates and runs a container from its stored declarative
// specification, using the single authoritative BuildRunArgsFromSpec
// builder so replay is exact: every bind, every environment variable, the
// full command argv, and all published ports are applied — not just the
// first bind, and not a spec with Env silently ignored.
type DeploySpecResult struct {
	Success                bool            `json:"success"`
	NewContainerID         string          `json:"newContainerId,omitempty"`
	OldContainerID         string          `json:"oldContainerId,omitempty"`
	BackupContainerName    string          `json:"backupContainerName,omitempty"`
	Rollback               *RollbackResult `json:"rollback,omitempty"`
	ManualRecoveryRequired bool            `json:"manualRecoveryRequired,omitempty"`
	BackupCleanupRequired  bool            `json:"backupCleanupRequired,omitempty"`
	Message                string          `json:"message"`
}

func (p *PodmanService) DeploySpec(name string) (*DeploySpecResult, error) {
	operation, release, lockErr := p.managedOperation()
	if lockErr != nil {
		return nil, lockErr
	}
	defer release()
	p = operation

	if err := checkManagedRecovery(strings.TrimSpace(name)); err != nil {
		return nil, err
	}
	result := &DeploySpecResult{}
	spec, err := p.GetSpec(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	if err := requirePersistedBindObjectWitnesses(spec); err != nil {
		return result, fmt.Errorf("stored spec bind approval is incomplete: %w", err)
	}
	if err := approveContainerSpecBinds(spec); err != nil {
		return result, fmt.Errorf("stored spec bind approval failed before mutation: %w", err)
	}
	if errs := ValidateSpec(*spec); len(errs) > 0 {
		return result, fmt.Errorf("stored spec for %q is not authoritative and replayable: %s", name, strings.Join(errs, "; "))
	}
	if !spec.Managed {
		return result, fmt.Errorf("DeploySpec only accepts verified Podder-managed specifications")
	}

	containers, err := p.ListContainers(true)
	if err != nil {
		return result, fmt.Errorf("deploy preflight could not inspect existing containers: %w", err)
	}
	target := findContainerByName(containers, spec.Name)
	ignoreID := ""
	if target != nil {
		ignoreID = target.Id
	}
	if _, err := p.validateMappingsForMutation(spec.PortMappings, ignoreID); err != nil {
		return result, fmt.Errorf("deploy preflight port validation failed: %w", err)
	}

	backupName := ""
	if target != nil {
		backupName = newBackupName(spec.Name)
	}
	journal, journalErr := p.beginManagedJournal(spec.Name, "deploy", backupName, "", target, *spec)
	if journalErr != nil {
		return nil, journalErr
	}
	defer p.finishManagedJournal(journal)
	if target == nil {
		args, buildErr := BuildRunArgsFromSpec(*spec)
		if buildErr != nil {
			return result, fmt.Errorf("stored spec is not replayable: %w", buildErr)
		}
		stdout, stderr, runErr := p.runSpecCommand(*spec, args...)
		var discoverErr error
		result.NewContainerID, _, discoverErr = p.transactionCandidateIdentity(stdout, *spec)
		if discoverErr != nil {
			result.ManualRecoveryRequired = true
			result.Message = discoverErr.Error()
			return result, nil
		}
		if runErr != nil {
			current, listErr := p.ListContainers(true)
			if listErr != nil {
				result.ManualRecoveryRequired = true
				result.Message = fmt.Sprintf("Podman reported create failure and Podder could not inspect whether a candidate exists: %v.", runErr)
				return result, nil
			}
			candidate := findContainerByExactID(current, result.NewContainerID)
			if result.NewContainerID != "" && candidate != nil {
				removeErr := p.exactTransactionContainer(result.NewContainerID, spec.Name, nil, spec)
				if removeErr == nil {
					p.StopContainer(result.NewContainerID)
					removeErr = p.forceRemoveContainer(result.NewContainerID)
				}
				removed := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool { return p.containerAbsent(result.NewContainerID, "") })
				if !removed {
					result.ManualRecoveryRequired = true
					result.Message = fmt.Sprintf("Podman reported create failure and identified candidate cleanup could not be verified (create error: %v, remove error: %v).", runErr, removeErr)
					return result, nil
				}
				result.Message = fmt.Sprintf("Podman reported create failure after an identified candidate appeared; the candidate was verified removed: %v.", runErr)
				return result, nil
			}
			if findContainerByName(current, spec.Name) != nil {
				result.ManualRecoveryRequired = true
				result.Message = fmt.Sprintf("Podman reported create failure and a container now occupies %q, but no exact candidate ID was returned; it was retained for manual inspection.", spec.Name)
				return result, nil
			}
			return result, fmt.Errorf("failed to create workload from spec: %v (stderr: %s)", runErr, strings.TrimSpace(stderr))
		}
		var created *Container
		verified := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
			current, listErr := p.ListContainers(true)
			if listErr != nil {
				return false
			}
			created = findContainerByExactID(current, result.NewContainerID)
			if created == nil {
				return false
			}
			kind, ok := classifyLifecycle(created.State)
			return ok && kind == lifecycleRunning && strings.TrimSpace(created.ImageID) == strings.TrimSpace(spec.ResolvedImage) && containerMatchesSpecLabels(created, *spec) && mappingsExactlyEqual(spec.PortMappings, created.PortMappings)
		})
		if !verified {
			removeErr := p.exactTransactionContainer(result.NewContainerID, spec.Name, nil, spec)
			if removeErr == nil {
				p.StopContainer(result.NewContainerID)
				removeErr = p.forceRemoveContainer(result.NewContainerID)
			}
			removed := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool { return p.containerAbsent(result.NewContainerID, spec.Name) })
			if !removed {
				result.ManualRecoveryRequired = true
				result.Message = fmt.Sprintf("Deployment verification failed and candidate cleanup could not be verified (remove error: %v).", removeErr)
				return result, nil
			}
			result.Message = "Deployment verification failed; the unverified candidate was removed."
			return result, nil
		}
		journal.Committed = true
		if err := journal.save(); err != nil {
			return result, err
		}
		result.Success = true
		result.Message = "Workload created from authoritative spec and verified."
		return result, nil
	}

	result.OldContainerID = target.Id
	if target.Provenance.Type != "podder" || target.Provenance.Service != spec.Name || !containerMatchesSpecLabels(target, *spec) {
		return result, fmt.Errorf("deploy blocked: existing workload %q does not match the stored Podder ownership metadata", spec.Name)
	}
	if strings.TrimSpace(target.ImageID) != strings.TrimSpace(spec.ResolvedImage) || !mappingsExactlyEqual(spec.PortMappings, target.PortMappings) {
		return result, fmt.Errorf("deploy blocked: existing workload %q has drifted from its authoritative image or port configuration", spec.Name)
	}
	originalLifecycle, ok := classifyLifecycle(target.State)
	if !ok {
		return result, fmt.Errorf("deploy blocked: lifecycle state %q cannot be safely reproduced", target.State)
	}
	var createArgs []string
	if originalLifecycle == lifecycleRunning {
		createArgs, err = BuildRunArgsFromSpec(*spec)
	} else {
		createArgs, err = BuildCreateArgsFromSpec(*spec)
	}
	if err != nil {
		return result, fmt.Errorf("stored spec is not replayable: %w", err)
	}

	// backup name was recorded durably before any engine mutation
	result.BackupContainerName = backupName
	if err := p.exactTransactionContainer(target.Id, spec.Name, target, spec); err != nil {
		return nil, err
	}
	if _, _, err := p.runCommand("rename", "--", target.Id, backupName); err != nil {
		rb, message := p.reconcileFailedRename(target, backupName, originalLifecycle, transactionWitness{Original: *target, Candidate: *spec})
		if rb.Attempted {
			result.Rollback = rb
		}
		result.ManualRecoveryRequired = !rb.Verified
		result.Message = fmt.Sprintf("Rename command reported %v. %s", err, message)
		return result, nil
	}
	rollback := func(reason string, candidateWasCreated bool) (*DeploySpecResult, error) {
		rb := p.executeRollback(backupName, spec.Name, result.NewContainerID, target.Id, originalLifecycle, candidateWasCreated, transactionWitness{Original: *target, Candidate: *spec})
		result.Rollback = rb
		if rb.Verified {
			result.Message = reason + " The original workload was restored and verified."
		} else {
			result.ManualRecoveryRequired = true
			result.Message = reason + " ROLLBACK FAILED / MANUAL RECOVERY REQUIRED: " + strings.Join(rb.Errors, "; ")
		}
		return result, nil
	}

	if originalLifecycle == lifecycleRunning {
		if err := p.StopContainer(target.Id); err != nil {
			return rollback(fmt.Sprintf("Deployment failed while stopping the original workload: %v.", err), false)
		}
		stopped := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
			current, listErr := p.ListContainers(true)
			if listErr != nil {
				return false
			}
			backup := findContainerByName(current, backupName)
			if backup == nil || backup.Id != target.Id {
				return false
			}
			kind, supported := classifyLifecycle(backup.State)
			return supported && kind == lifecycleStopped
		})
		if !stopped {
			return rollback("Deployment failed: original workload did not verify stopped.", false)
		}
	}

	stdout, stderr, runErr := p.runSpecCommand(*spec, createArgs...)
	var discoverErr error
	result.NewContainerID, _, discoverErr = p.transactionCandidateIdentity(stdout, *spec)
	if discoverErr != nil {
		result.ManualRecoveryRequired = true
		result.Message = discoverErr.Error()
		return result, nil
	}
	if runErr != nil {
		current, _ := p.ListContainers(true)
		candidateWasCreated := result.NewContainerID != "" && findContainerByExactID(current, result.NewContainerID) != nil
		return rollback(fmt.Sprintf("Deployment failed to create replacement: %v (stderr: %s).", runErr, strings.TrimSpace(stderr)), candidateWasCreated)
	}
	var replacement *Container
	verified := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
		current, listErr := p.ListContainers(true)
		if listErr != nil {
			return false
		}
		replacement = findContainerByExactID(current, result.NewContainerID)
		if replacement == nil {
			return false
		}
		kind, supported := classifyLifecycle(replacement.State)
		return supported && kind == originalLifecycle && strings.TrimSpace(replacement.ImageID) == strings.TrimSpace(spec.ResolvedImage) && containerMatchesSpecLabels(replacement, *spec) && mappingsExactlyEqual(spec.PortMappings, replacement.PortMappings)
	})
	if !verified {
		return rollback("Deployment failed: replacement identity, lifecycle, image, labels, or ports did not verify.", true)
	}

	journal.Committed = true
	if err := journal.save(); err != nil {
		return result, err
	}
	removeErr := p.exactTransactionContainer(target.Id, backupName, target, spec)
	if removeErr == nil {
		removeErr = p.forceRemoveContainer(target.Id)
	}
	removed := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool { return p.containerAbsent(target.Id, backupName) })
	result.Success = true
	if !removed {
		result.BackupCleanupRequired = true
		result.Message = fmt.Sprintf("Replacement committed and verified, but backup %s remains and requires manual cleanup (remove error: %v).", backupName, removeErr)
		return result, nil
	}
	result.Message = "Replacement committed, verified, and backup removed."
	return result, nil
}

func mappingsExactlyEqual(expected, actual []PortMapping) bool {
	equal, _, _ := portMappingSetEqual(expected, actual)
	return equal
}

func writePrivateFileAtomic(path string, data []byte) error {
	dir, name, err := privatePathParts(path)
	if err != nil {
		return err
	}
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	root, err := privateRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return writePrivateFileAtomicAt(root, name, data)
}

func writePrivateFileAtomicAt(root *os.Root, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." || name == "" {
		return fmt.Errorf("invalid private filename")
	}
	tmp := ".podder-private-" + newSessionID() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
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
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	return syncRootDirectory(root)
}
