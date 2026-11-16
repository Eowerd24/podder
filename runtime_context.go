package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Never treat remote loopback or rootful storage as a local disposable sandbox.
// Installed Podman info documents host.serviceIsRemote and security.rootless.
func (p *PodmanService) ensureLocalContext(rootless bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _, err := p.runSessionCommand(ctx, "podman", "info", "--format", "json")
	if err != nil {
		return fmt.Errorf("cannot verify the local Podman context")
	}
	var info struct {
		Host struct {
			ServiceIsRemote *bool `json:"serviceIsRemote"`
			Security        struct {
				Rootless *bool `json:"rootless"`
			} `json:"security"`
		} `json:"host"`
	}
	if json.Unmarshal([]byte(out), &info) != nil || info.Host.ServiceIsRemote == nil || *info.Host.ServiceIsRemote {
		return fmt.Errorf("this action requires verified local Podman; use an explicit Remote profile for SSH endpoints")
	}
	if rootless && (info.Host.Security.Rootless == nil || !*info.Host.Security.Rootless) {
		return fmt.Errorf("disposable sessions require rootless Podman; rootful operation is not supported")
	}
	return nil
}

// Podman output surfaces may include or omit the sha256: prefix. Only full IDs
// are compared; prefix matching is never used for runtime/image ownership.
func canonicalImageIdentity(id string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "sha256:")
}
func sameImageIdentity(a, b string) bool {
	return canonicalImageIdentity(a) != "" && canonicalImageIdentity(a) == canonicalImageIdentity(b)
}

func exactFullImageID(id string) (string, error) {
	canonical := canonicalImageIdentity(id)
	if !fullContainerIDPattern.MatchString(canonical) {
		return "", fmt.Errorf("exact full image ID required; tags, names and ID prefixes are not accepted")
	}
	return "sha256:" + canonical, nil
}

var sessionRuntimeIdentityPattern = regexp.MustCompile(`^v1:[0-9a-f]{64}$`)

// Bind recovery to the local user's exact store directories, including directory
// identity. Recreated stores, changed runroots and legacy journals fail closed.
func (p *PodmanService) sessionRuntimeIdentity() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _, err := p.runSessionCommand(ctx, "podman", "info", "--format", "json")
	var info struct {
		Host struct {
			ServiceIsRemote *bool `json:"serviceIsRemote"`
			Security        struct {
				Rootless *bool `json:"rootless"`
			} `json:"security"`
		} `json:"host"`
		Store struct {
			GraphRoot       string `json:"graphRoot"`
			RunRoot         string `json:"runRoot"`
			GraphDriverName string `json:"graphDriverName"`
		} `json:"store"`
	}
	if err != nil || json.Unmarshal([]byte(out), &info) != nil || info.Host.ServiceIsRemote == nil || *info.Host.ServiceIsRemote || info.Host.Security.Rootless == nil || !*info.Host.Security.Rootless || info.Store.GraphDriverName == "" {
		return "", fmt.Errorf("cannot establish local rootless runtime/store identity")
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "", fmt.Errorf("cannot establish runtime host identity")
	}
	parts := []string{"podder-store-v1", host, strconv.Itoa(os.Geteuid()), info.Store.GraphDriverName}
	for _, path := range []string{info.Store.GraphRoot, info.Store.RunRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", fmt.Errorf("invalid runtime store directory identity")
		}
		physical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("cannot resolve runtime store identity")
		}
		id, err := sessionDirectoryIdentity(physical)
		if err != nil {
			return "", fmt.Errorf("cannot establish runtime store directory identity")
		}
		parts = append(parts, physical, id)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "v1:" + hex.EncodeToString(sum[:]), nil
}
func (p *PodmanService) verifySessionRuntime(state *SessionRuntimeState) error {
	if !sessionRuntimeIdentityPattern.MatchString(state.RuntimeIdentity) {
		return fmt.Errorf("missing or malformed original runtime/store identity; legacy journal requires manual recovery, automatic mutation blocked")
	}
	current, err := p.sessionRuntimeIdentity()
	if err != nil {
		return err
	}
	if current != state.RuntimeIdentity {
		return fmt.Errorf("original runtime/store differs from selected context; automatic mutation and cleanup blocked")
	}
	return nil
}
