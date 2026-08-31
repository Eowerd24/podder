package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerJSONParsing(t *testing.T) {
	jsonInput := `[
		{
			"Id": "f0d3a6ae9da9ce991ec825e4c81efcd3a3f6f5a02abbdffc0cc3914a4bbe7899",
			"Names": ["test-alpine"],
			"Image": "docker.io/library/alpine:latest",
			"ImageID": "d529dd0c6e5597ac7e4a3e2dea65c3fcc6173f4cae713c409265c1dd9914a11b",
			"State": "running",
			"Status": "Up Less than a second",
			"Created": 1783792600,
			"ExitCode": 0,
			"Command": ["sleep", "1000"],
			"AutoRemove": false
		}
	]`

	var containers []Container
	err := json.Unmarshal([]byte(jsonInput), &containers)
	if err != nil {
		t.Fatalf("Failed to parse container JSON: %v", err)
	}

	if len(containers) != 1 {
		t.Fatalf("Expected 1 container, got %d", len(containers))
	}

	c := containers[0]
	if c.Id != "f0d3a6ae9da9ce991ec825e4c81efcd3a3f6f5a02abbdffc0cc3914a4bbe7899" {
		t.Errorf("Expected ID f0d3a6ae9da9ce991ec825e4c81efcd3a3f6f5a02abbdffc0cc3914a4bbe7899, got %s", c.Id)
	}
	if len(c.Names) == 0 || c.Names[0] != "test-alpine" {
		t.Errorf("Expected name 'test-alpine', got %v", c.Names)
	}
	if c.State != "running" {
		t.Errorf("Expected state 'running', got %s", c.State)
	}
}

func TestImageJSONParsing(t *testing.T) {
	jsonInput := `[
		{
			"Id": "d529dd0c6e5597ac7e4a3e2dea65c3fcc6173f4cae713c409265c1dd9914a11b",
			"Names": ["docker.io/library/alpine:latest"],
			"Size": 8709729,
			"CreatedAt": "2026-06-16T00:01:29Z",
			"Containers": 0
		}
	]`

	var images []Image
	err := json.Unmarshal([]byte(jsonInput), &images)
	if err != nil {
		t.Fatalf("Failed to parse image JSON: %v", err)
	}

	if len(images) != 1 {
		t.Fatalf("Expected 1 image, got %d", len(images))
	}

	img := images[0]
	if img.Id != "d529dd0c6e5597ac7e4a3e2dea65c3fcc6173f4cae713c409265c1dd9914a11b" {
		t.Errorf("Expected ID d529dd0c6e5597ac7e4a3e2dea65c3fcc6173f4cae713c409265c1dd9914a11b, got %s", img.Id)
	}
	if len(img.Names) == 0 || img.Names[0] != "docker.io/library/alpine:latest" {
		t.Errorf("Expected name 'docker.io/library/alpine:latest', got %v", img.Names)
	}
	if img.Size != 8709729 {
		t.Errorf("Expected size 8709729, got %d", img.Size)
	}
}

