package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func restrictedBindInspect(t *testing.T, source string, options []string) []byte {
	t.Helper()
	raw := modernAdoptionPolicyFixture(t)
	raw["Mounts"] = []map[string]any{{
		"Type": "bind", "Source": source, "Destination": "/data", "RW": false,
		"Propagation": "rprivate", "Options": options,
	}}
	return encodeAdoptionPolicyFixture(t, raw)
}

func TestAdoptionCapturesRestrictiveBindOptionsForReplay(t *testing.T) {
	source := t.TempDir()
	assessment, err := ParseInspectToAssessment(restrictedBindInspect(t, source, []string{"nosuid", "nodev", "rbind"}))
	if err != nil || !assessment.CanAdopt || len(assessment.ProposedSpec.Binds) != 1 {
		t.Fatalf("restrictive bind was not representable: %+v %v", assessment, err)
	}
	bind := assessment.ProposedSpec.Binds[0]
	if !bind.ReadOnly || !bind.NoSuid || !bind.NoDev || assessment.ProposedSpec.SchemaVersion != 4 {
		t.Fatalf("restrictive bind controls lost during adoption: %+v", assessment.ProposedSpec)
	}
	args, err := BuildRunArgsFromSpec(assessment.ProposedSpec)
	if err != nil {
		t.Fatal(err)
	}
	want := "type=bind,src=" + source + ",target=/data,readonly,nosuid,nodev"
	if !strings.Contains(strings.Join(args, " "), "--mount "+want) {
		t.Fatalf("replay omitted mount restrictions: %v", args)
	}
	for _, option := range []string{"z", "U", "noexec", "suid", "dev", "future-control"} {
		assessment, err := ParseInspectToAssessment(restrictedBindInspect(t, source, []string{"nosuid", "nodev", "rbind", option}))
		if err != nil || assessment.CanAdopt || !strings.Contains(strings.Join(assessment.Blockers, " "), "option") {
			t.Errorf("unsupported bind option %q accepted: %+v %v", option, assessment, err)
		}
	}
}

func TestBindRestrictionsPersistAndGuardOlderSchemas(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("persisting approved binds requires descriptor-backed mount handoff")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	source := t.TempDir()
	spec := ContainerSpec{Name: "restricted-bind", Image: "fixture", SchemaVersion: CurrentSpecSchemaVersion,
		Binds: []BindMountSpec{{HostPath: source, ContainerPath: "/data", ReadOnly: true, NoSuid: true, NoDev: true}}}
	if err := saveSpec(spec); err != nil {
		t.Fatal(err)
	}
	loaded, err := (&PodmanService{}).GetSpec(spec.Name)
	if err != nil || len(loaded.Binds) != 1 || !loaded.Binds[0].NoSuid || !loaded.Binds[0].NoDev || loaded.SchemaVersion != 4 {
		t.Fatalf("persisted restrictions changed: %+v %v", loaded, err)
	}
	args, err := BuildRunArgsFromSpec(*loaded)
	if err != nil || !strings.Contains(strings.Join(args, " "), ",readonly,nosuid,nodev") {
		t.Fatalf("cold replay lost restrictions: %v %v", args, err)
	}
	for _, schema := range []int{2, 3} {
		legacy := spec
		legacy.SchemaVersion = schema
		if err := saveSpec(legacy); err == nil {
			t.Errorf("new restrictions persisted under old schema %d", schema)
		}
		if _, _, err := writeCandidateSpecPrepared(legacy); err == nil {
			t.Errorf("new restrictions staged under old schema %d", schema)
		}
		if _, err := BuildRunArgsFromSpec(legacy); err == nil {
			t.Errorf("new restrictions replayed under old schema %d", schema)
		}
		legacy.Binds = []BindMountSpec{{HostPath: source, ContainerPath: "/data", ReadOnly: true}}
		legacy.Managed, legacy.ReplayComplete, legacy.ResolvedImage = true, true, "sha256:fixture"
		args, err := BuildRunArgsFromSpec(legacy)
		if err != nil || !strings.Contains(strings.Join(args, " "), "io.podder.schema-version="+strconv.Itoa(schema)) {
			t.Errorf("legacy schema %d ownership changed: %v %v", schema, args, err)
		}
	}
}

