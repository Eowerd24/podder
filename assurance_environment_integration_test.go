//go:build integration && linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssurancePrivateEnvironmentTransport(t *testing.T) {
	_, r, image := assuranceStore(t)
	secret := "private-test-quote'\"$equals=hash#value"
	seen := 0
	r.before = func(args []string) {
		if strings.Contains(strings.Join(args, " "), secret) {
			t.Fatal("secret in actual engine argv")
		}
		for i, a := range args {
			if a == "--env-file" && i+1 < len(args) {
				seen++
				info, err := os.Stat(args[i+1])
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("unsafe launch env file", err)
				}
			}
		}
	}
	p := &PodmanService{}
	// The contextual production path is used for sessions; inspect its arguments
	// through this runner as well, without altering the actual CLI execution.
	sessionRunner := &assuranceEnvironmentRunner{assuranceContextRunner{r}, r.before}
	p.runner = sessionRunner
	session := &PodmanService{runner: sessionRunner}
	env := map[string]string{"PODDER_TEST_SECRET": secret}
	managed, err := p.CreateContainer(ContainerCreateRequest{Image: image, Name: "env-managed", Managed: true, Env: env, Labels: map[string]string{"org.podder.assurance": "round2"}})
	if err != nil || managed == nil || !managed.Success {
		t.Fatalf("managed environment launch failed: %v", err)
	}
	state, err := session.LaunchDisposableSession(DisposableSessionSpec{SchemaVersion: 1, Name: "env-session", Image: image, Policy: "temporary", Env: env, Command: []string{"/fixture", "sleep"}})
	if err != nil || state == nil || state.State != "running" {
		t.Fatalf("session environment launch failed: %v", err)
	}
	for _, id := range []string{managed.ContainerID, state.ContainerID} {
		out, stderr, err := r.Run("podman", "exec", "--", id, "/fixture", "env")
		if err != nil {
			t.Fatal("fixture env probe failed", stderr, err)
		}
		if out != secret {
			t.Fatal("private environment did not arrive intact")
		}
	}
	if seen != 2 {
		t.Fatal("both launches did not use private environment files", seen)
	}
	stored, err := p.GetSpec("env-managed")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(stored)
	if err != nil || strings.Contains(string(b), "private-test-quote") {
		t.Fatal("secret in exported managed spec", err)
	}
	for _, dir := range []string{getConfigDir(), sessionDataDir()} {
		if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "private-test-quote") {
				t.Fatal("secret in persisted application metadata", path)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	root, err := environmentRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root.Name())
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "launch-") {
			t.Fatal("launch environment file retained after CLI returned")
		}
	}
	result, err := assuranceFinishSession(t, session, r, state.ID)
	if err != nil || result.State != "stopped" {
		t.Fatal("session cleanup failed", err)
	}
}

type assuranceEnvironmentRunner struct {
	assuranceContextRunner
	check func([]string)
}

func (r *assuranceEnvironmentRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "podman" {
		r.check(args)
	}
	return r.assuranceContextRunner.RunContext(ctx, name, args...)
}
