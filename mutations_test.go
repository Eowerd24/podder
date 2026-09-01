package main

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGenerateComposeSnippet(t *testing.T) {
	ports := []PortMapping{
		{
			HostIP:        "127.0.0.1",
			HostPort:      8080,
			ContainerPort: 80,
			Protocol:      "tcp",
		},
		{
			HostIP:        "0.0.0.0",
			HostPort:      5353,
			ContainerPort: 5353,
			Protocol:      "udp",
		},
	}

	snippet, err := GenerateComposeSnippet("web-service", ports)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(snippet, "web-service:") {
		t.Errorf("expected service name in snippet, got: %s", snippet)
	}
	if !strings.Contains(snippet, `"127.0.0.1:8080:80/tcp"`) {
		t.Errorf("expected loopback port in snippet, got: %s", snippet)
	}
	// An explicit "0.0.0.0" host bind must be preserved verbatim in
	// generated guidance, not silently canonicalized into an omitted bind.
	if !strings.Contains(snippet, `"0.0.0.0:5353:5353/udp"`) {
		t.Errorf("expected explicit wildcard port preserved in snippet, got: %s", snippet)
	}
}

// --- v1.4 hardening round 2: Compose service name must be validated before
// being emitted into generated YAML (finding 4). The identity commonly
// originates from external, attacker-influenceable Compose provenance
// labels and must never be able to inject additional YAML structure into
// guidance the operator is expected to paste verbatim. ---

func TestGenerateComposeSnippet_ValidServiceNamesAccepted(t *testing.T) {
	ports := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	valid := []string{"flowise", "foo-bar", "foo_bar", "foo.bar", "Web123", "a"}
	for _, name := range valid {
		snippet, err := GenerateComposeSnippet(name, ports)
		if err != nil {
			t.Errorf("expected valid service name %q to be accepted, got error: %v", name, err)
			continue
		}
		if !strings.Contains(snippet, name+":") {
			t.Errorf("expected snippet to contain %q as a mapping key, got: %s", name+":", snippet)
		}
	}
}

func TestGenerateComposeSnippet_HostileServiceNamesRejected(t *testing.T) {
	ports := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	hostile := []string{
		"",
		"foo:\n  evil: true",
		"foo\nother:",
		"foo: bar",
		"foo\r\nbar:",
		"foo bar",   // space
		"foo:bar",   // colon
		"../escape", // path-like, also has '/'
		"foo\x00bar",
	}
	for _, name := range hostile {
		snippet, err := GenerateComposeSnippet(name, ports)
		if err == nil {
			t.Errorf("expected hostile service name %q to be rejected, got snippet: %q", name, snippet)
		}
		if snippet != "" {
			t.Errorf("expected no snippet content on rejection for %q, got: %q", name, snippet)
		}
	}
}

// TestGenerateComposeSnippet_HostileNameNeverInjectsYAMLStructure is a
// stronger end-to-end check: even if a hostile name were somehow accepted,
// the resulting text must never parse as YAML with more top-level keys
// under "services" than the one intended service. Since hostile names are
// rejected outright, this instead proves the rejected snippet is genuinely
// empty (no partial/injected content leaks out).
func TestGenerateComposeSnippet_HostileNameNeverInjectsYAMLStructure(t *testing.T) {
	ports := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	snippet, err := GenerateComposeSnippet("foo:\n  evil: true\nservices:\n  another", ports)
	if err == nil {
		t.Fatalf("expected a hostile multi-line service name to be rejected")
	}
	if strings.Contains(snippet, "evil") || strings.Contains(snippet, "another") {
		t.Fatalf("expected no injected structure to leak into snippet output, got: %q", snippet)
	}
}

func TestPreviewComposeSnippet_RejectsHostileServiceName(t *testing.T) {
	svc := &PodmanService{}
	_, err := svc.PreviewComposeSnippet("foo:\n  evil: true", []PortMapping{{ContainerPort: 80, Protocol: "tcp"}})
	if err == nil {
		t.Errorf("expected PreviewComposeSnippet to reject a hostile service name")
	}
}

func TestGenerateQuadletSnippet(t *testing.T) {
	ports := []PortMapping{
		{
			HostIP:        "127.0.0.1",
			HostPort:      3000,
			ContainerPort: 3000,
			Protocol:      "tcp",
		},
	}

	snippet := GenerateQuadletSnippet(ports)
	if !strings.Contains(snippet, "[Container]") {
		t.Errorf("expected [Container] section, got: %s", snippet)
	}
	if !strings.Contains(snippet, "PublishPort=127.0.0.1:3000:3000/tcp") {
		t.Errorf("expected PublishPort line, got: %s", snippet)
	}
}

// --- v1.4 hardening: RangeSize must survive into generated snippets ---
// (item 2 requires Compose/Quadlet snippet generation to retain ranges; the
// "one formatter rule" requires this to go through FormatPublishSpec, never
// a hand-built JS/Go string, so a range-losing regression here would be a
// real correctness bug, not just a display nit.)

