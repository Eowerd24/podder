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

// Only the launch command is a real local child. All engine inventory is injected.
type localLaunchRunner struct{ inner *sessionTestRunner }

func (r localLaunchRunner) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && args[0] == "run" {
		return (execCommandRunner{}).Run(os.Args[0], "-test.run=^TestSessionLaunchProcessHelper$", "--", "child")
	}
	return r.inner.Run(name, args...)
}
func (r localLaunchRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "podman" && args[0] == "run" {
		return (execCommandRunner{}).RunContext(ctx, os.Args[0], "-test.run=^TestSessionLaunchProcessHelper$", "--", "child")
	}
	return r.inner.RunContext(ctx, name, args...)
}
func TestSessionLaunchProcessHelper(t *testing.T) {
	dir := os.Getenv("PODDER_TEST_LAUNCH_DIR")
	if dir == "" {
		return
	}
	if os.Args[len(os.Args)-1] == "child" {
		os.WriteFile(filepath.Join(dir, "ready"), nil, 0600)
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				os.WriteFile(filepath.Join(dir, "completed"), nil, 0600)
				return
			}
		}
		os.Exit(5)
	}
	r := &sessionTestRunner{storeRoot: os.Getenv("PODDER_TEST_STORE")}
	p := &PodmanService{runner: localLaunchRunner{r}}
	s := DisposableSessionSpec{SchemaVersion: 1, Name: "orphan-launch", Image: "fixture", Policy: "temporary", LaunchID: os.Getenv("PODDER_TEST_SESSION_ID"), Config: SessionMount{Target: "/config"}}
	var err error
	if os.Getenv("PODDER_TEST_LAUNCH_BUILT") == "1" {
		state, e := loadSessionState(s.LaunchID)
		if e != nil {
			os.Exit(7)
		}
		r.imageTag = state.ImageTag
		r.buildImage = state.ImageID
		_, err = p.LaunchBuiltSession(s.LaunchID, nil, nil)
	} else {
		_, err = p.LaunchDisposableSession(s)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(6)
	}
}
func TestSessionSurvivingLaunchExcludesCleanup(t *testing.T) {
	for _, built := range []bool{false, true} {
		t.Run(fmt.Sprint("built=", built), func(t *testing.T) {
			p, r, s := sessionFixture(t)
			id := newSessionID()
			if built {
				s.GitHub = &GitHubSource{URL: "https://github.com/example/fixture", Commit: strings.Repeat("b", 40)}
				state, err := p.ReviewGitHubSession(s)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = p.StartReviewedSessionBuild(state.ID, true, true); err != nil {
					t.Fatal(err)
				}
				state = waitBuild(t, state.ID)
				id = state.ID
			}
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLaunchProcessHelper$")
			cmd.Env = append(os.Environ(), "PODDER_TEST_LAUNCH_DIR="+dir, "PODDER_TEST_STORE="+r.storeRoot, "PODDER_TEST_SESSION_ID="+id, fmt.Sprintf("PODDER_TEST_LAUNCH_BUILT=%d", map[bool]int{false: 0, true: 1}[built]))
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.WriteFile(filepath.Join(dir, "release"), nil, 0600); cmd.Process.Kill() })
			waitFile(t, filepath.Join(dir, "ready"))
			state, err := loadSessionState(id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.StopAndCleanupSession(id, true, false); err == nil {
				t.Fatal("cleanup during live launch")
			}
			cmd.Process.Kill()
			cmd.Wait()
			if result, err := p.StopAndCleanupSession(id, true, false); err == nil {
				t.Fatalf("surviving launch lost exclusion: %+v", result)
			}
			assertWorkspace(t, state)
			if _, err := p.PreviewSessionCleanup(id); err == nil {
				t.Fatal("preview overrode surviving command")
			}
			if err := p.AcknowledgeInterruptedSessionLaunch(id, true); err == nil {
				t.Fatal("ack overrode surviving command")
			}
			if listed, err := p.ListDisposableSessions(); err != nil || listed[0].State != "starting" {
				t.Fatalf("active launch changed: %+v %v", listed, err)
			}
			os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
			waitFile(t, filepath.Join(dir, "completed"))
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				lock, err := acquireSessionOperation(id)
				if err == nil {
					lock.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal(err)
				}
			}
			// Parent could not record completion. Absence alone cannot resolve an uncertain start.
			if _, err = p.StopAndCleanupSession(id, true, false); err == nil {
				t.Fatal("interrupted start cleaned without acknowledgment")
			}
			assertWorkspace(t, state)
			preview, err := p.PreviewSessionCleanup(id)
			if err != nil || !preview.NeedLaunchConfirmation {
				t.Fatalf("pending launch preview: %+v %v", preview, err)
			}
			if err := p.AcknowledgeInterruptedSessionLaunch(id, false); err == nil {
				t.Fatal("ack without confirmation")
			}
			if err := p.AcknowledgeInterruptedSessionLaunch(id, true); err != nil {
				t.Fatal(err)
			}
			late := *state
			late.State = "running"
			if err := completeSessionLaunch(late); err == nil {
				t.Fatal("late completion overwrote acknowledgment")
			}
			if result, err := p.StopAndCleanupSession(id, true, false); err != nil || result.State != "stopped" {
				t.Fatalf("cleanup after exited command and acknowledgment: %+v %v", result, err)
			}
			if err := completeSessionLaunch(late); err == nil {
				t.Fatal("late completion recreated journal")
			}
		})
	}
}
func TestSessionRecipeStoredConsentDoesNotAuthorizeRelaunch(t *testing.T) {
	p, r, s := sessionFixture(t)
	s.Policy = "data"
	s.ExternalBinds = []BindMountSpec{{HostPath: t.TempDir(), ContainerPath: "/external"}}
	s.AllowExternalBinds = true
	s.PortMappings = []PortMapping{{HostIP: "0.0.0.0", HostPort: 50001, ContainerPort: 8080, Protocol: "tcp"}}
	s.AllowNetworkExposure = true
	if err := p.SaveSessionRecipe(s); err != nil {
		t.Fatal(err)
	}
	if _, err := p.RelaunchSessionRecipe(s.Name, nil, nil); err == nil {
		t.Fatalf("stored approvals reused; calls=%s", strings.Join(r.calls, "\n"))
	}
}

