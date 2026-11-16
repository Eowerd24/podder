package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalBindApprovalRejectsReplacementAndPinsApprovedObject(t *testing.T) {
	p, r, spec := sessionFixture(t)
	spec.Config = SessionMount{}
	spec.Data = SessionMount{}
	fixture := t.TempDir()
	approved := filepath.Join(fixture, "approved")
	moved := filepath.Join(fixture, "approved-moved")
	outside := filepath.Join(fixture, "outside")
	if err := os.Mkdir(approved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("outside-safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec.AllowExternalBinds = true
	spec.ExternalBinds = []BindMountSpec{{HostPath: approved, ContainerPath: "/approved", ReadOnly: true}}

	preview, err := p.PreviewDisposableSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	launch := spec
	launch.LaunchID = preview.Spec.LaunchID
	launch.ExternalBinds = preview.Spec.ExternalBinds
	launch.ExternalBindApprovals = preview.Spec.ExternalBindApprovals
	if err := os.Rename(approved, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, approved); err != nil {
		t.Fatal(err)
	}
	if _, err := p.LaunchDisposableSession(launch); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("path replacement was not rejected: %v", err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "podman run") {
			t.Fatalf("container ran after approval object changed: %s", call)
		}
	}
	if err := os.Remove(approved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, approved); err != nil {
		t.Fatal(err)
	}

	preview, err = p.PreviewDisposableSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	launch.LaunchID = preview.Spec.LaunchID
	launch.ExternalBinds = preview.Spec.ExternalBinds
	launch.ExternalBindApprovals = preview.Spec.ExternalBindApprovals
	p.sessionMountsPinnedHook = func() {
		if err := os.Rename(approved, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, approved); err != nil {
			t.Fatal(err)
		}
	}
	state, err := p.LaunchDisposableSession(launch)
	if err != nil || state.State != "running" {
		t.Fatalf("approved object did not survive final path swap: %+v %v", state, err)
	}
	foundPinnedMount := false
	for _, call := range r.calls {
		if strings.Contains(call, "podman run") && strings.Contains(call, "src=/proc/") && strings.Contains(call, ",target=/approved,readonly") {
			foundPinnedMount = true
		}
	}
	if !foundPinnedMount {
		t.Fatalf("run did not use a descriptor-backed mount: %v", r.calls)
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "outside-safe" {
		t.Fatalf("outside sentinel changed: %q %v", contents, err)
	}
}

func TestStableExternalSymlinkCanonicalizesAndLaunches(t *testing.T) {
	p, _, spec := sessionFixture(t)
	spec.Config = SessionMount{}
	spec.Data = SessionMount{}
	fixture := t.TempDir()
	target := filepath.Join(fixture, "target")
	link := filepath.Join(fixture, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	spec.AllowExternalBinds = true
	spec.ExternalBinds = []BindMountSpec{{HostPath: link, ContainerPath: "/stable", ReadOnly: true}}
	preview, err := p.PreviewDisposableSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Spec.ExternalBinds) != 1 || preview.Spec.ExternalBinds[0].HostPath != target {
		t.Fatalf("stable symlink was not canonicalized: %+v", preview.Spec.ExternalBinds)
	}
	spec.LaunchID = preview.Spec.LaunchID
	spec.ExternalBinds = preview.Spec.ExternalBinds
	spec.ExternalBindApprovals = preview.Spec.ExternalBindApprovals
	state, err := p.LaunchDisposableSession(spec)
	if err != nil || state.State != "running" {
		t.Fatalf("unchanged stable symlink target did not launch: %+v %v", state, err)
	}
}

func TestGeneratedMountPinsDirectoryAcrossAncestorReplacement(t *testing.T) {
	p, r, spec := sessionFixture(t)
	spec.Data = SessionMount{}
	preview, err := p.PreviewDisposableSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.LaunchID = preview.Spec.LaunchID
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(sessionRuntimeDir(), spec.LaunchID, "config")
	moved := config + "-moved"
	p.sessionMountsPinnedHook = func() {
		if err := os.Rename(config, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, config); err != nil {
			t.Fatal(err)
		}
	}
	state, err := p.LaunchDisposableSession(spec)
	if err != nil || state.State != "running" {
		t.Fatalf("generated mount launch failed after deterministic swap: %+v %v", state, err)
	}
	foundPinnedMount := false
	for _, call := range r.calls {
		if strings.Contains(call, "podman run") && strings.Contains(call, "src=/proc/") && strings.Contains(call, ",target=/app/config,readonly") {
			foundPinnedMount = true
		}
	}
	if !foundPinnedMount {
		t.Fatalf("generated mount was not descriptor-backed: %v", r.calls)
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "preserved" {
		t.Fatalf("outside sentinel changed: %q %v", contents, err)
	}
}

func TestPrivateDirectoryTraversalRejectsSymlinkAtEveryControlledComponent(t *testing.T) {
	for symlinkIndex := 0; symlinkIndex < 3; symlinkIndex++ {
		t.Run(fmt.Sprintf("component-%d", symlinkIndex), func(t *testing.T) {
			fixture := t.TempDir()
			outside := filepath.Join(fixture, "outside")
			if err := os.Mkdir(outside, 0o750); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, []byte("safe"), 0o640); err != nil {
				t.Fatal(err)
			}
			components := []string{"one", "two", "three"}
			current := fixture
			for i, component := range components {
				current = filepath.Join(current, component)
				if i == symlinkIndex {
					if err := os.Symlink(outside, current); err != nil {
						t.Fatal(err)
					}
					break
				}
				if err := os.Mkdir(current, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(fixture, filepath.Join(components...))
			if root, err := privateRoot(target); err == nil {
				root.Close()
				t.Fatal("private traversal followed a symlink")
			}
			contents, err := os.ReadFile(sentinel)
			if err != nil || string(contents) != "safe" {
				t.Fatalf("outside sentinel changed: %q %v", contents, err)
			}
			info, err := os.Stat(outside)
			if err != nil || info.Mode().Perm() != 0o750 {
				t.Fatalf("outside permissions changed: %v %v", info, err)
			}
		})
	}
}

func TestEveryPrivateStorageFamilyRejectsSymlinkRoot(t *testing.T) {
	fixture := t.TempDir()
	configHome := filepath.Join(fixture, "config")
	dataHome := filepath.Join(fixture, "data")
	runtimeHome := filepath.Join(fixture, "runtime")
	for _, path := range []string{configHome, dataHome, runtimeHome} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_RUNTIME_DIR", runtimeHome)
	if err := os.Mkdir(getConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sessionDataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sessionRuntimeDir()), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(fixture, "outside")
	if err := os.Mkdir(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o640); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		"services":             getServicesDir(),
		"managed-transactions": filepath.Join(getConfigDir(), "managed-transactions"),
		"recipes":              filepath.Join(getConfigDir(), "sessions"),
		"launchers":            filepath.Join(getConfigDir(), "launchers"),
		"session-state":        filepath.Join(sessionDataDir(), "session-state"),
		"environments":         filepath.Join(getConfigDir(), "environments"),
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			if root, err := privateRoot(path); err == nil {
				root.Close()
				t.Fatal("private storage accepted a symlink root")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		})
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "safe" {
		t.Fatalf("outside sentinel changed: %q %v", contents, err)
	}
	info, err := os.Stat(outside)
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("outside permissions changed: %v %v", info, err)
	}
}

func TestSessionWorkspaceCleanupQuarantinesAndRejectsObjectSubstitution(t *testing.T) {
	withTestHome(t)
	p := &PodmanService{}
	state := SessionRuntimeState{
		SchemaVersion: sessionSchema, ID: newSessionID(), Token: newSessionID(),
		Spec: DisposableSessionSpec{SchemaVersion: sessionSchema, Name: "cleanup-substitution", Policy: "temporary"},
	}
	binds, err := prepareSessionWorkspace(&state)
	if err != nil {
		t.Fatal(err)
	}
	closeSessionBindSources(binds)
	runtimeDir := sessionRuntimeDir()
	original := filepath.Join(runtimeDir, state.ID)
	moved := original + "-moved"
	replacement := filepath.Join(runtimeDir, "replacement-"+newSessionID())
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(replacement, "sentinel")
	if err := os.WriteFile(sentinel, []byte("replacement-preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.sessionWorkspaceCleanupHook = func() {
		if err := os.Rename(original, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, original); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.removeSessionWorkspace(state); err == nil || !strings.Contains(err.Error(), "changed before cleanup") {
		t.Fatalf("workspace substitution was not refused: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(original, "sentinel"))
	if err != nil || string(contents) != "replacement-preserved" {
		t.Fatalf("substituted workspace was deleted or changed: %q %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(moved, "owner.json")); err != nil {
		t.Fatalf("original workspace was not preserved: %v", err)
	}
}

func TestConfigDataAndRuntimeRootsRejectSymlinkAncestors(t *testing.T) {
	for _, rootKind := range []string{"config", "data", "runtime"} {
		t.Run(rootKind, func(t *testing.T) {
			fixture := t.TempDir()
			configHome := filepath.Join(fixture, "config")
			dataHome := filepath.Join(fixture, "data")
			runtimeHome := filepath.Join(fixture, "runtime")
			for _, path := range []string{configHome, dataHome, runtimeHome} {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("XDG_CONFIG_HOME", configHome)
			t.Setenv("XDG_DATA_HOME", dataHome)
			t.Setenv("XDG_RUNTIME_DIR", runtimeHome)
			outside := filepath.Join(fixture, "outside")
			if err := os.Mkdir(outside, 0o750); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, []byte("safe"), 0o640); err != nil {
				t.Fatal(err)
			}
			var protected string
			switch rootKind {
			case "config":
				protected = getConfigDir()
			case "data":
				protected = sessionDataDir()
			case "runtime":
				protected = filepath.Join(runtimeHome, "podder")
			}
			if err := os.Symlink(outside, protected); err != nil {
				t.Fatal(err)
			}
			var err error
			switch rootKind {
			case "config":
				err = (&PodmanService{}).SaveSettings(AppSettings{})
			case "data":
				var root *os.Root
				root, err = manifestRoot()
				if root != nil {
					root.Close()
				}
			case "runtime":
				var root *os.Root
				root, err = environmentRoot()
				if root != nil {
					root.Close()
				}
			}
			if err == nil {
				t.Fatal("private root followed a symlink ancestor")
			}
			contents, readErr := os.ReadFile(sentinel)
			if readErr != nil || string(contents) != "safe" {
				t.Fatalf("outside sentinel changed: %q %v", contents, readErr)
			}
			info, statErr := os.Stat(outside)
			if statErr != nil || info.Mode().Perm() != 0o750 {
				t.Fatalf("outside permissions changed: %v %v", info, statErr)
			}
		})
	}
}

func TestExternalBindsCannotOverlapPodderPrivateRoots(t *testing.T) {
	_, _, spec := sessionFixture(t)
	private := getConfigDir()
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	spec.AllowExternalBinds = true
	spec.ExternalBinds = []BindMountSpec{{HostPath: filepath.Dir(private), ContainerPath: "/host"}}
	if _, err := (&PodmanService{}).PreviewDisposableSession(spec); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("private-root ancestor bind was accepted: %v", err)
	}
}

func TestManagedAndAdoptedBindsCannotExposePodderPrivateRoots(t *testing.T) {
	withTestHome(t)
	privateAncestor := filepath.Dir(getConfigDir())
	spec := ContainerSpec{SchemaVersion: CurrentSpecSchemaVersion, Image: "fixture", Binds: []BindMountSpec{{HostPath: privateAncestor, ContainerPath: "/host"}}}
	if errs := ValidateSpec(spec); !strings.Contains(strings.Join(errs, ";"), "protected Podder private storage") {
		t.Fatalf("ordinary container bind was not rejected: %v", errs)
	}
	mount := fmt.Sprintf(`{"Type":"bind","Source":%q,"Destination":"/host","RW":true}`, privateAncestor)
	assessment, err := ParseInspectToAssessment([]byte(baseInspectJSON("", "", `,"Binds":["`+privateAncestor+`:/host"]`, mount, "")))
	if err != nil {
		t.Fatal(err)
	}
	if assessment.CanAdopt || !strings.Contains(strings.Join(assessment.Blockers, ";"), "blocked before mutation") {
		t.Fatalf("adoption did not default-deny the protected bind: %+v", assessment)
	}
	if err := os.MkdirAll(getConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "private-alias")
	if err := os.Symlink(getConfigDir(), alias); err != nil {
		t.Fatal(err)
	}
	aliased := ContainerSpec{Binds: []BindMountSpec{{HostPath: alias, ContainerPath: "/host"}}}
	if err := approveContainerSpecBinds(&aliased); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("symlink alias to private storage was accepted: %v", err)
	}

	outside := t.TempDir()
	spec.Binds[0].HostPath = outside
	if errs := ValidateSpec(spec); len(errs) != 0 {
		t.Fatalf("ordinary outside bind positive control failed: %v", errs)
	}
}

func TestManagedBindPinsApprovedObjectAcrossDeterministicSwap(t *testing.T) {
	p, runner, _ := sessionFixture(t)
	fixture := t.TempDir()
	approved := filepath.Join(fixture, "approved")
	moved := filepath.Join(fixture, "approved-moved")
	replacement := filepath.Join(fixture, "replacement")
	if err := os.Mkdir(approved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(replacement, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserved"), 0o640); err != nil {
		t.Fatal(err)
	}
	spec := ContainerSpec{SchemaVersion: CurrentSpecSchemaVersion, Image: "fixture", Name: "pinned-managed", Binds: []BindMountSpec{{HostPath: approved, ContainerPath: "/data", ReadOnly: true}}}
	if err := approveContainerSpecBinds(&spec); err != nil {
		t.Fatal(err)
	}
	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	p.sessionMountsPinnedHook = func() {
		if err := os.Rename(approved, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, approved); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := p.runSpecCommand(spec, args...); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range runner.calls {
		if strings.Contains(call, "podman run") && strings.Contains(call, "src=/proc/") && strings.Contains(call, ",target=/data,readonly") {
			found = true
		}
	}
	if !found {
		t.Fatalf("managed bind did not use the pinned descriptor: %v", runner.calls)
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "preserved" {
		t.Fatalf("replacement sentinel changed: %q %v", contents, err)
	}
}

func TestDeployRefusesMissingOrReplacedStoredBindWitness(t *testing.T) {
	withTestHome(t)
	fixture := t.TempDir()
	approved := filepath.Join(fixture, "approved")
	moved := filepath.Join(fixture, "approved-moved")
	replacement := filepath.Join(fixture, "replacement")
	if err := os.Mkdir(approved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	writeStored := func(t *testing.T, spec ContainerSpec) {
		t.Helper()
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		path, err := getSpecFilePath(spec.Name)
		if err != nil {
			t.Fatal(err)
		}
		if err := writePrivateFileAtomic(path, data); err != nil {
			t.Fatal(err)
		}
	}
	base := ContainerSpec{
		SchemaVersion: CurrentSpecSchemaVersion, Name: "stored-bind", Image: "fixture",
		ResolvedImage: strings.Repeat("a", 64), ReplayComplete: true, Managed: true,
		Binds: []BindMountSpec{{HostPath: approved, ContainerPath: "/external", ReadOnly: true}},
	}
	t.Run("missing-witness", func(t *testing.T) {
		writeStored(t, base)
		r := newFakeCommandRunner()
		p := &PodmanService{runner: r}
		if _, err := p.DeploySpec(base.Name); err == nil || !strings.Contains(err.Error(), "no filesystem-object approval witness") {
			t.Fatalf("legacy stored bind was not refused: %v", err)
		}
		if len(r.CallsMatching("podman run")) != 0 || len(r.CallsMatching("podman create")) != 0 {
			t.Fatalf("legacy refusal mutated runtime: %v", r.calls)
		}
	})
	t.Run("replaced-object", func(t *testing.T) {
		spec := base
		if err := approveContainerSpecBinds(&spec); err != nil {
			t.Fatal(err)
		}
		writeStored(t, spec)
		if err := os.Rename(approved, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, approved); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = os.Remove(approved)
			_ = os.Rename(moved, approved)
		}()
		r := newFakeCommandRunner()
		p := &PodmanService{runner: r}
		if _, err := p.DeploySpec(spec.Name); err == nil || !strings.Contains(err.Error(), "filesystem object changed") {
			t.Fatalf("replaced stored bind was not refused: %v", err)
		}
		if len(r.CallsMatching("podman run")) != 0 || len(r.CallsMatching("podman create")) != 0 {
			t.Fatalf("replacement refusal mutated runtime: %v", r.calls)
		}
	})
}

func TestLegacyAdoptionJournalWithoutAcceptedSnapshotStaysManual(t *testing.T) {
	sim := newMutationSim()
	p := setupManagedContainer(t, sim, "legacy-adopt", "fixture", "running", nil)
	containers, err := p.ListContainers(true)
	if err != nil || len(containers) != 1 {
		t.Fatalf("original snapshot: %+v %v", containers, err)
	}
	spec, err := p.GetSpec("legacy-adopt")
	if err != nil {
		t.Fatal(err)
	}
	journal, err := p.beginManagedJournal("legacy-adopt", "adopt", "legacy-adopt-backup", "", &containers[0], *spec)
	if err != nil {
		t.Fatal(err)
	}
	p.finishManagedJournal(journal)
	if _, err := loadManagedJournal(journal.Service); err != nil {
		t.Fatalf("legacy adoption receipt was retired without an accepted snapshot: %v", err)
	}
	if err := p.RecoverManagedTransaction(journal.Service, true); err == nil || !strings.Contains(err.Error(), "lacks the accepted original inspect snapshot") {
		t.Fatalf("legacy adoption recovery did not fail closed: %v", err)
	}
	original := sim.containers["legacy-adopt"]
	if original == nil || original.id != containers[0].Id || original.state != "running" || len(sim.containers) != 1 {
		t.Fatalf("manual-only recovery mutated the original: %+v", sim.containers)
	}
}

func TestSessionOwnedImageRemovalUsesFullImmutableIDAndRetaggingRetains(t *testing.T) {
	newState := func(t *testing.T, p *PodmanService, r *sessionTestRunner) *SessionRuntimeState {
		t.Helper()
		id := newSessionID()
		r.imageTag = "localhost/podder-session:" + id
		r.buildImage = "sha256:" + strings.Repeat("f", 64)
		runtime, err := p.sessionRuntimeIdentity()
		if err != nil {
			t.Fatal(err)
		}
		return &SessionRuntimeState{RuntimeIdentity: runtime, ID: id, ImageOwned: true, ImageTag: r.imageTag, ImageID: r.buildImage}
	}

	t.Run("positive-full-id", func(t *testing.T) {
		p, r, _ := sessionFixture(t)
		state := newState(t, p, r)
		if err := p.cleanupSessionImage(state); err != nil {
			t.Fatal(err)
		}
		want := "podman image rm -- sha256:" + strings.Repeat("f", 64)
		found := false
		for _, call := range r.calls {
			if call == want {
				found = true
			}
			if strings.Contains(call, "image rm") && strings.Contains(call, state.ImageTag) {
				t.Fatalf("mutable tag used for deletion: %s", call)
			}
		}
		if !found {
			t.Fatalf("exact full image ID was not used: %v", r.calls)
		}
	})

	t.Run("retag-at-barrier", func(t *testing.T) {
		p, r, _ := sessionFixture(t)
		state := newState(t, p, r)
		p.imageCleanupVerifiedHook = func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.buildImage = "sha256:" + strings.Repeat("e", 64)
		}
		if err := p.cleanupSessionImage(state); err == nil || !strings.Contains(err.Error(), "retained") {
			t.Fatalf("retagging did not retain the image: %v", err)
		}
		for _, call := range r.calls {
			if strings.Contains(call, "image rm") {
				t.Fatalf("image deletion ran after retagging: %s", call)
			}
		}
	})
}

func TestReplacementOnlyUnknownInspectFieldsAlwaysBlock(t *testing.T) {
	accepted, err := parseSingleInspect(baseInspectJSON("", "", "", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"zero-looking": "false", "nonzero": "true"} {
		t.Run(name, func(t *testing.T) {
			replacement, err := parseSingleInspect(baseInspectJSON("", "", `,"FutureReplacementOnly":`+value, "", ""))
			if err != nil {
				t.Fatal(err)
			}
			replacement.Id = accepted.Id
			if blockers := inspectPolicyBlockers(replacement); len(blockers) == 0 || !strings.Contains(strings.Join(blockers, ";"), "FutureReplacementOnly") {
				t.Fatalf("replacement-only unknown field was accepted: %v", blockers)
			}
		})
	}
}

type replacementUnknownRunner struct {
	base  *adoptionInspectRouter
	value any
}

func (r *replacementUnknownRunner) Run(name string, args ...string) (string, string, error) {
	out, stderr, err := r.base.Run(name, args...)
	if err != nil || name != "podman" || len(args) == 0 || args[0] != "inspect" {
		return out, stderr, err
	}
	id := args[len(args)-1]
	r.base.sim.mu.Lock()
	managed := false
	for containerName, container := range r.base.sim.containers {
		if containerName == id || container.id == id {
			managed = container.managed
			break
		}
	}
	r.base.sim.mu.Unlock()
	if !managed {
		return out, stderr, nil
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 {
		return "", "", fmt.Errorf("invalid replacement inspect fixture: %w", err)
	}
	list[0]["HostConfig"].(map[string]any)["FutureReplacementOnly"] = r.value
	data, err := json.Marshal(list)
	return string(data), stderr, err
}

func TestReplacementOnlyUnknownInspectFieldsRollBackAdoption(t *testing.T) {
	for name, value := range map[string]any{"zero-looking": false, "nonzero": true} {
		t.Run(name, func(t *testing.T) {
			withTestHome(t)
			withFastPolling(t)
			sim := newMutationSim()
			service := "replacement-" + strings.ReplaceAll(name, "-", "")
			ports := []PortMapping{{HostIP: "127.0.0.1", HostPort: 8181, ContainerPort: 80, Protocol: "tcp"}}
			sim.addContainer(service, "nginx:alpine", "running", ports, false, "")
			originalID := sim.containers[service].id
			base := &adoptionInspectRouter{sim: sim, image: "nginx:alpine", ports: ports}
			result, err := (&PodmanService{runner: &replacementUnknownRunner{base: base, value: value}}).AdoptContainer(originalID, service)
			if err != nil || result == nil || result.Success || result.Rollback == nil || !result.Rollback.Verified {
				t.Fatalf("replacement-only unknown field did not trigger verified rollback: %+v %v", result, err)
			}
			original := sim.containers[service]
			if original == nil || original.id != originalID || original.state != "running" || len(sim.containers) != 1 {
				t.Fatalf("original was not preserved exactly: %+v", sim.containers)
			}
			if _, err := (&PodmanService{}).GetSpec(service); err == nil {
				t.Fatal("replacement spec became authoritative despite unknown field")
			}
		})
	}
}

func TestAdoptionUserAndUlimitAssuranceMatrix(t *testing.T) {
	baseline, err := ParseInspectToAssessment([]byte(baseInspectJSON("", `,"User":""`, "", "", "")))
	if err != nil || !baseline.CanAdopt {
		t.Fatalf("supported default-root baseline was refused: %+v %v", baseline, err)
	}
	for _, user := range []string{"0", "root", "65534", "nobody", "1000:1000"} {
		t.Run("user-"+strings.ReplaceAll(user, ":", "-"), func(t *testing.T) {
			assessment, err := ParseInspectToAssessment([]byte(baseInspectJSON("", `,"User":"`+user+`"`, "", "", "")))
			if err != nil {
				t.Fatal(err)
			}
			if assessment.CanAdopt || !strings.Contains(strings.Join(assessment.Blockers, ";"), "custom user") {
				t.Fatalf("explicit numeric/named image user was not refused: %+v", assessment)
			}
		})
	}
	assessment, err := ParseInspectToAssessment([]byte(baseInspectJSON("", "", `,"Ulimits":[{"Name":"RLIMIT_NOFILE","Soft":1024,"Hard":2048},{"Name":"RLIMIT_NPROC","Soft":64,"Hard":128}]`, "", "")))
	if err != nil || !assessment.CanAdopt || len(assessment.ProposedSpec.Ulimits) != 2 {
		t.Fatalf("supported NOFILE/NPROC controls were not captured: %+v %v", assessment, err)
	}
	assessment.ProposedSpec.Managed = true
	args, err := BuildRunArgsFromSpec(assessment.ProposedSpec)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--ulimit nofile=1024:2048", "--ulimit nproc=64:128"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("accepted ulimit was not replayed (%s): %s", want, joined)
		}
	}
}

func TestCandidatePathReplacementCannotChangeJournalOrCommittedSpec(t *testing.T) {
	t.Run("journal-carries-prepared-object", func(t *testing.T) {
		p, _, _ := sessionFixture(t)
		spec := ContainerSpec{Name: "candidate-journal", Image: "alpine", SchemaVersion: CurrentSpecSchemaVersion, Managed: true, ReplayComplete: true}
		candidate, prepared, err := writeCandidateSpecPrepared(spec)
		if err != nil {
			t.Fatal(err)
		}
		attacker := prepared
		attacker.Image = "attacker.example/replaced"
		data, _ := json.Marshal(attacker)
		attackerPath := candidate + ".attacker"
		if err := os.WriteFile(attackerPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(attackerPath, candidate); err != nil {
			t.Fatal(err)
		}
		journal, err := p.beginManagedJournal(spec.Name, "create", "", candidate, nil, prepared)
		if err != nil {
			t.Fatal(err)
		}
		if journal.Candidate.Image != "alpine" {
			t.Fatalf("journal trusted replaced candidate bytes: %+v", journal.Candidate)
		}
		_ = journal.remove()
		discardCandidateSpec(candidate)
	})

	t.Run("commit-serializes-verified-object", func(t *testing.T) {
		withTestHome(t)
		spec := ContainerSpec{Name: "candidate-commit", Image: "alpine", SchemaVersion: CurrentSpecSchemaVersion, Managed: true, ReplayComplete: true}
		candidate, prepared, err := writeCandidateSpecPrepared(spec)
		if err != nil {
			t.Fatal(err)
		}
		attacker := prepared
		attacker.Image = "attacker.example/replaced"
		data, _ := json.Marshal(attacker)
		attackerPath := candidate + ".attacker"
		if err := os.WriteFile(attackerPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(attackerPath, candidate); err != nil {
			t.Fatal(err)
		}
		if err := commitCandidateSpec(candidate, prepared); err != nil {
			t.Fatal(err)
		}
		stored, err := (&PodmanService{}).GetSpec(spec.Name)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Image != "alpine" {
			t.Fatalf("substituted candidate became authoritative: %+v", stored)
		}
		if _, err := os.Stat(candidate); !os.IsNotExist(err) {
			t.Fatalf("staged candidate was not discarded: %v", err)
		}
		path, _ := getSpecFilePath(spec.Name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("committed permissions are not private: %v %v", info, err)
		}
	})
}

func TestPostPromotionFailureRestoresPreviousAuthorityBeforeRollback(t *testing.T) {
	withTestHome(t)
	old := ContainerSpec{Name: "commit-restore", Image: "old-image", SchemaVersion: CurrentSpecSchemaVersion, ReplayComplete: true}
	if err := saveSpec(old); err != nil {
		t.Fatal(err)
	}
	final, _ := getSpecFilePath(old.Name)
	oldBytes, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	verified := ContainerSpec{Name: old.Name, Image: "verified-image", ResolvedImage: "sha256:" + strings.Repeat("f", 64), Managed: true, SchemaVersion: CurrentSpecSchemaVersion, ReplayComplete: true}
	candidate, prepared, err := writeCandidateSpecPrepared(verified)
	if err != nil {
		t.Fatal(err)
	}
	journal := &managedJournal{
		Service:       old.Name,
		Token:         newSessionID(),
		Runtime:       "v1:" + strings.Repeat("a", 64),
		Operation:     "adopt",
		Step:          "verified",
		Candidate:     prepared,
		CandidatePath: candidate,
		OldSpec:       oldBytes,
		OldSpecExists: true,
	}
	if err := journal.save(); err != nil {
		t.Fatal(err)
	}
	p := &PodmanService{transaction: journal}
	p.commitSpec = func(path string, spec ContainerSpec) error {
		if err := commitCandidateSpec(path, spec); err != nil {
			return err
		}
		return errors.New("injected failure after authority promotion")
	}
	if err := p.commitCandidate(candidate, prepared); err == nil {
		t.Fatal("post-promotion failure was reported as success")
	}
	stored, err := p.GetSpec(old.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Image != old.Image || journal.CommitUncertain {
		t.Fatalf("old authority was not restored before rollback: %+v uncertain=%v", stored, journal.CommitUncertain)
	}
	if _, err := loadManagedJournal(old.Name); err != nil {
		t.Fatalf("recovery receipt disappeared before terminal cleanup: %v", err)
	}
}

type inspectOverrideRunner struct {
	base    CommandRunner
	inspect string
}

func (r *inspectOverrideRunner) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && args[0] == "inspect" {
		return r.inspect, "", nil
	}
	return r.base.Run(name, args...)
}

func TestRollbackRestoresNameAndLifecycleButRefusesVerifiedWhenAcceptedControlDrifts(t *testing.T) {
	sim := newMutationSim()
	p := setupManagedContainer(t, sim, "rollback-control", "fixture", "running", nil)
	originals, err := p.ListContainers(true)
	if err != nil || len(originals) != 1 {
		t.Fatalf("original snapshot: %+v %v", originals, err)
	}
	original := originals[0]
	backup := "rollback-control-backup"
	if _, _, err := sim.Run("podman", "rename", "--", original.Id, backup); err != nil {
		t.Fatal(err)
	}
	accepted := strings.Replace(baseInspectJSON("", "", `,"PidsLimit":0`, "", ""), "abc1234567890abcdef", original.Id, 1)
	accepted = strings.Replace(accepted, "/fixture", "/rollback-control", 1)
	drifted := strings.Replace(baseInspectJSON("", "", `,"PidsLimit":2048`, "", ""), "abc1234567890abcdef", original.Id, 1)
	drifted = strings.Replace(drifted, "/fixture", "/rollback-control", 1)
	p.runner = &inspectOverrideRunner{base: sim, inspect: drifted}
	witness := transactionWitness{Original: original, OriginalInspect: accepted}
	rollback := p.executeRollback(backup, "rollback-control", "", original.Id, lifecycleRunning, false, witness)
	if !rollback.RestoredName || !rollback.RestoredLifecycle || rollback.Verified {
		t.Fatalf("rollback assurance overstated: %+v", rollback)
	}
	if !strings.Contains(strings.Join(rollback.Errors, ";"), "accepted controls") {
		t.Fatalf("missing accepted-control failure: %+v", rollback)
	}
	container := sim.containers["rollback-control"]
	if container == nil || container.id != original.Id || container.state != "running" {
		t.Fatalf("original identity/lifecycle not restored: %+v", sim.containers)
	}

	journal := &managedJournal{
		Service:           "rollback-control",
		Token:             newSessionID(),
		Runtime:           "v1:" + strings.Repeat("a", 64),
		Operation:         "adopt",
		Step:              "rollback",
		Original:          &original,
		OriginalInspect:   accepted,
		OriginalLifecycle: lifecycleRunning,
		Candidate:         ContainerSpec{Name: "rollback-control", Image: "fixture", ResolvedImage: original.ImageID, Managed: true, SchemaVersion: CurrentSpecSchemaVersion, ReplayComplete: true},
	}
	if err := journal.save(); err != nil {
		t.Fatal(err)
	}
	p.finishManagedJournal(journal)
	if _, err := loadManagedJournal(journal.Service); err != nil {
		t.Fatalf("recovery journal was removed despite control drift: %v", err)
	}
}

func TestRecoveryAndFailedRenameRetainJournalWhenAcceptedControlDrifts(t *testing.T) {
	sim := newMutationSim()
	p := setupManagedContainer(t, sim, "recover-control", "fixture", "running", nil)
	originals, err := p.ListContainers(true)
	if err != nil || len(originals) != 1 {
		t.Fatalf("original snapshot: %+v %v", originals, err)
	}
	original := originals[0]
	backup := "recover-control-backup"
	if _, _, err := sim.Run("podman", "rename", "--", original.Id, backup); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sim.Run("podman", "stop", "--", original.Id); err != nil {
		t.Fatal(err)
	}
	accepted := strings.Replace(baseInspectJSON("", "", `,"PidsLimit":0`, "", ""), "abc1234567890abcdef", original.Id, 1)
	accepted = strings.Replace(accepted, "/fixture", "/recover-control", 1)
	drifted := strings.Replace(baseInspectJSON("", "", `,"PidsLimit":2048`, "", ""), "abc1234567890abcdef", original.Id, 1)
	drifted = strings.Replace(drifted, "/fixture", "/recover-control", 1)
	p.runner = &inspectOverrideRunner{base: sim, inspect: drifted}
	runtimeID, err := p.sessionRuntimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	journal := &managedJournal{
		Service:           "recover-control",
		Token:             newSessionID(),
		Runtime:           runtimeID,
		Operation:         "adopt",
		Step:              "rollback",
		Original:          &original,
		OriginalInspect:   accepted,
		OriginalLifecycle: lifecycleRunning,
		Backup:            backup,
		Candidate:         ContainerSpec{Name: "recover-control", Image: "fixture", ResolvedImage: original.ImageID, Managed: true, SchemaVersion: CurrentSpecSchemaVersion, ReplayComplete: true},
	}
	if err := journal.save(); err != nil {
		t.Fatal(err)
	}
	if err := p.RecoverManagedTransaction(journal.Service, true); err == nil || !strings.Contains(err.Error(), "accepted controls") {
		t.Fatalf("recovery ignored accepted-control drift: %v", err)
	}
	if _, err := loadManagedJournal(journal.Service); err != nil {
		t.Fatalf("recovery journal was removed despite drift: %v", err)
	}
	restored := sim.containers[journal.Service]
	if restored == nil || restored.id != original.Id || restored.state != "running" {
		t.Fatalf("exact original name/lifecycle was not restored: %+v", sim.containers)
	}
	result, _ := p.reconcileFailedRename(&original, backup, lifecycleRunning, transactionWitness{Original: original, OriginalInspect: accepted})
	if result.Verified || !result.RestoredName || !result.RestoredLifecycle {
		t.Fatalf("failed-rename reconciliation overstated assurance: %+v", result)
	}
}

func TestSessionJournalDirectorySyncCompletesBeforeContainerRun(t *testing.T) {
	p, r, spec := sessionFixture(t)
	originalSync := privateJSONDirectorySync
	privateJSONDirectorySync = func(*os.Root) error { return errors.New("injected directory sync failure") }
	t.Cleanup(func() { privateJSONDirectorySync = originalSync })
	state, err := p.LaunchDisposableSession(spec)
	if err == nil || state != nil || !strings.Contains(err.Error(), "directory sync failure") {
		t.Fatalf("journal sync failure did not abort launch: %+v %v", state, err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "podman run") {
			t.Fatalf("container ran before journal directory sync succeeded: %s", call)
		}
	}
}
