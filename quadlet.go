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

	if xdgRuntime := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); xdgRuntime != "" {
		dirs = append(dirs, quadletSearchDir{filepath.Join(xdgRuntime, "containers", "systemd"), QuadletScopeUser})
	}

	if xdgConfig := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdgConfig != "" {
		dirs = append(dirs, quadletSearchDir{filepath.Join(xdgConfig, "containers", "systemd"), QuadletScopeUser})
	} else if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		dirs = append(dirs, quadletSearchDir{filepath.Join(home, ".config", "containers", "systemd"), QuadletScopeUser})
	}

	uid := strconv.Itoa(os.Getuid())
	dirs = append(dirs,
		quadletSearchDir{prefixed(filepath.Join("/etc/containers/systemd/users", uid)), QuadletScopeUser},
		quadletSearchDir{prefixed("/etc/containers/systemd/users"), QuadletScopeUser},
		quadletSearchDir{prefixed(filepath.Join("/usr/share/containers/systemd/users", uid)), QuadletScopeUser},
		quadletSearchDir{prefixed("/usr/share/containers/systemd/users"), QuadletScopeUser},
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
	ServiceName  string        `json:"serviceName"`
	HasDropIns   bool          `json:"hasDropIns"`
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
			} else if !os.IsNotExist(statErr) {
				return "", "", fmt.Errorf("cannot inspect Quadlet candidate %s: %w", p, statErr)
			}
		}
		var found string
		walkErr := filepath.WalkDir(dir.path, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if path == dir.path || entry.IsDir() {
				return nil
			}
			for _, cand := range candidates {
				if entry.Name() == cand {
					found = path
					return filepath.SkipAll
				}
			}
			return nil
		})
		if walkErr != nil && !os.IsNotExist(walkErr) {
			return "", "", fmt.Errorf("cannot recursively inspect Quadlet directory %s: %w", dir.path, walkErr)
		}
		if found != "" {
			return found, dir.scope, nil
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

func QuadletServiceName(content, filePath string) string {
	serviceName := strings.TrimSuffix(filepath.Base(filePath), ".container")
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line)
			continue
		}
		if section == "[container]" && strings.HasPrefix(strings.ToLower(line), "servicename=") {
			if value := strings.TrimSpace(line[len("ServiceName="):]); value != "" {
				serviceName = strings.TrimSuffix(value, ".service")
			}
		}
	}
	return serviceName + ".service"
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
	serviceName := QuadletServiceName(content, filePath)
	dropIns, err := filepath.Glob(filePath + ".d/*.conf")
	if err != nil {
		return nil, fmt.Errorf("failed to inspect Quadlet drop-ins: %w", err)
	}

	return &QuadletFileDetails{
		UnitName:     unitName,
		FilePath:     filePath,
		Scope:        scope,
		Exists:       true,
		Content:      content,
		PortMappings: ports,
		ServiceName:  serviceName,
		HasDropIns:   len(dropIns) > 0,
	}, nil
}

// quadletIgnoreContainerID finds the container systemd currently manages
// for a given .service unit name, so preflight validation ignores that
// container's OWN current port claims (section 4.1/17.3's "self-conflict"
// MutateQuadletPorts is deliberately read-only. Automatic Quadlet edits remain
// disabled until Podder can compute effective drop-ins, validate with the real
// generator, observe lifecycle without conflating errors with inactivity, and
// verify exact runtime ports for both apply and rollback.
func (p *PodmanService) MutateQuadletPorts(unitName string, newPorts []PortMapping) (*PortMutationResult, error) {
	result := &PortMutationResult{Success: false, Steps: []PortMutationStepResult{}}
	result.RequiresExternal = true
	result.Guidance = "Automatic Quadlet mutation is disabled in this hardening build. Discovery and snippets remain available, but Podder does not yet compute effective drop-in configuration, run a version-aware generator validator, resolve lifecycle queries as a tri-state, and verify exact runtime ports and rollback."
	result.QuadletSnippet = GenerateQuadletSnippet(newPorts)
	result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: result.Guidance})
	return result, nil
}
