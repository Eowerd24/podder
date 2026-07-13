package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveComposeProviderPrefersPodmanCompose(t *testing.T) {
	t.Parallel()

	provider, err := resolveComposeProviderWithLookPath("up", func(file string) (string, error) {
		switch file {
		case "podman-compose":
			return "/usr/bin/podman-compose", nil
		case "podman":
			return "/usr/bin/podman", nil
		case "docker-compose":
			return "/usr/bin/docker-compose", nil
		default:
			return "", errors.New("missing")
		}
	})
	if err != nil {
		t.Fatalf("resolveComposeProviderWithLookPath returned error: %v", err)
	}

	if provider.path != "/usr/bin/podman-compose" {
		t.Fatalf("expected podman-compose provider, got %q", provider.path)
	}
	if provider.needsPodmanSocket {
		t.Fatal("podman-compose should not require Podman socket preflight")
	}
}

func TestResolveComposeProviderPrefersPodmanOverDockerCompose(t *testing.T) {
	t.Parallel()

	provider, err := resolveComposeProviderWithLookPath("down", func(file string) (string, error) {
		switch file {
		case "podman-compose":
			return "", errors.New("missing")
		case "podman":
			return "/usr/bin/podman", nil
		case "docker-compose":
			return "/usr/bin/docker-compose", nil
		default:
			return "", errors.New("missing")
		}
	})
	if err != nil {
		t.Fatalf("resolveComposeProviderWithLookPath returned error: %v", err)
	}

	if provider.path != "/usr/bin/podman" {
		t.Fatalf("expected podman provider, got %q", provider.path)
	}
	if !provider.needsPodmanSocket {
		t.Fatal("podman compose should require Podman socket preflight")
	}
	if len(provider.args) != 2 || provider.args[0] != "compose" || provider.args[1] != "down" {
		t.Fatalf("unexpected podman compose args: %v", provider.args)
	}
}

func TestPodmanSocketPathUsesRuntimeDir(t *testing.T) {
	t.Parallel()

	socketPath := podmanSocketPath(func(key string) string {
		if key == "XDG_RUNTIME_DIR" {
			return "/run/user/1000"
		}
		return ""
	}, "1000")

	if socketPath != "/run/user/1000/podman/podman.sock" {
		t.Fatalf("unexpected socket path: %q", socketPath)
	}
}

func TestPodmanSocketPathFallsBackToUID(t *testing.T) {
	t.Parallel()

	socketPath := podmanSocketPath(func(string) string { return "" }, "1001")
	if socketPath != "/run/user/1001/podman/podman.sock" {
		t.Fatalf("unexpected fallback socket path: %q", socketPath)
	}
}

func TestEnsurePodmanSocketStartsMissingSocket(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "podman", "podman.sock")

	runCalled := false
	err := ensurePodmanSocket(
		socketPath,
		fileExists,
		func(file string) (string, error) {
			if file == "systemctl" {
				return "/usr/bin/systemctl", nil
			}
			return "", errors.New("missing")
		},
		func(name string, args ...string) error {
			runCalled = true
			if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
				return err
			}
			return os.WriteFile(socketPath, []byte("ok"), 0o644)
		},
	)
	if err != nil {
		t.Fatalf("ensurePodmanSocket returned error: %v", err)
	}
	if !runCalled {
		t.Fatal("expected ensurePodmanSocket to invoke systemctl when socket is missing")
	}
}

func TestEnsurePodmanSocketReturnsHelpfulErrorWithoutSystemctl(t *testing.T) {
	t.Parallel()

	err := ensurePodmanSocket(
		"/run/user/1000/podman/podman.sock",
		func(string) bool { return false },
		func(string) (string, error) { return "", errors.New("missing") },
		func(string, ...string) error { return nil },
	)
	if err == nil {
		t.Fatal("expected an error when systemctl is unavailable")
	}
}
