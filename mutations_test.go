package main

import (
	"fmt"
	"os"
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

	snippet := GenerateComposeSnippet("web-service", ports)
	if !strings.Contains(snippet, "web-service:") {
		t.Errorf("expected service name in snippet, got: %s", snippet)
	}
	if !strings.Contains(snippet, `"127.0.0.1:8080:80/tcp"`) {
		t.Errorf("expected loopback port in snippet, got: %s", snippet)
	}
	if !strings.Contains(snippet, `"5353:5353/udp"`) {
		t.Errorf("expected wildcard port in snippet, got: %s", snippet)
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

// --- Mutation transaction simulator ---
//
// mutationSim implements CommandRunner and simulates just enough of Podman's
// container lifecycle (ps/rename/stop/start/rm/run/create/inspect) for the
// MutateContainerPorts and AdoptContainer transactions to be exercised
// end-to-end, including every failure point, without touching a real host.

type simContainer struct {
	id      string
	image   string
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
		id:      fmt.Sprintf("%040d", s.nextID),
		image:   image,
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
			labels = fmt.Sprintf(`{"io.podder.managed":"true","io.podder.service":"%s"}`, c.service)
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
		sb.WriteString(fmt.Sprintf(`{"Id":"%s","Names":["%s"],"Image":"%s","State":"%s","Ports":%s,"Labels":%s}`,
			c.id, name, c.image, c.state, portsJSON.String(), labels))
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
		if c, ok := s.containers[args[1]]; ok {
			c.state = "exited"
		}
		return "", "", nil
	case "start":
		if c, ok := s.containers[args[1]]; ok {
			c.state = "running"
		}
		return "", "", nil
	case "rm":
		target := args[len(args)-1]
		delete(s.containers, target)
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
	origHome := os.Getenv("HOME")
	t.Cleanup(func() { os.Setenv("HOME", origHome) })
	os.Setenv("HOME", tempDir)
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
		Name:         name,
		Image:        image,
		Managed:      true,
		PortMappings: ports,
		Binds:        []BindMountSpec{{HostPath: t.TempDir(), ContainerPath: "/data"}},
		Env:          map[string]string{"FOO": "bar"},
		Command:      []string{"sh", "-c", "echo hello world"},
	}
	if err := svc.SaveSpec(spec); err != nil {
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

// TestMutateContainerPorts_RenameFailureLeavesOriginalUntouched covers item
// 23: if the very first QUIESCE step (the rename) fails, the original
// container was never moved — this must be reported directly, without
// attempting (and spuriously failing) a rollback that has no backup to
// restore from.
func TestMutateContainerPorts_RenameFailureLeavesOriginalUntouched(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "cache", "alpine", "running", oldPorts)
	sim.failStep = "rename"

	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "cache", NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: 8081, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected mutation to fail when the initial rename fails")
	}
	if result.RolledBack {
		t.Errorf("expected RolledBack=false: nothing was moved, so there is nothing to roll back")
	}
	if result.ManualRecoveryRequired {
		t.Errorf("expected ManualRecoveryRequired=false: the original container was never touched")
	}
	if result.Rollback != nil {
		t.Errorf("expected no rollback to have been attempted at all, got: %+v", result.Rollback)
	}

	c := sim.containers["cache"]
	if c == nil {
		t.Fatalf("expected the original container to still exist under its original name")
	}
	if c.state != "running" || len(c.ports) != 1 || c.ports[0].HostPort != 8080 {
		t.Errorf("expected the original container completely untouched, got: %+v", c)
	}
}

// TestMutateContainerPorts_StopFailureDuringQuiesceStillRestoresOriginal
// covers item 23's second half: a QUIESCE-stage error that happens AFTER
// the rename (so the original genuinely was moved) must still trigger a
// real, verified rollback — the final observed state is what decides the
// outcome, not merely that some intermediate command returned an error.
func TestMutateContainerPorts_StopFailureDuringQuiesceStillRestoresOriginal(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "queue", "alpine", "running", oldPorts)
	sim.failStep = "stop"

	result, err := svc.MutateContainerPorts(PortMutationRequest{ContainerID: "queue", NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: 8082, ContainerPort: 80, Protocol: "tcp"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected mutation to fail when stop fails during quiesce")
	}
	if !result.RolledBack {
		t.Fatalf("expected a verified rollback when stop fails after a successful rename, result: %+v", result)
	}
	if result.ManualRecoveryRequired {
		t.Errorf("expected no manual recovery when the original is confirmed restored")
	}

	c := sim.containers["queue"]
	if c == nil {
		t.Fatalf("expected the original container to still exist — state must not be destroyed")
	}
	if c.state != "running" || len(c.ports) != 1 || c.ports[0].HostPort != 8080 {
		t.Errorf("expected the original container's state intact after rollback, got: %+v", c)
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
