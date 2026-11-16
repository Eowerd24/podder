package main

import (
	"bytes"
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

func mustState(t *testing.T, p *PodmanService, s DisposableSessionSpec) *SessionRuntimeState {
	t.Helper()
	state, err := p.LaunchDisposableSession(s)
	if err != nil || state.State != "running" {
		t.Fatalf("launch: %+v %v", state, err)
	}
	return state
}
func assertWorkspace(t *testing.T, state *SessionRuntimeState) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(sessionRuntimeDir(), state.ID, "owner.json")); err != nil {
		t.Fatalf("original workspace lost: %v", err)
	}
}
func TestSessionStoreSwitchPreservesOriginalRunningFiles(t *testing.T) {
	p, r, s := sessionFixture(t)
	state := mustState(t, p, s)
	originalStore, originalLive := r.storeRoot, r.live
	journal := filepath.Join(sessionDataDir(), "session-state", state.ID+".json")
	before, _ := os.ReadFile(journal)
	r.storeRoot = t.TempDir()
	r.live = nil
	r.calls = nil
	if _, err := p.PreviewSessionCleanup(state.ID); err == nil {
		t.Fatal("preview accepted different local rootless store")
	}
	if _, err := p.StopAndCleanupSession(state.ID, true, true); err == nil {
		t.Fatal("different store cleanup accepted")
	}
	if err := p.cleanupSessionImage(state); err == nil {
		t.Fatal("different store image cleanup accepted")
	}
	if _, err := p.ListDisposableSessions(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(journal)
	if !bytes.Equal(before, after) {
		t.Fatal("store switch rewrote original journal")
	}
	assertWorkspace(t, state)
	for _, call := range r.calls {
		if strings.Contains(call, " stop ") || strings.Contains(call, "image rm") {
			t.Fatal(call)
		}
	}
	r.storeRoot = originalStore
	r.live = originalLive
	result, err := p.StopAndCleanupSession(state.ID, true, false)
	if err != nil || result.State != "stopped" {
		t.Fatalf("matching store: %+v %v", result, err)
	}
}
func TestSessionMissingMalformedAndLegacyRuntimeIdentity(t *testing.T) {
	for _, identity := range []string{"", "v1:bad", "v2:" + strings.Repeat("a", 64), "v1:" + strings.Repeat("a", 64)} {
		t.Run(identity, func(t *testing.T) {
			p, _, s := sessionFixture(t)
			state := mustState(t, p, s)
			state.RuntimeIdentity = identity
			if err := saveSessionState(*state); err != nil {
				t.Fatal(err)
			}
			if _, err := p.PreviewSessionCleanup(state.ID); err == nil {
				t.Fatal("unsafe preview")
			}
			if _, err := p.StopAndCleanupSession(state.ID, true, true); err == nil {
				t.Fatal("unsafe cleanup")
			}
			assertWorkspace(t, state)
		})
	}
	for _, payload := range []string{`{}`, `{"host":{"serviceIsRemote":false,"security":{"rootless":true}},"store":{"graphRoot":"relative","runRoot":"/tmp","graphDriverName":"vfs"}}`, `malformed`} {
		t.Run(payload, func(t *testing.T) {
			p, r, s := sessionFixture(t)
			state := mustState(t, p, s)
			r.infoPayload = payload
			if _, err := p.StopAndCleanupSession(state.ID, true, true); err == nil {
				t.Fatal("unverifiable context accepted")
			}
			assertWorkspace(t, state)
		})
	}
}
func TestSessionDirectInterruptedCleanupRequiresAcknowledgment(t *testing.T) {
	for _, previewFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(previewFirst), func(t *testing.T) {
			p, r, s := sessionFixture(t)
			state := mustState(t, p, s)
			r.live = nil
			state.State = "building"
			state.InterruptedBuild = false
			state.BuildOperation = newSessionID()
			if err := saveSessionState(*state); err != nil {
				t.Fatal(err)
			}
			restarted := &PodmanService{runner: r}
			if previewFirst {
				preview, err := restarted.PreviewSessionCleanup(state.ID)
				if err != nil || !preview.NeedBuildConfirmation {
					t.Fatalf("%+v %v", preview, err)
				}
			}
			if _, err := restarted.StopAndCleanupSession(state.ID, true, false); err == nil {
				t.Fatal("cleanup bypassed interrupted acknowledgment")
			}
			assertWorkspace(t, state)
			if err := restarted.AcknowledgeInterruptedSessionBuild(state.ID, true); err != nil {
				t.Fatal(err)
			}
			result, err := restarted.StopAndCleanupSession(state.ID, true, false)
			if err != nil || result.State != "stopped" {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}
func TestSessionLateBuildCompletionCannotOverwriteRecovery(t *testing.T) {
	p, r, s := sessionFixture(t)
	state := mustState(t, p, s)
	r.live = nil
	state.State = "building"
	state.BuildOperation = newSessionID()
	if err := saveSessionState(*state); err != nil {
		t.Fatal(err)
	}
	late := *state
	late.State = "built"
	// A stale operation cannot overwrite a newer builder even with the same session token.
	current := *state
	current.BuildOperation = newSessionID()
	if err := saveSessionState(current); err != nil {
		t.Fatal(err)
	}
	if err := completeSessionBuild(late); err == nil {
		t.Fatal("stale build replaced current operation")
	}
	if err := p.AcknowledgeInterruptedSessionBuild(state.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := completeSessionBuild(late); err == nil {
		t.Fatal("late build replaced acknowledgment")
	}
	if _, err := p.StopAndCleanupSession(state.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if err := completeSessionBuild(late); err == nil {
		t.Fatal("late worker recreated cleaned journal")
	}
	if _, err := loadSessionState(state.ID); !os.IsNotExist(err) {
		t.Fatalf("journal recreated: %v", err)
	}
}

// This helper is a separate OS process, not an in-memory cancellation mock.
// The inherited child uses the production command runner and retains the lock
// descriptor after its parent is killed, until its local release file appears.
func TestSessionOperationProcessHelper(t *testing.T) {
	mode := os.Getenv("PODDER_TEST_LOCK_MODE")
	if mode == "" {
		return
	}
	dir := os.Getenv("PODDER_TEST_LOCK_DIR")
	if os.Args[len(os.Args)-1] == "descendant" {
		if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
			os.Exit(3)
		}
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				return
			}
		}
		os.Exit(4)
	}
	if mode == "build" {
		inner := &sessionTestRunner{storeRoot: os.Getenv("PODDER_TEST_STORE")}
		service := &PodmanService{runner: processBuildCommandRunner{inner}}
		id := os.Getenv("PODDER_TEST_SESSION_ID")
		if _, err := service.StartReviewedSessionBuild(id, true, true); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(8)
		}
		for deadline := time.Now().Add(10 * time.Second); sessionBuildActive(id); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(dir, "cancel")); err == nil {
				_ = service.CancelSessionBuild(id)
			}
			if time.Now().After(deadline) {
				os.Exit(9)
			}
		}
		return
	}
	file, err := acquireSessionOperation(os.Getenv("PODDER_TEST_SESSION_ID"))
	if err != nil {
		os.Exit(5)
	}
	defer file.Close()
	if mode == "inherit" {
		_, _, err := (execCommandRunner{}).RunContext(sessionCommandContext(context.Background(), file), os.Args[0], "-test.run=^TestSessionOperationProcessHelper$", "--", "descendant")
		if err != nil {
			os.Exit(6)
		}
		return
	}
	os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			return
		}
	}
	os.Exit(7)
}
func waitFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatal("fixture did not signal ready")
}
func TestSessionAnotherProcessAndOrphanedCommandBlockCleanup(t *testing.T) {
	for _, mode := range []string{"hold", "inherit"} {
		t.Run(mode, func(t *testing.T) {
			p, r, s := sessionFixture(t)
			state := mustState(t, p, s)
			r.live = nil
			state.State = "building"
			if err := saveSessionState(*state); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestSessionOperationProcessHelper$")
			cmd.Env = append(os.Environ(), "PODDER_TEST_LOCK_MODE="+mode, "PODDER_TEST_LOCK_DIR="+dir, "PODDER_TEST_SESSION_ID="+state.ID)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.WriteFile(filepath.Join(dir, "release"), nil, 0600); cmd.Process.Kill() })
			waitFile(t, filepath.Join(dir, "ready"))
			check := func() {
				t.Helper()
				if _, err := p.PreviewSessionCleanup(state.ID); err == nil {
					t.Fatal("preview while another process owns operation")
				}
				if _, err := p.StopAndCleanupSession(state.ID, true, false); err == nil {
					t.Fatal("cleanup while another process owns operation")
				}
				if err := p.AcknowledgeInterruptedSessionBuild(state.ID, true); err == nil {
					t.Fatal("acknowledgment overrode active owner")
				}
				assertWorkspace(t, state)
				listed, err := p.ListDisposableSessions()
				if err != nil || listed[0].InterruptedBuild || listed[0].State != "building" {
					t.Fatalf("active cross-process build misclassified: %+v %v", listed, err)
				}
			}
			check()
			if mode == "inherit" {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				cmd.Wait()
				check()
			}
			os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
			if mode == "hold" {
				if err := cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			}
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				lock, err := acquireSessionOperation(state.ID)
				if err == nil {
					lock.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("operation lock never released")
				}
			}
			preview, err := p.PreviewSessionCleanup(state.ID)
			if err != nil || !preview.NeedBuildConfirmation {
				t.Fatalf("crash recovery: %+v %v", preview, err)
			}
			if _, err := p.StopAndCleanupSession(state.ID, true, false); err == nil {
				t.Fatal("crash silently acknowledged")
			}
		})
	}
}
func TestSessionSourceSecretsRejectedEverywhere(t *testing.T) {
	p, r, s := sessionFixture(t)
	s.Policy = "configuration"
	for _, url := range []string{"https://user:SOURCE_SENTINEL@github.com/owner/repo", "https://github.com/owner/repo?token=SOURCE_SENTINEL", "https://github.com/owner/repo?credential=SOURCE_SENTINEL", "https://github.com/%SOURCE_SENTINEL", "https://github.com/../SOURCE_SENTINEL"} {
		s.GitHub = &GitHubSource{URL: url, Commit: strings.Repeat("b", 40)}
		checkErr := func(err error) {
			t.Helper()
			if err == nil || strings.Contains(err.Error(), "SOURCE_SENTINEL") {
				t.Fatalf("unsafe metadata accepted/leaked: %v", err)
			}
		}
		checkErr(p.SaveSessionRecipe(s))
		_, err := p.PreviewDisposableSession(s)
		checkErr(err)
		_, err = p.LaunchDisposableSession(s)
		checkErr(err)
		_, err = p.ReviewGitHubSession(s)
		checkErr(err)
		_, err = exportRecipe(s)
		checkErr(err)
		payload, _ := json.Marshal(s)
		_, err = p.ImportSessionRecipe(string(payload))
		checkErr(err)
		var out bytes.Buffer
		err = runSessionCLI([]string{"preview"}, bytes.NewReader(payload), &out, p)
		checkErr(err)
		if strings.Contains(out.String(), "SOURCE_SENTINEL") {
			t.Fatal("secret in CLI output")
		}
	}
	for _, call := range r.calls {
		if strings.Contains(call, "SOURCE_SENTINEL") {
			t.Fatal("secret reached command runner")
		}
	}
	recipes, err := p.ListSessionRecipes()
	if err != nil || len(recipes) != 0 {
		t.Fatal("unsafe recipe persisted")
	}
	// Old unsafe records are retained byte-for-byte and unavailable for listing/export.
	root, err := recipeRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writeRootJSON(root, launcherFile(s.Name), s); err != nil {
		t.Fatal(err)
	}
	before, _ := root.ReadFile(launcherFile(s.Name))
	value, err := p.ExportSessionRecipe(s.Name)
	if err == nil || strings.Contains(value+err.Error(), "SOURCE_SENTINEL") {
		t.Fatalf("legacy export leaked: %q %v", value, err)
	}
	if _, err := p.ListSessionRecipes(); err == nil {
		t.Fatal("legacy source exposed")
	}
	after, _ := root.ReadFile(launcherFile(s.Name))
	if !bytes.Equal(before, after) {
		t.Fatal("unsafe legacy file rewritten")
	}
	root.Remove(launcherFile(s.Name))
	s.GitHub = &GitHubSource{URL: "https://github.com/owner/repo.git", Commit: strings.Repeat("b", 40)}
	s.Env = map[string]string{"TOKEN": "ENV_SENTINEL"}
	if err := p.SaveSessionRecipe(s); err != nil {
		t.Fatal(err)
	}
	value, err = p.ExportSessionRecipe(s.Name)
	if err != nil || strings.Contains(value, "ENV_SENTINEL") {
		t.Fatalf("valid export: %q %v", value, err)
	}
	draft, err := p.ImportSessionRecipe(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PreviewDisposableSession(*draft); err == nil {
		t.Fatal("environment re-entry bypassed")
	}
}

// Git is real and entirely local here. Only Podman is mocked; generated fixture
// source is committed, checked out detached and checked clean before approval.
type localFixtureGitRunner struct {
	inner  *sessionTestRunner
	source string
	builds int
}

func (r *localFixtureGitRunner) Run(name string, args ...string) (string, string, error) {
	return r.inner.Run(name, args...)
}
func (r *localFixtureGitRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "git" {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "clone --no-checkout") {
			dest := args[len(args)-1]
			out, errs, err := (execCommandRunner{}).RunContext(ctx, "git", "-c", "core.hooksPath="+os.DevNull, "clone", "--no-hardlinks", "--no-checkout", "--", r.source, dest)
			// Host-created sibling material must never be a build context.
			os.MkdirAll(filepath.Join(filepath.Dir(dest), "sibling"), 0700)
			os.WriteFile(filepath.Join(filepath.Dir(dest), "sibling", "Containerfile"), []byte("FROM scratch\n"), 0600)
			return out, errs, err
		}
		if strings.Contains(joined, "fetch --depth=1") {
			return "", "", nil
		}
		return (execCommandRunner{}).RunContext(ctx, name, args...)
	}
	if name == "podman" && args[0] == "build" {
		r.builds++
	}
	return r.inner.RunContext(ctx, name, args...)
}
func committedFixture(t *testing.T, kind string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, errs, err := (execCommandRunner{}).RunContext(context.Background(), "git", gitSafeArgs(append([]string{"-C", repo}, args...)...)...)
		if err != nil {
			t.Fatalf("local git: %v %s", err, errs)
		}
		return strings.TrimSpace(out)
	}
	git("init", "--quiet")
	for _, dir := range []string{"app/definitions", "context"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"Containerfile", "app/Containerfile", "app/definitions/Buildfile", "context/Containerfile"} {
		if err := os.WriteFile(filepath.Join(repo, file), []byte("FROM scratch\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, path string) {
		t.Helper()
		os.RemoveAll(filepath.Join(repo, path))
		if err := os.Symlink(target, filepath.Join(repo, path)); err != nil {
			t.Fatal(err)
		}
	}
	switch kind {
	case "parent":
		link("..", "context")
	case "sibling":
		link("../sibling", "context")
	case "external-definition":
		link("../Containerfile", "context/Containerfile")
	case "contained":
		link("app", "context")
		link("definitions/Buildfile", "app/Containerfile")
	}
	git("add", "--all")
	git("-c", "user.name=Podder Fixture", "-c", "user.email=fixture@invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "local regression fixture")
	return repo, git("rev-parse", "HEAD")
}
func waitBuild(t *testing.T, id string) *SessionRuntimeState {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); sessionBuildActive(id); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("build worker did not finish")
		}
	}
	state, err := loadSessionState(id)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func TestSessionCommittedSymlinkBuildContainment(t *testing.T) {
	for _, kind := range []string{"parent", "sibling", "external-definition", "contained"} {
		t.Run(kind, func(t *testing.T) {
			p, inner, s := sessionFixture(t)
			repo, commit := committedFixture(t, kind)
			r := &localFixtureGitRunner{inner: inner, source: repo}
			p.runner = r
			file := "Containerfile"
			if kind == "parent" {
				file = "repo/Containerfile"
			}
			s.GitHub = &GitHubSource{URL: "https://github.com/example/fixture", Commit: commit, Subdirectory: "context", Containerfile: file}
			state, err := p.ReviewGitHubSession(s)
			if kind != "contained" {
				if err == nil && state.State == "draft" {
					t.Fatal("escaping checkout approved")
				}
				if state != nil {
					if _, err := p.StartReviewedSessionBuild(state.ID, true, true); err == nil {
						t.Fatal("escaping checkout built")
					}
				}
				if r.builds != 0 {
					t.Fatal("rejected input reached build runner")
				}
				return
			}
			if err != nil || state.State != "draft" {
				t.Fatalf("contained review: %+v %v", state, err)
			}
			if _, err := p.StartReviewedSessionBuild(state.ID, true, true); err != nil {
				t.Fatal(err)
			}
			state = waitBuild(t, state.ID)
			if state.State != "built" || r.builds != 1 {
				t.Fatalf("contained build: %+v calls=%d", state, r.builds)
			}
			// Revalidation refuses even a clean commit's redirected definition/context.
			state.State = "draft"
			if err := saveSessionState(*state); err != nil {
				t.Fatal(err)
			}
			context := filepath.Join(sessionRuntimeDir(), state.ID, "repo", "context")
			os.Remove(context)
			os.Symlink("..", context)
			if _, err := p.StartReviewedSessionBuild(state.ID, true, true); err == nil {
				t.Fatal("context redirect after review accepted")
			}
			if r.builds != 1 {
				t.Fatal("redirect reached build runner")
			}
		})
	}
}
func TestSessionImportPreservesConfigurationWithoutConsent(t *testing.T) {
	p, _, s := sessionFixture(t)
	s.Policy = "data"
	s.Favorite = true
	s.AllowExternalBinds = true
	s.AllowNetworkExposure = true
	s.ExternalBinds = []BindMountSpec{{HostPath: t.TempDir(), ContainerPath: "/external", ReadOnly: true}}
	s.PortMappings = []PortMapping{{HostIP: "0.0.0.0", HostPort: 50000, ContainerPort: 80, Protocol: "tcp"}}
	raw, err := exportRecipe(s)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := p.ImportSessionRecipe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if draft.AllowExternalBinds || draft.AllowNetworkExposure || !draft.Favorite || len(draft.ExternalBinds) != 1 {
		t.Fatalf("import changed config/granted consent: %+v", draft)
	}
	if _, err := p.PreviewDisposableSession(*draft); err == nil {
		t.Fatal("unapproved imported host access previewed")
	}
	draft.AllowExternalBinds = true
	draft.AllowNetworkExposure = true
	if _, err := p.PreviewDisposableSession(*draft); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCLIOperationResults(t *testing.T) {
	for _, kind := range []string{"launch-failed", "launch-success", "launch-warning", "cleanup-failed", "cleanup-success", "build-failed", "build-cancelled", "build-success"} {
		t.Run(kind, func(t *testing.T) {
			p, r, s := sessionFixture(t)
			s.Env = map[string]string{"TOKEN": "CLI_SECRET_SENTINEL"}
			var out bytes.Buffer
			var err error
			want := ""
			wantFailure := false
			switch kind {
			case "launch-failed", "launch-success", "launch-warning":
				r.failRun = kind == "launch-failed"
				want = "running"
				wantFailure = r.failRun
				if r.failRun {
					want = "cleanup_pending"
				}
				if kind == "launch-warning" {
					s.Policy = "configuration"
					root, e := recipeRoot()
					if e != nil {
						t.Fatal(e)
					}
					root.Mkdir(launcherFile(s.Name), 0700)
					root.Close()
				}
				raw, _ := json.Marshal(s)
				err = runSessionCLI([]string{"launch", "--confirm"}, bytes.NewReader(raw), &out, p)
			case "cleanup-failed", "cleanup-success":
				state := mustState(t, p, s)
				r.tamper = kind == "cleanup-failed"
				wantFailure = r.tamper
				want = "stopped"
				if r.tamper {
					want = "cleanup_failed"
				}
				err = runSessionCLI([]string{"stop", state.ID, "--confirm"}, strings.NewReader(""), &out, p)
			default:
				s.GitHub = &GitHubSource{URL: "https://github.com/example/fixture", Commit: strings.Repeat("b", 40)}
				state, e := p.ReviewGitHubSession(s)
				if e != nil {
					t.Fatal(e)
				}
				r.buildFail = kind == "build-failed"
				r.cancelBuild = kind == "build-cancelled"
				wantFailure = kind != "build-success"
				want = "built"
				if r.buildFail {
					want = "build_failed"
				}
				if r.cancelBuild {
					want = "build_cancelled"
				}
				var done = make(chan struct{})
				if r.cancelBuild {
					go func() {
						defer close(done)
						for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
							if p.CancelSessionBuild(state.ID) == nil {
								return
							}
						}
					}()
				} else {
					close(done)
				}
				err = runSessionCLI([]string{"build", state.ID, "--confirm", "--allow-network"}, strings.NewReader(""), &out, p)
				<-done
			}
			if (err != nil) != wantFailure {
				t.Fatalf("return error: %v output: %s", err, out.String())
			}
			var state SessionRuntimeState
			if e := json.Unmarshal(out.Bytes(), &state); e != nil || state.State != want {
				t.Fatalf("structured state: %+v %v", state, e)
			}
			if kind == "launch-warning" && state.Message == "" {
				t.Fatal("warning fixture did not produce warning")
			}
			if strings.Contains(out.String()+fmt.Sprint(err), "CLI_SECRET_SENTINEL") {
				t.Fatal("secret leaked")
			}
		})
	}
}

