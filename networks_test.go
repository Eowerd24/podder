package main

import (
	"strings"
	"sync"
	"testing"
)

// networkSim is a scripted CommandRunner covering just the podman
// subcommands ListNetworks/CreateNetwork/RemoveNetwork issue, so network
// mutation tests never create or delete a real host network.
type networkSim struct {
	mu                       sync.Mutex
	networksJSON             string
	containersJSON           string
	perContainerNetworksJSON map[string]string
	createCalls              [][]string
	removeCalls              [][]string
}

func (n *networkSim) Run(name string, args ...string) (string, string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if name != "podman" || len(args) == 0 {
		return "", "", nil
	}
	switch args[0] {
	case "network":
		if len(args) > 1 {
			switch args[1] {
			case "inspect":
				return n.networksJSON, "", nil
			case "create":
				n.createCalls = append(n.createCalls, append([]string{}, args...))
				return "", "", nil
			case "rm":
				n.removeCalls = append(n.removeCalls, append([]string{}, args...))
				return "", "", nil
			}
		}
	case "ps":
		if n.containersJSON == "" {
			return "[]", "", nil
		}
		return n.containersJSON, "", nil
	case "inspect":
		id := args[len(args)-1]
		if j, ok := n.perContainerNetworksJSON[id]; ok {
			return j, "", nil
		}
		return "[]", "", nil
	}
	return "", "", nil
}

const bridgeNetworkJSON = `[{"name":"podman","id":"net1","driver":"bridge","subnets":[{"subnet":"10.88.0.0/16","gateway":"10.88.0.1"}],"ipv6_enabled":false,"internal":false,"dns_enabled":true,"labels":{}}]`

func TestCreateNetwork_RejectsInvalidName(t *testing.T) {
	sim := &networkSim{}
	svc := &PodmanService{runner: sim}
	if err := svc.CreateNetwork("bad name!", "bridge", "", "", false, true); err == nil {
		t.Fatalf("expected invalid network name to be rejected")
	}
	if len(sim.createCalls) != 0 {
		t.Errorf("expected no podman command to run for an invalid name")
	}
}

func TestCreateNetwork_RejectsUnsupportedDriver(t *testing.T) {
	sim := &networkSim{}
	svc := &PodmanService{runner: sim}
	if err := svc.CreateNetwork("mynet", "macvlan", "10.10.0.0/24", "10.10.0.1", false, true); err == nil {
		t.Fatalf("expected unsupported driver to be rejected")
	}
	if len(sim.createCalls) != 0 {
		t.Errorf("expected no podman command to run for an unsupported driver")
	}
}

func TestCreateNetwork_RejectsInvalidSubnet(t *testing.T) {
	sim := &networkSim{}
	svc := &PodmanService{runner: sim}
	if err := svc.CreateNetwork("mynet", "bridge", "not-a-cidr", "", false, true); err == nil {
		t.Fatalf("expected invalid subnet CIDR to be rejected")
	}
}

func TestCreateNetwork_RejectsGatewayOutsideSubnet(t *testing.T) {
	sim := &networkSim{}
	svc := &PodmanService{runner: sim}
	if err := svc.CreateNetwork("mynet", "bridge", "10.10.0.0/24", "10.20.0.1", false, true); err == nil {
		t.Fatalf("expected gateway outside the subnet to be rejected")
	}
}

func TestCreateNetwork_RejectsGatewayFamilyMismatch(t *testing.T) {
	sim := &networkSim{}
	svc := &PodmanService{runner: sim}
	if err := svc.CreateNetwork("mynet", "bridge", "10.10.0.0/24", "fe80::1", false, true); err == nil {
		t.Fatalf("expected an IPv6 gateway on an IPv4 subnet to be rejected")
	}
}

