package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A commit can change the authoritative file and still report failure: its
// directory fsync or the following journal save may fail after the rename.
func TestManagedCommitFailureRestoresRuntimeAndSpec(t *testing.T) {
	for _, tc := range []struct {
		op      string
		oldSpec bool
	}{{"mutate", true}, {"adopt", false}, {"adopt", true}, {"create", false}, {"create", true}} {
		op := tc.op
		states := []string{"running", "exited", "created"}
		if op == "create" {
			states = []string{"absent"}
		}
		for _, state := range states {
			for _, fault := range []string{"before-promotion", "after-promotion", "restore-failure"} {
				t.Run(fmt.Sprintf("%s/old-spec=%v/%s/%s", op, tc.oldSpec, state, fault), func(t *testing.T) {
					sim := newMutationSim()
					name := "commit-recovery"
					oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8080, Protocol: "tcp"}}
					newPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 18081, ContainerPort: 8080, Protocol: "tcp"}}
					var p *PodmanService
					if op == "mutate" {
						p = setupManagedContainer(t, sim, name, "fixture", state, oldPorts)
					} else {
						withTestHome(t)
						withFastPolling(t)
						p = &PodmanService{runner: commitRecoveryCreateRunner{sim}}
						if tc.oldSpec {
							if err := saveSpec(ContainerSpec{Name: name, Image: "fixture", Env: map[string]string{"ORDINARY": "previous-value"}}); err != nil {
								t.Fatal(err)
							}
						}
						if op == "adopt" {
							sim.addContainer(name, "fixture", state, oldPorts, false, "")
							p.runner = &adoptionInspectRouter{sim: sim, image: "fixture", ports: oldPorts}
						}
					}
					originalID := ""
					if c := sim.containers[name]; c != nil {
						originalID = c.id
					}
					final, err := getSpecFilePath(name)
					if err != nil {
						t.Fatal(err)
					}
					oldBytes, readErr := os.ReadFile(final)
					if readErr != nil && !os.IsNotExist(readErr) {
						t.Fatal(readErr)
					}
					p.commitSpec = func(path string, spec ContainerSpec) error {
						if fault == "before-promotion" {
							return fmt.Errorf("injected failure before promotion")
						}
						if err := commitCandidateSpec(path, spec); err != nil {
							return err
						}
						if fault == "restore-failure" {
							if err := os.Remove(final); err != nil {
								t.Fatal(err)
							}
							if err := os.Mkdir(final, 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(final, "blocker"), []byte("fixture"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						return fmt.Errorf("injected fsync failure after successful authoritative rename")
					}
					var verified, manual bool
					switch op {
					case "mutate":
						x, err := p.MutateContainerPorts(PortMutationRequest{ContainerID: originalID, NewPorts: newPorts})
						if err != nil || x.Success {
							t.Fatalf("mutation: %+v %v", x, err)
						}
						verified, manual = x.RolledBack, x.ManualRecoveryRequired
					case "adopt":
						x, err := p.AdoptContainer(originalID, name)
						if err != nil || x.Success {
							t.Fatalf("adoption: %+v %v", x, err)
						}
						verified, manual = x.Rollback != nil && x.Rollback.Verified, x.ManualRecoveryRequired
					case "create":
						x, err := p.CreateContainer(ContainerCreateRequest{Name: name, Image: "fixture", Managed: true, PortMappings: newPorts})
						if err != nil || x.Success {
							t.Fatalf("creation: %+v %v", x, err)
						}
						verified, manual = !x.ManualRecoveryRequired, x.ManualRecoveryRequired
					}
					if originalID == "" {
						if len(sim.containers) != 0 {
							t.Fatal("failed create retained a candidate")
						}
					} else if c := sim.containers[name]; len(sim.containers) != 1 || c == nil || c.id != originalID || c.state != state || !mappingsExactlyEqual(c.ports, oldPorts) {
						t.Fatal("original identity, lifecycle or ports were not restored")
					}
					pending, err := p.ListManagedRecoveries()
					if err != nil {
						t.Fatal(err)
					}
					if fault == "restore-failure" {
						if verified || !manual || len(pending) != 1 {
							t.Fatalf("spec failure must retain recovery: verified=%v manual=%v pending=%d", verified, manual, len(pending))
						}
						if _, err := p.DeploySpec(name); err == nil || !strings.Contains(err.Error(), "unfinished") {
							t.Fatal("new operation admitted during unresolved restoration", err)
						}
						if err := os.Remove(filepath.Join(final, "blocker")); err != nil {
							t.Fatal(err)
						}
						if err := os.Remove(final); err != nil {
							t.Fatal(err)
						}
						if err := p.RecoverManagedTransaction(name, true); err != nil {
							t.Fatal("retry did not recover", err)
						}
					} else if !verified || manual || len(pending) != 0 {
						t.Fatalf("rollback incomplete: verified=%v manual=%v pending=%d", verified, manual, len(pending))
					}
					assertAuthoritativeSnapshot(t, final, oldBytes, readErr == nil)
					if pending, err := p.ListManagedRecoveries(); err != nil || len(pending) != 0 {
						t.Fatal("completed recovery retained a journal", pending, err)
					}
				})
			}
		}
	}
}

