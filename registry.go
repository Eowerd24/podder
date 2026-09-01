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
	Enabled              bool   `json:"enabled"`
	Path                 string `json:"path"`
	TreatUnscopedAsLocal bool   `json:"treatUnscopedAsLocal,omitempty"`
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
	// ComposeTrustedRoots is an explicit, operator-configured allowlist of
	// filesystem roots Podder is permitted to automatically read a Compose
	// project file from during provenance-based discovery (InspectCompose).
	// A container's own labels (working_dir, config_files) are a discovery
	// HINT, never authorization: containment within the container-reported
	// working directory alone is not sufficient (a malicious/malformed
	// container could report working_dir=/etc, config_files=passwd).
	// Automatic Compose file reading is disabled by default — the safe
	// default is an empty list — until the operator explicitly approves at
	// least one root here. Each root is canonicalized (cleaned,
	// symlink-resolved where it exists) at check time; see
	// FindComposeFile/composeFileWithinTrustedRoots.
	ComposeTrustedRoots []string `json:"composeTrustedRoots,omitempty"`
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
	// belongs to. An empty Node follows the explicit TreatUnscopedAsLocal setting; the secure default is not applicable
	// (see nodeApplies); a non-empty Node that does not match the
	// local node's identity means this record describes a REMOTE service,
	// not a locally required one.
	Node                string            `yaml:"node" json:"node,omitempty"`
	Provider            string            `yaml:"provider" json:"provider,omitempty"`
	ContainerID         string            `yaml:"container_id" json:"containerId,omitempty"`
	Protocol            string            `yaml:"protocol" json:"protocol"`
	ApplicationProtocol string            `yaml:"application_protocol" json:"applicationProtocol,omitempty"`
	Listener            RegistryListener  `yaml:"listener" json:"listener"`
	Container           RegistryContainer `yaml:"container" json:"container,omitempty"`
	// RangeSize, when > 1, means this record declares a published port
	// range of that many ports starting at Listener.Port (and, when set,
	// Container.Port) rather than a single port.
	RangeSize    int           `yaml:"range_size" json:"rangeSize,omitempty"`
	Scope        string        `yaml:"scope" json:"scope"`           // "loopback", "lan", "public", "management", "cluster"
	Class        string        `yaml:"class" json:"class,omitempty"` // "application", "observability", "infrastructure", etc.
	State        string        `yaml:"state" json:"state"`           // "active", "reserved", "planned", "temporary", "deprecated", "retired"
	Verification string        `yaml:"verification" json:"verification,omitempty"`
	Purpose      string        `yaml:"purpose" json:"purpose,omitempty"`
	Notes        RegistryNotes `yaml:"notes" json:"notes,omitempty"`
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
// Unscoped records are NOT local by default. Operators may explicitly opt in
// with PortRegistryConfig.TreatUnscopedAsLocal; otherwise they remain visible
// as not-applicable and cannot create local missing/reservation state.
func nodeApplies(recordNode, localNode string, treatUnscopedAsLocal bool) bool {
	recordNode = strings.TrimSpace(recordNode)
	if recordNode == "" {
		return treatUnscopedAsLocal
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
		if os.IsNotExist(err) {
			return &AppSettings{PortRegistry: PortRegistryConfig{Enabled: false}}, nil
		}
		return nil, fmt.Errorf("failed to read settings: %w", err)
	}

	var settings AppSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse settings: %w", err)
	}
	return &settings, nil
}

