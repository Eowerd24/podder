//go:build integration && linux

package main

// Self-provisioning fixtures for the completion-round regressions. Each parent
// test creates a NEW explicit root/runroot and HOME/XDG profile. No external
// store path is accepted; child processes inherit a receipt from that parent.
import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type assuranceContextRunner struct{ *round2Runner }

func (r assuranceContextRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	return r.RunContextWithFiles(ctx, name, nil, args...)
}
func (r assuranceContextRunner) RunContextWithFiles(ctx context.Context, name string, files []*os.File, args ...string) (string, string, error) {
	if name == "podman" {
		if err := assuranceOperands(args); err != nil {
			return "", "", err
		}
		return (execCommandRunner{}).RunContextWithFiles(ctx, "/usr/bin/podman", files, append(r.prefix(), args...)...)
	}
	if name != "ss" {
		return "", "", fmt.Errorf("unexpected executable %s", name)
	}
	return (execCommandRunner{}).RunContext(ctx, name, args...)
}
func assuranceOperands(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty engine command")
	}
	if args[0] == "system" || args[0] == "volume" || strings.Contains(args[0], "prune") {
		return fmt.Errorf("unscoped engine operation forbidden")
	}
	if map[string]bool{"rm": true, "stop": true, "start": true, "rename": true, "restart": true, "kill": true, "exec": true, "logs": true}[args[0]] {
		i := 1
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			if args[i] == "--" {
				i++
				break
			}
			if args[i] == "--time" || args[i] == "-t" || args[i] == "--tail" {
				i++
			}
			i++
		}
		if i >= len(args) {
			return fmt.Errorf("container ID missing")
		}
		if err := validateFullContainerID(args[i]); err != nil {
			return err
		}
		if args[0] != "rename" && args[0] != "exec" && args[0] != "logs" && i+1 != len(args) {
			return fmt.Errorf("only one exact container ID is allowed")
		}
	}
	return nil
}
func assuranceStore(t *testing.T) (*PodmanService, *round2Runner, string) {
	t.Helper()
	if os.Getenv("PODDER_ASSURANCE_INTEGRATION") != "1" {
		t.Skip("set PODDER_ASSURANCE_INTEGRATION=1 to provision a fresh isolated rootless store")
	}
	powerPhase := os.Getenv("PODDER_ASSURANCE_POWER_PHASE")
	if powerPhase != "" && (os.Getenv("PODDER_ASSURANCE_DISPOSABLE_VM") != "1" || !strings.HasPrefix(t.Name(), "TestAssurancePowerLossJournal")) {
		t.Fatal("power fixture is restricted to its explicitly selected disposable-VM test")
	}
	child := os.Getenv("PODDER_ASSURANCE_CHILD") == "1" || powerPhase == "verify"
	base := os.Getenv("PODDER_R3_BASE")
	if !child {
		var err error
		parent, pattern := "/tmp", "pdr3-"
		if powerPhase == "prepare" {
			parent, pattern = "/var/tmp", "pdr3-power-"
		}
		base, err = os.MkdirTemp(parent, pattern)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Fresh isolated store retained: %s", base)
		t.Setenv("PODDER_R3_BASE", base)
		for _, d := range []string{"home", "config", "data", "runtime", "cache", "root", "runroot", "out"} {
			if err := os.Mkdir(filepath.Join(base, d), 0700); err != nil {
				t.Fatal(err)
			}
		}
		token := newSessionID()
		t.Setenv("PODDER_R3_TOKEN", token)
		if err := os.WriteFile(filepath.Join(base, "receipt"), []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	validBase := filepath.Dir(base) == "/tmp" && strings.HasPrefix(filepath.Base(base), "pdr3-")
	if powerPhase != "" {
		validBase = filepath.Dir(base) == "/var/tmp" && strings.HasPrefix(filepath.Base(base), "pdr3-power-")
	}
	if !validBase {
		t.Fatal("invalid new campaign root")
	}
	receipt, err := os.ReadFile(filepath.Join(base, "receipt"))
	if err != nil || string(receipt) != os.Getenv("PODDER_R3_TOKEN") || !sessionIDPattern.MatchString(string(receipt)) {
		t.Fatal("campaign receipt mismatch")
	}
	for k, d := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_RUNTIME_DIR": "runtime", "XDG_CACHE_HOME": "cache"} {
		t.Setenv(k, filepath.Join(base, d))
	}
	// Ignore ambient remote selectors and user engine configuration.
	t.Setenv("CONTAINER_HOST", "")
	t.Setenv("CONTAINER_CONNECTION", "")
	t.Setenv("CONTAINERS_CONF", filepath.Join(base, "containers.conf"))
	if !child {
		if err := os.WriteFile(filepath.Join(base, "containers.conf"), []byte("[engine]\ncgroup_manager=\"cgroupfs\"\nevents_logger=\"file\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PODDER_R2_EVIDENCE", filepath.Join(base, "out"))
	r := &round2Runner{base: base}
	p := &PodmanService{runner: r}
	out, stderr, err := r.Run("podman", "info", "--format", "json")
	if err != nil {
		t.Fatalf("rootless engine preflight: %s %v", stderr, err)
	}
	var info struct {
		Host struct {
			ServiceIsRemote bool
			Security        struct{ Rootless bool }
		}
		Store   struct{ GraphRoot, RunRoot string }
		Version struct{ Version string }
	}
	if err = json.Unmarshal([]byte(out), &info); err != nil || !info.Host.Security.Rootless || info.Host.ServiceIsRemote || info.Store.GraphRoot != base+"/root" || info.Store.RunRoot != base+"/runroot" {
		t.Fatal("rootless store identity mismatch", err)
	}
	if !child {
		cs, err := p.ListContainers(true)
		if err != nil || len(cs) != 0 {
			t.Fatal("new store is not empty", err)
		}
		version, _, err := r.Run("podman", "version", "--format", "json")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Engine: %s", version)
		cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-o", base+"/fixture", "./internal/assurancefixture")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture build: %s %v", b, err)
		}
		tarfile, err := os.Create(base + "/fixture.tar")
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(tarfile)
		binary, err := os.Open(base + "/fixture")
		if err != nil {
			t.Fatal(err)
		}
		stat, err := binary.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err = tw.WriteHeader(&tar.Header{Name: "fixture", Mode: 0755, Size: stat.Size()}); err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(tw, binary); err != nil {
			t.Fatal(err)
		}
		binary.Close()
		if err = tw.Close(); err != nil {
			t.Fatal(err)
		}
		tarfile.Close()
		image, stderr, err := r.Run("podman", "import", "--change", `ENTRYPOINT ["/fixture"]`, base+"/fixture.tar", "localhost/podder-regression:fixture")
		if err != nil {
			t.Fatalf("fixture import: %s %v", stderr, err)
		}
		if err := os.WriteFile(base+"/out/fixture-image.txt", []byte(strings.TrimSpace(image)), 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cs, err := p.ListContainers(true)
			if err != nil {
				t.Error("teardown inventory", err)
				return
			}
			for _, c := range cs {
				// A new store is not itself deletion authority. Require fixture ownership
				// and image identity; every deletion still uses a validated full ID.
				if c.Labels["org.podder.assurance"] != "round2" && c.Labels["io.podder.session"] != "true" {
					t.Errorf("unowned container retained: %s", c.Id)
					continue
				}
				if !sameImageIdentity(c.ImageID, strings.TrimSpace(image)) {
					t.Errorf("image mismatch; retained %s", c.Id)
					continue
				}
				if err := p.exactTransactionContainer(c.Id, "", &c, nil); err != nil {
					t.Error(err)
					continue
				}
				if _, stderr, err := r.Run("podman", "rm", "-f", "--", c.Id); err != nil {
					t.Errorf("exact-ID teardown: %s %v", stderr, err)
				}
			}
			cs, err = p.ListContainers(true)
			if err != nil || len(cs) != 0 {
				t.Errorf("teardown left %d containers: %v", len(cs), err)
			}
		})
	}
	image, err := os.ReadFile(base + "/out/fixture-image.txt")
	if err != nil {
		t.Fatal(err)
	}
	return p, r, strings.TrimSpace(string(image))
}
