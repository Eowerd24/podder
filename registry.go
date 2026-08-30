package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"gopkg.in/yaml.v3"
)

// PortRegistryConfig holds registry configuration.
type PortRegistryConfig struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
}

// AppSettings represents global application configuration.
type AppSettings struct {
	PortRegistry PortRegistryConfig `json:"portRegistry"`
	// LocalNode identifies which node in a (potentially homelab-wide,
	// multi-node) external registry this Podder instance actually observes.
	// Registry records scoped to a different node must never be treated as
	// locally missing/enforced. Empty means "not explicitly set" — the
	// effective value falls back to os.Hostname() at resolution time (see
	// resolveLocalNode), never to a hard-coded name.
	LocalNode string `json:"localNode,omitempty"`
}

// RegistryListener models the listener block of a registry port entry.
type RegistryListener struct {
	Address string `yaml:"address" json:"address"`
	Port    uint16 `yaml:"port" json:"port"`
}

// RegistryContainer models container target metadata.
type RegistryContainer struct {
	Port uint16 `yaml:"port" json:"port"`
}

// RegistryNotes tolerates a registry's `notes` field being either a single
// scalar string or a YAML list of strings, since the homelab-wide registry
// this reads from is not exclusively authored by Podder.
type RegistryNotes []string

func (n *RegistryNotes) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		*n = nil
		return nil
	}
	switch node.Kind {
	case yaml.SequenceNode:
		var list []string
		if err := node.Decode(&list); err != nil {
			return fmt.Errorf("notes list must contain only strings: %w", err)
		}
		*n = list
		return nil
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return fmt.Errorf("notes must be a string or list of strings: %w", err)
		}
		if strings.TrimSpace(s) == "" {
			*n = nil
		} else {
			*n = []string{s}
		}
		return nil
	default:
		return fmt.Errorf("notes must be a scalar string or a list of strings")
	}
}

func (n RegistryNotes) MarshalJSON() ([]byte, error) {
	if n == nil {
		return []byte("null"), nil
	}
	return json.Marshal([]string(n))
}

// RegistryPort models a single declared port definition in ports.yaml.
type RegistryPort struct {
	ID      string `yaml:"id" json:"id"`
	Service string `yaml:"service" json:"service"`
	// Node identifies which host in a homelab-wide registry this record
	// belongs to. An empty Node is treated as applicable to every Podder
	// instance (see nodeApplies); a non-empty Node that does not match the
	// local node's identity means this record describes a REMOTE service,
	// not a locally required one.
	Node                string            `yaml:"node" json:"node,omitempty"`
	Provider            string            `yaml:"provider" json:"provider,omitempty"`
	ContainerID         string            `yaml:"container_id" json:"containerId,omitempty"`
	Protocol            string            `yaml:"protocol" json:"protocol"`
	ApplicationProtocol string            `yaml:"application_protocol" json:"applicationProtocol,omitempty"`
	Listener            RegistryListener  `yaml:"listener" json:"listener"`
	Container           RegistryContainer `yaml:"container" json:"container,omitempty"`
	Scope               string            `yaml:"scope" json:"scope"`           // "loopback", "lan", "public", "management", "cluster"
	Class               string            `yaml:"class" json:"class,omitempty"` // "application", "observability", "infrastructure", etc.
	State               string            `yaml:"state" json:"state"`           // "active", "reserved", "planned", "temporary", "deprecated", "retired"
	Verification        string            `yaml:"verification" json:"verification,omitempty"`
	Purpose             string            `yaml:"purpose" json:"purpose,omitempty"`
	Notes               RegistryNotes     `yaml:"notes" json:"notes,omitempty"`
}

// PortRegistryFile models the top-level V1 YAML schema.
type PortRegistryFile struct {
	Version int            `yaml:"version" json:"version"`
	Ports   []RegistryPort `yaml:"ports" json:"ports"`
}

