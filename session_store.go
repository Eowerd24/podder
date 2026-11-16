package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func sessionDataDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "podder")
}
func sessionRuntimeDir() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if filepath.IsAbs(base) {
		if info, err := os.Stat(base); err == nil && info.IsDir() && info.Mode().Perm()&0077 == 0 {
			return filepath.Join(base, "podder", "sessions")
		}
	}
	sum := sha256.Sum256([]byte(getConfigDir()))
	return filepath.Join(os.TempDir(), "podder-sessions-"+hex.EncodeToString(sum[:8]))
}
func manifestRoot() (*os.Root, error) {
	return privateRoot(filepath.Join(sessionDataDir(), "session-state"))
}
func recipeRoot() (*os.Root, error) { return privateRoot(filepath.Join(getConfigDir(), "sessions")) }

var privateJSONDirectorySync = syncRootDirectory

func writeRootJSON(root *os.Root, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 1024*1024 {
		return fmt.Errorf("metadata too large")
	}
	temp := ".write-" + newSessionID()
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(temp, name); err != nil {
		return err
	}
	return privateJSONDirectorySync(root)
}
func readRootJSON(root *os.Root, name string, value any) error {
	info, err := root.Stat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return fmt.Errorf("invalid metadata file")
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
func saveSessionState(state SessionRuntimeState) error {
	if err := validateSessionSource(state.Spec); err != nil {
		return err
	}
	if !sessionIDPattern.MatchString(state.ID) || state.SchemaVersion != sessionSchema {
		return fmt.Errorf("invalid session journal")
	}
	root, err := manifestRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return writeRootJSON(root, state.ID+".json", state)
}
func loadSessionState(id string) (*SessionRuntimeState, error) {
	if !sessionIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid session ID")
	}
	root, err := manifestRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var state SessionRuntimeState
	if err := readRootJSON(root, id+".json", &state); err != nil {
		return nil, err
	}
	if state.ID != id || state.SchemaVersion != sessionSchema || !sessionIDPattern.MatchString(state.Token) {
		return nil, fmt.Errorf("session journal identity/schema mismatch")
	}
	if err := validateSessionSource(state.Spec); err != nil {
		return nil, fmt.Errorf("unsafe source metadata in session journal; manual review required")
	}
	return &state, nil
}
func saveSessionRecipe(spec DisposableSessionSpec) error {
	if err := validateSessionSource(spec); err != nil {
		return err
	}
	root, err := recipeRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return writeRootJSON(root, launcherFile(spec.Name), savedSessionSpec(spec))
}
func listSessionStates() ([]SessionRuntimeState, error) {
	root, err := manifestRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := []SessionRuntimeState{}
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if !strings.HasSuffix(e.Name(), ".json") || !sessionIDPattern.MatchString(id) {
			continue
		}
		state, err := loadSessionState(id)
		if err != nil {
			return nil, fmt.Errorf("session journal %s: %w", id, err)
		}
		out = append(out, *state)
	}
	return out, nil
}
func (p *PodmanService) ListSessionRecipes() ([]DisposableSessionSpec, error) {
	root, err := recipeRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := []DisposableSessionSpec{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var spec DisposableSessionSpec
		if err := readRootJSON(root, e.Name(), &spec); err != nil {
			return nil, err
		}
		if spec.SchemaVersion != sessionSchema || launcherFile(spec.Name) != e.Name() {
			return nil, fmt.Errorf("invalid recipe identity/schema")
		}
		if err := validateSessionSource(spec); err != nil {
			return nil, fmt.Errorf("unsafe source metadata in saved recipe; manual review required")
		}
		out = append(out, savedSessionSpec(spec))
	}
	return out, nil
}

