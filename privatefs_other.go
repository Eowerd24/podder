//go:build !linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func descriptorMountHandoffAvailable() bool { return false }

func containerMountedFilesystemObjectIdentity(pid int, destination string) (string, error) {
	return "", fmt.Errorf("container mount identity verification is unavailable on this platform")
}

func secureOpenFilesystemObject(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("filesystem object path must be absolute and clean: %s", path)
	}
	dir, name := filepath.Split(path)
	parent, err := secureOpenDirectory(filepath.Clean(dir), false)
	if err != nil {
		return nil, err
	}
	parentRoot, err := os.OpenRoot(parent.Name())
	parent.Close()
	if err != nil {
		return nil, err
	}
	defer parentRoot.Close()
	before, err := parentRoot.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() && !before.Mode().IsRegular() {
		return nil, fmt.Errorf("filesystem object must be a non-symlink regular file or directory")
	}
	opened, err := parentRoot.Open(name)
	if err != nil {
		return nil, err
	}
	after, err := opened.Stat()
	if err != nil || !os.SameFile(before, after) {
		opened.Close()
		return nil, fmt.Errorf("filesystem object changed while it was being opened")
	}
	return opened, nil
}

func secureOpenDirectory(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("directory path must be absolute and clean: %s", path)
	}
	volume := filepath.VolumeName(path)
	rootPath := volume + string(filepath.Separator)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(path, rootPath)
	openedRoot, err := traverseRootDirectories(root, relative, create)
	root.Close()
	if err != nil {
		return nil, err
	}
	defer openedRoot.Close()
	return openedRoot.Open(".")
}

func openRootDirectoryNoFollow(root *os.Root, relative string) (*os.File, error) {
	start, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	openedRoot, err := traverseRootDirectories(start, relative, false)
	start.Close()
	if err != nil {
		return nil, err
	}
	defer openedRoot.Close()
	return openedRoot.Open(".")
}

// os.Root supplies the handle-relative containment on non-Linux platforms.
// Each component is lstat'd, opened as a child Root, and compared by file
// identity so a reparse-point/symlink or replacement race fails closed.
func traverseRootDirectories(start *os.Root, relative string, create bool) (*os.Root, error) {
	current, err := start.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == "" {
		return current, nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		current.Close()
		return nil, fmt.Errorf("invalid root-relative directory path")
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		info, statErr := current.Lstat(component)
		if os.IsNotExist(statErr) && create {
			if mkdirErr := current.Mkdir(component, 0o700); mkdirErr != nil && !os.IsExist(mkdirErr) {
				current.Close()
				return nil, mkdirErr
			}
			info, statErr = current.Lstat(component)
		}
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, fmt.Errorf("directory path contains a symlink, reparse point, non-directory, or inaccessible component %q", component)
		}
		next, openErr := current.OpenRoot(component)
		if openErr != nil {
			current.Close()
			return nil, openErr
		}
		opened, openErr := next.Open(".")
		if openErr != nil {
			next.Close()
			current.Close()
			return nil, openErr
		}
		openedInfo, openedErr := opened.Stat()
		opened.Close()
		if openedErr != nil || !os.SameFile(info, openedInfo) {
			next.Close()
			current.Close()
			return nil, fmt.Errorf("directory component changed while it was being opened: %q", component)
		}
		current.Close()
		current = next
	}
	return current, nil
}

func openFileIdentity(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d:%d", info.Name(), info.Size(), info.ModTime().UnixNano()), nil
}

func pinnedMountPath(f *os.File) (string, error) {
	return "", fmt.Errorf("descriptor-backed Podman bind mounts require Linux")
}
