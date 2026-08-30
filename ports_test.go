package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortMappingDisplayString(t *testing.T) {
	mapping1 := PortMapping{
		HostIP:        "127.0.0.1",
		HostPort:      3000,
		ContainerPort: 8080,
		Protocol:      "tcp",
	}
	if mapping1.DisplayString() != "127.0.0.1:3000 -> 8080/tcp" {
		t.Fatalf("unexpected display string: %s", mapping1.DisplayString())
	}

	mapping2 := PortMapping{
		HostIP:        "",
		HostPort:      5353,
		ContainerPort: 5353,
		Protocol:      "udp",
	}
	if mapping2.DisplayString() != "0.0.0.0:5353 -> 5353/udp" {
		t.Fatalf("unexpected display string: %s", mapping2.DisplayString())
	}
}

func TestCategorizeExposure(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"127.0.0.1", "loopback"},
		{"::1", "loopback"},
		{"[::1]", "loopback"},
		{"localhost", "loopback"},
		{"127.0.0.53", "loopback"},
		{"127.0.0.53%lo", "loopback"},
		{"0.0.0.0", "wildcard"},
		{"::", "wildcard"},
		{"*", "wildcard"},
		{"", "wildcard"},
		{"192.168.1.100", "specific-ip"},
		{"10.0.0.1", "specific-ip"},
		{"100.64.128.68", "specific-ip"},
	}

	for _, c := range cases {
		actual := CategorizeExposure(c.input)
		if actual != c.expected {
			t.Errorf("CategorizeExposure(%q) = %q; want %q", c.input, actual, c.expected)
		}
	}
}

func TestAddressesConflictMatrix(t *testing.T) {
	cases := []struct {
		addrA    string
		addrB    string
		conflict bool
		desc     string
	}{
		{"127.0.0.1", "127.0.0.1", true, "same specific IPv4 loopback"},
		{"192.168.0.15", "192.168.0.15", true, "same specific LAN IPv4"},
		{"127.0.0.1", "0.0.0.0", true, "specific vs wildcard"},
		{"0.0.0.0", "192.168.0.15", true, "wildcard vs specific"},
		{"0.0.0.0", "0.0.0.0", true, "wildcard vs wildcard"},
		{"*", "127.0.0.1", true, "* wildcard vs specific"},
		{"", "127.0.0.1", true, "empty wildcard vs specific"},
		{"::", "::", true, "IPv6 wildcard vs wildcard"},
		{"::", "127.0.0.1", true, "IPv6 wildcard vs specific (conservative)"},
		{"127.0.0.1", "192.168.0.15", false, "different specific IPv4 addresses"},
		{"192.168.1.10", "192.168.1.20", false, "different specific LAN IPs"},
	}

	for _, c := range cases {
		actual := AddressesConflict(c.addrA, c.addrB)
		if actual != c.conflict {
			t.Errorf("AddressesConflict(%q, %q) = %v; want %v (%s)", c.addrA, c.addrB, actual, c.conflict, c.desc)
		}
	}
}

func TestClaimsConflict(t *testing.T) {
	claimTCP1 := PortClaim{Address: "127.0.0.1", Port: 3000, Protocol: "tcp"}
	claimTCP2 := PortClaim{Address: "127.0.0.1", Port: 3000, Protocol: "tcp"}
	claimTCPDiffPort := PortClaim{Address: "127.0.0.1", Port: 3001, Protocol: "tcp"}
	claimUDP := PortClaim{Address: "127.0.0.1", Port: 3000, Protocol: "udp"}

	if !ClaimsConflict(claimTCP1, claimTCP2) {
		t.Errorf("expected identical claims to conflict")
	}

	if ClaimsConflict(claimTCP1, claimTCPDiffPort) {
		t.Errorf("different ports should not conflict")
	}

	if ClaimsConflict(claimTCP1, claimUDP) {
		t.Errorf("TCP and UDP on same port should not conflict")
	}
}