// Only generated ID paths are eligible, and only with the matching ownership marker.
func prepareSessionWorkspace(state *SessionRuntimeState) ([]BindMountSpec, error) {
	binds := []BindMountSpec{}
	complete := false
	defer func() {
		if !complete {
			closeSessionBindSources(binds)
		}
	}()
	runtime, err := privateRoot(sessionRuntimeDir())
	if err != nil {
		return nil, err
	}
	defer runtime.Close()
	if err := runtime.Mkdir(state.ID, 0700); err != nil {
		if !os.IsExist(err) {
			return nil, err
		}
		var marker map[string]string
		if err := readRootJSON(runtime, filepath.Join(state.ID, "owner.json"), &marker); err != nil || marker["id"] != state.ID || marker["token"] != state.Token {
			return nil, fmt.Errorf("existing workspace ownership mismatch")
		}
	} else if err := writeRootJSON(runtime, filepath.Join(state.ID, "owner.json"), map[string]string{"id": state.ID, "token": state.Token}); err != nil {
		return nil, err
	}
	for _, kind := range []string{"config", "data"} {
		mount := state.Spec.Config
		if kind == "data" {
			mount = state.Spec.Data
		}
		if mount.Target == "" {
			continue
		}
		retain := state.Spec.Policy == "data" || state.Spec.Policy == "configuration" && kind == "config"
		root := runtime
		relative := filepath.Join(state.ID, kind)
		if retain {
			root, err = privateRoot(filepath.Join(sessionDataDir(), "sessions"))
			if err != nil {
				return nil, err
			}
			defer root.Close()
			relative = filepath.Join(state.Spec.Name, kind)
		}
		if err := root.MkdirAll(relative, 0700); err != nil {
			return nil, err
		}
		if kind == "config" && state.Spec.Preset == "olivetin-hello" {
			file, err := root.OpenFile(filepath.Join(relative, "config.yaml"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil && !os.IsExist(err) {
				return nil, err
			}
			if err == nil {
				_, writeErr := file.WriteString("actions:\n  - title: Say hello\n    shell: printf 'Hello from Podder\\n'\n")
				closeErr := file.Close()
				if writeErr != nil {
					return nil, writeErr
				}
				if closeErr != nil {
					return nil, closeErr
				}
			}
		}
		physical, err := resolveWithinRoot(root.Name(), relative)
		if err != nil {
			return nil, err
		}
		opened, err := openRootDirectoryNoFollow(root, relative)
		if err != nil {
			return nil, err
		}
		info, err := opened.Stat()
		if err != nil || !info.IsDir() {
			opened.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("generated mount source is not a directory")
		}
		identity, err := openFileIdentity(opened)
		if err != nil {
			opened.Close()
			return nil, err
		}
		binds = append(binds, BindMountSpec{HostPath: physical, ContainerPath: mount.Target, ReadOnly: !mount.ReadWrite, sourceIdentity: identity, sourceFile: opened, podderGenerated: true})
		if retain {
			state.Retained = append(state.Retained, physical)
		}
	}
	for _, b := range state.Spec.ExternalBinds {
		physical, err := filepath.EvalSymlinks(b.HostPath)
		if err != nil {
			return nil, err
		}
		if sensitiveMountPath(physical) {
			return nil, fmt.Errorf("external bind resolves to protected path")
		}
		state.Retained = append(state.Retained, physical+" (external, never deleted)")
	}
	complete = true
	return binds, nil
}
func (p *PodmanService) removeSessionWorkspace(state SessionRuntimeState) error {
	root, err := privateRoot(sessionRuntimeDir())
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(state.ID); os.IsNotExist(err) {
		return nil
	}
	pinned, err := openRootDirectoryNoFollow(root, state.ID)
	if err != nil {
		return fmt.Errorf("workspace ownership unverified: %w", err)
	}
	defer pinned.Close()
	expectedIdentity, err := openFileIdentity(pinned)
	if err != nil {
		return err
	}
	workspace, err := root.OpenRoot(state.ID)
	if err != nil {
		return fmt.Errorf("workspace ownership unverified: %w", err)
	}
	opened, err := workspace.Open(".")
	if err != nil {
		workspace.Close()
		return err
	}
	openedIdentity, identityErr := openFileIdentity(opened)
	opened.Close()
	if identityErr != nil || openedIdentity != expectedIdentity {
		workspace.Close()
		return fmt.Errorf("workspace changed during ownership verification")
	}
	var marker map[string]string
	if err := readRootJSON(workspace, "owner.json", &marker); err != nil {
		workspace.Close()
		return fmt.Errorf("workspace ownership unverified: %w", err)
	}
	workspace.Close()
	if marker["id"] != state.ID || marker["token"] != state.Token {
		return fmt.Errorf("workspace ownership mismatch")
	}
	if p != nil && p.sessionWorkspaceCleanupHook != nil {
		p.sessionWorkspaceCleanupHook()
	}
	quarantine := ".cleanup-" + newSessionID()
	if err := root.Rename(state.ID, quarantine); err != nil {
		return err
	}
	moved, err := openRootDirectoryNoFollow(root, quarantine)
	if err != nil {
		_ = root.Rename(quarantine, state.ID)
		return fmt.Errorf("quarantined workspace could not be verified: %w", err)
	}
	movedIdentity, identityErr := openFileIdentity(moved)
	moved.Close()
	if identityErr != nil || movedIdentity != expectedIdentity {
		if _, statErr := root.Lstat(state.ID); os.IsNotExist(statErr) {
			_ = root.Rename(quarantine, state.ID)
		}
		return fmt.Errorf("workspace object changed before cleanup; nothing was deleted")
	}
	if err := root.RemoveAll(quarantine); err != nil {
		return err
	}
	return syncRootDirectory(root)
}

func plannedSessionBinds(spec DisposableSessionSpec, id string) []BindMountSpec {
	out := []BindMountSpec{}
	for _, kind := range []string{"config", "data"} {
		mount := spec.Config
		if kind == "data" {
			mount = spec.Data
		}
		if mount.Target == "" {
			continue
		}
		base := filepath.Join(sessionRuntimeDir(), id, kind)
		if spec.Policy == "data" || spec.Policy == "configuration" && kind == "config" {
			base = filepath.Join(sessionDataDir(), "sessions", spec.Name, kind)
		}
		out = append(out, BindMountSpec{HostPath: base, ContainerPath: mount.Target, ReadOnly: !mount.ReadWrite, podderGenerated: true})
	}
	return out
}
