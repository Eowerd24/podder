package main

import (
	"fmt"
	"os"
	"testing"
)

func TestSpecsCRUD(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{}

	// 1. Initial list should be empty
	specs, err := service.ListSpecs()
	if err != nil {
		t.Fatalf("unexpected error listing specs: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected 0 specs, got %d", len(specs))
	}

	// 2. Save spec
	spec1 := ContainerSpec{
		Name:  "my-app",
		Image: "docker.io/library/nginx:alpine",
		PortMappings: []PortMapping{
			{
				HostIP:        "127.0.0.1",
				HostPort:      8080,
				ContainerPort: 80,
				Protocol:      "tcp",
			},
		},
		Binds: []BindMountSpec{
			{
				HostPath:      "/tmp/html",
				ContainerPath: "/usr/share/nginx/html",
				ReadOnly:      true,
			},
		},
		Command: []string{"nginx", "-g", "daemon off;"},
	}

	if err := service.SaveSpec(spec1); err != nil {
		t.Fatalf("failed to save spec: %v", err)
	}

	// 3. Get spec
	loaded, err := service.GetSpec("my-app")
	if err != nil {
		t.Fatalf("failed to get spec: %v", err)
	}
	if loaded.Name != "my-app" || loaded.Image != "docker.io/library/nginx:alpine" {
		t.Errorf("unexpected loaded spec: %+v", loaded)
	}
	if len(loaded.PortMappings) != 1 || loaded.PortMappings[0].HostPort != 8080 {
		t.Errorf("unexpected port mapping in loaded spec: %+v", loaded.PortMappings)
	}
	if len(loaded.Binds) != 1 || loaded.Binds[0].HostPath != "/tmp/html" {
		t.Errorf("unexpected binds in loaded spec: %+v", loaded.Binds)
	}

	// 4. List specs should return 1
	specs, err = service.ListSpecs()
	if err != nil {
		t.Fatalf("failed to list specs: %v", err)
	}
	if len(specs) != 1 {
		t.Errorf("expected 1 spec, got %d", len(specs))
	}

	// 5. Delete spec
	if err := service.DeleteSpec("my-app"); err != nil {
		t.Fatalf("failed to delete spec: %v", err)
	}

	// 6. Get deleted spec should error
	_, err = service.GetSpec("my-app")
	if err == nil {
		t.Errorf("expected error getting deleted spec, got nil")
	}
}

func TestLegacySpecWithoutSchemaVersionMigratesToManaged(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	// Simulate a spec file written by the pre-hardening prototype: no
	// schemaVersion, no managed field at all (both post-date this pass).
	// Every such file was, in practice, written for a container that DID
	// carry io.podder.managed=true — the prototype applied that label
	// unconditionally.
	legacyJSON := `{
		"name": "legacy-app",
		"image": "docker.io/library/nginx:alpine",
		"portMappings": [{"hostIP":"127.0.0.1","hostPort":8080,"containerPort":80,"protocol":"tcp"}],
		"command": "nginx -g \"daemon off;\""
	}`

	svc := &PodmanService{}
	servicesDir := getServicesDir()
	if err := os.MkdirAll(servicesDir, 0o700); err != nil {
		t.Fatalf("failed to create services dir: %v", err)
	}
	if err := os.WriteFile(getSpecFilePath("legacy-app"), []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("failed to write legacy fixture: %v", err)
	}

	spec, err := svc.GetSpec("legacy-app")
	if err != nil {
		t.Fatalf("unexpected error loading legacy spec: %v", err)
	}
	if !spec.Managed {
		t.Fatalf("expected a legacy (pre-schemaVersion) spec to migrate to Managed=true, since the prototype applied io.podder.managed=true unconditionally; got Managed=false — replaying this spec would silently strip the managed label from a container that currently carries it")
	}
	if spec.SchemaVersion != CurrentSpecSchemaVersion {
		t.Errorf("expected migrated schema version %d, got %d", CurrentSpecSchemaVersion, spec.SchemaVersion)
	}

	// BuildRunArgsFromSpec on the migrated spec must therefore still apply
	// the managed label.
	args, err := BuildRunArgsFromSpec(*spec)
	if err != nil {
		t.Fatalf("unexpected error building args: %v", err)
	}
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--label" && args[i+1] == "io.podder.managed=true" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected migrated legacy spec to still produce io.podder.managed=true, got args: %v", args)
	}

	// A spec written by the CURRENT code (SchemaVersion already set) must
	// NOT be force-migrated — its explicit Managed value is authoritative.
	if err := svc.SaveSpec(ContainerSpec{Name: "explicit-unmanaged", Image: "alpine", Managed: false}); err != nil {
		t.Fatalf("failed to save spec: %v", err)
	}
	explicit, err := svc.GetSpec("explicit-unmanaged")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if explicit.Managed {
		t.Errorf("expected an explicitly-unmanaged current-schema spec to stay unmanaged, got Managed=true")
	}
}

