//go:build integration && linux

package main

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// This test deliberately bypasses the native window: UI control flow is covered
// separately. BrowserManager is the concrete Wails dispatcher used by openWeb.
// The browser is a fresh headless Firefox profile, reached through real xdg-open.
func TestApprovedNativeWebDispatch(t *testing.T) {
	if os.Getenv("PODDER_NATIVE_WEB_APPROVED") != "1" {
		t.Skip("requires approval for a new isolated Podman store and browser profile")
	}
	for _, executable := range []string{"podman", "xdg-open", "firefox-esr", "go", "timeout"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Fatal(err)
		}
	}
	p := approvedRoadmapService(t)
	base := filepath.Dir(os.Getenv("XDG_CONFIG_HOME"))
	for _, key := range []string{"CONTAINER_HOST", "CONTAINER_CONNECTION", "DBUS_SESSION_BUS_ADDRESS"} {
		t.Setenv(key, "")
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	if err := os.MkdirAll(filepath.Join(base, "config", "containers"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "config", "containers", "containers.conf"), []byte("[engine]\ncgroup_manager=\"cgroupfs\"\nevents_logger=\"file\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// No registry access or upstream image code: import a static local Go fixture.
	binary := filepath.Join(base, "web-fixture")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./scripts/fixtures/native-web")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v: %s", err, output)
	}
	archive := filepath.Join(base, "rootfs.tar")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	input, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	info, err := input.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "web", Mode: 0755, Size: info.Size()}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(tw, input); err != nil {
		t.Fatal(err)
	}
	input.Close()
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	image, _, err := p.runCommand("import", "--change", `ENTRYPOINT ["/web"]`, archive)
	if err != nil {
		t.Fatal(err)
	}
	image = strings.TrimSpace(image)
	port, err := p.FindFreePort(49160, "tcp", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	id, stderr, err := p.runCommand("run", "-d", "--label", "io.podder.acceptance=native-web", "--publish", fmt.Sprintf("127.0.0.1:%d:8080/tcp", port), image)
	if err != nil {
		t.Fatalf("fixture launch: %v: %s", err, stderr)
	}
	id = strings.TrimSpace(id)
	t.Cleanup(func() {
		label, _, err := p.runCommand("inspect", "--format", `{{index .Config.Labels "io.podder.acceptance"}}`, id)
		if err != nil || strings.TrimSpace(label) != "native-web" {
			t.Errorf("fixture cleanup blocked: identity %v %q", err, label)
			return
		}
		if _, stderr, err := p.runCommand("rm", "-f", id); err != nil {
			t.Errorf("fixture cleanup: %v %s", err, stderr)
		}
	})
	options, err := p.GetLauncherOptions(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Mappings) != 1 {
		t.Fatalf("mappings: %+v", options.Mappings)
	}
	definition := LauncherDefinition{SchemaVersion: 1, Kind: "web", Association: options.Association, OpenAfterStart: true, PreferredEndpointID: "slow"}
	for _, path := range []string{"/first", "/slow", "/unready"} {
		definition.Endpoints = append(definition.Endpoints, WebEndpoint{ID: path[1:], Scheme: "http", Mapping: options.Mappings[0], Path: path})
	}
	if err := p.SaveLauncherDefinition(id, definition); err != nil {
		t.Fatal(err)
	}
	// Keep default-browser selection private to this test. Firefox exits after
	// capturing the page; it never connects to the person's running browser.
	browserScript := filepath.Join(base, "browser")
	script := "#!/bin/sh\nset -eu\nprofile=$(mktemp -d \"$PODDER_NATIVE_BROWSER_DIR/profile-XXXXXX\")\ntimeout 22s /usr/bin/firefox-esr --headless --no-remote --profile \"$profile\" --screenshot \"$PODDER_NATIVE_SCREENSHOT\" --window-size=900,500 \"$1\" >>\"$PODDER_NATIVE_BROWSER_DIR/browser.log\" 2>&1\nprintf done > \"$PODDER_NATIVE_SCREENSHOT.done\"\n"
	if err := os.WriteFile(browserScript, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", browserScript)
	t.Setenv("DE", "generic")
	t.Setenv("XDG_CURRENT_DESKTOP", "")
	for _, key := range []string{"KDE_FULL_SESSION", "GNOME_DESKTOP_SESSION_ID", "DESKTOP_SESSION", "XDG_SESSION_DESKTOP"} {
		t.Setenv(key, "")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("PODDER_NATIVE_BROWSER_DIR", base)
	native := &application.BrowserManager{}
	p.browserOpen = native.OpenURL
	waitPage := func(path, screenshot string) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			logs, logerr, _ := p.runCommand("logs", id)
			logs += logerr
			if strings.Contains(logs, "GET "+path+" 200 Mozilla/") && strings.Contains(logs, "Firefox/") {
				_, done := os.Stat(screenshot + ".done")
				if file, err := os.Stat(screenshot); err == nil && file.Size() > 100 && done == nil {
					t.Logf("browser rendered %s; screenshot %s", path, screenshot)
					return
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		data, _ := os.ReadFile(filepath.Join(base, "browser.log"))
		t.Fatalf("browser did not render %s: %s", path, data)
	}
	urlFor := func(path string) string { return fmt.Sprintf("http://127.0.0.1:%d%s", port, path) }
	deadline := time.Now().Add(10 * time.Second)
	for probeWeb(context.Background(), urlFor("/first")) != nil {
		if time.Now().After(deadline) {
			t.Fatal("fixture not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	shot := filepath.Join(base, "open.png")
	t.Setenv("PODDER_NATIVE_SCREENSHOT", shot)
	result, err := p.OpenWebEndpoint(id, "first")
	if err != nil || !result.Opened || result.URL != urlFor("/first") {
		t.Fatalf("Open Web: %+v %v", result, err)
	}
	waitPage("/first", shot)
	copyURL, err := p.GetWebEndpointURL(id, "slow")
	if err != nil || copyURL != urlFor("/slow") {
		t.Fatalf("copy URL: %s %v", copyURL, err)
	}
	if _, _, err := p.runCommand("stop", "--time", "2", id); err != nil {
		t.Fatal(err)
	}
	shot = filepath.Join(base, "start-open.png")
	t.Setenv("PODDER_NATIVE_SCREENSHOT", shot)
	start := time.Now()
	result, err = p.StartAndOpenWebEndpoint(id, "slow")
	if err != nil || !result.Opened || result.URL != urlFor("/slow") {
		t.Fatalf("Start & Open: %+v %v", result, err)
	}
	if time.Since(start) < time.Second {
		t.Fatal("opened before delayed readiness")
	}
	waitPage("/slow", shot)
	logs, logerr, err := p.runCommand("logs", id)
	if err != nil || !strings.Contains(logs+logerr, "HEAD /slow 503") || !strings.Contains(logs+logerr, "HEAD /slow 200") {
		t.Fatalf("missing delayed-readiness transition: %v %s%s", err, logs, logerr)
	}
	// Force an actual Wails/exec dispatch failure without replacing xdg-open.
	p.browserOpen = func(target string) error {
		previous := os.Getenv("PATH")
		os.Setenv("PATH", filepath.Join(base, "no-opener"))
		defer os.Setenv("PATH", previous)
		return native.OpenURL(target)
	}
	result, err = p.OpenWebEndpoint(id, "first")
	if err != nil || result.Opened || result.URL != urlFor("/first") || !strings.Contains(result.Message, "Copy") {
		t.Fatalf("dispatch failure fallback: %+v %v", result, err)
	}
	p.browserOpen = native.OpenURL
	result, err = p.OpenWebEndpoint(id, "unready")
	if err != nil || result.Opened || result.URL != urlFor("/unready") {
		t.Fatalf("readiness fallback: %+v %v", result, err)
	}
	if _, _, err := p.runCommand("stop", "--time", "2", id); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	result, err = p.StartAndOpenWebEndpoint(id, "unready")
	if err != nil || result.Opened || result.URL != urlFor("/unready") || !strings.Contains(result.Message, "15 seconds") {
		t.Fatalf("bounded readiness: %+v %v", result, err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Second || elapsed > 22*time.Second {
		t.Fatalf("readiness elapsed %v", elapsed)
	}
	final, err := p.GetLauncherOptions(id)
	if err != nil || !final.Running || final.Association != options.Association || !reflect.DeepEqual(final.Mappings, options.Mappings) {
		t.Fatalf("fixture identity/mappings changed: %+v %v", final, err)
	}
	t.Logf("PASS real rootless fixture, Wails→xdg-open→Firefox, exact endpoints, delayed start, copy/failure/timeout paths; retained evidence %s", base)
}
