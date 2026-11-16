package main

import (
	"fmt"
	"testing"
)

type lostRenameRunner struct {
	sim  *mutationSim
	done bool
}

func (r *lostRenameRunner) Run(name string, args ...string) (string, string, error) {
	a, b, e := r.sim.Run(name, args...)
	if name == "podman" && len(args) > 0 && args[0] == "rename" && !r.done {
		r.done = true
		return "", "lost rename result", fmt.Errorf("lost result")
	}
	return a, b, e
}
func TestAmbiguousRenameRestoresExactOriginal(t *testing.T) {
	for _, op := range []string{"mutate", "deploy"} {
		t.Run(op, func(t *testing.T) {
			sim := newMutationSim()
			p := setupManagedContainer(t, sim, "rename-lost", "fixture", "running", nil)
			id := sim.containers["rename-lost"].id
			p.runner = &lostRenameRunner{sim: sim}
			if op == "mutate" {
				x, e := p.MutateContainerPorts(PortMutationRequest{ContainerID: id})
				if e != nil || x.ManualRecoveryRequired || !x.RolledBack {
					t.Fatalf("%+v %v", x, e)
				}
			} else {
				x, e := p.DeploySpec("rename-lost")
				if e != nil || x.ManualRecoveryRequired || x.Rollback == nil || !x.Rollback.Verified {
					t.Fatalf("%+v %v", x, e)
				}
			}
			if c := sim.containers["rename-lost"]; c == nil || c.id != id || c.state != "running" {
				t.Fatal("original not restored")
			}
		})
	}
}
