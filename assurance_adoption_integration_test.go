//go:build integration && linux

package main

// Adoption counterfactuals use the engine's unmodified inspect schema. The
// runner can deliberately drop a replayed ulimit to verify drift detection.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Bind fixtures must be readable by confined containers without requesting
// runtime relabel options or weakening SELinux. Only new campaign-owned
// sources are labeled, before filesystem identity approval or path-swap hooks.
func labelAssuranceBindSources(t *testing.T, sources ...string) {
	t.Helper()
	if _, err := os.Stat("/sys/fs/selinux/enforce"); os.IsNotExist(err) {
		return
	} else if err != nil {
		t.Fatalf("check fixture SELinux availability: %v", err)
	}
	base := os.Getenv("PODDER_R3_BASE")
	if !filepath.IsAbs(base) {
		t.Fatal("SELinux fixture labeling requires an explicit campaign root")
	}
	out := filepath.Join(filepath.Clean(base), "out")
	for _, source := range sources {
		physical, err := filepath.EvalSymlinks(source)
		if err != nil || physical != filepath.Clean(source) || physical == out || !pathContains(out, physical) {
			t.Fatalf("refuse SELinux labeling outside a new fixture source: %q (%v)", source, err)
		}
		paths := []string{}
		if err := filepath.WalkDir(physical, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return fmt.Errorf("fixture source contains a symlink or special file: %s", path)
			}
			paths = append(paths, path)
			return nil
		}); err != nil {
			t.Fatalf("validate SELinux fixture source: %v", err)
		}
		for _, path := range paths {
			output, err := exec.Command("/usr/bin/chcon", "--no-dereference", "--type=container_file_t", "--", path).CombinedOutput()
			if err != nil {
				t.Fatalf("label new bind fixture %q container_file_t: %s (%v)", path, strings.TrimSpace(string(output)), err)
			}
		}
	}
}

type assuranceAdoptionRunner struct {
	*round2Runner
	dropUlimit bool
	dropped    int
}

func (r *assuranceAdoptionRunner) Run(name string, args ...string) (string, string, error) {
	return r.RunContextWithFiles(context.Background(), name, nil, args...)
}

func (r *assuranceAdoptionRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	return r.RunContextWithFiles(ctx, name, nil, args...)
}

func (r *assuranceAdoptionRunner) RunWithFiles(name string, files []*os.File, args ...string) (string, string, error) {
	return r.RunContextWithFiles(context.Background(), name, files, args...)
}