func TestGenerateComposeSnippet_RetainsPortRange(t *testing.T) {
	ports := []PortMapping{{HostIP: "", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 6}}
	snippet, err := GenerateComposeSnippet("ranged-service", ports)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(snippet, `"8000-8005:9000-9005/tcp"`) {
		t.Errorf("expected full port range preserved in compose snippet, got: %s", snippet)
	}
	if strings.Contains(snippet, `"8000:9000/tcp"`) {
		t.Errorf("range must not be silently collapsed to a single port: %s", snippet)
	}
}

func TestGenerateQuadletSnippet_RetainsPortRange(t *testing.T) {
	ports := []PortMapping{{HostIP: "0.0.0.0", HostPort: 8000, ContainerPort: 9000, Protocol: "udp", RangeSize: 6}}
	snippet := GenerateQuadletSnippet(ports)
	if !strings.Contains(snippet, "PublishPort=0.0.0.0:8000-8005:9000-9005/udp") {
		t.Errorf("expected full port range preserved in quadlet snippet, got: %s", snippet)
	}
}

// --- v1.4 hardening: backend snippet-generation bound methods (item 4) ---
// The frontend must never hand-format Podman/Compose/Quadlet port syntax;
// PreviewComposeSnippet/PreviewQuadletSnippet are the bound methods it
// calls instead, going through the same canonical FormatPublishSpec.

func TestPreviewComposeSnippet_RequiresServiceName(t *testing.T) {
	svc := &PodmanService{}
	if _, err := svc.PreviewComposeSnippet("", []PortMapping{{ContainerPort: 80, Protocol: "tcp"}}); err == nil {
		t.Errorf("expected an error when no compose service name is available, never a snippet keyed under an invented name")
	}
}

func TestPreviewComposeSnippet_MatchesGenerateComposeSnippet(t *testing.T) {
	svc := &PodmanService{}
	ports := []PortMapping{{HostIP: "", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 6}}
	got, err := svc.PreviewComposeSnippet("flowise", ports)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want, wantErr := GenerateComposeSnippet("flowise", ports)
	if wantErr != nil {
		t.Fatalf("unexpected error: %v", wantErr)
	}
	if got != want {
		t.Errorf("PreviewComposeSnippet diverged from GenerateComposeSnippet:\ngot:  %s\nwant: %s", got, want)
	}
}

func TestPreviewQuadletSnippet_MatchesGenerateQuadletSnippet(t *testing.T) {
	svc := &PodmanService{}
	ports := []PortMapping{{HostIP: "::", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 3}}
	got, err := svc.PreviewQuadletSnippet(ports)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := GenerateQuadletSnippet(ports)
	if got != want {
		t.Errorf("PreviewQuadletSnippet diverged from GenerateQuadletSnippet:\ngot:  %s\nwant: %s", got, want)
	}
}

// --- Mutation transaction simulator ---
//
// mutationSim implements CommandRunner and simulates just enough of Podman's
// container lifecycle (ps/rename/stop/start/rm/run/create/inspect) for the
// MutateContainerPorts and AdoptContainer transactions to be exercised
// end-to-end, including every failure point, without touching a real host.

type simContainer struct {
	id      string
	image   string
	imageID string
	state   string // "running" or "exited"
	ports   []PortMapping
	managed bool
	service string
}

type mutationSim struct {
	mu         sync.Mutex
	containers map[string]*simContainer
	nextID     int
	// failStep, if non-empty, makes the matching command fail once.
	failStep string
}

func newMutationSim() *mutationSim {
	return &mutationSim{containers: map[string]*simContainer{}}
}

func (s *mutationSim) addContainer(name, image, state string, ports []PortMapping, managed bool, service string) {
	s.nextID++
	s.containers[name] = &simContainer{
		id:    fmt.Sprintf("%040d", s.nextID),
		image: image,
		imageID: func() string {
			if strings.HasPrefix(image, "sha256:") {
				return image
			}
			return "sha256:sim-" + image
		}(),
		state:   state,
		ports:   ports,
		managed: managed,
		service: service,
	}
}

func (s *mutationSim) psJSON() string {
	var sb strings.Builder
	sb.WriteString("[")
	first := true
	for name, c := range s.containers {
		if !first {
			sb.WriteString(",")
		}
		first = false
		labels := "{}"
		if c.managed {
			labels = fmt.Sprintf(`{"io.podder.managed":"true","io.podder.service":"%s","io.podder.schema-version":"%d"}`, c.service, CurrentSpecSchemaVersion)
		}
		var portsJSON strings.Builder
		portsJSON.WriteString("[")
		for i, p := range c.ports {
			if i > 0 {
				portsJSON.WriteString(",")
			}
			portsJSON.WriteString(fmt.Sprintf(`{"host_ip":"%s","container_port":%d,"host_port":%d,"protocol":"%s"}`, p.HostIP, p.ContainerPort, p.HostPort, p.Protocol))
		}
		portsJSON.WriteString("]")
		sb.WriteString(fmt.Sprintf(`{"Id":"%s","Names":["%s"],"Image":"%s","ImageID":"%s","State":"%s","Ports":%s,"Labels":%s}`,
			c.id, name, c.image, c.imageID, c.state, portsJSON.String(), labels))
	}
	sb.WriteString("]")
	return sb.String()
}

