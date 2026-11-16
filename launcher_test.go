package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLauncherURL(t *testing.T) {
	for _, tc := range []struct{ bind, want string }{
		{"127.0.0.1", "http://127.0.0.1:8080/"}, {"0.0.0.0", "http://127.0.0.1:8080/"}, {"", "http://127.0.0.1:8080/"}, {"*", "http://127.0.0.1:8080/"}, {"::1", "http://[::1]:8080/"}, {"::", "http://[::1]:8080/"}, {"192.0.2.1", "http://192.0.2.1:8080/"},
	} {
		t.Run(tc.bind, func(t *testing.T) {
			got, err := launcherURL(WebEndpoint{Scheme: "http", Mapping: PortMapping{HostIP: tc.bind, HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}})
			if err != nil || got != tc.want {
				t.Fatalf("%q %v", got, err)
			}
		})
	}
	for _, bad := range []string{"javascript", "file", "ftp", "http;evil", "HTTP"} {
		if _, err := launcherURL(WebEndpoint{Scheme: bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, bad := range []string{"//evil.test", "https://evil.test", "/\\evil", "/a?token=secret", "/a#fragment", "/a\n", "/%0a", "/%5c", "/%3fsecret", "/%xx"} {
		e := webDefinition(testMapping()).Endpoints[0]
		e.Path = bad
		if _, err := launcherURL(e); err == nil {
			t.Errorf("accepted path %q", bad)
		}
	}
	for _, bind := range []string{"evil.test", "127.0.0.1;touch /tmp/x", "fe80::1%eth0", "224.0.0.1"} {
		e := webDefinition(testMapping()).Endpoints[0]
		e.Mapping.HostIP = bind
		if _, err := launcherURL(e); err == nil {
			t.Errorf("accepted host %q", bind)
		}
	}
	e := webDefinition(testMapping()).Endpoints[0]
	e.Path = "/hello%20world/<script>"
	got, err := launcherURL(e)
	if err != nil || !strings.Contains(got, "%3Cscript%3E") {
		t.Fatalf("unsafe/unencoded URL: %s %v", got, err)
	}
}
func testMapping() PortMapping {
	return PortMapping{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}
}
func webDefinition(m PortMapping) LauncherDefinition {
	return LauncherDefinition{SchemaVersion: 1, Kind: "web", Endpoints: []WebEndpoint{{ID: "web", Scheme: "http", Mapping: m, Path: "/"}}}
}
func TestLauncherPublishedMappings(t *testing.T) {
	for _, spec := range []string{"8080:80/tcp", "[::1]:8080-8082:80-82/tcp", "8080:80/udp", "80", "0:80", "127.0.0.1::80", "8080:80"} {
		t.Run(spec, func(t *testing.T) {
			m, err := ParsePublishSpec(spec)
			if err != nil {
				t.Fatal(err)
			}
			got := launcherMappings([]PortMapping{*m})
			want := 1
			if m.HostPort == 0 || m.Protocol == "udp" {
				want = 0
			}
			if m.RangeSize > 1 {
				want = m.RangeSize
			}
			if len(got) != want {
				t.Fatalf("got %v want %d", got, want)
			}
			for _, single := range got {
				if single.RangeSize != 0 {
					t.Fatal("range was guessed")
				}
			}
		})
	}
	for _, m := range []PortMapping{{HostPort: 1}, {ContainerPort: 80}, {HostPort: 65535, ContainerPort: 80, RangeSize: 2}, {HostPort: 8080, ContainerPort: 80, RangeSize: 257}, {HostPort: 8080, ContainerPort: 80, Protocol: "sctp"}} {
		if len(launcherMappings([]PortMapping{m})) != 0 {
			t.Fatalf("accepted invalid mapping %+v", m)
		}
	}
}
func TestLauncherValidation(t *testing.T) {
	m := testMapping()
	d := webDefinition(m)
	if _, err := validateLauncher(d, []PortMapping{m}); err != nil {
		t.Fatal(err)
	}
	cases := []LauncherDefinition{d, d, d, d, d, d, d, d}
	cases[0].SchemaVersion = 2
	cases[1].Kind = "bad"
	cases[2].Endpoints = nil
	cases[3].PreferredEndpointID = "absent"
	cases[4].Kind = "none"
	cases[5].Endpoints = append([]WebEndpoint{}, d.Endpoints[0], d.Endpoints[0])
	cases[6].Endpoints = []WebEndpoint{{ID: "<x>", Scheme: "http", Mapping: m}}
	cases[7].Endpoints = []WebEndpoint{{ID: "x", Scheme: "http", Mapping: PortMapping{HostPort: 5432, ContainerPort: 5432}}}
	for i, c := range cases {
		if _, err := validateLauncher(c, []PortMapping{m}); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if _, err := validateLauncher(d, nil); err == nil {
		t.Fatal("stale published mapping accepted")
	}
	for _, kind := range []string{"none", "gui", "terminal"} {
		if out, err := validateLauncher(LauncherDefinition{SchemaVersion: 1, Kind: kind}, nil); err != nil || len(out) != 0 {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	second := d.Endpoints[0]
	second.ID = "second"
	d.Endpoints = append(d.Endpoints, second)
	d.OpenAfterStart = true
	if _, err := validateLauncher(d, []PortMapping{m}); err == nil {
		t.Fatal("ambiguous Start & Open accepted")
	}
	d.PreferredEndpointID = "second"
	if _, err := validateLauncher(d, []PortMapping{m}); err != nil {
		t.Fatal(err)
	}
}

type launcherRunner struct {
	containers []Container
	calls      []string
	fail       bool
}

func (r *launcherRunner) Run(name string, args ...string) (string, string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	args = stripTestOptionBoundary(args)
	if name != "podman" {
		return "", "", fmt.Errorf("unexpected command %s", name)
	}
	if len(args) > 0 && args[0] == "info" {
		return `{"host":{"serviceIsRemote":false,"security":{"rootless":true}}}`, "", nil
	}
	if len(args) > 0 && args[0] == "ps" {
		if r.fail {
			return "", "denied", errors.New("denied")
		}
		raw := []rawContainer{}
		for _, c := range r.containers {
			ports := []rawPodmanPort{}
			for _, m := range c.PortMappings {
				ports = append(ports, rawPodmanPort{HostIP: m.HostIP, HostPort: m.HostPort, ContainerPort: m.ContainerPort, Range: m.RangeSize, Protocol: m.Protocol})
			}
			raw = append(raw, rawContainer{Id: c.Id, Names: c.Names, ImageID: c.ImageID, State: c.State, Labels: c.Labels, Ports: ports, Pod: c.Pod})
		}
		data, _ := json.Marshal(raw)
		return string(data), "", nil
	}
	if len(args) == 2 && args[0] == "start" {
		r.containers[0].State = "running"
		return "", "", nil
	}
	return "", "", fmt.Errorf("unexpected mutation %v", args)
}
func launcherFixture(t *testing.T) (*PodmanService, *launcherRunner) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := &launcherRunner{containers: []Container{{Id: strings.Repeat("a", 64), Names: []string{"app"}, State: "running", ImageID: "sha256:image", PortMappings: []PortMapping{testMapping()}}}}
	p := &PodmanService{runner: r, browserOpen: func(string) error { return nil }, webProbe: func(context.Context, string) error { return nil }}
	return p, r
}
func saveFixtureLauncher(t *testing.T, p *PodmanService, id string) LauncherDefinition {
	t.Helper()
	o, err := p.GetLauncherOptions(id)
	if err != nil {
		t.Fatal(err)
	}
	d := webDefinition(testMapping())
	d.Association = o.Association
	if err := p.SaveLauncherDefinition(id, d); err != nil {
		t.Fatal(err)
	}
	return d
}
func TestLauncherPersistenceOpenAndCopy(t *testing.T) {
	p, r := launcherFixture(t)
	id := r.containers[0].Id
	o, _ := p.GetLauncherOptions(id)
	if len(o.Endpoints) != 0 || o.Definition != nil {
		t.Fatal("guessed HTTP from TCP")
	}
	d := saveFixtureLauncher(t, p, id)
	// New service instance simulates a restart, using only persisted metadata.
	p = &PodmanService{runner: r, browserOpen: func(target string) error {
		if target != "http://127.0.0.1:8080/" {
			t.Fatal(target)
		}
		return nil
	}, webProbe: func(context.Context, string) error { return nil }}
	result, err := p.OpenWebEndpoint(id, "web")
	if err != nil || !result.Opened {
		t.Fatalf("%+v %v", result, err)
	}
	url, err := p.GetWebEndpointURL(id, "web")
	if err != nil || url != result.URL {
		t.Fatalf("copy %s %v", url, err)
	}
	p.browserOpen = func(string) error { return errors.New("missing opener") }
	result, err = p.OpenWebEndpoint(id, "web")
	if err != nil || result.Opened || result.URL == "" {
		t.Fatal("missing copy fallback")
	}
	p.webProbe = func(context.Context, string) error { return errors.New("not HTTP") }
	result, err = p.OpenWebEndpoint(id, "web")
	if err != nil || result.Opened || !strings.Contains(result.Message, "HTTP") {
		t.Fatal("readiness bypassed")
	}
	r.containers[0].State = "exited"
	if _, err := p.OpenWebEndpoint(id, "web"); err == nil {
		t.Fatal("opened stopped container")
	}
	d.OpenAfterStart = true
	if err := p.SaveLauncherDefinition(id, d); err != nil {
		t.Fatal(err)
	}
	p.webProbe = func(context.Context, string) error { return nil }
	result, err = p.StartAndOpenWebEndpoint(id, "web")
	if err != nil || result.URL == "" || r.containers[0].State != "running" {
		t.Fatalf("start/open %v %v", result, err)
	}
	for _, call := range r.calls {
		if !strings.HasPrefix(call, "podman ps ") && !strings.HasPrefix(call, "podman info ") && call != "podman start -- "+id {
			t.Fatal("unauthorized command", call)
		}
	}
}
func TestLauncherExternalOwnershipAndStaleIdentity(t *testing.T) {
	for _, owner := range []string{"adhoc", "compose", "quadlet", "pod", "ambiguous"} {
		t.Run(owner, func(t *testing.T) {
			p, r := launcherFixture(t)
			c := &r.containers[0]
			switch owner {
			case "compose":
				c.Labels = map[string]string{"com.docker.compose.project": "p"}
			case "quadlet":
				c.Labels = map[string]string{"PODMAN_SYSTEMD_UNIT": "x.service"}
			case "pod":
				c.Pod = "pod-id"
			case "ambiguous":
				c.Labels = map[string]string{"com.docker.compose.project": "p", "io.podder.managed": "true"}
			}
			before := *c
			d := saveFixtureLauncher(t, p, c.Id)
			if !reflect.DeepEqual(before, *c) || d.Association.Key != "container:"+c.Id {
				t.Fatal("ownership altered")
			}
			originalID := c.Id
			c.Id = strings.Repeat("b", 64)
			o, err := p.GetLauncherOptions(c.Id)
			if err != nil || o.Definition != nil || len(o.Stale) != 1 {
				t.Fatalf("stale %+v %v", o, err)
			}
			if err := p.SaveLauncherDefinition(c.Id, d); err == nil {
				t.Fatal("stale association accepted")
			}
			d.Association = o.Association
			if err := p.SaveLauncherDefinition(c.Id, d); err != nil {
				t.Fatal(err)
			}
			if _, err := p.OpenWebEndpoint(originalID, "web"); err == nil {
				t.Fatal("removed identity accepted")
			}
			c.PortMappings[0].HostPort++
			if _, err := p.OpenWebEndpoint(c.Id, "web"); err == nil {
				t.Fatal("stale port accepted")
			}
			for _, call := range r.calls {
				if !strings.HasPrefix(call, "podman ps ") && !strings.HasPrefix(call, "podman info ") {
					t.Fatal("external mutation", call)
				}
			}
		})
	}
}
func TestLauncherVerifiedManagedIdentity(t *testing.T) {
	p, r := launcherFixture(t)
	c := &r.containers[0]
	c.Labels = map[string]string{"io.podder.managed": "true", "io.podder.service": "app", "io.podder.schema-version": "2"}
	// Forged labels without an authoritative local spec do not grant stable identity.
	o, _ := p.GetLauncherOptions(c.Id)
	if o.Association.Key != "container:"+c.Id {
		t.Fatal("trusted labels")
	}
	spec := ContainerSpec{SchemaVersion: 2, Name: "app", Image: "image", ResolvedImage: c.ImageID, Managed: true, ReplayComplete: true, PortMappings: c.PortMappings}
	if err := saveSpec(spec); err != nil {
		t.Fatal(err)
	}
	d := saveFixtureLauncher(t, p, c.Id)
	if d.Association.Key != "service:app" {
		t.Fatal(d.Association)
	}
	before, _ := os.ReadFile(filepath.Join(getConfigDir(), "services", "app.json"))
	c.Id = strings.Repeat("c", 64)
	o, _ = p.GetLauncherOptions(c.Id)
	if o.Definition == nil || len(o.Endpoints) != 1 {
		t.Fatalf("managed recreation lost settings %+v", o)
	}
	after, _ := os.ReadFile(filepath.Join(getConfigDir(), "services", "app.json"))
	if string(before) != string(after) {
		t.Fatal("spec changed")
	}
	args, err := BuildRunArgsFromSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range args {
		if arg == "--rm" {
			t.Fatal("managed workload became disposable")
		}
	}
}
func TestLauncherStorageAndFailClosed(t *testing.T) {
	p, r := launcherFixture(t)
	id := r.containers[0].Id
	d := saveFixtureLauncher(t, p, id)
	root, err := openLauncherRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	path := filepath.Join(root.Name(), launcherFile(d.Association.Key))
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if strings.Contains(launcherFile("../../escape"), "/") {
		t.Fatal("unsafe filename")
	}
	bad := d
	bad.SchemaVersion = 999
	data, _ := json.Marshal(bad)
	os.WriteFile(path, data, 0600)
	if _, err := p.OpenWebEndpoint(id, "web"); err == nil {
		t.Fatal("future schema accepted")
	}
	os.Remove(path)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte(`{}`), 0600)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := p.OpenWebEndpoint(id, "web"); err == nil {
		t.Fatal("symlink escape accepted")
	}
	r.fail = true
	if _, err := p.OpenWebEndpoint(id, "web"); err == nil {
		t.Fatal("runtime failure ignored")
	}
}
func TestWebProbeHTTPRedirectAndTLS(t *testing.T) {
	var redirected bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer server.Close()
	if err := probeWeb(context.Background(), server.URL); err != nil {
		t.Fatal(err)
	}
	if redirected {
		t.Fatal("followed redirect")
	}
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tls.Close()
	if err := probeWeb(context.Background(), tls.URL); err == nil {
		t.Fatal("TLS verification disabled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probeWeb(ctx, server.URL); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestLauncherStartOpenRequiresOptInAndTimesOutSafely(t *testing.T) {
	p, r := launcherFixture(t)
	id := r.containers[0].Id
	d := saveFixtureLauncher(t, p, id)
	r.containers[0].State = "exited"
	if _, err := p.StartAndOpenWebEndpoint(id, "web"); err == nil {
		t.Fatal("started without explicit opt-in")
	}
	if r.containers[0].State != "exited" {
		t.Fatal("unauthorized start")
	}
	d.OpenAfterStart = true
	if err := p.SaveLauncherDefinition(id, d); err != nil {
		t.Fatal(err)
	}
	opened := false
	p.browserOpen = func(string) error { opened = true; return nil }
	p.webProbe = func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }
	result, err := p.StartAndOpenWebEndpoint(id, "web")
	if err != nil || opened || result.Opened || result.URL == "" || !strings.Contains(result.Message, "15 seconds") {
		t.Fatalf("timeout %+v %v", result, err)
	}
	if r.containers[0].State != "running" {
		t.Fatal("timeout changed lifecycle after start")
	}
}
func TestLauncherRechecksIdentityAfterReadiness(t *testing.T) {
	p, r := launcherFixture(t)
	id := r.containers[0].Id
	saveFixtureLauncher(t, p, id)
	opened := false
	p.browserOpen = func(string) error { opened = true; return nil }
	p.webProbe = func(context.Context, string) error { r.containers[0].Id = strings.Repeat("d", 64); return nil }
	if _, err := p.OpenWebEndpoint(id, "web"); err == nil || opened {
		t.Fatal("opened removed container after readiness")
	}
}
