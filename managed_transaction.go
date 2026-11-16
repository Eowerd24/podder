package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

var managedTransactionMu sync.Mutex

// Serialize managed transactions across services and processes sharing this
// configuration store. External engine users are still free to race us, so
// every engine mutation must also use an exact observed container ID.
func lockManagedTransactionFile() (*os.File, func(), error) {
	if !managedTransactionMu.TryLock() {
		return nil, nil, fmt.Errorf("another managed transaction is active")
	}
	root, err := privateRoot(getConfigDir())
	if err != nil {
		managedTransactionMu.Unlock()
		return nil, nil, err
	}
	f, err := root.OpenFile("managed-transaction.lock", os.O_CREATE|os.O_RDWR, 0600)
	root.Close()
	if err != nil {
		managedTransactionMu.Unlock()
		return nil, nil, err
	}
	if err = lockSessionFile(f); err != nil {
		f.Close()
		managedTransactionMu.Unlock()
		return nil, nil, fmt.Errorf("another managed transaction is active or locking is unavailable: %w", err)
	}
	return f, func() { f.Close(); managedTransactionMu.Unlock() }, nil
}

func lockManagedTransaction() (func(), error) {
	_, release, err := lockManagedTransactionFile()
	return release, err
}
func (p *PodmanService) managedOperation() (*PodmanService, func(), error) {
	f, release, err := lockManagedTransactionFile()
	if err != nil {
		return nil, nil, err
	}
	pending, err := p.ListManagedRecoveries()
	if err != nil {
		release()
		return nil, nil, err
	}
	if len(pending) > 0 {
		release()
		return nil, nil, fmt.Errorf("unfinished managed transactions: review Managed recoveries before another managed action")
	}
	copy := *p
	copy.managedLock = f
	copy.transaction = nil
	return &copy, release, nil
}

type transactionWitness struct {
	Original        Container
	Candidate       ContainerSpec
	OriginalInspect string
}

// exactTransactionContainer never resolves names or prefixes. Ownership labels
// and image identity are re-observed before destructive operations.
func (p *PodmanService) exactTransactionContainer(id, name string, expected *Container, spec *ContainerSpec) error {
	if id == "" {
		return fmt.Errorf("no trustworthy container ID; retain resources for explicit recovery")
	}
	cs, err := p.ListContainers(true)
	if err != nil {
		return err
	}
	c := findContainerByExactID(cs, id)
	if c == nil {
		return fmt.Errorf("exact container %s is absent; retain resources for recovery", id)
	}
	if name != "" && (len(c.Names) == 0 || c.Names[0] != name && c.Names[0] != "/"+name) {
		return fmt.Errorf("container name changed; retain %s for recovery", id)
	}
	if expected != nil && (c.ImageID != expected.ImageID || !sameContainerLabels(c.Labels, expected.Labels)) {
		return fmt.Errorf("container identity/ownership changed; retain %s", id)
	}
	if spec != nil && (c.ImageID != spec.ResolvedImage || !containerMatchesSpecLabels(c, *spec)) {
		return fmt.Errorf("candidate ownership/image does not match; retain %s", id)
	}
	return nil
}

func sameContainerLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, exists := b[k]; !exists || other != v {
			return false
		}
	}
	return true
}

// A nonzero rename result does not prove that rename had no effect. Inspect
// only the captured full ID, and restore it if the backup rename took effect.
func (p *PodmanService) reconcileFailedRename(original *Container, backup, lifecycle string, witness transactionWitness) (*RollbackResult, string) {
	cs, err := p.ListContainers(true)
	if err != nil {
		return &RollbackResult{Errors: []string{err.Error()}}, "Rename result is uncertain; observation failed. Recovery journal retained."
	}
	current := findContainerByExactID(cs, original.Id)
	if current == nil {
		return &RollbackResult{Errors: []string{"original full ID not observed"}}, fmt.Sprintf("Rename result is uncertain; original ID %s was not observed. Backup name %s; recovery journal retained.", original.Id, backup)
	}
	if !sameContainerLabels(current.Labels, original.Labels) || !sameImageIdentity(current.ImageID, original.ImageID) {
		return &RollbackResult{Errors: []string{"original ownership changed"}}, "Original ownership changed; resources retained for recovery."
	}
	originalName := "unnamed"
	if len(original.Names) > 0 {
		originalName = strings.TrimPrefix(original.Names[0], "/")
	}
	kind, _ := classifyLifecycle(current.State)
	if containerHasName(current, originalName) && kind == lifecycle {
		if witness.OriginalInspect != "" {
			if err := p.verifyAcceptedAdoptionOriginal(original.Id, witness.OriginalInspect); err != nil {
				return &RollbackResult{RestoredName: true, RestoredLifecycle: true, RemovedCandidate: true, Errors: []string{err.Error()}}, "The exact original returned to its name and lifecycle, but its accepted controls changed; recovery journal retained."
			}
		}
		return &RollbackResult{RestoredName: true, RestoredLifecycle: true, RemovedCandidate: true, Verified: true}, "Observed the exact original ID under its original name and lifecycle after the failed rename result."
	}
	if containerHasName(current, backup) {
		rb := p.executeRollback(backup, originalName, "", original.Id, lifecycle, false, witness)
		if rb.Verified {
			return rb, "Rename executed despite its failed result; the exact original ID was restored and verified."
		}
		return rb, fmt.Sprintf("Rename executed despite its failed result; recovery is incomplete for original ID %s (backup %s): %s", original.Id, backup, strings.Join(rb.Errors, "; "))
	}
	return &RollbackResult{Errors: []string{"original has an unexpected name or lifecycle"}}, fmt.Sprintf("Original ID %s now has names %v and state %s; recovery journal retained (expected backup %s).", original.Id, current.Names, current.State, backup)
}