// SaveSettings persists settings atomically to disk. Config directory and
// file permissions are kept restrictive (0700/0600) because settings (and,
// more importantly, sibling spec files under the same config directory) can
// contain credentials via environment variables.
func (p *PodmanService) SaveSettings(settings AppSettings) error {
	dir := getConfigDir()
	if err := ensurePrivateDir(dir); err != nil {
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
		case rp.RangeSize < 0:
			warnings = append(warnings, fmt.Sprintf("%s: negative range_size %d, entry skipped", label, rp.RangeSize))
			continue
		case rp.RangeSize > 1 && int(rp.Listener.Port)+rp.RangeSize-1 > 65535:
			warnings = append(warnings, fmt.Sprintf("%s: listener port range of size %d starting at %d overflows past 65535, entry skipped", label, rp.RangeSize, rp.Listener.Port))
			continue
		case rp.RangeSize > 1 && rp.Container.Port != 0 && int(rp.Container.Port)+rp.RangeSize-1 > 65535:
			warnings = append(warnings, fmt.Sprintf("%s: container port range of size %d starting at %d overflows past 65535, entry skipped", label, rp.RangeSize, rp.Container.Port))
			continue
		}
		seenIDs[rp.ID] = true
		valid = append(valid, rp)
	}
	return valid, warnings
}

// LoadPortRegistry reads and parses an external registry file. This is the
// TOLERANT, DISPLAY/OBSERVATION-mode loader: a malformed entry is dropped
// with a warning (see validateAndFilterRegistryPorts) and the rest of the
// registry still loads — one bad record must never make the Ports tab go
// blank. Never use this result to decide whether it is safe to mutate,
// create, or adopt a workload; see LoadPortRegistryStrict for that.
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

// LoadPortRegistryStrict loads and parses the registry exactly like
// LoadPortRegistry, but is the FAIL-CLOSED, SAFETY/BLOCKING-mode
// counterpart used wherever the registry is being consulted to gate a
// destructive/safety-critical operation (port validation, free-port
// selection, and everything that gates create/mutate/adopt — see
// CollectBlockingClaimsStrict).
//
// Once an operator has explicitly enabled the registry as a source of port
// safety truth, a malformed or ambiguous entry can never be silently
// treated as irrelevant: the skipped record could have been exactly the
// reservation that would have blocked this operation. So unlike the
// tolerant display loader, ANY parse failure OR ANY dropped/invalid entry
// (even alongside otherwise-valid ones) makes this fail with an error —
// "unknown" must never be silently downgraded to "free".
func (p *PodmanService) LoadPortRegistryStrict(path string) (*PortRegistryResult, error) {
	result, err := p.LoadPortRegistry(path)
	if err != nil {
		return nil, err
	}
	if !result.Loaded {
		return result, fmt.Errorf("port registry is enabled but failed to load: %s", result.Error)
	}
	if len(result.Warnings) > 0 {
		return result, fmt.Errorf("registry cannot be safely enforced because it contains %d invalid/unsupported entry(ies) that could affect endpoint ownership/reservation safety: %s", len(result.Warnings), strings.Join(result.Warnings, "; "))
	}
	return result, nil
}

// registryWarnings nil-safely extracts the tolerant loader's per-entry
// warnings for display, so GetPortOverview's summary can distinguish
// "registry loaded cleanly" from "registry loaded for observation with N
// invalid entries" (and, by extension, so the frontend can tell the
// operator that safety-critical operations are blocked until they're
// fixed — see LoadPortRegistryStrict).
func registryWarnings(result *PortRegistryResult) []string {
	if result == nil {
		return nil
	}
	return result.Warnings
}

