package main

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PortMutationRequest represents a request to change the port mappings of
// an existing container. There is deliberately no "force" escape hatch:
// mutation eligibility is decided entirely by AssessMutationEligibility,
// never overridden by the caller.
type PortMutationRequest struct {
	ContainerID string        `json:"containerId"`
	ServiceName string        `json:"serviceName,omitempty"`
	NewPorts    []PortMapping `json:"newPorts"`
}

// PortMutationStepResult tracks an individual step in the mutation transaction.
type PortMutationStepResult struct {
	Step    string `json:"step"` // "PREFLIGHT", "SNAPSHOT", "QUIESCE", "CONTAINER_CREATE", "PORT_VERIFY", "COMMITTED", "ROLLED_BACK", "ROLLBACK_FAILED"
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// RollbackResult reports the true, verified outcome of a rollback attempt.
// Every boolean here reflects something that was actually checked — never
// an assumption that a command with a nil error means the desired state was
// reached.
type RollbackResult struct {
	Attempted         bool `json:"attempted"`
	RestoredName      bool `json:"restoredName"`
	RestoredLifecycle bool `json:"restoredLifecycle"`
	RemovedCandidate  bool `json:"removedCandidate"`
	// Verified is true only when the original workload was confirmed to
	// exist again under its original name, in its original lifecycle state,
	// with no errors recorded at any step. Callers must never report
	// "rolled back successfully" unless this is true.
	Verified bool     `json:"verified"`
	Errors   []string `json:"errors,omitempty"`
}

// PortMutationResult contains the outcome of an atomic port mutation transaction.
type PortMutationResult struct {
	Success        bool            `json:"success"`
	NewContainerID string          `json:"newContainerId,omitempty"`
	OldContainerID string          `json:"oldContainerId,omitempty"`
	RolledBack     bool            `json:"rolledBack"`
	RollbackReason string          `json:"rollbackReason,omitempty"`
	Rollback       *RollbackResult `json:"rollback,omitempty"`
	// ManualRecoveryRequired is set when a rollback was attempted but could
	// not be verified — the backup and/or candidate container names remain
	// available (never deleted in this case) for manual recovery.
	ManualRecoveryRequired bool                     `json:"manualRecoveryRequired,omitempty"`
	BackupCleanupRequired  bool                     `json:"backupCleanupRequired,omitempty"`
	BackupContainerName    string                   `json:"backupContainerName,omitempty"`
	Steps                  []PortMutationStepResult `json:"steps"`
	Guidance               string                   `json:"guidance,omitempty"`
	RequiresExternal       bool                     `json:"requiresExternal"`
	ComposeSnippet         string                   `json:"composeSnippet,omitempty"`
	QuadletSnippet         string                   `json:"quadletSnippet,omitempty"`
	// The three verification tiers actually performed — see
	// AnalyzeExposureTransition-style honesty: never claim a check that
	// wasn't run.
	ConfigurationVerified bool     `json:"configurationVerified,omitempty"`
	ListenerObserved      bool     `json:"listenerObserved,omitempty"`
	HealthVerified        bool     `json:"healthVerified,omitempty"`
	ExposureWarnings      []string `json:"exposureWarnings,omitempty"`
}

// Bounded-polling parameters for verification steps. Tests override these to
// small values so the suite runs fast without real wall-clock waits.
var (
	mutationPollAttempts = 10
	mutationPollInterval = 300 * time.Millisecond
)

const (
	lifecycleRunning = "running"
	lifecycleStopped = "stopped"
)

// classifyLifecycle buckets a raw Podman state string into a lifecycle this
// transaction knows how to safely reproduce. Anything else (paused,
// restarting, removing, unknown...) is explicitly unsupported: the
// transaction refuses rather than guessing.
func classifyLifecycle(state string) (kind string, supported bool) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running":
		return lifecycleRunning, true
	case "exited", "created", "stopped", "configured":
		return lifecycleStopped, true
	default:
		return state, false
	}
}

// GenerateComposeSnippet formats the proposed port mappings for a docker-compose.yml file.
// Callers must pass the container's actual, confidently-identified Compose
// service name — never a generic placeholder — since a snippet keyed under
// the wrong service could be pasted into the wrong place in the compose
// file. If the real service identity can't be determined, callers must
// state that instead of calling this with an invented name.
func GenerateComposeSnippet(serviceName string, ports []PortMapping) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("services:\n  %s:\n    ports:\n", serviceName))
	for _, m := range ports {
		sb.WriteString(fmt.Sprintf("      - \"%s\"\n", FormatPublishSpec(m)))
	}
	return sb.String()
}