func TestSystemInfoJSONParsing(t *testing.T) {
	jsonInput := `{
		"host": {
			"os": "linux",
			"kernel": "6.8.0-134-generic",
			"cpus": 2,
			"memTotal": 2063216640,
			"memFree": 354811904,
			"uptime": "9h 25m 20.00s",
			"distribution": {
				"distribution": "ubuntu",
				"version": "24.04"
			}
		},
		"store": {
			"containerStore": {
				"number": 5,
				"running": 2,
				"stopped": 3
			},
			"imageStore": {
				"number": 8
			}
		},
		"version": {
			"Version": "4.9.3"
		}
	}`

	var raw map[string]interface{}
	err := json.Unmarshal([]byte(jsonInput), &raw)
	if err != nil {
		t.Fatalf("Failed to parse raw system info: %v", err)
	}

	info := &SystemInfo{}

	if host, ok := raw["host"].(map[string]interface{}); ok {
		info.OS, _ = host["os"].(string)
		info.Kernel, _ = host["kernel"].(string)
		if cpusVal, ok := host["cpus"].(float64); ok {
			info.CPUs = int(cpusVal)
		}
		if memTotalVal, ok := host["memTotal"].(float64); ok {
			info.MemTotal = int64(memTotalVal)
		}
		if memFreeVal, ok := host["memFree"].(float64); ok {
			info.MemFree = int64(memFreeVal)
		}
		info.Uptime, _ = host["uptime"].(string)
		if dist, ok := host["distribution"].(map[string]interface{}); ok {
			distName, _ := dist["distribution"].(string)
			distVer, _ := dist["version"].(string)
			info.Distribution = distName + " " + distVer
		}
	}

	if store, ok := raw["store"].(map[string]interface{}); ok {
		if cStore, ok := store["containerStore"].(map[string]interface{}); ok {
			if num, ok := cStore["number"].(float64); ok {
				info.TotalContainers = int(num)
			}
			if run, ok := cStore["running"].(float64); ok {
				info.RunningContainers = int(run)
			}
			if stopped, ok := cStore["stopped"].(float64); ok {
				info.StoppedContainers = int(stopped)
			}
		}
		if iStore, ok := store["imageStore"].(map[string]interface{}); ok {
			if num, ok := iStore["number"].(float64); ok {
				info.TotalImages = int(num)
			}
		}
	}

	if version, ok := raw["version"].(map[string]interface{}); ok {
		info.PodmanVersion, _ = version["Version"].(string)
	}

	if info.OS != "linux" {
		t.Errorf("Expected OS 'linux', got %s", info.OS)
	}
	if info.CPUs != 2 {
		t.Errorf("Expected CPUs 2, got %d", info.CPUs)
	}
	if info.Distribution != "ubuntu 24.04" {
		t.Errorf("Expected Distribution 'ubuntu 24.04', got %s", info.Distribution)
	}
	if info.TotalContainers != 5 {
		t.Errorf("Expected TotalContainers 5, got %d", info.TotalContainers)
	}
	if info.RunningContainers != 2 {
		t.Errorf("Expected RunningContainers 2, got %d", info.RunningContainers)
	}
	if info.TotalImages != 8 {
		t.Errorf("Expected TotalImages 8, got %d", info.TotalImages)
	}
	if info.PodmanVersion != "4.9.3" {
		t.Errorf("Expected PodmanVersion '4.9.3', got %s", info.PodmanVersion)
	}
}

func TestBuildRunArgsFromSpecWithBindMount(t *testing.T) {
	tempDir := t.TempDir()
	hostPath := filepath.Join(tempDir, "content")
	if err := os.Mkdir(hostPath, 0o755); err != nil {
		t.Fatalf("failed to create host directory: %v", err)
	}

	spec := ContainerSpec{
		Name:  "demo",
		Image: "docker.io/library/nginx:latest",
		PortMappings: []PortMapping{
			{HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
		},
		Binds: []BindMountSpec{
			{HostPath: hostPath, ContainerPath: "/usr/share/nginx/html", ReadOnly: true},
		},
	}

	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		t.Fatalf("BuildRunArgsFromSpec returned error: %v", err)
	}

	expectedMount := "type=bind,src=" + hostPath + ",target=/usr/share/nginx/html,readonly"
	foundMount := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--mount" && args[i+1] == expectedMount {
			foundMount = true
			break
		}
	}

	if !foundMount {
		t.Fatalf("expected mount spec %q in args %v", expectedMount, args)
	}
}

func TestBuildRunArgsFromSpecRejectsIncompleteMount(t *testing.T) {
	spec := ContainerSpec{
		Image: "docker.io/library/alpine:latest",
		Binds: []BindMountSpec{
			{HostPath: "/tmp/example", ContainerPath: ""},
		},
	}
	if _, err := BuildRunArgsFromSpec(spec); err == nil {
		t.Fatal("expected missing container path to fail")
	}
}

func TestIsSupportedImageFile(t *testing.T) {
	if !isSupportedImageFile("/tmp/example.JPG") {
		t.Fatal("expected JPG file to be accepted")
	}

	if isSupportedImageFile("/tmp/example.txt") {
		t.Fatal("expected non-image file to be rejected")
	}
}

