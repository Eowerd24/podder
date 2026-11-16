package main

import (
	"reflect"
	"testing"
)

func TestFormatPublishSpec(t *testing.T) {
	cases := []struct {
		name string
		m    PortMapping
		want string
	}{
		{"ipv4-specific", PortMapping{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, "127.0.0.1:8080:80/tcp"},
		// An explicit "0.0.0.0" must NOT be canonicalized into omission: it
		// is a distinct declaration from an unspecified host bind, even
		// though both are wildcard exposure for conflict/exposure purposes.
		{"wildcard-explicit", PortMapping{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, "0.0.0.0:8080:80/tcp"},
		{"unspecified-omitted", PortMapping{HostIP: "", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, "8080:80/tcp"},
		{"star-omitted", PortMapping{HostIP: "*", HostPort: 53, ContainerPort: 53, Protocol: "udp"}, "53:53/udp"},
		{"no-host-port", PortMapping{ContainerPort: 80, Protocol: "tcp"}, "80/tcp"},
		{"ipv6-loopback", PortMapping{HostIP: "::1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, "[::1]:8080:80/tcp"},
		{"ipv6-wildcard", PortMapping{HostIP: "::", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, "[::]:8080:80/tcp"},
		{"default-protocol", PortMapping{HostIP: "127.0.0.1", HostPort: 9000, ContainerPort: 9000}, "127.0.0.1:9000:9000/tcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatPublishSpec(tc.m)
			if got != tc.want {
				t.Errorf("FormatPublishSpec(%+v) = %q, want %q", tc.m, got, tc.want)
			}
		})
	}
}

func TestParsePublishSpec(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    *PortMapping
		wantErr bool
	}{
		// An omitted host address must parse to HostIP=="" (unspecified —
		// Podman's own default bind), NOT be defaulted to "0.0.0.0": those
		// are distinct declarations and must remain distinguishable from an
		// explicit "0.0.0.0:..." spec (see TestParsePublishSpecPreservesExplicitWildcard).
		{"container-only", "80", &PortMapping{HostIP: "", ContainerPort: 80, Protocol: "tcp"}, false},
		{"host-container", "8080:80", &PortMapping{HostIP: "", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, false},
		{"host-container-proto", "8080:80/udp", &PortMapping{HostIP: "", HostPort: 8080, ContainerPort: 80, Protocol: "udp"}, false},
		{"ipv4-explicit-wildcard", "0.0.0.0:8080:80/tcp", &PortMapping{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, false},
		{"ipv4-full", "127.0.0.1:8080:80/tcp", &PortMapping{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, false},
		{"ipv6-loopback", "[::1]:8080:80/tcp", &PortMapping{HostIP: "::1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, false},
		{"ipv6-wildcard", "[::]:8080:80/tcp", &PortMapping{HostIP: "::", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, false},
		{"ipv6-unterminated", "[::1:8080:80/tcp", nil, true},
		{"ipv6-missing-colon-after-bracket", "[::1]8080:80/tcp", nil, true},
		{"invalid-protocol", "8080:80/sctp", nil, true},
		{"empty", "", nil, true},
		{"invalid-host-port", "abc:80/tcp", nil, true},
		{"zero-container-port", "8080:0/tcp", nil, true},
		{"too-many-parts", "a:b:c:d/tcp", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePublishSpec(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePublishSpec(%q) expected error, got %+v", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePublishSpec(%q) unexpected error: %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParsePublishSpec(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParsePublishSpecFormatPublishSpecRoundTrip(t *testing.T) {
	inputs := []string{
		"8080:80/tcp",
		"0.0.0.0:8080:80/tcp",
		"[::]:8080:80/tcp",
		"127.0.0.1:8080:80/tcp",
		"[::1]:8080:80/tcp",
		"[::]:8080:80/udp",
	}
	for _, in := range inputs {
		m, err := ParsePublishSpec(in)
		if err != nil {
			t.Fatalf("ParsePublishSpec(%q) failed: %v", in, err)
		}
		out := FormatPublishSpec(*m)
		if out != in {
			t.Errorf("round trip mismatch: parsed %q, formatted back as %q", in, out)
		}
	}
}

// TestUnspecifiedAndExplicitWildcardRemainDistinguishable is the exact
// regression this fix targets: an omitted host bind ("8080:80/tcp") and an
// explicit IPv4 wildcard ("0.0.0.0:8080:80/tcp") must not collapse into the
// same internal representation or the same formatted output — collapsing
// them would silently discard an operator's explicit choice.
func TestUnspecifiedAndExplicitWildcardRemainDistinguishable(t *testing.T) {
	unspecified, err := ParsePublishSpec("8080:80/tcp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	explicit, err := ParsePublishSpec("0.0.0.0:8080:80/tcp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if unspecified.HostIP == explicit.HostIP {
		t.Fatalf("expected distinct internal HostIP representations, got both %q", unspecified.HostIP)
	}
	if unspecified.HostIP != "" {
		t.Errorf("expected an omitted host bind to parse to HostIP==\"\", got %q", unspecified.HostIP)
	}
	if explicit.HostIP != "0.0.0.0" {
		t.Errorf("expected an explicit wildcard to parse to HostIP==\"0.0.0.0\", got %q", explicit.HostIP)
	}

	gotUnspecified := FormatPublishSpec(*unspecified)
	gotExplicit := FormatPublishSpec(*explicit)
	if gotUnspecified == gotExplicit {
		t.Fatalf("expected distinct formatted output, both formatted as %q", gotUnspecified)
	}
	if gotUnspecified != "8080:80/tcp" {
		t.Errorf("expected unspecified bind to format without a host component, got %q", gotUnspecified)
	}
	if gotExplicit != "0.0.0.0:8080:80/tcp" {
		t.Errorf("expected explicit wildcard to format with the explicit address preserved, got %q", gotExplicit)
	}
}

// TestFormatPublishSpecAutoAssignedPortPreservesHostAddress proves that an
// explicit host address combined with HostPort==0 (auto-assign, valid for
// unmanaged/ad-hoc mappings) is not silently dropped — losing it would
// widen exposure from the requested interface to all interfaces.
func TestFormatPublishSpecAutoAssignedPortPreservesHostAddress(t *testing.T) {
	m := PortMapping{HostIP: "127.0.0.1", HostPort: 0, ContainerPort: 80, Protocol: "tcp"}
	got := FormatPublishSpec(m)
	want := "127.0.0.1::80/tcp"
	if got != want {
		t.Fatalf("FormatPublishSpec(auto-assigned with explicit host) = %q, want %q", got, want)
	}

	parsed, err := ParsePublishSpec(got)
	if err != nil {
		t.Fatalf("ParsePublishSpec(%q) failed: %v", got, err)
	}
	if parsed.HostIP != "127.0.0.1" || parsed.HostPort != 0 || parsed.ContainerPort != 80 {
		t.Fatalf("round trip mismatch: %+v", parsed)
	}
}

func TestFormatPublishSpecRange(t *testing.T) {
	m := PortMapping{HostIP: "", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 6}
	got := FormatPublishSpec(m)
	want := "8000-8005:9000-9005/tcp"
	if got != want {
		t.Fatalf("FormatPublishSpec(range) = %q, want %q", got, want)
	}

	// An explicit wildcard range must preserve the explicit address too.
	mExplicit := PortMapping{HostIP: "0.0.0.0", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 6}
	gotExplicit := FormatPublishSpec(mExplicit)
	wantExplicit := "0.0.0.0:8000-8005:9000-9005/tcp"
	if gotExplicit != wantExplicit {
		t.Fatalf("FormatPublishSpec(explicit wildcard range) = %q, want %q", gotExplicit, wantExplicit)
	}

	// A bound host address plus a range must bracket/prefix correctly.
	m2 := PortMapping{HostIP: "127.0.0.1", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 3}
	got2 := FormatPublishSpec(m2)
	want2 := "127.0.0.1:8000-8002:9000-9002/tcp"
	if got2 != want2 {
		t.Fatalf("FormatPublishSpec(bound range) = %q, want %q", got2, want2)
	}
}

func TestParsePublishSpecRange(t *testing.T) {
	m, err := ParsePublishSpec("8000-8005:9000-9005/tcp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.HostPort != 8000 || m.ContainerPort != 9000 || m.RangeSize != 6 {
		t.Fatalf("unexpected parsed range mapping: %+v", m)
	}

	// Mismatched range counts between host and container sides must be
	// rejected — Podman requires them to name the same number of ports.
	if _, err := ParsePublishSpec("8000-8005:9000-9002/tcp"); err == nil {
		t.Fatalf("expected mismatched host/container range counts to be rejected")
	}

	// A range end below its start is invalid.
	if _, err := ParsePublishSpec("8005-8000:9000-9005/tcp"); err == nil {
		t.Fatalf("expected inverted range to be rejected")
	}

	// A range that overflows past the maximum port must be rejected.
	if _, err := ParsePublishSpec("65530-65540:9000-9010/tcp"); err == nil {
		t.Fatalf("expected an overflowing port range to be rejected")
	}
}

func TestParsePublishSpecFormatPublishSpecRangeRoundTrip(t *testing.T) {
	inputs := []string{
		"8000-8005:9000-9005/tcp",
		"127.0.0.1:8000-8005:9000-9005/tcp",
		"[::1]:8000-8002:9000-9002/udp",
	}
	for _, in := range inputs {
		m, err := ParsePublishSpec(in)
		if err != nil {
			t.Fatalf("ParsePublishSpec(%q) failed: %v", in, err)
		}
		out := FormatPublishSpec(*m)
		if out != in {
			t.Errorf("range round trip mismatch: parsed %q, formatted back as %q", in, out)
		}
	}
}

func TestExpandPortRange(t *testing.T) {
	m := PortMapping{HostIP: "0.0.0.0", HostPort: 8000, ContainerPort: 8000, Protocol: "tcp", RangeSize: 6}
	got := ExpandPortRange(m)
	if len(got) != 6 {
		t.Fatalf("expected 6 expanded ports, got %d", len(got))
	}
	if got[0].HostPort != 8000 || got[5].HostPort != 8005 {
		t.Fatalf("unexpected expanded range: %+v", got)
	}

	single := ExpandPortRange(PortMapping{HostPort: 80, ContainerPort: 80})
	if len(single) != 1 || single[0].HostPort != 80 {
		t.Fatalf("expected single-element expansion for RangeSize<=1, got %+v", single)
	}
}

func TestClaimsConflictRangeAware(t *testing.T) {
	rangeClaim := PortClaim{Address: "0.0.0.0", Port: 8000, Protocol: "tcp", RangeSize: 6}

	inRange := PortClaim{Address: "0.0.0.0", Port: 8003, Protocol: "tcp"}
	if !ClaimsConflict(rangeClaim, inRange) {
		t.Errorf("expected 8000-8005 to conflict with 8003")
	}

	outOfRange := PortClaim{Address: "0.0.0.0", Port: 8006, Protocol: "tcp"}
	if ClaimsConflict(rangeClaim, outOfRange) {
		t.Errorf("expected 8000-8005 to NOT conflict with 8006")
	}

	differentProto := PortClaim{Address: "0.0.0.0", Port: 8003, Protocol: "udp"}
	if ClaimsConflict(rangeClaim, differentProto) {
		t.Errorf("expected TCP range to not conflict with UDP claim solely on port number")
	}
}

func TestEndpointsEquivalentForReconciliation(t *testing.T) {
	// A wildcard registry declaration is NOT the same declared endpoint as a
	// runtime loopback-only bind, even though the two would conflict if both
	// tried to bind (EndpointsConflict), because they are not the same
	// declaration for reconciliation purposes.
	if EndpointsEquivalentForReconciliation("0.0.0.0", "127.0.0.1") {
		t.Errorf("expected wildcard and loopback to NOT be equivalent for reconciliation")
	}
	if !EndpointsConflict("0.0.0.0", "127.0.0.1") {
		t.Errorf("expected wildcard and loopback to conflict at the socket layer")
	}
	if !EndpointsEquivalentForReconciliation("127.0.0.1", "127.0.0.1") {
		t.Errorf("expected identical addresses to be equivalent")
	}
	if !EndpointsEquivalentForReconciliation("0.0.0.0", "*") {
		t.Errorf("expected wildcard synonyms to be equivalent to each other")
	}
}

// TestIPv4AndIPv6WildcardStayDistinct locks in that an IPv4 wildcard
// (0.0.0.0) and an IPv6 wildcard (::) are never treated as the same
// abstract endpoint for registry reconciliation, even though — per the
// accepted, unchanged conflict model — both remain wildcard binds that
// conflict with any other candidate at the socket layer (the conservative,
// fail-closed default). Allocation-conflict semantics and
// registry-equivalence semantics are deliberately different questions.
func TestIPv4AndIPv6WildcardStayDistinct(t *testing.T) {
	if EndpointsEquivalentForReconciliation("0.0.0.0", "::") {
		t.Errorf("expected IPv4 wildcard and IPv6 wildcard to be distinct declared endpoints for reconciliation")
	}
	if NormalizeAddress("0.0.0.0") == NormalizeAddress("::") {
		t.Errorf("expected NormalizeAddress to keep IPv4 and IPv6 wildcards distinct")
	}
	// Conflict detection stays conservative: both are still wildcards that
	// block any other candidate bind on the same port, which is the
	// accepted allocation-conflict behavior this fix does not change.
	if !EndpointsConflict("0.0.0.0", "::") {
		t.Errorf("expected IPv4 and IPv6 wildcards to still conflict at the socket layer (fail-closed)")
	}
}

// --- v1.4 hardening: CanonicalDeclaredBind / DeclaredEndpointsEquivalent ---
//
// These lock in the P1 requirement that declared-endpoint EQUALITY never
// reuses the conflict-oriented normalization (NormalizeAddress/
// AddressesConflict/EndpointsConflict), which deliberately folds an omitted
// bind, "0.0.0.0", and "*" together for allocation-safety purposes. An
// omitted host address, an explicit IPv4/IPv6 wildcard, an IPv4/IPv6
// loopback, and a specific address must never be silently treated as the
// same DECLARATION.

func TestCanonicalDeclaredBind_DistinctCategoriesStayDistinct(t *testing.T) {
	forms := map[string]string{
		"omitted":       "",
		"ipv4-wildcard": "0.0.0.0",
		"ipv6-wildcard": "::",
		"ipv4-loopback": "127.0.0.1",
		"ipv6-loopback": "::1",
		"specific-ipv4": "192.168.1.50",
		"specific-ipv6": "2001:db8::1",
	}
	seen := make(map[string]string)
	for name, addr := range forms {
		canon := CanonicalDeclaredBind(addr)
		if existing, ok := seen[canon]; ok {
			t.Errorf("expected %q (%q) and %q (%q) to canonicalize distinctly, both got %q", name, addr, existing, forms[existing], canon)
		}
		seen[canon] = name
	}
}

func TestCanonicalDeclaredBind_SynonymsFold(t *testing.T) {
	// "*" is an internal host-listener shorthand for the IPv4 wildcard, and
	// "localhost" is a textual synonym for 127.0.0.1 -- these ARE the same
	// declaration, just spelled differently, and must canonicalize together.
	if CanonicalDeclaredBind("*") != CanonicalDeclaredBind("0.0.0.0") {
		t.Errorf("expected '*' and '0.0.0.0' to canonicalize to the same declared bind")
	}
	if CanonicalDeclaredBind("localhost") != CanonicalDeclaredBind("127.0.0.1") {
		t.Errorf("expected 'localhost' and '127.0.0.1' to canonicalize to the same declared bind")
	}
	if CanonicalDeclaredBind("[::1]") != CanonicalDeclaredBind("::1") {
		t.Errorf("expected bracketed and unbracketed IPv6 loopback to canonicalize to the same declared bind")
	}
}

func TestDeclaredEndpointsEquivalent_OmittedIsNotWildcard(t *testing.T) {
	// This is the exact discrepancy the v1.4 hardening pass closes: an
	// omitted host bind ("") and an explicit "0.0.0.0" are NOT the same
	// declaration, even though EndpointsConflict/AddressesConflict
	// (conflict/allocation-safety semantics) treat both as wildcard.
	if DeclaredEndpointsEquivalent("", "0.0.0.0") {
		t.Errorf("expected omitted bind and explicit 0.0.0.0 to NOT be declared-equivalent")
	}
	if !EndpointsConflict("", "0.0.0.0") {
		t.Errorf("expected omitted bind and explicit 0.0.0.0 to still conflict at the socket layer")
	}
	if DeclaredEndpointsEquivalent("", "::") {
		t.Errorf("expected omitted bind and explicit :: to NOT be declared-equivalent")
	}
	if DeclaredEndpointsEquivalent("127.0.0.1", "::1") {
		t.Errorf("expected IPv4 and IPv6 loopback to NOT be declared-equivalent")
	}
	if !DeclaredEndpointsEquivalent("127.0.0.1", "127.0.0.1") {
		t.Errorf("expected identical declared binds to be equivalent")
	}
	if !DeclaredEndpointsEquivalent("*", "0.0.0.0") {
		t.Errorf("expected the '*' shorthand and '0.0.0.0' to be declared-equivalent")
	}
}

func TestEndpointsEquivalentForReconciliation_DelegatesToDeclaredEquivalence(t *testing.T) {
	// EndpointsEquivalentForReconciliation must be a thin wrapper over
	// DeclaredEndpointsEquivalent, not its own independent implementation
	// that could silently drift back toward NormalizeAddress semantics.
	cases := [][2]string{
		{"", "0.0.0.0"},
		{"0.0.0.0", "::"},
		{"127.0.0.1", "::1"},
		{"127.0.0.1", "127.0.0.1"},
		{"*", "0.0.0.0"},
	}
	for _, c := range cases {
		if EndpointsEquivalentForReconciliation(c[0], c[1]) != DeclaredEndpointsEquivalent(c[0], c[1]) {
			t.Errorf("EndpointsEquivalentForReconciliation(%q, %q) diverged from DeclaredEndpointsEquivalent", c[0], c[1])
		}
	}
}
