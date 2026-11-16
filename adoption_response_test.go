package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAdoptionResponseKeepsInspectSecretsInternal(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "adoptable", true: "blocked"}[blocked], func(t *testing.T) {
			var list []map[string]any
			if err := json.Unmarshal([]byte(baseInspectJSON("", "", "", "", "")), &list); err != nil {
				t.Fatal(err)
			}
			secret := "synthetic-inspect-only-value"
			list[0]["Config"].(map[string]any)["Env"] = []string{"ORDINARY_NAME=" + secret, "TOKEN=" + strings.Repeat("a", 64)}
			if blocked {
				list[0]["HostConfig"].(map[string]any)["ReadonlyRootfs"] = true
			}
			raw, err := json.Marshal(list)
			if err != nil {
				t.Fatal(err)
			}
			runner := newFakeCommandRunner()
			runner.On("podman inspect", func(string, []string) (string, string, error) { return string(raw), "", nil })
			p := &PodmanService{runner: runner}
			assessment, err := p.InspectContainerForAdoption(strings.Repeat("b", 64))
			if err != nil || assessment.CanAdopt == blocked {
				t.Fatalf("assessment failed: %+v %v", assessment, err)
			}
			response, err := json.Marshal(assessment)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(response), secret) || strings.Contains(string(response), strings.Repeat("a", 64)) || strings.Contains(string(response), "rawInspect") {
				t.Fatal("adoption response contains private inspect data")
			}
			if assessment.ProposedSpec.Env["ORDINARY_NAME"] != secret || !strings.Contains(assessment.RawInspect, secret) {
				t.Fatal("internal adoption material was lost")
			}
			before, err := parseSingleInspect(assessment.RawInspect)
			if err != nil || !adoptionReplayEquivalent(before, before) {
				t.Fatal("internal replay comparison stopped working", err)
			}
			list[0]["Config"].(map[string]any)["Env"] = []string{"ORDINARY_NAME=changed"}
			changed, _ := json.Marshal(list)
			after, err := parseSingleInspect(string(changed))
			if err != nil || adoptionReplayEquivalent(before, after) {
				t.Fatal("changed environment escaped replay verification", err)
			}
		})
	}
}