func TestSpecFilePermissionsAreRestrictive(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	service := &PodmanService{}
	spec := ContainerSpec{Name: "secret-app", Image: "alpine", Env: map[string]string{"TOKEN": "s3cr3t"}}
	if err := service.SaveSpec(spec); err != nil {
		t.Fatalf("failed to save spec: %v", err)
	}

	servicesDir := getServicesDir()
	dirInfo, err := os.Stat(servicesDir)
	if err != nil {
		t.Fatalf("failed to stat services dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("expected services dir mode 0700, got %o", perm)
	}

	fileInfo, err := os.Stat(getSpecFilePath("secret-app"))
	if err != nil {
		t.Fatalf("failed to stat spec file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected spec file mode 0600, got %o", perm)
	}
}

func TestCandidateSpecCommitAndDiscard(t *testing.T) {
	tempDir := t.TempDir()
	origHome := os.Getenv("HOME")
	defer os.Setenv("HOME", origHome)
	os.Setenv("HOME", tempDir)

	spec := ContainerSpec{Name: "candidate-app", Image: "alpine"}
	candidatePath, err := writeCandidateSpec(spec)
	if err != nil {
		t.Fatalf("failed to write candidate spec: %v", err)
	}

	info, err := os.Stat(candidatePath)
	if err != nil {
		t.Fatalf("candidate spec file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected candidate spec mode 0600, got %o", perm)
	}

	// A candidate must never be visible to ListSpecs/GetSpec before commit.
	svc := &PodmanService{}
	if _, err := svc.GetSpec("candidate-app"); err == nil {
		t.Errorf("expected candidate spec to not be authoritative before commit")
	}
	specs, err := svc.ListSpecs()
	if err != nil {
		t.Fatalf("unexpected error listing specs: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("expected ListSpecs to ignore uncommitted candidates, got %d", len(specs))
	}

	if err := commitCandidateSpec(candidatePath, spec); err != nil {
		t.Fatalf("failed to commit candidate spec: %v", err)
	}
	if _, err := svc.GetSpec("candidate-app"); err != nil {
		t.Errorf("expected committed spec to be authoritative: %v", err)
	}

	// discardCandidateSpec on an already-committed (moved) path must not panic.
	discardCandidateSpec("")
}

func TestBuildRunArgsFromSpecWithPodderLabels(t *testing.T) {
	spec := ContainerSpec{
		Name:    "flowise-service",
		Image:   "alpine:latest",
		Managed: true,
		PortMappings: []PortMapping{
			{HostIP: "127.0.0.1", HostPort: 3000, ContainerPort: 3000, Protocol: "tcp"},
		},
	}

	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasManagedLabel := false
	hasServiceLabel := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--label" && args[i+1] == "io.podder.managed=true" {
			hasManagedLabel = true
		}
		if args[i] == "--label" && args[i+1] == "io.podder.service=flowise-service" {
			hasServiceLabel = true
		}
	}

	if !hasManagedLabel || !hasServiceLabel {
		t.Errorf("expected Podder labels in args: %v", args)
	}
}

// --- Item 1: DeploySpec transactional behavior ---
// Reuses the mutationSim harness (mutations_test.go): a scripted fake
// CommandRunner that simulates enough of Podman's container lifecycle
// (ps/rename/stop/start/rm/run/create) to exercise DeploySpec's full
// transaction, including every failure point, deterministically.