// supportedRegistrySchemaVersion is the only ports.yaml `version` this build
// knows how to interpret. An unrecognized version is refused outright
// rather than guessed at; this never disables the rest of Podder — an
// invalid/unsupported registry simply loads with Loaded=false and ordinary
// (non-registry) operation continues.
const supportedRegistrySchemaVersion = 1

// PortRegistryResult encapsulates parsing and load outcome.
type PortRegistryResult struct {
	Loaded bool   `json:"loaded"`
	Error  string `json:"error,omitempty"`
	// Warnings lists individual entries that were dropped during
	// validation (duplicate ID, unsupported protocol/state vocabulary,
	// missing port fields, ...). The registry as a whole still loads with
	// its remaining valid entries — one bad entry never disables ordinary
	// Podder operation.
	Warnings     []string       `json:"warnings,omitempty"`
	Path         string         `json:"path"`
	Version      int            `json:"version"`
	TotalEntries int            `json:"totalEntries"`
	Ports        []RegistryPort `json:"ports"`
}

// nodeApplies reports whether a registry record scoped to recordNode is
// applicable to a Podder instance whose local node identity is localNode.
// An unscoped record (empty Node) is treated as applicable everywhere by
// default — this is the "explicit policy" for unscoped records: most
// homelab registries are authored per-node already, so defaulting unscoped
// entries to "local" avoids spurious REMOTE classification for the common
// case, while a record that DOES name a node is honored precisely (it must
// never contribute to local MISSING/enforcement state on a different node).
func nodeApplies(recordNode, localNode string) bool {
	recordNode = strings.TrimSpace(recordNode)
	if recordNode == "" {
		return true
	}
	return strings.EqualFold(recordNode, strings.TrimSpace(localNode))
}

// resolveLocalNode returns the effective local node identity: the
// explicitly configured override if set, otherwise os.Hostname(). Never a
// hard-coded name — Podder is a general-purpose tool and must not assume
// any particular homelab's node naming.
func resolveLocalNode(settings *AppSettings) string {
	if settings != nil && strings.TrimSpace(settings.LocalNode) != "" {
		return strings.TrimSpace(settings.LocalNode)
	}
	if h, err := os.Hostname(); err == nil {
		return strings.TrimSpace(h)
	}
	return ""
}

// GetLocalNode returns the local node identity Podder currently resolves
// to, for display in Settings.
func (p *PodmanService) GetLocalNode() (string, error) {
	settings, err := p.GetSettings()
	if err != nil {
		return "", err
	}
	return resolveLocalNode(settings), nil
}

func getConfigDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(configDir) == "" {
		home := os.Getenv("HOME")
		if home == "" {
			home = "."
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "podder")
}

func getSettingsFilePath() string {
	return filepath.Join(getConfigDir(), "config.json")
}

// GetSettings reads persisted settings or returns defaults.
func (p *PodmanService) GetSettings() (*AppSettings, error) {
	filePath := getSettingsFilePath()
	data, err := os.ReadFile(filePath)
	if err != nil {
		return &AppSettings{
			PortRegistry: PortRegistryConfig{
				Enabled: false,
				Path:    "",
			},
		}, nil
	}

	var settings AppSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return &AppSettings{}, nil
	}
	return &settings, nil
}

// SaveSettings persists settings atomically to disk. Config directory and
// file permissions are kept restrictive (0700/0600) because settings (and,
// more importantly, sibling spec files under the same config directory) can
// contain credentials via environment variables.
func (p *PodmanService) SaveSettings(settings AppSettings) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize settings: %w", err)
	}

	filePath := getSettingsFilePath()
	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temporary settings: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to commit settings file: %w", err)
	}

	return nil
}

// SelectRegistryFile opens a native file dialog to choose a ports.yaml file.
func (p *PodmanService) SelectRegistryFile() (string, error) {
	dialog := application.Get().Dialog.OpenFile().
		SetTitle("Select Port Registry YAML File").
		CanChooseDirectories(false).
		CanChooseFiles(true)

	path, err := dialog.PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("failed to open dialog: %v", err)
	}
	return path, nil
}

