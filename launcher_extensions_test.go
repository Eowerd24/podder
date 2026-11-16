package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTerminalConsentAndArgumentBoundaries(t *testing.T) {
	p, r := launcherFixture(t)
	id := r.containers[0].Id
	options, _ := p.GetLauncherOptions(id)
	d := LauncherDefinition{SchemaVersion: 1, Kind: "terminal", Association: options.Association, Terminal: &TerminalDefinition{Program: "konsole", Command: []string{"printf", "space ; $(touch nope)"}}}
	if err := p.SaveLauncherDefinition(id, d); err != nil {
		t.Fatal(err)
	}
	started := false
	p.lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	p.terminalStart = func(name string, args ...string) error {
		started = true
		if name != "/usr/bin/konsole" || !reflect.DeepEqual(args, []string{"-e", "podman", "exec", "-it", "--", id, "printf", "space ; $(touch nope)"}) {
			t.Fatal(name, args)
		}
		return nil
	}
	if err := p.OpenConfiguredTerminal(id, false); err == nil || started {
		t.Fatal("consent bypassed")
	}
	if err := p.OpenConfiguredTerminal(id, true); err != nil || !started {
		t.Fatal(err)
	}
	d.Terminal.Program = "sh"
	if err := p.SaveLauncherDefinition(id, d); err == nil {
		t.Fatal("arbitrary host executable accepted")
	}
}
func TestGUIRequiresProvisionedAuthenticatedProvider(t *testing.T) {
	p, r := launcherFixture(t)
	id := r.containers[0].Id
	options, _ := p.GetLauncherOptions(id)
	d := webDefinition(testMapping())
	d.Kind = "gui"
	d.Association = options.Association
	d.GUI = &GUIViewerDefinition{Provider: "xpra-html5", SessionIdentity: "session-identity"}
	if err := p.SaveLauncherDefinition(id, d); err == nil {
		t.Fatal("unauthenticated GUI exposed")
	}
	d.GUI.AuthenticationConfirmed = true
	if err := p.SaveLauncherDefinition(id, d); err != nil {
		t.Fatal(err)
	}
	result, err := p.OpenGUIEndpoint(id, "web")
	if err != nil || !result.Opened {
		t.Fatalf("%+v %v", result, err)
	}
	d.GUI.Provider = "host-x11"
	if err := p.SaveLauncherDefinition(id, d); err == nil {
		t.Fatal("host display provider accepted")
	}
}

type remoteTestRunner struct {
	calls []string
	live  []rawContainer
}

