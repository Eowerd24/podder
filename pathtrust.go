package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file centralizes filesystem-path trust hardening for provenance
// metadata (Compose/Quadlet container labels) that Podder uses only as
// DISCOVERY HINTS, never as filesystem authorization. A container's labels
// can be set by anything that created it — including a malicious or
// malformed image — so a label claiming "my compose file lives at X" or "my
// systemd unit is named Y" must be validated and contained before Podder
// treats it as a real path to open.

// ErrPathOutsideRoot is returned by resolveWithinRoot when a candidate path
// does not stay within its allowed root, either syntactically (after
// joining and cleaning) or, for a path that actually exists, physically
// (a symlink component resolves it outside the root).
var ErrPathOutsideRoot = errors.New("path resolves outside its allowed root")

// resolveWithinRoot resolves candidate (absolute, or relative to root) to a
// cleaned absolute path and proves it is contained within root both
// syntactically (filepath.Clean + prefix check) and, for a path that
// actually exists, physically (filepath.EvalSymlinks on both the candidate
// and the root, so an in-tree symlink — or a symlinked root itself — cannot
// be used to defeat the containment check).
//
// A nonexistent candidate passes the syntactic check (some callers use this
// purely to decide whether a candidate is even worth os.Stat-ing) but is
// never proven symlink-safe; callers that go on to actually read file
// contents must still confirm existence before trusting the result for I/O.
func resolveWithinRoot(root, candidate string) (string, error) {
	cleanRoot := filepath.Clean(root)

	var full string
	if filepath.IsAbs(candidate) {
		full = filepath.Clean(candidate)
	} else {
		full = filepath.Clean(filepath.Join(cleanRoot, candidate))
	}

	if full != cleanRoot && !strings.HasPrefix(full, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s is not contained within %s", ErrPathOutsideRoot, full, cleanRoot)
	}

	if _, err := os.Lstat(full); err == nil {
		resolvedFull, err := filepath.EvalSymlinks(full)
		if err != nil {
			return "", fmt.Errorf("cannot resolve symlinks for %s: %w", full, err)
		}
		resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
		if err != nil {
			return "", fmt.Errorf("cannot resolve symlinks for root %s: %w", cleanRoot, err)
		}
		resolvedFull = filepath.Clean(resolvedFull)
		resolvedRoot = filepath.Clean(resolvedRoot)
		if resolvedFull != resolvedRoot && !strings.HasPrefix(resolvedFull, resolvedRoot+string(filepath.Separator)) {
			return "", fmt.Errorf("%w: %s resolves (via symlink) outside %s", ErrPathOutsideRoot, full, cleanRoot)
		}
	}

	return full, nil
}

// validateQuadletUnitIdentifier rejects a Quadlet unit identifier that
// cannot be trusted as a plain file basename to search for. Container
// labels such as PODMAN_SYSTEMD_UNIT / io.systemd.unit are external
// provenance metadata, not filesystem authorization — a malicious or
// malformed container must not be able to abuse them to make Podder read an
// arbitrary accessible file via path traversal (e.g. "../../etc/passwd").
func validateQuadletUnitIdentifier(unitName string) error {
	if unitName == "" {
		return fmt.Errorf("quadlet unit identifier is empty")
	}
	if strings.ContainsRune(unitName, 0) {
		return fmt.Errorf("quadlet unit identifier contains a NUL byte")
	}
	if strings.ContainsAny(unitName, "/\\") {
		return fmt.Errorf("quadlet unit identifier %q must not contain a path separator", unitName)
	}
	if unitName == "." || unitName == ".." || strings.Contains(unitName, "..") {
		return fmt.Errorf("quadlet unit identifier %q must not contain '..'", unitName)
	}
	if filepath.IsAbs(unitName) {
		return fmt.Errorf("quadlet unit identifier %q must not be an absolute path", unitName)
	}
	// Conservative systemd-unit-basename charset: alphanumerics plus the
	// punctuation systemd itself allows/generates in practice (dash,
	// underscore, dot for the extension, '@' for template instances, ':'
	// for a small number of escaped-name cases). Anything else is refused
	// rather than guessed at.
	for _, r := range unitName {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '@' || r == ':':
		default:
			return fmt.Errorf("quadlet unit identifier %q contains disallowed character %q", unitName, string(r))
		}
	}
	return nil
}
