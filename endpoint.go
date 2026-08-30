package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// FormatHostBind formats a host bind address for embedding into a
// colon-delimited publish spec (Podman -p, Compose ports:, Quadlet
// PublishPort=), bracketing literal IPv6 addresses so the address can be
// told apart from the port-number colon. Returns "" for an address that
// should be omitted entirely from the spec (wildcard shorthand: an absent
// host address already means "all interfaces").
func FormatHostBind(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" || addr == "0.0.0.0" || addr == "*" {
		return ""
	}
	if strings.Contains(addr, ":") {
		if ip := net.ParseIP(addr); ip != nil {
			return "[" + addr + "]"
		}
	}
	return addr
}

// FormatPublishSpec formats a PortMapping into a Podman/Compose/Quadlet
// publish spec: "[hostIP:]hostPort:containerPort/protocol", or
// "containerPort/protocol" alone when no host port is set. This is the one
// canonical formatter for that grammar; the Podman -p builder, Compose
// writer, Quadlet writer, and generated snippets all call this instead of
// duplicating colon-splitting logic that breaks on IPv6 addresses.
func FormatPublishSpec(m PortMapping) string {
	proto := NormalizeProtocol(m.Protocol)
	bind := FormatHostBind(m.HostIP)
	switch {
	case bind != "" && m.HostPort != 0:
		return fmt.Sprintf("%s:%d:%d/%s", bind, m.HostPort, m.ContainerPort, proto)
	case m.HostPort != 0:
		return fmt.Sprintf("%d:%d/%s", m.HostPort, m.ContainerPort, proto)
	default:
		return fmt.Sprintf("%d/%s", m.ContainerPort, proto)
	}
}

// ParsePublishSpec parses a Podman/Compose/Quadlet-style publish spec into a
// PortMapping. Supports:
//
//	8080:80/tcp
//	8080:80            (defaults to tcp)
//	80                 (container-only, host port unset)
//	127.0.0.1:8080:80/tcp
//	[::1]:8080:80/tcp
//	[::]:8080:80/tcp
//
// IPv6 host addresses MUST be bracketed, matching Podman/Compose grammar;
// this is what makes the host-address / host-port / container-port split
// unambiguous. Plain strings.Split(entry, ":") breaks on IPv6 literals
// because they themselves contain colons.
func ParsePublishSpec(entry string) (*PortMapping, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil, fmt.Errorf("empty port spec")
	}

	proto := "tcp"
	if idx := strings.LastIndex(entry, "/"); idx != -1 {
		proto = entry[idx+1:]
		entry = entry[:idx]
	}
	proto = NormalizeProtocol(proto)
	if proto != "tcp" && proto != "udp" {
		return nil, fmt.Errorf("unsupported protocol in port spec %q", entry)
	}

	var hostIP, rest string
	if strings.HasPrefix(entry, "[") {
		closeIdx := strings.Index(entry, "]")
		if closeIdx == -1 {
			return nil, fmt.Errorf("unterminated IPv6 bracket in port spec %q", entry)
		}
		hostIP = entry[1:closeIdx]
		if net.ParseIP(hostIP) == nil {
			return nil, fmt.Errorf("invalid IPv6 host address %q in port spec", hostIP)
		}
		remainder := entry[closeIdx+1:]
		if !strings.HasPrefix(remainder, ":") {
			return nil, fmt.Errorf("expected ':' after IPv6 bracket in port spec %q", entry)
		}
		rest = strings.TrimPrefix(remainder, ":")
	} else {
		rest = entry
	}

	parts := strings.Split(rest, ":")
	var hPortStr, cPortStr string
	switch {
	case hostIP != "" && len(parts) == 2:
		hPortStr, cPortStr = parts[0], parts[1]
	case hostIP == "" && len(parts) == 3:
		hostIP = parts[0]
		if hostIP != "" && net.ParseIP(hostIP) == nil {
			return nil, fmt.Errorf("invalid host address %q in port spec", hostIP)
		}
		hPortStr, cPortStr = parts[1], parts[2]
	case hostIP == "" && len(parts) == 2:
		hPortStr, cPortStr = parts[0], parts[1]
	case hostIP == "" && len(parts) == 1:
		cPortStr = parts[0]
	default:
		return nil, fmt.Errorf("unrecognized port spec %q", entry)
	}

	var hPort uint64
	var err error
	if hPortStr != "" {
		hPort, err = strconv.ParseUint(hPortStr, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid host port in spec %q: %w", entry, err)
		}
	}
	cPort, err := strconv.ParseUint(cPortStr, 10, 16)
	if err != nil || cPort == 0 {
		return nil, fmt.Errorf("invalid container port in spec %q", entry)
	}

	bind := hostIP
	if bind == "" {
		bind = "0.0.0.0"
	}

	return &PortMapping{
		HostIP:        bind,
		HostPort:      uint16(hPort),
		ContainerPort: uint16(cPort),
		Protocol:      proto,
	}, nil
}

