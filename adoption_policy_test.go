package main

import (
	"encoding/json"
	"fmt"
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

// These fields are emitted by modern Podman even for a plain container with
// no healthcheck. Keep them in the fixture: omitting them masks schema drift.
func modernAdoptionPolicyFixture(t *testing.T) map[string]any {
	t.Helper()
	var list []map[string]any
	if err := json.Unmarshal([]byte(baseInspectJSON("", "", "", "", "")), &list); err != nil {
		t.Fatal(err)
	}
	raw := list[0]
	cfg := raw["Config"].(map[string]any)
	cfg["HealthLogDestination"] = "local"
	cfg["HealthcheckMaxLogCount"] = float64(5)
	cfg["HealthcheckMaxLogSize"] = float64(500)
	cfg["Tty"] = false
	host := raw["HostConfig"].(map[string]any)
	host["AutoRemoveImage"] = false
	host["HostsFile"] = ""
	host["ConsoleSize"] = []any{float64(0), float64(0)}
	raw["UseImageHosts"] = false
	raw["UseImageHostname"] = false
	raw["IsInitCtr"] = false
	raw["InitContainerType"] = ""
	return raw
}

func encodeAdoptionPolicyFixture(t *testing.T, raw map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal([]map[string]any{raw})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAdoptionModernInspectKnownDefaults(t *testing.T) {
	b := encodeAdoptionPolicyFixture(t, modernAdoptionPolicyFixture(t))
	assessment, err := ParseInspectToAssessment(b)
	if err != nil || !assessment.CanAdopt {
		t.Fatalf("modern default inspect refused: %+v %v", assessment, err)
	}
	before, err := parseSingleInspect(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAcceptedAdoptionInspect(before, before); err != nil {
		t.Fatalf("unchanged modern defaults refused: %v", err)
	}
}

func TestAdoptionModernInspectNondefaultControlsRefused(t *testing.T) {
	for _, tc := range []struct {
		section, key string
		value        any
	}{
		{"Config", "HealthLogDestination", "events_logger"},
		{"Config", "HealthLogDestination", "/tmp/custom-health-logs"},
		{"Config", "HealthcheckMaxLogCount", 0},
		{"Config", "HealthcheckMaxLogCount", 10},
		{"Config", "HealthcheckMaxLogCount", "5"},
		{"Config", "HealthcheckMaxLogSize", 0},
		{"Config", "HealthcheckMaxLogSize", 1024},
		{"Config", "HealthcheckMaxLogSize", "500"},
		{"HostConfig", "AutoRemoveImage", true},
		{"HostConfig", "AutoRemoveImage", "false"},
		{"HostConfig", "HostsFile", "/etc/hosts"},
		{"HostConfig", "HostsFile", false},
		{"HostConfig", "ConsoleSize", []int{24, 80}},
		{"", "UseImageHosts", true},
		{"", "UseImageHostname", true},
		{"", "UseImageHostname", "false"},
		{"", "IsInitCtr", true},
		{"", "InitContainerType", "always"},
		{"", "InitContainerType", false},
		{"", "UnclassifiedFutureControl", false},
	} {
		t.Run(tc.section+"."+tc.key+"/"+fmt.Sprint(tc.value), func(t *testing.T) {
			raw := modernAdoptionPolicyFixture(t)
			section := raw
			path := tc.key
			if tc.section != "" {
				section = raw[tc.section].(map[string]any)
				path = tc.section + "." + tc.key
			}
			section[tc.key] = tc.value
			assessment, err := ParseInspectToAssessment(encodeAdoptionPolicyFixture(t, raw))
			if err != nil || assessment.CanAdopt || !strings.Contains(strings.Join(assessment.Blockers, "\n"), path) {
				t.Fatalf("unsupported modern control %s not specifically refused: %+v %v", path, assessment, err)
			}
		})
	}
}

func TestAdoptionModernHealthLogDefaultsRequireNoHealthcheck(t *testing.T) {
	for _, key := range []string{"Healthcheck", "HealthcheckStartup"} {
		t.Run(key, func(t *testing.T) {
			raw := modernAdoptionPolicyFixture(t)
			raw["Config"].(map[string]any)[key] = map[string]any{"Test": []string{"CMD", "/probe"}}
			parsed, err := parseSingleInspect(string(encodeAdoptionPolicyFixture(t, raw)))
			if err != nil {
				t.Fatal(err)
			}
			blockers := strings.Join(inspectPolicyBlockers(parsed), "\n")
			for _, field := range []string{"HealthLogDestination", "HealthcheckMaxLogCount", "HealthcheckMaxLogSize"} {
				if !strings.Contains(blockers, "Config."+field) {
					t.Errorf("health-log field %s accepted with %s", field, key)
				}
			}
		})
	}
}

func TestAdoptionModernInspectReplayRetainsAcceptedControls(t *testing.T) {
	for _, tc := range []struct {
		section, key string
		value        any
	}{
		{"Config", "HealthLogDestination", ""},
		{"Config", "HealthcheckMaxLogCount", 0},
		{"Config", "HealthcheckMaxLogSize", 0},
		{"HostConfig", "AutoRemoveImage", true},
		{"HostConfig", "HostsFile", "/tmp/other-hosts"},
		{"", "UseImageHosts", true},
		{"", "UseImageHostname", true},
		{"", "IsInitCtr", true},
		{"", "InitContainerType", "once"},
	} {
		t.Run(tc.section+"."+tc.key, func(t *testing.T) {
			raw := modernAdoptionPolicyFixture(t)
			before, err := parseSingleInspect(string(encodeAdoptionPolicyFixture(t, raw)))
			if err != nil {
				t.Fatal(err)
			}
			section := raw
			if tc.section != "" {
				section = raw[tc.section].(map[string]any)
			}
			section[tc.key] = tc.value
			after, err := parseSingleInspect(string(encodeAdoptionPolicyFixture(t, raw)))
			if err != nil {
				t.Fatal(err)
			}
			if adoptionReplayEquivalent(before, after) || verifyAcceptedAdoptionInspect(before, after) == nil {
				t.Fatal("changed accepted modern control went unnoticed")
			}
		})
	}
}

func TestAdoptionExposedPortsBackedByPublishedMappings(t *testing.T) {
	raw := modernAdoptionPolicyFixture(t)
	raw["Config"].(map[string]any)["ExposedPorts"] = map[string]any{"8080/tcp": map[string]any{}, "5353/udp": map[string]any{}}
	raw["HostConfig"].(map[string]any)["PortBindings"] = map[string]any{
		"8080/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "18080"}},
		"5353/udp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "15353"}},
	}
	assessment, err := ParseInspectToAssessment(encodeAdoptionPolicyFixture(t, raw))
	if err != nil || !assessment.CanAdopt || len(assessment.ProposedSpec.PortMappings) != 2 {
		t.Fatalf("published exposure metadata refused: %+v %v", assessment, err)
	}
	args, err := BuildRunArgsFromSpec(assessment.ProposedSpec)
	if err != nil {
		t.Fatal(err)
	}
	for _, mapping := range []string{"-p 127.0.0.1:18080:8080/tcp", "-p 127.0.0.1:15353:5353/udp"} {
		if !strings.Contains(strings.Join(args, " "), mapping) {
			t.Errorf("exposed port lost its exact published mapping %q: %v", mapping, args)
		}
	}
}

func TestAdoptionExposedPortsUnsupportedMetadataRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		exposed  any
		bindings any
	}{
		{"expose-only", map[string]any{"8080/tcp": map[string]any{}}, map[string]any{}},
		{"extra-exposed-port", map[string]any{"8080/tcp": map[string]any{}, "9090/tcp": map[string]any{}}, nil},
		{"protocol-mismatch", map[string]any{"8080/udp": map[string]any{}}, nil},
		{"unsupported-protocol", map[string]any{"8080/sctp": map[string]any{}}, nil},
		{"missing-protocol", map[string]any{"8080": map[string]any{}}, nil},
		{"invalid-port", map[string]any{"65536/tcp": map[string]any{}}, nil},
		{"noncanonical-port", map[string]any{"08080/tcp": map[string]any{}}, nil},
		{"zero-port", map[string]any{"0/tcp": map[string]any{}}, nil},
		{"nonobject-map", []string{"8080/tcp"}, nil},
		{"nonobject-entry", map[string]any{"8080/tcp": false}, nil},
		{"null-entry", map[string]any{"8080/tcp": nil}, nil},
		{"unclassified-entry-metadata", map[string]any{"8080/tcp": map[string]any{"future": false}}, nil},
		{"unbound-port", map[string]any{"8080/tcp": map[string]any{}}, map[string]any{"8080/tcp": nil}},
		{"invalid-host-port", map[string]any{"8080/tcp": map[string]any{}}, map[string]any{"8080/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "65536"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := modernAdoptionPolicyFixture(t)
			raw["Config"].(map[string]any)["ExposedPorts"] = tc.exposed
			bindings := tc.bindings
			if bindings == nil {
				bindings = map[string]any{"8080/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "18080"}}}
			}
			raw["HostConfig"].(map[string]any)["PortBindings"] = bindings
			assessment, err := ParseInspectToAssessment(encodeAdoptionPolicyFixture(t, raw))
			if err != nil || assessment.CanAdopt || !strings.Contains(strings.Join(assessment.Blockers, "\n"), "Config.ExposedPorts") {
				t.Fatalf("unsupported exposure metadata not specifically refused: %+v %v", assessment, err)
			}
		})
	}
}

func TestAdoptionExposedPortsReplayRetainsMetadata(t *testing.T) {
	for _, key := range []string{"8080/tcp", "9090/tcp"} {
		t.Run(key, func(t *testing.T) {
			raw := modernAdoptionPolicyFixture(t)
			cfg := raw["Config"].(map[string]any)
			cfg["ExposedPorts"] = map[string]any{"8080/tcp": map[string]any{}}
			raw["HostConfig"].(map[string]any)["PortBindings"] = map[string]any{"8080/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "18080"}}}
			before, err := parseSingleInspect(string(encodeAdoptionPolicyFixture(t, raw)))
			if err != nil {
				t.Fatal(err)
			}
			if key == "8080/tcp" {
				cfg["ExposedPorts"] = map[string]any{}
			} else {
				cfg["ExposedPorts"].(map[string]any)[key] = map[string]any{}
			}
			after, err := parseSingleInspect(string(encodeAdoptionPolicyFixture(t, raw)))
			if err != nil {
				t.Fatal(err)
			}
			if adoptionReplayEquivalent(before, after) || verifyAcceptedAdoptionInspect(before, after) == nil {
				t.Fatal("changed exposure metadata went unnoticed")
			}
		})
	}
}