// GenerateQuadletSnippet formats the proposed port mappings for a systemd .container file.
func GenerateQuadletSnippet(ports []PortMapping) string {
	var sb strings.Builder
	sb.WriteString("[Container]\n")
	for _, m := range ports {
		sb.WriteString(fmt.Sprintf("PublishPort=%s\n", FormatPublishSpec(m)))
	}
	return sb.String()
}

// PreviewComposeSnippet is the bound method the frontend must call to
// render a Compose ports: snippet for display/copy — including live preview
// while an operator is still adjusting the desired port rows before
// generating the final snippet. This exists specifically so the frontend
// NEVER hand-formats a Podman/Compose port publish spec itself: the same
// canonical formatter (FormatPublishSpec, via GenerateComposeSnippet) used
// for actual mutation guidance is used here, so omitted-bind handling,
// IPv6 bracketing, port ranges, and protocol formatting can never drift
// between a "live preview" string built in JavaScript and the real one.
func (p *PodmanService) PreviewComposeSnippet(serviceName string, ports []PortMapping) (string, error) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return "", fmt.Errorf("compose service name cannot be empty")
	}
	return GenerateComposeSnippet(serviceName, ports), nil
}

// PreviewQuadletSnippet is the PreviewComposeSnippet counterpart for a
// systemd Quadlet .container unit's PublishPort= lines.
func (p *PodmanService) PreviewQuadletSnippet(ports []PortMapping) (string, error) {
	return GenerateQuadletSnippet(ports), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// newBackupName produces a collision-resistant backup container name: a
// timestamp alone (as the prototype used) is not sufficient because two
// mutations racing within the same second, or a clock that jumps, can
// collide. Combining a nanosecond timestamp with random bytes makes an
// accidental collision practically impossible.
func newBackupName(containerName string) string {
	return fmt.Sprintf("%s--podder-bak-%d-%s", containerName, time.Now().UnixNano(), randomHex(4))
}

// findContainerByIdentity locates a container by ID (or ID prefix) or by
// exact name among a slice already fetched via ListContainers.
func findContainerByIdentity(containers []Container, identity string) *Container {
	for i := range containers {
		c := &containers[i]
		if c.Id == identity || (identity != "" && strings.HasPrefix(c.Id, identity)) {
			return c
		}
		for _, name := range c.Names {
			if strings.TrimPrefix(name, "/") == identity {
				return c
			}
		}
	}
	return nil
}

// ContainerPortEditState is the single authoritative source a port editor
// (container-card path or Ports-tab path — there is deliberately only ONE
// way to open it now) must populate itself from before allowing an edit.
// Caller-supplied cached data (e.g. from a list rendered a few seconds ago)
// must never be trusted as the editable state: it can be stale, or — as the
// Ports-tab path used to do — simply empty, which would make a workload
// with real published ports look like it has none and let an operator
// submit a replacement configuration without ever seeing what they were
// about to remove.
type ContainerPortEditState struct {
	ContainerID   string             `json:"containerId"`
	ContainerName string             `json:"containerName"`
	Provenance    WorkloadProvenance `json:"provenance"`
	PortMappings  []PortMapping      `json:"portMappings"`
}

// GetContainerPortEditState resolves a container by exact ID (or unambiguous
// ID prefix — see findContainerByIdentity) and returns its CURRENT
// backend-observed provenance and port mappings. This is the one method the
// port editor must call before presenting anything editable; a caller that
// cannot resolve the container gets an explicit error rather than a
// same-shaped-but-empty result that could be silently treated as "no ports
// configured".
func (p *PodmanService) GetContainerPortEditState(containerID string) (*ContainerPortEditState, error) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return nil, fmt.Errorf("container id cannot be empty")
	}

	containers, err := p.ListContainers(true)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect local containers: %w", err)
	}

	target := findContainerByIdentity(containers, containerID)
	if target == nil {
		return nil, fmt.Errorf("container %s not found", containerID)
	}

	name := "unnamed"
	if len(target.Names) > 0 {
		name = strings.TrimPrefix(target.Names[0], "/")
	}

	mappings := make([]PortMapping, len(target.PortMappings))
	copy(mappings, target.PortMappings)

	return &ContainerPortEditState{
		ContainerID:   target.Id,
		ContainerName: name,
		Provenance:    target.Provenance,
		PortMappings:  mappings,
	}, nil
}

