package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func launcherFile(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:]) + ".json"
}
func openLauncherRoot() (*os.Root, error) {
	return privateRoot(filepath.Join(getConfigDir(), "launchers"))
}
func readLauncher(root *os.Root, key string) (*LauncherDefinition, error) {
	info, err := root.Stat(launcherFile(key))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, fmt.Errorf("launcher metadata exceeds size limit")
	}
	data, err := root.ReadFile(launcherFile(key))
	if err != nil {
		return nil, err
	}
	var d LauncherDefinition
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Association.Key != key || d.SchemaVersion != launcherSchemaVersion {
		return nil, fmt.Errorf("launcher identity or schema mismatch")
	}
	return &d, nil
}
func writeLauncher(d LauncherDefinition) error {
	root, err := openLauncherRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return fmt.Errorf("launcher metadata exceeds size limit")
	}
	// A random exclusive temporary file, then an atomic rename inside the root.
	temp := ".launcher-" + rand.Text()
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(temp, launcherFile(d.Association.Key)); err != nil {
		return err
	}
	return syncRootDirectory(root)
}
