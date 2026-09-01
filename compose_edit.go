package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComposeFileDetails holds inspection details of a Docker/Podman Compose
// file, distinguishing what the container CLAIMS (untrusted provenance
// metadata) from what Podder actually VERIFIED and read.
type ComposeFileDetails struct {
	ContainerID string `json:"containerId"`
	// WorkingDir and ClaimedConfigFile are exactly what the container's own
	// labels report — untrusted discovery hints, always safe to display as
	// metadata, never implicitly safe to read from. Always populated when
	// any compose provenance was detected, regardless of Trusted.
	WorkingDir        string `json:"workingDir"`
	ClaimedConfigFile string `json:"claimedConfigFile,omitempty"`
	// ComposeFile and Content are populated ONLY when Trusted is true — the
	// candidate resolved inside both the claimed working directory AND an
	// operator-approved trusted root, and Podder actually read it.
	ComposeFile string `json:"composeFile,omitempty"`
	Content     string `json:"content,omitempty"`
	// Trusted reports whether ComposeFile/Content reflect an actually-read
	// local file. false means Podder detected Compose provenance but is
	// refusing to read the reported location automatically; UntrustedReason
	// explains why (see FindComposeFile's sentinel errors).
	Trusted         bool          `json:"trusted"`
	UntrustedReason string        `json:"untrustedReason,omitempty"`
	Project         string        `json:"project"`
	Service         string        `json:"service"`
	PortMappings    []PortMapping `json:"portMappings"`
}

// ErrMultipleComposeFiles is returned when a container's Compose provenance
// names more than one configuration file (a multi `-f` project). The
// effective project is the merge of all of them; editing only the first
// could miss where a service's ports: actually live, or silently conflict
// with an override file. Callers must block automatic mutation and fall
// back to manual guidance rather than guess which file to touch.
var ErrMultipleComposeFiles = errors.New("compose project is defined by multiple configuration files; automatic mutation is not safe")

// ErrComposeFileOutsideWorkingDir is returned when a container's Compose
// provenance metadata (com.docker.compose.project.config_files or
// equivalent) names a file that does not resolve within the container's
// reported working directory. Provenance labels are discovery hints, not
// filesystem authorization — a container (malicious or merely malformed)
// must never be able to make Podder open an arbitrary accessible file
// simply by claiming it is "the" compose file. Such a declaration is
// treated as unresolved provenance: Podder will not guess at reading it.
var ErrComposeFileOutsideWorkingDir = errors.New("declared compose config file is outside the container's reported working directory; treating as unresolved provenance rather than reading it")

// ErrComposeFileNotTrusted is returned when a candidate compose file DOES
// resolve within the container's reported working directory, but that
// working directory itself has not been established as a legitimate
// Compose project location — it is, itself, still just container-supplied
// label content. Containment within a claimed working directory alone is
// not authorization: a malicious/malformed container could report
// working_dir=/etc, config_files=passwd, and "/etc/passwd is inside /etc"
// would trivially hold. Automatic reading additionally requires the
// resolved candidate to fall under at least one operator-approved root
// (AppSettings.ComposeTrustedRoots); with no roots configured, this is
// always the outcome (safe default).
var ErrComposeFileNotTrusted = errors.New("compose file is outside every operator-approved Compose trusted root; a container-reported working directory alone is never sufficient authorization to read it automatically")

// composeFileWithinTrustedRoots reports whether the resolved, absolute
// candidate path falls within at least one operator-configured trusted
// root. Each root is canonicalized at check time (resolveWithinRoot itself
// performs the clean + symlink-safe containment check), so a symlink
// planted inside an approved root that points outside it is still refused.
// An empty/unconfigured root list always returns false — the safe default
// is that NOTHING is automatically trusted until the operator opts in.
func composeFileWithinTrustedRoots(candidate string, trustedRoots []string) bool {
	for _, root := range trustedRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if _, err := resolveWithinRoot(root, candidate); err == nil {
			return true
		}
	}
	return false
}

