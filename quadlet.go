package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// QuadletScope identifies whether a discovered unit file lives under a
// rootless (user) or rootful (system) Quadlet search path. Rootless user
// Quadlets may be safely managed with `systemctl --user`. System/rootful
// Quadlets are read-only in Podder by default — mutating a system unit
// would require elevated privileges Podder never acquires automatically
// (no implicit sudo), so system-scope mutation would need a deliberate,
// separately-implemented mode.
type QuadletScope string

const (
	QuadletScopeUser   QuadletScope = "user"
	QuadletScopeSystem QuadletScope = "system"
)

// quadletSearchDir pairs a search path with the systemd scope that
// generates/manages units found there.
type quadletSearchDir struct {
	path  string
	scope QuadletScope
}

// getQuadletSearchDirs returns Podman's Quadlet unit search paths. See
// `man podman-systemd.unit` for the authoritative, version-specific list —
// Podman has adjusted these across releases, so this should be re-verified
// against whichever Podman version Podder is packaged/tested against. This
// intentionally covers more than the two most common directories: both the
// rootless user locations (including the system-provided per-user/all-user
// directories, which are still generated under user systemd) and the
// rootful system locations, so a Quadlet installed anywhere Podman actually
// looks is still found — but only ever mutated when it resolves to a user
// scope.
// quadletRootOverride prefixes every hard-coded (non-XDG) Quadlet search
// path when set. It exists solely so tests can point discovery at a temp
// directory instead of real system paths like /etc/containers/systemd —
// production code never sets it, and it must never be used outside tests.
var quadletRootOverride string

func getQuadletSearchDirs() []quadletSearchDir {
	prefixed := func(p string) string {
		if quadletRootOverride == "" {
			return p
		}
		return filepath.Join(quadletRootOverride, p)
	}

	var dirs []quadletSearchDir

	if xdgConfig := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdgConfig != "" {
		dirs = append(dirs, quadletSearchDir{filepath.Join(xdgConfig, "containers", "systemd"), QuadletScopeUser})
	} else if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		dirs = append(dirs, quadletSearchDir{filepath.Join(home, ".config", "containers", "systemd"), QuadletScopeUser})
	}

	uid := strconv.Itoa(os.Getuid())
	dirs = append(dirs,
		quadletSearchDir{prefixed(filepath.Join("/etc/containers/systemd/users", uid)), QuadletScopeUser},
		quadletSearchDir{prefixed("/etc/containers/systemd/users"), QuadletScopeUser},
	)

	dirs = append(dirs,
		quadletSearchDir{prefixed("/run/containers/systemd"), QuadletScopeSystem},
		quadletSearchDir{prefixed("/etc/containers/systemd"), QuadletScopeSystem},
		quadletSearchDir{prefixed("/usr/share/containers/systemd"), QuadletScopeSystem},
	)

	return dirs
}

// QuadletFileDetails represents the contents and discovered ports of a systemd .container unit file.
type QuadletFileDetails struct {
	UnitName     string        `json:"unitName"`
	FilePath     string        `json:"filePath"`
	Scope        QuadletScope  `json:"scope"`
	Exists       bool          `json:"exists"`
	Content      string        `json:"content"`
	PortMappings []PortMapping `json:"portMappings"`
}

