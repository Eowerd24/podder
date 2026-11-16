//go:build integration && linux

package main

// Adapted from completion_{fd,launch_parentdeath,remote_ssh}_test.go.txt.
// These are assertions against real processes, not log-only observations.
import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func assuranceWait(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatal("timeout: " + description)
}
func assuranceLockFree(path string) bool {
	f, e := os.OpenFile(path, os.O_RDWR, 0)
	if e != nil {
		return false
	}
	defer f.Close()
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return false
	}
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return true
}
func assuranceLockPath(t *testing.T, id string) string {
	t.Helper()
	root, err := manifestRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return filepath.Join(root.Name(), id+".lock")
}
func TestAssuranceSessionLockNonLeak(t *testing.T) {
	_, r, image := assuranceStore(t)
	p := &PodmanService{runner: assuranceContextRunner{r}}
	state, err := p.LaunchDisposableSession(DisposableSessionSpec{SchemaVersion: 1, Name: "fd-probe", Image: image, Policy: "temporary", Command: []string{"/fixture", "sleep"}})
	if err != nil || state == nil || state.State != "running" {
		t.Fatalf("launch: %+v %v", state, err)
	}
	lock := assuranceLockPath(t, state.ID)
	if !assuranceLockFree(lock) {
		t.Fatal("operation lock leaked past launch return")
	}
	out, stderr, err := r.Run("podman", "inspect", "--format", "{{.State.Pid}} {{.State.ConmonPid}}", "--", state.ContainerID)
	if err != nil {
		t.Fatal(stderr, err)
	}
	pids := strings.Fields(out)
	if len(pids) != 2 {
		t.Fatal("missing real container/conmon PIDs")
	}
	lockInfo, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		if n, e := strconv.Atoi(pid); e != nil || n <= 1 {
			t.Fatal("invalid runtime PID")
		}
		fds, err := os.ReadDir("/proc/" + pid + "/fd")
		if err != nil {
			t.Fatal("cannot inspect runtime fds", err)
		}
		for _, fd := range fds {
			info, e := os.Stat("/proc/" + pid + "/fd/" + fd.Name())
			if e == nil && os.SameFile(lockInfo, info) {
				t.Fatal("lock fd inherited by runtime process", pid)
			}
		}
	}
	result, err := assuranceFinishSession(t, p, r, state.ID)
	if err != nil || result.State != "stopped" {
		t.Fatal(result, err)
	}
}

type assuranceSlowLaunch struct{ assuranceContextRunner }

