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
		if m.RangeSize > 1 {
			if int(m.ContainerPort)+m.RangeSize-1 > 65535 {
				errs = append(errs, fmt.Sprintf("port mapping #%d: container port range overflows past 65535", i+1))
			}
			if m.HostPort != 0 && int(m.HostPort)+m.RangeSize-1 > 65535 {
				errs = append(errs, fmt.Sprintf("port mapping #%d: host port range overflows past 65535", i+1))
			}
		}
		if m.HostPort != 0 {
			key := fmt.Sprintf("%s|%d|%d|%s", NormalizeAddress(m.HostIP), m.HostPort, m.RangeSize, proto)
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
	spec.SchemaVersion = CurrentSpecSchemaVersion
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

// DeploySpecResult reports the outcome of a DeploySpec transaction. It
// follows the same honesty rules as PortMutationResult: Success is only
// ever true after the workload was actually verified, and a failure from
// QUIESCE onward is either a truthfully-verified rollback or an explicit
// ManualRecoveryRequired — never a silent "probably fine".
type DeploySpecResult struct {
	Success bool `json:"success"`
	// ContainerID is the new/current container's ID: the freshly-created
	// one when there was no existing container to replace, or the
	// replacement's ID once CREATE succeeds.
	ContainerID string `json:"containerId,omitempty"`
	// OldContainerID is set only when an existing container occupying the
	// spec's name was found and replaced.
	OldContainerID         string                   `json:"oldContainerId,omitempty"`
	Replaced               bool                     `json:"replaced"`
	RolledBack             bool                     `json:"rolledBack"`
	RollbackReason         string                   `json:"rollbackReason,omitempty"`
	Rollback               *RollbackResult          `json:"rollback,omitempty"`
	ManualRecoveryRequired bool                     `json:"manualRecoveryRequired,omitempty"`
	BackupContainerName    string                   `json:"backupContainerName,omitempty"`
	Steps                  []PortMutationStepResult `json:"steps"`
	ConfigurationVerified  bool                     `json:"configurationVerified,omitempty"`
	ListenerObserved       bool                     `json:"listenerObserved,omitempty"`
	HealthVerified         bool                     `json:"healthVerified,omitempty"`
	// CleanupWarning is set when the deployment itself committed
	// successfully but a purely cosmetic follow-up (removing the backup
	// container) failed. It must never be confused with the transaction
	// itself failing — Success stays true.
	CleanupWarning string `json:"cleanupWarning,omitempty"`
}

// DeploySpec recreates and runs a container from its stored declarative
// specification, using the single authoritative BuildRunArgsFromSpec
// builder so replay is exact: every bind, every environment variable, the
// full command argv, and all published ports are applied — not just the
// first bind, and not a spec with Env silently ignored.
//
// This is fully transactional, mirroring MutateContainerPorts and reusing
// its same primitives (classifyLifecycle, newBackupName, pollUntil,
// portMappingSetEqual, executeRollback):
//
//	PREFLIGHT  - load + migrate the spec, validate it, resolve any existing
//	             container occupying its name, capture that container's
//	             lifecycle, and prove the candidate spec builds into valid
//	             run/create arguments and passes final port-collision
//	             validation — all before touching anything.
//	SNAPSHOT   - compute a collision-resistant backup name.
//	QUIESCE    - rename the existing container out of the way and stop it
//	             if it was running, verifying the stop.
//	CREATE     - recreate entirely from the authoritative spec, preserving
//	             the existing container's original lifecycle.
//	VERIFY     - identity, expected lifecycle, exact port mappings, and
//	             (for a Managed spec) the expected Podder labels; for a
//	             running replacement, best-effort listener/health evidence.
//	COMMIT     - remove the backup container.
//	ROLLBACK   - on any failure from QUIESCE onward, restore the original
//	             container's name and lifecycle and report a structured,
//	             truthfully-verified RollbackResult. The backup is never
//	             deleted unless rollback is verified successful, and if the
//	             very first QUIESCE step (the rename) itself fails, nothing
//	             was ever moved — this is reported directly, without
//	             attempting (and spuriously failing) a rollback that has
//	             nothing to restore.
//
// If no existing container occupies the spec's name, there is nothing to
// quiesce or roll back: creation uses the simpler managed-creation path
// (build, run, verify) instead. Never remove the only working instance
// before proving the candidate is replayable.
func (p *PodmanService) DeploySpec(name string) (*DeploySpecResult, error) {
	result := &DeploySpecResult{Steps: []PortMutationStepResult{}}

	spec, err := p.GetSpec(name)
	if err != nil {
		return nil, err
	}

	if errs := ValidateSpec(*spec); len(errs) > 0 {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Stored spec for %q failed validation, refusing to deploy: %s", name, strings.Join(errs, "; ")),
		})
		return result, nil
	}

	containers, err := p.ListContainers(true)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect local containers: %w", err)
	}
	target := findContainerByName(containers, spec.Name)

	if target == nil {
		return p.deployFreshFromSpec(*spec, result)
	}

	return p.redeployReplacingContainer(*spec, target, result)
}

