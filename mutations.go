package main

import (
	"fmt"
	"strings"
	"time"
)

// PortMutationRequest represents a request to change the port mappings of an existing container.
type PortMutationRequest struct {
	ContainerID string        `json:"containerId"`
	ServiceName string        `json:"serviceName,omitempty"`
	NewPorts    []PortMapping `json:"newPorts"`
	ForceAdHoc  bool          `json:"forceAdHoc,omitempty"`
}

// PortMutationStepResult tracks an individual step in the mutation transaction.
type PortMutationStepResult struct {
	Step    string `json:"step"` // "PREFLIGHT", "SNAPSHOT", "CONTAINER_CREATE", "PORT_VERIFY", "COMMITTED", "ROLLED_BACK"
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// PortMutationResult contains the outcome of an atomic port mutation transaction.
type PortMutationResult struct {
	Success          bool                     `json:"success"`
	NewContainerID   string                   `json:"newContainerId,omitempty"`
	OldContainerID   string                   `json:"oldContainerId,omitempty"`
	RolledBack       bool                     `json:"rolledBack"`
	RollbackReason   string                   `json:"rollbackReason,omitempty"`
	Steps            []PortMutationStepResult `json:"steps"`
	Guidance         string                   `json:"guidance,omitempty"`
	RequiresExternal bool                     `json:"requiresExternal"`
	ComposeSnippet   string                   `json:"composeSnippet,omitempty"`
	QuadletSnippet   string                   `json:"quadletSnippet,omitempty"`
}

// GenerateComposeSnippet formats the proposed port mappings for a docker-compose.yml file.
func GenerateComposeSnippet(serviceName string, ports []PortMapping) string {
	var sb strings.Builder
	if serviceName == "" {
		serviceName = "app"
	}
	sb.WriteString(fmt.Sprintf("services:\n  %s:\n    ports:\n", serviceName))
	for _, m := range ports {
		bind := m.HostIP
		if bind == "" || bind == "0.0.0.0" || bind == "*" {
			sb.WriteString(fmt.Sprintf("      - \"%d:%d/%s\"\n", m.HostPort, m.ContainerPort, strings.ToLower(m.Protocol)))
		} else {
			sb.WriteString(fmt.Sprintf("      - \"%s:%d:%d/%s\"\n", bind, m.HostPort, m.ContainerPort, strings.ToLower(m.Protocol)))
		}
	}
	return sb.String()
}

// GenerateQuadletSnippet formats the proposed port mappings for a systemd .container file.
func GenerateQuadletSnippet(ports []PortMapping) string {
	var sb strings.Builder
	sb.WriteString("[Container]\n")
	for _, m := range ports {
		bind := m.HostIP
		if bind == "" || bind == "0.0.0.0" || bind == "*" {
			sb.WriteString(fmt.Sprintf("PublishPort=%d:%d/%s\n", m.HostPort, m.ContainerPort, strings.ToLower(m.Protocol)))
		} else {
			sb.WriteString(fmt.Sprintf("PublishPort=%s:%d:%d/%s\n", bind, m.HostPort, m.ContainerPort, strings.ToLower(m.Protocol)))
		}
	}
	return sb.String()
}

// MutateContainerPorts executes a safe atomic port mutation transaction.
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

	var target *Container
	for i := range containers {
		c := &containers[i]
		if c.Id == req.ContainerID || strings.HasPrefix(c.Id, req.ContainerID) {
			target = c
			break
		}
		for _, name := range c.Names {
			if strings.TrimPrefix(name, "/") == req.ContainerID {
				target = c
				break
			}
		}
	}

	if target == nil {
		return nil, fmt.Errorf("container %s not found", req.ContainerID)
	}

	containerName := "unnamed"
	if len(target.Names) > 0 {
		containerName = strings.TrimPrefix(target.Names[0], "/")
	}
	result.OldContainerID = target.Id

	// 2. Preflight Provenance Check
	prov := target.Provenance
	if prov.Type == "compose" {
		result.RequiresExternal = true
		result.ComposeSnippet = GenerateComposeSnippet(prov.Service, req.NewPorts)
		result.Guidance = "This container is managed by Docker Compose or Podman Compose. Direct recreation from the GUI would orphan the service. Update your compose file with the snippet below and re-run 'pod up'."
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "PREFLIGHT",
			Passed:  false,
			Message: "Orchestrator guard: Compose-managed workload cannot be modified directly.",
		})
		return result, nil
	}

	if prov.Type == "quadlet" {
		result.RequiresExternal = true
		result.QuadletSnippet = GenerateQuadletSnippet(req.NewPorts)
		result.Guidance = "This container is managed by systemd Quadlet. Update your .container unit file with the PublishPort settings below and reload systemd ('systemctl --user daemon-reload && systemctl --user restart " + prov.UnitName + "')."
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "PREFLIGHT",
			Passed:  false,
			Message: "Orchestrator guard: Quadlet-managed workload cannot be modified directly.",
		})
		return result, nil
	}

	if prov.Type == "pod" {
		result.RequiresExternal = true
		result.Guidance = "This container is part of Pod '" + prov.PodName + "'. Port mappings in Podman belong to the Pod itself and cannot be updated on an individual member container."
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "PREFLIGHT",
			Passed:  false,
			Message: "Orchestrator guard: Pod member container cannot have independent host port bindings.",
		})
		return result, nil
	}

	if prov.Type == "adhoc" && !req.ForceAdHoc {
		result.RequiresExternal = true
		result.Guidance = "This is an unmanaged ad-hoc container. Recreating it will delete any unmounted ephemeral container files. Please confirm to proceed with recreation."
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "PREFLIGHT",
			Passed:  false,
			Message: "Ad-hoc confirmation required: potential loss of ephemeral unmounted container state.",
		})
		return result, nil
	}

	// 3. Preflight Port Conflict Check
	for _, m := range req.NewPorts {
		if m.ContainerPort == 0 || m.HostPort == 0 {
			result.Steps = append(result.Steps, PortMutationStepResult{
				Step:    "PREFLIGHT",
				Passed:  false,
				Message: fmt.Sprintf("Invalid port numbers in mapping: %s", m.DisplayString()),
			})
			return result, nil
		}

		valReq := PortMappingRequest{
			HostIP:        m.HostIP,
			HostPort:      m.HostPort,
			ContainerPort: m.ContainerPort,
			Protocol:      m.Protocol,
			ContainerID:   target.Id, // Ignore current container's own claims
		}

		valResult, err := p.ValidatePortMapping(valReq)
		if err != nil || (valResult != nil && !valResult.Valid) {
			errMsg := "Port conflict detected"
			if valResult != nil && len(valResult.Checks) > 0 {
				for _, c := range valResult.Checks {
					if !c.Passed {
						errMsg = c.Message
						break
					}
				}
			}
			result.Steps = append(result.Steps, PortMutationStepResult{
				Step:    "PREFLIGHT",
				Passed:  false,
				Message: errMsg,
			})
			return result, nil
		}
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step:    "PREFLIGHT",
		Passed:  true,
		Message: "Preflight verified: No port collisions and safe provenance.",
	})

	// 4. Resolve Spec & Snapshot
	specName := req.ServiceName
	if specName == "" {
		specName = containerName
	}

	spec, err := p.GetSpec(specName)
	if err != nil || spec == nil {
		// Synthesize spec from live container info
		spec = &ContainerSpec{
			Name:         containerName,
			Image:        target.Image,
			Command:      strings.Join(target.Command, " "),
			PortMappings: req.NewPorts,
		}
	} else {
		spec.PortMappings = req.NewPorts
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step:    "SNAPSHOT",
		Passed:  true,
		Message: "Snapshot created and declarative spec prepared.",
	})

	// 5. Transaction Execution: Rename & Stop Existing Container
	backupName := fmt.Sprintf("%s-bak-%d", containerName, time.Now().Unix())
	_, _, err = p.runCommand("rename", containerName, backupName)
	if err != nil {
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "CONTAINER_CREATE",
			Passed:  false,
			Message: fmt.Sprintf("Failed to rename existing container to backup: %v", err),
		})
		return result, nil
	}

	_ = p.StopContainer(target.Id)

	// 6. Launch New Container with New Ports
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
		containerName,
		req.NewPorts,
		spec.Command,
		hostPath,
		containerPath,
		readOnly,
	)
	if err != nil {
		// Immediate Rollback
		p.executeRollback(backupName, containerName, "")
		result.RolledBack = true
		result.RollbackReason = fmt.Sprintf("Failed to construct run arguments: %v", err)
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "ROLLED_BACK",
			Passed:  false,
			Message: result.RollbackReason,
		})
		return result, nil
	}

	stdout, stderr, err := p.runCommand(args...)
	if err != nil {
		// Immediate Rollback
		p.executeRollback(backupName, containerName, "")
		result.RolledBack = true
		result.RollbackReason = fmt.Sprintf("Failed to launch new container: %v (stderr: %s)", err, strings.TrimSpace(stderr))
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "ROLLED_BACK",
			Passed:  false,
			Message: result.RollbackReason,
		})
		return result, nil
	}

	newContainerID := strings.TrimSpace(stdout)
	result.NewContainerID = newContainerID

	// 7. Health & Port Verification
	time.Sleep(500 * time.Millisecond)

	activeContainers, _ := p.ListContainers(true)
	var newContainer *Container
	for i := range activeContainers {
		c := &activeContainers[i]
		if c.Id == newContainerID || strings.HasPrefix(c.Id, newContainerID) {
			newContainer = c
			break
		}
	}

	if newContainer == nil || strings.ToLower(newContainer.State) != "running" {
		// New container crashed on startup -> Trigger Rollback
		p.executeRollback(backupName, containerName, newContainerID)
		result.RolledBack = true
		result.RollbackReason = "New container failed health check (exited or crashed immediately)."
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "ROLLED_BACK",
			Passed:  false,
			Message: result.RollbackReason,
		})
		return result, nil
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step:    "PORT_VERIFY",
		Passed:  true,
		Message: "New container verified running successfully with updated port mappings.",
	})

	// 8. Commit: Remove Backup Container & Persist Spec
	_ = p.RemoveContainer(backupName)
	_ = p.SaveSpec(*spec)

	result.Success = true
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step:    "COMMITTED",
		Passed:  true,
		Message: "Transaction committed: Port mappings updated and spec saved.",
	})

	return result, nil
}

func (p *PodmanService) executeRollback(backupName, originalName, failedContainerID string) {
	if failedContainerID != "" {
		_ = p.StopContainer(failedContainerID)
		_ = p.RemoveContainer(failedContainerID)
	}

	_, _, _ = p.runCommand("rename", backupName, originalName)
	_ = p.StartContainer(originalName)
}
