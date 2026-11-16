//go:build integration && linux

package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestRound2AdoptionLostResults(t *testing.T) {
	p, r, image := round2Fixture(t)
	for _, mode := range []string{"rename", "start-failure"} {
		name := "r2-adopt-result-" + mode
		port := r2port(t)
		args := []string{"run", "-d", "--name", name, "--label", "org.podder.assurance=round2", "-p", "127.0.0.1:" + fmt.Sprint(port) + ":8080", image, "sleep"}
		out, stderr, e := r.Run("podman", args...)
		if e != nil {
			t.Fatal(stderr, e)
		}
		id := strings.TrimSpace(out)
		var bad *PodmanService
		if mode == "rename" {
			bad = &PodmanService{runner: &r2fault{round2Runner: r, mode: "ambiguous", k: 1}}
		} else {
			bad = &PodmanService{runner: &r2PortGrab{round2Runner: r, port: port}}
		}
		res, e := bad.AdoptContainer(id, name)
		if g, ok := bad.runner.(*r2PortGrab); ok && !g.emptyStdout {
			t.Fatal("adoption start failure unexpectedly returned an ID")
		}
		if e != nil || res.Success || res.ManualRecoveryRequired || res.Rollback == nil || !res.Rollback.Verified {
			t.Fatalf("%s %+v %v", mode, res, e)
		}
		cs, e := p.ListContainers(true)
		if e != nil {
			t.Fatal(e)
		}
		original := findContainerByExactID(cs, id)
		if !containerHasName(original, name) || original.State != "running" {
			t.Fatal("original not restored")
		}
		r2cleanup(t, p, r, image, name)
	}
}