// fakePsRunner is a minimal fake runner for CreateContainer tests: it scripts
// `podman run` and reflects the resulting container back out of `podman ps`.
func fakePsRunner(containerID, name string, running bool) *fakeCommandRunner {
	f := newFakeCommandRunner()
	f.On("podman image", func(name_ string, args []string) (string, string, error) {
		return `[{"Id":"sha256:test-image"}]`, "", nil
	})
	created := false
	f.On("podman run", func(name_ string, args []string) (string, string, error) {
		created = true
		return containerID + "\n", "", nil
	})
	state := "running"
	if !running {
		state = "exited"
	}
	f.On("podman ps", func(n string, args []string) (string, string, error) {
		if !created {
			return "[]", "", nil
		}
		psJSON := `[{"Id":"` + containerID + `","Names":["` + name + `"],"Image":"alpine:latest","ImageID":"sha256:test-image","State":"` + state + `","Ports":[{"host_ip":"127.0.0.1","host_port":8080,"container_port":80,"protocol":"tcp","range":1}],"Labels":{"io.podder.managed":"true","io.podder.service":"` + name + `","io.podder.schema-version":"2"}}]`
		return psJSON, "", nil
	})
	return f
}

func TestCreateContainerManagedCommitsSpecOnSuccess(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)
	setTestConfigHome(t, tempDir)

	runner := fakePsRunner("abc123def456", "svc1", true)
	svc := &PodmanService{runner: runner}

	req := ContainerCreateRequest{
		Image:   "alpine:latest",
		Name:    "svc1",
		Managed: true,
		PortMappings: []PortMapping{
			{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
		},
	}

	result, err := svc.CreateContainer(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success || !result.Managed {
		t.Fatalf("expected successful managed creation, got %+v", result)
	}

	spec, err := svc.GetSpec("svc1")
	if err != nil {
		t.Fatalf("expected committed spec to be loadable: %v", err)
	}
	if !spec.Managed || spec.Image != "alpine:latest" {
		t.Errorf("unexpected committed spec: %+v", spec)
	}
}

func TestCreateContainerUnmanagedSavesNoSpec(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)
	setTestConfigHome(t, tempDir)

	runner := fakePsRunner("abc123", "svc2", true)
	svc := &PodmanService{runner: runner}

	req := ContainerCreateRequest{
		Image: "alpine:latest",
		Name:  "svc2",
		PortMappings: []PortMapping{
			{HostPort: 8081, ContainerPort: 80, Protocol: "tcp"},
		},
	}

	result, err := svc.CreateContainer(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success || result.Managed {
		t.Fatalf("expected successful unmanaged creation, got %+v", result)
	}

	if _, err := svc.GetSpec("svc2"); err == nil {
		t.Errorf("expected no spec to be persisted for an unmanaged creation")
	}

	// Also verify no io.podder.managed label reached the run invocation.
	for _, call := range runner.CallsMatching("io.podder.managed") {
		t.Errorf("unmanaged creation must not apply the managed label, got call: %v", call)
	}
}

func TestCreateContainerFailedCreateLeavesNoCandidateSpec(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)
	setTestConfigHome(t, tempDir)

	runner := newFakeCommandRunner()
	runner.On("podman image", func(n string, args []string) (string, string, error) { return `[{"Id":"sha256:test-image"}]`, "", nil })
	runner.On("podman run", func(n string, args []string) (string, string, error) {
		return "", "some failure", fmt.Errorf("exit status 1")
	})
	svc := &PodmanService{runner: runner}

	req := ContainerCreateRequest{
		Image:   "alpine:latest",
		Name:    "svc3",
		Managed: true,
	}

	if _, err := svc.CreateContainer(req); err == nil {
		t.Fatalf("expected error when container creation fails")
	}

	if _, err := svc.GetSpec("svc3"); err == nil {
		t.Errorf("a failed create must not leave a committed spec claiming the workload exists")
	}

	entries, _ := os.ReadDir(getServicesDir())
	for _, e := range entries {
		t.Errorf("expected no leftover candidate spec files, found: %s", e.Name())
	}
}

