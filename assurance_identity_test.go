package main

import (
	"fmt"
	"os"
	"testing"
)

func TestManagedTransactionRejectsHeldStoreLock(t *testing.T) {
	withTestHome(t)
	root, err := privateRoot(getConfigDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := root.OpenFile("managed-transaction.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = lockSessionFile(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if release, err := lockManagedTransaction(); err == nil {
		release()
		f.Close()
		t.Fatal("concurrent transaction admitted")
	}
	f.Close()
	release, err := lockManagedTransaction()
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// The mock supplies only deterministic timing; separate retained probes run
// these same interleavings against real Podman in an isolated store.
type assuranceNameRace struct {
	sim                           *mutationSim
	originalID, name, unrelatedID string
	beforeRename, injected        bool
}

func (r *assuranceNameRace) Run(program string, args ...string) (string, string, error) {
	if program == "podman" && len(args) > 0 {
		if !r.injected && ((r.beforeRename && args[0] == "rename") || (!r.beforeRename && (args[0] == "run" || args[0] == "create"))) {
			r.injected = true
			r.sim.mu.Lock()
			if r.beforeRename {
				r.sim.containers[r.name+"-parked"] = r.sim.containers[r.name]
				delete(r.sim.containers, r.name)
			}
			r.sim.addContainer(r.name, "unrelated-image", "running", nil, false, "")
			r.unrelatedID = r.sim.containers[r.name].id
			r.sim.mu.Unlock()
		}
		if r.injected && (args[0] == "run" || args[0] == "create") {
			return "", "controlled name collision", fmt.Errorf("controlled create failure without ID")
		}
	}
	return r.sim.Run(program, args...)
}
func TestManagedNameReplacementRetainsUnrelatedContainer(t *testing.T) {
	for _, beforeRename := range []bool{true, false} {
		t.Run(fmt.Sprint(beforeRename), func(t *testing.T) {
			sim := newMutationSim()
			p := setupManagedContainer(t, sim, "race", "fixture", "running", nil)
			original := sim.containers["race"].id
			r := &assuranceNameRace{sim: sim, originalID: original, name: "race", beforeRename: beforeRename}
			p.runner = r
			result, err := p.MutateContainerPorts(PortMutationRequest{ContainerID: original})
			if err != nil || result.Success || !result.ManualRecoveryRequired {
				t.Fatalf("%+v %v", result, err)
			}
			if c := sim.containers["race"]; c == nil || c.id != r.unrelatedID || c.state != "running" {
				t.Fatalf("unrelated occupant changed: %+v", c)
			}
			found := false
			for _, c := range sim.containers {
				if c.id == original {
					found = true
				}
			}
			if !found {
				t.Fatal("original was removed")
			}
		})
	}
}
