package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAdoptionExplicitControlsAndUnknownFields(t *testing.T) {
	base := baseInspectJSON("", "", "", "", "")
	for _, tc := range []struct {
		section, key string
		value        any
	}{{"HostConfig", "ReadonlyRootfs", true}, {"HostConfig", "PidsLimit", float64(24)}, {"HostConfig", "ShmSize", float64(8 << 20)}, {"HostConfig", "OomScoreAdj", float64(900)}, {"HostConfig", "GroupAdd", []string{"keep-groups"}}, {"HostConfig", "UTSMode", "host"}, {"HostConfig", "CgroupnsMode", "host"}, {"HostConfig", "Sysctls", map[string]string{"net.ipv4.ping_group_range": "0 0"}}, {"HostConfig", "NewUnknownControl", false}, {"Config", "Umask", "0077"}, {"Config", "Annotations", map[string]string{"custom": "x"}}} {
		t.Run(tc.section+tc.key, func(t *testing.T) {
			var list []map[string]any
			json.Unmarshal([]byte(base), &list)
			list[0][tc.section].(map[string]any)[tc.key] = tc.value
			b, _ := json.Marshal(list)
			a, e := ParseInspectToAssessment(b)
			if e != nil || a.CanAdopt {
				t.Fatalf("unsafe control accepted %+v %v", a, e)
			}
		})
	}
}
func TestAdoptionLegacyInspectFormatsAndKnownDefaults(t *testing.T) {
	var list []map[string]any
	json.Unmarshal([]byte(baseInspectJSON("", "", "", "", "")), &list)
	cfg := list[0]["Config"].(map[string]any)
	hc := list[0]["HostConfig"].(map[string]any)
	cfg["Entrypoint"] = "/fixture"
	cfg["StopSignal"] = 15
	cfg["WorkingDir"] = "/"
	hc["PidMode"] = "private"
	hc["PidsLimit"] = 2048
	hc["ShmSize"] = 65536000
	hc["Ulimits"] = []map[string]any{{"Name": "RLIMIT_NOFILE", "Soft": 1024, "Hard": 1024}}
	b, _ := json.Marshal(list)
	a, e := ParseInspectToAssessment(b)
	if e != nil || !a.CanAdopt || len(a.ProposedSpec.Ulimits) != 1 {
		t.Fatalf("%+v %v", a, e)
	}
	a.ProposedSpec.Managed = true
	args, e := BuildRunArgsFromSpec(a.ProposedSpec)
	if e != nil || !strings.Contains(strings.Join(args, " "), "--ulimit nofile=1024:1024") {
		t.Fatal(args, e)
	}
	before, e := parseSingleInspect(string(b))
	if e != nil {
		t.Fatal(e)
	}
	hc["PidsLimit"] = 0
	afterJSON, _ := json.Marshal(list)
	after, _ := parseSingleInspect(string(afterJSON))
	if adoptionReplayEquivalent(before, after) {
		t.Fatal("changed accepted default went unnoticed")
	}
}
func TestLegacySpecSchemaKeepsOwnershipVersion(t *testing.T) {
	s := ContainerSpec{SchemaVersion: 2, Name: "legacy", Image: "fixture", ResolvedImage: "sha256:fixture", ReplayComplete: true, Managed: true}
	args, e := BuildRunArgsFromSpec(s)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(strings.Join(args, " "), "io.podder.schema-version=2") {
		t.Fatal("legacy ownership version changed")
	}
}
