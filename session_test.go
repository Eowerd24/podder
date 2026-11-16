package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type sessionTestRunner struct {
	mu          sync.Mutex
	live        []rawContainer
	calls       []string
	imageTag    string
	buildImage  string
	failRun     bool
	tamper      bool
	buildFail   bool
	storeRoot   string
	infoPayload string
	cancelBuild bool
}

func (r *sessionTestRunner) Run(name string, args ...string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name == "ss" {
		return "", "", nil
	}
	if name != "podman" {
		return "", "", fmt.Errorf("unexpected command %s %v", name, args)
	}
	switch args[0] {
	case "info":
		if r.infoPayload != "" {
			return r.infoPayload, "", nil
		}
		data, _ := json.Marshal(map[string]any{"host": map[string]any{"serviceIsRemote": false, "security": map[string]bool{"rootless": true}}, "store": map[string]string{"graphRoot": r.storeRoot, "runRoot": r.storeRoot, "graphDriverName": "vfs"}})
		return string(data), "", nil
	case "ps":
		data, _ := json.Marshal(r.live)
		return string(data), "", nil
	case "image":
		if args[1] == "inspect" {
			ref := args[len(args)-1]
			if strings.HasPrefix(ref, "localhost/podder-session:") && r.imageTag != ref {
				return "", "", fmt.Errorf("absent")
			}
			id := "sha256:" + strings.Repeat("f", 64)
			if strings.HasPrefix(ref, "localhost/podder-session:") {
				id = r.buildImage
			}
			labels := ""
			if ref == r.imageTag || ref == r.buildImage || sameImageIdentity(ref, r.buildImage) {
				labels = `,"Labels":{"io.podder.session-id":"` + strings.TrimPrefix(ref, "localhost/podder-session:") + `"}`
				if ref != r.imageTag {
					labels = `,"Labels":{"io.podder.session-id":"` + strings.TrimPrefix(r.imageTag, "localhost/podder-session:") + `"}`
				}
			}
			return `[{"Id":"` + id + `"` + labels + `}]`, "", nil
		}
		if args[1] == "rm" {
			r.imageTag = ""
			return "", "", nil
		}
	case "images":
		data, _ := json.Marshal([]Image{{Id: r.buildImage, Names: []string{r.imageTag}}})
		return string(data), "", nil
	case "run":
		if r.failRun {
			return "", "secret environment details", fmt.Errorf("failed")
		}
		c := rawContainer{Id: strings.Repeat("a", 64), State: "running", AutoRemove: true, ImageID: "sha256:" + strings.Repeat("f", 64), Labels: map[string]string{}}
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--name":
				c.Names = []string{args[i+1]}
			case "--label":
				key, value, _ := strings.Cut(args[i+1], "=")
				c.Labels[key] = value
			case "-p":
				m, err := ParsePublishSpec(args[i+1])
				if err != nil {
					return "", "", err
				}
				c.Ports = append(c.Ports, rawPodmanPort{HostIP: m.HostIP, HostPort: m.HostPort, ContainerPort: m.ContainerPort, Protocol: m.Protocol, Range: m.RangeSize})
			}
		}
		if r.buildImage != "" {
			c.ImageID = r.buildImage
		}
		r.live = append(r.live, c)
		return c.Id, "", nil
	case "stop":
		if r.tamper {
			return "", "", fmt.Errorf("stop failed")
		}
		r.live = nil
		return "", "", nil
	}
	return "", "", fmt.Errorf("unexpected command %s %v", name, args)
}
func (r *sessionTestRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "git" {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "clone --no-checkout") {
			path := args[len(args)-1]
			if err := os.MkdirAll(path, 0700); err != nil {
				return "", "", err
			}
			if err := os.WriteFile(filepath.Join(path, "Containerfile"), []byte("FROM scratch\nCMD [\"/app\"]\n"), 0600); err != nil {
				return "", "", err
			}
		}
		if strings.HasSuffix(joined, "rev-parse HEAD") {
			return strings.Repeat("b", 40), "", nil
		}
		return "", "", nil
	}
	if name == "podman" && args[0] == "build" {
		if r.cancelBuild {
			<-ctx.Done()
			return "", "", ctx.Err()
		}
		if r.buildFail {
			return "", "TOKEN=do-not-save", fmt.Errorf("build failure")
		}
		r.mu.Lock()
		r.imageTag = args[4]
		r.buildImage = "sha256:" + strings.Repeat("f", 64)
		r.mu.Unlock()
		return "built successfully", "", nil
	}
	return r.Run(name, args...)
}
func sessionFixture(t *testing.T) (*PodmanService, *sessionTestRunner, DisposableSessionSpec) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	runtime := t.TempDir()
	os.Chmod(runtime, 0700)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	r := &sessionTestRunner{storeRoot: t.TempDir()}
	return &PodmanService{runner: r}, r, DisposableSessionSpec{SchemaVersion: 1, Name: "demo", Image: "alpine", Policy: "temporary", Config: SessionMount{Target: "/app/config"}, Data: SessionMount{Target: "/app/data", ReadWrite: true}}
}
func TestSessionArgvAndValidation(t *testing.T) {
	_, _, s := sessionFixture(t)
	s.Command = []string{"printf", "space ; $(touch no)", "quoted\"value"}
	args, err := BuildSessionRunArgs(s, strings.Repeat("a", 26), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args[len(args)-3:], "|") != strings.Join(s.Command, "|") {
		t.Fatal("argument boundary lost")
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--rm") || strings.Contains(joined, "io.podder.managed") || strings.Contains(joined, "--privileged") {
		t.Fatal(joined)
	}
	cases := []DisposableSessionSpec{s, s, s, s}
	cases[0].Name = "../bad"
	cases[1].Policy = "bad"
	cases[2].Image = "--privileged"
	cases[3].PortMappings = []PortMapping{{HostIP: "0.0.0.0", HostPort: 50000, ContainerPort: 80}}
	for i, c := range cases {
		if err := validateSessionSpec(c); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
func TestSessionPersistencePoliciesAndCleanup(t *testing.T) {
	for _, policy := range []string{"temporary", "configuration", "data"} {
		t.Run(policy, func(t *testing.T) {
			p, r, s := sessionFixture(t)
			s.Policy = policy
			s.Env = map[string]string{"PASSWORD": "never-persist", "MODE": "also-not-persisted"}
			s.Command = []string{"sleep", "10"}
			state, err := p.LaunchDisposableSession(s)
			if err != nil || state.State != "running" {
				t.Fatalf("%+v %v", state, err)
			}
			runtime := filepath.Join(sessionRuntimeDir(), state.ID)
			os.WriteFile(filepath.Join(runtime, "data", "value"), []byte("sample"), 0600)
			raw, _ := os.ReadFile(filepath.Join(sessionDataDir(), "session-state", state.ID+".json"))
			if strings.Contains(string(raw), "never-persist") || strings.Contains(string(raw), "also-not-persisted") {
				t.Fatal("secret persisted")
			}
			recipes, err := p.ListSessionRecipes()
			if err != nil {
				t.Fatal(err)
			}
			if (len(recipes) > 0) != (policy != "temporary") {
				t.Fatal("recipe policy", recipes)
			}
			// Restart reconciliation uses only durable manifests plus live identity labels.
			restarted := &PodmanService{runner: r}
			states, err := restarted.ListDisposableSessions()
			if err != nil || len(states) != 1 || states[0].State != "running" {
				t.Fatalf("reconcile %+v %v", states, err)
			}
			preview, err := p.PreviewSessionCleanup(state.ID)
			if err != nil || preview.ContainerID == "" {
				t.Fatalf("preview %+v %v", preview, err)
			}
			if _, err := p.StopAndCleanupSession(state.ID, false, false); err == nil {
				t.Fatal("cleanup without confirmation")
			}
			stopped, err := p.StopAndCleanupSession(state.ID, true, true)
			if err != nil || stopped.State != "stopped" {
				t.Fatalf("%+v %v", stopped, err)
			}
			if _, err := os.Stat(runtime); !os.IsNotExist(err) {
				t.Fatal("temporary workspace retained")
			}
			if policy != "temporary" {
				if _, err := os.Stat(filepath.Join(sessionDataDir(), "sessions", "demo", "config")); err != nil {
					t.Fatal("config lost")
				}
				if _, err := p.RelaunchSessionRecipe("demo", nil, nil); err == nil {
					t.Fatal("recipe ignored required secrets/command")
				}
				s.Env = map[string]string{"PASSWORD": "new", "MODE": "new"}
				relaunched, err := p.RelaunchSessionRecipe("demo", s.Env, s.Command)
				if err != nil || relaunched.State != "running" {
					t.Fatalf("relaunch %+v %v", relaunched, err)
				}
			}
			for _, c := range r.calls {
				if strings.Contains(c, "prune") || strings.Contains(c, "image rm") {
					t.Fatal("unowned image deleted")
				}
			}
		})
	}
}
func TestSessionExternalMountAndSymlinkCleanupSafety(t *testing.T) {
	p, _, s := sessionFixture(t)
	external := t.TempDir()
	marker := filepath.Join(external, "keep")
	os.WriteFile(marker, []byte("keep"), 0600)
	s.ExternalBinds = []BindMountSpec{{HostPath: external, ContainerPath: "/external", ReadOnly: true}}
	s.AllowExternalBinds = true
	preview, err := p.PreviewDisposableSession(s)
	if err != nil {
		t.Fatal(err)
	}
	s.LaunchID = preview.Spec.LaunchID
	s.ExternalBinds = preview.Spec.ExternalBinds
	s.ExternalBindApprovals = preview.Spec.ExternalBindApprovals
	state, err := p.LaunchDisposableSession(s)
	if err != nil {
		t.Fatal(err)
	}
	os.Symlink(external, filepath.Join(sessionRuntimeDir(), state.ID, "escape"))
	if result, err := p.StopAndCleanupSession(state.ID, true, false); err != nil || result.State != "stopped" {
		t.Fatalf("%+v %v", result, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatal("external data changed")
	}
}
func TestSessionIdentityAndOwnershipFailClosed(t *testing.T) {
	p, r, s := sessionFixture(t)
	state, err := p.LaunchDisposableSession(s)
	if err != nil {
		t.Fatal(err)
	}
	r.live[0].Labels["com.docker.compose.project"] = "unrelated"
	if _, err := p.StopAndCleanupSession(state.ID, true, false); err == nil {
		t.Fatal("mutated third-party ownership")
	}
	delete(r.live[0].Labels, "com.docker.compose.project")
	r.live = nil
	root, err := privateRoot(sessionRuntimeDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	writeRootJSON(root, filepath.Join(state.ID, "owner.json"), map[string]string{"id": state.ID, "token": "wrong"})
	result, err := p.StopAndCleanupSession(state.ID, true, false)
	if err != nil || result.State != "cleanup_failed" {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestSessionPreviewAndPortRace(t *testing.T) {
	p, r, s := sessionFixture(t)
	s.PortMappings = []PortMapping{{HostIP: "127.0.0.1", HostPort: 50000, ContainerPort: 80, Protocol: "tcp"}}
	s.Env = map[string]string{"TOKEN": "secret"}
	preview, err := p.PreviewDisposableSession(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(preview.Argv, " "), "secret") || !strings.Contains(strings.Join(preview.Argv, " "), "--mount") {
		t.Fatal("preview missing mounts or leaking values")
	}
	s.LaunchID = preview.Spec.LaunchID
	r.failRun = true
	state, err := p.LaunchDisposableSession(s)
	if err != nil || state.State != "cleanup_pending" || strings.Contains(state.Message, "secret") {
		t.Fatalf("%+v %v", state, err)
	}
	if _, err := p.LaunchDisposableSession(s); err == nil {
		t.Fatal("reused launch identity")
	}
}
func TestSessionGitHubValidationAndBuild(t *testing.T) {
	p, r, s := sessionFixture(t)
	s.GitHub = &GitHubSource{URL: "https://github.com/example/demo", Commit: strings.Repeat("b", 40)}
	if _, err := p.LaunchDisposableSession(s); err == nil {
		t.Fatal("unreviewed repository executed")
	}
	for _, u := range []string{"http://github.com/a/b", "https://github.com/a/b?token=x", "https://user:secret@github.com/a/b", "https://evil.test/a/b", "https://github.com/a/b/../../evil"} {
		bad := *s.GitHub
		bad.URL = u
		if err := validateGitHubSource(bad); err == nil {
			t.Fatal(u)
		}
	}
	state, err := p.ReviewGitHubSession(s)
	if err != nil || state.State != "draft" || state.BuildDigest == "" {
		t.Fatalf("%+v %v", state, err)
	}
	if _, err := p.StartReviewedSessionBuild(state.ID, false, false); err == nil {
		t.Fatal("unapproved build")
	}
	if _, err := p.StartReviewedSessionBuild(state.ID, true, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for sessionBuildActive(state.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	state, err = loadSessionState(state.ID)
	if err != nil || state.State != "built" || !state.ImageOwned {
		t.Fatalf("%+v %v", state, err)
	}
	states, err := p.ListDisposableSessions()
	if err != nil || states[0].State != "built" {
		t.Fatal("reconciliation lost built state")
	}
	launched, err := p.LaunchBuiltSession(state.ID, nil, nil)
	if err != nil || launched.State != "running" {
		t.Fatalf("%+v %v", launched, err)
	}
	stopped, err := p.StopAndCleanupSession(state.ID, true, true)
	if err != nil || stopped.State != "stopped" || stopped.ImageOwned || r.imageTag != "" {
		t.Fatalf("%+v %v", stopped, err)
	}
}
func TestSessionBuildCancelAndFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			p, r, s := sessionFixture(t)
			s.GitHub = &GitHubSource{URL: "https://github.com/example/demo", Commit: strings.Repeat("b", 40)}
			state, err := p.ReviewGitHubSession(s)
			if err != nil {
				t.Fatal(err)
			}
			r.cancelBuild = cancelled
			r.buildFail = !cancelled
			if _, err := p.StartReviewedSessionBuild(state.ID, true, true); err != nil {
				t.Fatal(err)
			}
			if cancelled {
				if _, err := p.StopAndCleanupSession(state.ID, true, false); err == nil {
					t.Fatal("cleanup while building")
				}
				if err := p.CancelSessionBuild(state.ID); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(time.Second)
			for sessionBuildActive(state.ID) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			state, err = loadSessionState(state.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "build_failed"
			if cancelled {
				want = "build_cancelled"
			}
			if state.State != want || strings.Contains(state.BuildLog, "do-not-save") {
				t.Fatalf("%+v", state)
			}
		})
	}
}
func TestSessionRecipeExportImportSecrets(t *testing.T) {
	p, _, s := sessionFixture(t)
	s.Policy = "configuration"
	s.Env = map[string]string{"PASSWORD": "secret"}
	if err := p.SaveSessionRecipe(s); err != nil {
		t.Fatal(err)
	}
	text, err := p.ExportSessionRecipe(s.Name)
	if err != nil || strings.Contains(text, "secret") {
		t.Fatal(text, err)
	}
	imported, err := p.ImportSessionRecipe(text)
	if err != nil || len(imported.RequiredEnv) != 1 {
		t.Fatalf("%+v %v", imported, err)
	}
	if err := p.SetSessionRecipeFavorite(s.Name, true); err != nil {
		t.Fatal(err)
	}
	recipes, _ := p.ListSessionRecipes()
	if !recipes[0].Favorite || len(recipes[0].RequiredEnv) != 1 {
		t.Fatal("favorite lost requirements")
	}
}

func TestSessionMissingBuildDefinitionAndChangedSource(t *testing.T) {
	p, runner, s := sessionFixture(t)
	s.GitHub = &GitHubSource{URL: "https://github.com/example/demo", Commit: strings.Repeat("b", 40)}
	reviewed, err := p.ReviewGitHubSession(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionRuntimeDir(), reviewed.ID, "repo", "Containerfile")
	if err := os.WriteFile(path, []byte("FROM unexpected"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.StartReviewedSessionBuild(reviewed.ID, true, true); err == nil {
		t.Fatal("changed build definition executed")
	}
	// A clean immutable checkout without a supported build file gets guidance.
	p.runner = noBuildFileRunner{inner: runner}
	failed, err := p.ReviewGitHubSession(s)
	if err != nil || failed.State != "cleanup_pending" || !strings.Contains(failed.Message, "no readable Containerfile") {
		t.Fatalf("%+v %v", failed, err)
	}
	p.runner = authFailureRunner{inner: runner}
	failed, err = p.ReviewGitHubSession(s)
	if err != nil || failed.State != "cleanup_pending" || !strings.Contains(failed.Message, "auth-required") {
		t.Fatalf("%+v %v", failed, err)
	}
}

type noBuildFileRunner struct{ inner *sessionTestRunner }

func (r noBuildFileRunner) Run(name string, args ...string) (string, string, error) {
	return r.inner.Run(name, args...)
}
func (r noBuildFileRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	out, errs, err := r.inner.RunContext(ctx, name, args...)
	if name == "git" && strings.Contains(strings.Join(args, " "), "clone --no-checkout") {
		os.Remove(filepath.Join(args[len(args)-1], "Containerfile"))
	}
	return out, errs, err
}

type authFailureRunner struct{ inner *sessionTestRunner }

func (r authFailureRunner) Run(name string, args ...string) (string, string, error) {
	if name == "podman" {
		return r.inner.Run(name, args...)
	}
	return "", "authentication required", fmt.Errorf("authentication required")
}
func TestSessionInterruptedBuildAndOrphanLabels(t *testing.T) {
	p, r, s := sessionFixture(t)
	runtimeID, err := p.sessionRuntimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	state := SessionRuntimeState{RuntimeIdentity: runtimeID, SchemaVersion: 1, ID: newSessionID(), Token: newSessionID(), State: "building", Spec: s, ImageTag: "localhost/podder-session:unknown"}
	if err := saveSessionState(state); err != nil {
		t.Fatal(err)
	}
	listed, err := p.ListDisposableSessions()
	if err != nil || !listed[0].InterruptedBuild {
		t.Fatalf("%+v %v", listed, err)
	}
	if _, err := p.StopAndCleanupSession(state.ID, true, false); err == nil {
		t.Fatal("interrupted build cleanup without verification")
	}
	if err := p.AcknowledgeInterruptedSessionBuild(state.ID, false); err == nil {
		t.Fatal("missing acknowledgment accepted")
	}
	if err := p.AcknowledgeInterruptedSessionBuild(state.ID, true); err != nil {
		t.Fatal(err)
	}
	r.live = []rawContainer{{Id: "untracked", Names: []string{"orphan"}, Labels: map[string]string{"io.podder.session": "true", "io.podder.session-id": newSessionID()}}}
	listed, err = p.ListDisposableSessions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range listed {
		if entry.State == "untracked" {
			found = true
			if _, err := p.StopAndCleanupSession(entry.ID, true, false); err == nil {
				t.Fatal("unowned labeled container mutated")
			}
		}
	}
	if !found {
		t.Fatal("unjournaled session not reported")
	}
}
func TestSessionPreexistingBuildImageIsRetained(t *testing.T) {
	p, r, s := sessionFixture(t)
	r.buildImage = "sha256:" + strings.Repeat("f", 64)
	s.GitHub = &GitHubSource{URL: "https://github.com/example/demo", Commit: strings.Repeat("b", 40)}
	state, err := p.ReviewGitHubSession(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.StartReviewedSessionBuild(state.ID, true, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for sessionBuildActive(state.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	state, err = loadSessionState(state.ID)
	if err != nil || state.State != "built" || state.ImageOwned {
		t.Fatalf("preexisting image ownership %+v %v", state, err)
	}
	if _, err := p.LaunchBuiltSession(state.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.StopAndCleanupSession(state.ID, true, true); err != nil {
		t.Fatal(err)
	}
	if r.imageTag == "" {
		t.Fatal("preexisting image/tag deleted")
	}
}
func TestSessionStarterDoesNotOverwriteConfiguration(t *testing.T) {
	p, _, _ := sessionFixture(t)
	spec := p.SessionStarterProfiles()[0]
	spec.PortMappings = nil
	spec.Web = nil
	state, err := p.LaunchDisposableSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(sessionDataDir(), "sessions", spec.Name, "config", "config.yaml")
	value, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(value), "Say hello") {
		t.Fatalf("%s %v", value, err)
	}
	if _, err := p.StopAndCleanupSession(state.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("operator configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.RelaunchSessionRecipe(spec.Name, nil, nil); err != nil {
		t.Fatal(err)
	}
	value, _ = os.ReadFile(config)
	if string(value) != "operator configuration" {
		t.Fatal("existing config overwritten")
	}
}

func TestSessionImageIdentityRepresentations(t *testing.T) {
	if !sameImageIdentity("sha256:"+strings.Repeat("a", 64), strings.Repeat("a", 64)) || sameImageIdentity("sha256:abcd", "abc") || sameImageIdentity("", "") {
		t.Fatal("image identity normalization weakened exact comparison")
	}
	p, r, s := sessionFixture(t)
	state, err := p.LaunchDisposableSession(s)
	if err != nil {
		t.Fatal(err)
	}
	r.live[0].ImageID = strings.TrimPrefix(r.live[0].ImageID, "sha256:")
	if _, err := p.StopAndCleanupSession(state.ID, true, false); err != nil {
		t.Fatal(err)
	}
}

func TestSessionKeepIDIsExplicitAndIsolatedFromManagedBuilder(t *testing.T) {
	_, _, s := sessionFixture(t)
	plain, err := BuildSessionRunArgs(s, strings.Repeat("a", 26), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(plain, " "), "--userns") {
		t.Fatal("UID mapping enabled without opt-in")
	}
	s.KeepID = true
	mapped, err := BuildSessionRunArgs(s, strings.Repeat("a", 26), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(mapped, " "), "--userns=keep-id") {
		t.Fatal("explicit UID mapping was dropped")
	}
}
