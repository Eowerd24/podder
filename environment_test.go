package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentNeverAppearsInArgvOrMetadata(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret := "argv-secret-unique-value"
	s := ContainerSpec{Name: "private-env", Image: "fixture", Env: map[string]string{"ANY_NAME": secret, "EMPTY": ""}}
	for _, build := range []func(ContainerSpec) ([]string, error){BuildRunArgsFromSpec, BuildCreateArgsFromSpec} {
		args, err := build(s)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(args, " "), secret) {
			t.Fatal("secret in argv")
		}
		actual, cleanup, err := prepareEnvironmentArgs(args, s.Env)
		if err != nil {
			t.Fatal(err)
		}
		path := ""
		for i, a := range actual {
			if a == "--env-file" {
				path = actual[i+1]
			}
		}
		if path == "" || strings.Contains(strings.Join(actual, " "), secret) {
			t.Fatal("missing private environment channel")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("insecure env file", err)
		}
		b, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(b), "ANY_NAME="+secret+"\n") || !strings.Contains(string(b), "EMPTY=\n") {
			t.Fatal("environment not faithfully carried", err)
		}
		cleanup()
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("launch file retained after return")
		}
	}
	sessionArgs, err := BuildSessionRunArgs(DisposableSessionSpec{SchemaVersion: 1, Name: "session", Image: "fixture", Policy: "temporary", Env: s.Env}, strings.Repeat("a", 26), nil)
	if err != nil || strings.Contains(strings.Join(sessionArgs, " "), secret) {
		t.Fatal(sessionArgs, err)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), secret) {
		t.Fatal("secret in JSON output")
	}
	if err := saveSpec(s); err != nil {
		t.Fatal(err)
	}
	p := &PodmanService{}
	stored, err := p.GetSpec(s.Name)
	if err != nil || stored.Env["ANY_NAME"] != secret {
		t.Fatal("runtime replay lost environment", err)
	}
	filepath.WalkDir(getConfigDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsDir() {
			b, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(string(b), secret) {
				t.Fatal("secret persisted in metadata", path)
			}
		}
		return nil
	})
	root, _ := environmentRoot()
	if err := root.Remove(stored.EnvironmentID + ".json"); err != nil {
		t.Fatal(err)
	}
	root.Close()
	missing, err := p.GetSpec(s.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRunArgsFromSpec(*missing); err == nil {
		t.Fatal("replay silently dropped missing environment")
	}
	if err := p.SetServiceEnvironment(s.Name, map[string]string{"OTHER": "wrong"}); err == nil {
		t.Fatal("changed key set accepted")
	}
	if err := p.SetServiceEnvironment(s.Name, s.Env); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRunArgsFromSpec(*missing); err != nil {
		t.Fatal("re-entry did not restore replay", err)
	}
}
func TestEnvironmentRejectsLineInjectionAndMasksErrors(t *testing.T) {
	for _, env := range []map[string]string{{"KEY": "one\nOTHER=injected"}, {"KEY\nOTHER": "value"}, {"#KEY": "value"}, {" KEY": "value"}} {
		if _, err := BuildRunArgsFromSpec(ContainerSpec{Image: "fixture", Env: env}); err == nil {
			t.Fatal("ambiguous environment accepted")
		}
	}
	secret := "unique-private-value"
	err := redactEnvironmentError(fmt.Errorf("engine echoed %s", secret), map[string]string{"ANY": secret})
	if strings.Contains(err.Error(), secret) {
		t.Fatal("secret in error")
	}
}

func TestManagedJournalDoesNotPersistEnvironmentAndSurvivesRuntimeLoss(t *testing.T) {
	sim := newMutationSim()
	p := setupManagedContainer(t, sim, "journal-env", "fixture", "running", nil)
	s, err := p.GetSpec("journal-env")
	if err != nil {
		t.Fatal(err)
	}
	s.Env = map[string]string{"ANY_NAME": "journal-secret-value"}
	s.EnvironmentID = ""
	if err := saveSpec(*s); err != nil {
		t.Fatal(err)
	}
	s, err = p.GetSpec("journal-env")
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := p.ListContainers(true)
	path, err := writeCandidateSpec(*s)
	if err != nil {
		t.Fatal(err)
	}
	j, err := p.beginManagedJournal(s.Name, "mutate", "journal-env-backup", path, &cs[0], *s)
	if err != nil {
		t.Fatal(err)
	}
	jpath, _ := managedJournalPath(s.Name)
	b, err := os.ReadFile(jpath)
	if err != nil || strings.Contains(string(b), "journal-secret-value") {
		t.Fatal("secret in journal", err)
	}
	// Remove runtime material as logout/reboot does; recovery compares private
	// reference identity and must still restore the exact original without it.
	root, _ := environmentRoot()
	if err := root.Remove(s.EnvironmentID + ".json"); err != nil {
		t.Fatal(err)
	}
	root.Close()
	p.transaction = nil
	if err := p.RecoverManagedTransaction(j.Service, true); err != nil {
		t.Fatal(err)
	}
}

type environmentEchoRunner struct{}

func (environmentEchoRunner) Run(string, ...string) (string, string, error) {
	return "synthetic-secret-echo", "synthetic-secret-echo", fmt.Errorf("synthetic-secret-echo")
}
func TestEnvironmentEchoCannotEnterTransactionJournal(t *testing.T) {
	sim := newMutationSim()
	p := setupManagedContainer(t, sim, "echo-env", "fixture", "running", nil)
	s, err := p.GetSpec("echo-env")
	if err != nil {
		t.Fatal(err)
	}
	s.Env = map[string]string{"ANY_NAME": "synthetic-secret-echo"}
	cs, _ := p.ListContainers(true)
	j, err := p.beginManagedJournal(s.Name, "mutate", "echo-env-backup", "", &cs[0], *s)
	if err != nil {
		t.Fatal(err)
	}
	p.runner = environmentEchoRunner{}
	args, err := BuildRunArgsFromSpec(*s)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := p.runSpecCommand(*s, args...)
	if err == nil || strings.Contains(out+stderr+err.Error(), "synthetic-secret-echo") {
		t.Fatal("engine echoed secret outside private transport")
	}
	if j.CandidateID != "" {
		t.Fatal("non-ID stdout persisted as container identity")
	}
	path, _ := managedJournalPath(s.Name)
	b, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(b), "synthetic-secret-echo") {
		t.Fatal("echo leaked to journal", err)
	}
}

func TestEnvironmentRedactionIsSinglePass(t *testing.T) {
	env := map[string]string{"FIRST": "a", "SECOND": "e", "THIRD": "redacted", "OVERLAP": "ab"}
	if got := redactEnvironment("ab a e redacted", env); got != "<redacted> <redacted> <redacted> <redacted>" {
		t.Fatalf("replacement text was reprocessed: %q", got)
	}
	if got := redactEnvironment("unchanged", nil); got != "unchanged" {
		t.Fatal(got)
	}
}