func TestCreateContainerVerifyFailureRemovesContainerAndSpec(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)
	setTestConfigHome(t, tempDir)

	runner := newFakeCommandRunner()
	runner.On("podman image", func(n string, args []string) (string, string, error) { return `[{"Id":"sha256:test-image"}]`, "", nil })
	runner.On("podman run", func(n string, args []string) (string, string, error) {
		return "deadbeef\n", "", nil
	})
	// `podman ps` never reports the new container back — simulating a
	// verification failure.
	runner.On("podman ps", func(n string, args []string) (string, string, error) {
		return "[]", "", nil
	})
	var removeCalled bool
	runner.On("podman rm", func(n string, args []string) (string, string, error) {
		removeCalled = true
		return "", "", nil
	})
	svc := &PodmanService{runner: runner}

	req := ContainerCreateRequest{
		Image:   "alpine:latest",
		Name:    "svc4",
		Managed: true,
	}

	result, err := svc.CreateContainer(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || result.ManualRecoveryRequired {
		t.Fatalf("expected verified cleanup without success or manual recovery, got %+v", result)
	}
	if !removeCalled {
		t.Errorf("expected the unverified container to be removed")
	}
	if _, err := svc.GetSpec("svc4"); err == nil {
		t.Errorf("a verify failure must not leave a committed managed spec")
	}
}

func TestValidateSpecCatchesDuplicatePortsAndBadBinds(t *testing.T) {
	spec := ContainerSpec{
		Image: "alpine",
		PortMappings: []PortMapping{
			{HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
			{HostPort: 8080, ContainerPort: 81, Protocol: "tcp"},
		},
		Binds: []BindMountSpec{{HostPath: "", ContainerPath: "/x"}},
	}
	errs := ValidateSpec(spec)
	if len(errs) < 2 {
		t.Fatalf("expected at least 2 validation errors, got: %v", errs)
	}
}

func TestCreateContainerRejectsIntraRequestPortConflict(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)
	setTestConfigHome(t, tempDir)

	svc := &PodmanService{runner: newFakeCommandRunner()}
	req := ContainerCreateRequest{
		Image: "alpine",
		Name:  "clashing",
		PortMappings: []PortMapping{
			{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
			{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 81, Protocol: "tcp"},
		},
	}
	if _, err := svc.CreateContainer(req); err == nil {
		t.Fatalf("expected two mappings claiming the same host port within one request to be rejected")
	}
}

// TestCreateContainerManagedRejectsAutoAssignedHostPort proves a
// Podder-managed workload must name an explicit host port: a declarative
// managed service should not depend on an unpredictable Podman-auto-assigned
// endpoint.
func TestCreateContainerManagedRejectsAutoAssignedHostPort(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)
	setTestConfigHome(t, tempDir)

	runner := newFakeCommandRunner()
	runner.On("podman image", func(string, []string) (string, string, error) {
		return `[{"Id":"sha256:test-image"}]`, "", nil
	})
	svc := &PodmanService{runner: runner}
	req := ContainerCreateRequest{
		Image:   "alpine",
		Name:    "managed-auto-port",
		Managed: true,
		PortMappings: []PortMapping{
			{HostIP: "127.0.0.1", HostPort: 0, ContainerPort: 80, Protocol: "tcp"},
		},
	}
	if _, err := svc.CreateContainer(req); err == nil {
		t.Fatalf("expected managed creation with HostPort==0 to be rejected")
	}
}

// TestCreateContainerUnmanagedAllowsAutoAssignedHostPort proves unmanaged
// creation may still leave HostPort==0 to let Podman auto-assign a port,
// matching the frontend's "blank Host Port means auto-assign" behavior
// instead of contradicting it.
func TestCreateContainerUnmanagedAllowsAutoAssignedHostPort(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)
	setTestConfigHome(t, tempDir)

	runner := newFakeCommandRunner()
	var seenRunArgs []string
	runner.On("podman run", func(_ string, args []string) (string, string, error) {
		seenRunArgs = args
		return "deadbeef1234\n", "", nil
	})
	svc := &PodmanService{runner: runner}
	req := ContainerCreateRequest{
		Image:   "alpine",
		Name:    "unmanaged-auto-port",
		Managed: false,
		PortMappings: []PortMapping{
			{HostIP: "127.0.0.1", HostPort: 0, ContainerPort: 80, Protocol: "tcp"},
		},
	}
	result, err := svc.CreateContainer(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected unmanaged creation with HostPort==0 to succeed, message: %s", result.Message)
	}
	// The requested host address must still be preserved even though no
	// host port was named — publishing "127.0.0.1::80" (podman's own
	// auto-assign-on-this-interface syntax), never widening to all
	// interfaces just because the port was left unset.
	joined := strings.Join(seenRunArgs, " ")
	if !strings.Contains(joined, "127.0.0.1::80/tcp") {
		t.Fatalf("expected run args to preserve the explicit host address with an auto-assigned port (127.0.0.1::80/tcp), got: %v", seenRunArgs)
	}
}