// All managed execution paths must observe the fault, including the
// descriptor-backed mount handoff. Inspect output is always passed through.
func (r *assuranceAdoptionRunner) RunContextWithFiles(ctx context.Context, name string, files []*os.File, args ...string) (string, string, error) {
	if r.dropUlimit && name == "podman" && len(args) > 0 && (args[0] == "run" || args[0] == "create") {
		filtered := []string{}
		for i := 0; i < len(args); i++ {
			if args[i] == "--" {
				filtered = append(filtered, args[i:]...)
				break
			}
			if args[i] == "--ulimit" {
				i++
				r.dropped++
				continue
			}
			filtered = append(filtered, args[i])
		}
		args = filtered
	}
	if name == "podman" {
		return r.round2Runner.RunContextWithFiles(ctx, name, files, args...)
	}
	if len(files) != 0 {
		return "", "", fmt.Errorf("unexpected inherited files for %s", name)
	}
	if name == "ss" {
		return (execCommandRunner{}).RunContext(ctx, "/usr/bin/ss", args...)
	}
	return "", "", fmt.Errorf("unexpected executable %s", name)
}
func TestAssuranceAdoptionCounterfactual(t *testing.T) {
	raw, r, image := assuranceStore(t)
	scenarios := []struct {
		name    string
		flags   []string
		blocker string
	}{
		{"baseline", nil, ""}, {"read-only", []string{"--read-only"}, "ReadonlyRootfs"},
		{"nofile", []string{"--ulimit=nofile=1024:2048"}, ""}, {"nproc", []string{"--ulimit=nproc=64:128"}, ""},
		{"explicit-root-user", []string{"--user=0"}, "custom user"}, {"numeric-user", []string{"--user=65534"}, "custom user"},
		{"pids", []string{"--pids-limit=24"}, "PidsLimit"}, {"network", []string{"--network=none"}, "network"},
		{"shm", []string{"--shm-size=8m"}, "ShmSize"}, {"oom", []string{"--oom-score-adj=900"}, "OomScoreAdj"},
		{"umask", []string{"--umask=0077"}, "Umask"}, {"group", []string{"--group-add=keep-groups"}, "--group-add"},
		{"sysctl", []string{"--sysctl=net.ipv4.ping_group_range=0 0"}, "--sysctl"},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			name := "cf-" + sc.name
			args := append([]string{"run", "-d", "--name", name, "--label", "org.podder.assurance=round2"}, sc.flags...)
			args = append(args, "--", image, "sleep")
			out, stderr, err := r.Run("podman", args...)
			if err != nil {
				t.Fatalf("fixture: %s %v", stderr, err)
			}
			id := strings.TrimSpace(out)
			p := &PodmanService{runner: &assuranceAdoptionRunner{round2Runner: r}}
			a, err := p.InspectContainerForAdoption(id)
			if err != nil {
				t.Fatal(err)
			}
			if sc.blocker == "" {
				if !a.CanAdopt {
					t.Fatal("counterfactual baseline refused", a.Blockers)
				}
				result, err := p.AdoptContainer(id, name)
				if err != nil || !result.Success {
					t.Fatalf("adopt: %+v %v", result, err)
				}
				cs, err := raw.ListContainers(true)
				if err != nil {
					t.Fatal(err)
				}
				replacement := findContainerByName(cs, name)
				if replacement == nil || replacement.Id == id {
					t.Fatal("replacement not found")
				}
				before, _, err := r.Run("podman", "inspect", "--format", "json", "--", id)
				if err != nil {
					t.Fatal(err)
				}
				after, _, err := r.Run("podman", "inspect", "--format", "json", "--", replacement.Id)
				if err != nil {
					t.Fatal(err)
				}
				old, e := parseSingleInspect(before)
				if e != nil {
					t.Fatal(e)
				}
				fresh, e := parseSingleInspect(after)
				if e != nil || !adoptionReplayEquivalent(old, fresh) {
					t.Fatal("actual accepted controls changed", e)
				}
			} else {
				if a.CanAdopt || !strings.Contains(strings.Join(a.Blockers, " "), sc.blocker) {
					t.Fatalf("hardened control not specifically refused: %+v", a)
				}
				before, err := raw.ListContainers(true)
				if err != nil {
					t.Fatal(err)
				}
				if result, err := p.AdoptContainer(id, name); err == nil && (result == nil || result.Success) {
					t.Fatal("blocked adoption succeeded")
				}
				after, err := raw.ListContainers(true)
				if err != nil || len(before) != len(after) {
					t.Fatal("blocked adoption mutated inventory", err)
				}
				c := findContainerByExactID(after, id)
				if !containerHasName(c, name) || c.State != "running" {
					t.Fatal("blocked original changed")
				}
			}
		})
	}
}
func TestAssuranceAdoptionPostVerifyCounterfactual(t *testing.T) {
	raw, r, image := assuranceStore(t)
	name := "post-verify"
	out, stderr, err := r.Run("podman", "run", "-d", "--name", name, "--label", "org.podder.assurance=round2", "--ulimit", "nofile=1024:1024", "--", image, "sleep")
	if err != nil {
		t.Fatalf("fixture: %s %v", stderr, err)
	}
	id := strings.TrimSpace(out)
	modified := &assuranceAdoptionRunner{round2Runner: r, dropUlimit: true}
	p := &PodmanService{runner: modified}
	a, err := p.InspectContainerForAdoption(id)
	if err != nil || !a.CanAdopt {
		t.Fatal("valid original refused", a, err)
	}
	result, err := p.AdoptContainer(id, name)
	if err != nil || result == nil || result.Success || modified.dropped == 0 || result.Rollback == nil || !result.Rollback.Verified {
		t.Fatalf("actual lost ulimit was not rolled back: %+v %v dropped=%d", result, err, modified.dropped)
	}
	cs, err := raw.ListContainers(true)
	if err != nil {
		t.Fatal(err)
	}
	c := findContainerByExactID(cs, id)
	if !containerHasName(c, name) || c.State != "running" || len(cs) != 1 {
		t.Fatal(fmt.Sprintf("original not restored: %+v", cs))
	}
}