func (r *remoteTestRunner) Run(name string, args ...string) (string, string, error) {
	return r.RunContext(context.Background(), name, args...)
}
func (r *remoteTestRunner) RunContext(ctx context.Context, name string, args ...string) (string, string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name != "ssh" || len(args) < 7 || args[len(args)-1] != "podman --remote --url unix:///run/user/1000/podman/podman.sock ps --format json -a" {
		return "", "", fmt.Errorf("unexpected local operation")
	}
	data, err := json.Marshal(r.live)
	return string(data), "", err
}
func TestRemoteProfilesContextAndSSHTunnel(t *testing.T) {
	p, _, _ := sessionFixture(t)
	profile := RemoteProfile{Name: "lab", Host: "example.test", User: "app", Port: 22, Socket: "/run/user/1000/podman/podman.sock"}
	if err := p.SaveRemoteProfile(profile); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("c", 64)
	r := &remoteTestRunner{live: []rawContainer{{Id: id, Names: []string{"web"}, State: "running", Ports: []rawPodmanPort{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}}}}
	p.runner = r
	options, err := p.GetRemoteLauncherOptions(profile.Name, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(options.Association.Key, "remote:") || len(options.Endpoints) > 0 {
		t.Fatal("remote association or detection")
	}
	d := webDefinition(options.Mappings[0])
	d.Association = options.Association
	if err := p.SaveRemoteLauncherDefinition(profile.Name, id, d); err != nil {
		t.Fatal(err)
	}
	local, err := openLauncherRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readLauncher(local, "container:"+id); !os.IsNotExist(err) {
		t.Fatal("remote preferences leaked into local context")
	}
	local.Close()
	started := false
	p.tunnelStart = func(ctx context.Context, name string, args ...string) (<-chan error, error) {
		started = true
		joined := strings.Join(args, " ")
		if name != "ssh" || !strings.Contains(joined, "StrictHostKeyChecking=yes") || strings.Contains(joined, "ClearAllForwardings=yes") || !strings.Contains(joined, "127.0.0.1:") || !strings.Contains(joined, ":127.0.0.1:8080") || !strings.Contains(joined, "-F "+os.DevNull) {
			t.Fatal(joined)
		}
		done := make(chan error, 1)
		go func() { <-ctx.Done(); done <- ctx.Err(); close(done) }()
		return done, nil
	}
	p.tunnelReady = func(context.Context, string, RemoteProfile) error { return nil }
	p.webProbe = func(context.Context, string) error { return nil }
	opened := ""
	p.browserOpen = func(target string) error { opened = target; return nil }
	if _, err := p.OpenRemoteWebEndpoint(profile.Name, id, "web", false); err == nil || started {
		t.Fatal("unapproved SSH forwarding")
	}
	result, err := p.OpenRemoteWebEndpoint(profile.Name, id, "web", true)
	if err != nil || !result.Opened || !strings.HasPrefix(opened, "http://127.0.0.1:") || opened == "http://127.0.0.1:8080/" {
		t.Fatalf("%+v %v", result, err)
	}
	tunnels := p.ListRemoteTunnels()
	if len(tunnels) != 1 || tunnels[0].State != "active" {
		t.Fatal(tunnels)
	}
	if err := p.CloseRemoteTunnel(tunnels[0].ID); err != nil {
		t.Fatal(err)
	}
	p.tunnelReady = func(context.Context, string, RemoteProfile) error { return errors.New("no authenticated master") }
	p.webProbe = func(context.Context, string) error { t.Fatal("HTTP must not substitute for SSH ownership"); return nil }
	opened = ""
	failed, err := p.OpenRemoteWebEndpoint(profile.Name, id, "web", true)
	if err != nil || failed.Opened || opened != "" || len(p.ListRemoteTunnels()) != 0 {
		t.Fatalf("unproven forwarding opened browser: %+v %v", failed, err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, " stop ") || strings.Contains(call, " run ") || !strings.Contains(call, "app@example.test podman --remote --url unix:///run/user/1000/podman/podman.sock ps --format json -a") {
			t.Fatal("remote scope", call)
		}
	}
}
func TestRemoteValidationAndContextRefusal(t *testing.T) {
	base := RemoteProfile{Name: "lab", Host: "example.test", User: "app", Port: 22, Socket: "/run/user/1000/podman/podman.sock"}
	for _, bad := range []string{"--proxycommand=evil", "host;touch /tmp/x", "user@host", "host/../../evil"} {
		copy := base
		copy.Host = bad
		if err := validateRemoteProfile(copy); err == nil {
			t.Fatal(bad)
		}
	}
	copy := base
	copy.User = "root"
	if err := validateRemoteProfile(copy); err == nil {
		t.Fatal("rootful remote accepted")
	}
	for _, socket := range []string{"/run/podman/podman.sock", "/run/user/1000/podman/podman.sock;touch /tmp/evil", "/run/user/1000/../podman/podman.sock", "/run/user/1000/podman/podman.sock\nwhoami"} {
		copy = base
		copy.Socket = socket
		if err := validateRemoteProfile(copy); err == nil {
			t.Fatalf("unsafe socket accepted: %q", socket)
		}
	}
	p, _, s := sessionFixture(t)
	p.runner = contextInfoRunner{payload: `{"host":{"serviceIsRemote":true,"security":{"rootless":true}}}`}
	if _, err := p.LaunchDisposableSession(s); err == nil {
		t.Fatal("remote context treated as local")
	}
	p.runner = contextInfoRunner{payload: `{"host":{"serviceIsRemote":false,"security":{"rootless":false}}}`}
	if _, err := p.LaunchDisposableSession(s); err == nil {
		t.Fatal("rootful context accepted")
	}
}

type contextInfoRunner struct{ payload string }

func (r contextInfoRunner) Run(name string, args ...string) (string, string, error) {
	if name == "podman" && len(args) > 0 && args[0] == "info" {
		return r.payload, "", nil
	}
	return "", "", errors.New("unexpected operation")
}
func TestSessionHeadlessPreviewAndImportConsent(t *testing.T) {
	p, _, s := sessionFixture(t)
	s.Policy = "configuration"
	payload, _ := json.Marshal(s)
	var out bytes.Buffer
	if err := runSessionCLI([]string{"preview"}, bytes.NewReader(payload), &out, p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--rm") {
		t.Fatal(out.String())
	}
	if err := runSessionCLI([]string{"launch"}, bytes.NewReader(payload), &out, p); err == nil {
		t.Fatal("headless launch without consent")
	}
	out.Reset()
	if err := runSessionCLI([]string{"import"}, bytes.NewReader(payload), &out, p); err != nil {
		t.Fatal(err)
	}
	recipes, _ := p.ListSessionRecipes()
	if len(recipes) != 0 {
		t.Fatal("review imported file was saved automatically")
	}
	if err := runSessionCLI([]string{"import", "--confirm"}, bytes.NewReader(payload), &out, p); err != nil {
		t.Fatal(err)
	}
	recipes, _ = p.ListSessionRecipes()
	if len(recipes) != 1 {
		t.Fatal("recipe not saved")
	}
}
func TestSessionRetainedDataAndOwnedImageInUse(t *testing.T) {
	p, r, s := sessionFixture(t)
	s.Policy = "data"
	state, err := p.LaunchDisposableSession(s)
	if err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(sessionDataDir(), "sessions", s.Name, "data", "keep")
	if err := os.WriteFile(dataPath, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.StopAndCleanupSession(state.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.RelaunchSessionRecipe(s.Name, nil, nil); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(dataPath)
	if err != nil || string(value) != "retained" {
		t.Fatal("data did not survive relaunch")
	}
	// Image cleanup refuses an image used by an unrelated container.
	state.ImageOwned = true
	state.ImageTag = "localhost/podder-session:" + state.ID
	r.imageTag = state.ImageTag
	r.buildImage = state.ImageID
	if err := p.cleanupSessionImage(state); err == nil {
		t.Fatal("in-use image deleted")
	}
}

func TestRemoteExplicitHostTrust(t *testing.T) {
	profile := RemoteProfile{Name: "lab", Host: "example.test", User: "app", Port: 2222, Socket: "/run/user/1000/podman/podman.sock", IdentityFile: "/keys/test key", KnownHostsFile: `/trust/lab "quoted" %keys`}
	if err := validateRemoteProfile(profile); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{remoteObservationArgs(profile), sshTunnelArgs(profile, 49190, "127.0.0.1", 8080)} {
		joined := strings.Join(args, " ")
		for _, want := range []string{"StrictHostKeyChecking=yes", "UpdateHostKeys=no", "BatchMode=yes", "IdentitiesOnly=yes", `UserKnownHostsFile="/trust/lab \"quoted\" %%keys"`, "-p 2222"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("missing %q in %v", want, args)
			}
		}
	}
	for _, path := range []string{"relative", "/trust/../hosts", "/trust/hosts\nProxyCommand=evil", "/trust/hosts\x00"} {
		profile.KnownHostsFile = path
		if validateRemoteProfile(profile) == nil {
			t.Fatalf("accepted host-key path %q", path)
		}
	}
}