// Enter the real production main function in a disposable subprocess. Only its
// runner is replaced. main's os.Exit(1) and successful return are both exercised.
func TestSessionCLIProcessHelper(t *testing.T) {
	kind := os.Getenv("PODDER_TEST_CLI_KIND")
	if kind == "" {
		return
	}
	r := &sessionTestRunner{storeRoot: os.Getenv("PODDER_TEST_STORE"), failRun: kind == "launch-failed", buildFail: kind == "build-failed", cancelBuild: kind == "build-cancelled", tamper: kind == "cleanup-failed"}
	if raw := os.Getenv("PODDER_TEST_LIVE"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &r.live); err != nil {
			os.Exit(3)
		}
	}
	defaultCommandRunner = r
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("PODDER_TEST_CLI_ARGS")), &args); err != nil {
		os.Exit(4)
	}
	if kind == "build-cancelled" {
		go func() {
			p := &PodmanService{}
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
				if p.CancelSessionBuild(args[1]) == nil {
					return
				}
			}
		}()
	}
	os.Args = append([]string{"podder", "session"}, args...)
	main()
	os.Exit(0)
}
func TestSessionCLIActualProcessExitStatus(t *testing.T) {
	for _, kind := range []string{"launch-failed", "launch-success", "cleanup-failed", "cleanup-success", "build-failed", "build-cancelled", "build-success"} {
		t.Run(kind, func(t *testing.T) {
			p, r, s := sessionFixture(t)
			s.Env = map[string]string{"TOKEN": "PROCESS_SECRET_SENTINEL"}
			var input []byte
			var args []string
			wantFailure := true
			want := ""
			switch kind {
			case "launch-failed", "launch-success":
				args = []string{"launch", "--confirm"}
				input, _ = json.Marshal(s)
				want = "cleanup_pending"
				if kind == "launch-success" {
					wantFailure = false
					want = "running"
				}
			case "cleanup-failed", "cleanup-success":
				state := mustState(t, p, s)
				args = []string{"stop", state.ID, "--confirm"}
				want = "cleanup_failed"
				if kind == "cleanup-success" {
					wantFailure = false
					want = "stopped"
				}
			default:
				s.GitHub = &GitHubSource{URL: "https://github.com/example/fixture", Commit: strings.Repeat("b", 40)}
				state, err := p.ReviewGitHubSession(s)
				if err != nil {
					t.Fatal(err)
				}
				args = []string{"build", state.ID, "--confirm", "--allow-network"}
				want = "build_failed"
				if kind == "build-success" {
					wantFailure = false
					want = "built"
				}
				if kind == "build-cancelled" {
					want = "build_cancelled"
				}
			}
			argsJSON, _ := json.Marshal(args)
			liveJSON, _ := json.Marshal(r.live)
			cmd := exec.Command(os.Args[0], "-test.run=^TestSessionCLIProcessHelper$")
			cmd.Env = append(os.Environ(), "PODDER_TEST_CLI_KIND="+kind, "PODDER_TEST_STORE="+r.storeRoot, "PODDER_TEST_CLI_ARGS="+string(argsJSON), "PODDER_TEST_LIVE="+string(liveJSON))
			cmd.Stdin = bytes.NewReader(input)
			var out, errs bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &errs
			err := cmd.Run()
			if (err != nil) != wantFailure {
				t.Fatalf("process: %v stdout=%s stderr=%s", err, out.String(), errs.String())
			}
			if wantFailure {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 {
					t.Fatalf("exit status: %v", err)
				}
			}
			var state SessionRuntimeState
			if err := json.Unmarshal(out.Bytes(), &state); err != nil || state.State != want {
				t.Fatalf("JSON recovery state: %+v %v stdout=%s", state, err, out.String())
			}
			if strings.Contains(out.String()+errs.String(), "PROCESS_SECRET_SENTINEL") {
				t.Fatal("secret in process output")
			}
		})
	}
}

