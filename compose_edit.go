package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ComposeFileDetails holds inspection details of a Docker/Podman Compose file.
type ComposeFileDetails struct {
	ContainerID  string        `json:"containerId"`
	WorkingDir   string        `json:"workingDir"`
	ComposeFile  string        `json:"composeFile"`
	Service      string        `json:"service"`
	Content      string        `json:"content"`
	PortMappings []PortMapping `json:"portMappings"`
}

// ErrMultipleComposeFiles is returned when a container's Compose provenance
// names more than one configuration file (a multi `-f` project). The
// effective project is the merge of all of them; editing only the first
// could miss where a service's ports: actually live, or silently conflict
// with an override file. Callers must block automatic mutation and fall
// back to manual guidance rather than guess which file to touch.
var ErrMultipleComposeFiles = errors.New("compose project is defined by multiple configuration files; automatic mutation is not safe")

// FindComposeFile locates the compose file from project metadata or directory scan.
func FindComposeFile(workingDir, projectConfigFiles string) (string, error) {
	if projectConfigFiles != "" {
		var files []string
		for _, f := range strings.Split(projectConfigFiles, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				files = append(files, f)
			}
		}

		if len(files) > 1 {
			return "", fmt.Errorf("%w: %s", ErrMultipleComposeFiles, strings.Join(files, ", "))
		}

		if len(files) == 1 {
			f := files[0]
			if filepath.IsAbs(f) {
				if _, err := os.Stat(f); err == nil {
					return f, nil
				}
			} else if workingDir != "" {
				p := filepath.Join(workingDir, f)
				if _, err := os.Stat(p); err == nil {
					return p, nil
				}
			}
		}
	}

	if workingDir != "" {
		candidates := []string{
			"compose.yaml",
			"compose.yml",
			"docker-compose.yaml",
			"docker-compose.yml",
			"podman-compose.yaml",
			"podman-compose.yml",
		}
		for _, cand := range candidates {
			p := filepath.Join(workingDir, cand)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}

	return "", fmt.Errorf("compose file not found in %s", workingDir)
}

// ParseComposePortEntry parses a single Compose port string (e.g. "8080:80", "127.0.0.1:8080:80/tcp").
func ParseComposePortEntry(entry string) *PortMapping {
	pm, err := ParsePublishSpec(entry)
	if err != nil {
		return nil
	}
	return pm
}

// composeLongFormKnownKeys are the long-form port attributes Podder's
// PortMapping model can faithfully represent and replay.
var composeLongFormKnownKeys = map[string]bool{
	"target":    true,
	"published": true,
	"host_ip":   true,
	"protocol":  true,
}

// composePortsUnsupportedLongFormKeys scans an existing ports: sequence node
// for long-form mapping entries carrying attributes Podder does not model
// (name, mode, app_protocol, ...). Rewriting such an entry via
// UpdateComposePorts would silently discard that metadata.
func composePortsUnsupportedLongFormKeys(portsNode *yaml.Node) []string {
	if portsNode == nil {
		return nil
	}
	seen := map[string]bool{}
	var unsupported []string
	for _, item := range portsNode.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i < len(item.Content)-1; i += 2 {
			key := item.Content[i].Value
			if !composeLongFormKnownKeys[key] && !seen[key] {
				seen[key] = true
				unsupported = append(unsupported, key)
			}
		}
	}
	return unsupported
}