func TestAssuranceAdoptionBindPositiveControl(t *testing.T) {
	raw, r, image := assuranceStore(t)
	fixture := filepath.Join(os.Getenv("PODDER_R3_BASE"), "out", "adoption-bind")
	if err := os.MkdirAll(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "sentinel"), []byte("adopted-bind"), 0o600); err != nil {
		t.Fatal(err)
	}
	labelAssuranceBindSources(t, fixture)
	name := "adoption-bind"
	mount := "type=bind,src=" + fixture + ",target=/external,readonly,nosuid,nodev"
	out, stderr, err := r.Run("podman", "run", "-d", "--name", name, "--label", "org.podder.assurance=round2", "--mount", mount, "--", image, "sleep")
	if err != nil {
		t.Fatalf("fixture: %s %v", stderr, err)
	}
	originalID := strings.TrimSpace(out)
	p := &PodmanService{runner: &assuranceAdoptionRunner{round2Runner: r}}
	assessment, err := p.InspectContainerForAdoption(originalID)
	if err != nil || !assessment.CanAdopt {
		t.Fatalf("unchanged bind was not adoptable: %+v %v", assessment, err)
	}
	result, err := p.AdoptContainer(originalID, name)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("unchanged bind adoption failed: %+v %v", result, err)
	}
	t.Cleanup(func() { r2cleanup(t, p, r, image, name) })
	containers, err := raw.ListContainers(true)
	if err != nil {
		t.Fatal(err)
	}
	replacement := findContainerByName(containers, name)
	if replacement == nil || replacement.Id == originalID {
		t.Fatalf("replacement not found: %+v", containers)
	}
	contents, stderr, err := r.Run("podman", "exec", "--", replacement.Id, "/fixture", "read", "/external/sentinel")
	if err != nil || contents != "adopted-bind" {
		t.Fatalf("adopted bind positive control failed: %q %s %v", contents, stderr, err)
	}
	stored, err := p.GetSpec(name)
	if err != nil || stored == nil || stored.SchemaVersion != 4 || len(stored.Binds) != 1 || !stored.Binds[0].ReadOnly || !stored.Binds[0].NoSuid || !stored.Binds[0].NoDev {
		t.Fatalf("restrictive bind did not persist as schema 4: %+v %v", stored, err)
	}
	// Exercise the persisted spec with no existing managed container. This
	// must reproduce restrictions without the original assessment in memory.
	if err := p.StopContainer(replacement.Id); err != nil {
		t.Fatal(err)
	}
	if err := p.forceRemoveContainer(replacement.Id); err != nil {
		t.Fatal(err)
	}
	deployed, err := p.DeploySpec(name)
	if err != nil || deployed == nil || !deployed.Success || deployed.NewContainerID == replacement.Id {
		t.Fatalf("cold restricted-bind deployment failed: %+v %v", deployed, err)
	}
	verify := func(stage string) {
		t.Helper()
		data, stderr, err := r.Run("podman", "inspect", "--format", "json", "--", deployed.NewContainerID)
		if err != nil {
			t.Fatalf("%s inspect failed: %s %v", stage, stderr, err)
		}
		current, err := parseSingleInspect(data)
		if err != nil || current.Id != deployed.NewContainerID {
			t.Fatalf("%s container identity did not verify: %v", stage, err)
		}
		if err := verifyRequestedBindRestrictions(current, stored.Binds); err != nil {
			t.Fatalf("%s lost persisted bind restrictions: %v", stage, err)
		}
		contents, stderr, err := r.Run("podman", "exec", "--", deployed.NewContainerID, "/fixture", "read", "/external/sentinel")
		if err != nil || contents != "adopted-bind" {
			t.Fatalf("%s bind contents changed: %q %s %v", stage, contents, stderr, err)
		}
	}
	verify("cold deploy")
	if err := p.RestartContainer(deployed.NewContainerID); err != nil {
		t.Fatalf("ordinary restricted-bind restart failed: %v", err)
	}
	verify("ordinary restart")
}