func TestParseSSOutput(t *testing.T) {
	ssTCPOutput := `
LISTEN   0        4096                     100.64.128.68:51472      0.0.0.0:*   
LISTEN   0        512                            0.0.0.0:11435      0.0.0.0:*   users:(("llama.cpp",pid=12345,fd=4))
LISTEN   0        4096                     127.0.0.53%lo:53         0.0.0.0:*   
LISTEN   0        4096                              [::]:43355         [::]:*   
LISTEN   0        4096       [fd7a:115c:a1e0::6436:8045]:60772         [::]:*   
LISTEN   0        4096                                 *:3001             *:*   
LISTEN   0        4096                             [::1]:9090          [::]:*   
`

	listeners := parseSSOutput(ssTCPOutput, "tcp")
	if len(listeners) != 7 {
		t.Fatalf("expected 7 listeners, got %d", len(listeners))
	}

	// Verify llama.cpp listener with process info
	llama := listeners[1]
	if llama.Address != "0.0.0.0" || llama.Port != 11435 || llama.Protocol != "tcp" {
		t.Errorf("unexpected llama listener: %+v", llama)
	}
	if llama.Process != "llama.cpp" || llama.PID != 12345 {
		t.Errorf("unexpected process info: process=%s, pid=%d", llama.Process, llama.PID)
	}

	// Verify IPv6 bracket listener
	ipv6 := listeners[6]
	if ipv6.Address != "::1" || ipv6.Port != 9090 {
		t.Errorf("unexpected IPv6 listener: %+v", ipv6)
	}

	// Verify interface zone stripped
	dns := listeners[2]
	if dns.Address != "127.0.0.53" || dns.Port != 53 {
		t.Errorf("unexpected DNS listener: %+v", dns)
	}
}

func TestParseContainersJSONWithPorts(t *testing.T) {
	jsonInput := `[
		{
			"Id": "a3ad5a84973d949d80b8aca1bb60dd57d5e449ffad92cd0aabc91708cdb7b2e7",
			"Names": ["ctroadmap-beta"],
			"Image": "ghcr.io/noobcity99/ctroadmap:beta",
			"ImageID": "e58638098478f14030bb30a7f3c31bb2fe3fdd553ee07bf2f641466b9695c345",
			"State": "running",
			"Status": "Up 4 days",
			"Created": 1786891268,
			"ExitCode": 0,
			"Command": ["uvicorn", "backend.app.main:app"],
			"AutoRemove": false,
			"Ports": [
				{
					"host_ip": "",
					"container_port": 8088,
					"host_port": 8088,
					"range": 1,
					"protocol": "tcp"
				}
			]
		},
		{
			"Id": "c292aee71ee980da5d26f4e52fdfa5972e748c4d8ee771a61cdf2db035b5896c",
			"Names": ["openwebui"],
			"Image": "ghcr.io/open-webui/open-webui:main",
			"ImageID": "e97bf95319168ab7fdfc5bd1e869f6a1cf6349bdf6d3e8fe16c733d2ca473491",
			"State": "running",
			"Status": "Up 11 hours",
			"Created": 1787905023,
			"ExitCode": 0,
			"Command": ["bash", "start.sh"],
			"AutoRemove": false,
			"Ports": [
				{
					"host_ip": "127.0.0.1",
					"container_port": 8080,
					"host_port": 3000,
					"range": 1,
					"protocol": "tcp"
				},
				{
					"host_ip": "127.0.0.1",
					"container_port": 5353,
					"host_port": 5353,
					"range": 1,
					"protocol": "udp"
				}
			]
		}
	]`

	containers, err := parseContainersJSON([]byte(jsonInput))
	if err != nil {
		t.Fatalf("failed to parse containers JSON: %v", err)
	}

	if len(containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(containers))
	}

	c1 := containers[0]
	if len(c1.PortMappings) != 1 {
		t.Fatalf("expected 1 port mapping for c1, got %d", len(c1.PortMappings))
	}
	if c1.PortMappings[0].HostPort != 8088 || c1.PortMappings[0].ContainerPort != 8088 {
		t.Errorf("unexpected mapping for c1: %+v", c1.PortMappings[0])
	}

	c2 := containers[1]
	if len(c2.PortMappings) != 2 {
		t.Fatalf("expected 2 port mappings for c2, got %d", len(c2.PortMappings))
	}
	if c2.PortMappings[0].HostIP != "127.0.0.1" || c2.PortMappings[0].HostPort != 3000 || c2.PortMappings[0].ContainerPort != 8080 {
		t.Errorf("unexpected mapping 0 for c2: %+v", c2.PortMappings[0])
	}
	if c2.PortMappings[1].Protocol != "udp" || c2.PortMappings[1].HostPort != 5353 {
		t.Errorf("unexpected mapping 1 for c2: %+v", c2.PortMappings[1])
	}
}