// ParseComposePorts parses compose YAML to extract port mappings for a specific service.
func ParseComposePorts(composeYAML, serviceName string) ([]PortMapping, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &root); err != nil {
		return nil, fmt.Errorf("failed to parse compose YAML: %w", err)
	}

	if len(root.Content) == 0 {
		return []PortMapping{}, nil
	}

	servicesNode := findMapKeyNode(root.Content[0], "services")
	if servicesNode == nil {
		return []PortMapping{}, nil
	}

	serviceNode := findMapKeyNode(servicesNode, serviceName)
	if serviceNode == nil {
		return []PortMapping{}, nil
	}

	portsNode := findMapKeyNode(serviceNode, "ports")
	if portsNode == nil || portsNode.Kind != yaml.SequenceNode {
		return []PortMapping{}, nil
	}

	var mappings []PortMapping
	for _, item := range portsNode.Content {
		if item.Kind == yaml.ScalarNode {
			if pm := ParseComposePortEntry(item.Value); pm != nil {
				mappings = append(mappings, *pm)
			}
		} else if item.Kind == yaml.MappingNode {
			// Long-form port mapping: target: 80, published: 8080, host_ip: 127.0.0.1, protocol: tcp
			targetNode := findMapKeyNode(item, "target")
			publishedNode := findMapKeyNode(item, "published")
			hostIPNode := findMapKeyNode(item, "host_ip")
			protoNode := findMapKeyNode(item, "protocol")

			cPort := 0
			hPort := 0
			hIP := "0.0.0.0"
			proto := "tcp"

			if targetNode != nil {
				cPort, _ = strconv.Atoi(targetNode.Value)
			}
			if publishedNode != nil {
				hPort, _ = strconv.Atoi(publishedNode.Value)
			}
			if hostIPNode != nil {
				hIP = hostIPNode.Value
			}
			if protoNode != nil {
				proto = protoNode.Value
			}

			if cPort > 0 {
				mappings = append(mappings, PortMapping{
					HostIP:        hIP,
					HostPort:      uint16(hPort),
					ContainerPort: uint16(cPort),
					Protocol:      proto,
				})
			}
		}
	}

	return mappings, nil
}

// UpdateComposePorts updates the ports for a specific service in YAML,
// preserving structure (comments, anchors, aliases, x- extensions,
// unrelated services and keys) via a yaml.Node tree edit that only
// replaces the ports: sequence itself. If the existing ports: sequence uses
// long-form entries with attributes Podder's PortMapping model does not
// represent (name, mode, app_protocol, ...), it refuses rather than
// silently discarding that metadata.
func UpdateComposePorts(composeYAML, serviceName string, newPorts []PortMapping) (string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &root); err != nil {
		return "", fmt.Errorf("failed to parse compose YAML: %w", err)
	}

	if len(root.Content) == 0 {
		return "", fmt.Errorf("empty compose YAML")
	}

	docMap := root.Content[0]
	servicesNode := findMapKeyNode(docMap, "services")
	if servicesNode == nil {
		return "", fmt.Errorf("no 'services' key found in compose file")
	}

	serviceNode := findMapKeyNode(servicesNode, serviceName)
	if serviceNode == nil {
		return "", fmt.Errorf("service '%s' not found under 'services' in compose file", serviceName)
	}

	existingPortsNode := findMapKeyNode(serviceNode, "ports")
	if unsupported := composePortsUnsupportedLongFormKeys(existingPortsNode); len(unsupported) > 0 {
		return "", fmt.Errorf("refusing to rewrite ports: for service %q — existing entries use long-form attribute(s) %s that Podder does not model and would silently discard; edit this service's compose file manually", serviceName, strings.Join(unsupported, ", "))
	}

	// Build new ports sequence node
	newPortsSeq := &yaml.Node{
		Kind: yaml.SequenceNode,
		Tag:  "!!seq",
	}

	for _, m := range newPorts {
		newPortsSeq.Content = append(newPortsSeq.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: FormatPublishSpec(m),
		})
	}

	// Replace or insert "ports" key in service node
	replaced := false
	for i := 0; i < len(serviceNode.Content); i += 2 {
		keyNode := serviceNode.Content[i]
		if keyNode.Value == "ports" {
			serviceNode.Content[i+1] = newPortsSeq
			replaced = true
			break
		}
	}

	if !replaced {
		serviceNode.Content = append(serviceNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "ports"},
			newPortsSeq,
		)
	}

	var buf strings.Builder
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return "", fmt.Errorf("failed to re-encode compose YAML: %w", err)
	}

	return buf.String(), nil
}

func findMapKeyNode(mapNode *yaml.Node, key string) *yaml.Node {
	if mapNode == nil || mapNode.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(mapNode.Content); i += 2 {
		k := mapNode.Content[i]
		if k.Value == key && i+1 < len(mapNode.Content) {
			return mapNode.Content[i+1]
		}
	}
	return nil
}

// writeFileAtomicPreservingMode writes data to path via a same-directory
// temp file + rename, so a reader never observes a partially-written file,
// and preserves the original file's permission bits (Compose files can
// carry secrets via environment values) rather than hard-coding 0644.
func writeFileAtomicPreservingMode(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".podder-compose-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to finalize temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to set file permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to replace file: %w", err)
	}
	return nil
}