func TestAssuranceManagedBindUsesVerifiedStartHandoff(t *testing.T) {
	p, r, image := assuranceStore(t)
	fixture := filepath.Join(os.Getenv("PODDER_R3_BASE"), "out", "managed-bind-restart")
	if err := os.MkdirAll(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "sentinel"), []byte("managed-bind-restart"), 0o600); err != nil {
		t.Fatal(err)
	}
	labelAssuranceBindSources(t, fixture)
	name := "managed-bind-restart"
	created, err := p.CreateContainer(ContainerCreateRequest{
		Image: image, Name: name, Managed: true,
		Labels: map[string]string{"org.podder.assurance": "round2"},
		Binds:  []BindMountSpec{{HostPath: fixture, ContainerPath: "/external", ReadOnly: true}},
	})
	if err != nil || created == nil || !created.Success {
		t.Fatalf("managed bind create failed: %+v %v", created, err)
	}
	t.Cleanup(func() { r2cleanup(t, p, r, image, name) })
	if err := p.StopContainer(created.ContainerID); err != nil {
		t.Fatal(err)
	}
	if err := p.StartContainer(created.ContainerID); err != nil {
		t.Fatalf("verified bind start failed: %v", err)
	}
	contents, stderr, err := r.Run("podman", "exec", "--", created.ContainerID, "/fixture", "read", "/external/sentinel")
	if err != nil || contents != "managed-bind-restart" {
		t.Fatalf("restarted managed bind mismatch: %q %s %v", contents, stderr, err)
	}
	if err := p.StopContainer(created.ContainerID); err != nil {
		t.Fatal(err)
	}
	moved := fixture + "-moved"
	replacement := fixture + "-replacement"
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "sentinel"), []byte("managed-replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	labelAssuranceBindSources(t, replacement)
	if err := os.Rename(fixture, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, fixture); err != nil {
		t.Fatal(err)
	}
	if err := p.StartContainer(created.ContainerID); err == nil || !strings.Contains(err.Error(), "filesystem object changed") {
		t.Fatalf("pre-start managed source replacement was not refused: %v", err)
	}
	if contents, err := os.ReadFile(filepath.Join(replacement, "sentinel")); err != nil || string(contents) != "managed-replacement" {
		t.Fatalf("managed replacement sentinel changed: %q %v", contents, err)
	}
	if err := os.Remove(fixture); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, fixture); err != nil {
		t.Fatal(err)
	}
}