type commitRecoveryCreateRunner struct{ *mutationSim }

func (r commitRecoveryCreateRunner) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 1 && args[0] == "image" && args[1] == "inspect" {
		return `[{"Id":"sha256:sim-fixture"}]`, "", nil
	}
	return r.mutationSim.Run(name, args...)
}

func assertAuthoritativeSnapshot(t *testing.T, path string, want []byte, exists bool) {
	t.Helper()
	got, err := os.ReadFile(path)
	if !exists {
		if !os.IsNotExist(err) {
			t.Fatal("authoritative spec must remain absent", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if json.Unmarshal(got, &a) != nil || json.Unmarshal(want, &b) != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("authoritative spec differs from the original snapshot")
	}
}

func TestCommitJournalSaveFailureRestoresAuthoritativeSpec(t *testing.T) {
	sim := newMutationSim()
	oldPorts := []PortMapping{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8080, Protocol: "tcp"}}
	p := setupManagedContainer(t, sim, "journal-save-failure", "fixture", "running", oldPorts)
	originals, err := p.ListContainers(true)
	if err != nil {
		t.Fatal(err)
	}
	original := originals[0]
	spec, err := p.GetSpec("journal-save-failure")
	if err != nil {
		t.Fatal(err)
	}
	spec.PortMappings = []PortMapping{{HostIP: "127.0.0.1", HostPort: 18081, ContainerPort: 8080, Protocol: "tcp"}}
	path, err := writeCandidateSpec(*spec)
	if err != nil {
		t.Fatal(err)
	}
	j, err := p.beginManagedJournal(spec.Name, "mutate", spec.Name+"-backup", path, &original, *spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.runCommand("rename", "--", original.Id, j.Backup); err != nil {
		t.Fatal(err)
	}
	if err := p.StopContainer(original.Id); err != nil {
		t.Fatal(err)
	}
	args, err := BuildRunArgsFromSpec(*spec)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := p.runSpecCommand(*spec, args...)
	if err != nil {
		t.Fatal(err)
	}
	journalPath, _ := managedJournalPath(j.Service)
	dir := filepath.Dir(journalPath)
	held := dir + "-held"
	p.commitSpec = func(path string, candidate ContainerSpec) error {
		if err := commitCandidateSpec(path, candidate); err != nil {
			return err
		}
		// Make the real following journal save fail, retaining its prior bytes.
		if err := os.Rename(dir, held); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, []byte("temporary journal obstruction"), 0600); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	if err := p.commitCandidate(path, *spec); err == nil || j.Committed || j.CommitUncertain {
		t.Fatal("expected post-promotion journal persistence failure", err)
	}
	final, _ := getSpecFilePath(j.Service)
	assertAuthoritativeSnapshot(t, final, j.OldSpec, true)
	// The transient fault clears before rollback; production must restore the
	// prior spec and keep the transaction recoverable even though persisting
	// the post-promotion journal state failed.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(held, dir); err != nil {
		t.Fatal(err)
	}
	rb := p.executeRollback(j.Backup, j.Service, strings.TrimSpace(id), original.Id, lifecycleRunning, true, transactionWitness{Original: original, Candidate: *spec})
	if !rb.Verified {
		t.Fatalf("rollback failed: %+v", rb)
	}
	assertAuthoritativeSnapshot(t, final, j.OldSpec, true)
	p.finishManagedJournal(j)
	if pending, err := p.ListManagedRecoveries(); err != nil || len(pending) != 0 {
		t.Fatal("consistent rollback not finalized", pending, err)
	}
}

func TestJournalRetirementRequiresOriginalAuthoritativeSpec(t *testing.T) {
	sim := newMutationSim()
	p := setupManagedContainer(t, sim, "retirement-guard", "fixture", "running", nil)
	cs, _ := p.ListContainers(true)
	spec, _ := p.GetSpec("retirement-guard")
	j, err := p.beginManagedJournal(spec.Name, "mutate", "unused-backup", "", &cs[0], *spec)
	if err != nil {
		t.Fatal(err)
	}
	// Runtime is the original, but a different spec is authoritative. The
	// deferred terminal check must not discard the only recovery snapshot.
	spec.Command = CommandArgv{"different-command"}
	if err := saveSpec(*spec); err != nil {
		t.Fatal(err)
	}
	p.finishManagedJournal(j)
	if pending, err := p.ListManagedRecoveries(); err != nil || len(pending) != 1 {
		t.Fatal("mismatched authoritative state lost recovery", pending, err)
	}
	p.transaction = nil
	if err := p.RecoverManagedTransaction(j.Service, true); err != nil {
		t.Fatal(err)
	}
	final, _ := getSpecFilePath(j.Service)
	assertAuthoritativeSnapshot(t, final, j.OldSpec, true)
}
