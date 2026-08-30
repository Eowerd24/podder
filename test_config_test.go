package main

import (
	"path/filepath"
	"testing"
)

// setTestConfigHome isolates every Podder settings/spec write even when the
// surrounding environment defines XDG_CONFIG_HOME (as GitHub-hosted runners
// do). os.UserConfigDir prefers XDG_CONFIG_HOME over HOME on Unix.
func setTestConfigHome(t *testing.T, root string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
}