func TestAssuranceAdoptionBindSourceReplacementRefused(t *testing.T) {
	raw, r, image := assuranceStore(t)
	fixture := filepath.Join(os.Getenv("PODDER_R3_BASE"), "out", "adoption-bind-replacement")
	approved := filepath.Join(fixture, "approved")
	moved := filepath.Join(fixture, "approved-moved")
	replacement := filepath.Join(fixture, "replacement")
	if err := os.MkdirAll(approved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(approved, "sentinel"), []byte("original-mounted-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "sentinel"), []byte("replacement-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	labelAssuranceBindSources(t, approved, replacement)
	name := "adoption-bind-replaced"
	mount := "type=bind,src=" + approved + ",target=/external,readonly"
	out, stderr, err := r.Run("podman", "run", "-d", "--name", name, "--label", "org.podder.assurance=round2", "--mount", mount, "--", image, "sleep")
	if err != nil {
		t.Fatalf("fixture: %s %v", stderr, err)
	}
	originalID := strings.TrimSpace(out)
	if err := os.Rename(approved, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, approved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(approved); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove replacement link: %v", err)
		}
		if err := os.Rename(moved, approved); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore approved directory: %v", err)
		}
	})

	p := &PodmanService{runner: &assuranceAdoptionRunner{round2Runner: r}}
	assessment, err := p.InspectContainerForAdoption(originalID)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.CanAdopt || !strings.Contains(strings.Join(assessment.Blockers, " "), "filesystem object mounted") {
		t.Fatalf("replacement source was not specifically refused: %+v", assessment)
	}
	result, err := p.AdoptContainer(originalID, name)
	if err != nil || result == nil || result.Success {
		t.Fatalf("blocked bind replacement was adopted: %+v %v", result, err)
	}
	containers, err := raw.ListContainers(true)
	if err != nil || len(containers) != 1 {
		t.Fatalf("blocked adoption mutated inventory: %+v %v", containers, err)
	}
	original := findContainerByExactID(containers, originalID)
	if !containerHasName(original, name) || original.State != "running" {
		t.Fatalf("original container was not preserved: %+v", containers)
	}
	contents, stderr, err := r.Run("podman", "exec", "--", originalID, "/fixture", "read", "/external/sentinel")
	if err != nil || contents != "original-mounted-object" {
		t.Fatalf("original mounted object changed: %q %s %v", contents, stderr, err)
	}
	if contents, err := os.ReadFile(filepath.Join(replacement, "sentinel")); err != nil || string(contents) != "replacement-object" {
		t.Fatalf("replacement sentinel changed: %q %v", contents, err)
	}
}

func cleanupAssuranceSession(t *testing.T, p *PodmanService, state *SessionRuntimeState) {
	t.Helper()
	result, cleanupErr := p.StopAndCleanupSession(state.ID, true, false)
	if cleanupErr == nil && result != nil && result.State != "stopped" {
		current, verifyErr := p.sessionContainer(state)
		ownedOrAbsent := verifyErr == nil && current == nil
		if verifyErr == nil && current != nil {
			_ = p.forceRemoveContainer(state.ContainerID)
			ownedOrAbsent = pollUntil(20, 50*time.Millisecond, func() bool { return p.containerAbsent(state.ContainerID, "") })
		}
		if ownedOrAbsent {
			result, cleanupErr = p.StopAndCleanupSession(state.ID, true, false)
		}
	}
	if cleanupErr != nil || result == nil || result.State != "stopped" {
		t.Errorf("session cleanup failed: %+v %v", result, cleanupErr)
	}
}

func TestAssuranceKeepIDSession(t *testing.T) {
	p, r, image := assuranceStore(t)
	spec := DisposableSessionSpec{SchemaVersion: sessionSchema, Name: "keep-id-session", Image: image, Policy: "temporary", KeepID: true}
	preview, err := p.PreviewDisposableSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.LaunchID = preview.Spec.LaunchID
	state, err := p.LaunchDisposableSession(spec)
	if err != nil || state.State != "running" {
		t.Fatalf("keep-id session did not start: %+v %v", state, err)
	}
	t.Cleanup(func() { cleanupAssuranceSession(t, p, state) })
	out, stderr, err := r.Run("podman", "inspect", "--format", "json", "--", state.ContainerID)
	if err != nil {
		t.Fatalf("inspect: %s %v", stderr, err)
	}
	current, err := parseSingleInspect(out)
	if err != nil {
		t.Fatal(err)
	}
	createCommand, _ := json.Marshal(current.configFields["CreateCommand"])
	if !strings.Contains(string(createCommand), "--userns=keep-id") {
		t.Fatalf("real engine did not receive keep-id: %s", createCommand)
	}
}