func TestDeploySpec_FreshDeployNoExistingContainer(t *testing.T) {
	sim := newMutationSim()
	withTestHome(t)
	withFastPolling(t)
	svc := &PodmanService{runner: sim}

	spec := ContainerSpec{
		Name:  "fresh",
		Image: "alpine:latest",
		PortMappings: []PortMapping{
			{HostIP: "127.0.0.1", HostPort: 9001, ContainerPort: 80, Protocol: "tcp"},
		},
	}
	if err := svc.SaveSpec(spec); err != nil {
		t.Fatalf("failed to seed spec: %v", err)
	}

	result, err := svc.DeploySpec("fresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected successful deploy, steps: %+v", result.Steps)
	}
	if result.Replaced {
		t.Errorf("expected Replaced=false when there was no existing container")
	}
	if result.ContainerID == "" {
		t.Errorf("expected a container ID to be reported")
	}

	c, ok := sim.containers["fresh"]
	if !ok || c.state != "running" {
		t.Fatalf("expected container 'fresh' to exist and be running: %+v", sim.containers)
	}
}

func TestDeploySpec_ReplaceExistingSucceedsAndPreservesLifecycle(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "web", "nginx:alpine", "running", oldPorts)

	// Update the stored spec (e.g. changed ports/env) before redeploying.
	newSpec := ContainerSpec{
		Name:         "web",
		Image:        "nginx:alpine",
		Managed:      true,
		PortMappings: []PortMapping{{HostIP: "127.0.0.1", HostPort: 9091, ContainerPort: 80, Protocol: "tcp"}},
		Env:          map[string]string{"FOO": "baz"},
	}
	if err := svc.SaveSpec(newSpec); err != nil {
		t.Fatalf("failed to update spec: %v", err)
	}

	result, err := svc.DeploySpec("web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected successful deploy, steps: %+v", result.Steps)
	}
	if !result.Replaced {
		t.Errorf("expected Replaced=true when an existing container occupied the name")
	}
	if !result.ConfigurationVerified {
		t.Errorf("expected ConfigurationVerified=true")
	}

	c, ok := sim.containers["web"]
	if !ok {
		t.Fatalf("expected replacement container 'web' to exist")
	}
	if c.state != "running" {
		t.Errorf("expected replacement to remain running (original was running), got %s", c.state)
	}
	if len(c.ports) != 1 || c.ports[0].HostPort != 9091 {
		t.Errorf("expected new port 9091 configured, got %+v", c.ports)
	}

	// The old backup must be gone (COMMIT removed it) — only one "web".
	if len(sim.containers) != 1 {
		t.Errorf("expected exactly 1 container after commit (backup removed), got %d: %+v", len(sim.containers), sim.containers)
	}
}

func TestDeploySpec_StoppedContainerStaysStoppedAndNotAutoStarted(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "worker", "alpine", "exited", oldPorts)

	result, err := svc.DeploySpec("worker")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected successful deploy, steps: %+v", result.Steps)
	}

	c := sim.containers["worker"]
	if c == nil {
		t.Fatalf("expected replacement container to exist")
	}
	if c.state != "exited" {
		t.Errorf("expected replacement to remain stopped (never auto-started), got state %q", c.state)
	}
}

// TestDeploySpec_ReplacementFailureRestoresOriginal covers item 27's
// "DeploySpec replacement failure restores original": if recreation
// (podman run/create) fails after the original was quiesced, the original
// container must come back exactly as it was, verified — not just "an
// error was returned".
func TestDeploySpec_ReplacementFailureRestoresOriginal(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "api", "alpine", "running", oldPorts)
	sim.failStep = "run"

	result, err := svc.DeploySpec("api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected deploy to fail when recreation fails")
	}
	if !result.RolledBack {
		t.Fatalf("expected verified rollback, result: %+v", result)
	}
	if result.Rollback == nil || !result.Rollback.Verified {
		t.Fatalf("expected verified rollback result, got: %+v", result.Rollback)
	}
	if result.ManualRecoveryRequired {
		t.Errorf("expected no manual recovery when rollback succeeds")
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
	if len(sim.containers) != 1 {
		t.Errorf("expected exactly 1 container after rollback (no orphaned backup), got %d: %+v", len(sim.containers), sim.containers)
	}
}