// InspectCompose discovers and parses the compose file for a given container.
func (p *PodmanService) InspectCompose(containerID string) (*ComposeFileDetails, error) {
	containers, err := p.ListContainers(true)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	target := findContainerByIdentity(containers, containerID)
	if target == nil {
		return nil, fmt.Errorf("container %s not found", containerID)
	}

	workingDir := target.Provenance.WorkingDir
	configFile := target.Provenance.ConfigFile
	serviceName := target.Provenance.Service

	if workingDir == "" && configFile == "" {
		return nil, fmt.Errorf("container %s has no compose provenance labels", containerID)
	}

	foundFile, err := FindComposeFile(workingDir, configFile)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(foundFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read compose file %s: %w", foundFile, err)
	}

	content := string(data)
	ports, _ := ParseComposePorts(content, serviceName)

	return &ComposeFileDetails{
		ContainerID:  target.Id,
		WorkingDir:   workingDir,
		ComposeFile:  foundFile,
		Service:      serviceName,
		Content:      content,
		PortMappings: ports,
	}, nil
}

// findComposeServiceContainer locates the container Podman/Compose most
// recently created for (project, service), used to verify a Compose apply
// actually took effect.
func findComposeServiceContainer(containers []Container, project, service string) *Container {
	for i := range containers {
		c := &containers[i]
		p := c.Provenance
		if p.Type != "compose" {
			continue
		}
		if p.Service == service && (project == "" || strings.HasPrefix(p.Name, project)) {
			return c
		}
	}
	return nil
}

