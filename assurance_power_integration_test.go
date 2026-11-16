//go:build integration && linux

package main

// Adapted from completion_power_vm_test.go.txt. Never resets/reboots anything.
// An external operator/hypervisor cuts only a NEW disposable VM after the ready
// receipt appears; a separate run asserts survival after its boot ID changes.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type assurancePowerReceipt struct {
	Base, Token, BootID, Point, OriginalID, Image string
	Files                                         map[string]string
}
type assurancePowerRunner struct {
	*round2Runner
	point string
	cut   func() error
}

func (r assurancePowerRunner) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && r.point == "before-"+args[0] {
		if err := r.cut(); err != nil {
			return "", "", err
		}
	}
	out, stderr, err := r.round2Runner.Run(name, args...)
	if name == "podman" && len(args) > 0 && r.point == "after-"+args[0] && err == nil {
		if e := r.cut(); e != nil {
			return out, stderr, e
		}
	}
	return out, stderr, err
}
func TestAssurancePowerLossJournal(t *testing.T) {
	phase := os.Getenv("PODDER_ASSURANCE_POWER_PHASE")
	if os.Getenv("PODDER_ASSURANCE_DISPOSABLE_VM") != "1" || (phase != "prepare" && phase != "verify") {
		t.Skip("requires explicit disposable-VM prepare/verify power-loss campaign; never reboot the host")
	}
	p, r, image := assuranceStore(t)
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	if phase == "prepare" {
		point := os.Getenv("PODDER_ASSURANCE_POWER_POINT")
		allowed := map[string]bool{"before-rename": true, "after-rename": true, "after-stop": true, "after-run": true, "before-rm": true}
		if !allowed[point] {
			t.Fatal("select a supported journal cut point")
		}
		original := r2create(t, p, image, "power-loss")
		triggered := false
		cut := func() error {
			if triggered {
				return fmt.Errorf("power cut did not occur")
			}
			triggered = true
			receipt := assurancePowerReceipt{Base: r.base, Token: os.Getenv("PODDER_R3_TOKEN"), BootID: strings.TrimSpace(string(boot)), Point: point, OriginalID: original.ContainerID, Image: image, Files: map[string]string{}}
			for _, dir := range []string{getServicesDir(), filepath.Join(getConfigDir(), "managed-transactions")} {
				entries, e := os.ReadDir(dir)
				if e != nil {
					return e
				}
				for _, entry := range entries {
					if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
						continue
					}
					path := filepath.Join(dir, entry.Name())
					b, e := os.ReadFile(path)
					if e != nil {
						return e
					}
					if !json.Valid(b) {
						return fmt.Errorf("invalid pre-cut JSON")
					}
					sum := sha256.Sum256(b)
					receipt.Files[path] = hex.EncodeToString(sum[:])
				}
			}
			if len(receipt.Files) < 2 {
				return fmt.Errorf("no durable journal/spec pair")
			}
			b, e := json.MarshalIndent(receipt, "", "  ")
			if e != nil {
				return e
			}
			if e = writePrivateFileAtomic(r.base+"/power-ready.json", b); e != nil {
				return e
			}
			t.Logf("READY for external disposable-VM power cut: %s/power-ready.json", r.base)
			time.Sleep(2 * time.Minute)
			return fmt.Errorf("no external VM power cut occurred within the test window")
		}
		_, err := (&PodmanService{runner: assurancePowerRunner{r, point, cut}}).DeploySpec("power-loss")
		t.Fatalf("prepare must end by external VM power cut, not return: %v (triggered=%v)", err, triggered)
	}
	b, err := os.ReadFile(r.base + "/power-ready.json")
	if err != nil {
		t.Fatal(err)
	}
	var receipt assurancePowerReceipt
	if err = json.Unmarshal(b, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Base != r.base || receipt.Token != os.Getenv("PODDER_R3_TOKEN") || receipt.Image != image || receipt.BootID == strings.TrimSpace(string(boot)) {
		t.Fatal("receipt/store mismatch or VM did not reboot")
	}
	if validateFullContainerID(receipt.OriginalID) != nil {
		t.Fatal("invalid exact original ID")
	}
	if len(receipt.Files) < 2 {
		t.Fatal("missing recorded journal/spec files")
	}
	for path, digest := range receipt.Files {
		relative, err := filepath.Rel(getConfigDir(), path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			t.Fatal("receipt path outside fresh profile")
		}
		b, err := os.ReadFile(path)
		if err != nil || !json.Valid(b) {
			t.Fatal("journal/spec did not survive as valid JSON", path, err)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != digest {
			t.Fatal("durable bytes changed after power loss", path)
		}
	}
	pending, err := p.ListManagedRecoveries()
	if err != nil || len(pending) != 1 || pending[0].OriginalID != receipt.OriginalID {
		t.Fatal("identity-bound recovery missing", pending, err)
	}
	if err := p.RecoverManagedTransaction("power-loss", true); err != nil {
		t.Fatal(err)
	}
	cs, err := p.ListContainers(true)
	if err != nil {
		t.Fatal(err)
	}
	original := findContainerByExactID(cs, receipt.OriginalID)
	if len(cs) != 1 || !containerHasName(original, "power-loss") || original.State != "running" {
		t.Fatalf("exact original not restored: %+v", cs)
	}
	if pending, err = p.ListManagedRecoveries(); err != nil || len(pending) != 0 {
		t.Fatal("journal not retired after recovery", err)
	}
	r2cleanup(t, p, r, image, "power-loss")
}