func (r assuranceSlowLaunch) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && args[0] == "run" {
		return (execCommandRunner{}).RunContext(ctx, os.Args[0], "-test.run=^TestAssuranceLaunchRelay$", "--", strings.Join(args, "\x1f"))
	}
	return r.assuranceContextRunner.RunContext(ctx, name, args...)
}
func TestAssuranceLaunchRelay(t *testing.T) {
	if os.Getenv("PODDER_ASSURANCE_RELAY") != "1" {
		t.Skip("child relay only")
	}
	_, r, _ := assuranceStore(t)
	args := strings.Split(os.Args[len(os.Args)-1], "\x1f")
	out, stderr, err := assuranceContextRunner{r}.RunContext(context.Background(), "podman", args...)
	if err != nil {
		t.Fatal(stderr, err)
	}
	if validateFullContainerID(strings.TrimSpace(out)) != nil {
		t.Fatal("no full container ID")
	}
	if err := os.WriteFile(r.base+"/relay-ready", []byte(strings.TrimSpace(out)), 0600); err != nil {
		t.Fatal(err)
	}
	assuranceWait(t, "parent releases relay", func() bool { _, e := os.Stat(r.base + "/relay-release"); return e == nil })
	// The caller was killed, so stdout is deliberately lost. The relay exits and
	// releases the operation lock inherited through the production executor.
}
func TestAssuranceLaunchParent(t *testing.T) {
	if os.Getenv("PODDER_ASSURANCE_LAUNCH_PARENT") != "1" {
		t.Skip("child parent only")
	}
	_, r, image := assuranceStore(t)
	p := &PodmanService{runner: assuranceSlowLaunch{assuranceContextRunner{r}}}
	_, err := p.LaunchDisposableSession(DisposableSessionSpec{SchemaVersion: 1, LaunchID: os.Getenv("PODDER_ASSURANCE_SESSION_ID"), Name: "death-probe", Image: image, Policy: "temporary", Command: []string{"/fixture", "sleep"}})
	if err != nil {
		t.Fatal(err)
	}
}
func TestAssuranceOCILaunchParentDeath(t *testing.T) {
	_, r, _ := assuranceStore(t)
	id := newSessionID()
	c := exec.Command(os.Args[0], "-test.run=^TestAssuranceLaunchParent$")
	c.Env = append(os.Environ(), "PODDER_ASSURANCE_CHILD=1", "PODDER_ASSURANCE_RELAY=1", "PODDER_ASSURANCE_LAUNCH_PARENT=1", "PODDER_ASSURANCE_SESSION_ID="+id)
	log, err := os.Create(r.base + "/parent.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	c.Stdout = log
	c.Stderr = log
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { c.Process.Kill(); os.WriteFile(r.base+"/relay-release", []byte("release"), 0600) }()
	assuranceWait(t, "real OCI run completed in in-flight relay", func() bool { _, e := os.Stat(r.base + "/relay-ready"); return e == nil })
	lock := assuranceLockPath(t, id)
	if assuranceLockFree(lock) {
		t.Fatal("operation lock absent during launch")
	}
	if err := c.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := c.Wait()
	exit, ok := waitErr.(*exec.ExitError)
	if !ok || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatal("parent was not SIGKILLed", waitErr)
	}
	p := &PodmanService{runner: assuranceContextRunner{r}}
	if err := p.AcknowledgeInterruptedSessionLaunch(id, true); err == nil {
		t.Fatal("ack allowed while surviving launch holds operation lock")
	}
	if err := os.WriteFile(r.base+"/relay-release", []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	assuranceWait(t, "surviving launch exits and releases lock", func() bool { return assuranceLockFree(lock) })
	sessions, err := p.ListDisposableSessions()
	if err != nil {
		t.Fatal(err)
	}
	var found *SessionRuntimeState
	for i := range sessions {
		if sessions[i].ID == id {
			found = &sessions[i]
		}
	}
	full, err := os.ReadFile(r.base + "/relay-ready")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || !found.InterruptedLaunch || found.ContainerID != string(full) {
		t.Fatalf("fresh reconciliation failed: %+v", found)
	}
	if _, err := p.StopAndCleanupSession(id, true, false); err == nil {
		t.Fatal("cleanup allowed before launch acknowledgment")
	}
	if err := p.AcknowledgeInterruptedSessionLaunch(id, false); err == nil {
		t.Fatal("implicit acknowledgment accepted")
	}
	if err := p.AcknowledgeInterruptedSessionLaunch(id, true); err != nil {
		t.Fatal(err)
	}
	result, err := assuranceFinishSession(t, p, r, id)
	if err != nil || result.State != "stopped" {
		t.Fatal(result, err)
	}
	cs, err := p.ListContainers(true)
	if err != nil || findContainerByExactID(cs, string(full)) != nil {
		t.Fatal("exact session container survived cleanup", err)
	}
}

func TestAssuranceSSHParent(t *testing.T) {
	if os.Getenv("PODDER_ASSURANCE_SSH_PARENT") != "1" {
		t.Skip("child parent only")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("PODDER_ASSURANCE_SSH_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	p := &PodmanService{}
	done, err := p.startSSHTunnel(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestAssuranceSSHHelperParentDeath(t *testing.T) {
	if os.Getenv("PODDER_ASSURANCE_INTEGRATION") != "1" {
		t.Skip("set PODDER_ASSURANCE_INTEGRATION=1 for real loopback SSH fixture")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SSH_AUTH_SOCK", "")
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"host", "client"} {
		c := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, name))
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("fresh key: %s %v", b, err)
		}
	}
	pub, err := os.ReadFile(dir + "/client.pub")
	if err != nil {
		t.Fatal(err)
	}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "parent-death-fixture") }))
	defer destination.Close()
	_, destPortString, err := net.SplitHostPort(strings.TrimPrefix(destination.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	destPort, _ := strconv.Atoi(destPortString)
	if err := os.WriteFile(dir+"/authorized_keys", append([]byte(fmt.Sprintf("restrict,port-forwarding,permitopen=\"127.0.0.1:%d\" ", destPort)), pub...), 0600); err != nil {
		t.Fatal(err)
	}
	sshPort, localPort := r2port(t), r2port(t)
	config := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s/host\nPidFile %s/sshd.pid\nAuthorizedKeysFile %s/authorized_keys\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nAllowUsers %s\nAllowTcpForwarding local\nPermitOpen 127.0.0.1:%d\nLogLevel ERROR\n", sshPort, dir, dir, dir, current.Username, destPort)
	if err := os.WriteFile(dir+"/sshd_config", []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	server := exec.Command("/usr/sbin/sshd", "-D", "-e", "-f", dir+"/sshd_config")
	serverLog, err := os.Create(dir + "/sshd.log")
	if err != nil {
		t.Fatal(err)
	}
	defer serverLog.Close()
	server.Stderr = serverLog
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { server.Process.Kill(); server.Wait() }()
	assuranceWait(t, "isolated sshd listener", func() bool {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", sshPort), 100*time.Millisecond)
		if e != nil {
			return false
		}
		c.Close()
		return true
	})
	hostpub, err := os.ReadFile(dir + "/host.pub")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/known_hosts", append([]byte(fmt.Sprintf("[127.0.0.1]:%d ", sshPort)), hostpub...), 0600); err != nil {
		t.Fatal(err)
	}
	profile := RemoteProfile{Host: "127.0.0.1", User: current.Username, Port: sshPort, IdentityFile: dir + "/client", KnownHostsFile: dir + "/known_hosts"}
	args := sshTunnelArgs(profile, localPort, "127.0.0.1", uint16(destPort))
	argsJSON, _ := json.Marshal(args)
	parent := exec.Command(os.Args[0], "-test.run=^TestAssuranceSSHParent$")
	parent.Env = append(os.Environ(), "PODDER_ASSURANCE_SSH_PARENT=1", "PODDER_ASSURANCE_SSH_ARGS="+string(argsJSON))
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer parent.Process.Kill()
	url := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	client := &http.Client{Timeout: 300 * time.Millisecond}
	fetch := func() bool {
		res, e := client.Get(url)
		if e != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	}
	assuranceWait(t, "real authenticated SSH forward", fetch)
	// Capture a stable kernel process handle while the verified helper is alive.
	pidfd := -1
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		tokens := strings.Split(string(cmdline), "\x00")
		matchedKey, matchedForward := false, false
		for _, token := range tokens {
			matchedKey = matchedKey || token == profile.IdentityFile
			matchedForward = matchedForward || token == fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, destPort)
		}
		if matchedKey && matchedForward {
			if pidfd != -1 {
				t.Fatal("multiple matching SSH helpers")
			}
			pidfd, err = unix.PidfdOpen(pid, 0)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if pidfd < 0 {
		t.Fatal("live SSH helper identity not found")
	}
	defer unix.Close(pidfd)
	defer unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := parent.Wait(); err == nil {
		t.Fatal("parent not killed")
	}
	assuranceWait(t, "SSH helper exits after parent SIGKILL", func() bool {
		fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
		n, e := unix.Poll(fds, 0)
		return e == nil && n == 1
	})
	assuranceWait(t, "forward listener closes", func() bool { return !fetch() })
}

// Podman 4.9 may retain an exited --rm fixture after its cleanup subprocess
// exits. That is not deletion authority. Assert Podder retained its workspace,
// verify the exact stopped container, remove that fixture by full ID, then retry.
// The descriptor/acknowledgment assertions above do not depend on this fallback.
func assuranceFinishSession(t *testing.T, p *PodmanService, r *round2Runner, id string) (*SessionRuntimeState, error) {
	t.Helper()
	result, err := p.StopAndCleanupSession(id, true, false)
	if err == nil && result != nil && result.State == "cleanup_failed" && strings.Contains(result.Message, "auto-removal not yet confirmed") {
		state, e := loadSessionState(id)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := os.Stat(filepath.Join(sessionRuntimeDir(), id)); e != nil {
			t.Fatal("workspace deleted before verified absence", e)
		}
		c, e := p.sessionContainer(state)
		if e != nil || c == nil {
			t.Fatal("cannot verify retained fixture", e)
		}
		lifecycle, ok := classifyLifecycle(c.State)
		if !ok || lifecycle == lifecycleRunning {
			t.Fatal("fixture still running; refuse manual teardown")
		}
		if e := p.exactTransactionContainer(c.Id, "", c, nil); e != nil {
			t.Fatal(e)
		}
		if _, stderr, e := r.Run("podman", "rm", "--", c.Id); e != nil {
			t.Fatal("exact stopped fixture removal", stderr, e)
		}
		t.Log("engine retained exited --rm fixture; verified workspace retention, removed exact stopped fixture ID and retried cleanup")
		return p.StopAndCleanupSession(id, true, false)
	}
	return result, err
}
