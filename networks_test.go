package main

import (
	"testing"
)

func TestParseNetworksInspectJSON(t *testing.T) {
	rawJSON := `[
  {
    "name": "podman",
    "id": "2f2c8230af94294a",
    "driver": "bridge",
    "network_interface": "podman0",
    "created": "2026-08-30T06:00:00Z",
    "subnets": [
      {
        "subnet": "10.88.0.0/16",
        "gateway": "10.88.0.1"
      }
    ],
    "ipv6_enabled": false,
    "internal": false,
    "dns_enabled": true,
    "labels": {}
  },
  {
    "name": "isolated-net",
    "id": "8a7b6c5d4e3f2a1b",
    "driver": "bridge",
    "created": "2026-08-30T07:00:00Z",
    "subnets": [
      {
        "subnet": "10.89.10.0/24",
        "gateway": "10.89.10.1"
      }
    ],
    "ipv6_enabled": true,
    "internal": true,
    "dns_enabled": false,
    "labels": {
      "com.example.env": "production"
    }
  }
]`

	networks, err := ParseNetworksInspectJSON([]byte(rawJSON))
	if err != nil {
		t.Fatalf("unexpected error parsing networks JSON: %v", err)
	}

	if len(networks) != 2 {
		t.Fatalf("expected 2 networks, got %d", len(networks))
	}

	n1 := networks[0]
	if n1.Name != "podman" || n1.Driver != "bridge" || !n1.DNSEnabled || n1.Internal {
		t.Errorf("unexpected network 1: %+v", n1)
	}
	if len(n1.Subnets) != 1 || n1.Subnets[0].Subnet != "10.88.0.0/16" || n1.Subnets[0].Gateway != "10.88.0.1" {
		t.Errorf("unexpected subnet for network 1: %+v", n1.Subnets)
	}

	n2 := networks[1]
	if n2.Name != "isolated-net" || !n2.Internal || !n2.IPv6Enabled || n2.DNSEnabled {
		t.Errorf("unexpected network 2: %+v", n2)
	}
}
