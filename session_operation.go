package main

import (
	"context"
	"fmt"
	"os"
)

type sessionLockContextKey struct{}

// Lock files live outside the deletable workspace and are never unlinked: every
// process must lock the same inode, even after a temporary journal is removed.
// Close releases our reference; never LOCK_UN (a surviving child may hold it).
func acquireSessionOperation(id string) (*os.File, error) {
	if !sessionIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid session ID")
	}
	root, err := manifestRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.OpenFile(id+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockSessionFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("session operation active or lock unavailable; wait for the owning process before cleanup")
	}
	return file, nil
}
func interruptedSessionBuild(state *SessionRuntimeState) bool {
	return state.State == "building" || state.InterruptedBuild
}

// Called while holding the operation lock. A stale worker may never recreate a
// removed journal, replace another operation, or overwrite a recovery decision.
func completeSessionBuild(state SessionRuntimeState) error {
	current, err := loadSessionState(state.ID)
	if err != nil {
		return err
	}
	if current.State != "building" || current.Token != state.Token || current.BuildOperation == "" || current.BuildOperation != state.BuildOperation {
		return fmt.Errorf("stale build operation; completion discarded")
	}
	return saveSessionState(state)
}
func sessionCommandContext(ctx context.Context, file *os.File) context.Context {
	return context.WithValue(ctx, sessionLockContextKey{}, file)
}

func interruptedSessionLaunch(state *SessionRuntimeState) bool {
	return state.State == "starting" || state.InterruptedLaunch || state.LaunchOperation != ""
}

// A late launch result cannot recreate a deleted journal or overwrite recovery.
func completeSessionLaunch(state SessionRuntimeState) error {
	current, err := loadSessionState(state.ID)
	if err != nil {
		return err
	}
	if current.State != "starting" || current.Token != state.Token || current.LaunchOperation == "" || current.LaunchOperation != state.LaunchOperation {
		return fmt.Errorf("stale launch operation; completion discarded")
	}
	state.LaunchOperation = ""
	return saveSessionState(state)
}