// findContainerByName locates a container by exact name.
func findContainerByName(containers []Container, name string) *Container {
	for i := range containers {
		c := &containers[i]
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == name {
				return c
			}
		}
	}
	return nil
}

// MutateContainerPorts executes a safe, genuinely transactional port
// mutation:
//
//	PREFLIGHT  - resolve provenance and eligibility, load and validate the
//	             authoritative spec, build+validate the complete candidate
//	             spec (including resolving every bind path), capture the
//	             original lifecycle state.
//	SNAPSHOT   - persist a candidate spec under a distinct filename and
//	             compute a collision-resistant backup name, without
//	             touching the current known-good spec.
//	QUIESCE    - rename the original container out of the way and stop it
//	             if it was running, verifying the stop.
//	CREATE     - recreate the replacement from the complete candidate spec,
//	             preserving the original lifecycle (a stopped container is
//	             recreated stopped, never auto-started).
//	VERIFY     - verify existence, lifecycle, and exact configured port
//	             mappings; best-effort observe host listeners and container
//	             health without pretending either is guaranteed.
//	COMMIT     - atomically promote the candidate spec and remove the
//	             backup container only after that commit succeeds.
//	ROLLBACK   - on any failure from QUIESCE onward, restore the original
//	             container's name and lifecycle and report a structured,
//	             truthfully-verified RollbackResult; the backup is never
//	             deleted unless rollback is verified successful.
func (p *PodmanService) MutateContainerPorts(req PortMutationRequest) (*PortMutationResult, error) {
	result := &PortMutationResult{
		Success: false,
		Steps:   []PortMutationStepResult{},
	}

	req.ContainerID = strings.TrimSpace(req.ContainerID)
	if req.ContainerID == "" {
		return nil, fmt.Errorf("container id cannot be empty")
	}

	// 1. Fetch container details
	containers, err := p.ListContainers(true)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect local containers: %w", err)
	}

	target := findContainerByIdentity(containers, req.ContainerID)
	if target == nil {
		return nil, fmt.Errorf("container %s not found", req.ContainerID)
	}

	containerName := "unnamed"
	if len(target.Names) > 0 {
		containerName = strings.TrimPrefix(target.Names[0], "/")
	}
	result.OldContainerID = target.Id

	// 2. Preflight: provenance / eligibility gate.
	//
	// Allowed mutation matrix:
	//   Podder-managed + valid complete stored spec -> eligible
	//   Compose-managed                              -> external workflow only
	//   Quadlet-managed                               -> external workflow only
	//   Pod member                                    -> no per-container mutation
	//   Ad-hoc                                        -> disabled; adopt first
	//   Unknown/ambiguous provenance                  -> disabled
	prov := target.Provenance
	switch prov.Type {
	case "compose":
		result.RequiresExternal = true
		result.Guidance = "This container is managed by Docker Compose or Podman Compose. Direct recreation from the GUI would orphan the service. Update your compose file with the snippet below and re-run 'pod up'."
		// The generated snippet must name the container's ACTUAL Compose
		// service — a generic placeholder key could be pasted under the
		// wrong service entirely. If the label evidence didn't actually
		// include a service name, state that plainly instead of guessing.
		if strings.TrimSpace(prov.Service) != "" {
			result.ComposeSnippet = GenerateComposeSnippet(prov.Service, req.NewPorts)
		} else {
			result.Guidance += " The Compose service identity for this container could not be safely determined, so no snippet was generated; update your compose file's ports: for the correct service manually."
		}
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: "Orchestrator guard: Compose-managed workload cannot be modified directly.",
		})
		return result, nil

	case "quadlet":
		result.RequiresExternal = true
		result.QuadletSnippet = GenerateQuadletSnippet(req.NewPorts)
		result.Guidance = "This container is managed by systemd Quadlet. Update your .container unit file with the PublishPort settings below and reload systemd ('systemctl --user daemon-reload && systemctl --user restart " + prov.UnitName + "')."
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: "Orchestrator guard: Quadlet-managed workload cannot be modified directly.",
		})
		return result, nil

	case "pod":
		result.RequiresExternal = true
		result.Guidance = "This container is part of Pod '" + prov.PodName + "'. Port mappings in Podman belong to the Pod itself and cannot be updated on an individual member container."
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: "Orchestrator guard: Pod member container cannot have independent host port bindings.",
		})
		return result, nil

	case "adhoc":
		result.RequiresExternal = true
		result.Guidance = "This container is not safely reproducible by Podder. Adopt it into Podder before editing its deployment configuration."
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: "Direct ad-hoc port mutation is disabled: Podder has no authoritative spec for this container and cannot prove it can reproduce it. Adopt the workload first.",
		})
		return result, nil

	case "podder":
		// fall through to authoritative-spec resolution below.

	default:
		result.RequiresExternal = true
		result.Guidance = "This container's ownership could not be determined with confidence. Mutation is disabled until provenance is unambiguous."
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Unknown/ambiguous provenance (%q): mutation blocked.", prov.Type),
		})
		return result, nil
	}

	// 2a. Resolve the authoritative spec via the container's own
	// io.podder.service label (never via req.ServiceName alone, and never
	// synthesized from `podman ps` — an absent or invalid spec blocks the
	// mutation rather than being papered over).
	specName := strings.TrimSpace(prov.Service)
	if specName == "" || specName == "Podder Service" {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: "Podder-managed metadata inconsistent: container has no resolvable io.podder.service label. Mutation blocked.",
		})
		return result, nil
	}

	oldSpec, err := p.GetSpec(specName)
	if err != nil || oldSpec == nil {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Podder-managed metadata inconsistent: no stored spec for service %q. Mutation blocked.", specName),
		})
		return result, nil
	}

	if errs := ValidateSpec(*oldSpec); len(errs) > 0 {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Stored spec for %q failed validation, mutation blocked: %s", specName, strings.Join(errs, "; ")),
		})
		return result, nil
	}

	// 2b. The runtime name, immutable image identity, and configured ports
	// must correspond to the authoritative spec. The display image string is
	// deliberately not compared: Podman may render a tag, digest, or image ID.
	if oldSpec.Name != containerName {
		result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: fmt.Sprintf("Podder-managed metadata inconsistent: runtime name %q does not match stored spec name %q. Mutation blocked.", containerName, oldSpec.Name)})
		return result, nil
	}
	if strings.TrimSpace(target.ImageID) == "" || strings.TrimSpace(target.ImageID) != strings.TrimSpace(oldSpec.ResolvedImage) {
		result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: fmt.Sprintf("Podder-managed metadata inconsistent: runtime image identity %q does not match authoritative image identity %q. Mutation blocked.", target.ImageID, oldSpec.ResolvedImage)})
		return result, nil
	}
	if ok, missing, unexpected := portMappingSetEqual(oldSpec.PortMappings, target.PortMappings); !ok {
		result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: fmt.Sprintf("Podder-managed metadata inconsistent: current runtime ports differ from the known-good spec (missing: %v, unexpected: %v). Mutation blocked.", missing, unexpected)})
		return result, nil
	}

	// 2c. Capture original lifecycle; refuse anything we cannot safely
	// reproduce (paused, restarting, unknown...).
	originalLifecycle, lifecycleSupported := classifyLifecycle(target.State)
	if !lifecycleSupported {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Container lifecycle state %q cannot be safely reproduced by Podder. Mutation refused.", target.State),
		})
		return result, nil
	}

	// 2d. Build and validate the complete candidate spec (every field from
	// the old spec is preserved; only PortMappings changes) before touching
	// anything. This also resolves all bind paths via BuildRunArgsFromSpec.
	candidateSpec := *oldSpec
	candidateSpec.PortMappings = req.NewPorts

	if errs := ValidateSpec(candidateSpec); len(errs) > 0 {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Candidate spec failed validation: %s", strings.Join(errs, "; ")),
		})
		return result, nil
	}

	exposureWarnings, err := p.validateMappingsForMutation(req.NewPorts, target.Id, oldSpec.PortMappings)
	if err != nil {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: err.Error(),
		})
		return result, nil
	}

	result.ExposureWarnings = exposureWarnings

	if _, err := BuildRunArgsFromSpec(candidateSpec); err != nil {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "PREFLIGHT", Passed: false,
			Message: fmt.Sprintf("Candidate spec is not replayable: %v", err),
		})
		return result, nil
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "PREFLIGHT", Passed: true,
		Message: "Preflight verified: authoritative spec resolved, candidate validated and replayable, no port collisions, safe provenance and lifecycle.",
	})

	// 3. SNAPSHOT: persist the candidate spec separately from the current
	// known-good one, and compute a collision-resistant backup name.
	candidatePath, err := writeCandidateSpec(candidateSpec)
	if err != nil {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "SNAPSHOT", Passed: false,
			Message: fmt.Sprintf("Failed to persist candidate spec: %v", err),
		})
		return result, nil
	}

	backupName := newBackupName(containerName)
	candidateID := ""
	result.BackupContainerName = backupName
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "SNAPSHOT", Passed: true,
		Message: fmt.Sprintf("Candidate spec staged; backup identity reserved as %s. Current known-good spec untouched.", backupName),
	})

	fail := func(step, message string, candidateWasCreated bool) (*PortMutationResult, error) {
		discardCandidateSpec(candidatePath)
		rb := p.executeRollback(backupName, containerName, candidateID, target.Id, originalLifecycle, candidateWasCreated)
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

	// 4. QUIESCE: rename the original out of the way, stop it if running,
	// and verify the stop before doing anything destructive to it further.
	if _, _, err := p.runCommand("rename", containerName, backupName); err != nil {
		discardCandidateSpec(candidatePath)
		result.Steps = append(result.Steps, PortMutationStepResult{Step: "QUIESCE", Passed: false, Message: fmt.Sprintf("Failed to rename existing container to backup; original workload was not changed: %v", err)})
		return result, nil
	}

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
		Message: fmt.Sprintf("Original container renamed to %s and quiesced.", backupName),
	})

	// 5. CREATE: recreate the replacement from the complete candidate spec,
	// preserving the original lifecycle state.
	var buildArgsErr error
	var createArgs []string
	if originalLifecycle == lifecycleRunning {
		createArgs, buildArgsErr = BuildRunArgsFromSpec(candidateSpec)
	} else {
		createArgs, buildArgsErr = BuildCreateArgsFromSpec(candidateSpec)
	}
	if buildArgsErr != nil {
		return fail("CONTAINER_CREATE", fmt.Sprintf("Failed to construct run arguments: %v", buildArgsErr), false)
	}

	stdout, stderr, err := p.runCommand(createArgs...)
	if err != nil {
		// Podman can report a create/run failure (e.g. the OCI runtime
		// failing to start the container) after the container object was
		// nonetheless created under containerName. Detect that so
		// executeRollback actually stops/removes the leftover candidate
		// instead of assuming there is nothing to clean up and then
		// colliding with it when renaming the backup back.
		candidateWasCreated := p.containerExistsByIdentity(strings.TrimSpace(stdout), containerName)
		if candidateWasCreated {
			candidateID = containerName
		}
		return fail("CONTAINER_CREATE", fmt.Sprintf("Failed to create replacement container: %v (stderr: %s)", err, strings.TrimSpace(stderr)), candidateWasCreated)
	}

	newContainerID := strings.TrimSpace(stdout)
	candidateID = newContainerID
	if newContainerID == "" {
		return fail("CONTAINER_CREATE", "Replacement creation returned no container identity; ambiguous candidate was retained rather than deleted by name.", false)
	}
	result.NewContainerID = newContainerID

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "CONTAINER_CREATE", Passed: true,
		Message: "Replacement container created from the complete candidate spec.",
	})

	// 6. VERIFY: existence, lifecycle, exact configured port mappings, and
	// best-effort listener/health observation. Only the checks actually
	// performed are reported as true.
	var newContainer *Container
	existsAndLifecycleOK := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
		cs, err := p.ListContainers(true)
		if err != nil {
			return false
		}
		c := findContainerByIdentity(cs, newContainerID)
		if c == nil {
			return false
		}
		kind, _ := classifyLifecycle(c.State)
		if kind != originalLifecycle {
			return false
		}
		newContainer = c
		return true
	})

	if !existsAndLifecycleOK || newContainer == nil {
		return fail("PORT_VERIFY", "New container failed to verify: it did not appear with the expected lifecycle state.", true)
	}

	if strings.TrimSpace(newContainer.ImageID) != strings.TrimSpace(candidateSpec.ResolvedImage) {
		return fail("PORT_VERIFY", fmt.Sprintf("Replacement image identity %q does not match candidate identity %q.", newContainer.ImageID, candidateSpec.ResolvedImage), true)
	}
	if !containerMatchesSpecLabels(newContainer, candidateSpec) {
		return fail("PORT_VERIFY", "Replacement did not carry the exact managed ownership labels from the candidate spec.", true)
	}
	if ok, missing, unexpected := portMappingSetEqual(candidateSpec.PortMappings, newContainer.PortMappings); !ok {
		return fail("PORT_VERIFY", fmt.Sprintf("Configured port mappings do not match the candidate spec (missing: %v, unexpected: %v).", missing, unexpected), true)
	}
	result.ConfigurationVerified = true

	if originalLifecycle == lifecycleRunning {
		allObserved := true
		for _, m := range candidateSpec.PortMappings {
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
			var finalStatus string
			pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
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
				return fail("PORT_VERIFY", fmt.Sprintf("Replacement container healthcheck reports unhealthy (final status: %q).", finalStatus), true)
			default:
				// Still "starting" (or unknown) after the bounded wait:
				// recorded as unverified (HealthVerified stays false), but
				// not treated as fatal — many real healthchecks have a
				// start_period far longer than this poll window, and
				// "not yet verified" is not the same as "unhealthy".
			}
		}
	}

	verifyMsg := "Replacement container verified: configuration matches the candidate spec."
	if result.ListenerObserved {
		verifyMsg += " Host listeners observed for all published ports."
	}
	if result.HealthVerified {
		verifyMsg += " Podman healthcheck reports healthy."
	}
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "PORT_VERIFY", Passed: true, Message: verifyMsg})

	// 7. COMMIT: promote the candidate spec, then remove the backup only
	// after that commit succeeds.
	if err := p.commitCandidate(candidatePath, candidateSpec); err != nil {
		return fail("PORT_VERIFY", fmt.Sprintf("Failed to commit candidate spec after successful recreation: %v", err), true)
	}

	if err := p.forceRemoveContainer(backupName); err != nil {
		result.Success = true
		result.BackupCleanupRequired = true
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step: "COMMITTED", Passed: true,
			Message: fmt.Sprintf("Transaction committed and replacement verified, but recoverable backup %s could not be removed: %v. Manual cleanup required.", backupName, err),
		})
		return result, nil
	}

	result.Success = true
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "COMMITTED", Passed: true,
		Message: "Transaction committed: port mappings updated, spec saved, and backup removed.",
	})

	return result, nil
}

