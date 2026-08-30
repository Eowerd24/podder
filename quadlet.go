package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// QuadletFileDetails represents the contents and discovered ports of a systemd .container unit file.
type QuadletFileDetails struct {
	UnitName     string        `json:"unitName"`
	FilePath     string        `json:"filePath"`
	Exists       bool          `json:"exists"`
	Content      string        `json:"content"`
	PortMappings []PortMapping `json:"portMappings"`
}

func getQuadletSearchDirs() []string {
	var dirs []string
	home := os.Getenv("HOME")
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", "containers", "systemd"))
	}
	dirs = append(dirs, "/etc/containers/systemd")
	return dirs
}

// FindQuadletFile searches the standard Quadlet unit paths for a matching .container file.
func FindQuadletFile(unitName string) (string, error) {
	unitName = strings.TrimSpace(unitName)
	if unitName == "" {
		return "", fmt.Errorf("unit name cannot be empty")
	}

	baseName := strings.TrimSuffix(unitName, ".service")
	baseName = strings.TrimSuffix(baseName, ".container")

	candidates := []string{
		baseName + ".container",
		unitName,
	}

	for _, dir := range getQuadletSearchDirs() {
		for _, cand := range candidates {
			p := filepath.Join(dir, cand)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}

	return "", fmt.Errorf("quadlet unit file not found for %s", unitName)
}

// ParseQuadletContent parses a .container INI file content to extract PublishPort mappings.
func ParseQuadletContent(content string) []PortMapping {
	var mappings []PortMapping
	scanner := bufio.NewScanner(strings.NewReader(content))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(strings.ToLower(line), "publishport=") {
			val := strings.TrimSpace(line[len("publishport="):])
			// Parse format: [hostIP:][hostPort:]containerPort[/protocol]
			proto := "tcp"
			if idx := strings.Index(val, "/"); idx != -1 {
				proto = strings.ToLower(val[idx+1:])
				val = val[:idx]
			}

			parts := strings.Split(val, ":")
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
				mappings = append(mappings, PortMapping{
					HostIP:        hIP,
					HostPort:      uint16(hPort),
					ContainerPort: uint16(cPort),
					Protocol:      proto,
				})
			}
		}
	}

	return mappings
}

// UpdateQuadletContent replaces PublishPort lines under [Container] in a .container file.
func UpdateQuadletContent(content string, newPorts []PortMapping) string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	hasContainerSection := false
	inContainerSection := false
	containerSectionIndex := -1

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			sectionName := strings.ToLower(trimmed)
			if sectionName == "[container]" {
				hasContainerSection = true
				inContainerSection = true
				containerSectionIndex = len(lines)
			} else {
				inContainerSection = false
			}
		}

		// Skip existing PublishPort lines inside [Container]
		if inContainerSection && strings.HasPrefix(strings.ToLower(trimmed), "publishport=") {
			continue
		}

		lines = append(lines, line)
	}

	// Prepare new PublishPort lines
	var newPortLines []string
	for _, m := range newPorts {
		bind := m.HostIP
		proto := strings.ToLower(m.Protocol)
		if proto == "" {
			proto = "tcp"
		}
		if bind == "" || bind == "0.0.0.0" || bind == "*" {
			newPortLines = append(newPortLines, fmt.Sprintf("PublishPort=%d:%d/%s", m.HostPort, m.ContainerPort, proto))
		} else {
			newPortLines = append(newPortLines, fmt.Sprintf("PublishPort=%s:%d:%d/%s", bind, m.HostPort, m.ContainerPort, proto))
		}
	}

	if !hasContainerSection {
		lines = append([]string{"[Container]"}, append(newPortLines, lines...)...)
	} else {
		// Insert new port lines right after [Container]
		var resultLines []string
		for i, l := range lines {
			resultLines = append(resultLines, l)
			if i == containerSectionIndex {
				resultLines = append(resultLines, newPortLines...)
			}
		}
		lines = resultLines
	}

	return strings.Join(lines, "\n") + "\n"
}