// TestDeploySpec_StopFailureDuringQuiesceDoesNotDestroyState covers item
// 27's "DeploySpec stop/remove failure doesn't destroy state": the old,
// pre-transactional DeploySpec ignored stop/remove errors entirely and
// pressed on to create a replacement regardless. The transactional version
// must instead roll back to a verified, intact original.
func TestDeploySpec_StopFailureDuringQuiesceDoesNotDestroyState(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "db", "alpine", "running", oldPorts)
	sim.failStep = "stop"

	result, err := svc.DeploySpec("db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected deploy to fail when stop fails during quiesce")
	}
	if !result.RolledBack {
		t.Fatalf("expected verified rollback when stop fails, result: %+v", result)
	}
	if result.ManualRecoveryRequired {
		t.Errorf("expected no manual recovery when rollback succeeds")
	}

	c := sim.containers["db"]
	if c == nil {
		t.Fatalf("expected original container to still exist — state must not be destroyed")
	}
	if c.state != "running" {
		t.Errorf("expected original container's running state preserved, got %s", c.state)
	}
	if len(c.ports) != 1 || c.ports[0].HostPort != 8080 {
		t.Errorf("expected original port mapping intact, got %+v", c.ports)
	}
}

// TestDeploySpec_RenameFailureLeavesOriginalUntouched covers item 23: if
// the very first QUIESCE step (the rename) fails, nothing was ever moved —
// this must be reported directly, WITHOUT attempting (and spuriously
// failing) a rollback that has no backup to restore from.
func TestDeploySpec_RenameFailureLeavesOriginalUntouched(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "cache", "alpine", "running", oldPorts)
	sim.failStep = "rename"

	result, err := svc.DeploySpec("cache")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected deploy to fail when the initial rename fails")
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

func TestDeploySpec_VerifyFailureOnMappingMismatchRollsBack(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "svc", "alpine", "running", oldPorts)

	wrapper := &mismatchInjector{sim: sim}
	svc.runner = wrapper

	newSpec := ContainerSpec{
		Name:         "svc",
		Image:        "alpine",
		Managed:      true,
		PortMappings: []PortMapping{{HostIP: "127.0.0.1", HostPort: 8099, ContainerPort: 80, Protocol: "tcp"}},
	}
	if err := svc.SaveSpec(newSpec); err != nil {
		t.Fatalf("failed to update spec: %v", err)
	}

	result, err := svc.DeploySpec("svc")
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

func TestDeploySpec_UnsupportedLifecycleBlocked(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "paused-app", "alpine", "paused", oldPorts)

	result, err := svc.DeploySpec("paused-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected an unsupported lifecycle state to block deployment")
	}
	if result.Rollback != nil {
		t.Errorf("expected no rollback attempt: nothing was touched during preflight")
	}
	c := sim.containers["paused-app"]
	if c == nil || c.state != "paused" {
		t.Errorf("expected the original container to be completely untouched, got: %+v", c)
	}
}

func TestDeploySpec_InvalidStoredSpecBlocked(t *testing.T) {
	withTestHome(t)
	svc := &PodmanService{runner: newMutationSim()}
	// SaveSpec only checks for a non-empty name/image; a spec whose
	// SchemaVersion is newer than this build supports still saves fine but
	// must be rejected by DeploySpec's own ValidateSpec-based preflight —
	// never guessed at or partially replayed.
	spec := ContainerSpec{Name: "broken", Image: "alpine", SchemaVersion: CurrentSpecSchemaVersion + 1}
	if err := svc.SaveSpec(spec); err != nil {
		t.Fatalf("failed to save fixture spec: %v", err)
	}

	result, err := svc.DeploySpec("broken")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected an invalid stored spec to block deployment")
	}
}

func TestDeploySpec_FreshDeployVerifyFailureRemovesContainer(t *testing.T) {
	sim := newMutationSim()
	withTestHome(t)
	withFastPolling(t)
	svc := &PodmanService{runner: sim}

	// A spec with no host port at all so we can force a mismatch by
	// corrupting the freshly-created container's reported ports.
	spec := ContainerSpec{
		Name:         "newsvc",
		Image:        "alpine",
		Managed:      true,
		PortMappings: []PortMapping{{HostIP: "127.0.0.1", HostPort: 7000, ContainerPort: 80, Protocol: "tcp"}},
	}
	if err := svc.SaveSpec(spec); err != nil {
		t.Fatalf("failed to seed spec: %v", err)
	}

	wrapper := &mismatchInjector{sim: sim}
	svc.runner = wrapper

	result, err := svc.DeploySpec("newsvc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected verify failure to fail the deployment")
	}
	if result.ManualRecoveryRequired {
		t.Errorf("expected removal of the unverified container to succeed, not require manual recovery")
	}
	if _, ok := sim.containers["newsvc"]; ok {
		t.Errorf("expected the unverified new container to have been removed")
	}
}