func TestValidateSpecRejectsFutureSchemaVersion(t *testing.T) {
	spec := ContainerSpec{Image: "alpine", SchemaVersion: CurrentSpecSchemaVersion + 1}
	errs := ValidateSpec(spec)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "newer") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a schema-version error, got: %v", errs)
	}
}

func TestCreateContainerCleanupFailureReportsManualRecovery(t *testing.T) {
	withTestHome(t)
	withFastPolling(t)

	runner := newFakeCommandRunner()
	created := false
	runner.On("podman image", func(string, []string) (string, string, error) {
		return `[{"Id":"sha256:test-image"}]`, "", nil
	})
	runner.On("podman run", func(string, []string) (string, string, error) {
		created = true
		return "survivor-id", "", nil
	})
	runner.On("podman ps", func(string, []string) (string, string, error) {
		if !created {
			return "[]", "", nil
		}
		return `[{"Id":"survivor-id","Names":["survivor"],"Image":"alpine","ImageID":"sha256:test-image","State":"exited","Ports":[],"Labels":{"io.podder.managed":"true","io.podder.service":"survivor","io.podder.schema-version":"2"}}]`, "", nil
	})
	runner.On("podman rm", func(string, []string) (string, string, error) {
		return "", "busy", fmt.Errorf("container is busy")
	})

	svc := &PodmanService{runner: runner}
	result, err := svc.CreateContainer(ContainerCreateRequest{Image: "alpine", Name: "survivor", Managed: true})
	if err != nil {
		t.Fatalf("expected structured recovery result, got top-level error: %v", err)
	}
	if result.Success || !result.ManualRecoveryRequired || result.ContainerID != "survivor-id" || result.ContainerName != "survivor" {
		t.Fatalf("cleanup failure did not expose surviving identity and recovery state: %+v", result)
	}
	if result.CandidateSpecPath == "" {
		t.Fatalf("valid candidate spec must be retained for manual recovery")
	}
	if _, err := os.Stat(result.CandidateSpecPath); err != nil {
		t.Fatalf("retained candidate spec is not accessible: %v", err)
	}
}