type processBuildCommandRunner struct{ inner *sessionTestRunner }

func (r processBuildCommandRunner) Run(name string, args ...string) (string, string, error) {
	return r.inner.Run(name, args...)
}
func (r processBuildCommandRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "podman" && args[0] == "build" {
		return (execCommandRunner{}).RunContext(ctx, os.Args[0], "-test.run=^TestSessionOperationProcessHelper$", "--", "descendant")
	}
	return r.inner.RunContext(ctx, name, args...)
}
func TestSessionActiveBuildInAnotherProcessAndRealCancellation(t *testing.T) {
	p, r, s := sessionFixture(t)
	s.GitHub = &GitHubSource{URL: "https://github.com/example/fixture", Commit: strings.Repeat("b", 40)}
	state, err := p.ReviewGitHubSession(s)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionOperationProcessHelper$")
	cmd.Env = append(os.Environ(), "PODDER_TEST_LOCK_MODE=build", "PODDER_TEST_LOCK_DIR="+dir, "PODDER_TEST_SESSION_ID="+state.ID, "PODDER_TEST_STORE="+r.storeRoot)
	var errs bytes.Buffer
	cmd.Stderr = &errs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.WriteFile(filepath.Join(dir, "release"), nil, 0600); cmd.Process.Kill() })
	waitFile(t, filepath.Join(dir, "ready"))
	if _, err := p.StopAndCleanupSession(state.ID, true, false); err == nil {
		t.Fatal("another process's actual builder cleaned")
	}
	if _, err := p.PreviewSessionCleanup(state.ID); err == nil {
		t.Fatal("another process's actual builder previewed")
	}
	if err := p.AcknowledgeInterruptedSessionBuild(state.ID, true); err == nil {
		t.Fatal("ack overrode actual builder")
	}
	assertWorkspace(t, state)
	if listed, err := p.ListDisposableSessions(); err != nil || listed[0].State != "building" || listed[0].InterruptedBuild {
		t.Fatalf("active builder reconciliation: %+v %v", listed, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cancel"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("builder fixture: %v %s", err, errs.String())
	}
	state, err = loadSessionState(state.ID)
	if err != nil || state.State != "build_cancelled" {
		t.Fatalf("real command cancellation: %+v %v", state, err)
	}
	preview, err := p.PreviewSessionCleanup(state.ID)
	if err != nil || preview.NeedBuildConfirmation {
		t.Fatalf("completed cancellation: %+v %v", preview, err)
	}
	if result, err := p.StopAndCleanupSession(state.ID, true, false); err != nil || result.State != "stopped" {
		t.Fatalf("cleanup after worker exit: %+v %v", result, err)
	}
}

