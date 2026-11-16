//go:build integration && linux

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Run only in an explicitly selected compatibility environment. The harness
// supplies an expected engine version so an old engine or a skipped live test
// cannot accidentally produce a successful compatibility result.
func TestPodmanCompatibility(t *testing.T) {
	if os.Getenv("PODDER_ASSURANCE_INTEGRATION") != "1" {
		t.Skip("requires an explicitly provisioned isolated rootless environment")
	}
	expected := strings.TrimSuffix(os.Getenv("PODDER_COMPAT_EXPECT_VERSION"), ".")
	major, err := strconv.Atoi(strings.SplitN(expected, ".", 2)[0])
	if err != nil || major < 5 {
		t.Fatal("PODDER_COMPAT_EXPECT_VERSION must identify a Podman 5+ version")
	}
	p, r, image := assuranceStoreWithRuntime(t, "overlay", "systemd")
	stdout, stderr, err := r.Run("podman", "info", "--format", "json")
	if err != nil {
		t.Fatalf("engine info: %s %v", stderr, err)
	}
	var info struct {
		Version struct{ Version string }
		Host    struct {
			CgroupVersion  string
			NetworkBackend string
			OCIRuntime     struct{ Name string }
		}
	}
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version.Version != expected && !strings.HasPrefix(info.Version.Version, expected+".") {
		t.Fatalf("expected Podman %s, got %s", expected, info.Version.Version)
	}
	if info.Host.CgroupVersion != "v2" || info.Host.NetworkBackend != "netavark" {
		t.Fatalf("expected a cgroup-v2/netavark environment: %+v", info.Host)
	}
	t.Logf("Podman %s: rootless overlay/systemd, cgroup %s, network %s, OCI runtime %s",
		info.Version.Version, info.Host.CgroupVersion, info.Host.NetworkBackend, info.Host.OCIRuntime.Name)

	t.Run("default_network_http_adoption_mutation_redeploy", func(t *testing.T) {
		name := "compat-web"
		port := r2port(t)
		out, stderr, err := r.Run("podman", "run", "-d", "--name", name,
			"--label", "org.podder.assurance=round2", "-p", fmt.Sprintf("127.0.0.1:%d:8080/tcp", port),
			"--", image, "serve")
		if err != nil {
			t.Fatalf("HTTP fixture: %s %v", stderr, err)
		}
		originalID := strings.TrimSpace(out)
		if err := validateFullContainerID(originalID); err != nil {
			t.Fatal(err)
		}
		raw, stderr, err := r.Run("podman", "inspect", "--format", "json", "--", originalID)
		if err != nil {
			t.Fatalf("unmodified inspect: %s %v", stderr, err)
		}
		inspected, err := parseSingleInspect(raw)
		if err != nil {
			t.Fatal(err)
		}
		if inspected.HostConfig.NetworkMode != "pasta" {
			t.Fatalf("expected the modern default pasta network, got %q", inspected.HostConfig.NetworkMode)
		}
		t.Logf("Observed default rootless network: %s", inspected.HostConfig.NetworkMode)
		compatibilityHTTP(t, port)
		assessment, err := p.InspectContainerForAdoption(originalID)
		if err != nil || assessment == nil || !assessment.CanAdopt {
			t.Fatalf("raw default inspect was refused: %+v %v", assessment, err)
		}
		adopted, err := p.AdoptContainer(originalID, name)
		if err != nil || adopted == nil || !adopted.Success {
			t.Fatalf("HTTP adoption failed: %+v %v", adopted, err)
		}
		current := compatibilityService(t, p, name, image)
		if current.Id == originalID {
			t.Fatal("adoption did not replace the original container")
		}
		compatibilityHTTP(t, port)
		editor, err := p.GetContainerPortEditState(current.Id)
		if err != nil {
			t.Fatal(err)
		}
		newPort := r2port(t)
		for newPort == port {
			newPort = r2port(t)
		}
		changed, err := p.MutateContainerPorts(PortMutationRequest{
			ContainerID: current.Id, ServiceName: name, Revision: editor.Revision,
			NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: newPort, ContainerPort: 8080, Protocol: "tcp"}},
		})
		if err != nil || changed == nil || !changed.Success || !changed.ConfigurationVerified {
			t.Fatalf("HTTP port mutation failed: %+v %v", changed, err)
		}
		if changed.NewContainerID == current.Id {
			t.Fatal("port mutation did not replace the container")
		}
		compatibilityHTTP(t, newPort)
		assuranceWait(t, "previous published port closes", func() bool {
			connection, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
			if err != nil {
				return true
			}
			connection.Close()
			return false
		})
		deployed, err := p.DeploySpec(name)
		if err != nil || deployed == nil || !deployed.Success {
			t.Fatalf("HTTP redeploy failed: %+v %v", deployed, err)
		}
		if deployed.NewContainerID == changed.NewContainerID {
			t.Fatal("redeploy did not replace the container")
		}
		current = compatibilityService(t, p, name, image)
		if current.Id != deployed.NewContainerID {
			t.Fatal("service identity differs after redeploy")
		}
		compatibilityHTTP(t, newPort)
		r2cleanup(t, p, r, image, name)
	})

	t.Run("bridge_network_and_connected_container_discovery", func(t *testing.T) {
		name := "compat-net-" + newSessionID()[:12]
		out, stderr, err := r.Run("podman", "network", "create", "--label", "org.podder.compatibility="+name, "--", name)
		if err != nil {
			t.Fatalf("network create: %s %v", stderr, err)
		}
		_ = out
		network := compatibilityNetwork(t, p, name)
		if network.ID == "" || network.Driver != "bridge" || len(network.Subnets) == 0 || network.Labels["org.podder.compatibility"] != name {
			t.Fatalf("network identity/IPAM not discovered: %+v", network)
		}
		networkID := network.ID
		t.Cleanup(func() {
			containers, err := p.ListContainers(true)
			if err != nil {
				t.Error(err)
				return
			}
			for _, c := range containers {
				if c.Labels["org.podder.compatibility.network"] != name {
					continue
				}
				if !sameImageIdentity(c.ImageID, image) || validateFullContainerID(c.Id) != nil {
					t.Error("network fixture cleanup identity mismatch")
					return
				}
				if _, stderr, err := r.Run("podman", "rm", "-f", "--", c.Id); err != nil {
					t.Errorf("network fixture cleanup: %s %v", stderr, err)
					return
				}
			}
			current := compatibilityNetwork(t, p, name)
			if current.ID != networkID || current.Labels["org.podder.compatibility"] != name {
				t.Error("network cleanup identity mismatch")
				return
			}
			if _, stderr, err := r.Run("podman", "network", "rm", "--", networkID); err != nil {
				t.Errorf("network cleanup: %s %v", stderr, err)
			}
		})
		out, stderr, err = r.Run("podman", "run", "-d", "--name", "compat-network-app", "--network", name,
			"--label", "org.podder.assurance=round2", "--label", "org.podder.compatibility.network="+name, "--", image, "sleep")
		if err != nil {
			t.Fatalf("bridge fixture: %s %v", stderr, err)
		}
		id := strings.TrimSpace(out)
		if err := validateFullContainerID(id); err != nil {
			t.Fatal(err)
		}
		network = compatibilityNetwork(t, p, name)
		// The Networks view intentionally returns a 12-character display ID;
		// all engine operations and cleanup above retain the full ID.
		if len(network.ConnectedContainers) != 1 || network.ConnectedContainers[0].ID != id[:12] || network.ConnectedContainers[0].Name != "compat-network-app" || network.ConnectedContainers[0].IPv4Address == "" {
			t.Fatalf("live bridge attachment not discovered: %+v", network.ConnectedContainers)
		}
	})
}

func compatibilityService(t *testing.T, p *PodmanService, name, image string) *Container {
	t.Helper()
	containers, err := p.ListContainers(true)
	if err != nil {
		t.Fatal(err)
	}
	c := findContainerByName(containers, name)
	if c == nil || c.Labels["io.podder.managed"] != "true" || c.Labels["io.podder.service"] != name || !sameImageIdentity(c.ImageID, image) {
		t.Fatalf("managed service identity not preserved: %+v", c)
	}
	return c
}

func compatibilityNetwork(t *testing.T, p *PodmanService, name string) PodmanNetwork {
	t.Helper()
	networks, err := p.ListNetworks()
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range networks {
		if network.Name == name {
			return network
		}
	}
	t.Fatalf("network %s not discovered", name)
	return PodmanNetwork{}
}

func compatibilityHTTP(t *testing.T, port uint16) {
	t.Helper()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	assuranceWait(t, fmt.Sprintf("HTTP forwarding on 127.0.0.1:%d", port), func() bool {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			return false
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 128))
		return err == nil && response.StatusCode == http.StatusOK && string(body) == "fixture"
	})
}
