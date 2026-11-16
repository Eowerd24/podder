package main

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const launcherSchemaVersion = 1

// Launcher metadata is local application preference, never workload ownership.
type LauncherAssociation struct {
	Key           string `json:"key"`
	ContainerID   string `json:"containerID"`
	ContainerName string `json:"containerName"`
}
type WebEndpoint struct {
	ID      string      `json:"id"`
	Scheme  string      `json:"scheme"`
	Mapping PortMapping `json:"mapping"`
	Path    string      `json:"path"`
}
type LauncherDefinition struct {
	GUI                 *GUIViewerDefinition `json:"gui,omitempty"`
	Terminal            *TerminalDefinition  `json:"terminal,omitempty"`
	SchemaVersion       int                  `json:"schemaVersion"`
	Kind                string               `json:"kind"`
	Association         LauncherAssociation  `json:"association"`
	Endpoints           []WebEndpoint        `json:"endpoints"`
	PreferredEndpointID string               `json:"preferredEndpointID"`
	OpenAfterStart      bool                 `json:"openAfterStart"`
}
type LauncherEndpointOption struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}
type LauncherOptions struct {
	Association LauncherAssociation      `json:"association"`
	Definition  *LauncherDefinition      `json:"definition"`
	Mappings    []PortMapping            `json:"mappings"`
	Endpoints   []LauncherEndpointOption `json:"endpoints"`
	Stale       []LauncherDefinition     `json:"stale"`
	Running     bool                     `json:"running"`
	Message     string                   `json:"message"`
}

// These are TCP configuration candidates, deliberately not HTTP suggestions.
// Require a real published host/container pair; never guess an auto-assigned port.
func launcherMappings(mappings []PortMapping) []PortMapping {
	out := []PortMapping{}
	seen := map[string]bool{}
	for _, m := range mappings {
		if NormalizeProtocol(m.Protocol) != "tcp" || m.HostPort == 0 || m.ContainerPort == 0 || m.RangeSize < 0 || m.RangeSize > 256 {
			continue
		}
		if m.RangeSize > 1 && (int(m.HostPort)+m.RangeSize-1 > 65535 || int(m.ContainerPort)+m.RangeSize-1 > 65535) {
			continue
		}
		for _, port := range ExpandPortRange(m) {
			port.Protocol = "tcp"
			port.RangeSize = 0
			port.HostIP = CanonicalDeclaredBind(port.HostIP)
			if _, err := launcherURL(WebEndpoint{Scheme: "http", Mapping: port, Path: "/"}); err != nil {
				continue
			}
			key := FormatPublishSpec(port)
			if !seen[key] {
				out = append(out, port)
				seen[key] = true
			}
		}
	}
	return out
}

// Build solely from the operator's scheme/path and the runtime's host binding.
func launcherURL(e WebEndpoint) (string, error) {
	if e.Scheme != "http" && e.Scheme != "https" {
		return "", fmt.Errorf("only HTTP and HTTPS are supported")
	}
	if e.Mapping.HostPort == 0 || e.Mapping.ContainerPort == 0 || NormalizeProtocol(e.Mapping.Protocol) != "tcp" || e.Mapping.RangeSize > 1 {
		return "", fmt.Errorf("choose one published TCP port")
	}
	host := CanonicalDeclaredBind(e.Mapping.HostIP)
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return "", fmt.Errorf("unsupported local host binding")
	}
	path := e.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\?#") {
		return "", fmt.Errorf("path must start with / and contain no query, fragment or backslash; do not store credentials")
	}
	decoded, err := url.PathUnescape(path)
	if err != nil || strings.ContainsAny(decoded, "\\?#") || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("invalid URL path")
	}
	u := &url.URL{Scheme: e.Scheme, Host: net.JoinHostPort(ip.String(), strconv.Itoa(int(e.Mapping.HostPort))), Path: decoded}
	return u.String(), nil
}

func validateLauncher(d LauncherDefinition, mappings []PortMapping) ([]LauncherEndpointOption, error) {
	if d.SchemaVersion != launcherSchemaVersion {
		return nil, fmt.Errorf("unsupported launcher schema %d", d.SchemaVersion)
	}
	switch d.Kind {
	case "none":
		if len(d.Endpoints) != 0 || d.OpenAfterStart || d.PreferredEndpointID != "" {
			return nil, fmt.Errorf("web settings require an interface kind")
		}
		return []LauncherEndpointOption{}, nil
	case "terminal":
		if len(d.Endpoints) != 0 || d.OpenAfterStart || d.PreferredEndpointID != "" {
			return nil, fmt.Errorf("terminal actions do not use web settings")
		}
		if d.Terminal == nil {
			return []LauncherEndpointOption{}, nil
		} // Old reserved metadata remains unavailable.
		if _, _, err := terminalCommandArgs(*d.Terminal, strings.Repeat("0", 64)); err != nil {
			return nil, err
		}
		return []LauncherEndpointOption{}, nil
	case "gui":
		if d.GUI == nil && len(d.Endpoints) == 0 {
			return []LauncherEndpointOption{}, nil
		}
		if err := validateGUIViewer(d); err != nil {
			return nil, err
		}
	case "web":
	default:
		return nil, fmt.Errorf("unknown application kind")
	}
	if len(d.Endpoints) == 0 || len(d.Endpoints) > 16 {
		return nil, fmt.Errorf("configure between 1 and 16 web endpoints")
	}
	choices := launcherMappings(mappings)
	out := []LauncherEndpointOption{}
	seen := map[string]bool{}
	for _, e := range d.Endpoints {
		if len(e.ID) == 0 || len(e.ID) > 64 || strings.IndexFunc(e.ID, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) >= 0 || seen[e.ID] {
			return nil, fmt.Errorf("invalid or duplicate endpoint ID")
		}
		seen[e.ID] = true
		found := false
		for _, m := range choices {
			if m.HostPort == e.Mapping.HostPort && m.ContainerPort == e.Mapping.ContainerPort && DeclaredEndpointsEquivalent(m.HostIP, e.Mapping.HostIP) && NormalizeProtocol(e.Mapping.Protocol) == "tcp" && e.Mapping.RangeSize <= 1 {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("endpoint %s is no longer published; configure the application again", e.ID)
		}
		target, err := launcherURL(e)
		if err != nil {
			return nil, err
		}
		out = append(out, LauncherEndpointOption{ID: e.ID, URL: target})
	}
	if d.PreferredEndpointID != "" && !seen[d.PreferredEndpointID] {
		return nil, fmt.Errorf("preferred endpoint is missing")
	}
	if d.OpenAfterStart && len(out) > 1 && d.PreferredEndpointID == "" {
		return nil, fmt.Errorf("choose a preferred endpoint for Start & Open")
	}
	return out, nil
}