// removalFailingMismatchInjector corrupts a freshly-created container's
// ports (like mismatchInjector) AND makes the compensating `podman rm` fail,
// to exercise "verification failed, and cleanup itself failed too".
type removalFailingMismatchInjector struct {
	sim       *mutationSim
	corrupted bool
}

func (m *removalFailingMismatchInjector) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && args[0] == "rm" {
		return "", "simulated rm failure", fmt.Errorf("simulated rm failure")
	}
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

func TestDeploySpec_FreshDeployCleanupFailureReportsManualRecovery(t *testing.T) {
	sim := newMutationSim()
	withTestHome(t)
	withFastPolling(t)
	svc := &PodmanService{runner: sim}

	spec := ContainerSpec{
		Name:         "newsvc2",
		Image:        "alpine",
		Managed:      true,
		PortMappings: []PortMapping{{HostIP: "127.0.0.1", HostPort: 7001, ContainerPort: 80, Protocol: "tcp"}},
	}
	if err := svc.SaveSpec(spec); err != nil {
		t.Fatalf("failed to seed spec: %v", err)
	}

	svc.runner = &removalFailingMismatchInjector{sim: sim}

	result, err := svc.DeploySpec("newsvc2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected verify failure to fail the deployment")
	}
	if !result.ManualRecoveryRequired {
		t.Fatalf("expected ManualRecoveryRequired=true when cleanup of the unverified container itself fails")
	}
	if result.ContainerID == "" {
		t.Errorf("expected the surviving container's ID to be exposed for manual recovery")
	}
}

// TestDeploySpec_CommitCleanupWarningDoesNotContradictSuccess covers item
// 25: a failed best-effort backup removal after a successful commit must
// be reported as a distinct, non-contradictory warning — never a
// COMMITTED:false step immediately followed by a COMMITTED:true one.
func TestDeploySpec_CommitCleanupWarningDoesNotContradictSuccess(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	svc := setupManagedContainer(t, sim, "cleanup-app", "alpine", "running", oldPorts)

	svc.runner = &rmFailsOnceRunner{sim: sim}

	newSpec := ContainerSpec{
		Name:         "cleanup-app",
		Image:        "alpine",
		Managed:      true,
		PortMappings: []PortMapping{{HostIP: "127.0.0.1", HostPort: 9099, ContainerPort: 80, Protocol: "tcp"}},
	}
	if err := svc.SaveSpec(newSpec); err != nil {
		t.Fatalf("failed to update spec: %v", err)
	}

	result, err := svc.DeploySpec("cleanup-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected the deployment itself to succeed despite the cleanup failure, steps: %+v", result.Steps)
	}
	if result.CleanupWarning == "" {
		t.Errorf("expected a CleanupWarning to be reported")
	}

	sawCommittedFalse := false
	sawCommittedTrue := false
	for _, s := range result.Steps {
		if s.Step == "COMMITTED" {
			if s.Passed {
				sawCommittedTrue = true
			} else {
				sawCommittedFalse = true
			}
		}
	}
	if !sawCommittedTrue {
		t.Errorf("expected a COMMITTED:true step, got: %+v", result.Steps)
	}
	if sawCommittedFalse {
		t.Errorf("expected no contradictory COMMITTED:false step alongside COMMITTED:true, got: %+v", result.Steps)
	}
}

// rmFailsOnceRunner fails exactly one `podman rm` call (the backup cleanup
// after a successful VERIFY), leaving everything else to the sim.
type rmFailsOnceRunner struct {
	sim    *mutationSim
	failed bool
}

func (r *rmFailsOnceRunner) Run(name string, args ...string) (string, string, error) {
	if !r.failed && name == "podman" && len(args) > 0 && args[0] == "rm" {
		r.failed = true
		return "", "simulated rm failure", fmt.Errorf("simulated rm failure")
	}
	return r.sim.Run(name, args...)
}

func TestBuildRunArgsFromSpecUnmanagedHasNoLabels(t *testing.T) {
	spec := ContainerSpec{
		Name:  "scratch",
		Image: "alpine:latest",
		PortMappings: []PortMapping{
			{HostPort: 3001, ContainerPort: 3000, Protocol: "tcp"},
		},
	}

	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--label" {
			t.Errorf("expected no Podder labels for an unmanaged spec, got: %v", args)
		}
	}
}
