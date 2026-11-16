//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func syncRootDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	handle, err := reopenPrivateDirectoryHandle(dir, windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		return fmt.Errorf("reopen private directory handle for durability sync: %w", err)
	}
	defer windows.CloseHandle(handle)
	return windows.FlushFileBuffers(handle)
}
