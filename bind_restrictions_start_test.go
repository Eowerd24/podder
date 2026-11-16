package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// This runner opts into the production descriptor handoff without executing
// Podman, unshare, or the mount namespace script.
type bindRestrictionStartRunner struct {
	*fakeCommandRunner
	startWithFiles func(string, []*os.File, []string) (string, string, error)
}

func (*bindRestrictionStartRunner) podmanInnerPrefix() []string { return nil }

func (r *bindRestrictionStartRunner) RunWithFiles(name string, files []*os.File, args ...string) (string, string, error) {
	return r.startWithFiles(name, files, args)
}

func TestStartContainerStopsWhenStoredBindRestrictionsAreLost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor-backed bind approval requires Linux")
	}
	for _, missing := range []string{"nosuid", "nodev"} {
		for _, stopFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("missing-%s/stop-fails-%t", missing, stopFails), func(t *testing.T) {
				withTestHome(t)
				source := t.TempDir()
				id := strings.Repeat("a", 64)
				spec := ContainerSpec{
					Name: "restricted-start", Image: "fixture", ResolvedImage: "sha256:fixture",
					SchemaVersion: CurrentSpecSchemaVersion, Managed: true, ReplayComplete: true,
					Binds: []BindMountSpec{{HostPath: source, ContainerPath: "/data", ReadOnly: true, NoSuid: true, NoDev: true}},
				}
				if err := saveSpec(spec); err != nil {
					t.Fatal(err)
				}
				stored, err := (&PodmanService{}).GetSpec(spec.Name)
				if err != nil {
					t.Fatal(err)
				}
				if len(stored.Binds) != 1 || stored.Binds[0].ObjectIdentity == "" {
					t.Fatalf("fixture lacks persisted source-object approval: %+v", stored.Binds)
				}

				base := newFakeCommandRunner()
				base.fallback = func(name string, args []string) (string, string, error) {
					return "", "", fmt.Errorf("unexpected command: %s %v", name, args)
				}
				base.On("podman ps", func(string, []string) (string, string, error) {
					data, err := json.Marshal([]map[string]any{{
						"Id": id, "Names": []string{spec.Name}, "Image": spec.Image,
						"ImageID": spec.ResolvedImage, "State": "exited",
						"Labels": map[string]string{
							"io.podder.managed": "true", "io.podder.service": spec.Name,
							"io.podder.schema-version": fmt.Sprint(spec.SchemaVersion),
						},
					}})
					return string(data), "", err
				})
				started, inspectedAfterStart, startCalls := false, false, 0
				base.On("podman inspect", func(_ string, args []string) (string, string, error) {
					if !reflect.DeepEqual(args, []string{"inspect", "--format", "json", id}) {
						t.Fatalf("inspect did not target exact container ID: %v", args)
					}
					options := []string{"rbind", "nosuid", "nodev"}
					if started {
						inspectedAfterStart = true
						options = []string{"rbind"}
						if missing == "nosuid" {
							options = append(options, "nodev")
						} else {
							options = append(options, "nosuid")
						}
					}
					data, err := json.Marshal([]map[string]any{{"Id": id, "Mounts": []map[string]any{{
						"Type": "bind", "Source": source, "Destination": "/data", "RW": false, "Options": options,
					}}}})
					return string(data), "", err
				})
				stopErr := errors.New("fixture stop failed")
				base.On("podman stop", func(string, []string) (string, string, error) {
					if !inspectedAfterStart {
						t.Fatal("container was stopped before its post-start restrictions were inspected")
					}
					if stopFails {
						return "", stopErr.Error(), stopErr
					}
					return id, "", nil
				})
				runner := &bindRestrictionStartRunner{fakeCommandRunner: base}
				runner.startWithFiles = func(name string, files []*os.File, args []string) (string, string, error) {
					startCalls++
					if name != "podman" || len(args) < 3 || !reflect.DeepEqual(args[:2], []string{"unshare", "bash"}) ||
						!reflect.DeepEqual(args[len(args)-3:], []string{"start", "--", id}) {
						t.Fatalf("start did not use exact-ID namespace handoff: %s %v", name, args)
					}
					if len(files) != 1 {
						t.Fatalf("start inherited %d source descriptors, want 1", len(files))
					}
					pinned, err := files[0].Stat()
					if err != nil {
						t.Fatal(err)
					}
					approved, err := os.Stat(source)
					if err != nil || !os.SameFile(pinned, approved) {
						t.Fatalf("start descriptor does not refer to approved source: %v", err)
					}
					started = true
					return id, "", nil
				}

				err = (&PodmanService{runner: runner}).StartContainer(id)
				if err == nil || !strings.Contains(err.Error(), "required bind restrictions were not enforced") {
					t.Fatalf("missing %s was reported as a successful start: %v", missing, err)
				}
				if startCalls != 1 || !inspectedAfterStart {
					t.Fatalf("start/inspect sequence incomplete: descriptor starts=%d, inspected after start=%t", startCalls, inspectedAfterStart)
				}
				stops := base.CallsMatching("podman stop")
				if len(stops) != 1 || !reflect.DeepEqual(stops[0], []string{"podman", "stop", "--", id}) {
					t.Fatalf("failed verification did not stop the exact container ID once: %v", stops)
				}
				if stopFails && (!strings.Contains(err.Error(), "container stop after failed mount verification also failed") || !strings.Contains(err.Error(), stopErr.Error())) {
					t.Fatalf("stop failure was lost: %v", err)
				}
			})
		}
	}
}

func TestBindRestrictionSchemaZeroPersistenceAndReplay(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("persisting source-object approval requires Linux")
	}
	for _, flags := range []struct {
		name          string
		noSuid, noDev bool
	}{{"nosuid", true, false}, {"nodev", false, true}, {"both", true, true}} {
		t.Run(flags.name, func(t *testing.T) {
			withTestHome(t)
			spec := ContainerSpec{Name: "schema-zero", Image: "fixture", Binds: []BindMountSpec{{
				HostPath: t.TempDir(), ContainerPath: "/data", NoSuid: flags.noSuid, NoDev: flags.noDev,
			}}}
			if _, err := BuildRunArgsFromSpec(spec); err == nil || !strings.Contains(err.Error(), "require spec schema 4 or later") {
				t.Fatalf("direct schema-zero replay did not reject restrictive bind: %v", err)
			}
			if err := saveSpec(spec); err != nil {
				t.Fatalf("saving schema-zero spec failed: %v", err)
			}
			path, err := getSpecFilePath(spec.Name)
			if err != nil {
				t.Fatal(err)
			}
			assertStored := func(path string) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var stored ContainerSpec
				if err := json.Unmarshal(data, &stored); err != nil {
					t.Fatal(err)
				}
				if stored.SchemaVersion != 4 || len(stored.Binds) != 1 || stored.Binds[0].NoSuid != flags.noSuid || stored.Binds[0].NoDev != flags.noDev {
					t.Fatalf("schema-zero persistence lost schema or restrictions: %+v", stored)
				}
			}
			assertStored(path)
			candidatePath, prepared, err := writeCandidateSpecPrepared(spec)
			if err != nil {
				t.Fatalf("staging schema-zero spec failed: %v", err)
			}
			if prepared.SchemaVersion != 4 {
				t.Fatalf("prepared schema-zero spec has schema %d, want 4", prepared.SchemaVersion)
			}
			assertStored(candidatePath)
		})
	}
}