// parseNameAndPorts extracts --name/-p flags and the image from a
// podman run/create argv, correctly stopping at the image even when a
// command argv (e.g. "sh -c ...") follows it positionally.
func parseNameAndPorts(args []string) (name, image string, ports []PortMapping) {
	flagsWithValue := map[string]bool{"--name": true, "-p": true, "--mount": true, "--env": true, "--label": true, "--entrypoint": true}

	i := 0
	if len(args) > 0 && (args[0] == "run" || args[0] == "create") {
		i = 1
	}
	if i < len(args) && args[i] == "-d" {
		i++
	}
	for i < len(args) {
		a := args[i]
		if flagsWithValue[a] {
			if i+1 < len(args) {
				switch a {
				case "--name":
					name = args[i+1]
				case "-p":
					if pm, err := ParsePublishSpec(args[i+1]); err == nil {
						ports = append(ports, *pm)
					}
				}
				i += 2
				continue
			}
			i++
			continue
		}
		image = a
		break
	}
	return name, image, ports
}

func (s *mutationSim) Run(name string, args ...string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if name == "ss" {
		return "", "", nil
	}
	if name != "podman" || len(args) == 0 {
		return "", "", nil
	}

	verb := args[0]
	if s.failStep == verb {
		s.failStep = "" // fail once
		return "", "simulated failure", fmt.Errorf("simulated failure at %s", verb)
	}

	switch verb {
	case "ps":
		return s.psJSON(), "", nil
	case "rename":
		old, newName := args[1], args[2]
		c, ok := s.containers[old]
		if !ok {
			return "", "no such container", fmt.Errorf("no such container %s", old)
		}
		delete(s.containers, old)
		s.containers[newName] = c
		return "", "", nil
	case "stop":
		target := args[1]
		for containerName, c := range s.containers {
			if containerName == target || c.id == target {
				c.state = "exited"
			}
		}
		return "", "", nil
	case "start":
		target := args[1]
		for containerName, c := range s.containers {
			if containerName == target || c.id == target {
				c.state = "running"
			}
		}
		return "", "", nil
	case "rm":
		target := args[len(args)-1]
		for containerName, c := range s.containers {
			if containerName == target || c.id == target {
				delete(s.containers, containerName)
			}
		}
		return "", "", nil
	case "run", "create":
		cname, image, ports := parseNameAndPorts(args)
		state := "exited"
		if verb == "run" {
			state = "running"
		}
		managed := strings.Contains(strings.Join(args, " "), "io.podder.managed=true")
		service := cname
		s.addContainer(cname, image, state, ports, managed, service)
		return s.containers[cname].id, "", nil
	case "inspect":
		return "[]", "", nil
	}
	return "", "", nil
}

func withTestHome(t *testing.T) {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	setTestConfigHome(t, tempDir)
}

func withFastPolling(t *testing.T) {
	t.Helper()
	origAttempts, origInterval := mutationPollAttempts, mutationPollInterval
	mutationPollAttempts, mutationPollInterval = 3, time.Millisecond
	t.Cleanup(func() { mutationPollAttempts, mutationPollInterval = origAttempts, origInterval })
}

func setupManagedContainer(t *testing.T, sim *mutationSim, name, image, state string, ports []PortMapping) *PodmanService {
	t.Helper()
	withTestHome(t)
	withFastPolling(t)

	svc := &PodmanService{runner: sim}
	spec := ContainerSpec{
		Name:           name,
		Image:          image,
		Managed:        true,
		SchemaVersion:  CurrentSpecSchemaVersion,
		ResolvedImage:  "sha256:sim-" + image,
		ReplayComplete: true,
		PortMappings:   ports,
		Binds:          []BindMountSpec{{HostPath: t.TempDir(), ContainerPath: "/data"}},
		Env:            map[string]string{"FOO": "bar"},
		Command:        []string{"sh", "-c", "echo hello world"},
	}
	if err := saveSpec(spec); err != nil {
		t.Fatalf("failed to seed spec: %v", err)
	}
	sim.addContainer(name, image, state, ports, true, name)
	return svc
}

