package main

import (
	"os"
	"strings"
	"testing"
)

func TestManagedJournalRecoveryRestoresOriginal(t *testing.T) {
	sim := newMutationSim()
	p := setupManagedContainer(t, sim, "recover", "fixture", "running", nil)
	old := sim.containers["recover"].id
	cs, _ := p.ListContainers(true)
	original := findContainerByExactID(cs, old)
	spec, _ := p.GetSpec("recover")
	j, e := p.beginManagedJournal("recover", "mutate", "recover-backup", "", original, *spec)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = p.runCommand("rename", old, "recover-backup"); e != nil {
		t.Fatal(e)
	}
	if e = p.StopContainer(old); e != nil {
		t.Fatal(e)
	}
	p.transaction = nil
	if _, e = p.DeploySpec("recover"); e == nil || !strings.Contains(e.Error(), "unfinished") {
		t.Fatal("pending transaction not blocked", e)
	}
	pending, e := p.ListManagedRecoveries()
	if e != nil || len(pending) != 1 || pending[0].OriginalID != old {
		t.Fatal(pending, e)
	}
	if e = p.RecoverManagedTransaction("recover", false); e == nil {
		t.Fatal("implicit recovery")
	}
	if e = p.RecoverManagedTransaction("recover", true); e != nil {
		t.Fatal(e)
	}
	if c := sim.containers["recover"]; c == nil || c.id != old || c.state != "running" {
		t.Fatal("original not restored")
	}
	if _, e = loadManagedJournal(j.Service); !os.IsNotExist(e) {
		t.Fatal("journal not retired", e)
	}
}
func TestManagedJournalRecoveryRefusesChangedStore(t *testing.T) {
	sim := newMutationSim()
	p := setupManagedContainer(t, sim, "store-guard", "fixture", "running", nil)
	cs, _ := p.ListContainers(true)
	spec, _ := p.GetSpec("store-guard")
	j, e := p.beginManagedJournal("store-guard", "mutate", "backup", "", &cs[0], *spec)
	if e != nil {
		t.Fatal(e)
	}
	j.Runtime = "v1:" + strings.Repeat("a", 64)
	if e = j.save(); e != nil {
		t.Fatal(e)
	}
	p.transaction = nil
	if e = p.RecoverManagedTransaction("store-guard", true); e == nil {
		t.Fatal("store mismatch accepted")
	}
}
func TestRecoveryKeepsJournalWhenCommittedSpecDoesNotMatch(t *testing.T) {
	sim := newMutationSim()
	p := setupManagedContainer(t, sim, "commit-check", "fixture", "running", nil)
	cs, _ := p.ListContainers(true)
	old := cs[0]
	spec, _ := p.GetSpec("commit-check")
	candidate := *spec
	candidate.Command = CommandArgv{"different-command"}
	j, e := p.beginManagedJournal("commit-check", "mutate", "commit-check-backup", "", &old, candidate)
	if e != nil {
		t.Fatal(e)
	}
	p.runCommand("rename", "--", old.Id, j.Backup)
	args, _ := BuildRunArgsFromSpec(candidate)
	if _, _, e = p.runCommand(args...); e != nil {
		t.Fatal(e)
	}
	j.Committed = true
	if e = j.save(); e != nil {
		t.Fatal(e)
	}
	delete(sim.containers, j.Backup)
	p.transaction = nil
	if e = p.RecoverManagedTransaction("commit-check", true); e == nil {
		t.Fatal("mismatched committed spec silently finalized")
	}
	if _, e = loadManagedJournal(j.Service); e != nil {
		t.Fatal("recovery evidence lost", e)
	}
}