func TestCreateNetwork_RejectsOverlappingSubnet(t *testing.T) {
	sim := &networkSim{networksJSON: bridgeNetworkJSON}
	svc := &PodmanService{runner: sim}
	// 10.88.5.0/24 is within the existing 10.88.0.0/16 network.
	if err := svc.CreateNetwork("mynet", "bridge", "10.88.5.0/24", "10.88.5.1", false, true); err == nil {
		t.Fatalf("expected overlapping subnet to be rejected")
	}
	if len(sim.createCalls) != 0 {
		t.Errorf("expected no podman network create call for an overlapping subnet")
	}
}

func TestCreateNetwork_SuccessSendsExpectedArgs(t *testing.T) {
	sim := &networkSim{networksJSON: bridgeNetworkJSON}
	svc := &PodmanService{runner: sim}
	if err := svc.CreateNetwork("mynet", "", "10.90.0.0/24", "10.90.0.1", true, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sim.createCalls) != 1 {
		t.Fatalf("expected exactly 1 network create call, got %d", len(sim.createCalls))
	}
	joined := strings.Join(sim.createCalls[0], " ")
	for _, want := range []string{"--driver bridge", "--subnet 10.90.0.0/24", "--gateway 10.90.0.1", "--internal", "--disable-dns", "mynet"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected create args to contain %q, got: %s", want, joined)
		}
	}
}

func TestRemoveNetwork_RefusesDefaultNetwork(t *testing.T) {
	sim := &networkSim{}
	svc := &PodmanService{runner: sim}
	if err := svc.RemoveNetwork("podman"); err == nil {
		t.Fatalf("expected removal of the default network to be refused")
	}
}

func TestRemoveNetwork_RefusesWhenInUse(t *testing.T) {
	networksJSON := `[{"name":"busy-net","id":"net2","driver":"bridge","subnets":[{"subnet":"10.91.0.0/24","gateway":"10.91.0.1"}],"ipv6_enabled":false,"internal":false,"dns_enabled":true,"labels":{}}]`
	containersJSON := `[{"Id":"c1","Names":["web"],"State":"running"}]`
	perContainer := map[string]string{
		"c1": `[{"NetworkSettings":{"Networks":{"busy-net":{"IPAddress":"10.91.0.5"}}}}]`,
	}
	sim := &networkSim{networksJSON: networksJSON, containersJSON: containersJSON, perContainerNetworksJSON: perContainer}
	svc := &PodmanService{runner: sim}

	if err := svc.RemoveNetwork("busy-net"); err == nil {
		t.Fatalf("expected removal of an in-use network to be refused")
	}
	if len(sim.removeCalls) != 0 {
		t.Errorf("expected no podman network rm call for an in-use network")
	}
}

func TestRemoveNetwork_SucceedsWhenUnusedAndNeverForces(t *testing.T) {
	sim := &networkSim{networksJSON: bridgeNetworkJSON, containersJSON: "[]"}
	svc := &PodmanService{runner: sim}

	if err := svc.RemoveNetwork("unused-net"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sim.removeCalls) != 1 {
		t.Fatalf("expected exactly 1 network rm call, got %d", len(sim.removeCalls))
	}
	for _, a := range sim.removeCalls[0] {
		if a == "-f" || a == "--force" {
			t.Errorf("expected ordinary network removal to never force, got args: %v", sim.removeCalls[0])
		}
	}
}

func TestSubnetsOverlapAndGatewayValidation(t *testing.T) {
	a, _ := validateSubnetCIDR("10.0.0.0/24")
	b, _ := validateSubnetCIDR("10.0.0.128/25")
	if !subnetsOverlap(a, b) {
		t.Errorf("expected 10.0.0.0/24 and 10.0.0.128/25 to overlap")
	}
	c, _ := validateSubnetCIDR("10.1.0.0/24")
	if subnetsOverlap(a, c) {
		t.Errorf("expected 10.0.0.0/24 and 10.1.0.0/24 to not overlap")
	}

	if err := validateGateway("10.0.0.1", a); err != nil {
		t.Errorf("expected gateway within subnet to validate, got: %v", err)
	}
	if err := validateGateway("not-an-ip", a); err == nil {
		t.Errorf("expected an invalid gateway IP to fail validation")
	}
}

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
