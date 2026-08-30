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

// RegistryPort models a single declared port definition in ports.yaml.
type RegistryPort struct {
	ID                  string            `yaml:"id" json:"id"`
	Service             string            `yaml:"service" json:"service"`
	Node                string            `yaml:"node" json:"node,omitempty"`
	Provider            string            `yaml:"provider" json:"provider,omitempty"`
	ContainerID         string            `yaml:"container_id" json:"containerId,omitempty"`
	Protocol            string            `yaml:"protocol" json:"protocol"`
	ApplicationProtocol string            `yaml:"application_protocol" json:"applicationProtocol,omitempty"`
	Listener            RegistryListener  `yaml:"listener" json:"listener"`
	Container           RegistryContainer `yaml:"container" json:"container,omitempty"`
	Scope               string            `yaml:"scope" json:"scope"`                   // "loopback", "lan", "public", "management", "cluster"
	Class               string            `yaml:"class" json:"class,omitempty"`         // "application", "observability", "infrastructure", etc.
	State               string            `yaml:"state" json:"state"`                   // "active", "reserved", "planned", "temporary", "deprecated", "retired"
	Verification        string            `yaml:"verification" json:"verification,omitempty"`
	Purpose             string            `yaml:"purpose" json:"purpose,omitempty"`
	Notes               string            `yaml:"notes" json:"notes,omitempty"`
}

// PortRegistryFile models the top-level V1 YAML schema.
type PortRegistryFile struct {
	Version int            `yaml:"version" json:"version"`
	Ports   []RegistryPort `yaml:"ports" json:"ports"`
}

// PortRegistryResult encapsulates parsing and load outcome.
type PortRegistryResult struct {
	Loaded       bool           `json:"loaded"`
	Error        string         `json:"error,omitempty"`
	Path         string         `json:"path"`
	Version      int            `json:"version"`
	TotalEntries int            `json:"totalEntries"`
	Ports        []RegistryPort `json:"ports"`
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

// SaveSettings persists settings atomically to disk.
func (p *PodmanService) SaveSettings(settings AppSettings) error {
	dir := getConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize settings: %w", err)
	}

	filePath := getSettingsFilePath()
	tmpFile := filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return fmt.Errorf("failed to write temporary settings: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
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

	result.Loaded = true
	result.Version = file.Version
	result.Ports = file.Ports
	result.TotalEntries = len(file.Ports)

	// Normalize defaults
	for i := range result.Ports {
		p := &result.Ports[i]
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

	return result
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