// TestCreateContainerManagedVerificationRetriesBeforeGivingUp covers an
// adversarial-review finding: verifyCreatedManagedContainer was checked with
// a single immediate ListContainers snapshot instead of the same pollUntil
// retry every equivalent post-create check elsewhere in this codebase uses.
// A container that only settles into `podman ps` output on a later poll
// must still verify successfully, not be torn down as if it never existed.
func TestCreateContainerManagedVerificationRetriesBeforeGivingUp(t *testing.T) {
	withTestHome(t)
	withFastPolling(t)

	containerID := "abc123def456"
	name := "svc-race"
	runner := newFakeCommandRunner()
	runner.On("podman image", func(string, []string) (string, string, error) {
		return `[{"Id":"sha256:test-image"}]`, "", nil
	})
	created := false
	runner.On("podman run", func(string, []string) (string, string, error) {
		created = true
		return containerID + "\n", "", nil
	})
	psCallsAfterCreate := 0
	runner.On("podman ps", func(string, []string) (string, string, error) {
		if !created {
			return "[]", "", nil
		}
		psCallsAfterCreate++
		if psCallsAfterCreate < 2 {
			// Simulate the container not yet settled on the very first
			// snapshot immediately after `podman run` returns.
			return "[]", "", nil
		}
		return `[{"Id":"` + containerID + `","Names":["` + name + `"],"Image":"alpine","ImageID":"sha256:test-image","State":"running","Ports":[],"Labels":{"io.podder.managed":"true","io.podder.service":"` + name + `","io.podder.schema-version":"2"}}]`, "", nil
	})
	runner.On("podman rm", func(string, []string) (string, string, error) {
		t.Fatalf("a delayed-but-eventually-correct verification must not be treated as a failure and torn down")
		return "", "", nil
	})

	svc := &PodmanService{runner: runner}
	result, err := svc.CreateContainer(ContainerCreateRequest{Image: "alpine", Name: name, Managed: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected verification to succeed once the container settles on a later poll, got: %+v", result)
	}
	if psCallsAfterCreate < 2 {
		t.Fatalf("expected verification to retry via pollUntil, got only %d podman ps call(s) after create", psCallsAfterCreate)
	}
}

// TestCreateContainerManagedAutoPullsUnresolvedImage covers an adversarial-
// review finding: resolveImageID uses `podman image inspect`, which never
// pulls, so a managed create from an image not yet present locally used to
// fail outright even though plain `podman run` would have auto-pulled it.
func TestCreateContainerManagedAutoPullsUnresolvedImage(t *testing.T) {
	withTestHome(t)
	withFastPolling(t)

	containerID := "deadbeef0001"
	name := "svc-pull"
	runner := newFakeCommandRunner()
	imageInspectCalls := 0
	pulled := false
	runner.On("podman image", func(string, []string) (string, string, error) {
		imageInspectCalls++
		if !pulled {
			return "", "no such image", fmt.Errorf("no such image")
		}
		return `[{"Id":"sha256:test-image"}]`, "", nil
	})
	runner.On("podman pull", func(string, []string) (string, string, error) {
		pulled = true
		return "", "", nil
	})
	created := false
	runner.On("podman run", func(string, []string) (string, string, error) {
		created = true
		return containerID + "\n", "", nil
	})
	runner.On("podman ps", func(string, []string) (string, string, error) {
		if !created {
			return "[]", "", nil
		}
		return `[{"Id":"` + containerID + `","Names":["` + name + `"],"Image":"example.com/fresh:latest","ImageID":"sha256:test-image","State":"running","Ports":[],"Labels":{"io.podder.managed":"true","io.podder.service":"` + name + `","io.podder.schema-version":"2"}}]`, "", nil
	})

	svc := &PodmanService{runner: runner}
	result, err := svc.CreateContainer(ContainerCreateRequest{Image: "example.com/fresh:latest", Name: name, Managed: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected managed creation to succeed by auto-pulling the not-yet-local image, got: %+v", result)
	}
	if !pulled {
		t.Fatalf("expected PullImage to be attempted after the initial inspect failure")
	}
	if imageInspectCalls < 2 {
		t.Fatalf("expected resolveImageID to be retried after the pull, got %d inspect call(s)", imageInspectCalls)
	}
}

// TestRemoveImageDoesNotForce proves normal image deletion never escalates
// to podman's --force / -f behavior, which can silently delete containers
// that depend on the image before deleting the image itself. The GUI
// presents this as ordinary image removal, so the backend must match that
// disclosed intent exactly.
func TestRemoveImageDoesNotForce(t *testing.T) {
	runner := newFakeCommandRunner()
	var seenArgs []string
	runner.On("podman rmi", func(_ string, args []string) (string, string, error) {
		seenArgs = args
		return "", "", nil
	})

	svc := &PodmanService{runner: runner}
	if err := svc.RemoveImage("sha256:abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, a := range seenArgs {
		if a == "-f" || a == "--force" {
			t.Fatalf("RemoveImage must never pass -f/--force to podman rmi, got args: %v", seenArgs)
		}
	}
	if want := []string{"rmi", "sha256:abc123"}; len(seenArgs) != len(want) || seenArgs[0] != want[0] || seenArgs[1] != want[1] {
		t.Fatalf("expected podman rmi to be called with exactly [rmi, sha256:abc123], got: %v", seenArgs)
	}
}

// TestRemoveImageInUseReturnsRefusalNotEscalation proves that when Podman
// refuses to remove an image because a container depends on it, RemoveImage
// surfaces that refusal to the caller instead of silently deleting the
// dependent container to force the removal through.
func TestRemoveImageInUseReturnsRefusalNotEscalation(t *testing.T) {
	runner := newFakeCommandRunner()
	runner.On("podman rmi", func(_ string, args []string) (string, string, error) {
		for _, a := range args {
			if a == "-f" || a == "--force" {
				t.Fatalf("must not force even when the image is in use, got args: %v", args)
			}
		}
		return errOut("Error: image is in use by a container: image used by 1234 (consider using --force)")
	})
	runner.On("podman rm", func(_ string, args []string) (string, string, error) {
		t.Fatalf("RemoveImage must never remove containers to satisfy an image deletion, got: podman rm %v", args)
		return "", "", nil
	})

	svc := &PodmanService{runner: runner}
	err := svc.RemoveImage("sha256:abc123")
	if err == nil {
		t.Fatalf("expected an error surfacing Podman's refusal, got nil")
	}
	if !strings.Contains(err.Error(), "image is in use") {
		t.Fatalf("expected the refusal reason to be surfaced to the caller, got: %v", err)
	}
	if calls := runner.CallsMatching("rm"); len(calls) != 1 {
		t.Fatalf("expected exactly one rm-family call (the refused rmi), got: %v", calls)
	}
}

// TestResolveComposeSelectionDirectory proves that selecting a directory
// leaves composeFile empty, deferring to the provider's own default
// filename discovery — unchanged from the pre-fix behavior.
func TestResolveComposeSelectionDirectory(t *testing.T) {
	tmp := t.TempDir()
	dir, composeFile, err := resolveComposeSelection(tmp, os.Stat)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != tmp {
		t.Errorf("expected dir %q, got %q", tmp, dir)
	}
	if composeFile != "" {
		t.Errorf("expected no explicit compose file when a directory is selected, got %q", composeFile)
	}
}

// TestResolveComposeSelectionExplicitFile proves that selecting a specific
// file — including a non-default filename like compose-gpu.yml in a
// directory that also holds compose.yml — preserves that exact file instead
// of reducing the selection to its parent directory and letting default
// filename discovery silently pick a different file.
func TestResolveComposeSelectionExplicitFile(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"compose.yml", "compose-test.yml", "compose-gpu.yml"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("services: {}\n"), 0o644); err != nil {
			t.Fatalf("failed to write fixture %s: %v", name, err)
		}
	}

	cases := []string{"compose.yml", "compose-test.yml", "compose-gpu.yml"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			selected := filepath.Join(tmp, name)
			dir, composeFile, err := resolveComposeSelection(selected, os.Stat)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dir != tmp {
				t.Errorf("expected working dir %q, got %q", tmp, dir)
			}
			if composeFile != selected {
				t.Errorf("expected the exact selected file %q preserved, got %q", selected, composeFile)
			}
		})
	}
}

