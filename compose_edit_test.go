package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseComposePorts(t *testing.T) {
	yamlContent := `
services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
      - "127.0.0.1:443:443/tcp"
      - "5353:5353/udp"
  redis:
    image: redis:alpine
    ports:
      - "6379:6379"
`

	ports, err := ParseComposePorts(yamlContent, "web")
	if err != nil {
		t.Fatalf("unexpected error parsing compose ports: %v", err)
	}

	if len(ports) != 3 {
		t.Fatalf("expected 3 ports for 'web', got %d", len(ports))
	}
	if ports[0].HostPort != 8080 || ports[0].ContainerPort != 80 {
		t.Errorf("unexpected port 0: %+v", ports[0])
	}
	if ports[1].HostIP != "127.0.0.1" || ports[1].HostPort != 443 {
		t.Errorf("unexpected port 1: %+v", ports[1])
	}
	if ports[2].Protocol != "udp" || ports[2].HostPort != 5353 {
		t.Errorf("unexpected port 2: %+v", ports[2])
	}

	// redis ports
	redisPorts, err := ParseComposePorts(yamlContent, "redis")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(redisPorts) != 1 || redisPorts[0].HostPort != 6379 {
		t.Errorf("unexpected redis ports: %+v", redisPorts)
	}
}

func TestUpdateComposePorts(t *testing.T) {
	yamlContent := `services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
    restart: always
  redis:
    image: redis:alpine
`

	newPorts := []PortMapping{
		{
			HostIP:        "127.0.0.1",
			HostPort:      3000,
			ContainerPort: 80,
			Protocol:      "tcp",
		},
		{
			HostIP:        "127.0.0.1",
			HostPort:      8443,
			ContainerPort: 443,
			Protocol:      "tcp",
		},
	}

	updated, err := UpdateComposePorts(yamlContent, "web", newPorts)
	if err != nil {
		t.Fatalf("failed to update compose ports: %v", err)
	}

	if strings.Contains(updated, "8080:80") {
		t.Errorf("expected old port 8080 to be replaced")
	}
	if !strings.Contains(updated, "127.0.0.1:3000:80/tcp") {
		t.Errorf("expected port 3000 in updated YAML: %s", updated)
	}
	if !strings.Contains(updated, "127.0.0.1:8443:443/tcp") {
		t.Errorf("expected port 8443 in updated YAML: %s", updated)
	}
	if !strings.Contains(updated, "restart: always") {
		t.Errorf("expected restart setting to be preserved")
	}
	if !strings.Contains(updated, "redis:") {
		t.Errorf("expected redis service to be preserved")
	}
}

func TestFindComposeFileBlocksMultipleConfigFiles(t *testing.T) {
	_, err := FindComposeFile("/some/dir", "compose.yaml,compose.override.yaml")
	if err == nil {
		t.Fatalf("expected multiple compose files to be blocked")
	}
	if !errors.Is(err, ErrMultipleComposeFiles) {
		t.Errorf("expected ErrMultipleComposeFiles, got: %v", err)
	}
}

// --- v1.4 hardening: Compose provenance is a discovery hint, not
// filesystem authorization (item 5) ---
//
// com.docker.compose.project.working_dir / .config_files are external,
// container-supplied label metadata. A malicious or malformed container
// must not be able to make Podder read an arbitrary accessible file merely
// by claiming its path is "the" compose file.

func TestFindComposeFile_RejectsAbsolutePathOutsideWorkingDir(t *testing.T) {
	workingDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "not-actually-compose.yaml")
	if err := os.WriteFile(outsideFile, []byte("version: '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if path, err := FindComposeFile(workingDir, outsideFile); err == nil {
		t.Fatalf("expected an absolute config-file path outside the working dir to be refused, got path=%q", path)
	} else if !errors.Is(err, ErrComposeFileOutsideWorkingDir) {
		t.Errorf("expected ErrComposeFileOutsideWorkingDir, got: %v", err)
	}
}