func TestBuildRunArgsFromSpecWithMappings(t *testing.T) {
	spec := ContainerSpec{
		Name:  "web",
		Image: "nginx:alpine",
		PortMappings: []PortMapping{
			{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
			{HostIP: "127.0.0.1", HostPort: 5353, ContainerPort: 5353, Protocol: "udp"},
		},
	}

	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedFlags := []string{
		"-p", "127.0.0.1:8080:80/tcp",
		"-p", "127.0.0.1:5353:5353/udp",
	}

	for i := 0; i < len(expectedFlags); i += 2 {
		flag := expectedFlags[i]
		val := expectedFlags[i+1]
		found := false
		for j := 0; j < len(args)-1; j++ {
			if args[j] == flag && args[j+1] == val {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %s %s in args %v", flag, val, args)
		}
	}
}

func TestBuildRunArgsFromSpecMultipleBindsAndEnv(t *testing.T) {
	tempDir := t.TempDir()
	hostA := filepath.Join(tempDir, "a")
	hostB := filepath.Join(tempDir, "b")
	if err := os.Mkdir(hostA, 0o755); err != nil {
		t.Fatalf("failed to create dir a: %v", err)
	}
	if err := os.Mkdir(hostB, 0o755); err != nil {
		t.Fatalf("failed to create dir b: %v", err)
	}

	spec := ContainerSpec{
		Name:  "multi",
		Image: "alpine:latest",
		Binds: []BindMountSpec{
			{HostPath: hostA, ContainerPath: "/data/a", ReadOnly: true},
			{HostPath: hostB, ContainerPath: "/data/b"},
		},
		Env: map[string]string{"FOO": "bar", "BAZ": "qux"},
	}

	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mountCount := 0
	envCount := 0
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--mount" {
			mountCount++
		}
		if args[i] == "--env" {
			envCount++
		}
	}
	if mountCount != 2 {
		t.Errorf("expected 2 --mount flags (both binds preserved), got %d in %v", mountCount, args)
	}
	if envCount != 2 {
		t.Errorf("expected 2 --env flags (all env vars preserved), got %d in %v", envCount, args)
	}
}

func TestValidatePortMapping(t *testing.T) {
	service := &PodmanService{}

	// 1. Invalid host port
	res1, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP:        "127.0.0.1",
		HostPort:      0,
		ContainerPort: 80,
		Protocol:      "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res1.Valid {
		t.Errorf("expected port 0 to be invalid")
	}

	// 2. Invalid container port
	res2, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP:        "127.0.0.1",
		HostPort:      8080,
		ContainerPort: 0,
		Protocol:      "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Valid {
		t.Errorf("expected container port 0 to be invalid")
	}

	// 3. Wildcard exposure classification: a brand-new mapping has nothing
	// to have "changed" from, so ExposureChange must stay false absent an
	// OldHostIP to compare against — only the classification is reported.
	res3, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP:        "0.0.0.0",
		HostPort:      59999,
		ContainerPort: 80,
		Protocol:      "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res3.ExposureChange {
		t.Errorf("expected no ExposureChange for a brand-new mapping with no prior state: %+v", res3)
	}
	if res3.Exposure != "wildcard" {
		t.Errorf("expected wildcard exposure classification: %+v", res3)
	}
	for _, c := range res3.Checks {
		if strings.Contains(c.Message, "Public Exposure") {
			t.Errorf("expected wildcard wording to avoid asserting Internet-'public' reachability, got: %q", c.Message)
		}
	}

	// 4. A genuine exposure transition (loopback -> wildcard) IS reported
	// when the caller supplies the previous bind address.
	res4, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP:        "0.0.0.0",
		HostPort:      59998,
		ContainerPort: 80,
		Protocol:      "tcp",
		OldHostIP:     "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res4.ExposureChange {
		t.Errorf("expected ExposureChange=true for a loopback->wildcard transition: %+v", res4)
	}
}