func TestMutateContainerPorts_AdhocBlocked(t *testing.T) {
	sim := newMutationSim()
	withTestHome(t)
	svc := &PodmanService{runner: sim}
	sim.addContainer("legacy", "nginx", "running", nil, false, "")

	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "legacy", NewPorts: []PortMapping{{HostPort: 9000, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected ad-hoc mutation to be blocked, got success")
	}
	if !strings.Contains(result.Guidance, "Adopt") {
		t.Errorf("expected adopt-first guidance, got: %s", result.Guidance)
	}
	if !result.RequiresExternal {
		t.Errorf("expected RequiresExternal for ad-hoc container")
	}
}

func TestMutateContainerPorts_ComposeGuidanceUsesRealServiceName(t *testing.T) {
	withTestHome(t)
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(string, []string) (string, string, error) {
		return `[{"Id":"composeid1","Names":["myproj_web_1"],"State":"running","Ports":[],"Labels":{"com.docker.compose.project":"myproj","com.docker.compose.service":"web"}}]`, "", nil
	})
	svc := &PodmanService{runner: runner}

	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "composeid1", NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !result.RequiresExternal {
		t.Fatalf("expected Compose-managed mutation to be externally guided, got: %+v", result)
	}
	if !strings.Contains(result.ComposeSnippet, "web:") {
		t.Errorf("expected the generated snippet to use the real Compose service name 'web', got: %s", result.ComposeSnippet)
	}
}

func TestMutateContainerPorts_ComposeGuidanceWithoutServiceLabelInventsNoName(t *testing.T) {
	withTestHome(t)
	runner := newFakeCommandRunner()
	// A container with Compose project evidence but no service label at
	// all — detectCompose still classifies this as "compose" provenance,
	// but the real service identity is not actually known.
	runner.On("podman ps", func(string, []string) (string, string, error) {
		return `[{"Id":"composeid2","Names":["myproj_unknown_1"],"State":"running","Ports":[],"Labels":{"com.docker.compose.project":"myproj"}}]`, "", nil
	})
	svc := &PodmanService{runner: runner}

	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "composeid2", NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ComposeSnippet != "" {
		t.Errorf("expected no invented-name snippet when the service identity is unknown, got: %s", result.ComposeSnippet)
	}
	if !strings.Contains(result.Guidance, "could not be safely determined") {
		t.Errorf("expected guidance to state the identity could not be determined, got: %s", result.Guidance)
	}
}

func TestMutateContainerPorts_MissingSpecBlocked(t *testing.T) {
	sim := newMutationSim()
	withTestHome(t)
	svc := &PodmanService{runner: sim}
	// Container carries Podder labels but no spec was ever saved.
	sim.addContainer("ghost", "nginx", "running", nil, true, "ghost")

	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "ghost", NewPorts: []PortMapping{{HostPort: 9000, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected missing-spec mutation to be blocked")
	}
	if !strings.Contains(result.Steps[0].Message, "inconsistent") {
		t.Errorf("expected 'metadata inconsistent' message, got: %s", result.Steps[0].Message)
	}
}

func TestMutateContainerPorts_SuccessfulRunningMutation(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "web", "nginx:alpine", "running", oldPorts)

	newPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}}
	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "web", NewPorts: newPorts})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected successful mutation, steps: %+v", result.Steps)
	}
	if !result.ConfigurationVerified {
		t.Errorf("expected ConfigurationVerified to be true")
	}

	c, ok := sim.containers["web"]
	if !ok {
		t.Fatalf("expected container 'web' to exist after mutation")
	}
	if c.state != "running" {
		t.Errorf("expected replacement to remain running, got %s", c.state)
	}
	if len(c.ports) != 1 || c.ports[0].HostPort != 9090 {
		t.Errorf("expected new port 9090 configured, got %+v", c.ports)
	}

	spec, err := svc.GetSpec("web")
	if err != nil {
		t.Fatalf("failed to load committed spec: %v", err)
	}
	if len(spec.PortMappings) != 1 || spec.PortMappings[0].HostPort != 9090 {
		t.Errorf("expected committed spec to reflect new ports, got %+v", spec.PortMappings)
	}
	if len(spec.Binds) != 1 || spec.Env["FOO"] != "bar" {
		t.Errorf("expected binds/env preserved in committed spec: %+v", spec)
	}
	if len(spec.Command) != 3 || spec.Command[2] != "echo hello world" {
		t.Errorf("expected command argv preserved exactly, got %+v", spec.Command)
	}
}

func TestMutateContainerPorts_StoppedContainerStaysStoppedAndNotAutoStarted(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "worker", "alpine", "exited", oldPorts)

	newPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8081, ContainerPort: 80, Protocol: "tcp"}}
	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "worker", NewPorts: newPorts})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected successful mutation, steps: %+v", result.Steps)
	}

	c := sim.containers["worker"]
	if c == nil {
		t.Fatalf("expected replacement container to exist")
	}
	if c.state != "exited" {
		t.Errorf("expected replacement to remain stopped (never auto-started), got state %q", c.state)
	}
}

