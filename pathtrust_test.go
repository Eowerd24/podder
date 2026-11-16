package main

import (
	"os"
	"path/filepath"
	"testing"
)

// --- v1.4 hardening: resolveWithinRoot / validateQuadletUnitIdentifier ---
// (item 5: provenance-derived filesystem paths are never trusted as
// authorization without containment + symlink-safe canonicalization)

func TestResolveWithinRoot_AcceptsPlainContainedPath(t *testing.T) {
	root := t.TempDir()
	if _, err := os.Create(filepath.Join(root, "file.txt")); err != nil {
		t.Fatal(err)
	}
	got, err := resolveWithinRoot(root, "file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(root, "file.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveWithinRoot_RejectsSyntacticEscape(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		"../escaped",
		"../../etc/passwd",
		"foo/../../bar",
	}
	for _, cand := range cases {
		if got, err := resolveWithinRoot(root, cand); err == nil {
			t.Errorf("resolveWithinRoot(%q, %q) expected error, got %q", root, cand, got)
		}
	}
}

func TestResolveWithinRoot_RejectsAbsoluteCandidateOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if _, err := resolveWithinRoot(root, filepath.Join(outside, "file.txt")); err == nil {
		t.Errorf("expected an absolute candidate outside root to be rejected")
	}
}

func TestResolveWithinRoot_AcceptsAbsoluteCandidateInsideRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "sub", "file.txt")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveWithinRoot(root, inside)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Clean(inside) {
		t.Errorf("got %q, want %q", got, inside)
	}
}

// TestResolveWithinRoot_SymlinkEscapeIsDetected proves that a symlink
// PLACED INSIDE the allowed root, but pointing OUTSIDE it, is still
// rejected -- syntactic containment alone (string-prefix checking) is not
// enough, since the symlink's target is what actually gets opened.
func TestResolveWithinRoot_SymlinkEscapeIsDetected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secretFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("do-not-read"), 0o644); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(root, "innocuous-looking.txt")
	if err := os.Symlink(secretFile, linkPath); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}

	if got, err := resolveWithinRoot(root, "innocuous-looking.txt"); err == nil {
		t.Errorf("expected a symlink escaping the root to be rejected, got %q", got)
	}
}

// TestResolveWithinRoot_SymlinkStayingWithinRootIsAccepted proves the
// symlink check isn't so aggressive that it rejects a symlink whose target
// legitimately stays inside the same root.
func TestResolveWithinRoot_SymlinkStayingWithinRootIsAccepted(t *testing.T) {
	root := t.TempDir()
	realFile := filepath.Join(root, "real.txt")
	if err := os.WriteFile(realFile, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "alias.txt")
	if err := os.Symlink(realFile, linkPath); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}

	if _, err := resolveWithinRoot(root, "alias.txt"); err != nil {
		t.Errorf("expected an in-root symlink target to be accepted, got: %v", err)
	}
}

func TestResolveWithinRoot_NonexistentCandidatePassesSyntacticCheck(t *testing.T) {
	root := t.TempDir()
	// A nonexistent candidate can't be symlink-verified, but a purely
	// syntactic containment pass is still useful for callers deciding
	// whether a path is even worth stat-ing.
	if _, err := resolveWithinRoot(root, "does-not-exist.txt"); err != nil {
		t.Errorf("expected a nonexistent-but-contained candidate to pass the syntactic check, got: %v", err)
	}
}
