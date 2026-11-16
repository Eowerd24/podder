//go:build integration && linux

package main

// Adapted from completion_{tx,startfail,adoption}_vm_test.go.txt. These tests
// never start without the NEW campaign's explicit store and fixture receipt.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type round2Runner struct {
	base   string
	before func([]string)
}

func (r *round2Runner) prefix() []string {
	return []string{"--remote=false", "--root", r.base + "/root", "--runroot", r.base + "/runroot", "--storage-driver", "vfs", "--cgroup-manager", "cgroupfs", "--events-backend", "file"}
}
func (r *round2Runner) podmanInnerPrefix() []string { return append([]string{}, r.prefix()...) }
func (r *round2Runner) RunWithFiles(name string, files []*os.File, args ...string) (string, string, error) {
	return r.RunContextWithFiles(context.Background(), name, files, args...)
}
func (r *round2Runner) RunContextWithFiles(ctx context.Context, name string, files []*os.File, args ...string) (string, string, error) {
	if name != "podman" {
		return "", "", fmt.Errorf("unexpected executable")
	}
	if err := assuranceOperands(args); err != nil {
		return "", "", err
	}
	if r.before != nil {
		r.before(args)
	}
	return (execCommandRunner{}).RunContextWithFiles(ctx, "/usr/bin/podman", files, append(r.prefix(), args...)...)
}
func (r *round2Runner) Run(name string, args ...string) (string, string, error) {
	if name == "ss" {
		c := exec.Command("/usr/bin/ss", args...)
		b, e := c.CombinedOutput()
		return string(b), "", e
	}
	if name != "podman" {
		return "", "", fmt.Errorf("unexpected executable")
	}
	if err := assuranceOperands(args); err != nil {
		return "", "", err
	}
	if r.before != nil {
		r.before(args)
	}
	c := exec.Command("/usr/bin/podman", append(r.prefix(), args...)...)
	var a, b bytes.Buffer
	c.Stdout = &a
	c.Stderr = &b
	e := c.Run()
	return a.String(), b.String(), e
}
func round2Fixture(t *testing.T) (*PodmanService, *round2Runner, string) {
	return assuranceStore(t)
}

func r2port(t *testing.T) uint16 {
	t.Helper()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}
func r2create(t *testing.T, p *PodmanService, image, name string) *ContainerCreateResult {
	t.Helper()
	x, e := p.CreateContainer(ContainerCreateRequest{Image: image, Name: name, Managed: true, Labels: map[string]string{"org.podder.assurance": "round2"}, PortMappings: []PortMapping{{HostIP: "127.0.0.1", HostPort: r2port(t), ContainerPort: 8080, Protocol: "tcp"}}})
	if e != nil || !x.Success {
		t.Fatalf("create %+v %v", x, e)
	}
	return x
}
func r2cleanup(t *testing.T, p *PodmanService, r *round2Runner, image, name string) {
	t.Helper()
	cs, e := p.ListContainers(true)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range cs {
		if c.Provenance.Service == name || c.Labels["org.podder.assurance"] == "round2" && containerHasName(&c, name) {
			if !sameImageIdentity(c.ImageID, image) {
				t.Fatal("cleanup image mismatch")
			}
			if e = p.exactTransactionContainer(c.Id, "", &c, nil); e != nil {
				t.Fatal(e)
			}
			if _, stderr, e := r.Run("podman", "rm", "-f", c.Id); e != nil {
				t.Fatal(stderr, e)
			}
		}
	}
	_ = p.DeleteSpec(name)
}

type r2fault struct {
	*round2Runner
	mode     string
	k, count int
}