func TestSessionRecreatedStoreIdentityFailsClosed(t *testing.T) {
	p, r, s := sessionFixture(t)
	state := mustState(t, p, s)
	original := r.storeRoot + "-original"
	if err := os.Rename(r.storeRoot, original); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(original)
	if err := os.Mkdir(r.storeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := p.StopAndCleanupSession(state.ID, true, true); err == nil {
		t.Fatal("recreated store silently associated")
	}
	assertWorkspace(t, state)
}
func TestSessionLegacyUnsafeSourceNeverExposed(t *testing.T) {
	p, _, s := sessionFixture(t)
	state := mustState(t, p, s)
	state.Spec.GitHub = &GitHubSource{URL: "https://u:LEGACY_SECRET_SENTINEL@github.com/a/b", Commit: strings.Repeat("b", 40)}
	root, err := manifestRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writeRootJSON(root, state.ID+".json", state); err != nil {
		t.Fatal(err)
	}
	check := func(err error) {
		t.Helper()
		if err == nil || strings.Contains(err.Error(), "LEGACY_SECRET_SENTINEL") {
			t.Fatalf("unsafe legacy exposed: %v", err)
		}
	}
	_, err = p.ListDisposableSessions()
	check(err)
	_, err = p.PreviewSessionCleanup(state.ID)
	check(err)
	_, err = p.StartReviewedSessionBuild(state.ID, true, true)
	check(err)
	_, err = p.LaunchBuiltSession(state.ID, nil, nil)
	check(err)
	_, err = p.StopAndCleanupSession(state.ID, true, true)
	check(err)
	assertWorkspace(t, state)
}
