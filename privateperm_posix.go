//go:build !windows

package main

import (
	"fmt"
	"os"
)

func preparePrivateRootPermissions(file *os.File, path string) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("storage root must be a private directory: %s", path)
	}
	return nil
}

func securePrivateDirectoryPermissions(file *os.File, path string) error {
	if err := file.Chmod(0o700); err != nil {
		return err
	}
	return nil
}
