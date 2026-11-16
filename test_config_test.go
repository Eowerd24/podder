package main

import (
	"os"
	"path/filepath"
	"testing"
)

// setTestConfigHome isolates every Podder settings/spec write even when the
// surrounding environment defines XDG_CONFIG_HOME (as GitHub-hosted runners
// do). os.UserConfigDir prefers XDG_CONFIG_HOME over HOME on Unix.
func setTestConfigHome(t *testing.T, root string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	runtime := filepath.Join(root, "runtime")
	if err := os.MkdirAll(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
}