// ExpandPortRange returns the individual host/container port pairs implied
// by a mapping's RangeSize (e.g. a mapping representing 8000-8005 published
// to a matching span of container ports). A mapping with RangeSize <= 1
// expands to itself. This lets conflict detection treat a range as the set
// of individual ports it actually occupies instead of checking only the
// first port and silently reporting the rest as free.
func ExpandPortRange(m PortMapping) []PortMapping {
	size := m.RangeSize
	if size <= 1 {
		return []PortMapping{m}
	}
	out := make([]PortMapping, 0, size)
	for i := 0; i < size; i++ {
		if int(m.HostPort)+i > 65535 || int(m.ContainerPort)+i > 65535 {
			break
		}
		out = append(out, PortMapping{
			HostIP:        m.HostIP,
			HostPort:      m.HostPort + uint16(i),
			ContainerPort: m.ContainerPort + uint16(i),
			Protocol:      m.Protocol,
		})
	}
	return out
}

// expandClaimRange is ExpandPortRange's PortClaim counterpart, used by
// ClaimsConflict to compare ranged claims port-by-port instead of only
// checking their first port.
func expandClaimRange(c PortClaim) []PortClaim {
	size := c.RangeSize
	if size <= 1 {
		return []PortClaim{c}
	}
	out := make([]PortClaim, 0, size)
	for i := 0; i < size; i++ {
		if int(c.Port)+i > 65535 {
			break
		}
		cc := c
		cc.Port = c.Port + uint16(i)
		cc.RangeSize = 0
		out = append(out, cc)
	}
	return out
}

// EndpointsConflict reports whether two bind addresses on the same port and
// protocol would collide if both attempted to listen (e.g. a wildcard bind
// blocks every other bind on that port). This answers "can both be
// allocated safely" — it is deliberately looser than address equality, and
// must NOT be used to decide whether a runtime endpoint fulfills a registry
// declaration; see EndpointsEquivalentForReconciliation for that.
func EndpointsConflict(addrA, addrB string) bool {
	return AddressesConflict(addrA, addrB)
}

// ClassifyExposure buckets a candidate bind address into a coarse exposure
// category ("loopback", "specific-ip", "wildcard"), independent of any
// prior state. This is candidate classification, not a "change" — a
// brand-new wildcard mapping has nothing to have "changed" from. See
// AnalyzeExposureTransition for genuine before/after comparisons.
func ClassifyExposure(address string) string {
	return CategorizeExposure(address)
}

// ExposureTransition describes what changed (if anything) about a
// mapping's network exposure between an old and a new bind address.
type ExposureTransition struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Widened bool   `json:"widened"`
	Notice  string `json:"notice,omitempty"`
}

// exposureRank orders exposure categories from least to most reachable, so
// a transition can be judged as widening, narrowing, or unchanged.
func exposureRank(category string) int {
	switch category {
	case "loopback":
		return 0
	case "specific-ip":
		return 1
	case "wildcard":
		return 2
	default:
		return 1
	}
}

// AnalyzeExposureTransition compares the exposure of an old and a new bind
// address and reports whether the change widens network reachability.
// Unlike a bare "is this wildcard" flag, this distinguishes an actual
// transition (loopback -> LAN, LAN -> wildcard, ...) from a brand-new
// mapping with no prior state, and it never claims Internet-"public"
// reachability merely because a service binds 0.0.0.0 — wildcard means all
// local interfaces, not necessarily Internet-public routability, which
// depends on firewall/NAT rules this function cannot see.
func AnalyzeExposureTransition(oldAddress, newAddress string) ExposureTransition {
	from := ClassifyExposure(oldAddress)
	to := ClassifyExposure(newAddress)

	t := ExposureTransition{From: from, To: to}
	if exposureRank(to) <= exposureRank(from) {
		return t
	}

	t.Widened = true
	switch {
	case from == "loopback" && to == "wildcard":
		t.Notice = "Exposure is widening from local-only (loopback) to all interfaces. Any network-reachable host may connect, subject to firewall rules."
	case from == "loopback" && to == "specific-ip":
		t.Notice = "Exposure is widening from local-only (loopback) to a specific network interface. Hosts able to reach that interface may connect."
	case from == "specific-ip" && to == "wildcard":
		t.Notice = "Exposure is widening from a specific network interface to all interfaces."
	default:
		t.Notice = "Network exposure is widening."
	}
	return t
}

// EndpointsEquivalentForReconciliation reports whether two bind addresses
// represent the same declared endpoint for reconciliation purposes. Unlike
// EndpointsConflict, wildcard does not equal everything here: a registry
// entry declaring 0.0.0.0:3000 is NOT satisfied by a runtime container that
// only bound 127.0.0.1:3000 (and vice versa) — the two would conflict at the
// socket layer if both tried to bind, but they are not the same declaration,
// and treating them as a match would hide a real "the service is bound more
// narrowly/broadly than the registry says" discrepancy.
func EndpointsEquivalentForReconciliation(addrA, addrB string) bool {
	return strings.EqualFold(NormalizeAddress(addrA), NormalizeAddress(addrB))
}