// --- Item 9: safety-critical port discovery must fail closed ---

func TestCollectBlockingClaimsStrict_PodmanFailureBlocksValidation(t *testing.T) {
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(n string, args []string) (string, string, error) {
		return "", "podman: connection refused", fmt.Errorf("exit status 1")
	})
	service := &PodmanService{runner: runner}

	if _, err := service.CollectBlockingClaimsStrict(); err == nil {
		t.Fatalf("expected CollectBlockingClaimsStrict to fail when podman ps fails")
	}

	validation, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validation.Valid {
		t.Errorf("expected validation to fail closed when podman ps fails, got: %+v", validation.Checks)
	}

	if _, err := service.FindFreePort(3000, "tcp", "0.0.0.0"); err == nil {
		t.Errorf("expected FindFreePort to fail rather than report a confident free port when podman ps fails")
	}
}

func TestCollectBlockingClaimsStrict_SSFailureBlocksValidation(t *testing.T) {
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(n string, args []string) (string, string, error) {
		return "[]", "", nil
	})
	runner.On("ss", func(n string, args []string) (string, string, error) {
		return "", "ss: permission denied", fmt.Errorf("exit status 1")
	})
	service := &PodmanService{runner: runner}

	if _, err := service.CollectBlockingClaimsStrict(); err == nil {
		t.Fatalf("expected CollectBlockingClaimsStrict to fail when ss fails")
	}

	validation, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validation.Valid {
		t.Errorf("expected validation to fail closed when ss fails, got: %+v", validation.Checks)
	}

	if _, err := service.FindFreePort(3000, "tcp", "0.0.0.0"); err == nil {
		t.Errorf("expected FindFreePort to fail rather than report a confident free port when ss fails")
	}
}