// classifyRegistryMatch computes the reconciliation status to report for a
// registry-declared record that HAS a currently-observed runtime endpoint
// (container port mapping or host listener) exactly fulfilling its
// declaration (and, for states that assert live workload identity, whose
// owner also matches — see findRegistryMatch in ports.go), and whether that
// observation should count toward the "matched" (ordinary active-service)
// summary bucket as opposed to some other lifecycle-specific bucket (e.g. a
// reservation that is, notably, currently in use).
//
// Lifecycle semantics (explicit, not inferred from "not reserved/planned"):
//
//   - active/unrecognized: MATCH — this is the ordinary "declared and
//     running" case, and the ONLY one counted toward registryMatch.
//   - reserved: RESERVED_IN_USE — a reservation is expected to stay UNUSED;
//     something occupying it is drift worth surfacing distinctly, never
//     folded into ordinary MATCH.
//   - planned: PLANNED — informational future intent; even if something
//     already happens to occupy that endpoint, planned records never
//     report anything but PLANNED (see the brief's lifecycle table).
//   - temporary: TEMPORARY_ACTIVE — running, but explicitly not ordinary
//     permanent active state. Informational, not counted as an ordinary
//     match NOR as drift (a temporary declaration running is expected).
//   - deprecated: DEPRECATED_ACTIVE — still permitted to exist, flagged for
//     migration/removal. Counted as drift (registryStatusIsDrift), never as
//     an ordinary match — conflating "deprecated but still running" with a
//     clean MATCH would hide exactly the migration signal this state
//     exists to surface.
//   - retired: RETIRED_IN_USE — should no longer exist; a live match here
//     is real, useful drift information (registryStatusIsDrift), never an
//     ordinary match.
func classifyRegistryMatch(state string) (status string, countsAsOrdinaryMatch bool) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "reserved":
		return "RESERVED_IN_USE", false
	case "planned":
		return "PLANNED", false
	case "temporary":
		return "TEMPORARY_ACTIVE", false
	case "deprecated":
		return "DEPRECATED_ACTIVE", false
	case "retired":
		return "RETIRED_IN_USE", false
	default: // "active" and any future/unrecognized state default to active semantics.
		return "MATCH", true
	}
}

// classifyRegistryMissing computes the status to report for a
// registry-declared record with NO currently-observed runtime endpoint
// fulfilling it, and whether that absence is an actual operational fault
// (as opposed to expected/informational absence). Only "active" (a service
// declared to be currently running) is a fault when missing — "reserved" is
// handled entirely by the caller via host-occupancy (RESERVED_FREE/
// RESERVED_IN_USE), never routed through here.
func classifyRegistryMissing(state string) (status string, isOperationalFault bool) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "planned":
		return "PLANNED", false
	case "temporary":
		return "TEMPORARY_MISSING", false
	case "deprecated":
		return "DEPRECATED_MISSING", false
	case "retired":
		return "RETIRED_FREE", false
	default: // "active" and any future/unrecognized state default to active semantics.
		return "DECLARED_MISSING", true
	}
}

// registryStateExpectsBindMatch reports whether a registry-declared
// record's lifecycle state carries a live "this should be running with
// exactly this bind" expectation strong enough to make a bind-address
// mismatch (same port/protocol/service, different address) worth surfacing
// as DECLARED_ENDPOINT_MISMATCH. Reserved/planned records make no runtime
// bind assertion at all, and retired records are expected to be gone
// entirely (a bind difference is the least interesting thing about them
// still running) — mismatch reporting is scoped to states that actually
// assert current, exact runtime configuration.
func registryStateExpectsBindMatch(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "active", "temporary", "deprecated":
		return true
	default:
		return false
	}
}

// registryStatusIsDrift reports whether a reconciliation status
// (PortOverviewItem.ReconciliationStatus) represents a lifecycle-aware
// operator-visible problem worth counting toward
// PortOverviewSummary.RegistryDrift, as opposed to an ordinary match, a
// reservation (already tracked by RegistryDrift's sibling
// RegistryReserved/RESERVED_IN_USE badge), or a merely-informational
// lifecycle status (PLANNED, TEMPORARY_ACTIVE/MISSING, DEPRECATED_MISSING,
// RETIRED_FREE — none of these assert something is currently wrong).
//
// Explicit and separate from "ordinary MATCH" so a deprecated/retired
// endpoint that is still running, an endpoint whose bind differs from what
// was declared, or one occupied by the wrong workload entirely, can never
// be silently folded into the plain "registryMatch" metric — see
// classifyRegistryMatch's countsAsOrdinaryMatch, which already excludes
// these, and findRegistryOwnerMismatch/findRegistryBindMismatch in
// ports.go, which produce OWNER_MISMATCH/OWNER_UNKNOWN/
// DECLARED_ENDPOINT_MISMATCH.
func registryStatusIsDrift(status string) bool {
	switch status {
	case "DEPRECATED_ACTIVE", "RETIRED_IN_USE", "OWNER_MISMATCH", "OWNER_UNKNOWN", "DECLARED_ENDPOINT_MISMATCH":
		return true
	default:
		return false
	}
}
