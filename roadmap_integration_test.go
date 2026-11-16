//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// This campaign is inert unless an operator explicitly approves a pinned image.
// Its independent Podman storage is deliberately retained for review; it never
// prunes the user's normal image store or deletes a live container's storage.
type isolatedRoadmapRunner struct{ prefix []string }

func (r isolatedRoadmapRunner) podmanInnerPrefix() []string { return append([]string{}, r.prefix...) }

func (r isolatedRoadmapRunner) Run(name string, args ...string) (string, string, error) {
	if name == "podman" {
		args = append(append([]string{}, r.prefix...), args...)
	}
	return (execCommandRunner{}).Run(name, args...)
}
func (r isolatedRoadmapRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "podman" {
		args = append(append([]string{}, r.prefix...), args...)
	}
	return (execCommandRunner{}).RunContext(ctx, name, args...)
}
func approvedRoadmapService(t *testing.T) *PodmanService {
	t.Helper()
	if os.Getenv("PODDER_SMOKE_APPROVED") != "1" {
		t.Skip("requires explicit isolated-environment approval")
	}
	base, err := os.MkdirTemp("", "podder-roadmap-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Isolated storage retained for review: %s", base)
	for _, name := range []string{"config", "data", "runtime", "engine", "runroot"} {
		if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(base, "runtime"))
	p := &PodmanService{runner: isolatedRoadmapRunner{prefix: []string{"--remote=false", "--root", filepath.Join(base, "engine"), "--runroot", filepath.Join(base, "runroot"), "--storage-driver=vfs"}}, browserOpen: func(string) error { return nil }}
	if err := p.ensureLocalContext(true); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestApprovedIsolatedOCIWorkflow(t *testing.T) {
	image := os.Getenv("PODDER_SMOKE_IMAGE")
	if os.Getenv("PODDER_SMOKE_APPROVED") != "1" {
		t.Skip("requires explicit isolated-environment approval")
	}
	if !regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`).MatchString(image) {
		t.Fatal("PODDER_SMOKE_IMAGE must be an approved immutable HTTP image reference")
	}
	port, err := strconv.ParseUint(os.Getenv("PODDER_SMOKE_CONTAINER_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("PODDER_SMOKE_CONTAINER_PORT must explicitly identify its HTTP port")
	}
	p := approvedRoadmapService(t)
	for _, policy := range []string{"temporary", "configuration", "data"} {
		t.Run(policy, func(t *testing.T) {
			host, err := p.FindFreePort(49160, "tcp", "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			mapping := PortMapping{HostIP: "127.0.0.1", HostPort: host, ContainerPort: uint16(port), Protocol: "tcp"}
			spec := DisposableSessionSpec{SchemaVersion: 1, Name: "roadmap-smoke-" + policy, Image: image, Policy: policy, PortMappings: []PortMapping{mapping}, Config: SessionMount{Target: "/podder-verification/config"}, Data: SessionMount{Target: "/podder-verification/data", ReadWrite: true}, Web: &WebEndpoint{ID: "web", Scheme: "http", Mapping: mapping, Path: "/"}}
			preview, err := p.PreviewDisposableSession(spec)
			if err != nil {
				t.Fatal(err)
			}
			spec.LaunchID = preview.Spec.LaunchID
			state, err := p.LaunchDisposableSession(spec)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if state == nil {
					return
				}
				result, err := p.StopAndCleanupSession(state.ID, true, false)
				if err != nil || result.State != "stopped" {
					t.Errorf("scoped cleanup incomplete; retained journal %s: %+v %v", state.ID, result, err)
				}
			}()
			if state.State != "running" {
				t.Fatal(state.Message)
			}
			opts, err := p.GetLauncherOptions(state.ContainerID)
			if err != nil {
				t.Fatal(err)
			}
			d := *opts.Definition
			d.OpenAfterStart = true
			if err := p.SaveLauncherDefinition(state.ContainerID, d); err != nil {
				t.Fatal(err)
			}
			result, err := p.StartAndOpenWebEndpoint(state.ContainerID, "web")
			if err != nil || !result.Opened {
				t.Fatalf("real HTTP check: %+v %v", result, err)
			}
			recreated := &PodmanService{runner: p.runner}
			listed, err := recreated.ListDisposableSessions()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, s := range listed {
				if s.ID == state.ID && s.State == "running" {
					found = true
				}
			}
			if !found {
				t.Fatal("relaunch reconciliation failed")
			}
			binds := plannedSessionBinds(spec, state.ID)
			for _, bind := range binds {
				if err := os.WriteFile(filepath.Join(bind.HostPath, "sentinel"), []byte(policy), 0600); err != nil {
					t.Fatal(err)
				}
				out, _, err := p.runCommand("exec", state.ContainerID, "cat", bind.ContainerPath+"/sentinel")
				if err != nil || out != policy {
					t.Fatalf("mounted sentinel unreadable: %q %v", out, err)
				}
			}
			cleaned, err := p.StopAndCleanupSession(state.ID, true, false)
			if err != nil || cleaned.State != "stopped" {
				t.Fatalf("cleanup %+v %v", cleaned, err)
			}
			if _, err := os.Stat(filepath.Join(sessionRuntimeDir(), state.ID)); !os.IsNotExist(err) {
				t.Fatalf("temporary workspace remains: %v", err)
			}
			for index, bind := range binds {
				retained := policy == "data" || policy == "configuration" && index == 0
				_, err := os.Stat(filepath.Join(bind.HostPath, "sentinel"))
				if retained && err != nil || !retained && !os.IsNotExist(err) {
					t.Fatalf("retention mismatch for %s: %v", bind.HostPath, err)
				}
			}
			if _, err := p.resolveImageID(state.ImageID); err != nil {
				t.Fatalf("source image was removed: %v", err)
			}
			if policy != "temporary" {
				relaunched, err := p.RelaunchSessionRecipe(spec.Name, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				state = relaunched // deferred cleanup also covers a failed relaunch assertion
				if state.State != "running" {
					t.Fatal(state.Message)
				}
				for index, bind := range plannedSessionBinds(state.Spec, state.ID) {
					out, _, err := p.runCommand("exec", state.ContainerID, "cat", bind.ContainerPath+"/sentinel")
					retained := policy == "data" || index == 0
					if retained && (err != nil || out != policy) || !retained && err == nil {
						t.Fatalf("relaunch retention mismatch for %s: %q %v", bind.ContainerPath, out, err)
					}
				}
			} else {
				// Temporary cleanup removed its journal, so the defer must not repeat it.
				state = nil
			}
			t.Log("Verified mount contents, temporary removal, source-image retention and retained recipe relaunch")
		})
	}
}
func TestApprovedIsolatedGitHubWorkflow(t *testing.T) {
	if os.Getenv("PODDER_SMOKE_BUILD_APPROVED") != "1" {
		t.Skip("requires separate approval of the exact immutable repository build")
	}
	p := approvedRoadmapService(t)
	source := &GitHubSource{URL: os.Getenv("PODDER_SMOKE_REPO"), Commit: os.Getenv("PODDER_SMOKE_COMMIT"), Subdirectory: os.Getenv("PODDER_SMOKE_SUBDIRECTORY")}
	if err := validateGitHubSource(*source); err != nil {
		t.Fatal(err)
	}
	spec := DisposableSessionSpec{SchemaVersion: 1, Name: "roadmap-github", Image: "review-pending", Policy: "temporary", GitHub: source}
	fixture := os.Getenv("PODDER_SMOKE_FIXTURE")
	if fixture != "" {
		if fixture != "glance" && fixture != "vault-dashboard" {
			t.Fatal("unknown explicit fixture")
		}
		portStart := uint16(49160)
		if value := os.Getenv("PODDER_SMOKE_PORT_START"); value != "" {
			parsed, err := strconv.ParseUint(value, 10, 16)
			if err != nil || parsed < 1024 {
				t.Fatal("invalid fixture port start")
			}
			portStart = uint16(parsed)
		}
		host, err := p.FindFreePort(portStart, "tcp", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		mapping := PortMapping{HostIP: "127.0.0.1", HostPort: host, ContainerPort: 8080, Protocol: "tcp"}
		spec.Name = fixture
		spec.PortMappings = []PortMapping{mapping}
		spec.Web = &WebEndpoint{ID: "web", Scheme: "http", Mapping: mapping, Path: "/"}
		if fixture == "glance" {
			spec.Config = SessionMount{Target: "/app/config"}
		} else {
			spec.KeepID = true
			spec.Config = SessionMount{Target: "/config"}
			spec.Data = SessionMount{Target: "/data", ReadWrite: true}
			spec.Env = map[string]string{"HOST_PROC": "/proc", "HOST_SYS": "/sys", "OS_RELEASE": "/etc/os-release", "TZ": "UTC"}
		}
	}

	state, err := p.ReviewGitHubSession(spec)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "draft" {
		t.Fatal(state.Message)
	}
	sessionID := state.ID
	t.Cleanup(func() {
		if sessionBuildActive(sessionID) {
			_ = p.CancelSessionBuild(sessionID)
			for sessionBuildActive(sessionID) {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if _, err := loadSessionState(sessionID); err == nil {
			result, e := p.StopAndCleanupSession(sessionID, true, true)
			if e != nil || result.State != "stopped" {
				t.Errorf("owned cleanup incomplete for %s: %+v %v", sessionID, result, e)
			}
		}
	})
	if fixture != "" {
		workspaceBinds, err := prepareSessionWorkspace(state)
		if err != nil {
			t.Fatal(err)
		}
		closeSessionBindSources(workspaceBinds)
		root, err := privateRoot(sessionRuntimeDir())
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		filename := "glance.yml"
		contents := "pages:\n  - name: Podder Smoke\n    columns:\n      - size: full\n        widgets:\n          - type: calendar\n"
		if fixture == "vault-dashboard" {
			filename = "vault.json"
			data, _ := json.Marshal(map[string]any{"host": "podder-smoke", "network": "isolated", "services": []map[string]string{{"id": "self", "check": "http://127.0.0.1:8080/"}}})
			contents = string(data)
		}
		file, err := root.OpenFile(filepath.Join(state.ID, "config", filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(contents); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("Reviewed source %s @ %s\n%s", source.URL, source.Commit, state.BuildDefinition)
	state, err = p.StartReviewedSessionBuild(state.ID, true, os.Getenv("PODDER_SMOKE_BUILD_NETWORK") == "1")
	if err != nil {
		t.Fatal(err)
	}
	buildStarted := time.Now()
	for sessionBuildActive(state.ID) {
		time.Sleep(time.Second)
		if elapsed := int(time.Since(buildStarted).Seconds()); elapsed > 0 && elapsed%30 == 0 {
			t.Logf("Build active for %ds", elapsed)
		}
	}
	state, err = loadSessionState(state.ID)
	if err != nil || state.State != "built" {
		t.Fatalf("build %+v %v", state, err)
	}
	state, err = p.LaunchBuiltSession(state.ID, spec.Env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "running" {
		t.Fatal(state.Message)
	}
	if fixture != "" {
		options, err := p.GetLauncherOptions(state.ContainerID)
		if err != nil {
			t.Fatal(err)
		}
		definition := *options.Definition
		definition.OpenAfterStart = true
		if err := p.SaveLauncherDefinition(state.ContainerID, definition); err != nil {
			t.Fatal(err)
		}
		opened, err := p.StartAndOpenWebEndpoint(state.ContainerID, "web")
		if err != nil || !opened.Opened {
			logs, _ := p.GetContainerLogs(state.ContainerID)
			t.Fatalf("HTTP startup %+v %v; scoped logs: %s", opened, err, logs)
		}
		client := http.Client{Timeout: 3 * time.Second}
		response, err := client.Get(opened.URL)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode >= 500 {
			t.Fatal(response.Status)
		}
		t.Logf("Live HTTP endpoint verified: %s; image %s; owns image: %v", opened.URL, state.ImageID, state.ImageOwned)
	}
	result, err := p.StopAndCleanupSession(state.ID, true, true)
	if err != nil || result.State != "stopped" {
		t.Fatal(fmt.Sprintf("cleanup %+v %v", result, err))
	}
	if _, err := os.Stat(filepath.Join(sessionRuntimeDir(), sessionID)); !os.IsNotExist(err) {
		t.Fatalf("owned source workspace remains: %v", err)
	}
	if state.ImageOwned {
		if _, err := p.resolveImageID(state.ImageTag); err == nil {
			t.Fatal("owned image tag remains after requested cleanup")
		}
	}
}

// The VM fixture is provisioned separately by the operator/test harness. This
// test only observes its exact container ID and opens/closes local SSH helpers.
func TestApprovedRemoteWorkflow(t *testing.T) {
	if os.Getenv("PODDER_REMOTE_APPROVED") != "1" {
		t.Skip("requires approval and an explicitly provisioned remote fixture")
	}
	base, err := os.MkdirTemp("", "pdr-remote-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownLauncherHelpers(); os.RemoveAll(base) })
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_RUNTIME_DIR", base)
	profile := RemoteProfile{Name: "approved-vm", Host: os.Getenv("PODDER_REMOTE_HOST"), User: os.Getenv("PODDER_REMOTE_USER"), Port: 22, Socket: "/run/user/1000/podman/podman.sock", IdentityFile: os.Getenv("PODDER_REMOTE_IDENTITY"), KnownHostsFile: os.Getenv("PODDER_REMOTE_KNOWN_HOSTS")}
	id := os.Getenv("PODDER_REMOTE_CONTAINER")
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) {
		t.Fatal("full fixture container ID required")
	}
	opened := ""
	guiBrowserVerified := false
	p := &PodmanService{browserOpen: func(target string) error {
		opened = target
		if script := os.Getenv("PODDER_GUI_BROWSER_SCRIPT"); script != "" && !guiBrowserVerified {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "node", script)
			command.Env = append(os.Environ(), "PODDER_GUI_URL="+target)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Logf("GUI browser validation: %v: %s", err, output)
				return fmt.Errorf("GUI browser validation failed")
			}
			t.Logf("%s", output)
			guiBrowserVerified = true
		}
		return nil
	}}
	if err := p.SaveRemoteProfile(profile); err != nil {
		t.Fatal(err)
	}
	options, err := p.GetRemoteLauncherOptions(profile.Name, id)
	if err != nil {
		t.Fatal(err)
	}
	if profile.KnownHostsFile != "" {
		// This file contains public host keys, not private credentials. Verify
		// actual OpenSSH quoting and literal percent-token handling as well.
		trust, err := os.ReadFile(profile.KnownHostsFile)
		if err != nil {
			t.Fatal(err)
		}
		profile.KnownHostsFile = filepath.Join(base, `known "quoted" %hosts`)
		if err := os.WriteFile(profile.KnownHostsFile, trust, 0600); err != nil {
			t.Fatal(err)
		}
		if err := p.SaveRemoteProfile(profile); err != nil {
			t.Fatal(err)
		}
		if _, err := p.ListRemoteContainers(profile.Name); err != nil {
			t.Fatalf("literal known-hosts path: %v", err)
		}
	}
	if !options.Running || len(options.Mappings) != 1 {
		t.Fatalf("fixture mappings: %+v", options)
	}
	definition := webDefinition(options.Mappings[0])
	definition.Association = options.Association
	if os.Getenv("PODDER_REMOTE_GUI_ID") != "" {
		definition.Kind = "gui"
		definition.GUI = &GUIViewerDefinition{Provider: "xpra-html5", SessionIdentity: os.Getenv("PODDER_REMOTE_GUI_ID"), AuthenticationConfirmed: true}
	}
	if err := p.SaveRemoteLauncherDefinition(profile.Name, id, definition); err != nil {
		t.Fatal(err)
	}
	if _, err := p.OpenRemoteWebEndpoint(profile.Name, id, "web", false); err == nil || opened != "" || len(p.ListRemoteTunnels()) != 0 {
		t.Fatal("consent gate bypassed")
	}
	for _, shutdown := range []bool{false, true} {
		result, err := p.OpenRemoteWebEndpoint(profile.Name, id, "web", true)
		if err != nil || !result.Opened || opened != result.URL {
			t.Fatalf("remote open: %+v %v", result, err)
		}
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Get(result.URL)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode >= 500 {
			t.Fatal(response.Status)
		}
		tunnels := p.ListRemoteTunnels()
		if len(tunnels) != 1 || tunnels[0].State != "active" {
			t.Fatalf("tunnel registry: %+v", tunnels)
		}
		target, _ := url.Parse(result.URL)
		if shutdown {
			shutdownLauncherHelpers()
		} else if err := p.CloseRemoteTunnel(tunnels[0].ID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			connection, err := net.DialTimeout("tcp", target.Host, 100*time.Millisecond)
			if err != nil {
				break
			}
			connection.Close()
			if time.Now().After(deadline) {
				t.Fatal("SSH listener survived close/shutdown")
			}
			time.Sleep(50 * time.Millisecond)
		}
		if len(p.ListRemoteTunnels()) != 0 {
			t.Fatal("stale helper registry after close/shutdown")
		}
		t.Logf("Verified %s routing, HTTP %d, authenticated SSH master and helper shutdown=%v", definition.Kind, response.StatusCode, shutdown)
	}
	if os.Getenv("PODDER_GUI_BROWSER_SCRIPT") != "" && !guiBrowserVerified {
		t.Fatal("live GUI browser validation did not pass")
	}
	// A wrong, empty trust file must fail without a prompt or browser dispatch.
	wrongTrust := filepath.Join(base, "empty-known-hosts")
	if err := os.WriteFile(wrongTrust, nil, 0600); err != nil {
		t.Fatal(err)
	}
	profile.KnownHostsFile = wrongTrust
	if err := p.SaveRemoteProfile(profile); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ListRemoteContainers(profile.Name); err == nil {
		t.Fatal("unknown SSH host key accepted")
	}
}