func TestCollectBlockingClaimsStrict_EnabledRegistryReadFailureBlocksValidation(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	runner := newFakeCommandRunner()
	service := &PodmanService{runner: runner}
	if err := service.SaveSettings(AppSettings{
		PortRegistry: PortRegistryConfig{
			Enabled: true,
			// Points at a file that does not exist: the registry is
			// enabled and expected to be enforced, but cannot be read.
			Path: filepath.Join(tempDir, "does-not-exist.yaml"),
		},
	}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	if _, err := service.CollectBlockingClaimsStrict(); err == nil {
		t.Fatalf("expected CollectBlockingClaimsStrict to fail when the enabled registry file cannot be read")
	}

	validation, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validation.Valid {
		t.Errorf("expected validation to fail closed when the enabled registry cannot be read, got: %+v", validation.Checks)
	}
}

func TestCollectBlockingClaimsStrict_MalformedEnabledRegistryBlocksValidation(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	registryPath := filepath.Join(tempDir, "ports.yaml")
	if err := os.WriteFile(registryPath, []byte(malformedRegistryYAML), 0o644); err != nil {
		t.Fatalf("failed to write malformed registry fixture: %v", err)
	}

	runner := newFakeCommandRunner()
	service := &PodmanService{runner: runner}
	if err := service.SaveSettings(AppSettings{
		PortRegistry: PortRegistryConfig{Enabled: true, Path: registryPath},
	}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	if _, err := service.CollectBlockingClaimsStrict(); err == nil {
		t.Fatalf("expected CollectBlockingClaimsStrict to fail when the enabled registry file is malformed")
	}

	validation, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validation.Valid {
		t.Errorf("expected validation to fail closed when the enabled registry is malformed, got: %+v", validation.Checks)
	}
}

func TestCollectBlockingClaimsStrict_DisabledRegistryIsNotAnError(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	// The registry is optional: when it's simply not enabled, a missing or
	// unreadable file at whatever stale path happens to be configured must
	// not block ordinary (non-registry) validation.
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(n string, args []string) (string, string, error) { return "[]", "", nil })
	runner.On("ss", func(n string, args []string) (string, string, error) { return "", "", nil })
	service := &PodmanService{runner: runner}
	if err := service.SaveSettings(AppSettings{
		PortRegistry: PortRegistryConfig{Enabled: false, Path: filepath.Join(tempDir, "does-not-exist.yaml")},
	}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	claims, err := service.CollectBlockingClaimsStrict()
	if err != nil {
		t.Fatalf("expected no error when the registry is simply disabled, got: %v", err)
	}
	if len(claims) != 0 {
		t.Errorf("expected no claims, got: %+v", claims)
	}
}

// --- Item 8: a port range must be checked against existing claims across
// every port it actually occupies, not merely its first port. ---

func TestRangeVsExistingConflictDetected(t *testing.T) {
	runner := newFakeCommandRunner()
	// An existing container occupies port 8003 alone — squarely inside the
	// middle of a candidate 8000-8005 range.
	runner.On("podman ps", func(n string, args []string) (string, string, error) {
		return `[{"Id":"abc123","Names":["existing"],"State":"running","Ports":[{"host_ip":"0.0.0.0","container_port":8003,"host_port":8003,"range":1,"protocol":"tcp"}]}]`, "", nil
	})
	runner.On("ss", func(n string, args []string) (string, string, error) { return "", "", nil })
	service := &PodmanService{runner: runner}

	validation, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP: "0.0.0.0", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 6,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validation.Valid {
		t.Errorf("expected a range covering an already-claimed port to be rejected, got: %+v", validation.Checks)
	}

	// A range that does NOT touch the existing claim must be accepted.
	clearValidation, err := service.ValidatePortMapping(PortMappingRequest{
		HostIP: "0.0.0.0", HostPort: 8010, ContainerPort: 9010, Protocol: "tcp", RangeSize: 6,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !clearValidation.Valid {
		t.Errorf("expected a non-overlapping range to be accepted, got: %+v", clearValidation.Checks)
	}
}

func TestFindConflictWithIgnoreContainer(t *testing.T) {
	claims := []PortClaim{
		{
			Address:     "127.0.0.1",
			Port:        3000,
			Protocol:    "tcp",
			Source:      "podman",
			ContainerID: "abcdef123456",
			OwnerName:   "my-existing-container",
		},
		{
			Address:   "127.0.0.1",
			Port:      5678,
			Protocol:  "tcp",
			Source:    "host-listener",
			OwnerName: "n8n",
		},
	}

	// Conflict with another container
	candidate1 := PortClaim{
		Address:  "127.0.0.1",
		Port:     3000,
		Protocol: "tcp",
	}
	conflict1 := FindConflict(claims, candidate1, "")
	if conflict1 == nil || conflict1.OwnerName != "my-existing-container" {
		t.Errorf("expected conflict with my-existing-container, got %+v", conflict1)
	}

	// Ignored when container editing its own port
	conflictIgnored := FindConflict(claims, candidate1, "abcdef123456")
	if conflictIgnored != nil {
		t.Errorf("expected conflict to be ignored for same container ID, got %+v", conflictIgnored)
	}

	// Conflict with host listener
	candidate2 := PortClaim{
		Address:  "127.0.0.1",
		Port:     5678,
		Protocol: "tcp",
	}
	conflict2 := FindConflict(claims, candidate2, "")
	if conflict2 == nil || conflict2.OwnerName != "n8n" {
		t.Errorf("expected conflict with n8n, got %+v", conflict2)
	}
}
