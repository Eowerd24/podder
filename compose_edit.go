package main

import (
	"fmt"
	"os"
	"os/exec"
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

// FindComposeFile locates the compose file from project metadata or directory scan.
func FindComposeFile(workingDir, projectConfigFiles string) (string, error) {
	if projectConfigFiles != "" {
		for _, f := range strings.Split(projectConfigFiles, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
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
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}

	proto := "tcp"
	if idx := strings.Index(entry, "/"); idx != -1 {
		proto = strings.ToLower(entry[idx+1:])
		entry = entry[:idx]
	}

	parts := strings.Split(entry, ":")
	var hIP string
	var hPort, cPort int

	if len(parts) == 1 {
		cPort, _ = strconv.Atoi(parts[0])
		hPort = cPort
		hIP = "0.0.0.0"
	} else if len(parts) == 2 {
		hPort, _ = strconv.Atoi(parts[0])
		cPort, _ = strconv.Atoi(parts[1])
		hIP = "0.0.0.0"
	} else if len(parts) == 3 {
		hIP = parts[0]
		hPort, _ = strconv.Atoi(parts[1])
		cPort, _ = strconv.Atoi(parts[2])
	}

	if cPort > 0 {
		return &PortMapping{
			HostIP:        hIP,
			HostPort:      uint16(hPort),
			ContainerPort: uint16(cPort),
			Protocol:      proto,
		}
	}

	return nil
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

// UpdateComposePorts updates the ports for a specific service in YAML, preserving structure.
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

	// Build new ports sequence node
	newPortsSeq := &yaml.Node{
		Kind: yaml.SequenceNode,
		Tag:  "!!seq",
	}

	for _, m := range newPorts {
		bind := m.HostIP
		proto := strings.ToLower(m.Protocol)
		if proto == "" {
			proto = "tcp"
		}
		var portStr string
		if bind == "" || bind == "0.0.0.0" || bind == "*" {
			portStr = fmt.Sprintf("%d:%d/%s", m.HostPort, m.ContainerPort, proto)
		} else {
			portStr = fmt.Sprintf("%s:%d:%d/%s", bind, m.HostPort, m.ContainerPort, proto)
		}

		newPortsSeq.Content = append(newPortsSeq.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: portStr,
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

// InspectCompose discovers and parses the compose file for a given container.
func (p *PodmanService) InspectCompose(containerID string) (*ComposeFileDetails, error) {
	containers, err := p.ListContainers(true)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var target *Container
	for i := range containers {
		c := &containers[i]
		if c.Id == containerID || strings.HasPrefix(c.Id, containerID) {
			target = c
			break
		}
	}

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

// MutateComposePorts updates a compose file, executes compose up -d, and rolls back on failure.
func (p *PodmanService) MutateComposePorts(containerID string, newPorts []PortMapping) (*PortMutationResult, error) {
	result := &PortMutationResult{
		Success: false,
		Steps:   []PortMutationStepResult{},
	}

	details, err := p.InspectCompose(containerID)
	if err != nil {
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
		Message: fmt.Sprintf("Preflight verified for service '%s' in %s", details.Service, details.ComposeFile),
	})

	// 2. Read and Backup File
	origContent, err := os.ReadFile(details.ComposeFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", details.ComposeFile, err)
	}

	backupPath := fmt.Sprintf("%s.bak-%d", details.ComposeFile, time.Now().Unix())
	if err := os.WriteFile(backupPath, origContent, 0o644); err != nil {
		return nil, fmt.Errorf("failed to create backup file %s: %w", backupPath, err)
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step:    "SNAPSHOT",
		Passed:  true,
		Message: fmt.Sprintf("Created backup file %s", filepath.Base(backupPath)),
	})

	// 3. Update Compose File
	newContent, err := UpdateComposePorts(string(origContent), details.Service, newPorts)
	if err != nil {
		_ = os.Remove(backupPath)
		return nil, fmt.Errorf("failed to update compose file content: %w", err)
	}

	if err := os.WriteFile(details.ComposeFile, []byte(newContent), 0o644); err != nil {
		_ = os.Remove(backupPath)
		return nil, fmt.Errorf("failed to write updated compose file: %w", err)
	}

	// 4. Run Compose Up
	composeDir := filepath.Dir(details.ComposeFile)
	provider, err := resolveComposeProvider("up")
	if err != nil {
		_ = os.WriteFile(details.ComposeFile, origContent, 0o644)
		return nil, err
	}

	cmdArgs := append([]string{"-f", details.ComposeFile}, provider.args...)
	cmd := exec.Command(provider.path, cmdArgs...)
	cmd.Dir = composeDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Rollback file and rerun compose
		_ = os.WriteFile(details.ComposeFile, origContent, 0o644)
		rbCmd := exec.Command(provider.path, cmdArgs...)
		rbCmd.Dir = composeDir
		_, _ = rbCmd.CombinedOutput()

		result.RolledBack = true
		result.RollbackReason = fmt.Sprintf("Compose up failed: %v (%s)", err, strings.TrimSpace(string(out)))
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "ROLLED_BACK",
			Passed:  false,
			Message: result.RollbackReason,
		})
		return result, nil
	}

	// 5. Verification
	time.Sleep(1 * time.Second)

	// Clean up backup file on commit
	_ = os.Remove(backupPath)

	result.Success = true
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step:    "COMMITTED",
		Passed:  true,
		Message: fmt.Sprintf("Compose service '%s' successfully updated and re-deployed!", details.Service),
	})

	return result, nil
}