// pollUntil calls check up to attempts times, waiting interval between
// attempts, returning true as soon as check reports success. This replaces
// fixed single sleeps with bounded polling for every verification step.
func pollUntil(attempts int, interval time.Duration, check func() bool) bool {
	if attempts < 1 {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		if check() {
			return true
		}
		if i < attempts-1 {
			time.Sleep(interval)
		}
	}
	return false
}

// portMappingSetEqual compares two port mapping sets for exact DECLARED
// equality (as sets, ignoring order), so a mutation can prove that the
// removed mappings are actually gone and the new ones are actually
// configured exactly as declared — not merely that the container reports
// "running", and not merely that the new bind would avoid a socket
// conflict with the old one. This is a declared-endpoint comparison
// (CanonicalDeclaredBind), never a conflict/allocation-safety comparison
// (AddressesConflict/NormalizeAddress/EndpointsConflict) — those answer a
// different question ("could these collide"), not "is this the exact
// configuration that was declared".
func portMappingSetEqual(want, got []PortMapping) (ok bool, missing, unexpected []PortMapping) {
	key := func(m PortMapping) string {
		rangeSize := m.RangeSize
		if rangeSize <= 1 {
			rangeSize = 1
		}
		return fmt.Sprintf("%s|%d|%d|%d|%s", CanonicalDeclaredBind(m.HostIP), m.HostPort, m.ContainerPort, rangeSize, NormalizeProtocol(m.Protocol))
	}
	wantSet := make(map[string]PortMapping, len(want))
	for _, m := range want {
		wantSet[key(m)] = m
	}
	gotSet := make(map[string]PortMapping, len(got))
	for _, m := range got {
		gotSet[key(m)] = m
	}
	for k, m := range wantSet {
		if _, found := gotSet[k]; !found {
			missing = append(missing, m)
		}
	}
	for k, m := range gotSet {
		if _, found := wantSet[k]; !found {
			unexpected = append(unexpected, m)
		}
	}
	return len(missing) == 0 && len(unexpected) == 0, missing, unexpected
}

