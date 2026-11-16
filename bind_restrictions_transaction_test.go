package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestUnmanagedCreateRejectsBindRestrictionsBeforeEngine(t *testing.T) {
	for _, bind := range []BindMountSpec{{NoSuid: true}, {NoDev: true}, {NoSuid: true, NoDev: true}} {
		bind.HostPath, bind.ContainerPath = t.TempDir(), "/data"
		runner := newFakeCommandRunner()
		result, err := (&PodmanService{runner: runner}).CreateContainer(ContainerCreateRequest{
			Name: "unmanaged-restrictions", Image: "fixture", Binds: []BindMountSpec{bind},
		})
		if err == nil || result != nil || runner.CallCount() != 0 {
			t.Fatalf("restricted unmanaged request reached the engine: %+v %v calls=%v", result, err, runner.calls)
		}
	}
}

func TestUnmanagedCreateLegacyBindRemainsSupported(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("approved bind execution requires descriptor-backed handoff")
	}
	withTestHome(t)
	runner := fakePsRunner(strings.Repeat("a", 64), "unmanaged-legacy-bind", true)
	result, err := (&PodmanService{runner: runner}).CreateContainer(ContainerCreateRequest{
		Name: "unmanaged-legacy-bind", Image: "fixture",
		Binds: []BindMountSpec{{HostPath: t.TempDir(), ContainerPath: "/data", ReadOnly: true}},
	})
	if err != nil || result == nil || !result.Success || result.Managed {
		t.Fatalf("legacy unmanaged bind creation changed: %+v %v", result, err)
	}
	if len(runner.CallsMatching("podman run")) != 1 || len(runner.CallsMatching("io.podder.managed=true")) != 0 {
		t.Fatalf("legacy unmanaged ownership changed: %v", runner.calls)
	}
}

// This engine simulates successful creation while independently reporting a
// lost runtime mount restriction. Empty stdout forces transaction-marker
// identity recovery, rather than exercising only runSpecCommand's early check.
type restrictionTransactionRunner struct {
	t               *testing.T
	sim             *mutationSim
	source          string
	originalID      string
	emptyStdout     bool
	loseRestriction bool
	createdID       string
	candidateChecks int
}

func (r *restrictionTransactionRunner) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 {
		if args[0] == "image" {
			return `[{"Id":"sha256:sim-fixture"}]`, "", nil
		}
		if args[0] == "inspect" {
			if len(args) < 3 || args[1] != "--format" || args[2] != "json" {
				return "", "", nil
			}
			id := args[len(args)-1]
			r.sim.mu.Lock()
			found := false
			for _, container := range r.sim.containers {
				found = found || container.id == id
			}
			r.sim.mu.Unlock()
			if !found {
				return "[]", "", nil
			}
			options := []string{"nosuid", "nodev", "rbind"}
			if id != r.originalID {
				r.candidateChecks++
				if r.loseRestriction {
					options = []string{"nodev", "rbind"}
				}
			}
			var list []map[string]any
			if err := json.Unmarshal(restrictedBindInspect(r.t, r.source, options), &list); err != nil {
				r.t.Fatal(err)
			}
			list[0]["Id"] = id
			data, err := json.Marshal(list)
			return string(data), "", err
		}
	}
	out, stderr, err := r.sim.Run(name, args...)
	if name == "podman" && len(args) > 0 && (args[0] == "run" || args[0] == "create") && err == nil {
		r.createdID = strings.TrimSpace(out)
		if r.emptyStdout {
			return "", stderr, nil
		}
	}
	return out, stderr, err
}

func TestManagedTransactionsVerifyBindRestrictionsAfterIdentityRecovery(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed bind transactions require descriptor-backed handoff")
	}
	for _, op := range []string{"create", "deploy", "deploy-missing", "mutate"} {
		for _, empty := range []bool{false, true} {
			for _, lose := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/empty-stdout-%t/lost-restriction-%t", op, empty, lose), func(t *testing.T) {
					sim := newMutationSim()
					name := "restriction-" + op
					runner := &restrictionTransactionRunner{t: t, sim: sim, emptyStdout: empty, loseRestriction: lose}
					var p *PodmanService
					if op == "create" {
						withTestHome(t)
						withFastPolling(t)
						runner.source = t.TempDir()
						p = &PodmanService{runner: runner}
					} else {
						p = setupManagedContainer(t, sim, name, "fixture", "running", nil)
						runner.originalID = sim.containers[name].id
						spec, err := p.GetSpec(name)
						if err != nil {
							t.Fatal(err)
						}
						runner.source = spec.Binds[0].HostPath
						spec.Binds[0].NoSuid, spec.Binds[0].NoDev, spec.Binds[0].ReadOnly = true, true, true
						if err := saveSpec(*spec); err != nil {
							t.Fatal(err)
						}
						if op == "deploy-missing" {
							delete(sim.containers, name)
						}
						p.runner = runner
					}
					success, rollbackVerified, manual := false, false, false
					switch op {
					case "create":
						result, err := p.CreateContainer(ContainerCreateRequest{Name: name, Image: "fixture", Managed: true,
							Binds: []BindMountSpec{{HostPath: runner.source, ContainerPath: "/data", ReadOnly: true, NoSuid: true, NoDev: true}}})
						if err != nil || result == nil {
							t.Fatalf("managed create failed outside verification: %+v %v", result, err)
						}
						success, manual = result.Success, result.ManualRecoveryRequired
						if result.ContainerID != runner.createdID {
							t.Fatalf("identified candidate was lost: %+v", result)
						}
					case "deploy", "deploy-missing":
						result, err := p.DeploySpec(name)
						if err != nil || result == nil {
							t.Fatalf("deploy failed outside verification: %+v %v", result, err)
						}
						success, manual = result.Success, result.ManualRecoveryRequired
						rollbackVerified = result.Rollback != nil && result.Rollback.Verified
					case "mutate":
						result, err := p.MutateContainerPorts(PortMutationRequest{ContainerID: runner.originalID})
						if err != nil || result == nil {
							t.Fatalf("mutation failed outside verification: %+v %v", result, err)
						}
						success, manual = result.Success, result.ManualRecoveryRequired
						rollbackVerified = result.RolledBack && result.Rollback != nil && result.Rollback.Verified
					}
					if success == lose || manual || runner.createdID == "" || runner.candidateChecks == 0 {
						t.Fatalf("wrong transaction result: success=%v manual=%v lost=%v candidate=%q checks=%d", success, manual, lose, runner.createdID, runner.candidateChecks)
					}
					if lose && (op == "deploy" || op == "mutate") {
						original := sim.containers[name]
						if !rollbackVerified || len(sim.containers) != 1 || original == nil || original.id != runner.originalID || original.state != "running" {
							t.Fatalf("original did not verify restored: rollback=%v containers=%+v", rollbackVerified, sim.containers)
						}
					} else if lose {
						if len(sim.containers) != 0 {
							t.Fatalf("failed candidate survived cleanup: %+v", sim.containers)
						}
					} else if len(sim.containers) != 1 {
						t.Fatalf("successful transaction left unexpected inventory: %+v", sim.containers)
					}
					recoveries, err := p.ListManagedRecoveries()
					if err != nil || len(recoveries) != 0 {
						t.Fatalf("completed cleanup left managed recovery state: %+v %v", recoveries, err)
					}
				})
			}
		}
	}
}
