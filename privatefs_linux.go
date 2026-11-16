//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func descriptorMountHandoffAvailable() bool { return true }

func containerMountedFilesystemObjectIdentity(pid int, destination string) (string, error) {
	if pid <= 0 || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return "", fmt.Errorf("running container mount identity is unavailable")
	}
	fd, err := unix.Open(fmt.Sprintf("/proc/%d/root", pid), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	clean := strings.TrimPrefix(destination, string(filepath.Separator))
	if clean == "" {
		file := os.NewFile(uintptr(fd), destination)
		defer file.Close()
		return openFileIdentity(file)
	}
	components := strings.Split(clean, string(filepath.Separator))
	for i, component := range components {
		flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i != len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(fd, component, flags, 0)
		unix.Close(fd)
		if openErr != nil {
			return "", openErr
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), destination)
	defer file.Close()
	return openFileIdentity(file)
}

func secureOpenFilesystemObject(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("filesystem object path must be absolute and clean: %s", path)
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for i, component := range components {
		flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i != len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(fd, component, flags, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("filesystem object path contains a symlink, non-directory ancestor, or inaccessible component %q: %w", component, openErr)
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR && st.Mode&unix.S_IFMT != unix.S_IFREG {
		file.Close()
		return nil, fmt.Errorf("filesystem object must be a regular file or directory")
	}
	return file, nil
}

// secureOpenDirectory walks from an already-open root and uses O_NOFOLLOW for
// every component. A returned descriptor therefore names the exact directory
// object even if an ancestor is renamed or replaced afterward.
func secureOpenDirectory(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("directory path must be absolute and clean: %s", path)
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	clean := strings.TrimPrefix(path, string(filepath.Separator))
	if clean == "" {
		return os.NewFile(uintptr(fd), path), nil
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr == unix.ENOENT && create {
			if mkdirErr := unix.Mkdirat(fd, component, 0o700); mkdirErr != nil && mkdirErr != unix.EEXIST {
				unix.Close(fd)
				return nil, mkdirErr
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("directory path contains a symlink, non-directory, or inaccessible component %q: %w", component, openErr)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openRootDirectoryNoFollow(root *os.Root, relative string) (*os.File, error) {
	base, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	fd := int(base.Fd())
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		base.Close()
		return nil, fmt.Errorf("invalid root-relative directory path")
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		base.Close()
		if openErr != nil {
			return nil, fmt.Errorf("generated directory path contains a symlink, non-directory, or inaccessible component %q: %w", component, openErr)
		}
		base = os.NewFile(uintptr(next), component)
		fd = next
	}
	return base, nil
}

func openFileIdentity(f *os.File) (string, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return "", err
	}
	if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFDIR && kind != unix.S_IFREG {
		return "", fmt.Errorf("filesystem object is not a regular file or directory")
	}
	return strconv.FormatUint(uint64(st.Dev), 16) + ":" + strconv.FormatUint(st.Ino, 16), nil
}

func pinnedMountPath(f *os.File) (string, error) {
	if f == nil {
		return "", fmt.Errorf("missing pinned mount descriptor")
	}
	return fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), f.Fd()), nil
}