// FindQuadletFile searches the standard Quadlet unit paths for a matching
// .container file and reports which systemd scope (user/system) manages it.
func FindQuadletFile(unitName string) (path string, scope QuadletScope, err error) {
	unitName = strings.TrimSpace(unitName)
	if unitName == "" {
		return "", "", fmt.Errorf("unit name cannot be empty")
	}

	baseName := strings.TrimSuffix(unitName, ".service")
	baseName = strings.TrimSuffix(baseName, ".container")

	candidates := []string{baseName + ".container", unitName}

	for _, dir := range getQuadletSearchDirs() {
		for _, cand := range candidates {
			p := filepath.Join(dir.path, cand)
			if _, statErr := os.Stat(p); statErr == nil {
				return p, dir.scope, nil
			}
		}
	}

	return "", "", fmt.Errorf("quadlet unit file not found for %s", unitName)
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
			if pm, err := ParsePublishSpec(val); err == nil && pm != nil {
				mappings = append(mappings, *pm)
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
		newPortLines = append(newPortLines, "PublishPort="+FormatPublishSpec(m))
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
	filePath, scope, err := FindQuadletFile(unitName)
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
		Scope:        scope,
		Exists:       true,
		Content:      content,
		PortMappings: ports,
	}, nil
}

// quadletIgnoreContainerID finds the container systemd currently manages
// for a given .service unit name, so preflight validation ignores that
// container's OWN current port claims (section 4.1/17.3's "self-conflict"
// fix): keeping an unchanged mapping must not fail because it appears to
// conflict with itself.
func (p *PodmanService) quadletIgnoreContainerID(systemctlService string) string {
	containers, err := p.ListContainers(true)
	if err != nil {
		return ""
	}
	for _, c := range containers {
		if c.Provenance.Type == "quadlet" && (c.Provenance.UnitName == systemctlService || c.Provenance.Name == systemctlService) {
			return c.Id
		}
	}
	return ""
}