func (f *r2fault) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && map[string]bool{"rename": true, "stop": true, "start": true, "run": true, "create": true, "rm": true}[args[0]] {
		f.count++
		if f.count == f.k {
			if f.mode == "kill-before" {
				syscall.Kill(os.Getpid(), syscall.SIGKILL)
				select {}
			}
			if f.mode == "kill-after" {
				f.round2Runner.Run(name, args...)
				syscall.Kill(os.Getpid(), syscall.SIGKILL)
				select {}
			}
			if f.mode == "ambiguous" {
				f.round2Runner.Run(name, args...)
				return "", "controlled lost result", fmt.Errorf("controlled lost result")
			}
		}
	}
	return f.round2Runner.Run(name, args...)
}
func TestRound2TransactionChild(t *testing.T) {
	op := os.Getenv("R2_OP")
	if op == "" {
		t.Skip("child only")
	}
	_, r, image := round2Fixture(t)
	k, _ := strconv.Atoi(os.Getenv("R2_K"))
	f := &r2fault{round2Runner: r, mode: os.Getenv("R2_MODE"), k: k}
	p := &PodmanService{runner: f}
	name := os.Getenv("R2_SERVICE")
	if barrier := os.Getenv("R2_BARRIER"); barrier != "" {
		for i := 0; i < 500; i++ {
			if _, e := os.Stat(barrier); e == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	var result any
	var err error
	switch op {
	case "mutate":
		result, err = p.MutateContainerPorts(PortMutationRequest{ContainerID: os.Getenv("R2_ID"), NewPorts: []PortMapping{{HostIP: "127.0.0.1", HostPort: r2port(t), ContainerPort: 8080, Protocol: "tcp"}}})
	case "deploy":
		result, err = p.DeploySpec(name)
	case "create":
		result, err = p.CreateContainer(ContainerCreateRequest{Image: image, Name: name, Managed: true, Labels: map[string]string{"org.podder.assurance": "round2"}})
	case "adopt":
		result, err = p.AdoptContainer(os.Getenv("R2_ID"), name)
	}
	b, _ := json.Marshal(map[string]any{"result": result, "error": fmt.Sprint(err)})
	if file := os.Getenv("R2_RESULT"); file != "" {
		os.WriteFile(file, b, 0600)
	}
}
func r2child(t *testing.T, env map[string]string) *exec.Cmd {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestRound2TransactionChild$")
	c.Env = append(os.Environ(), "PODDER_ASSURANCE_CHILD=1")
	for k, v := range env {
		c.Env = append(c.Env, k+"="+v)
	}
	if e := c.Start(); e != nil {
		t.Fatal(e)
	}
	return c
}
func TestRound2N1CrashRecovery(t *testing.T) {
	p, r, image := round2Fixture(t)
	for _, op := range []string{"mutate", "deploy", "create", "adopt"} {
		for _, mode := range []string{"kill-before", "kill-after", "ambiguous"} {
			limit := 4
			if op == "create" {
				limit = 1
			}
			if op == "adopt" {
				limit = 3
			}
			for k := 1; k <= limit; k++ {
				t.Run(fmt.Sprintf("%s/%s/%d", op, mode, k), func(t *testing.T) {
					name := fmt.Sprintf("r2-%s-%s-%d", op, mode, k)
					id := ""
					if op == "adopt" {
						out, stderr, e := r.Run("podman", "run", "-d", "--name", name, "--label", "org.podder.assurance=round2", image, "sleep")
						if e != nil {
							t.Fatal(stderr, e)
						}
						id = strings.TrimSpace(out)
					} else if op != "create" {
						id = r2create(t, p, image, name).ContainerID
					}
					c := r2child(t, map[string]string{"R2_OP": op, "R2_MODE": mode, "R2_K": strconv.Itoa(k), "R2_SERVICE": name, "R2_ID": id})
					waitErr := c.Wait()
					if mode == "kill-before" || mode == "kill-after" {
						exit, ok := waitErr.(*exec.ExitError)
						if !ok || !exit.Sys().(syscall.WaitStatus).Signaled() || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
							t.Fatalf("intended kill point was not reached: %v", waitErr)
						}
					}
					pending, e := p.ListManagedRecoveries()
					if e != nil {
						t.Fatal(e)
					}
					for _, j := range pending {
						if j.Service == name {
							if _, blocked := p.DeploySpec(name); blocked == nil || !strings.Contains(blocked.Error(), "unfinished") {
								t.Fatal("normal action allowed during recovery", blocked)
							}
							if denied := p.RecoverManagedTransaction(name, false); denied == nil {
								t.Fatal("recovery allowed without acknowledgment")
							}
							if e = p.RecoverManagedTransaction(name, true); e != nil {
								t.Fatal(e)
							}
						}
					}
					pending, e = p.ListManagedRecoveries()
					if e != nil || len(pending) != 0 {
						t.Fatal("recovery journal not retired", e)
					}
					entries, e := os.ReadDir(getServicesDir())
					if e != nil && !os.IsNotExist(e) {
						t.Fatal(e)
					}
					for _, entry := range entries {
						if strings.HasPrefix(entry.Name(), name+".candidate-") {
							t.Fatal("candidate draft retained", entry.Name())
						}
					}
					cs, e := p.ListContainers(true)
					if e != nil {
						t.Fatal(e)
					}
					if id != "" {
						original := findContainerByExactID(cs, id)
						if original != nil {
							if !containerHasName(original, name) || original.State != "running" {
								t.Fatalf("original not restored %+v", original)
							}
						} else {
							live := findContainerByName(cs, name)
							if live == nil || live.State != "running" {
								t.Fatal("neither original nor committed replacement running")
							}
						}
					}
					r2cleanup(t, p, r, image, name)
				})
			}
		}
	}
}
func TestRound2N1Concurrency(t *testing.T) {
	p, r, image := round2Fixture(t)
	for _, pair := range [][2]string{{"mutate", "mutate"}, {"mutate", "deploy"}, {"create", "create"}} {
		for iteration := 0; iteration < 6; iteration++ {
			name := fmt.Sprintf("r2-concurrent-%s-%s-%d", pair[0], pair[1], iteration)
			id := ""
			if pair[0] != "create" {
				id = r2create(t, p, image, name).ContainerID
			}
			barrier := filepath.Join(r.base, name+".go")
			cmds := []*exec.Cmd{}
			files := []string{}
			for i, op := range pair {
				file := filepath.Join(r.base, fmt.Sprintf("%s-%d.json", name, i))
				files = append(files, file)
				cmds = append(cmds, r2child(t, map[string]string{"R2_OP": op, "R2_ID": id, "R2_SERVICE": name, "R2_BARRIER": barrier, "R2_RESULT": file}))
			}
			time.Sleep(200 * time.Millisecond)
			os.WriteFile(barrier, []byte("go"), 0600)
			for _, c := range cmds {
				_ = c.Wait()
			}
			for _, file := range files {
				b, e := os.ReadFile(file)
				if e != nil {
					t.Fatal(e)
				}
				if strings.Contains(string(b), "MANUAL RECOVERY") {
					t.Fatal("false recovery alarm", string(b))
				}
			}
			cs, e := p.ListContainers(true)
			if e != nil {
				t.Fatal(e)
			}
			live := findContainerByName(cs, name)
			if live == nil || live.State != "running" {
				t.Fatal("concurrent service down")
			}
			r2cleanup(t, p, r, image, name)
		}
	}
}
func TestRound2N2LostRename(t *testing.T) {
	p, r, image := round2Fixture(t)
	for _, op := range []string{"mutate", "deploy"} {
		name := "r2-lost-rename-" + op
		x := r2create(t, p, image, name)
		f := &r2fault{round2Runner: r, mode: "ambiguous", k: 1}
		bad := &PodmanService{runner: f}
		if op == "mutate" {
			res, e := bad.MutateContainerPorts(PortMutationRequest{ContainerID: x.ContainerID, NewPorts: []PortMapping{}})
			if e != nil || !res.RolledBack || res.ManualRecoveryRequired {
				t.Fatalf("%+v %v", res, e)
			}
		} else {
			res, e := bad.DeploySpec(name)
			if e != nil || res.Rollback == nil || !res.Rollback.Verified || res.ManualRecoveryRequired {
				t.Fatalf("%+v %v", res, e)
			}
		}
		cs, e := p.ListContainers(true)
		if e != nil {
			t.Fatal(e)
		}
		c := findContainerByExactID(cs, x.ContainerID)
		if !containerHasName(c, name) || c.State != "running" {
			t.Fatal("lost rename not recovered")
		}
		r2cleanup(t, p, r, image, name)
	}
}

// Release the controlled competitor after it caused a genuine engine start
// failure, so restoration can acquire the port. Holding it would necessarily
// keep the original stopped too; that case must retain a recovery offer.
type r2PortGrab struct {
	*round2Runner
	port          uint16
	grabbed       bool
	keep          bool
	listener      net.Listener
	emptyStdout   bool
	engineFailure string
}

func (g *r2PortGrab) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && args[0] == "run" && !g.grabbed {
		l, e := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", g.port))
		if e != nil {
			return "", "", e
		}
		g.listener = l
		g.grabbed = true
		a, b, e := g.round2Runner.Run(name, args...)
		g.emptyStdout = strings.TrimSpace(a) == ""
		g.engineFailure = b
		if !g.keep {
			g.listener.Close()
		}
		return a, b, e
	}
	return g.round2Runner.Run(name, args...)
}
func TestRound2N3StartFailure(t *testing.T) {
	p, r, image := round2Fixture(t)
	for _, op := range []string{"deploy", "mutate"} {
		name := "r2-start-failure-" + op
		x := r2create(t, p, image, name)
		spec, e := p.GetSpec(name)
		if e != nil {
			t.Fatal(e)
		}
		g := &r2PortGrab{round2Runner: r, port: spec.PortMappings[0].HostPort}
		bad := &PodmanService{runner: g}
		if op == "deploy" {
			res, e := bad.DeploySpec(name)
			if e != nil || res.Success || res.ManualRecoveryRequired || res.Rollback == nil || !res.Rollback.Verified {
				t.Fatalf("%+v %v", res, e)
			}
		} else {
			res, e := bad.MutateContainerPorts(PortMutationRequest{ContainerID: x.ContainerID, NewPorts: spec.PortMappings})
			if e != nil || res.Success || !res.RolledBack || res.ManualRecoveryRequired {
				t.Fatalf("%+v %v", res, e)
			}
		}
		if !g.grabbed || !g.emptyStdout {
			t.Fatalf("genuine no-stdout start failure was not observed: grabbed=%v empty=%v", g.grabbed, g.emptyStdout)
		}
		t.Logf("%s: actual start failure, empty stdout=%v, stderr=%s", op, g.emptyStdout, g.engineFailure)
		cs, e := p.ListContainers(true)
		if e != nil {
			t.Fatal(e)
		}
		c := findContainerByExactID(cs, x.ContainerID)
		if !containerHasName(c, name) || c.State != "running" {
			t.Fatal("original not restored")
		}
		for _, c := range cs {
			if c.Provenance.Service == name && c.Id != x.ContainerID {
				t.Fatal("stray candidate")
			}
		}
		r2cleanup(t, p, r, image, name)
	}
}
func TestRound2N4Adoption(t *testing.T) {
	p, r, image := round2Fixture(t)
	scenarios := []struct {
		name  string
		flags []string
	}{{"baseline", nil}, {"readonly", []string{"--read-only"}}, {"pids", []string{"--pids-limit=24"}}, {"network", []string{"--network=none"}}, {"shm", []string{"--shm-size=8m"}}, {"oom", []string{"--oom-score-adj=900"}}, {"umask", []string{"--umask=0077"}}, {"group", []string{"--group-add=keep-groups"}}, {"uts", []string{"--uts=host"}}, {"sysctl", []string{"--sysctl=net.ipv4.ping_group_range=0 0"}}}
	records := []map[string]any{}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			name := "r2-adopt-" + sc.name
			args := append([]string{"run", "-d", "--name", name, "--label", "org.podder.assurance=round2"}, sc.flags...)
			args = append(args, image, "sleep")
			out, stderr, e := r.Run("podman", args...)
			if e != nil {
				t.Fatalf("fixture unsupported by this engine: %s %v", stderr, e)
			}
			id := strings.TrimSpace(out)
			defer func() {
				cs, e := p.ListContainers(true)
				if e != nil {
					t.Fatal(e)
				}
				for _, c := range cs {
					if containerHasName(&c, name) || strings.HasPrefix(firstR2Name(c), name+"--podder-bak-") {
						if !sameImageIdentity(c.ImageID, image) {
							t.Fatal("adoption teardown identity mismatch")
						}
						if _, stderr, e := r.Run("podman", "rm", "-f", c.Id); e != nil {
							t.Fatal(stderr, e)
						}
					}
				}
				_ = p.DeleteSpec(name)
			}()
			beforeJSON, _, e := r.Run("podman", "inspect", "--format", "json", id)
			if e != nil {
				t.Fatal(e)
			}
			a, e := p.InspectContainerForAdoption(id)
			if e != nil {
				t.Fatal(e)
			}
			rec := map[string]any{"scenario": sc.name, "assessment": a, "before": beforeJSON}
			if sc.name == "baseline" {
				if !a.CanAdopt {
					t.Fatal("baseline refused", a.Blockers)
				}
				res, e := p.AdoptContainer(id, name)
				rec["result"] = res
				if e != nil || !res.Success {
					t.Fatalf("baseline adoption %+v %v", res, e)
				}
				cs, _ := p.ListContainers(true)
				c := findContainerByName(cs, name)
				if c == nil || c.Id == id {
					t.Fatal("no replacement")
				}
				after, _, _ := r.Run("podman", "inspect", "--format", "json", c.Id)
				rec["after"] = after
				old, _ := parseSingleInspect(beforeJSON)
				fresh, _ := parseSingleInspect(after)
				if !adoptionReplayEquivalent(old, fresh) {
					t.Fatal("accepted control lost")
				}
			} else {
				if a.CanAdopt {
					t.Fatal("hardened scenario unexpectedly accepted", sc.name)
				}
				cs, _ := p.ListContainers(true)
				if findContainerByExactID(cs, id) == nil {
					t.Fatal("blocked original changed")
				}
				probe, _, e := r.Run("podman", "exec", id, "/fixture", "writeroot")
				if e != nil {
					t.Fatal(e)
				}
				rec["rootProbe"] = probe
				if sc.name == "readonly" && !strings.Contains(probe, "ROOTFS-WRITE-DENIED") {
					t.Fatal("read-only probe not enforced")
				}
			}
			records = append(records, rec)
		})
	}
	b, _ := json.MarshalIndent(records, "", "  ")
	os.WriteFile(filepath.Join(os.Getenv("PODDER_R2_EVIDENCE"), "n4-adoption.json"), b, 0600)
}
func firstR2Name(c Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return ""
}
func TestRound2N5QuadletGuidance(t *testing.T) {
	p, r, image := round2Fixture(t)
	label := "a.service; echo REVIEW-FIXTURE"
	out, stderr, e := r.Run("podman", "run", "-d", "--name", "r2-hostile-guidance", "--label", "PODMAN_SYSTEMD_UNIT="+label, image, "sleep")
	if e != nil {
		t.Fatal(stderr, e)
	}
	id := strings.TrimSpace(out)
	defer r.Run("podman", "rm", "-f", id)
	x, e := p.MutateContainerPorts(PortMutationRequest{ContainerID: id})
	if e != nil || strings.Contains(x.Guidance, "systemctl") || strings.Contains(x.Guidance, label) {
		t.Fatalf("unsafe guidance %+v %v", x, e)
	}
}
func TestRound2N6MatchingToolchain(t *testing.T) {
	p, r, image := round2Fixture(t)
	if runtime.Version() != "go1.26.11" {
		t.Fatalf("matching toolchain required, have %s", runtime.Version())
	}
	x := r2create(t, p, image, "r2-go-toolchain")
	if x.ContainerID == "" {
		t.Fatal("no exact ID")
	}
	r2cleanup(t, p, r, image, "r2-go-toolchain")
}
func TestRound2N7OperandsAndDurability(t *testing.T) {
	p, r, image := round2Fixture(t)
	x := r2create(t, p, image, "r2-operands")
	defer r2cleanup(t, p, r, image, "r2-operands")
	for _, fn := range []func(string) error{p.StartContainer, p.StopContainer, p.RestartContainer, p.RemoveContainer, p.StopAndRemoveContainer, p.PullImage, p.RemoveImage} {
		if e := fn("--all"); e == nil {
			t.Fatal("option reached engine")
		}
	}
	if e := p.StopContainer(x.ContainerID); e != nil {
		t.Fatal(e)
	}
	if e := p.StartContainer(x.ContainerID); e != nil {
		t.Fatal(e)
	}
	if _, e := p.GetContainerLogs(x.ContainerID); e != nil {
		t.Fatal(e)
	}
	if e := writePrivateFileAtomic(filepath.Join(getConfigDir(), "durability-fixture"), []byte("new bytes")); e != nil {
		t.Fatal(e)
	}
}
func TestRound2ProductionRunnerLockRelease(t *testing.T) {
	_, r, image := round2Fixture(t)
	p := &PodmanService{runner: assuranceContextRunner{r}}
	want, err := (&PodmanService{runner: r}).sessionRuntimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := p.sessionRuntimeIdentity()
	if err != nil || actual != want {
		t.Fatal("explicit wrapper mismatch", err)
	}
	x := r2create(t, p, image, "r2-production-runner")
	if _, e := p.DeploySpec("r2-production-runner"); e != nil {
		t.Fatalf("production lock leaked past CLI completion: %v", e)
	}
	if x.ContainerID == "" {
		t.Fatal("missing ID")
	}
	r2cleanup(t, p, r, image, "r2-production-runner")
}