func TestMutateContainerPorts_CreateFailureRollsBackSuccessfully(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "api", "alpine", "running", oldPorts)
	sim.failStep = "run"

	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "api", NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: 8082, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected mutation to fail when create fails")
	}
	if !result.RolledBack {
		t.Fatalf("expected rollback to succeed, result: %+v", result)
	}
	if result.Rollback == nil || !result.Rollback.Verified {
		t.Fatalf("expected verified rollback result, got: %+v", result.Rollback)
	}

	c := sim.containers["api"]
	if c == nil {
		t.Fatalf("expected original container restored under its original name")
	}
	if c.state != "running" {
		t.Errorf("expected original running state restored, got %s", c.state)
	}
	if len(c.ports) != 1 || c.ports[0].HostPort != 8080 {
		t.Errorf("expected original port mapping restored, got %+v", c.ports)
	}

	spec, err := svc.GetSpec("api")
	if err != nil {
		t.Fatalf("expected old known-good spec retained: %v", err)
	}
	if spec.PortMappings[0].HostPort != 8080 {
		t.Errorf("expected retained spec to still show the OLD port, got %+v", spec.PortMappings)
	}
}

func TestMutateContainerPorts_VerifyFailureOnMappingMismatchRollsBack(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "svc", "alpine", "running", oldPorts)

	// Simulate the replacement container coming up with the WRONG ports by
	// intercepting "run" and manually fixing up the sim afterward.
	origRun := sim
	_ = origRun
	newPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8099, ContainerPort: 80, Protocol: "tcp"}}

	// Wrap the sim so that once the replacement is created, its ports get
	// silently overwritten to something else before VERIFY reads them back.
	wrapper := &mismatchInjector{sim: sim}
	svc.runner = wrapper

	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "svc", NewPorts: newPorts})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected mismatch to fail the transaction")
	}
	if !result.RolledBack {
		t.Fatalf("expected successful rollback after mapping mismatch, got: %+v", result)
	}
}

// mismatchInjector wraps mutationSim and corrupts the freshly-created
// container's ports once, simulating a replacement that came up configured
// differently than requested.
type mismatchInjector struct {
	sim       *mutationSim
	corrupted bool
}

func (m *mismatchInjector) Run(name string, args ...string) (string, string, error) {
	out, errOut, err := m.sim.Run(name, args...)
	if !m.corrupted && len(args) > 0 && args[0] == "run" {
		m.corrupted = true
		m.sim.mu.Lock()
		for _, c := range m.sim.containers {
			if c.state == "running" {
				c.ports = []PortMapping{{HostIP: "127.0.0.1", HostPort: 1234, ContainerPort: 80, Protocol: "tcp"}}
			}
		}
		m.sim.mu.Unlock()
	}
	return out, errOut, err
}

func TestMutateContainerPorts_RollbackFailureReportsManualRecovery(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "db", "alpine", "running", oldPorts)

	failer := &failNthRename{sim: sim, failAtRenameCall: 2} // 1st rename = quiesce (must succeed), 2nd = rollback restore (fails)
	svc.runner = failer

	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "db", NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: 8088, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected transaction to fail")
	}
	if result.RolledBack {
		t.Fatalf("expected rollback to be reported as FAILED, not succeeded")
	}
	if !result.ManualRecoveryRequired {
		t.Fatalf("expected ManualRecoveryRequired=true when rollback cannot be verified")
	}
	if result.Rollback == nil || result.Rollback.Verified {
		t.Fatalf("expected an unverified rollback result, got: %+v", result.Rollback)
	}
	if len(result.Rollback.Errors) == 0 {
		t.Errorf("expected rollback errors to be retained, not discarded")
	}
}

// failNthRename fails create (so a rollback is triggered) and then fails the
// rollback's own rename-back step, to exercise "rollback itself fails".
type failNthRename struct {
	sim              *mutationSim
	renameCalls      int
	failAtRenameCall int
}

func (f *failNthRename) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && args[0] == "rename" {
		f.renameCalls++
		if f.renameCalls == f.failAtRenameCall {
			return "", "simulated rename failure", fmt.Errorf("simulated rename failure")
		}
	}
	if name == "podman" && len(args) > 0 && args[0] == "run" {
		return "", "simulated create failure", fmt.Errorf("simulated create failure")
	}
	return f.sim.Run(name, args...)
}

func TestPortMappingSetEqual(t *testing.T) {
	a := []PortMapping{{HostIP: "127.0.0.1", HostPort: 80, ContainerPort: 80, Protocol: "tcp"}}
	b := []PortMapping{{HostIP: "127.0.0.1", HostPort: 80, ContainerPort: 80, Protocol: "tcp"}}
	if ok, _, _ := portMappingSetEqual(a, b); !ok {
		t.Errorf("expected identical sets to be equal")
	}

	c := []PortMapping{{HostIP: "127.0.0.1", HostPort: 81, ContainerPort: 80, Protocol: "tcp"}}
	ok, missing, unexpected := portMappingSetEqual(a, c)
	if ok || len(missing) != 1 || len(unexpected) != 1 {
		t.Errorf("expected mismatch to be reported, got ok=%v missing=%v unexpected=%v", ok, missing, unexpected)
	}
}