func TestSessionInterruptedStartRequiresAcknowledgmentWithoutListing(t *testing.T) {
	for _, list := range []bool{false, true} {
		t.Run(fmt.Sprint(list), func(t *testing.T) {
			p, r, s := sessionFixture(t)
			state := mustState(t, p, s)
			r.live = nil
			state.State = "starting"
			state.LaunchOperation = "" // includes legacy interrupted starts
			if err := saveSessionState(*state); err != nil {
				t.Fatal(err)
			}
			if list {
				if _, err := p.ListDisposableSessions(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.StopAndCleanupSession(state.ID, true, false); err == nil {
				t.Fatal("uncertain start cleaned")
			}
			assertWorkspace(t, state)
			if err := p.AcknowledgeInterruptedSessionLaunch(state.ID, true); err != nil {
				t.Fatal(err)
			}
			if _, err := p.StopAndCleanupSession(state.ID, true, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSessionLateLaunchCompletionRejectsChangedJournal(t *testing.T) {
	p, r, s := sessionFixture(t)
	state := mustState(t, p, s)
	r.live = nil
	state.State = "starting"
	state.LaunchOperation = newSessionID()
	if err := saveSessionState(*state); err != nil {
		t.Fatal(err)
	}
	late := *state
	late.State = "running"
	for _, change := range []string{"operation", "token", "state"} {
		current := *state
		switch change {
		case "operation":
			current.LaunchOperation = newSessionID()
		case "token":
			current.Token = newSessionID()
		case "state":
			current.State = "cleanup_pending"
		}
		if err := saveSessionState(current); err != nil {
			t.Fatal(err)
		}
		if err := completeSessionLaunch(late); err == nil {
			t.Fatalf("late completion ignored %s", change)
		}
	}
}
func TestSessionEveryRecipeLaunchRequiresCurrentConsent(t *testing.T) {
	for _, entry := range []string{"recipe", "built", "cli-built"} {
		for _, access := range []string{"external", "network", "both"} {
			t.Run(entry+"/"+access, func(t *testing.T) {
				p, r, s := sessionFixture(t)
				s.Policy = "data"
				s.Env = map[string]string{"TOKEN": "CONSENT_SECRET_SENTINEL"}
				s.Command = []string{"app", "serve"}
				if access != "network" {
					s.ExternalBinds = []BindMountSpec{{HostPath: t.TempDir(), ContainerPath: "/external"}}
					s.AllowExternalBinds = true
				}
				if access != "external" {
					s.PortMappings = []PortMapping{{HostIP: "0.0.0.0", HostPort: 50001, ContainerPort: 8080, Protocol: "tcp"}}
					s.AllowNetworkExposure = true
				}
				var id string
				if entry == "recipe" {
					// Simulate a legacy file that persisted approval flags.
					legacy := savedSessionSpec(s)
					legacy.AllowExternalBinds = true
					legacy.AllowNetworkExposure = true
					root, err := recipeRoot()
					if err != nil {
						t.Fatal(err)
					}
					err = writeRootJSON(root, launcherFile(s.Name), legacy)
					root.Close()
					if err != nil {
						t.Fatal(err)
					}
				} else {
					s.GitHub = &GitHubSource{URL: "https://github.com/example/fixture", Commit: strings.Repeat("b", 40)}
					state, err := p.ReviewGitHubSession(s)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = p.StartReviewedSessionBuild(state.ID, true, true); err != nil {
						t.Fatal(err)
					}
					state = waitBuild(t, state.ID)
					state.Spec.AllowExternalBinds = true
					state.Spec.AllowNetworkExposure = true
					if err = saveSessionState(*state); err != nil {
						t.Fatal(err)
					}
					id = state.ID
				}
				launch := func(consent SessionLaunchConsent, env map[string]string, command []string) error {
					switch entry {
					case "recipe":
						_, err := p.RelaunchSessionRecipeWithConsent(s.Name, env, command, consent)
						return err
					case "built":
						_, err := p.LaunchBuiltSessionWithConsent(id, env, command, consent)
						return err
					default:
						material := struct {
							SessionLaunchConsent
							Env     map[string]string `json:"env"`
							Command []string          `json:"command"`
						}{consent, env, command}
						raw, _ := json.Marshal(material)
						var out bytes.Buffer
						return runSessionCLI([]string{"launch-built", id, "--confirm"}, bytes.NewReader(raw), &out, p)
					}
				}
				before := len(r.calls)
				if entry == "recipe" {
					if _, err := p.RelaunchSessionRecipe(s.Name, s.Env, s.Command); err == nil {
						t.Fatal("old entry reused consent")
					}
				}
				if entry == "built" {
					if _, err := p.LaunchBuiltSession(id, s.Env, s.Command); err == nil {
						t.Fatal("old built entry reused consent")
					}
				}
				for _, consent := range []SessionLaunchConsent{{}, {AllowExternalBinds: access == "both"}, {AllowNetworkExposure: access == "both"}} {
					if err := launch(consent, s.Env, s.Command); err == nil {
						t.Fatal("missing consent accepted")
					}
				}
				full := SessionLaunchConsent{AllowExternalBinds: true, AllowNetworkExposure: true}
				if len(s.ExternalBinds) > 0 {
					previewInput := s
					previewInput.LaunchID = id
					preview, previewErr := p.PreviewDisposableSession(previewInput)
					if previewErr != nil {
						t.Fatal(previewErr)
					}
					full.ExternalBindApprovals = preview.Spec.ExternalBindApprovals
				}
				if err := launch(full, nil, s.Command); err == nil {
					t.Fatal("missing environment accepted")
				}
				if err := launch(full, s.Env, nil); err == nil {
					t.Fatal("omitted command accepted")
				}
				for _, call := range r.calls[before:] {
					if strings.HasPrefix(call, "podman run ") {
						t.Fatal("rejected call reached launch")
					}
				}
				if err := launch(full, s.Env, s.Command); err != nil {
					t.Fatal(err)
				}
				recipes, err := p.ListSessionRecipes()
				if err != nil || len(recipes) != 1 {
					t.Fatalf("recipes %+v %v", recipes, err)
				}
				if recipes[0].AllowExternalBinds || recipes[0].AllowNetworkExposure {
					t.Fatal("approval persisted")
				}
				exported, err := p.ExportSessionRecipe(s.Name)
				if err != nil || strings.Contains(exported, "CONSENT_SECRET_SENTINEL") {
					t.Fatalf("export: %v %s", err, exported)
				}
			})
		}
	}
}
func TestSessionBuiltRecipePersistencePreservesMetadata(t *testing.T) {
	p, _, s := sessionFixture(t)
	s.Policy = "data"
	s.Favorite = true
	s.KeepID = true
	s.GitHub = &GitHubSource{URL: "https://github.com/example/app", Commit: strings.Repeat("b", 40), Subdirectory: "app", Containerfile: "Containerfile"}
	s.Image = "sha256:" + strings.Repeat("f", 64)
	s.Env = map[string]string{"TOKEN": "SAVE_SECRET_SENTINEL"}
	s.Command = []string{"app", "serve"}
	s.ExternalBinds = []BindMountSpec{{HostPath: t.TempDir(), ContainerPath: "/external"}}
	s.AllowExternalBinds = true
	if err := p.SaveSessionRecipe(s); err != nil {
		t.Fatal(err)
	}
	exported, err := p.ExportSessionRecipe(s.Name)
	if err != nil {
		t.Fatal(err)
	}
	var actual DisposableSessionSpec
	if err = json.Unmarshal([]byte(exported), &actual); err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(savedSessionSpec(s))
	got, _ := json.Marshal(actual)
	if !bytes.Equal(expected, got) || actual.GitHub == nil {
		t.Fatalf("metadata changed: %s", got)
	}
	if strings.Contains(exported, "SAVE_SECRET_SENTINEL") {
		t.Fatal("environment persisted")
	}
}

type unverifiedLaunchRunner struct{ inner *sessionTestRunner }

func (r unverifiedLaunchRunner) Run(name string, args ...string) (string, string, error) {
	return r.inner.Run(name, args...)
}
func (r unverifiedLaunchRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	out, errs, err := r.inner.RunContext(ctx, name, args...)
	if name == "podman" && args[0] == "run" {
		r.inner.mu.Lock()
		r.inner.live = nil
		r.inner.mu.Unlock()
	}
	return out, errs, err
}
func TestSessionUncertainLaunchOutcomeRetainsWorkspace(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint("failed=", failed), func(t *testing.T) {
			p, r, s := sessionFixture(t)
			r.failRun = failed
			p.runner = unverifiedLaunchRunner{r}
			state, err := p.LaunchDisposableSession(s)
			if err != nil || state.State != "cleanup_pending" || !state.InterruptedLaunch {
				t.Fatalf("uncertain launch: %+v %v", state, err)
			}
			if _, err := p.StopAndCleanupSession(state.ID, true, false); err == nil {
				t.Fatal("uncertain outcome cleaned")
			}
			assertWorkspace(t, state)
			stored, err := loadSessionState(state.ID)
			if err != nil || !stored.InterruptedLaunch {
				t.Fatalf("uncertainty not durable: %+v %v", stored, err)
			}
			original := r.storeRoot
			r.storeRoot = t.TempDir()
			if err := p.AcknowledgeInterruptedSessionLaunch(state.ID, true); err == nil {
				t.Fatal("ack ignored original store")
			}
			r.storeRoot = original
			if err := p.AcknowledgeInterruptedSessionLaunch(state.ID, true); err != nil {
				t.Fatal(err)
			}
			if _, err := p.StopAndCleanupSession(state.ID, true, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}