func TestAssurancePinnedMountSurvivesPathReplacement(t *testing.T) {
	p, r, image := assuranceStore(t)
	fixture := filepath.Join(os.Getenv("PODDER_R3_BASE"), "out", "mount-swap")
	approved := filepath.Join(fixture, "approved")
	moved := filepath.Join(fixture, "approved-moved")
	outside := filepath.Join(fixture, "outside")
	if err := os.MkdirAll(approved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(approved, "sentinel"), []byte("approved-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("replacement-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	labelAssuranceBindSources(t, approved, outside)
	spec := DisposableSessionSpec{SchemaVersion: sessionSchema, Name: "pinned-mount", Image: image, Policy: "temporary", Command: []string{"sleep", "-d"}, AllowExternalBinds: true, ExternalBinds: []BindMountSpec{{HostPath: approved, ContainerPath: "/external", ReadOnly: true}}}
	preview, err := p.PreviewDisposableSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.LaunchID = preview.Spec.LaunchID
	spec.ExternalBinds = preview.Spec.ExternalBinds
	spec.ExternalBindApprovals = preview.Spec.ExternalBindApprovals
	p.sessionMountsPinnedHook = func() {
		if err := os.Rename(approved, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, approved); err != nil {
			t.Fatal(err)
		}
	}
	p.sessionRunResultHook = func(stderr string, err error) {
		if err != nil {
			t.Logf("pinned launch stderr: %s (%v)", stderr, err)
		}
	}
	state, err := p.LaunchDisposableSession(spec)
	if err != nil || state.State != "running" {
		t.Fatalf("descriptor-backed session did not start: %+v %v", state, err)
	}
	t.Cleanup(func() { cleanupAssuranceSession(t, p, state) })
	out, stderr, err := r.Run("podman", "exec", "--", state.ContainerID, "/fixture", "read", "/external/sentinel")
	if err != nil || out != "approved-object" {
		t.Fatalf("container observed replacement instead of approved object: %q %s %v", out, stderr, err)
	}
	inspectJSON, stderr, err := r.Run("podman", "inspect", "--format", "json", "--", state.ContainerID)
	if err != nil {
		t.Fatalf("inspect pinned command: %s %v", stderr, err)
	}
	current, err := parseSingleInspect(inspectJSON)
	if err != nil {
		t.Fatal(err)
	}
	commandJSON, _ := json.Marshal(current.configFields["Cmd"])
	if !strings.Contains(string(commandJSON), `"-d"`) {
		t.Fatalf("workload command -d was altered by the run-to-create wrapper: %s", commandJSON)
	}
	if contents, err := os.ReadFile(filepath.Join(outside, "sentinel")); err != nil || string(contents) != "replacement-object" {
		t.Fatalf("outside sentinel changed: %q %v", contents, err)
	}
	if err := os.Remove(approved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, approved); err != nil {
		t.Fatal(err)
	}
}

func TestAssurancePinnedMountRejectsSecondPathReplacementBeforeStart(t *testing.T) {
	p, _, image := assuranceStore(t)
	fixture := filepath.Join(os.Getenv("PODDER_R3_BASE"), "out", "mount-second-swap")
	approved := filepath.Join(fixture, "approved")
	moved := filepath.Join(fixture, "approved-moved")
	replacement := filepath.Join(fixture, "replacement")
	ready := filepath.Join(fixture, "handoff-ready")
	resume := filepath.Join(fixture, "handoff-resume")
	if err := os.MkdirAll(approved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(approved, "sentinel"), []byte("approved-second-window"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "sentinel"), []byte("replacement-second-window"), 0o600); err != nil {
		t.Fatal(err)
	}
	labelAssuranceBindSources(t, approved, replacement)
	spec := DisposableSessionSpec{
		SchemaVersion: sessionSchema, Name: "second-window", Image: image, Policy: "temporary",
		AllowExternalBinds: true,
		ExternalBinds:      []BindMountSpec{{HostPath: approved, ContainerPath: "/external", ReadOnly: true}},
	}
	preview, err := p.PreviewDisposableSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.LaunchID = preview.Spec.LaunchID
	spec.ExternalBinds = preview.Spec.ExternalBinds
	spec.ExternalBindApprovals = preview.Spec.ExternalBindApprovals
	p.mountHandoffReadyPath = ready
	p.mountHandoffContinuePath = resume
	type launchResult struct {
		state *SessionRuntimeState
		err   error
	}
	done := make(chan launchResult, 1)
	go func() {
		state, launchErr := p.LaunchDisposableSession(spec)
		done <- launchResult{state: state, err: launchErr}
	}()
	if !pollUntil(2000, 10*time.Millisecond, func() bool {
		_, statErr := os.Stat(ready)
		return statErr == nil
	}) {
		t.Fatal("mount handoff did not reach the controlled barrier")
	}
	if err := os.Rename(approved, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, approved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resume, []byte("resume"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil || result.state == nil || result.state.State == "running" {
		t.Fatalf("second-window replacement was not safely refused: %+v %v", result.state, result.err)
	}
	containers, err := p.ListContainers(true)
	if err != nil || len(containers) != 0 {
		t.Fatalf("failed handoff left a container: %+v %v", containers, err)
	}
	if contents, err := os.ReadFile(filepath.Join(replacement, "sentinel")); err != nil || string(contents) != "replacement-second-window" {
		t.Fatalf("replacement sentinel changed: %q %v", contents, err)
	}
	if err := os.Remove(approved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, approved); err != nil {
		t.Fatal(err)
	}
}

func TestAssuranceManagedBindSurvivesPathReplacement(t *testing.T) {
	p, r, image := assuranceStore(t)
	fixture := filepath.Join(os.Getenv("PODDER_R3_BASE"), "out", "managed-mount-swap")
	approved := filepath.Join(fixture, "approved")
	moved := filepath.Join(fixture, "approved-moved")
	replacement := filepath.Join(fixture, "replacement")
	if err := os.MkdirAll(approved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(approved, "sentinel"), []byte("approved-managed-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "sentinel"), []byte("replacement-managed-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	labelAssuranceBindSources(t, approved, replacement)
	p.sessionMountsPinnedHook = func() {
		if err := os.Rename(approved, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, approved); err != nil {
			t.Fatal(err)
		}
	}
	name := "pinned-managed"
	result, err := p.CreateContainer(ContainerCreateRequest{
		Image: image, Name: name, Managed: true,
		Labels: map[string]string{"org.podder.assurance": "round2"},
		Binds:  []BindMountSpec{{HostPath: approved, ContainerPath: "/external", ReadOnly: true}},
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("descriptor-backed managed create failed: %+v %v", result, err)
	}
	t.Cleanup(func() { r2cleanup(t, p, r, image, name) })
	out, stderr, err := r.Run("podman", "exec", "--", result.ContainerID, "/fixture", "read", "/external/sentinel")
	if err != nil || out != "approved-managed-object" {
		t.Fatalf("managed container observed replacement instead of approved object: %q %s %v", out, stderr, err)
	}
	if contents, err := os.ReadFile(filepath.Join(replacement, "sentinel")); err != nil || string(contents) != "replacement-managed-object" {
		t.Fatalf("replacement sentinel changed: %q %v", contents, err)
	}
	if err := os.Remove(approved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, approved); err != nil {
		t.Fatal(err)
	}
}

func TestAssuranceManagedFileBindSurvivesPathReplacement(t *testing.T) {
	p, r, image := assuranceStore(t)
	fixture := filepath.Join(os.Getenv("PODDER_R3_BASE"), "out", "managed-file-swap")
	if err := os.MkdirAll(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	approved := filepath.Join(fixture, "approved.img")
	moved := filepath.Join(fixture, "approved-moved.img")
	replacement := filepath.Join(fixture, "replacement.img")
	if err := os.WriteFile(approved, []byte("approved-file-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("replacement-file-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	labelAssuranceBindSources(t, approved, replacement)
	p.sessionMountsPinnedHook = func() {
		if err := os.Rename(approved, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, approved); err != nil {
			t.Fatal(err)
		}
	}
	name := "pinned-managed-file"
	result, err := p.CreateContainer(ContainerCreateRequest{
		Image: image, Name: name, Managed: true,
		Labels: map[string]string{"org.podder.assurance": "round2"},
		Binds:  []BindMountSpec{{HostPath: approved, ContainerPath: "/external/image.img", ReadOnly: true}},
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("descriptor-backed managed file create failed: %+v %v", result, err)
	}
	t.Cleanup(func() { r2cleanup(t, p, r, image, name) })
	out, stderr, err := r.Run("podman", "exec", "--", result.ContainerID, "/fixture", "read", "/external/image.img")
	if err != nil || out != "approved-file-object" {
		t.Fatalf("managed file bind observed replacement: %q %s %v", out, stderr, err)
	}
	if contents, err := os.ReadFile(replacement); err != nil || string(contents) != "replacement-file-object" {
		t.Fatalf("replacement file changed: %q %v", contents, err)
	}
	if err := os.Remove(approved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, approved); err != nil {
		t.Fatal(err)
	}
}

func TestAssuranceUnmanagedBindRestartRefusesMissingApprovalWitness(t *testing.T) {
	p, r, image := assuranceStore(t)
	fixture := filepath.Join(os.Getenv("PODDER_R3_BASE"), "out", "unmanaged-restart-swap")
	approved := filepath.Join(fixture, "approved")
	moved := filepath.Join(fixture, "approved-moved")
	replacement := filepath.Join(fixture, "replacement")
	if err := os.MkdirAll(approved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(approved, "sentinel"), []byte("unmanaged-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "sentinel"), []byte("unmanaged-replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	labelAssuranceBindSources(t, approved, replacement)
	name := "unmanaged-bind-restart"
	created, err := p.CreateContainer(ContainerCreateRequest{
		Image: image, Name: name, Managed: false,
		Labels: map[string]string{"org.podder.assurance": "round2"},
		Binds:  []BindMountSpec{{HostPath: approved, ContainerPath: "/external", ReadOnly: true}},
	})
	if err != nil || created == nil || !created.Success {
		t.Fatalf("unmanaged bind create failed: %+v %v", created, err)
	}
	t.Cleanup(func() { r2cleanup(t, p, r, image, name) })
	if err := p.StopContainer(created.ContainerID); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(approved, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, approved); err != nil {
		t.Fatal(err)
	}
	if err := p.StartContainer(created.ContainerID); err == nil || !strings.Contains(err.Error(), "no persisted filesystem-object approval") {
		t.Fatalf("unmanaged bind restart was not refused: %v", err)
	}
	containers, err := p.ListContainers(true)
	if err != nil {
		t.Fatal(err)
	}
	current := findContainerByExactID(containers, created.ContainerID)
	if current == nil || current.State == "running" {
		t.Fatalf("refused unmanaged container changed lifecycle: %+v", current)
	}
	if contents, err := os.ReadFile(filepath.Join(replacement, "sentinel")); err != nil || string(contents) != "unmanaged-replacement" {
		t.Fatalf("replacement sentinel changed: %q %v", contents, err)
	}
	if err := os.Remove(approved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, approved); err != nil {
		t.Fatal(err)
	}
}