// deployFreshFromSpec handles DeploySpec when no existing container
// occupies the spec's name: there is nothing to quiesce or roll back to, so
// the simpler managed-creation path applies. A container that fails to
// verify is removed rather than left behind unverified.
func (p *PodmanService) deployFreshFromSpec(spec ContainerSpec, result *DeploySpecResult) (*DeploySpecResult, error) {
	if err := p.validateMappingsForMutation(spec.PortMappings, ""); err != nil {
		result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: err.Error()})
		return result, nil
	}
	if _, err := BuildRunArgsFromSpec(spec); err != nil {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Stored spec is not replayable: %v", err),
		})
		return result, nil
	}
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "PREFLIGHT", Passed: true,
		Message: "No existing container occupies this name; deploying fresh from the authoritative spec.",
	})

	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("failed to build run arguments from spec: %w", err)
	}
	stdout, stderr, err := p.runCommand(args...)
	if err != nil {
		return nil, fmt.Errorf("failed to run container from spec: %v, stderr: %s", err, strings.TrimSpace(stderr))
	}
	containerID := strings.TrimSpace(stdout)
	result.ContainerID = containerID
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "CREATE", Passed: true, Message: "Container created from the authoritative spec."})

	// Prefer removing by the well-known spec name over the raw container
	// ID: it's the identity the rest of Podder's transactions (rename,
	// stop, start) already key off of, and it's guaranteed to be the
	// exact name --name just created the container under.
	removalIdentity := containerID
	if spec.Name != "" {
		removalIdentity = spec.Name
	}

	newContainer, verified := p.verifyDeployedContainer(containerID, spec, lifecycleRunning)
	if !verified || newContainer == nil {
		if rmErr := p.RemoveContainer(removalIdentity); rmErr != nil {
			result.ManualRecoveryRequired = true
			result.Steps = append(result.Steps, PortMutationStepResult{
				Step: "VERIFY", Passed: false,
				Message: fmt.Sprintf("New container failed to verify after deployment and could not be automatically removed (%v). Manual recovery required for container %s.", rmErr, containerID),
			})
			return result, nil
		}
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "VERIFY", Passed: false,
			Message: "New container failed to verify after deployment (identity, lifecycle, ports, or Podder labels did not match the spec); it has been removed.",
		})
		return result, nil
	}
	result.ConfigurationVerified = true
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "VERIFY", Passed: true, Message: "New container verified: identity, lifecycle, ports, and labels match the spec."})

	result.Success = true
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "COMMITTED", Passed: true, Message: "Deployment committed."})
	return result, nil
}

