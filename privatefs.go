package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// privateRoot opens a private directory only after every path component has
// been traversed without following symlinks. The returned os.Root is then
// checked against that pinned object before the traversal descriptor is
// released, so a pathname replacement cannot redirect later operations.
func privateRoot(path string) (*os.Root, error) {
	pinned, err := secureOpenDirectory(path, true)
	if err != nil {
		return nil, err
	}
	defer pinned.Close()
	info, err := pinned.Stat()
	if err != nil {
		return nil, err
	}
	if err := preparePrivateRootPermissions(pinned, path); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(info, openedInfo) {
		root.Close()
		return nil, fmt.Errorf("storage root changed while it was being opened: %s", path)
	}
	return root, nil
}

func ensurePrivateDir(path string) error {
	pinned, err := secureOpenDirectory(path, true)
	if err != nil {
		return err
	}
	defer pinned.Close()
	if err := securePrivateDirectoryPermissions(pinned, path); err != nil {
		return err
	}
	return preparePrivateRootPermissions(pinned, path)
}

func filesystemObjectIdentity(path string) (string, error) {
	f, err := secureOpenDirectory(path, false)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return openFileIdentity(f)
}

func openPinnedDirectory(path, expectedIdentity string) (*os.File, error) {
	f, err := secureOpenDirectory(path, false)
	if err != nil {
		return nil, err
	}
	identity, err := openFileIdentity(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if expectedIdentity != "" && identity != expectedIdentity {
		f.Close()
		return nil, fmt.Errorf("filesystem object changed after approval: %s", path)
	}
	return f, nil
}

func openPinnedFilesystemObject(path, expectedIdentity string) (*os.File, error) {
	f, err := secureOpenFilesystemObject(path)
	if err != nil {
		return nil, err
	}
	identity, err := openFileIdentity(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if expectedIdentity != "" && identity != expectedIdentity {
		f.Close()
		return nil, fmt.Errorf("filesystem object changed after approval: %s", path)
	}
	return f, nil
}

func privatePathParts(path string) (dir, name string, err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", fmt.Errorf("private path must be absolute and clean")
	}
	dir, name = filepath.Dir(path), filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "", "", fmt.Errorf("invalid private file path")
	}
	return dir, name, nil
}