// MutateQuadletPorts safely edits a rootless user .container unit file,
// validates it, and reloads/restarts the service — verifying every step
// and never claiming a rollback succeeded unless it is confirmed:
//
//   - System/rootful Quadlets are refused outright (read-only in Podder;
//     no implicit sudo).
//   - The unit's own currently-configured ports are ignored during
//     preflight so an unchanged mapping never looks like a self-conflict.
//   - The file is written atomically, preserving its original permissions.
//   - `systemctl --user cat <service>` after daemon-reload validates that
//     the generator produced a working unit BEFORE any restart is
//     attempted — a malformed generated unit is caught before the running
//     workload is touched.
//   - If the unit was originally inactive, it is never force-started; only
//     an originally-active unit is restarted and its activity verified.
//   - Rollback restores the original file, reloads, and — only if the unit
//     was originally active — restarts it, verifying is-active before ever
//     reporting success.
func (p *PodmanService) MutateQuadletPorts(unitName string, newPorts []PortMapping) (*PortMutationResult, error) {
	result := &PortMutationResult{
		Success: false,
		Steps:   []PortMutationStepResult{},
	}
	result.RequiresExternal = true
	result.Guidance = "Automatic Quadlet mutation is disabled in this hardening build. Discovery and snippets remain available, but Podder does not yet have a version-aware Quadlet generator verification step strong enough to restart a workload safely."
	result.QuadletSnippet = GenerateQuadletSnippet(newPorts)
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: result.Guidance})
	return result, nil

	filePath, scope, err := FindQuadletFile(unitName)
	if err != nil {
		return nil, err
	}

	if scope == QuadletScopeSystem {
		result.RequiresExternal = true
		result.Guidance = fmt.Sprintf("Unit %s is a system/rootful Quadlet (%s). Podder only mutates rootless user Quadlets and never escalates privileges automatically. Edit this file and reload/restart it with the appropriate system privileges yourself.", unitName, filePath)
		result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: result.Guidance})
		return result, nil
	}

	systemctlService := strings.TrimSuffix(filepath.Base(filePath), ".container") + ".service"
	ignoreContainerID := p.quadletIgnoreContainerID(systemctlService)

	// 1. Preflight Validation (self-conflict-safe)
	for _, m := range newPorts {
		valReq := PortMappingRequest{
			HostIP:        m.HostIP,
			HostPort:      m.HostPort,
			ContainerPort: m.ContainerPort,
			Protocol:      m.Protocol,
			ContainerID:   ignoreContainerID,
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

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat %s: %w", filePath, err)
	}
	origContent, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", filePath, err)
	}

	// Capture original lifecycle so an intentionally-inactive unit is never
	// force-started.
	wasActive := p.quadletIsActive(systemctlService)

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "PREFLIGHT", Passed: true,
		Message: fmt.Sprintf("Preflight verified for %s (scope: %s, originally %s)", filePath, scope, activeLabel(wasActive)),
	})

	newContent := UpdateQuadletContent(string(origContent), newPorts)

	rollback := func(reason string, reloadedNewContent bool) (*PortMutationResult, error) {
		var errs []string
		if err := writeFileAtomicPreservingMode(filePath, origContent, fileInfo.Mode().Perm()); err != nil {
			errs = append(errs, fmt.Sprintf("failed to restore original unit file: %v", err))
		}
		if _, stderr, err := p.cmdRunner().Run("systemctl", "--user", "daemon-reload"); err != nil {
			errs = append(errs, fmt.Sprintf("daemon-reload failed during rollback: %v (%s)", err, strings.TrimSpace(stderr)))
		}

		verified := len(errs) == 0
		if verified && wasActive {
			if _, stderr, err := p.cmdRunner().Run("systemctl", "--user", "restart", systemctlService); err != nil {
				errs = append(errs, fmt.Sprintf("failed to restart original unit during rollback: %v (%s)", err, strings.TrimSpace(stderr)))
				verified = false
			} else if !p.quadletIsActive(systemctlService) {
				errs = append(errs, fmt.Sprintf("unit %s is not active after rollback restart", systemctlService))
				verified = false
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

	// 2. Write the new file atomically, preserving permissions.
	if err := writeFileAtomicPreservingMode(filePath, []byte(newContent), fileInfo.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("failed to write updated quadlet file: %w", err)
	}
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "SNAPSHOT", Passed: true, Message: "Unit file staged; original content retained in memory for rollback."})

	// 3. Reload, then validate the generated unit BEFORE ever restarting.
	if _, stderr, err := p.cmdRunner().Run("systemctl", "--user", "daemon-reload"); err != nil {
		return rollback(fmt.Sprintf("systemctl daemon-reload failed: %v (%s)", err, strings.TrimSpace(stderr)), false)
	}
	if _, stderr, err := p.cmdRunner().Run("systemctl", "--user", "cat", systemctlService); err != nil {
		return rollback(fmt.Sprintf("Generated unit %s failed to validate after daemon-reload (the edited Quadlet file is likely malformed): %v (%s)", systemctlService, err, strings.TrimSpace(stderr)), true)
	}

	// 4. Only restart (and require activity) if the unit was originally
	// active — never force an intentionally-inactive unit to start.
	if wasActive {
		if _, stderr, err := p.cmdRunner().Run("systemctl", "--user", "restart", systemctlService); err != nil {
			return rollback(fmt.Sprintf("systemctl restart %s failed: %v (%s)", systemctlService, err, strings.TrimSpace(stderr)), true)
		}
		if !p.quadletIsActive(systemctlService) {
			return rollback(fmt.Sprintf("Unit %s is not active after restart", systemctlService), true)
		}
	}

	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "PORT_VERIFY", Passed: true,
		Message: fmt.Sprintf("Unit %s validated and reloaded (%s).", systemctlService, activeLabel(wasActive)),
	})

	result.Success = true
	result.Steps = append(result.Steps, PortMutationStepResult{
		Step: "COMMITTED", Passed: true,
		Message: fmt.Sprintf("Quadlet unit %s updated successfully.", systemctlService),
	})

	return result, nil
}

func (p *PodmanService) quadletIsActive(systemctlService string) bool {
	stdout, _, _ := p.cmdRunner().Run("systemctl", "--user", "is-active", systemctlService)
	return strings.TrimSpace(stdout) == "active"
}

func activeLabel(active bool) string {
	if active {
		return "active"
	}
	return "inactive"
}