// FindComposeFile locates the compose file from project metadata or
// directory scan. workingDir and projectConfigFiles are external
// provenance metadata sourced from container labels — not filesystem
// authorization — so a candidate must pass BOTH of the following before it
// is stat'd/read:
//
//  1. it is contained within the claimed workingDir itself
//     (resolveWithinRoot, symlink-safe canonicalization) — see
//     ErrComposeFileOutsideWorkingDir; and
//  2. it is contained within at least one operator-approved Compose
//     trusted root (composeFileWithinTrustedRoots) — see
//     ErrComposeFileNotTrusted. The claimed workingDir is NEVER itself
//     treated as an implicit trust root, no matter how deeply a candidate
//     nests inside it.
//
// trustedRoots is the caller's resolved AppSettings.ComposeTrustedRoots; an
// empty list means nothing will ever be read automatically (the safe
// default until an operator explicitly approves at least one root).
func FindComposeFile(workingDir, projectConfigFiles string, trustedRoots []string) (string, error) {
	workingDir = strings.TrimSpace(workingDir)

	checkCandidate := func(resolved string) (string, error) {
		if !composeFileWithinTrustedRoots(resolved, trustedRoots) {
			return "", fmt.Errorf("%w: %q", ErrComposeFileNotTrusted, resolved)
		}
		return resolved, nil
	}

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
			if workingDir == "" {
				return "", fmt.Errorf("%w: %q (no working directory reported to validate it against)", ErrComposeFileOutsideWorkingDir, f)
			}
			resolved, resolveErr := resolveWithinRoot(workingDir, f)
			if resolveErr != nil {
				return "", fmt.Errorf("%w: %q", ErrComposeFileOutsideWorkingDir, f)
			}
			if _, err := os.Stat(resolved); err == nil {
				return checkCandidate(resolved)
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
			p, resolveErr := resolveWithinRoot(workingDir, cand)
			if resolveErr != nil {
				continue
			}
			if _, err := os.Stat(p); err == nil {
				return checkCandidate(p)
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
	if portsNode == nil {
		return []PortMapping{}, nil
	}
	if portsNode.Kind == yaml.AliasNode {
		return nil, fmt.Errorf("service %q uses a YAML alias for ports; automatic mutation is disabled", serviceName)
	}
	if portsNode.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("service %q ports must be a sequence; automatic mutation is disabled", serviceName)
	}

	var mappings []PortMapping
	for i, item := range portsNode.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			entry := strings.TrimSpace(item.Value)
			if entry == "" || strings.Contains(entry, "$") {
				return nil, fmt.Errorf("service %q port entry #%d contains interpolation or is empty; automatic mutation is disabled", serviceName, i+1)
			}
			pm, err := ParsePublishSpec(entry)
			if err != nil || pm == nil {
				return nil, fmt.Errorf("service %q port entry #%d (%q) is not representable: %v", serviceName, i+1, entry, err)
			}
			mappings = append(mappings, *pm)
		case yaml.MappingNode:
			if unsupported := composePortsUnsupportedLongFormKeys(&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{item}}); len(unsupported) > 0 {
				return nil, fmt.Errorf("service %q port entry #%d uses unsupported long-form attribute(s) %s", serviceName, i+1, strings.Join(unsupported, ", "))
			}
			targetNode := findMapKeyNode(item, "target")
			publishedNode := findMapKeyNode(item, "published")
			if targetNode == nil || publishedNode == nil || targetNode.Kind != yaml.ScalarNode || publishedNode.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("service %q port entry #%d must have scalar target and published values", serviceName, i+1)
			}
			hostIP := "0.0.0.0"
			if n := findMapKeyNode(item, "host_ip"); n != nil {
				if n.Kind != yaml.ScalarNode || strings.Contains(n.Value, "$") {
					return nil, fmt.Errorf("service %q port entry #%d has an unrepresentable host_ip", serviceName, i+1)
				}
				hostIP = n.Value
			}
			protocol := "tcp"
			if n := findMapKeyNode(item, "protocol"); n != nil {
				if n.Kind != yaml.ScalarNode || strings.Contains(n.Value, "$") {
					return nil, fmt.Errorf("service %q port entry #%d has an unrepresentable protocol", serviceName, i+1)
				}
				protocol = n.Value
			}
			if strings.Contains(targetNode.Value, "$") || strings.Contains(publishedNode.Value, "$") {
				return nil, fmt.Errorf("service %q port entry #%d contains interpolation; automatic mutation is disabled", serviceName, i+1)
			}
			entry := fmt.Sprintf("%s:%s:%s/%s", hostIP, publishedNode.Value, targetNode.Value, protocol)
			pm, err := ParsePublishSpec(entry)
			if err != nil || pm == nil {
				return nil, fmt.Errorf("service %q port entry #%d is not representable: %v", serviceName, i+1, err)
			}
			mappings = append(mappings, *pm)
		case yaml.AliasNode:
			return nil, fmt.Errorf("service %q port entry #%d uses a YAML alias; automatic mutation is disabled", serviceName, i+1)
		default:
			return nil, fmt.Errorf("service %q port entry #%d has unsupported YAML node type %d", serviceName, i+1, item.Kind)
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

	if _, err := ParseComposePorts(composeYAML, serviceName); err != nil {
		return "", err
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

// InspectCompose discovers and parses the compose file for a given
// container. A container's Compose provenance labels (working_dir,
// config_files) are DISCOVERY HINTS only, never filesystem authorization —
// see FindComposeFile / AppSettings.ComposeTrustedRoots. When the reported
// location cannot be established as trustworthy, this still returns a
// details object describing the CLAIMED metadata (so the operator sees what
// the container reports) with Trusted=false and no file content — it never
// silently reads and returns an unverified file, and it never returns a
// same-shaped "success" result for content that was never actually
// verified. A hard error is returned only when there's nothing meaningful
// to show at all (container not found, no compose provenance whatsoever).
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

	claimed := &ComposeFileDetails{
		ContainerID:       target.Id,
		WorkingDir:        workingDir,
		ClaimedConfigFile: configFile,
		Project:           target.Provenance.Project,
		Service:           serviceName,
		Trusted:           false,
	}

	settings, err := p.GetSettings()
	if err != nil {
		claimed.UntrustedReason = fmt.Sprintf("could not read Podder settings to resolve approved Compose trusted roots: %v", err)
		return claimed, nil
	}

	foundFile, err := FindComposeFile(workingDir, configFile, settings.ComposeTrustedRoots)
	if err != nil {
		claimed.UntrustedReason = err.Error()
		return claimed, nil
	}

	data, err := os.ReadFile(foundFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read compose file %s: %w", foundFile, err)
	}

	content := string(data)
	// InspectCompose is a read-only display helper, not the (already
	// disabled) mutation path: a service whose ports: use YAML aliases,
	// ${VAR} interpolation, or another unrepresentable construct should
	// still return the compose file's location/content rather than fail
	// outright — the caller degrades to already-known live Podman ports.
	ports, _ := ParseComposePorts(content, serviceName)

	claimed.ComposeFile = foundFile
	claimed.Content = content
	claimed.Trusted = true
	claimed.PortMappings = ports
	return claimed, nil
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
		if p.Service == service && p.Project == project {
			return c
		}
	}
	return nil
}

