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
// told apart from the port-number colon. Returns "" only for an address that
// should be omitted entirely from the spec: HostIP == "" (unspecified —
// Podman's own default bind, never emitted) or the internal "*" shorthand
// used in a few host-listener contexts as an equivalent of unspecified.
//
// An explicit "0.0.0.0" is deliberately NOT collapsed into omission here:
// "" (unspecified/default Podman bind) and "0.0.0.0" (an explicit IPv4
// wildcard) are distinct declarations that must round-trip distinctly, even
// though both are classified as wildcard exposure for conflict/exposure
// purposes elsewhere. Silently canonicalizing an explicit 0.0.0.0 into
// omission would discard the operator's explicit choice without proof that
// omission means the same thing on every target (Compose/Quadlet defaults,
// or a future non-Podman runtime, are not guaranteed to match Podman's own
// default bind).
func FormatHostBind(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" || addr == "*" {
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
// "containerPort/protocol" alone when no host port is set. When RangeSize is
// greater than 1, both the host and container port components are rendered
// as an inclusive "start-end" range (e.g. "8000-8005:9000-9005/tcp") — the
// Podman range syntax, which requires both sides to name the same count of
// ports. This is the one canonical formatter for that grammar; the Podman -p
// builder, Compose writer, Quadlet writer, and generated snippets all call
// this instead of duplicating colon-splitting logic that breaks on IPv6
// addresses.
func FormatPublishSpec(m PortMapping) string {
	proto := NormalizeProtocol(m.Protocol)
	bind := FormatHostBind(m.HostIP)
	hostComponent := formatPortComponent(m.HostPort, m.RangeSize)
	containerComponent := formatPortComponent(m.ContainerPort, m.RangeSize)
	switch {
	case bind != "" && m.HostPort != 0:
		return fmt.Sprintf("%s:%s:%s/%s", bind, hostComponent, containerComponent, proto)
	case bind != "" && m.HostPort == 0:
		// An explicit host address with an auto-assigned host port (an
		// unmanaged/ad-hoc mapping with HostPort==0) must still restrict the
		// bind to that address: dropping the address here — as a naive
		// "no host port set" fallback would — silently widens exposure from
		// the requested interface to all interfaces, since podman -p
		// <containerPort>/proto alone binds every interface. The
		// double-colon form (host::container) is Podman/Quadlet's own
		// syntax for "this address, auto-assigned port".
		return fmt.Sprintf("%s::%s/%s", bind, containerComponent, proto)
	case m.HostPort != 0:
		return fmt.Sprintf("%s:%s/%s", hostComponent, containerComponent, proto)
	default:
		return fmt.Sprintf("%s/%s", containerComponent, proto)
	}
}

// formatPortComponent renders a single port number, or (when rangeSize > 1)
// an inclusive "start-end" range string.
func formatPortComponent(start uint16, rangeSize int) string {
	if rangeSize > 1 {
		end := int(start) + rangeSize - 1
		return fmt.Sprintf("%d-%d", start, end)
	}
	return strconv.Itoa(int(start))
}

// parsePortComponent parses a single publish-spec port component, which is
// either a plain port number or an inclusive "start-end" range. It returns
// the starting port and a count: 0 means the component was empty (not
// specified at all), 1 means a single explicit port, and >1 means a range of
// that many ports. This is the range-aware counterpart of a bare
// strconv.ParseUint used before range support existed.
func parsePortComponent(s string) (start uint16, count int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	if idx := strings.Index(s, "-"); idx != -1 {
		startStr := s[:idx]
		endStr := s[idx+1:]
		st, errStart := strconv.ParseUint(startStr, 10, 32)
		en, errEnd := strconv.ParseUint(endStr, 10, 32)
		if errStart != nil || errEnd != nil {
			return 0, 0, fmt.Errorf("invalid port range %q", s)
		}
		if st == 0 {
			return 0, 0, fmt.Errorf("port range %q cannot start at port 0", s)
		}
		if en < st {
			return 0, 0, fmt.Errorf("port range %q ends before it starts", s)
		}
		if en > 65535 {
			return 0, 0, fmt.Errorf("port range %q exceeds the maximum port 65535", s)
		}
		return uint16(st), int(en-st) + 1, nil
	}
	v, parseErr := strconv.ParseUint(s, 10, 16)
	if parseErr != nil {
		return 0, 0, parseErr
	}
	return uint16(v), 1, nil
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
//	8000-8005:9000-9005/tcp  (port ranges; host and container ranges must
//	                          contain the same number of ports)
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

	hStart, hCount, err := parsePortComponent(hPortStr)
	if err != nil {
		return nil, fmt.Errorf("invalid host port in spec %q: %w", entry, err)
	}
	cStart, cCount, err := parsePortComponent(cPortStr)
	if err != nil {
		return nil, fmt.Errorf("invalid container port in spec %q: %w", entry, err)
	}
	if cCount == 0 || cStart == 0 {
		return nil, fmt.Errorf("invalid container port in spec %q", entry)
	}

	rangeSize := 0
	if hCount > 1 || cCount > 1 {
		effectiveHostCount := hCount
		if hPortStr == "" {
			// No host port at all (container-only publish): nothing to
			// reconcile counts against, the container-side range stands on
			// its own.
			effectiveHostCount = cCount
		}
		if effectiveHostCount != cCount {
			return nil, fmt.Errorf("host and container port ranges in spec %q must contain the same number of ports (%d vs %d)", entry, effectiveHostCount, cCount)
		}
		rangeSize = cCount
	}

	// hostIP is left exactly as parsed: "" means the spec never named a host
	// address at all (unspecified/default Podman bind), which is distinct
	// from an explicit "0.0.0.0" (IPv4 wildcard) or "::" (IPv6 wildcard).
	// Defaulting an omitted address to "0.0.0.0" here would make
	// "8080:80/tcp" and "0.0.0.0:8080:80/tcp" indistinguishable, silently
	// losing the operator's explicit choice on the next format/round-trip.
	return &PortMapping{
		HostIP:        hostIP,
		HostPort:      hStart,
		ContainerPort: cStart,
		Protocol:      proto,
		RangeSize:     rangeSize,
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