// observeListenerForMapping best-effort checks whether something is
// currently listening on the mapping's host port/protocol. This is
// deliberately loose about the bind address: a published port may appear to
// the host as a proxy/forwarder bound to an address that doesn't textually
// match the container's configured HostIP depending on the network backend,
// so this reports LISTENER OBSERVED, distinct from and strictly weaker than
// CONFIGURATION VERIFIED — it is never treated as proof by itself, and its
// absence is never treated as fatal (the application inside the container
// may simply not have bound yet).
func (p *PodmanService) observeListenerForMapping(m PortMapping) bool {
	if m.HostPort == 0 {
		return false
	}
	listeners, err := p.ListHostListeners()
	if err != nil {
		return false
	}
	proto := NormalizeProtocol(m.Protocol)
	rangeSize := m.RangeSize
	if rangeSize <= 1 {
		rangeSize = 1
	}
	for offset := 0; offset < rangeSize; offset++ {
		want := uint16(int(m.HostPort) + offset)
		found := false
		for _, l := range listeners {
			if l.Port == want && NormalizeProtocol(l.Protocol) == proto {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// containerHealthStatus returns a container's Podman healthcheck status
// ("starting", "healthy", "unhealthy") and whether it has a healthcheck
// configured at all. A container with no healthcheck is not a failure —
// HEALTH VERIFIED is simply never claimed for it.
func (p *PodmanService) containerHealthStatus(idOrName string) (string, bool) {
	stdout, _, err := p.runCommand("inspect", "--format", "json", idOrName)
	if err != nil {
		return "", false
	}
	var list []struct {
		State struct {
			Health struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if jsonErr := json.Unmarshal([]byte(stdout), &list); jsonErr != nil || len(list) == 0 {
		return "", false
	}
	status := list[0].State.Health.Status
	if status == "" {
		return "", false
	}
	return status, true
}

// validateMappingsForMutation runs the same mandatory final backend
// validation as container creation (registry/runtime collisions, checked
// immediately before applying), ignoring the target container's own current
// claims, plus intra-request conflict detection.
func (p *PodmanService) validateMappingsForMutation(mappings []PortMapping, ignoreContainerID string, previousMappings ...[]PortMapping) ([]string, error) {
	var seen []PortClaim
	var previous []PortMapping
	if len(previousMappings) > 0 {
		previous = previousMappings[0]
	}
	var exposureWarnings []string
	for _, m := range mappings {
		if m.ContainerPort == 0 || m.HostPort == 0 {
			return nil, fmt.Errorf("invalid port numbers in mapping: %s", m.DisplayString())
		}

		valReq := PortMappingRequest{
			HostIP:        m.HostIP,
			HostPort:      m.HostPort,
			ContainerPort: m.ContainerPort,
			Protocol:      m.Protocol,
			ContainerID:   ignoreContainerID,
			RangeSize:     m.RangeSize,
			OldHostIP:     previousHostIPForMapping(m, previous),
		}
		valResult, err := p.ValidatePortMapping(valReq)
		if err != nil || (valResult != nil && !valResult.Valid) {
			errMsg := "Port conflict detected"
			if valResult != nil {
				for _, c := range valResult.Checks {
					if !c.Passed {
						errMsg = c.Message
						break
					}
				}
			}
			return nil, fmt.Errorf("%s", errMsg)
		}
		if valResult != nil && valResult.ExposureChange {
			exposureWarnings = append(exposureWarnings, valResult.ExposureNotice)
		}

		candidate := PortClaim{Address: m.HostIP, Port: m.HostPort, Protocol: m.Protocol, RangeSize: m.RangeSize}
		if conflict := FindConflict(seen, candidate, ""); conflict != nil {
			return nil, fmt.Errorf("port mapping %s conflicts with another mapping in this same request", m.DisplayString())
		}
		seen = append(seen, candidate)
	}
	return exposureWarnings, nil
}

func previousHostIPForMapping(candidate PortMapping, previous []PortMapping) string {
	var match string
	count := 0
	for _, old := range previous {
		if old.ContainerPort == candidate.ContainerPort && NormalizeProtocol(old.Protocol) == NormalizeProtocol(candidate.Protocol) && normalizedRangeSize(old.RangeSize) == normalizedRangeSize(candidate.RangeSize) {
			match = old.HostIP
			count++
		}
	}
	if count == 1 {
		return match
	}
	return ""
}

func normalizedRangeSize(size int) int {
	if size <= 1 {
		return 1
	}
	return size
}

// executeRollback restores the original container's name and lifecycle
// after a failed mutation, returning a structured, truthfully-verified
// result. It never deletes the backup unless the restoration is verified,
// and it never claims success optimistically: RestoredLifecycle and
// Verified reflect what was actually observed afterward, not merely
// whether a command returned without error. candidateName is the identity
// the failed replacement container was created under — for a port mutation
// this is always the same as originalName, but adoption may create the
// replacement under a different (user-chosen) service name.
func (p *PodmanService) executeRollback(backupName, originalName, candidateIdentity, originalID, originalLifecycle string, candidateWasCreated bool) *RollbackResult {
	result := &RollbackResult{Attempted: true}

	if candidateWasCreated {
		stopErr := p.StopContainer(candidateIdentity)
		removeErr := p.forceRemoveContainer(candidateIdentity)
		containers, verifyErr := p.ListContainers(true)
		if verifyErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to verify candidate removal: %v", verifyErr))
		} else if findContainerByIdentity(containers, candidateIdentity) != nil {
			if stopErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("failed to stop failed candidate %q: %v", candidateIdentity, stopErr))
			}
			if removeErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("failed to remove failed candidate %q: %v", candidateIdentity, removeErr))
			}
			result.Errors = append(result.Errors, fmt.Sprintf("failed candidate %q still exists after removal", candidateIdentity))
		} else {
			result.RemovedCandidate = true
		}
	} else {
		result.RemovedCandidate = true
	}

	if !result.RemovedCandidate {
		result.Errors = append(result.Errors, "rollback stopped before restoring the backup because the failed candidate was not verified absent")
		return result
	}

	_, _, renameErr := p.runCommand("rename", backupName, originalName)
	containers, inspectErr := p.ListContainers(true)
	if inspectErr != nil {
		if renameErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to rename backup %q back to %q: %v", backupName, originalName, renameErr))
		}
		result.Errors = append(result.Errors, fmt.Sprintf("failed to verify restored container: %v", inspectErr))
		return result
	}
	c := findContainerByName(containers, originalName)
	if c == nil || (originalID != "" && c.Id != originalID) {
		if renameErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to rename backup %q back to %q: %v", backupName, originalName, renameErr))
		}
		if c == nil {
			result.Errors = append(result.Errors, fmt.Sprintf("original container %q not found after rollback", originalName))
		} else {
			result.Errors = append(result.Errors, fmt.Sprintf("container restored under %q has ID %q, expected original ID %q", originalName, c.Id, originalID))
		}
		return result
	}
	result.RestoredName = true

	var startErr error
	if originalLifecycle == lifecycleRunning {
		startErr = p.StartContainer(originalName)
	}
	var restored *Container
	observed := pollUntil(mutationPollAttempts, mutationPollInterval, func() bool {
		current, err := p.ListContainers(true)
		if err != nil {
			return false
		}
		candidate := findContainerByName(current, originalName)
		if candidate == nil || (originalID != "" && candidate.Id != originalID) {
			return false
		}
		kind, ok := classifyLifecycle(candidate.State)
		if !ok || kind != originalLifecycle {
			return false
		}
		restored = candidate
		return true
	})
	if !observed || restored == nil {
		if startErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to restart original container %q: %v", originalName, startErr))
		}
		result.Errors = append(result.Errors, fmt.Sprintf("original container %q did not verify in lifecycle %q after rollback", originalName, originalLifecycle))
		return result
	}
	result.RestoredLifecycle = true
	result.Verified = result.RestoredName && result.RestoredLifecycle && result.RemovedCandidate && len(result.Errors) == 0
	return result
}