// TestPortMappingSetEqual_OmittedBindIsNotSameDeclarationAsExplicitWildcard
// is the exact v1.4 regression this hardening pass closes: portMappingSetEqual
// previously keyed on NormalizeAddress (conflict-oriented normalization),
// which folds an omitted host bind ("") and an explicit "0.0.0.0" together.
// That let a declared spec with an omitted bind silently "verify" against a
// runtime mapping that was actually configured with an explicit wildcard
// (or vice versa) — a real configuration discrepancy that must be reported,
// not hidden by treating the two as the same set member.
func TestPortMappingSetEqual_OmittedBindIsNotSameDeclarationAsExplicitWildcard(t *testing.T) {
	declaredOmitted := []PortMapping{{HostIP: "", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	observedExplicitWildcard := []PortMapping{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}

	ok, missing, unexpected := portMappingSetEqual(declaredOmitted, observedExplicitWildcard)
	if ok {
		t.Fatalf("expected an omitted declared bind and an explicit 0.0.0.0 observed bind to NOT be treated as the same declaration")
	}
	if len(missing) != 1 || len(unexpected) != 1 {
		t.Errorf("expected exactly one missing (declared) and one unexpected (observed) mapping, got missing=%v unexpected=%v", missing, unexpected)
	}

	// An exact match on both sides (same declared form) must still verify.
	if ok, _, _ := portMappingSetEqual(declaredOmitted, declaredOmitted); !ok {
		t.Errorf("expected an identical omitted-bind set to compare equal to itself")
	}
	explicitBoth := []PortMapping{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	if ok, _, _ := portMappingSetEqual(observedExplicitWildcard, explicitBoth); !ok {
		t.Errorf("expected identical explicit-wildcard sets to compare equal")
	}
}

func TestPortMappingSetEqual_RangeSizeIsPartOfIdentity(t *testing.T) {
	a := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 6}}
	sameRange := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 6}}
	collapsedToSinglePort := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp"}}

	if ok, _, _ := portMappingSetEqual(a, sameRange); !ok {
		t.Errorf("expected identical ranged mappings to compare equal")
	}
	if ok, _, _ := portMappingSetEqual(a, collapsedToSinglePort); ok {
		t.Errorf("expected a range silently collapsed to a single port to be reported as a mismatch, not verified as equal")
	}
}

func TestClassifyLifecycle(t *testing.T) {
	if kind, ok := classifyLifecycle("running"); kind != lifecycleRunning || !ok {
		t.Errorf("expected running to classify as supported/running")
	}
	if kind, ok := classifyLifecycle("exited"); kind != lifecycleStopped || !ok {
		t.Errorf("expected exited to classify as supported/stopped")
	}
	if _, ok := classifyLifecycle("paused"); ok {
		t.Errorf("expected paused to be unsupported")
	}
}

func TestMutateContainerPorts_UnchangedPortDoesNotConflictWithItself(t *testing.T) {
	sim := newMutationSim()
	samePorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "steady", "alpine", "running", samePorts)

	// Requesting the exact same mapping the container already holds must
	// not be rejected as a self-conflict.
	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "steady", NewPorts: samePorts})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected an unchanged port mapping to be treated as its own claim, not a conflict: %+v", result.Steps)
	}
}

func TestNewBackupNameIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		n := newBackupName("svc")
		if seen[n] {
			t.Fatalf("expected unique backup names, got collision: %s", n)
		}
		seen[n] = true
	}
}

func TestMutateContainerPortsReportsRealExposureWidening(t *testing.T) {
	sim := newMutationSim()
	svc := setupManagedContainer(t, sim, "exposure-app", "alpine:latest", "running", []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}})
	result, err := svc.MutateContainerPorts(PortMutationRequest{
		ContainerID: "exposure-app",
		NewPorts:    []PortMapping{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}},
	})
	if err != nil || !result.Success {
		t.Fatalf("expected successful mutation, result=%+v err=%v", result, err)
	}
	if len(result.ExposureWarnings) == 0 {
		t.Fatalf("actual loopback-to-wildcard edit must report an exposure widening")
	}
}

func TestMutateContainerPortsRenameFailureNeedsNoRollback(t *testing.T) {
	sim := newMutationSim()
	svc := setupManagedContainer(t, sim, "rename-safe", "alpine:latest", "running", []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}})
	sim.failStep = "rename"
	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "rename-safe", NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Rollback != nil || result.RolledBack || result.ManualRecoveryRequired {
		t.Fatalf("rename failure happened before mutation and must not trigger recovery: %+v", result)
	}
	if original := sim.containers["rename-safe"]; original == nil || original.state != "running" {
		t.Fatalf("original workload changed despite rename failure: %+v", original)
	}
}