// MutateComposePorts updates a compose file for a single service, applies
// it via the resolved compose provider (reusing the same argv construction
// and socket-readiness preflight as CLI passthrough and GUI actions), and
// verifies the result before treating the file change as final:
//
//  1. block automatically if the project spans multiple compose files, or
//     if the existing ports: entries use long-form attributes Podder can't
//     faithfully preserve
//  2. atomically rewrite only the target service's ports:, preserving the
//     original file's permissions, and keep the original content in memory
//     until verification succeeds
//  3. redeploy only the target service (`compose up -d SERVICE`), not the
//     whole project
//  4. verify the service's container exists with the exact candidate port
//     mappings before treating the apply as committed
//  5. on any failure, restore the original file content AND re-deploy from
//     it, verifying the original service is back before ever reporting
//     RolledBack=true
func (p *PodmanService) MutateComposePorts(containerID string, newPorts []PortMapping) (*PortMutationResult, error) {
	result := &PortMutationResult{
		Success: false,
		Steps:   []PortMutationStepResult{},
	}
	result.RequiresExternal = true
	result.Guidance = "Automatic in-place Compose mutation is disabled in this hardening build. Podder cannot yet prove complete effective-project identity, preserve every YAML construct, and verify rollback across provider implementations. Use the generated snippet and the authoritative Compose workflow manually."
	result.ComposeSnippet = GenerateComposeSnippet("service", newPorts)
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: result.Guidance})
	return result, nil

	details, err := p.InspectCompose(containerID)
	if err != nil {
		if errors.Is(err, ErrMultipleComposeFiles) {
			result.RequiresExternal = true
			result.Guidance = fmt.Sprintf("This Compose project is defined by multiple configuration files. Podder cannot safely determine which file is authoritative for this service's ports, so automatic mutation is disabled: %v. Please edit the appropriate file manually and re-run 'pod up'.", err)
			result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: result.Guidance})
			return result, nil
		}
		return nil, err
	}

	// 1. Preflight Validation
	for _, m := range newPorts {
		valReq := PortMappingRequest{
			HostIP:        m.HostIP,
			HostPort:      m.HostPort,
			ContainerPort: m.ContainerPort,
			Protocol:      m.Protocol,
			ContainerID:   containerID,
			RangeSize:     m.RangeSize,
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
			result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: errMsg})
			return result, nil
		}
	}

	fileInfo, err := os.Stat(details.ComposeFile)
	if err != nil {
		return nil, fmt.Errorf("failed to stat %s: %w", details.ComposeFile, err)
	}

	// Preflight-build the new content before touching anything, and refuse
	// automatically if it isn't safely representable.
	origContent, err := os.ReadFile(details.ComposeFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", details.ComposeFile, err)
	}

	newContent, err := UpdateComposePorts(string(origContent), details.Service, newPorts)
	if err != nil {
		result.RequiresExternal = true
		result.Guidance = err.Error()
		result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: err.Error()})
		return result, nil
	}

	provider, err := resolveComposeProviderWithLookPath("up", p.lookPathFn())
	if err != nil {
		return nil, err
	}
	if err := ensureComposeProviderReady(provider); err != nil {
		return nil, err
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "PREFLIGHT", Passed: true,
		Message: fmt.Sprintf("Preflight verified for service '%s' in %s", details.Service, details.ComposeFile),
	})

	// 2. Write the new file atomically, preserving permissions. The
	// original content is retained in memory (not merely a sibling backup
	// file) until verification succeeds.
	if err := writeFileAtomicPreservingMode(details.ComposeFile, []byte(newContent), fileInfo.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("failed to write updated compose file: %w", err)
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "SNAPSHOT", Passed: true,
		Message: "Compose file staged; original content retained in memory for rollback.",
	})

	rollback := func(reason string) (*PortMutationResult, error) {
		restoreErr := writeFileAtomicPreservingMode(details.ComposeFile, origContent, fileInfo.Mode().Perm())
		var errs []string
		if restoreErr != nil {
			errs = append(errs, fmt.Sprintf("failed to restore original compose file: %v", restoreErr))
		}

		verb, extra, _ := composeVerbAndArgs("up")
		rbArgs := provider.BuildArgs(details.ComposeFile, verb, extra, details.Service)
		_, rbStderr, rbErr := p.cmdRunner().Run(provider.path, rbArgs...)
		if rbErr != nil {
			errs = append(errs, fmt.Sprintf("failed to redeploy original service: %v (%s)", rbErr, strings.TrimSpace(rbStderr)))
		}

		verified := false
		if restoreErr == nil {
			time.Sleep(50 * time.Millisecond)
			if containers, lcErr := p.ListContainers(true); lcErr == nil {
				if c := findComposeServiceContainer(containers, "", details.Service); c != nil {
					if eq, _, _ := portMappingSetEqual(details.PortMappings, c.PortMappings); eq {
						verified = true
					} else {
						errs = append(errs, "original service is running again but its port mappings do not match the pre-mutation configuration")
					}
				} else {
					errs = append(errs, "original service container was not found after rollback redeploy")
				}
			} else {
				errs = append(errs, fmt.Sprintf("failed to verify rollback: %v", lcErr))
			}
		}

		result.RollbackReason = reason
		if verified {
			result.RolledBack = true
			result.Steps = append(result.Steps, PortMutationStepResult{Step: "ROLLED_BACK", Passed: true, Message: reason})
		} else {
			result.ManualRecoveryRequired = true
			result.Steps = append(result.Steps, PortMutationStepResult{
				Step: "ROLLBACK_FAILED", Passed: false,
				Message: fmt.Sprintf("ROLLBACK FAILED / MANUAL RECOVERY REQUIRED: %s (%s)", reason, strings.Join(errs, "; ")),
			})
		}
		return result, nil
	}

	// 3. Apply: redeploy only the target service.
	verb, extra, _ := composeVerbAndArgs("up")
	applyArgs := provider.BuildArgs(details.ComposeFile, verb, extra, details.Service)
	_, applyStderr, err := p.cmdRunner().Run(provider.path, applyArgs...)
	if err != nil {
		return rollback(fmt.Sprintf("Compose up failed: %v (%s)", err, strings.TrimSpace(applyStderr)))
	}

	// 4. Verify: the service's container exists with exactly the
	// candidate port mappings before treating this as committed.
	time.Sleep(50 * time.Millisecond)
	containers, err := p.ListContainers(true)
	if err != nil {
		return rollback(fmt.Sprintf("Failed to verify service after compose up: %v", err))
	}
	newContainer := findComposeServiceContainer(containers, "", details.Service)
	if newContainer == nil {
		return rollback("Service container did not appear after compose up.")
	}
	if eq, missing, unexpected := portMappingSetEqual(newPorts, newContainer.PortMappings); !eq {
		return rollback(fmt.Sprintf("Configured port mappings do not match after compose up (missing: %v, unexpected: %v).", missing, unexpected))
	}

	result.Success = true
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "COMMITTED", Passed: true,
		Message: fmt.Sprintf("Compose service '%s' successfully updated and re-deployed!", details.Service),
	})

	return result, nil
}
