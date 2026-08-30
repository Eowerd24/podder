package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// NetworkSubnet holds CIDR and gateway information for a network.
type NetworkSubnet struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway,omitempty"`
}

// ConnectedContainer represents a container attached to a Podman network.
type ConnectedContainer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IPv4Address string `json:"ipv4Address,omitempty"`
	IPv6Address string `json:"ipv6Address,omitempty"`
	MacAddress  string `json:"macAddress,omitempty"`
}

// PodmanNetwork models a local Podman bridge or custom network.
type PodmanNetwork struct {
	Name                string               `json:"name"`
	ID                  string               `json:"id"`
	Driver              string               `json:"driver"`
	Subnets             []NetworkSubnet      `json:"subnets"`
	IPv6Enabled         bool                 `json:"ipv6Enabled"`
	DNSEnabled          bool                 `json:"dnsEnabled"`
	Internal            bool                 `json:"internal"`
	ConnectedContainers []ConnectedContainer `json:"connectedContainers"`
	CreatedAt           string               `json:"createdAt,omitempty"`
	Labels              map[string]string    `json:"labels,omitempty"`
}

// rawNetworkInspect models raw JSON output from podman network inspect.
type rawNetworkInspect struct {
	Name        string `json:"name"`
	Id          string `json:"id"`
	Driver      string `json:"driver"`
	IPv6Enabled bool   `json:"ipv6_enabled"`
	DNSEnabled  bool   `json:"dns_enabled"`
	Internal    bool   `json:"internal"`
	Created     string `json:"created"`
	Subnets     []struct {
		Subnet  string `json:"subnet"`
		Gateway string `json:"gateway"`
	} `json:"subnets"`
	Labels map[string]string `json:"labels"`
}

// ParseNetworksInspectJSON parses raw JSON array from podman network inspect.
func ParseNetworksInspectJSON(data []byte) ([]PodmanNetwork, error) {
	var rawList []rawNetworkInspect
	if err := json.Unmarshal(data, &rawList); err != nil {
		return nil, fmt.Errorf("failed to parse network inspect JSON: %w", err)
	}

	var networks []PodmanNetwork
	for _, rn := range rawList {
		var subnets []NetworkSubnet
		for _, s := range rn.Subnets {
			subnets = append(subnets, NetworkSubnet{
				Subnet:  s.Subnet,
				Gateway: s.Gateway,
			})
		}

		networks = append(networks, PodmanNetwork{
			Name:                rn.Name,
			ID:                  rn.Id,
			Driver:              rn.Driver,
			Subnets:             subnets,
			IPv6Enabled:         rn.IPv6Enabled,
			DNSEnabled:          rn.DNSEnabled,
			Internal:            rn.Internal,
			CreatedAt:           rn.Created,
			Labels:              rn.Labels,
			ConnectedContainers: []ConnectedContainer{},
		})
	}

	return networks, nil
}

// ListNetworks lists all local Podman networks and attaches connected container IPs.
func (p *PodmanService) ListNetworks() ([]PodmanNetwork, error) {
	// 1. Inspect all networks
	stdout, stderr, err := p.runCommand("network", "inspect", "-a")
	if err != nil {
		// Fallback: list names first then inspect
		lsOut, lsErr, err2 := p.runCommand("network", "ls", "--format", "{{.Name}}")
		if err2 != nil {
			return nil, fmt.Errorf("failed to list networks: %v (stderr: %s)", err2, strings.TrimSpace(lsErr))
		}
		names := strings.Fields(lsOut)
		if len(names) == 0 {
			return []PodmanNetwork{}, nil
		}
		inspArgs := append([]string{"network", "inspect"}, names...)
		stdout, stderr, err = p.runCommand(inspArgs...)
		if err != nil {
			return nil, fmt.Errorf("failed to inspect networks: %v (stderr: %s)", err, strings.TrimSpace(stderr))
		}
	}

	networks, err := ParseNetworksInspectJSON([]byte(stdout))
	if err != nil {
		return nil, err
	}

	// 2. Discover connected container IP allocations from running containers
	containers, _ := p.ListContainers(true)
	for _, c := range containers {
		cName := "unnamed"
		if len(c.Names) > 0 {
			cName = strings.TrimPrefix(c.Names[0], "/")
		}

		// Inspect container networks
		inspOut, _, inspErr := p.runCommand("inspect", "--format", "json", c.Id)
		if inspErr == nil {
			var inspList []struct {
				NetworkSettings struct {
					Networks map[string]struct {
						IPAddress   string `json:"IPAddress"`
						GlobalIPv6  string `json:"GlobalIPv6Address"`
						MacAddress  string `json:"MacAddress"`
					} `json:"Networks"`
				} `json:"NetworkSettings"`
			}
			if json.Unmarshal([]byte(inspOut), &inspList) == nil && len(inspList) > 0 {
				for netName, netInfo := range inspList[0].NetworkSettings.Networks {
					for i := range networks {
						if networks[i].Name == netName {
							networks[i].ConnectedContainers = append(networks[i].ConnectedContainers, ConnectedContainer{
								ID:          c.Id[:min(len(c.Id), 12)],
								Name:        cName,
								IPv4Address: netInfo.IPAddress,
								IPv6Address: netInfo.GlobalIPv6,
								MacAddress:  netInfo.MacAddress,
							})
							break
						}
					}
				}
			}
		}
	}

	return networks, nil
}

// CreateNetwork creates a new Podman network with specified subnet and options.
func (p *PodmanService) CreateNetwork(name string, driver string, subnet string, gateway string, internal bool, dns bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("network name cannot be empty")
	}

	driver = strings.TrimSpace(driver)
	if driver == "" {
		driver = "bridge"
	}

	args := []string{"network", "create", "--driver", driver}

	subnet = strings.TrimSpace(subnet)
	if subnet != "" {
		args = append(args, "--subnet", subnet)
	}

	gateway = strings.TrimSpace(gateway)
	if gateway != "" {
		args = append(args, "--gateway", gateway)
	}

	if internal {
		args = append(args, "--internal")
	}

	if !dns {
		args = append(args, "--disable-dns")
	}

	args = append(args, name)

	_, stderr, err := p.runCommand(args...)
	if err != nil {
		return fmt.Errorf("failed to create network %s: %v (stderr: %s)", name, err, strings.TrimSpace(stderr))
	}

	return nil
}

// RemoveNetwork removes an existing Podman network.
func (p *PodmanService) RemoveNetwork(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("network name cannot be empty")
	}

	if name == "podman" || name == "default" {
		return fmt.Errorf("cannot remove default network '%s'", name)
	}

	_, stderr, err := p.runCommand("network", "rm", "-f", name)
	if err != nil {
		return fmt.Errorf("failed to remove network %s: %v (stderr: %s)", name, err, strings.TrimSpace(stderr))
	}

	return nil
}