// redeployReplacingContainer handles DeploySpec when an existing container
// already occupies the spec's name: the full quiesce/create/verify/commit
// transaction, with rollback on any failure.
func (p *PodmanService) redeployReplacingContainer(spec ContainerSpec, target *Container, result *DeploySpecResult) (*DeploySpecResult, error) {
	containerName := strings.TrimSpace(spec.Name)
	if containerName == "" {
		containerName = "unnamed"
		if len(target.Names) > 0 {
			containerName = strings.TrimPrefix(target.Names[0], "/")
		}
	}
	result.OldContainerID = target.Id

	originalLifecycle, lifecycleSupported := classifyLifecycle(target.State)
	if !lifecycleSupported {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Existing container lifecycle state %q cannot be safely reproduced by Podder. Deployment refused.", target.State),
		})
		return result, nil
	}

	if err := p.validateMappingsForMutation(spec.PortMappings, target.Id); err != nil {
		result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: err.Error()})
		return result, nil
	}
	if _, err := BuildRunArgsFromSpec(spec); err != nil {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Stored spec is not replayable: %v", err),
		})
		return result, nil
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "PREFLIGHT", Passed: true,
		Message: "Preflight verified: spec validated and replayable, no port collisions, safe existing lifecycle.",
	})

	backupName := newBackupName(containerName)
	result.BackupContainerName = backupName
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "SNAPSHOT", Passed: true,
		Message: fmt.Sprintf("Backup identity reserved as %s.", backupName),
	})

	// quiesced tracks whether the existing container was actually renamed
	// out of the way yet. If that rename itself fails, nothing was ever
	// moved — there is no backup to roll back from, and attempting one
	// anyway would rename a nonexistent backup name back (which fails)
	// and spuriously report manual recovery for a workload that was never
	// touched.
	quiesced := false

	fail := func(step, message string, candidateWasCreated bool) (*DeploySpecResult, error) {
		if !quiesced {
			result.RollbackReason = message
			result.Steps = append(result.Steps, PortMutationStepResult{Step: step, Passed: false, Message: message})
			return result, nil
		}
		rb := p.executeRollback(backupName, containerName, containerName, originalLifecycle, candidateWasCreated)
		result.Rollback = rb
		result.RollbackReason = message
		if rb.Verified {
			result.RolledBack = true
			result.Steps = append(result.Steps, PortMutationStepResult{Step: "ROLLED_BACK", Passed: true, Message: message})
		} else {
			result.ManualRecoveryRequired = true
			result.Steps = append(result.Steps, PortMutationStepResult{
				Step: "ROLLBACK_FAILED", Passed: false,
				Message: fmt.Sprintf("ROLLBACK FAILED / MANUAL RECOVERY REQUIRED: %s (backup=%s, candidate-name=%s, errors: %s)", message, backupName, containerName, strings.Join(rb.Errors, "; ")),
			})
		}
		return result, nil
	}

	// QUIESCE
	if _, _, err := p.runCommand("rename", containerName, backupName); err != nil {
		return fail("QUIESCE", fmt.Sprintf("Failed to rename existing container to backup: %v", err), false)
	}
	quiesced = true

	if originalLifecycle == lifecycleRunning {
		if err := p.StopContainer(backupName); err != nil {
			return fail("QUIESCE", fmt.Sprintf("Failed to stop original container before recreation: %v", err), false)
		}
		stopped := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
			cs, err := p.ListContainers(true)
			if err != nil {
				return false
			}
			c := findContainerByName(cs, backupName)
			if c == nil {
				return false
			}
			kind, _ := classifyLifecycle(c.State)
			return kind == lifecycleStopped
		})
		if !stopped {
			return fail("QUIESCE", "Timed out waiting for original container to stop before recreation.", false)
		}
	}
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "QUIESCE", Passed: true,
		Message: fmt.Sprintf("Existing container renamed to %s and quiesced.", backupName),
	})

	// CREATE
	var createArgs []string
	var buildErr error
	if originalLifecycle == lifecycleRunning {
		createArgs, buildErr = BuildRunArgsFromSpec(spec)
	} else {
		createArgs, buildErr = BuildCreateArgsFromSpec(spec)
	}
	if buildErr != nil {
		return fail("CREATE", fmt.Sprintf("Failed to construct run arguments: %v", buildErr), false)
	}
	stdout, stderr, err := p.runCommand(createArgs...)
	if err != nil {
		return fail("CREATE", fmt.Sprintf("Failed to create replacement container: %v (stderr: %s)", err, strings.TrimSpace(stderr)), false)
	}
	newContainerID := strings.TrimSpace(stdout)
	result.ContainerID = newContainerID
	result.Replaced = true
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "CREATE", Passed: true, Message: "Replacement container created from the authoritative spec."})

	// VERIFY: identity, lifecycle, exact port mappings, and (if Managed)
	// Podder labels.
	newContainer, verified := p.verifyDeployedContainer(newContainerID, spec, originalLifecycle)
	if !verified || newContainer == nil {
		return fail("VERIFY", "New container failed to verify: identity, lifecycle, ports, or Podder labels did not match the spec.", true)
	}
	result.ConfigurationVerified = true

	if originalLifecycle == lifecycleRunning {
		allObserved := true
		for _, m := range spec.PortMappings {
			if m.HostPort == 0 {
				continue
			}
			observed := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
				return p.observeListenerForMapping(m)
			})
			if !observed {
				allObserved = false
			}
		}
		result.ListenerObserved = allObserved

		if _, hasHealthcheck := p.containerHealthStatus(newContainer.Id); hasHealthcheck {
			// Read the FINAL health state after polling, not whatever
			// snapshot was observed before/during the wait — a container
			// that starts "starting" and later transitions to
			// "unhealthy" must be caught, not waved through because its
			// initial status looked harmless.
			var finalStatus string
			_ = pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
				s, ok := p.containerHealthStatus(newContainer.Id)
				if ok {
					finalStatus = s
				}
				return ok && s == "healthy"
			})
			switch finalStatus {
			case "healthy":
				result.HealthVerified = true
			case "unhealthy":
				return fail("VERIFY", "Replacement container reports an unhealthy Podman healthcheck status.", true)
			default:
				// still "starting" (or unknown) after the bounded wait:
				// recorded as not (yet) verified, but not fatal — some
				// services are legitimately slow to become healthy.
			}
		}
	}

	verifyMsg := "Replacement container verified: identity, lifecycle, ports, and labels match the spec."
	if result.ListenerObserved {
		verifyMsg += " Host listeners observed for all published ports."
	}
	if result.HealthVerified {
		verifyMsg += " Podman healthcheck reports healthy."
	}
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "VERIFY", Passed: true, Message: verifyMsg})

	// COMMIT: remove the backup. A cleanup failure here is reported as a
	// distinct warning — it never contradicts the already-successful
	// COMMITTED step, since the deployment itself is done and verified.
	result.Success = true
	if err := p.RemoveContainer(backupName); err != nil {
		result.CleanupWarning = fmt.Sprintf("Backup container %s could not be removed automatically: %v. Manual cleanup recommended.", backupName, err)
	}
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "COMMITTED", Passed: true, Message: "Deployment committed: container recreated from the authoritative spec."})
	if result.CleanupWarning != "" {
		result.Steps = append(result.Steps, PortMutationStepResult{Step: "CLEANUP_WARNING", Passed: false, Message: result.CleanupWarning})
	}

	return result, nil
}

// verifyDeployedContainer bounded-polls for a container matching
// containerID (or spec.Name) to appear with the expected lifecycle, exact
// port mappings, and — if the spec is Managed — the expected Podder
// labels/provenance. Shared by DeploySpec's two paths.
func (p *PodmanService) verifyDeployedContainer(containerID string, spec ContainerSpec, expectedLifecycle string) (*Container, bool) {
	var found *Container
	ok := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
		cs, err := p.ListContainers(true)
		if err != nil {
			return false
		}
		c := findContainerByIdentity(cs, containerID)
		if c == nil && spec.Name != "" {
			c = findContainerByName(cs, spec.Name)
		}
		if c == nil {
			return false
		}
		kind, supported := classifyLifecycle(c.State)
		if !supported || kind != expectedLifecycle {
			return false
		}
		if eq, _, _ := portMappingSetEqual(spec.PortMappings, c.PortMappings); !eq {
			return false
		}
		if spec.Managed && (c.Provenance.Type != "podder" || c.Provenance.Service != spec.Name) {
			return false
		}
		found = c
		return true
	})
	return found, ok
}
