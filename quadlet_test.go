package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withQuadletTestDirs isolates Quadlet discovery to temp directories for
// the duration of a test: XDG_CONFIG_HOME controls the rootless user
// search path, and quadletRootOverride prefixes the hard-coded system
// search paths so tests never touch real host directories like
// /etc/containers/systemd.
func withQuadletTestDirs(t *testing.T) (userDir, systemDir string) {
	t.Helper()
	root := t.TempDir()

	userDir = filepath.Join(root, "xdg", "containers", "systemd")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("failed to create user quadlet dir: %v", err)
	}
	systemDir = filepath.Join(root, "sysroot", "etc", "containers", "systemd")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("failed to create system quadlet dir: %v", err)
	}

	origXDG := os.Getenv("XDG_CONFIG_HOME")
	origOverride := quadletRootOverride
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	quadletRootOverride = filepath.Join(root, "sysroot")
	t.Cleanup(func() {
		os.Setenv("XDG_CONFIG_HOME", origXDG)
		quadletRootOverride = origOverride
	})

	return userDir, systemDir
}

func TestFindQuadletFile_UserScope(t *testing.T) {
	userDir, _ := withQuadletTestDirs(t)
	unitPath := filepath.Join(userDir, "myapp.container")
	if err := os.WriteFile(unitPath, []byte("[Container]\nImage=alpine\n"), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	path, scope, err := FindQuadletFile("myapp.service")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != unitPath {
		t.Errorf("expected path %q, got %q", unitPath, path)
	}
	if scope != QuadletScopeUser {
		t.Errorf("expected user scope, got %q", scope)
	}
}

func TestFindQuadletFile_SystemScope(t *testing.T) {
	_, systemDir := withQuadletTestDirs(t)
	unitPath := filepath.Join(systemDir, "sysapp.container")
	if err := os.WriteFile(unitPath, []byte("[Container]\nImage=alpine\n"), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	path, scope, err := FindQuadletFile("sysapp.service")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != unitPath {
		t.Errorf("expected path %q, got %q", unitPath, path)
	}
	if scope != QuadletScopeSystem {
		t.Errorf("expected system scope, got %q", scope)
	}
}

func TestMutateQuadletPorts_SystemScopeIsReadOnly(t *testing.T) {
	_, systemDir := withQuadletTestDirs(t)
	unitPath := filepath.Join(systemDir, "sysapp.container")
	if err := os.WriteFile(unitPath, []byte("[Container]\nImage=alpine\nPublishPort=8080:80\n"), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	svc := &PodmanService{runner: newFakeCommandRunner()}
	result, err := svc.MutateQuadletPorts("sysapp.service", []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected system-scope Quadlet mutation to be refused")
	}
	if !result.RequiresExternal {
		t.Errorf("expected RequiresExternal=true for a system-scope unit")
	}

	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("failed to read unit file: %v", err)
	}
	if !strings.Contains(string(data), "8080:80") {
		t.Errorf("expected the system unit file to remain untouched, got: %s", data)
	}
}

// quadletSim provides scripted systemctl/podman behavior for
// MutateQuadletPorts tests: it tracks the unit's active state and lets a
// test fail a specific systemctl verb once.
type quadletSim struct {
	active   bool
	failVerb string // "daemon-reload", "cat", "restart"
}

func (q *quadletSim) Run(name string, args ...string) (string, string, error) {
	if name == "podman" {
		return "[]", "", nil
	}
	if name != "systemctl" || len(args) < 2 {
		return "", "", nil
	}
	verb := args[1]
	if verb == q.failVerb {
		return "", "simulated failure", fmt.Errorf("simulated failure at %s", verb)
	}
	switch verb {
	case "restart":
		q.active = true
		return "", "", nil
	case "is-active":
		if q.active {
			return "active\n", "", nil
		}
		return "inactive\n", "", nil
	}
	return "", "", nil
}

func TestMutateQuadletPorts_UserScopeSuccessPreservesActiveState(t *testing.T) {
	userDir, _ := withQuadletTestDirs(t)
	unitPath := filepath.Join(userDir, "webapp.container")
	if err := os.WriteFile(unitPath, []byte("[Container]\nImage=alpine\nPublishPort=8080:80\n"), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	sim := &quadletSim{active: true}
	svc := &PodmanService{runner: sim}

	result, err := svc.MutateQuadletPorts("webapp.service", []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !result.RequiresExternal {
		t.Fatalf("expected Quadlet mutation to be safely disabled with manual guidance, got: %+v", result)
	}

	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("failed to read unit file: %v", err)
	}
	if strings.Contains(string(data), "9090") {
		t.Errorf("read-only Quadlet mutation must not alter the unit file, got: %s", data)
	}
}

func TestMutateQuadletPorts_InactiveUnitNeverForceStarted(t *testing.T) {
	userDir, _ := withQuadletTestDirs(t)
	unitPath := filepath.Join(userDir, "idleapp.container")
	if err := os.WriteFile(unitPath, []byte("[Container]\nImage=alpine\nPublishPort=8080:80\n"), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	sim := &quadletSim{active: false}
	svc := &PodmanService{runner: sim}

	result, err := svc.MutateQuadletPorts("idleapp.service", []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !result.RequiresExternal {
		t.Fatalf("expected Quadlet mutation to be safely disabled with manual guidance, got: %+v", result)
	}
	if sim.active {
		t.Errorf("expected an originally-inactive unit to remain inactive (never auto-started)")
	}
}

func TestMutateQuadletPorts_ReloadFailureRollsBack(t *testing.T) {
	userDir, _ := withQuadletTestDirs(t)
	unitPath := filepath.Join(userDir, "app1.container")
	orig := "[Container]\nImage=alpine\nPublishPort=8080:80\n"
	if err := os.WriteFile(unitPath, []byte(orig), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	sim := &quadletSim{active: true, failVerb: "daemon-reload"}
	svc := &PodmanService{runner: sim}

	result, err := svc.MutateQuadletPorts("app1.service", []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected reload failure to fail the transaction")
	}

	// The rollback itself also calls daemon-reload, and quadletSim's
	// failVerb fires on every matching call — so rollback's own reload
	// fails too, and this must be reported honestly as manual recovery
	// rather than a false RolledBack=true.
	if result.RolledBack {
		t.Fatalf("expected rollback to be reported as failed when its own daemon-reload also fails")
	}
	if !result.RequiresExternal || result.ManualRecoveryRequired {
		t.Errorf("expected preflight refusal without a rollback attempt, got: %+v", result)
	}
}

func TestMutateQuadletPorts_RestartFailureRollsBackSuccessfully(t *testing.T) {
	userDir, _ := withQuadletTestDirs(t)
	unitPath := filepath.Join(userDir, "app2.container")
	orig := "[Container]\nImage=alpine\nPublishPort=8080:80\n"
	if err := os.WriteFile(unitPath, []byte(orig), 0o644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	sim := &quadletSim{active: true, failVerb: "restart"}
	svc := &PodmanService{runner: sim}

	result, err := svc.MutateQuadletPorts("app2.service", []PortMapping{{HostIP: "127.0.0.1", HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected restart failure to fail the transaction")
	}

	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("failed to read unit file: %v", err)
	}
	if !strings.Contains(string(data), "8080:80") {
		t.Errorf("expected original unit file content restored, got: %s", data)
	}
}

func TestMutateQuadletPorts_PreservesFilePermissions(t *testing.T) {
	userDir, _ := withQuadletTestDirs(t)
	unitPath := filepath.Join(userDir, "perms.container")
	if err := os.WriteFile(unitPath, []byte("[Container]\nImage=alpine\nPublishPort=8080:80\n"), 0o640); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	sim := &quadletSim{active: true}
	svc := &PodmanService{runner: sim}
	if _, err := svc.MutateQuadletPorts("perms.service", []PortMapping{{HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("failed to stat unit file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("expected preserved 0640 permissions, got %o", perm)
	}
}

func TestParseQuadletContent(t *testing.T) {
	content := `
[Unit]
Description=Caddy Web Server
After=network-online.target

[Container]
Image=docker.io/library/caddy:alpine
PublishPort=80:80
PublishPort=127.0.0.1:443:443/tcp
PublishPort=5353:5353/udp

[Service]
Restart=always
`

	ports := ParseQuadletContent(content)
	if len(ports) != 3 {
		t.Fatalf("expected 3 port mappings, got %d", len(ports))
	}

	// "PublishPort=80:80" names no host address at all, so it must parse to
	// HostIP=="" (unspecified/default Podman bind) rather than being
	// defaulted to "0.0.0.0", which is a distinct, explicit declaration.
	p1 := ports[0]
	if p1.HostPort != 80 || p1.ContainerPort != 80 || p1.HostIP != "" || p1.Protocol != "tcp" {
		t.Errorf("unexpected port 1: %+v", p1)
	}

	p2 := ports[1]
	if p2.HostPort != 443 || p2.ContainerPort != 443 || p2.HostIP != "127.0.0.1" || p2.Protocol != "tcp" {
		t.Errorf("unexpected port 2: %+v", p2)
	}

	p3 := ports[2]
	if p3.HostPort != 5353 || p3.ContainerPort != 5353 || p3.Protocol != "udp" {
		t.Errorf("unexpected port 3: %+v", p3)
	}
}

func TestUpdateQuadletContent(t *testing.T) {
	orig := `
[Unit]
Description=Vaultwarden Password Manager

[Container]
Image=docker.io/vaultwarden/server:latest
PublishPort=8080:80/tcp
Environment=WEBSOCKET_ENABLED=true

[Service]
Restart=always
`

	newPorts := []PortMapping{
		{
			HostIP:        "127.0.0.1",
			HostPort:      9090,
			ContainerPort: 80,
			Protocol:      "tcp",
		},
		{
			HostIP:        "127.0.0.1",
			HostPort:      3012,
			ContainerPort: 3012,
			Protocol:      "tcp",
		},
	}

	updated := UpdateQuadletContent(orig, newPorts)

	if strings.Contains(updated, "8080:80") {
		t.Errorf("expected old port 8080 to be removed")
	}
	if !strings.Contains(updated, "PublishPort=127.0.0.1:9090:80/tcp") {
		t.Errorf("expected new port 9090 to be present in updated content: %s", updated)
	}
	if !strings.Contains(updated, "PublishPort=127.0.0.1:3012:3012/tcp") {
		t.Errorf("expected new port 3012 to be present in updated content: %s", updated)
	}
	if !strings.Contains(updated, "Environment=WEBSOCKET_ENABLED=true") {
		t.Errorf("expected other container options to be preserved")
	}
	if !strings.Contains(updated, "[Service]") {
		t.Errorf("expected other sections to be preserved")
	}
}

func TestQuadletSearchDirsIncludeCurrentRootlessPathsInOrder(t *testing.T) {
	root := t.TempDir()
	oldRuntime, oldConfig, oldHome := os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("HOME")
	oldOverride := quadletRootOverride
	defer func() {
		os.Setenv("XDG_RUNTIME_DIR", oldRuntime)
		os.Setenv("XDG_CONFIG_HOME", oldConfig)
		os.Setenv("HOME", oldHome)
		quadletRootOverride = oldOverride
	}()
	os.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	os.Setenv("HOME", filepath.Join(root, "home"))
	quadletRootOverride = filepath.Join(root, "sysroot")

	dirs := getQuadletSearchDirs()
	uid := fmt.Sprintf("%d", os.Getuid())
	wants := []string{
		filepath.Join(root, "runtime", "containers", "systemd"),
		filepath.Join(root, "config", "containers", "systemd"),
		filepath.Join(root, "sysroot", "etc", "containers", "systemd", "users", uid),
		filepath.Join(root, "sysroot", "etc", "containers", "systemd", "users"),
		filepath.Join(root, "sysroot", "usr", "share", "containers", "systemd", "users", uid),
		filepath.Join(root, "sysroot", "usr", "share", "containers", "systemd", "users"),
	}
	if len(dirs) < len(wants) {
		t.Fatalf("missing rootless search paths: %+v", dirs)
	}
	for i, want := range wants {
		if dirs[i].path != want || dirs[i].scope != QuadletScopeUser {
			t.Fatalf("rootless precedence mismatch at %d: got %+v want %s", i, dirs[i], want)
		}
	}
}

func TestFindQuadletFileRecursesWithinUserSearchPath(t *testing.T) {
	userDir, _ := withQuadletTestDirs(t)
	nested := filepath.Join(userDir, "team", "apps")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(nested, "nested.container")
	if err := os.WriteFile(unitPath, []byte("[Container]\nImage=alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, scope, err := FindQuadletFile("nested.service")
	if err != nil || path != unitPath || scope != QuadletScopeUser {
		t.Fatalf("recursive Quadlet discovery failed: path=%q scope=%q err=%v", path, scope, err)
	}
}

func TestInspectQuadletReportsDropInsAndServiceNameOverride(t *testing.T) {
	userDir, _ := withQuadletTestDirs(t)
	unitPath := filepath.Join(userDir, "web.container")
	content := "[Container]\nImage=alpine\nServiceName=custom-web\nPublishPort=8080:80\n"
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	dropInDir := unitPath + ".d"
	if err := os.MkdirAll(dropInDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropInDir, "20-ports.conf"), []byte("[Container]\nPublishPort=9090:80\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	details, err := (&PodmanService{}).InspectQuadlet("web.service")
	if err != nil {
		t.Fatal(err)
	}
	if details.ServiceName != "custom-web.service" || !details.HasDropIns {
		t.Fatalf("Quadlet effective identity/ambiguity not exposed: %+v", details)
	}
	result, err := (&PodmanService{}).MutateQuadletPorts("web.service", []PortMapping{{HostPort: 9090, ContainerPort: 80, Protocol: "tcp"}})
	if err != nil || result.Success || !result.RequiresExternal {
		t.Fatalf("drop-in-bearing Quadlet must remain read-only: result=%+v err=%v", result, err)
	}
}