// TestSelectedComposeFileProducesExplicitProviderArgv is the end-to-end
// regression: given a selection of a non-default compose filename,
// resolveComposeSelection + composeProvider.BuildArgs (the single place
// Compose argv is constructed) must produce a provider invocation that
// names that exact file, so the provider cannot fall back to its own
// default filename discovery.
func TestSelectedComposeFileProducesExplicitProviderArgv(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}
	gpuFile := filepath.Join(tmp, "compose-gpu.yml")
	if err := os.WriteFile(gpuFile, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	dir, composeFile, err := resolveComposeSelection(gpuFile, os.Stat)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != tmp || composeFile != gpuFile {
		t.Fatalf("unexpected resolution: dir=%q composeFile=%q", dir, composeFile)
	}

	provider := &composeProvider{path: "/usr/bin/podman-compose"}
	args := provider.BuildArgs(composeFile, "up", []string{"-d"}, "")
	want := []string{"-f", gpuFile, "up", "-d"}
	if len(args) != len(want) {
		t.Fatalf("BuildArgs() = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("BuildArgs() = %v, want %v", args, want)
		}
	}
	for _, a := range args {
		if a == "compose.yml" || a == filepath.Join(tmp, "compose.yml") {
			t.Fatalf("provider argv must not fall back to the default compose.yml when %q was explicitly selected, got: %v", gpuFile, args)
		}
	}
}

// --- Container removal: safe user-facing RemoveContainer vs. graceful
// StopAndRemoveContainer vs. internal-only forceRemoveContainer. ---