// ParseRegistryYAML parses raw YAML data into a PortRegistryResult.
func ParseRegistryYAML(data []byte, path string) *PortRegistryResult {
	result := &PortRegistryResult{
		Path:  path,
		Ports: []RegistryPort{},
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		result.Error = "Registry file is empty"
		return result
	}

	var file PortRegistryFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		result.Error = fmt.Sprintf("YAML parsing error: %v", err)
		return result
	}

	if file.Version != supportedRegistrySchemaVersion {
		result.Error = fmt.Sprintf("unsupported registry schema version %d (this build supports version %d)", file.Version, supportedRegistrySchemaVersion)
		result.Version = file.Version
		return result
	}

	result.Version = file.Version
	result.TotalEntries = len(file.Ports)

	// Normalize defaults before validation so validation sees the
	// effective values.
	for i := range file.Ports {
		p := &file.Ports[i]
		if p.Protocol == "" {
			p.Protocol = "tcp"
		}
		p.Protocol = strings.ToLower(p.Protocol)
		if p.State == "" {
			p.State = "active"
		}
		p.State = strings.ToLower(p.State)
		if p.Scope == "" {
			p.Scope = CategorizeExposure(p.Listener.Address)
		}
		p.Scope = strings.ToLower(p.Scope)
	}

	valid, warnings := validateAndFilterRegistryPorts(file.Ports)
	result.Ports = valid
	result.Warnings = warnings
	result.Loaded = true

	return result
}

// validateAndFilterRegistryPorts checks each entry for problems the YAML
// parser itself can't catch: missing id, duplicate id, missing/zero
// listener port, and unsupported protocol/lifecycle-state vocabulary.
// Invalid entries are dropped (not fatal) so a single malformed entry in a
// homelab-wide registry never disables ordinary Podder operation; each drop
// is reported as a warning the caller can surface.
func validateAndFilterRegistryPorts(ports []RegistryPort) (valid []RegistryPort, warnings []string) {
	seenIDs := make(map[string]bool)
	validProtocols := map[string]bool{"tcp": true, "udp": true}
	validStates := map[string]bool{"active": true, "reserved": true, "planned": true, "temporary": true, "deprecated": true, "retired": true}

	for i, rp := range ports {
		label := rp.ID
		if label == "" {
			label = fmt.Sprintf("entry #%d", i+1)
		}
		switch {
		case strings.TrimSpace(rp.ID) == "":
			warnings = append(warnings, fmt.Sprintf("%s: missing 'id', entry skipped", label))
			continue
		case seenIDs[rp.ID]:
			warnings = append(warnings, fmt.Sprintf("duplicate registry id %q, entry skipped", rp.ID))
			continue
		case rp.Listener.Port == 0:
			warnings = append(warnings, fmt.Sprintf("%s: missing or zero listener.port, entry skipped", label))
			continue
		case !validProtocols[rp.Protocol]:
			warnings = append(warnings, fmt.Sprintf("%s: unsupported protocol %q, entry skipped", label, rp.Protocol))
			continue
		case !validStates[rp.State]:
			warnings = append(warnings, fmt.Sprintf("%s: unrecognized lifecycle state %q, entry skipped", label, rp.State))
			continue
		}
		seenIDs[rp.ID] = true
		valid = append(valid, rp)
	}
	return valid, warnings
}

// LoadPortRegistry reads and parses an external registry file.
func (p *PodmanService) LoadPortRegistry(path string) (*PortRegistryResult, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return &PortRegistryResult{
			Loaded: false,
			Error:  "No registry path specified",
		}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return &PortRegistryResult{
			Loaded: false,
			Path:   path,
			Error:  fmt.Sprintf("Failed to read file: %v", err),
		}, nil
	}

	return ParseRegistryYAML(data, path), nil
}