func TestFindComposeFile_RejectsTraversalInConfigFilesValue(t *testing.T) {
	workingDir := t.TempDir()
	// A sibling file placed exactly where "../escaped.yaml" would resolve.
	sibling := filepath.Join(filepath.Dir(workingDir), "escaped.yaml")
	if err := os.WriteFile(sibling, []byte("version: '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(sibling)

	malicious := []string{
		"../../some-file",
		"../escaped.yaml",
		"../foo.container",
		"foo/../../bar",
	}
	for _, cfg := range malicious {
		if path, err := FindComposeFile(workingDir, cfg); err == nil {
			t.Errorf("expected FindComposeFile(workingDir, %q) to be refused as a traversal attempt, got path=%q", cfg, path)
		}
	}
}

func TestFindComposeFile_RejectsAbsolutePathWithNoWorkingDir(t *testing.T) {
	// No working directory at all to validate an absolute claim against:
	// this must fail closed, never read the absolute path anyway.
	if path, err := FindComposeFile("", "/etc/passwd"); err == nil {
		t.Fatalf("expected refusal when there is no working directory to validate against, got path=%q", path)
	}
}

func TestFindComposeFile_AcceptsRecognizedFileWithinWorkingDir(t *testing.T) {
	workingDir := t.TempDir()
	composePath := filepath.Join(workingDir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte("version: '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := FindComposeFile(workingDir, "")
	if err != nil {
		t.Fatalf("expected a normal recognized compose file under the working dir to resolve: %v", err)
	}
	if path != composePath {
		t.Errorf("expected %q, got %q", composePath, path)
	}
}

func TestFindComposeFile_AcceptsExplicitRelativeConfigFileWithinWorkingDir(t *testing.T) {
	workingDir := t.TempDir()
	composePath := filepath.Join(workingDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("version: '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := FindComposeFile(workingDir, "docker-compose.yml")
	if err != nil {
		t.Fatalf("expected an explicit relative config-file path within the working dir to resolve: %v", err)
	}
	if path != composePath {
		t.Errorf("expected %q, got %q", composePath, path)
	}
}

func TestFindComposeFile_AcceptsAbsolutePathWithinWorkingDir(t *testing.T) {
	workingDir := t.TempDir()
	composePath := filepath.Join(workingDir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte("version: '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An absolute path that legitimately resolves inside the reported
	// working directory is fine -- containment is what matters, not
	// whether the label happened to spell it as absolute or relative.
	path, err := FindComposeFile(workingDir, composePath)
	if err != nil {
		t.Fatalf("expected an absolute path within the working dir to resolve: %v", err)
	}
	if path != composePath {
		t.Errorf("expected %q, got %q", composePath, path)
	}
}

// TestInspectCompose_MaliciousWorkingDirCannotReadArbitraryFile is an
// end-to-end regression proving InspectCompose (the actual bound method
// exposed to the frontend) never returns the content of a file outside a
// container's reported working directory, even when that working directory
// itself is entirely attacker-controlled label content.
func TestInspectCompose_MaliciousWorkingDirCannotReadArbitraryFile(t *testing.T) {
	secretDir := t.TempDir()
	secretFile := filepath.Join(secretDir, "secret.yaml")
	if err := os.WriteFile(secretFile, []byte("THIS-MUST-NEVER-BE-READ"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := newFakeCommandRunner()
	psJSON := `[{"Id":"cafebabe00000000000000000000000000000001","Names":["evil"],"Image":"alpine","ImageID":"sha256:x","State":"running","Ports":[],` +
		`"Labels":{"com.docker.compose.project":"proj","com.docker.compose.service":"svc",` +
		`"com.docker.compose.project.working_dir":"/tmp/does-not-exist-as-a-real-project-dir",` +
		`"com.docker.compose.project.config_files":"` + secretFile + `"}}]`
	runner.On("podman ps", func(n string, args []string) (string, string, error) { return psJSON, "", nil })
	svc := &PodmanService{runner: runner}

	details, err := svc.InspectCompose("cafebabe00000000000000000000000000000001")
	if err == nil {
		t.Fatalf("expected InspectCompose to refuse an out-of-working-dir absolute path, got: %+v", details)
	}
	if !errors.Is(err, ErrComposeFileOutsideWorkingDir) {
		t.Errorf("expected ErrComposeFileOutsideWorkingDir, got: %v", err)
	}
}

func TestUpdateComposePortsRefusesUnsupportedLongForm(t *testing.T) {
	yamlContent := `services:
  web:
    image: nginx:alpine
    ports:
      - target: 80
        published: 8080
        protocol: tcp
        name: web-http
        mode: host
`
	_, err := UpdateComposePorts(yamlContent, "web", []PortMapping{{HostPort: 3000, ContainerPort: 80, Protocol: "tcp"}})
	if err == nil {
		t.Fatalf("expected update to refuse rewriting long-form ports with unsupported attributes")
	}
	if !strings.Contains(err.Error(), "name") && !strings.Contains(err.Error(), "mode") {
		t.Errorf("expected error to name the unsupported attribute(s), got: %v", err)
	}
}

func TestUpdateComposePortsAllowsSupportedLongForm(t *testing.T) {
	yamlContent := `services:
  web:
    image: nginx:alpine
    ports:
      - target: 80
        published: 8080
        host_ip: 127.0.0.1
        protocol: tcp
`
	updated, err := UpdateComposePorts(yamlContent, "web", []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("expected supported long-form attributes to allow rewriting: %v", err)
	}
	if !strings.Contains(updated, "127.0.0.1:9090:80/tcp") {
		t.Errorf("expected new short-form port entry, got: %s", updated)
	}
}

func TestComposeYAMLRoundTripPreservesCommentsAnchorsAndExtensions(t *testing.T) {
	yamlContent := `# top-level comment
x-common: &defaults
  restart: always

services:
  web:
    <<: *defaults
    image: nginx:alpine # inline comment
    ports:
      - "8080:80"
    environment:
      - FOO=${BAR:-default}
    profiles: ["dev"]
`
	updated, err := UpdateComposePorts(yamlContent, "web", []PortMapping{{HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{"x-common", "&defaults", "<<: *defaults", "profiles", `${BAR:-default}`} {
		if !strings.Contains(updated, want) {
			t.Errorf("expected round trip to preserve %q, got:\n%s", want, updated)
		}
	}
}

// composeApplySim is a minimal CommandRunner that simulates `podman ps`
// (reflecting a single Compose-labeled container) and a compose provider's
// `up` invocation (applying the compose file's current ports for the
// target service onto that simulated container), so MutateComposePorts can
// be exercised end-to-end without a real compose install.
type composeApplySim struct {
	mu        sync.Mutex
	id        string
	project   string
	service   string
	ports     []PortMapping
	failApply bool
}

func (c *composeApplySim) Run(name string, args ...string) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if name == "podman" && len(args) > 0 && args[0] == "ps" {
		var portsJSON strings.Builder
		portsJSON.WriteString("[")
		for i, p := range c.ports {
			if i > 0 {
				portsJSON.WriteString(",")
			}
			portsJSON.WriteString(fmt.Sprintf(`{"host_ip":"%s","container_port":%d,"host_port":%d,"protocol":"%s"}`, p.HostIP, p.ContainerPort, p.HostPort, p.Protocol))
		}
		portsJSON.WriteString("]")
		labels := fmt.Sprintf(`{"com.docker.compose.project":"%s","com.docker.compose.service":"%s"}`, c.project, c.service)
		return fmt.Sprintf(`[{"Id":"%s","Names":["%s_%s_1"],"Image":"nginx:alpine","State":"running","Ports":%s,"Labels":%s}]`, c.id, c.project, c.service, portsJSON.String(), labels), "", nil
	}

	if name == "podman-compose" {
		if c.failApply {
			return "", "simulated compose failure", fmt.Errorf("simulated compose failure")
		}
		var file string
		for i, a := range args {
			if a == "-f" && i+1 < len(args) {
				file = args[i+1]
			}
		}
		if data, err := os.ReadFile(file); err == nil {
			if ports, perr := ParseComposePorts(string(data), c.service); perr == nil {
				c.ports = ports
			}
		}
		return "", "", nil
	}

	return "", "", nil
}

func fakeLookPathOnly(names ...string) lookPathFunc {
	allowed := map[string]bool{}
	for _, n := range names {
		allowed[n] = true
	}
	return func(file string) (string, error) {
		if allowed[file] {
			// Return the bare name (not an absolute path) so the fake
			// CommandRunner, which matches on the literal name it's asked
			// to run, recognizes it.
			return file, nil
		}
		return "", fmt.Errorf("not found: %s", file)
	}
}

func TestMutateComposePorts_SuccessfulServiceScopedApply(t *testing.T) {
	tempDir := t.TempDir()
	composeFile := filepath.Join(tempDir, "compose.yaml")
	content := "services:\n  web:\n    image: nginx:alpine\n    ports:\n      - \"8080:80\"\n"
	if err := os.WriteFile(composeFile, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	sim := &composeApplySim{id: "abcproj1", project: "myproj", service: "web", ports: []PortMapping{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}}
	simWithLabels := &composeApplySimWithConfig{composeApplySim: sim, workingDir: tempDir, configFile: composeFile}
	svc := &PodmanService{runner: simWithLabels, lookPath: fakeLookPathOnly("podman-compose")}

	result, err := svc.MutateComposePorts("abcproj1", []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !result.RequiresExternal {
		t.Fatalf("expected Compose mutation to be safely disabled with manual guidance, got: %+v", result)
	}

	// The generated guidance snippet must use the container's ACTUAL
	// Compose service ("web"), not a generic placeholder key — a snippet
	// keyed "service:" could be pasted under the wrong service entirely.
	if !strings.Contains(result.ComposeSnippet, "web:") {
		t.Errorf("expected generated snippet to use the real service name 'web', got: %s", result.ComposeSnippet)
	}
	if strings.Contains(result.ComposeSnippet, "\n  service:") {
		t.Errorf("did not expect the generic placeholder service key in the snippet, got: %s", result.ComposeSnippet)
	}

	updated, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatalf("failed to read updated compose file: %v", err)
	}
	if string(updated) != content {
		t.Errorf("read-only Compose mutation must not alter the file, got: %s", updated)
	}
}

func TestMutateComposePorts_FailedApplyRollsBackFile(t *testing.T) {
	tempDir := t.TempDir()
	composeFile := filepath.Join(tempDir, "compose.yaml")
	content := "services:\n  web:\n    image: nginx:alpine\n    ports:\n      - \"8080:80\"\n"
	if err := os.WriteFile(composeFile, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	sim := &composeApplySim{id: "abcproj2", project: "myproj", service: "web", ports: []PortMapping{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}, failApply: true}
	simWithLabels := &composeApplySimWithConfig{composeApplySim: sim, workingDir: tempDir, configFile: composeFile}
	svc := &PodmanService{runner: simWithLabels, lookPath: fakeLookPathOnly("podman-compose")}

	result, err := svc.MutateComposePorts("abcproj2", []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failed compose apply to fail the transaction")
	}
	if !result.RequiresExternal || result.RolledBack {
		t.Fatalf("expected preflight refusal without rollback attempt, got: %+v", result)
	}

	restored, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatalf("failed to read compose file: %v", err)
	}
	if !strings.Contains(string(restored), "8080:80") || strings.Contains(string(restored), "9090") {
		t.Errorf("expected original compose file content restored, got: %s", restored)
	}
}

// TestMutateComposePorts_UnknownServiceIdentityGeneratesNoSnippet proves
// that when the container's Compose service identity cannot be confidently
// determined (e.g. the container is not found at all), Podder does not
// invent a generic placeholder service name for the guidance snippet — it
// states that the identity could not be determined instead.
func TestMutateComposePorts_UnknownServiceIdentityGeneratesNoSnippet(t *testing.T) {
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(string, []string) (string, string, error) { return "[]", "", nil })
	svc := &PodmanService{runner: runner}

	result, err := svc.MutateComposePorts("does-not-exist", []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ComposeSnippet != "" {
		t.Errorf("expected no snippet when the Compose service identity could not be determined, got: %s", result.ComposeSnippet)
	}
	if !strings.Contains(result.Guidance, "could not be safely determined") {
		t.Errorf("expected guidance to state the identity could not be determined, got: %s", result.Guidance)
	}
}

// composeApplySimWithConfig augments composeApplySim's `podman ps` labels
// with working_dir/config_files so InspectCompose's FindComposeFile can
// locate the fixture file.
type composeApplySimWithConfig struct {
	*composeApplySim
	workingDir string
	configFile string
}

func (c *composeApplySimWithConfig) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && args[0] == "ps" {
		c.mu.Lock()
		var portsJSON strings.Builder
		portsJSON.WriteString("[")
		for i, p := range c.ports {
			if i > 0 {
				portsJSON.WriteString(",")
			}
			portsJSON.WriteString(fmt.Sprintf(`{"host_ip":"%s","container_port":%d,"host_port":%d,"protocol":"%s"}`, p.HostIP, p.ContainerPort, p.HostPort, p.Protocol))
		}
		portsJSON.WriteString("]")
		labels := fmt.Sprintf(`{"com.docker.compose.project":"%s","com.docker.compose.service":"%s","com.docker.compose.project.working_dir":"%s","com.docker.compose.project.config_files":"%s"}`,
			c.project, c.service, c.workingDir, c.configFile)
		out := fmt.Sprintf(`[{"Id":"%s","Names":["%s_%s_1"],"Image":"nginx:alpine","State":"running","Ports":%s,"Labels":%s}]`, c.id, c.project, c.service, portsJSON.String(), labels)
		c.mu.Unlock()
		return out, "", nil
	}
	return c.composeApplySim.Run(name, args...)
}

func TestWriteFileAtomicPreservingModePreservesPermissions(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "compose.yaml")
	if err := os.WriteFile(path, []byte("services: {}\n"), 0o640); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	if err := writeFileAtomicPreservingMode(path, []byte("services: {}\nupdated: true\n"), 0o640); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("expected preserved 0640 permissions, got %o", perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if !strings.Contains(string(data), "updated: true") {
		t.Errorf("expected new content to be written, got: %s", data)
	}
}

func TestComposePortsFailClosedOnUnrepresentableEntries(t *testing.T) {
	cases := map[string]string{
		"interpolation": "services:\n  web:\n    ports:\n      - \"${APP_PORT:-3000}:3000\"\n",
		"alias":         "x-ports: &shared\n  - \"8080:80\"\nservices:\n  web:\n    ports: *shared\n",
		"malformed":     "services:\n  web:\n    ports:\n      - \"not-a-port\"\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseComposePorts(content, "web"); err == nil {
				t.Fatalf("expected %s entry to block automatic parsing", name)
			}
			if _, err := UpdateComposePorts(content, "web", []PortMapping{{HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}}); err == nil {
				t.Fatalf("expected %s entry to block automatic rewrite", name)
			}
		})
	}
}

func TestComposePortRangeIsRepresentedExactly(t *testing.T) {
	content := "services:\n  web:\n    ports:\n      - \"8000-8002:80-82/tcp\"\n"
	ports, err := ParseComposePorts(content, "web")
	if err != nil {
		t.Fatalf("range should be supported end-to-end: %v", err)
	}
	if len(ports) != 1 || ports[0].RangeSize != 3 {
		t.Fatalf("range was collapsed or skipped: %+v", ports)
	}
}

func TestComposeVerificationRequiresExactProjectAndService(t *testing.T) {
	containers := []Container{
		{Id: "a", Provenance: WorkloadProvenance{Type: "compose", Project: "project-a", Service: "web"}},
		{Id: "b", Provenance: WorkloadProvenance{Type: "compose", Project: "project-b", Service: "web"}},
	}
	if got := findComposeServiceContainer(containers, "project-b", "web"); got == nil || got.Id != "b" {
		t.Fatalf("project-a/web must not satisfy project-b/web verification: %+v", got)
	}
	if got := findComposeServiceContainer(containers, "", "web"); got != nil {
		t.Fatalf("empty project identity must not accept an arbitrary web service: %+v", got)
	}
}