type startErrorAfterSuccessSim struct {
	sim *mutationSim
}

func (s *startErrorAfterSuccessSim) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 1 && args[0] == "start" {
		s.sim.mu.Lock()
		if c := s.sim.containers[args[1]]; c != nil {
			c.state = "running"
		}
		s.sim.mu.Unlock()
		return "", "reported failure after starting", fmt.Errorf("reported failure after starting")
	}
	return s.sim.Run(name, args...)
}

func TestRollbackUsesObservedStateWhenStartReportsError(t *testing.T) {
	sim := newMutationSim()
	svc := setupManagedContainer(t, sim, "start-observed", "alpine:latest", "running", []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}})
	svc.runner = &startErrorAfterSuccessSim{sim: sim}
	sim.failStep = "run"
	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "start-observed", NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rollback == nil || !result.Rollback.Verified || result.ManualRecoveryRequired {
		t.Fatalf("final observed running state should make rollback verified despite start error: %+v", result)
	}
}

type healthTransitionSim struct {
	sim   *mutationSim
	calls int
}

func (h *healthTransitionSim) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && args[0] == "inspect" {
		h.calls++
		status := "starting"
		if h.calls > 1 {
			status = "unhealthy"
		}
		return fmt.Sprintf(`[{"State":{"Health":{"Status":"%s"}}}]`, status), "", nil
	}
	return h.sim.Run(name, args...)
}

func TestHealthStartingThenUnhealthyTriggersRollback(t *testing.T) {
	sim := newMutationSim()
	svc := setupManagedContainer(t, sim, "health-app", "alpine:latest", "running", []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}})
	health := &healthTransitionSim{sim: sim}
	svc.runner = health
	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "health-app", NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Rollback == nil || !result.Rollback.Verified {
		t.Fatalf("final unhealthy state must fail and rollback the transaction: %+v", result)
	}
}

// --- v1.4 hardening: GetContainerPortEditState (item 1) ---
//
// This is the ONE backend method the port editor now uses to populate
// itself, resolving fresh state by exact container ID regardless of which
// UI entry point (container-card action or Ports-tab action) opened it.
// The bug this closes: the container-card path used to pass its cached
// mappings into the editor while the Ports-tab path passed `[]`, so the
// exact same Podder-managed workload could appear to have real published
// ports or none at all depending on which button was clicked.

func TestGetContainerPortEditState_ReturnsFreshMappings(t *testing.T) {
	ports := []PortMapping{
		{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp", RangeSize: 1},
		{HostIP: "0.0.0.0", HostPort: 8000, ContainerPort: 9000, Protocol: "tcp", RangeSize: 6},
	}
	psJSON := `[{"Id":"deadbeefcafe0000000000000000000000000001","Names":["web-app"],"Image":"nginx:latest","ImageID":"sha256:abc","State":"running","Ports":[` +
		`{"host_ip":"127.0.0.1","host_port":8080,"container_port":80,"protocol":"tcp","range":1},` +
		`{"host_ip":"0.0.0.0","host_port":8000,"container_port":9000,"protocol":"tcp","range":6}` +
		`],"Labels":{"io.podder.managed":"true","io.podder.service":"web-app","io.podder.schema-version":"2"}}]`

	runner := newFakeCommandRunner()
	runner.On("podman ps", func(n string, args []string) (string, string, error) { return psJSON, "", nil })
	svc := &PodmanService{runner: runner}

	state, err := svc.GetContainerPortEditState("deadbeefcafe0000000000000000000000000001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.ContainerName != "web-app" {
		t.Errorf("expected container name web-app, got %q", state.ContainerName)
	}
	if state.Provenance.Type != "podder" {
		t.Errorf("expected podder provenance, got %+v", state.Provenance)
	}
	if !reflect.DeepEqual(state.PortMappings, ports) {
		t.Fatalf("expected fresh backend-observed port mappings %+v, got %+v", ports, state.PortMappings)
	}
	// A second call must not return a mapping slice that aliases the first
	// call's backing array (defensive copy, not a shared cache reference).
	state.PortMappings[0].HostPort = 1
	state2, err := svc.GetContainerPortEditState("deadbeefcafe0000000000000000000000000001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state2.PortMappings[0].HostPort != 8080 {
		t.Errorf("expected mutating a previously returned edit state to not affect a fresh fetch, got %+v", state2.PortMappings[0])
	}
}

func TestGetContainerPortEditState_UnknownContainerFailsVisibly(t *testing.T) {
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(n string, args []string) (string, string, error) { return "[]", "", nil })
	svc := &PodmanService{runner: runner}

	if _, err := svc.GetContainerPortEditState("does-not-exist"); err == nil {
		t.Fatalf("expected an error for an unresolvable container, never a same-shaped empty result the UI could mistake for 'no ports configured'")
	}
}

