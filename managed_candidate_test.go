package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

type noIDCreateRunner struct {
	sim    *mutationSim
	failed bool
}

func (r *noIDCreateRunner) Run(name string, args ...string) (string, string, error) {
	a, b, e := r.sim.Run(name, args...)
	if name == "podman" && len(args) > 0 && (args[0] == "run" || args[0] == "create") && !r.failed {
		r.failed = true
		return "", "created but start failed", fmt.Errorf("start failed")
	}
	return a, b, e
}
func TestTransactionMarkerRecoversNoStdoutCandidate(t *testing.T) {
	for _, op := range []string{"mutate", "deploy"} {
		t.Run(op, func(t *testing.T) {
			sim := newMutationSim()
			p := setupManagedContainer(t, sim, "no-id", "fixture", "running", nil)
			old := sim.containers["no-id"].id
			p.runner = &noIDCreateRunner{sim: sim}
			if op == "mutate" {
				x, e := p.MutateContainerPorts(PortMutationRequest{ContainerID: old})
				if e != nil || !x.RolledBack || x.ManualRecoveryRequired {
					t.Fatalf("%+v %v", x, e)
				}
			} else {
				x, e := p.DeploySpec("no-id")
				if e != nil || x.Rollback == nil || !x.Rollback.Verified || x.ManualRecoveryRequired {
					t.Fatalf("%+v %v", x, e)
				}
			}
			if len(sim.containers) != 1 || sim.containers["no-id"].id != old || sim.containers["no-id"].state != "running" {
				t.Fatal("original not restored")
			}
		})
	}
}

func TestTransactionMarkerAmbiguityRetainsResources(t *testing.T) {
	for _, mode := range []string{"multiple", "wrong-image", "wrong-service", "returned-ID-disagrees"} {
		t.Run(mode, func(t *testing.T) {
			withTestHome(t)
			spec := ContainerSpec{Name: "marked", Managed: true, SchemaVersion: CurrentSpecSchemaVersion, ResolvedImage: strings.Repeat("f", 64)}
			token := newSessionID()
			labels := map[string]string{"io.podder.managed": "true", "io.podder.service": spec.Name, "io.podder.schema-version": strconv.Itoa(spec.SchemaVersion), "io.podder.txn": token}
			c := rawContainer{Id: strings.Repeat("a", 64), Names: []string{spec.Name}, ImageID: spec.ResolvedImage, Labels: labels}
			cs := []rawContainer{c}
			returned := ""
			switch mode {
			case "multiple":
				other := c
				other.Id = strings.Repeat("b", 64)
				cs = append(cs, other)
			case "wrong-image":
				cs[0].ImageID = strings.Repeat("e", 64)
			case "wrong-service":
				labels["io.podder.service"] = "unrelated"
			case "returned-ID-disagrees":
				returned = strings.Repeat("c", 64)
			}
			data, _ := json.Marshal(cs)
			r := newFakeCommandRunner()
			r.On("podman ps", func(string, []string) (string, string, error) { return string(data), "", nil })
			p := &PodmanService{runner: r, transaction: &managedJournal{Token: token, Candidate: spec}}
			id, found, e := p.transactionCandidateIdentity(returned, spec)
			if e == nil || id != "" || found || p.transaction.CandidateID != "" {
				t.Fatalf("ambiguous candidate authorized: %q %v %v", id, found, e)
			}
			for _, call := range r.calls {
				if len(call) < 2 || call[1] != "ps" {
					t.Fatalf("unexpected mutating call: %v", call)
				}
			}
		})
	}
}
