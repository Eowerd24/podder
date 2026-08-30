package main

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// PortMapping represents a published container port mapping.
type PortMapping struct {
	HostIP        string `json:"hostIP"`
	HostPort      uint16 `json:"hostPort"`
	ContainerPort uint16 `json:"containerPort"`
	Protocol      string `json:"protocol"` // "tcp" or "udp"
	RangeSize     int    `json:"rangeSize,omitempty"`
}

// DisplayString returns a human-readable representation of the port mapping.
func (p PortMapping) DisplayString() string {
	bind := strings.TrimSpace(p.HostIP)
	if bind == "" {
		bind = "0.0.0.0"
	}
	proto := strings.ToLower(strings.TrimSpace(p.Protocol))
	if proto == "" {
		proto = "tcp"
	}
	return fmt.Sprintf("%s:%d -> %d/%s", bind, p.HostPort, p.ContainerPort, proto)
}

// ExposureCategory returns the exposure level of the mapping.
func (p PortMapping) ExposureCategory() string {
	return CategorizeExposure(p.HostIP)
}

// HostListener represents a socket listening on the host system.
type HostListener struct {
	Address  string `json:"address"`
	Port     uint16 `json:"port"`
	Protocol string `json:"protocol"` // "tcp" or "udp"
	Process  string `json:"process,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Source   string `json:"source"` // "host-listener"
}

// PortClaim represents a local endpoint claim used for conflict detection.
type PortClaim struct {
	Address     string `json:"address"`
	Port        uint16 `json:"port"`
	Protocol    string `json:"protocol"` // "tcp" or "udp"
	Source      string `json:"source"`   // "podman", "host-listener", "registry-active", "registry-reserved", "registry-planned"
	OwnerID     string `json:"ownerId,omitempty"`
	OwnerName   string `json:"ownerName,omitempty"`
	ContainerID string `json:"containerId,omitempty"`
}

// PortOverviewItem represents an entry in the aggregate Ports view.
type PortOverviewItem struct {
	ID                   string `json:"id"`
	Source               string `json:"source"` // "podman", "host-listener", "registry-declared"
	Owner                string `json:"owner"`
	ContainerID          string `json:"containerId,omitempty"`
	BindAddress          string `json:"bindAddress"`
	HostPort             uint16 `json:"hostPort"`
	ContainerPort        uint16 `json:"containerPort,omitempty"`
	Protocol             string `json:"protocol"`
	Exposure             string `json:"exposure"` // "loopback", "specific-ip", "wildcard", "lan", "public", etc.
	Status               string `json:"status"`   // "ACTIVE", "STOPPED_CONFIGURED", "CONFLICT", "RESERVED", "MISSING", "PLANNED"
	ConflictNote         string `json:"conflictNote,omitempty"`
	IsContainer          bool   `json:"isContainer"`
	RegistryID           string `json:"registryId,omitempty"`
	RegistryState        string `json:"registryState,omitempty"`
	Scope                string `json:"scope,omitempty"`
	ApplicationProtocol  string `json:"applicationProtocol,omitempty"`
	ReconciliationStatus string             `json:"reconciliationStatus"` // "MATCH", "UNDECLARED", "DECLARED_MISSING", "RESERVED_FREE", "RESERVED_IN_USE", "PLANNED", "HOST"
	Purpose              string             `json:"purpose,omitempty"`
	Provenance           WorkloadProvenance `json:"provenance,omitempty"`
}

// PortOverviewSummary holds high-level port stats.
type PortOverviewSummary struct {
	TotalPublishedMappings int    `json:"totalPublishedMappings"`
	TotalHostListeners     int    `json:"totalHostListeners"`
	TotalConflicts         int    `json:"totalConflicts"`
	UniquePorts            int    `json:"uniquePorts"`
	RegistryLoaded         bool   `json:"registryLoaded"`
	RegistryPath           string `json:"registryPath,omitempty"`
	RegistryMatch          int    `json:"registryMatch"`
	RegistryUndeclared     int    `json:"registryUndeclared"`
	RegistryMissing        int    `json:"registryMissing"`
	RegistryReserved       int    `json:"registryReserved"`
}

// PortOverview is the aggregate model returned to the frontend.
type PortOverview struct {
	Items   []PortOverviewItem  `json:"items"`
	Summary PortOverviewSummary `json:"summary"`
}

// PortMappingRequest represents a request to validate or configure a port mapping.
type PortMappingRequest struct {
	HostIP        string `json:"hostIP"`
	HostPort      uint16 `json:"hostPort"`
	ContainerPort uint16 `json:"containerPort"`
	Protocol      string `json:"protocol"`
	ContainerID   string `json:"containerId,omitempty"`
}

// ValidationCheck represents a single validation check result.
type ValidationCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
	Level   string `json:"level"` // "ok", "warning", "error"
}

// PortValidationResult contains the full analysis of a proposed mapping.
type PortValidationResult struct {
	Valid          bool              `json:"valid"`
	Exposure       string            `json:"exposure"` // "loopback", "specific-ip", "wildcard"
	ExposureChange bool              `json:"exposureChange"`
	ExposureNotice string            `json:"exposureNotice,omitempty"`
	Checks         []ValidationCheck `json:"checks"`
	ConflictWith   *PortClaim        `json:"conflictWith,omitempty"`
}

// CategorizeExposure determines whether an address is loopback, wildcard, or a specific IP.
func CategorizeExposure(address string) string {
	cleaned := strings.TrimSpace(address)
	cleaned = strings.Trim(cleaned, "[]")
	if idx := strings.Index(cleaned, "%"); idx != -1 {
		cleaned = cleaned[:idx]
	}

	if cleaned == "" || cleaned == "0.0.0.0" || cleaned == "::" || cleaned == "*" {
		return "wildcard"
	}

	ip := net.ParseIP(cleaned)
	if ip != nil {
		if ip.IsLoopback() || cleaned == "127.0.0.1" || cleaned == "::1" {
			return "loopback"
		}
		if ip.IsUnspecified() {
			return "wildcard"
		}
		return "specific-ip"
	}

	if strings.EqualFold(cleaned, "localhost") {
		return "loopback"
	}

	return "wildcard"
}

// NormalizeAddress normalizes IP strings for comparison.
func NormalizeAddress(addr string) string {
	cleaned := strings.TrimSpace(addr)
	cleaned = strings.Trim(cleaned, "[]")
	if idx := strings.Index(cleaned, "%"); idx != -1 {
		cleaned = cleaned[:idx]
	}
	if cleaned == "" || cleaned == "*" {
		return "0.0.0.0"
	}
	return cleaned
}

// NormalizeProtocol normalizes transport protocols ("tcp" or "udp").
func NormalizeProtocol(proto string) string {
	p := strings.ToLower(strings.TrimSpace(proto))
	if p == "" || p == "tcp" {
		return "tcp"
	}
	if p == "udp" {
		return "udp"
	}
	return p
}

// IsWildcardAddress returns true if the address represents all interfaces.
func IsWildcardAddress(addr string) bool {
	norm := NormalizeAddress(addr)
	return norm == "0.0.0.0" || norm == "::" || norm == "" || norm == "*"
}

// AddressesConflict determines if two bind addresses on the same port & protocol conflict.
func AddressesConflict(addrA, addrB string) bool {
	normA := NormalizeAddress(addrA)
	normB := NormalizeAddress(addrB)

	// If either is wildcard, it conflicts with all addresses on the host
	if IsWildcardAddress(normA) || IsWildcardAddress(normB) {
		return true
	}

	// Exact match
	if strings.EqualFold(normA, normB) {
		return true
	}

	// Loopback representations
	ipA := net.ParseIP(normA)
	ipB := net.ParseIP(normB)

	if ipA != nil && ipB != nil {
		if ipA.IsLoopback() && ipB.IsLoopback() {
			return ipA.Equal(ipB)
		}
		return ipA.Equal(ipB)
	}

	return false
}

// ClaimsConflict checks if two PortClaims conflict.
func ClaimsConflict(claimA, claimB PortClaim) bool {
	// Protocols must match
	if NormalizeProtocol(claimA.Protocol) != NormalizeProtocol(claimB.Protocol) {
		return false
	}

	// Ports must match
	if claimA.Port != claimB.Port {
		return false
	}

	return AddressesConflict(claimA.Address, claimB.Address)
}

// FindConflict finds the first conflicting claim from a list of existing claims.
func FindConflict(existingClaims []PortClaim, candidate PortClaim, ignoreContainerID string) *PortClaim {
	for _, claim := range existingClaims {
		if ignoreContainerID != "" && claim.ContainerID != "" && strings.HasPrefix(claim.ContainerID, ignoreContainerID) {
			continue
		}
		if ClaimsConflict(claim, candidate) {
			match := claim
			return &match
		}
	}
	return nil
}

// parseSSOutput parses raw output from ss -H -lnt or ss -H -lnu.
func parseSSOutput(output string, protocol string) []HostListener {
	var listeners []HostListener
	protocol = NormalizeProtocol(protocol)

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			continue
		}

		localAddrPort := fields[3]
		addr, port, err := parseAddressAndPort(localAddrPort)
		if err != nil {
			continue
		}

		var processName string
		var pid int
		if len(fields) >= 6 {
			processName, pid = extractProcessInfo(fields[5])
		}

		listeners = append(listeners, HostListener{
			Address:  addr,
			Port:     port,
			Protocol: protocol,
			Process:  processName,
			PID:      pid,
			Source:   "host-listener",
		})
	}

	return listeners
}

// parseAddressAndPort splits a local endpoint string into address and port uint16.
func parseAddressAndPort(endpoint string) (string, uint16, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", 0, fmt.Errorf("empty endpoint")
	}

	// Handle IPv6 enclosed in brackets like [::]:8080 or [::1]:9090
	if strings.HasPrefix(endpoint, "[") {
		closingIdx := strings.LastIndex(endpoint, "]")
		if closingIdx != -1 && len(endpoint) > closingIdx+1 && endpoint[closingIdx+1] == ':' {
			addr := endpoint[1:closingIdx]
			portStr := endpoint[closingIdx+2:]
			portNum, err := strconv.ParseUint(portStr, 10, 16)
			if err != nil {
				return "", 0, err
			}
			return addr, uint16(portNum), nil
		}
	}

	// Handle standard IPv4 or wildcard like 127.0.0.1:3000 or *:8088 or 0.0.0.0:80
	lastColon := strings.LastIndex(endpoint, ":")
	if lastColon == -1 {
		return "", 0, fmt.Errorf("no port found in endpoint: %s", endpoint)
	}

	addr := endpoint[:lastColon]
	portStr := endpoint[lastColon+1:]

	portNum, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return "", 0, err
	}

	// Remove interface zone identifiers like 127.0.0.53%lo
	if idx := strings.Index(addr, "%"); idx != -1 {
		addr = addr[:idx]
	}

	if addr == "*" {
		addr = "0.0.0.0"
	}

	return addr, uint16(portNum), nil
}

// extractProcessInfo extracts process name and PID from ss process output like:
// users:(("open-webui",pid=2200331,fd=7))
func extractProcessInfo(procField string) (string, int) {
	procField = strings.TrimSpace(procField)
	if procField == "" {
		return "", 0
	}

	var name string
	var pid int

	startQuote := strings.Index(procField, "\"")
	if startQuote != -1 {
		endQuote := strings.Index(procField[startQuote+1:], "\"")
		if endQuote != -1 {
			name = procField[startQuote+1 : startQuote+1+endQuote]
		}
	}

	pidIdx := strings.Index(procField, "pid=")
	if pidIdx != -1 {
		numStr := procField[pidIdx+4:]
		commaIdx := strings.IndexAny(numStr, ",)")
		if commaIdx != -1 {
			numStr = numStr[:commaIdx]
		}
		if p, err := strconv.Atoi(numStr); err == nil {
			pid = p
		}
	}

	return name, pid
}

// ListHostListeners executes ss to discover all listening sockets on the host.
func (p *PodmanService) ListHostListeners() ([]HostListener, error) {
	var allListeners []HostListener

	// 1. TCP listeners
	cmdTCP := exec.Command("ss", "-H", "-lntp")
	var stdoutTCP, stderrTCP bytes.Buffer
	cmdTCP.Stdout = &stdoutTCP
	cmdTCP.Stderr = &stderrTCP
	if err := cmdTCP.Run(); err != nil {
		cmdTCP = exec.Command("ss", "-H", "-lnt")
		stdoutTCP.Reset()
		cmdTCP.Stdout = &stdoutTCP
		if errFallback := cmdTCP.Run(); errFallback != nil {
			return nil, fmt.Errorf("failed to run ss for TCP: %v, stderr: %s", err, stderrTCP.String())
		}
	}
	allListeners = append(allListeners, parseSSOutput(stdoutTCP.String(), "tcp")...)

	// 2. UDP listeners
	cmdUDP := exec.Command("ss", "-H", "-lnup")
	var stdoutUDP, stderrUDP bytes.Buffer
	cmdUDP.Stdout = &stdoutUDP
	cmdUDP.Stderr = &stderrUDP
	if err := cmdUDP.Run(); err != nil {
		cmdUDP = exec.Command("ss", "-H", "-lnu")
		stdoutUDP.Reset()
		cmdUDP.Stdout = &stdoutUDP
		if errFallback := cmdUDP.Run(); errFallback != nil {
			return nil, fmt.Errorf("failed to run ss for UDP: %v, stderr: %s", err, stderrUDP.String())
		}
	}
	allListeners = append(allListeners, parseSSOutput(stdoutUDP.String(), "udp")...)

	return allListeners, nil
}

// CollectPortClaims aggregates claims from running containers, host listeners, and optional registry.
func (p *PodmanService) CollectPortClaims() ([]PortClaim, error) {
	var claims []PortClaim

	// 1. Collect from Podman containers
	containers, err := p.ListContainers(true)
	if err == nil {
		for _, c := range containers {
			cName := "unnamed"
			if len(c.Names) > 0 {
				cName = c.Names[0]
			}
			for _, m := range c.PortMappings {
				claims = append(claims, PortClaim{
					Address:     m.HostIP,
					Port:        m.HostPort,
					Protocol:    m.Protocol,
					Source:      "podman",
					OwnerID:     c.Id,
					OwnerName:   cName,
					ContainerID: c.Id,
				})
			}
		}
	}

	// 2. Collect from host listeners
	listeners, err := p.ListHostListeners()
	if err == nil {
		for _, l := range listeners {
			alreadyCovered := false
			for _, c := range claims {
				if c.Source == "podman" &&
					c.Port == l.Port &&
					NormalizeProtocol(c.Protocol) == NormalizeProtocol(l.Protocol) &&
					AddressesConflict(c.Address, l.Address) {
					alreadyCovered = true
					break
				}
			}

			if !alreadyCovered {
				owner := l.Process
				if owner == "" {
					owner = "Host Process"
				}
				claims = append(claims, PortClaim{
					Address:   l.Address,
					Port:      l.Port,
					Protocol:  l.Protocol,
					Source:    "host-listener",
					OwnerName: owner,
				})
			}
		}
	}

	// 3. Collect from external port registry if configured & enabled
	settings, err := p.GetSettings()
	if err == nil && settings.PortRegistry.Enabled && settings.PortRegistry.Path != "" {
		regResult, err := p.LoadPortRegistry(settings.PortRegistry.Path)
		if err == nil && regResult.Loaded {
			for _, rp := range regResult.Ports {
				claimSource := "registry-active"
				if rp.State == "reserved" {
					claimSource = "registry-reserved"
				} else if rp.State == "planned" {
					claimSource = "registry-planned"
				}

				owner := rp.Service
				if owner == "" {
					owner = rp.ID
				}

				claims = append(claims, PortClaim{
					Address:   rp.Listener.Address,
					Port:      rp.Listener.Port,
					Protocol:  rp.Protocol,
					Source:    claimSource,
					OwnerID:   rp.ID,
					OwnerName: owner,
				})
			}
		}
	}

	return claims, nil
}

// GetPortOverview aggregates all port mapping data, host listeners, and external registry reconciliation.
func (p *PodmanService) GetPortOverview() (*PortOverview, error) {
	var items []PortOverviewItem
	portSet := make(map[uint16]struct{})
	conflictCount := 0

	// 1. Get all containers (running and stopped)
	containers, err := p.ListContainers(true)
	if err != nil {
		return nil, fmt.Errorf("failed to get containers for port overview: %w", err)
	}

	// 2. Get host listeners
	listeners, _ := p.ListHostListeners()

	// 3. Load external registry if enabled
	settings, _ := p.GetSettings()
	var registryResult *PortRegistryResult
	if settings != nil && settings.PortRegistry.Enabled && settings.PortRegistry.Path != "" {
		registryResult, _ = p.LoadPortRegistry(settings.PortRegistry.Path)
	}

	matchedRegistryIDs := make(map[string]bool)
	registryMatchCount := 0
	registryUndeclaredCount := 0
	registryMissingCount := 0
	registryReservedCount := 0

	// Helper to find matching registry port
	findRegistryMatch := func(bind string, port uint16, protocol string, serviceName string) *RegistryPort {
		if registryResult == nil || !registryResult.Loaded {
			return nil
		}
		for i := range registryResult.Ports {
			rp := &registryResult.Ports[i]
			if rp.Listener.Port == port &&
				NormalizeProtocol(rp.Protocol) == NormalizeProtocol(protocol) &&
				AddressesConflict(bind, rp.Listener.Address) {
				return rp
			}
		}
		// Fallback match by service name and port
		if serviceName != "" {
			for i := range registryResult.Ports {
				rp := &registryResult.Ports[i]
				if rp.Listener.Port == port &&
					NormalizeProtocol(rp.Protocol) == NormalizeProtocol(protocol) &&
					strings.EqualFold(rp.Service, serviceName) {
					return rp
				}
			}
		}
		return nil
	}

	// 4. Collect claims for conflict cross-checking
	claims, _ := p.CollectPortClaims()

	totalPublished := 0
	// Add container port mappings
	for _, c := range containers {
		cName := "unnamed"
		if len(c.Names) > 0 {
			cName = c.Names[0]
		}

		for idx, m := range c.PortMappings {
			totalPublished++
			portSet[m.HostPort] = struct{}{}

			exposure := m.ExposureCategory()
			status := "ACTIVE"
			if strings.ToLower(c.State) != "running" {
				status = "STOPPED_CONFIGURED"
			}

			claim := PortClaim{
				Address:     m.HostIP,
				Port:        m.HostPort,
				Protocol:    m.Protocol,
				ContainerID: c.Id,
			}
			conflict := FindConflict(claims, claim, c.Id)
			var conflictNote string
			if conflict != nil && conflict.Source != "registry-active" {
				status = "CONFLICT"
				conflictCount++
				conflictNote = fmt.Sprintf("Conflicts with %s (%s)", conflict.OwnerName, conflict.Source)
			}

			// Reconciliation against registry
			reconcileStatus := "UNDECLARED"
			var regID, regState, scope, appProto, purpose string
			scope = exposure
			if regMatch := findRegistryMatch(m.HostIP, m.HostPort, m.Protocol, cName); regMatch != nil {
				reconcileStatus = "MATCH"
				matchedRegistryIDs[regMatch.ID] = true
				registryMatchCount++
				regID = regMatch.ID
				regState = regMatch.State
				scope = regMatch.Scope
				appProto = regMatch.ApplicationProtocol
				purpose = regMatch.Purpose
			} else {
				if registryResult != nil && registryResult.Loaded {
					registryUndeclaredCount++
				}
			}

			item := PortOverviewItem{
				ID:                   fmt.Sprintf("container-%s-%d", c.Id[:min(len(c.Id), 12)], idx),
				Source:               "podman",
				Owner:                cName,
				ContainerID:          c.Id,
				BindAddress:          m.HostIP,
				HostPort:             m.HostPort,
				ContainerPort:        m.ContainerPort,
				Protocol:             strings.ToUpper(m.Protocol),
				Exposure:             exposure,
				Status:               status,
				ConflictNote:         conflictNote,
				IsContainer:          true,
				RegistryID:           regID,
				RegistryState:        regState,
				Scope:                scope,
				ApplicationProtocol:  appProto,
				ReconciliationStatus: reconcileStatus,
				Purpose:              purpose,
				Provenance:           c.Provenance,
			}
			items = append(items, item)
		}
	}

	// Add host listeners that are not owned by Podman mappings
	for idx, l := range listeners {
		isPodmanListener := false
		for _, item := range items {
			if item.IsContainer &&
				item.HostPort == l.Port &&
				strings.EqualFold(item.Protocol, l.Protocol) &&
				AddressesConflict(item.BindAddress, l.Address) {
				isPodmanListener = true
				break
			}
		}

		if !isPodmanListener {
			portSet[l.Port] = struct{}{}
			owner := l.Process
			if owner == "" {
				owner = "Host listener"
			}

			exposure := CategorizeExposure(l.Address)
			status := "ACTIVE"

			claim := PortClaim{
				Address:  l.Address,
				Port:     l.Port,
				Protocol: l.Protocol,
			}
			conflict := FindConflict(claims, claim, "")
			var conflictNote string
			if conflict != nil && conflict.Source == "podman" {
				status = "CONFLICT"
				conflictCount++
				conflictNote = fmt.Sprintf("Conflicts with container %s", conflict.OwnerName)
			}

			// Check registry reconciliation for host listener
			reconcileStatus := "HOST"
			var regID, regState, scope, appProto, purpose string
			scope = exposure
			if regMatch := findRegistryMatch(l.Address, l.Port, l.Protocol, owner); regMatch != nil {
				reconcileStatus = "MATCH"
				matchedRegistryIDs[regMatch.ID] = true
				registryMatchCount++
				regID = regMatch.ID
				regState = regMatch.State
				scope = regMatch.Scope
				appProto = regMatch.ApplicationProtocol
				purpose = regMatch.Purpose
			} else {
				if registryResult != nil && registryResult.Loaded {
					registryUndeclaredCount++
				}
			}

			item := PortOverviewItem{
				ID:                   fmt.Sprintf("host-%s-%d-%d", l.Protocol, l.Port, idx),
				Source:               "host-listener",
				Owner:                owner,
				BindAddress:          l.Address,
				HostPort:             l.Port,
				Protocol:             strings.ToUpper(l.Protocol),
				Exposure:             exposure,
				Status:               status,
				ConflictNote:         conflictNote,
				IsContainer:          false,
				RegistryID:           regID,
				RegistryState:        regState,
				Scope:                scope,
				ApplicationProtocol:  appProto,
				ReconciliationStatus: reconcileStatus,
				Purpose:              purpose,
			}
			items = append(items, item)
		}
	}

	// 5. Add unmatched declared entries from registry (Missing, Reserved, Planned)
	if registryResult != nil && registryResult.Loaded {
		for idx, rp := range registryResult.Ports {
			if matchedRegistryIDs[rp.ID] {
				continue
			}

			portSet[rp.Listener.Port] = struct{}{}
			status := "DECLARED"
			reconcileStatus := "DECLARED_MISSING"

			// Check if host port is currently occupied by another socket
			isHostOccupied := false
			for _, l := range listeners {
				if l.Port == rp.Listener.Port &&
					NormalizeProtocol(l.Protocol) == NormalizeProtocol(rp.Protocol) &&
					AddressesConflict(l.Address, rp.Listener.Address) {
					isHostOccupied = true
					break
				}
			}

			if rp.State == "reserved" {
				registryReservedCount++
				if isHostOccupied {
					status = "RESERVED_IN_USE"
					reconcileStatus = "RESERVED_IN_USE"
				} else {
					status = "RESERVED_FREE"
					reconcileStatus = "RESERVED_FREE"
				}
			} else if rp.State == "planned" {
				status = "PLANNED"
				reconcileStatus = "PLANNED"
			} else {
				registryMissingCount++
				status = "DECLARED_MISSING"
				reconcileStatus = "DECLARED_MISSING"
			}

			exposure := CategorizeExposure(rp.Listener.Address)
			if rp.Scope != "" {
				exposure = rp.Scope
			}

			owner := rp.Service
			if owner == "" {
				owner = rp.ID
			}

			item := PortOverviewItem{
				ID:                   fmt.Sprintf("registry-%s-%d", rp.ID, idx),
				Source:               "registry-declared",
				Owner:                owner,
				BindAddress:          rp.Listener.Address,
				HostPort:             rp.Listener.Port,
				ContainerPort:        rp.Container.Port,
				Protocol:             strings.ToUpper(rp.Protocol),
				Exposure:             exposure,
				Status:               status,
				IsContainer:          rp.Container.Port > 0,
				RegistryID:           rp.ID,
				RegistryState:        rp.State,
				Scope:                rp.Scope,
				ApplicationProtocol:  rp.ApplicationProtocol,
				ReconciliationStatus: reconcileStatus,
				Purpose:              rp.Purpose,
			}
			items = append(items, item)
		}
	}

	overview := &PortOverview{
		Items: items,
		Summary: PortOverviewSummary{
			TotalPublishedMappings: totalPublished,
			TotalHostListeners:     len(listeners),
			TotalConflicts:         conflictCount,
			UniquePorts:            len(portSet),
			RegistryLoaded:         registryResult != nil && registryResult.Loaded,
			RegistryPath:           settings.PortRegistry.Path,
			RegistryMatch:          registryMatchCount,
			RegistryUndeclared:     registryUndeclaredCount,
			RegistryMissing:        registryMissingCount,
			RegistryReserved:       registryReservedCount,
		},
	}

	return overview, nil
}

// ValidatePortMapping validates syntax, conflict safety, and exposure for a requested mapping.
func (p *PodmanService) ValidatePortMapping(req PortMappingRequest) (*PortValidationResult, error) {
	result := &PortValidationResult{
		Valid:  true,
		Checks: []ValidationCheck{},
	}

	// 1. Port range validation
	if req.HostPort == 0 {
		result.Valid = false
		result.Checks = append(result.Checks, ValidationCheck{
			Name:    "Host Port",
			Passed:  false,
			Message: "Host port must be between 1 and 65535",
			Level:   "error",
		})
	} else {
		result.Checks = append(result.Checks, ValidationCheck{
			Name:    "Host Port Range",
			Passed:  true,
			Message: fmt.Sprintf("Host port %d is valid", req.HostPort),
			Level:   "ok",
		})
	}

	if req.ContainerPort == 0 {
		result.Valid = false
		result.Checks = append(result.Checks, ValidationCheck{
			Name:    "Container Target Port",
			Passed:  false,
			Message: "Container target port must be between 1 and 65535",
			Level:   "error",
		})
	} else {
		result.Checks = append(result.Checks, ValidationCheck{
			Name:    "Target Port Range",
			Passed:  true,
			Message: fmt.Sprintf("Container target port %d is valid", req.ContainerPort),
			Level:   "ok",
		})
	}

	// 2. Protocol check
	proto := NormalizeProtocol(req.Protocol)
	if proto != "tcp" && proto != "udp" {
		result.Valid = false
		result.Checks = append(result.Checks, ValidationCheck{
			Name:    "Protocol",
			Passed:  false,
			Message: "Protocol must be TCP or UDP",
			Level:   "error",
		})
	} else {
		result.Checks = append(result.Checks, ValidationCheck{
			Name:    "Protocol",
			Passed:  true,
			Message: strings.ToUpper(proto),
			Level:   "ok",
		})
	}

	// 3. Exposure categorization
	exposure := CategorizeExposure(req.HostIP)
	result.Exposure = exposure
	if exposure == "wildcard" {
		result.ExposureChange = true
		result.ExposureNotice = "Binding to wildcard (0.0.0.0 or all interfaces) exposes this port to external network traffic subject to host firewall rules."
		result.Checks = append(result.Checks, ValidationCheck{
			Name:    "Exposure Level",
			Passed:  true,
			Message: "Wildcard / Public Exposure (0.0.0.0 / all interfaces)",
			Level:   "warning",
		})
	} else if exposure == "loopback" {
		result.Checks = append(result.Checks, ValidationCheck{
			Name:    "Exposure Level",
			Passed:  true,
			Message: "Local-only / Loopback (127.0.0.1)",
			Level:   "ok",
		})
	} else {
		result.Checks = append(result.Checks, ValidationCheck{
			Name:    "Exposure Level",
			Passed:  true,
			Message: fmt.Sprintf("Specific Host IP (%s)", req.HostIP),
			Level:   "ok",
		})
	}

	// 4. Collision check against active claims (containers, host listeners, registry reservations)
	claims, err := p.CollectPortClaims()
	if err == nil && req.HostPort > 0 {
		candidate := PortClaim{
			Address:  req.HostIP,
			Port:     req.HostPort,
			Protocol: proto,
		}
		conflict := FindConflict(claims, candidate, req.ContainerID)
		if conflict != nil {
			if conflict.Source == "registry-reserved" {
				result.Valid = false
				result.ConflictWith = conflict
				result.Checks = append(result.Checks, ValidationCheck{
					Name:    "Registry Reservation",
					Passed:  false,
					Message: fmt.Sprintf("Port %d/%s is reserved in external registry for %s", req.HostPort, strings.ToUpper(proto), conflict.OwnerName),
					Level:   "error",
				})
			} else {
				result.Valid = false
				result.ConflictWith = conflict
				result.Checks = append(result.Checks, ValidationCheck{
					Name:    "Port Availability",
					Passed:  false,
					Message: fmt.Sprintf("Port %d/%s is already in use by %s (%s)", req.HostPort, strings.ToUpper(proto), conflict.OwnerName, conflict.Source),
					Level:   "error",
				})
			}
		} else {
			result.Checks = append(result.Checks, ValidationCheck{
				Name:    "Port Availability",
				Passed:  true,
				Message: fmt.Sprintf("Port %d/%s is free and available", req.HostPort, strings.ToUpper(proto)),
				Level:   "ok",
			})
		}
	}

	return result, nil
}

// FindFreePort finds the next available port on the host for the given protocol and bind address.
func (p *PodmanService) FindFreePort(startPort uint16, protocol string, bindAddress string) (uint16, error) {
	proto := NormalizeProtocol(protocol)
	if startPort < 1024 {
		startPort = 3000
	}

	claims, err := p.CollectPortClaims()
	if err != nil {
		claims = []PortClaim{}
	}

	for port := startPort; port < 65535; port++ {
		candidate := PortClaim{
			Address:  bindAddress,
			Port:     port,
			Protocol: proto,
		}

		if FindConflict(claims, candidate, "") == nil {
			return port, nil
		}
	}

	return 0, fmt.Errorf("no free port found in range %d-65535", startPort)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