func TestBindRestrictionVerificationRejectsMissingControls(t *testing.T) {
	wanted := []BindMountSpec{{ContainerPath: "/data", ReadOnly: true, NoSuid: true, NoDev: true}}
	for _, tc := range []struct {
		name    string
		options []string
		wantErr bool
	}{
		{"enforced", []string{"nosuid", "nodev", "rbind"}, false},
		{"lost-nosuid", []string{"nodev", "rbind"}, true},
		{"lost-nodev", []string{"nosuid", "rbind"}, true},
		{"contradictory-suid", []string{"nosuid", "nodev", "suid"}, true},
		{"contradictory-dev", []string{"nosuid", "nodev", "dev"}, true},
		{"writable", []string{"nosuid", "nodev", "rw"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := parseSingleInspect(string(restrictedBindInspect(t, "/fixture", tc.options)))
			if err != nil {
				t.Fatal(err)
			}
			err = verifyRequestedBindRestrictions(raw, wanted)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wrong restriction verification: %v", err)
			}
		})
	}
	raw, err := parseSingleInspect(string(restrictedBindInspect(t, "/fixture", []string{"nosuid", "nodev"})))
	if err != nil {
		t.Fatal(err)
	}
	raw.Mounts[0].RW = true
	if verifyRequestedBindRestrictions(raw, wanted) == nil {
		t.Fatal("read-only restriction lost unnoticed")
	}
	raw.Mounts[0].RW = false
	raw.Mounts[0].Type = "volume"
	if verifyRequestedBindRestrictions(raw, wanted) == nil {
		t.Fatal("replacement volume accepted as restricted bind")
	}
	raw.Mounts[0].Type = "bind"
	raw.Mounts = append(raw.Mounts, raw.Mounts[0])
	if verifyRequestedBindRestrictions(raw, wanted) == nil {
		t.Fatal("ambiguous duplicate mount accepted")
	}
	raw.Mounts = nil
	if verifyRequestedBindRestrictions(raw, wanted) == nil {
		t.Fatal("missing required bind accepted")
	}
}

func TestBindRestrictionsSurviveDescriptorMountHandoff(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor-backed bind handoff is Linux-only")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	source := t.TempDir()
	spec := ContainerSpec{SchemaVersion: CurrentSpecSchemaVersion,
		Binds: []BindMountSpec{{HostPath: source, ContainerPath: "/data", ReadOnly: true, NoSuid: true, NoDev: true}}}
	if err := approveContainerSpecBinds(&spec); err != nil {
		t.Fatal(err)
	}
	pinned, closePinned, err := pinApprovedContainerBinds(spec.Binds)
	if err != nil {
		t.Fatal(err)
	}
	defer closePinned()
	// Plan preparation does not execute Podman or mount anything in the host.
	p := &PodmanService{runner: execCommandRunner{}}
	execution, plan, err := p.preparePinnedMountPlan("restricted-handoff", pinned)
	if err != nil || len(plan.files) != 1 || len(execution) != 1 {
		t.Fatalf("descriptor plan missing: %+v %v", plan, err)
	}
	if bind := execution[0]; !bind.ReadOnly || !bind.NoSuid || !bind.NoDev || bind.HostPath != source || bind.ContainerPath != "/data" {
		t.Fatalf("descriptor handoff lost mount restrictions: %+v", bind)
	}
}

func TestManagedCommandRejectsLostBindRestrictionAfterEngineSuccess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor-backed bind execution is Linux-only")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	for _, drop := range []bool{false, true} {
		source := t.TempDir()
		spec := ContainerSpec{Name: "restricted-command", Image: "fixture", SchemaVersion: CurrentSpecSchemaVersion,
			Binds: []BindMountSpec{{HostPath: source, ContainerPath: "/data", ReadOnly: true, NoSuid: true, NoDev: true}}}
		if err := approveContainerSpecBinds(&spec); err != nil {
			t.Fatal(err)
		}
		id := strings.Repeat("a", 64)
		runner := newFakeCommandRunner()
		runner.On("podman run", func(_ string, args []string) (string, string, error) {
			if !strings.Contains(strings.Join(args, " "), ",readonly,nosuid,nodev") {
				t.Fatal("engine did not receive restrictive mount options", args)
			}
			return id, "", nil
		})
		runner.On("podman inspect", func(string, []string) (string, string, error) {
			var list []map[string]any
			options := []string{"nosuid", "nodev", "rbind"}
			if drop {
				options = []string{"nodev", "rbind"}
			}
			if err := json.Unmarshal(restrictedBindInspect(t, source, options), &list); err != nil {
				t.Fatal(err)
			}
			list[0]["Id"] = id
			data, err := json.Marshal(list)
			return string(data), "", err
		})
		out, _, err := (&PodmanService{runner: runner}).runSpecCommand(spec, "run")
		if out != id || (err != nil) != drop || len(runner.CallsMatching("podman inspect")) != 1 {
			t.Fatalf("engine-success restriction check failed: drop=%v id=%q err=%v", drop, out, err)
		}
	}
}

func TestSessionSchemaOneRejectsManagedOnlyBindRestrictions(t *testing.T) {
	for _, flags := range []BindMountSpec{{NoSuid: true}, {NoDev: true}} {
		flags.HostPath = filepath.Join(t.TempDir(), "source")
		flags.ContainerPath = "/data"
		if err := os.Mkdir(flags.HostPath, 0o700); err != nil {
			t.Fatal(err)
		}
		spec := DisposableSessionSpec{SchemaVersion: sessionSchema, Name: "session-restrictions", Image: "fixture", Policy: "configuration",
			AllowExternalBinds: true, ExternalBinds: []BindMountSpec{flags}}
		if err := validateSessionSpec(spec); err == nil {
			t.Fatal("old session schema accepted new restrictions")
		}
		if _, err := exportRecipe(spec); err == nil {
			t.Fatal("new restrictions exported under old session schema")
		}
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (&PodmanService{}).ImportSessionRecipe(string(data)); err == nil {
			t.Fatal("new restrictions imported under old session schema")
		}
	}
}