// MutateComposePorts is deliberately read-only. Automatic Compose edits remain
// disabled until Podder can prove effective-project representability, exact
// project/service identity, lifecycle preservation, and bounded apply/rollback
// verification across providers.
func (p *PodmanService) MutateComposePorts(containerID string, newPorts []PortMapping) (*PortMutationResult, error) {
	result := &PortMutationResult{Success: false, Steps: []PortMutationStepResult{}}
	result.RequiresExternal = true
	result.Guidance = "Automatic in-place Compose mutation is disabled in this hardening build. Podder cannot yet prove complete effective-project identity, preserve every YAML construct, preserve stopped lifecycle, and verify apply and rollback across provider implementations. Use the generated snippet and authoritative Compose workflow manually."

	// The generated guidance must name the container's ACTUAL Compose
	// service — using a generic placeholder key like "service" would
	// produce a snippet the operator could paste under the wrong service
	// entirely. If the real identity can't be confidently determined (the
	// container wasn't found, or isn't actually Compose-managed), no
	// snippet is generated rather than inventing a name.
	serviceName := ""
	if id := strings.TrimSpace(containerID); id != "" {
		if containers, err := p.ListContainers(true); err == nil {
			if target := findContainerByIdentity(containers, id); target != nil && target.Provenance.Type == "compose" {
				serviceName = strings.TrimSpace(target.Provenance.Service)
			}
		}
	}

	if snippet, err := GenerateComposeSnippet(serviceName, newPorts); err == nil {
		result.ComposeSnippet = snippet
	} else {
		result.Guidance += " The Compose service identity for this container could not be safely determined or represented, so no snippet was generated; update your compose file's ports: for the correct service manually."
	}

	result.Steps = append(result.Steps, PortMutationStepResult{Step: "PREFLIGHT", Passed: false, Message: result.Guidance})
	return result, nil
}