func TestRemoveContainer_RefusesRunningContainer(t *testing.T) {
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(string, []string) (string, string, error) {
		return `[{"Id":"abc123","Names":["web"],"State":"running","Ports":[]}]`, "", nil
	})
	runner.On("podman rm", func(_ string, args []string) (string, string, error) {
		t.Fatalf("RemoveContainer must not attempt removal of a running container, got: podman rm %v", args)
		return "", "", nil
	})

	svc := &PodmanService{runner: runner}
	err := svc.RemoveContainer("web")
	if err == nil {
		t.Fatalf("expected RemoveContainer to refuse a running container")
	}
	if !strings.Contains(err.Error(), "running") {
		t.Errorf("expected the refusal reason to mention the container is running, got: %v", err)
	}
}

func TestRemoveContainer_SucceedsWithoutForceForStoppedContainer(t *testing.T) {
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(string, []string) (string, string, error) {
		return `[{"Id":"abc123","Names":["web"],"State":"exited","Ports":[]}]`, "", nil
	})
	var seenArgs []string
	runner.On("podman rm", func(_ string, args []string) (string, string, error) {
		seenArgs = args
		return "", "", nil
	})

	svc := &PodmanService{runner: runner}
	if err := svc.RemoveContainer("web"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range seenArgs {
		if a == "-f" || a == "--force" {
			t.Fatalf("RemoveContainer must never pass -f/--force, got args: %v", seenArgs)
		}
	}
	if want := []string{"rm", "web"}; len(seenArgs) != len(want) || seenArgs[0] != want[0] || seenArgs[1] != want[1] {
		t.Fatalf("expected podman rm called with exactly %v, got: %v", want, seenArgs)
	}
}

func TestStopAndRemoveContainer_StopsThenRemovesWithoutForce(t *testing.T) {
	runner := newFakeCommandRunner()
	var calls []string
	runner.On("podman stop", func(_ string, args []string) (string, string, error) {
		calls = append(calls, "stop")
		return "", "", nil
	})
	runner.On("podman rm", func(_ string, args []string) (string, string, error) {
		calls = append(calls, "rm")
		for _, a := range args {
			if a == "-f" || a == "--force" {
				t.Fatalf("StopAndRemoveContainer must not need -f after a real stop, got args: %v", args)
			}
		}
		return "", "", nil
	})

	svc := &PodmanService{runner: runner}
	if err := svc.StopAndRemoveContainer("web"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 || calls[0] != "stop" || calls[1] != "rm" {
		t.Fatalf("expected stop then rm, got: %v", calls)
	}
}

func TestStopAndRemoveContainer_StopFailurePreventsRemoval(t *testing.T) {
	runner := newFakeCommandRunner()
	runner.On("podman stop", func(string, []string) (string, string, error) {
		return errOut("simulated stop failure")
	})
	runner.On("podman rm", func(_ string, args []string) (string, string, error) {
		t.Fatalf("must not attempt removal after a failed stop, got: podman rm %v", args)
		return "", "", nil
	})

	svc := &PodmanService{runner: runner}
	if err := svc.StopAndRemoveContainer("web"); err == nil {
		t.Fatalf("expected an error when stop fails")
	}
}

// TestForceRemoveContainerStillForces proves internal transaction cleanup
// (rollback, candidate cleanup, recovery) keeps its unconditional force
// semantic even though the public RemoveContainer no longer forces —
// user-facing removal and transaction-internal recovery are deliberately
// different operations.
func TestForceRemoveContainerStillForces(t *testing.T) {
	runner := newFakeCommandRunner()
	var seenArgs []string
	runner.On("podman rm", func(_ string, args []string) (string, string, error) {
		seenArgs = args
		return "", "", nil
	})

	svc := &PodmanService{runner: runner}
	if err := svc.forceRemoveContainer("candidate-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"rm", "-f", "candidate-123"}; len(seenArgs) != len(want) || seenArgs[0] != want[0] || seenArgs[1] != want[1] || seenArgs[2] != want[2] {
		t.Fatalf("expected forceRemoveContainer to call exactly %v, got: %v", want, seenArgs)
	}
}
