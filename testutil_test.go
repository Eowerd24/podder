package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// fakeCommandRunner is a scripted CommandRunner used across the test suite
// to exercise transaction failure points deterministically, without a real
// Podman/systemd/compose install. Register handlers per leading argument
// (podman subcommand, or systemctl/compose verb) via On/OnDefault.
type fakeCommandRunner struct {
	mu      sync.Mutex
	calls   [][]string
	handler map[string]func(name string, args []string) (string, string, error)
	// fallback is used when no handler matches; nil means succeed with
	// empty output.
	fallback func(name string, args []string) (string, string, error)
}

func newFakeCommandRunner() *fakeCommandRunner {
	r := &fakeCommandRunner{handler: make(map[string]func(name string, args []string) (string, string, error))}
	r.On("podman info", func(string, []string) (string, string, error) { return fakeManagedStoreInfo(), "", nil })
	return r
}

// On registers a handler keyed by "name verb" (e.g. "podman rename",
// "systemctl restart"), matched against the command name and its first
// argument.
func (f *fakeCommandRunner) On(key string, h func(name string, args []string) (string, string, error)) {
	f.handler[key] = h
}

func (f *fakeCommandRunner) Run(name string, args ...string) (string, string, error) {
	f.mu.Lock()
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	if name == "podman" {
		args = stripTestOptionBoundary(args)
	}

	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	if h, ok := f.handler[key]; ok {
		return h(name, args)
	}
	if h, ok := f.handler[name]; ok {
		return h(name, args)
	}
	if f.fallback != nil {
		return f.fallback(name, args)
	}
	return "", "", nil
}

// CallsMatching returns every recorded call whose joined args contain substr.
func (f *fakeCommandRunner) CallsMatching(substr string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), substr) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeCommandRunner) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// errOut is a small helper for handlers that need to fail with stderr text.
func errOut(stderr string) (string, string, error) {
	return "", stderr, fmt.Errorf("%s", stderr)
}

func fakeManagedStoreInfo() string {
	root := filepath.Join(getConfigDir(), "test-engine")
	_ = os.MkdirAll(root, 0700)
	b, _ := json.Marshal(map[string]any{"host": map[string]any{"serviceIsRemote": false, "security": map[string]bool{"rootless": true}}, "store": map[string]string{"graphRoot": root, "runRoot": root, "graphDriverName": "vfs"}})
	return string(b)
}

func stripTestOptionBoundary(args []string) []string {
	out := append([]string{}, args...)
	for i, a := range out {
		if a == "--" {
			return append(out[:i], out[i+1:]...)
		}
	}
	return out
}