func TestGetContainerPortEditState_EmptyIDRejected(t *testing.T) {
	svc := &PodmanService{runner: newFakeCommandRunner()}
	if _, err := svc.GetContainerPortEditState(""); err == nil {
		t.Fatalf("expected an empty container id to be rejected")
	}
}

func TestGetContainerPortEditState_InspectFailureFailsRatherThanBlank(t *testing.T) {
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(n string, args []string) (string, string, error) {
		return "", "podman: connection refused", fmt.Errorf("exit status 1")
	})
	svc := &PodmanService{runner: runner}

	if _, err := svc.GetContainerPortEditState("some-container"); err == nil {
		t.Fatalf("expected a failed backend inspection to surface as an error, not a blank editable state")
	}
}

// --- v1.4 hardening round 2: GetContainerPortEditState requires an EXACT
// container ID, never a prefix or a name (finding 5) ---

func newFullIDPortEditFixture() (*fakeCommandRunner, string) {
	const fullID = "deadbeefcafe0000000000000000000000000001"
	psJSON := `[{"Id":"` + fullID + `","Names":["web-app"],"Image":"nginx:latest","ImageID":"sha256:abc","State":"running","Ports":[` +
		`{"host_ip":"127.0.0.1","host_port":8080,"container_port":80,"protocol":"tcp","range":1}` +
		`],"Labels":{"io.podder.managed":"true","io.podder.service":"web-app","io.podder.schema-version":"2"}}]`
	runner := newFakeCommandRunner()
	runner.On("podman ps", func(n string, args []string) (string, string, error) { return psJSON, "", nil })
	return runner, fullID
}

func TestGetContainerPortEditState_ExactFullIDSucceeds(t *testing.T) {
	runner, fullID := newFullIDPortEditFixture()
	svc := &PodmanService{runner: runner}

	state, err := svc.GetContainerPortEditState(fullID)
	if err != nil {
		t.Fatalf("expected the exact full container ID to resolve, got: %v", err)
	}
	if state.ContainerID != fullID {
		t.Errorf("expected ContainerID %q, got %q", fullID, state.ContainerID)
	}
}

func TestGetContainerPortEditState_IDPrefixRejected(t *testing.T) {
	runner, fullID := newFullIDPortEditFixture()
	svc := &PodmanService{runner: runner}

	prefix := fullID[:12] // a valid, non-empty, unambiguous-looking short prefix
	if _, err := svc.GetContainerPortEditState(prefix); err == nil {
		t.Fatalf("expected an ID prefix to be rejected by this safety-critical lookup, which requires an exact ID")
	}
}

func TestGetContainerPortEditState_NameRejected(t *testing.T) {
	runner, _ := newFullIDPortEditFixture()
	svc := &PodmanService{runner: runner}

	if _, err := svc.GetContainerPortEditState("web-app"); err == nil {
		t.Fatalf("expected a container NAME to be rejected by this safety-critical lookup, which requires an exact ID")
	}
}

func TestGetContainerPortEditState_UnknownExactIDRejected(t *testing.T) {
	runner, _ := newFullIDPortEditFixture()
	svc := &PodmanService{runner: runner}

	if _, err := svc.GetContainerPortEditState("0000000000000000000000000000000000009999"); err == nil {
		t.Fatalf("expected an unknown exact ID to be rejected")
	}
}

// findContainerByIdentity (the fuzzier, non-safety-critical resolver) must
// still accept a prefix/name for its other, non-safety-critical callers —
// this pins its unchanged behavior so it isn't accidentally tightened (or
// loosened) alongside the GetContainerPortEditState fix.
func TestFindContainerByIdentity_StillAcceptsPrefixAndName(t *testing.T) {
	containers := []Container{
		{Id: "deadbeefcafe0000000000000000000000000001", Names: []string{"/web-app"}},
	}
	if c := findContainerByIdentity(containers, "deadbeefcafe0000000000000000000000000001"); c == nil {
		t.Errorf("expected exact ID to resolve")
	}
	if c := findContainerByIdentity(containers, "deadbeefcafe"); c == nil {
		t.Errorf("expected ID prefix to still resolve via findContainerByIdentity")
	}
	if c := findContainerByIdentity(containers, "web-app"); c == nil {
		t.Errorf("expected name to still resolve via findContainerByIdentity")
	}
}

func TestFindContainerByExactID_RejectsPrefixAndName(t *testing.T) {
	containers := []Container{
		{Id: "deadbeefcafe0000000000000000000000000001", Names: []string{"/web-app"}},
	}
	if c := findContainerByExactID(containers, "deadbeefcafe0000000000000000000000000001"); c == nil {
		t.Errorf("expected exact ID to resolve")
	}
	if c := findContainerByExactID(containers, "deadbeefcafe"); c != nil {
		t.Errorf("expected a prefix to be rejected by findContainerByExactID, got %+v", c)
	}
	if c := findContainerByExactID(containers, "web-app"); c != nil {
		t.Errorf("expected a name to be rejected by findContainerByExactID, got %+v", c)
	}
}
