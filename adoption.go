package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AdoptionAssessment models the safety analysis and proposed spec for adopting a container.
type AdoptionAssessment struct {
	ContainerID   string        `json:"containerId"`
	ContainerName string        `json:"containerName"`
	CanAdopt      bool          `json:"canAdopt"`
	Blockers      []string      `json:"blockers,omitempty"`
	Warnings      []string      `json:"warnings,omitempty"`
	ProposedSpec  ContainerSpec `json:"proposedSpec"`
	RawInspect    string        `json:"rawInspect,omitempty"`
}

// AdoptionResult represents the outcome of an adoption transaction.
type AdoptionResult struct {
	Success     bool          `json:"success"`
	ServiceName string        `json:"serviceName"`
	Spec        ContainerSpec `json:"spec"`
	Message     string        `json:"message"`
}

// Raw inspect structures
type inspectContainer struct {
	Id      string `json:"Id"`
	Name    string `json:"Name"`
	Image   string `json:"Image"`
	Path    string `json:"Path"`
	Args    []string `json:"Args"`
	Config  struct {
		Image      string            `json:"Image"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint []string          `json:"Entrypoint"`
		Env        []string          `json:"Env"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
		Binds         []string `json:"Binds"`
		Privileged    bool     `json:"Privileged"`
		NetworkMode   string   `json:"NetworkMode"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	Pod string `json:"Pod"`
}

// ParseInspectToAssessment converts podman inspect JSON into an AdoptionAssessment.
func ParseInspectToAssessment(inspectJSON []byte) (*AdoptionAssessment, error) {
	var list []inspectContainer
	if err := json.Unmarshal(inspectJSON, &list); err != nil {
		return nil, fmt.Errorf("failed to parse inspect JSON: %w", err)
	}

	if len(list) == 0 {
		return nil, fmt.Errorf("no container details found in inspect JSON")
	}

	raw := list[0]
	containerName := strings.TrimPrefix(raw.Name, "/")

	assessment := &AdoptionAssessment{
		ContainerID:   raw.Id,
		ContainerName: containerName,
		CanAdopt:      true,
		Blockers:      []string{},
		Warnings:      []string{},
	}

	// Provenance classification
	prov := ClassifyProvenance(raw.Config.Labels, raw.Pod, "")
	if prov.Type == "compose" {
		assessment.CanAdopt = false
		assessment.Blockers = append(assessment.Blockers, "Container is managed by Docker/Podman Compose. Adopt via Compose directly.")
	} else if prov.Type == "quadlet" {
		assessment.CanAdopt = false
		assessment.Blockers = append(assessment.Blockers, "Container is managed by systemd Quadlet. Unit file is the source of truth.")
	} else if prov.Type == "pod" {
		assessment.CanAdopt = false
		assessment.Blockers = append(assessment.Blockers, fmt.Sprintf("Container is a member of Pod '%s'. Adopt the Pod rather than an individual member.", prov.PodName))
	} else if prov.Type == "podder" {
		assessment.Warnings = append(assessment.Warnings, "Container is already managed by Podder.")
	}

	if raw.HostConfig.Privileged {
		assessment.Warnings = append(assessment.Warnings, "Container is running with elevated --privileged permissions.")
	}
	if raw.HostConfig.NetworkMode == "host" {
		assessment.Warnings = append(assessment.Warnings, "Container is running in host network mode (ports are not isolated).")
	}

	// 1. Port mappings
	var portMappings []PortMapping
	for portKey, bindings := range raw.HostConfig.PortBindings {
		parts := strings.Split(portKey, "/")
		cPort, _ := strconv.Atoi(parts[0])
		proto := "tcp"
		if len(parts) > 1 {
			proto = parts[1]
		}

		for _, b := range bindings {
			hPort, _ := strconv.Atoi(b.HostPort)
			hIP := b.HostIP
			if hIP == "" {
				hIP = "0.0.0.0"
			}
			portMappings = append(portMappings, PortMapping{
				HostIP:        hIP,
				HostPort:      uint16(hPort),
				ContainerPort: uint16(cPort),
				Protocol:      proto,
			})
		}
	}

	// 2. Bind mounts
	var binds []BindMountSpec
	for _, m := range raw.Mounts {
		if m.Type == "bind" {
			binds = append(binds, BindMountSpec{
				HostPath:      m.Source,
				ContainerPath: m.Destination,
				ReadOnly:      !m.RW,
			})
		} else if m.Type == "volume" {
			assessment.Warnings = append(assessment.Warnings, fmt.Sprintf("Volume '%s' mounted to '%s' (named/anonymous volume state).", m.Source, m.Destination))
		}
	}

	// 3. Environment variables (filter standard runtime defaults)
	envMap := make(map[string]string)
	for _, e := range raw.Config.Env {
		kv := strings.SplitN(e, "=", 2)
		if len(kv) == 2 {
			k := kv[0]
			v := kv[1]
			// Omit standard base image envs
			if k != "PATH" && k != "HOSTNAME" && k != "HOME" && k != "TERM" {
				envMap[k] = v
			}
		}
	}

	// 4. Command
	cmdStr := ""
	if len(raw.Config.Cmd) > 0 {
		cmdStr = strings.Join(raw.Config.Cmd, " ")
	}

	imageName := raw.Config.Image
	if imageName == "" {
		imageName = raw.Image
	}

	assessment.ProposedSpec = ContainerSpec{
		Name:         containerName,
		Image:        imageName,
		PortMappings: portMappings,
		Binds:        binds,
		Env:          envMap,
		Command:      cmdStr,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	return assessment, nil
}

// InspectContainerForAdoption returns an analysis and proposed declarative spec for a container.
func (p *PodmanService) InspectContainerForAdoption(containerID string) (*AdoptionAssessment, error) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return nil, fmt.Errorf("container id cannot be empty")
	}

	stdout, stderr, err := p.runCommand("inspect", "--format", "json", containerID)
	if err != nil {
		return nil, fmt.Errorf("podman inspect failed: %v (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	assessment, err := ParseInspectToAssessment([]byte(stdout))
	if err != nil {
		return nil, err
	}

	return assessment, nil
}

// AdoptContainer persists the container's declarative spec and converts it to Podder-managed.
func (p *PodmanService) AdoptContainer(containerID string, serviceName string) (*AdoptionResult, error) {
	assessment, err := p.InspectContainerForAdoption(containerID)
	if err != nil {
		return nil, fmt.Errorf("adoption preflight failed: %w", err)
	}

	if !assessment.CanAdopt {
		return nil, fmt.Errorf("container cannot be adopted: %s", strings.Join(assessment.Blockers, "; "))
	}

	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		serviceName = assessment.ContainerName
	}
	assessment.ProposedSpec.Name = serviceName

	// 1. Save declarative spec under ~/.config/podder/services/<serviceName>.json
	if err := p.SaveSpec(assessment.ProposedSpec); err != nil {
		return nil, fmt.Errorf("failed to save adopted container spec: %w", err)
	}

	// 2. Recreate / mutate container with Podder labels to formalize ownership
	_, _ = p.MutateContainerPorts(PortMutationRequest{
		ContainerID: containerID,
		ServiceName: serviceName,
		NewPorts:    assessment.ProposedSpec.PortMappings,
		ForceAdHoc:  true,
	})

	return &AdoptionResult{
		Success:     true,
		ServiceName: serviceName,
		Spec:        assessment.ProposedSpec,
		Message:     fmt.Sprintf("Workload '%s' successfully adopted into Podder!", serviceName),
	}, nil
}
