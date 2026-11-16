//go:build integration && linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssuranceCommitFailureRecovery(t *testing.T) {
	for _, op := range []string{"mutate", "adopt", "create"} {
		for _, restoreFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restore-failure=%v", op, restoreFails), func(t *testing.T) {
				p, r, image := assuranceStore(t)
				name := "commit-recovery"
				var originalID string
				if op == "mutate" {
					originalID = r2create(t, p, image, name).ContainerID
				} else if op == "adopt" {
					out, stderr, err := r.Run("podman", "run", "-d", "--name", name, "--label", "org.podder.assurance=round2", "--", image)
					if err != nil {
						t.Fatal("unmanaged fixture", stderr, err)
					}
					originalID = strings.TrimSpace(out)
					if err := validateFullContainerID(originalID); err != nil {
						t.Fatal(err)
					}
				}
				cs, err := p.ListContainers(true)
				if err != nil {
					t.Fatal(err)
				}
				original := findContainerByExactID(cs, originalID)
				final, _ := getSpecFilePath(name)
				oldSpec, readErr := os.ReadFile(final)
				if readErr != nil && !os.IsNotExist(readErr) {
					t.Fatal(readErr)
				}
				p.commitSpec = func(path string, spec ContainerSpec) error {
					if err := commitCandidateSpec(path, spec); err != nil {
						return err
					}
					if restoreFails {
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
					return fmt.Errorf("injected persistence failure after real spec promotion")
				}
				var manual, rolledBack bool
				switch op {
				case "mutate":
					x, err := p.MutateContainerPorts(PortMutationRequest{ContainerID: originalID, NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: r2port(t), ContainerPort: 8080, Protocol: "tcp"}}})
					if err != nil || x.Success {
						t.Fatalf("mutation: %+v %v", x, err)
					}
					manual, rolledBack = x.ManualRecoveryRequired, x.RolledBack
				case "adopt":
					x, err := p.AdoptContainer(originalID, name)
					if err != nil || x.Success {
						t.Fatalf("adoption: %+v %v", x, err)
					}
					manual, rolledBack = x.ManualRecoveryRequired, x.Rollback != nil && x.Rollback.Verified
				case "create":
					x, err := p.CreateContainer(ContainerCreateRequest{Name: name, Image: image, Managed: true, Labels: map[string]string{"org.podder.assurance": "round2"}})
					if err != nil || x.Success {
						t.Fatalf("creation: %+v %v", x, err)
					}
					manual, rolledBack = x.ManualRecoveryRequired, !x.ManualRecoveryRequired
				}
				if manual != restoreFails || rolledBack == restoreFails {
					t.Fatalf("incorrect persistence status: manual=%v rolledBack=%v", manual, rolledBack)
				}
				pending, err := p.ListManagedRecoveries()
				if err != nil {
					t.Fatal(err)
				}
				if restoreFails {
					if len(pending) != 1 {
						t.Fatal("failed restoration lost journal", pending)
					}
					if err := os.Remove(filepath.Join(final, "blocker")); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(final); err != nil {
						t.Fatal(err)
					}
					if err := p.RecoverManagedTransaction(name, true); err != nil {
						t.Fatal(err)
					}
				} else if len(pending) != 0 {
					t.Fatal("consistent rollback retained journal", pending)
				}
				assertAuthoritativeSnapshot(t, final, oldSpec, readErr == nil)
				cs, err = p.ListContainers(true)
				if err != nil {
					t.Fatal(err)
				}
				if original == nil {
					if len(cs) != 0 {
						t.Fatal("failed create retained a container", cs)
					}
				} else {
					current := findContainerByExactID(cs, original.Id)
					if len(cs) != 1 || current == nil || !containerHasName(current, name) || current.State != original.State || !sameImageIdentity(current.ImageID, original.ImageID) || !sameContainerLabels(current.Labels, original.Labels) || !mappingsExactlyEqual(current.PortMappings, original.PortMappings) {
						t.Fatal("original runtime not restored", cs)
					}
				}
			})
		}
	}
}