// InspectQuadlet returns discovered details and ports of a Quadlet file.
func (p *PodmanService) InspectQuadlet(unitName string) (*QuadletFileDetails, error) {
	filePath, err := FindQuadletFile(unitName)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read quadlet file %s: %w", filePath, err)
	}

	content := string(data)
	ports := ParseQuadletContent(content)

	return &QuadletFileDetails{
		UnitName:     unitName,
		FilePath:     filePath,
		Exists:       true,
		Content:      content,
		PortMappings: ports,
	}, nil
}

// MutateQuadletPorts safely edits the .container unit file, reloads systemd, and restarts the service.
func (p *PodmanService) MutateQuadletPorts(unitName string, newPorts []PortMapping) (*PortMutationResult, error) {
	result := &PortMutationResult{
		Success: false,
		Steps:   []PortMutationStepResult{},
	}

	filePath, err := FindQuadletFile(unitName)
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
		Message: fmt.Sprintf("Preflight verified for %s", filePath),
	})

	// 2. Read and Backup File
	origContent, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", filePath, err)
	}

	backupPath := fmt.Sprintf("%s.bak-%d", filePath, time.Now().Unix())
	if err := os.WriteFile(backupPath, origContent, 0o644); err != nil {
		return nil, fmt.Errorf("failed to create backup file %s: %w", backupPath, err)
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step:    "SNAPSHOT",
		Passed:  true,
		Message: fmt.Sprintf("Created backup file %s", filepath.Base(backupPath)),
	})

	// 3. Update File Content
	newContent := UpdateQuadletContent(string(origContent), newPorts)
	if err := os.WriteFile(filePath, []byte(newContent), 0o644); err != nil {
		_ = os.Remove(backupPath)
		return nil, fmt.Errorf("failed to write updated quadlet file: %w", err)
	}

	// 4. Systemd Daemon-Reload and Restart
	systemctlService := strings.TrimSuffix(filepath.Base(filePath), ".container") + ".service"
	
	cmdReload := exec.Command("systemctl", "--user", "daemon-reload")
	if out, err := cmdReload.CombinedOutput(); err != nil {
		// Rollback file
		_ = os.WriteFile(filePath, origContent, 0o644)
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		result.RolledBack = true
		result.RollbackReason = fmt.Sprintf("systemctl daemon-reload failed: %v (%s)", err, strings.TrimSpace(string(out)))
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "ROLLED_BACK",
			Passed:  false,
			Message: result.RollbackReason,
		})
		return result, nil
	}

	cmdRestart := exec.Command("systemctl", "--user", "restart", systemctlService)
	if out, err := cmdRestart.CombinedOutput(); err != nil {
		// Rollback file and restart original
		_ = os.WriteFile(filePath, origContent, 0o644)
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		_ = exec.Command("systemctl", "--user", "restart", systemctlService).Run()
		result.RolledBack = true
		result.RollbackReason = fmt.Sprintf("systemctl restart %s failed: %v (%s)", systemctlService, err, strings.TrimSpace(string(out)))
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "ROLLED_BACK",
			Passed:  false,
			Message: result.RollbackReason,
		})
		return result, nil
	}

	// 5. Verification
	time.Sleep(1 * time.Second)
	cmdStatus := exec.Command("systemctl", "--user", "is-active", systemctlService)
	statusOut, _ := cmdStatus.CombinedOutput()
	if strings.TrimSpace(string(statusOut)) != "active" {
		// Rollback
		_ = os.WriteFile(filePath, origContent, 0o644)
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		_ = exec.Command("systemctl", "--user", "restart", systemctlService).Run()
		result.RolledBack = true
		result.RollbackReason = fmt.Sprintf("Unit %s is not active after restart (status: %s)", systemctlService, strings.TrimSpace(string(statusOut)))
		result.Steps = append(result.Steps, PortMutationStepResult{
			Step:    "ROLLED_BACK",
			Passed:  false,
			Message: result.RollbackReason,
		})
		return result, nil
	}

	// 6. Commit: Clean up backup
	_ = os.Remove(backupPath)

	result.Success = true
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step:    "COMMITTED",
		Passed:  true,
		Message: fmt.Sprintf("Quadlet unit %s updated and restarted successfully!", systemctlService),
	})

	return result, nil
}
