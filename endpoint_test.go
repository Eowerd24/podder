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
		{"wildcard-omitted", PortMapping{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, "8080:80/tcp"},
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
		{"container-only", "80", &PortMapping{HostIP: "0.0.0.0", ContainerPort: 80, Protocol: "tcp"}, false},
		{"host-container", "8080:80", &PortMapping{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, false},
		{"host-container-proto", "8080:80/udp", &PortMapping{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "udp"}, false},
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
	inputs := []string{"8080:80/tcp", "127.0.0.1:8080:80/tcp", "[::1]:8080:80/tcp", "[::]:8080:80/udp"}
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
