package main

import (
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails uses Go's `embed` package to embed the frontend files into the binary.
// Any files in the frontend/dist folder will be embedded into the binary and
// made available to the frontend.
// See https://pkg.go.dev/embed for more information.

//go:embed all:frontend/dist
var assets embed.FS

type composeProvider struct {
	path              string
	args              []string
	needsPodmanSocket bool
}

type lookPathFunc func(file string) (string, error)
type pathExistsFunc func(path string) bool
type commandRunner func(name string, args ...string) error

// main function serves as the application's entry point. It initializes the application, creates a window,
// and starts the application.
func main() {
	// Disable WebKitGTK sandbox to prevent bubblewrap (bwrap) crash on systems with restricted user namespaces
	// This is a known issue on Ubuntu 24.04 and other Linux systems with AppArmor restricting user namespaces.
	os.Setenv("WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS", "1")

	// Disable WebKitGTK hardware acceleration/compositing features that fail in environments without DRI3
	// This fixes the sluggishness and libEGL warnings.
	os.Setenv("WEBKIT_DISABLE_COMPOSITING_MODE", "1")
	os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")

	// CLI Arguments Parsing
	if len(os.Args) > 1 {
		cmd := os.Args[1]
		if cmd == "up" || cmd == "down" {
			handleComposeCommand(cmd)
			return
		}
		if cmd == "help" || cmd == "--help" || cmd == "-h" {
			printUsage()
			// Also pass through to podman so they see the native help too
			handlePodmanPassthrough(os.Args[1:])
			return
		}
		// Treat any other command as a native podman command passthrough
		handlePodmanPassthrough(os.Args[1:])
		return
	}

	// Create a new Wails application by providing the necessary options.
	app := application.New(application.Options{
		Name:        "podder",
		Description: "Simple lightweight GUI wrapper for basic Podman control",
		Services: []application.Service{
			application.NewService(&PodmanService{}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// Create a new window with the necessary options.
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Podder",
		Width:  1100,
		Height: 700,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(11, 13, 25),
		URL:              "/",
	})

	// Run the application. This blocks until the application has been exited.
	err := app.Run()

	// If an error occurred while running the application, log it and exit.
	if err != nil {
		log.Fatal(err)
	}
}

// handleComposeCommand routes compose up/down requests to podman-compose, docker-compose, or podman compose.
func handleComposeCommand(action string) {
	if err := ensureComposeFilePresent(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	provider, err := resolveComposeProvider(action)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	if err := ensureComposeProviderReady(provider); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	composeCmd := exec.Command(provider.path, provider.args...)

	// Stream stdout, stderr and stdin directly
	composeCmd.Stdout = os.Stdout
	composeCmd.Stderr = os.Stderr
	composeCmd.Stdin = os.Stdin

	fmt.Printf("Executing: %s %s...\n", composeCmd.Path, strings.Join(composeCmd.Args[1:], " "))
	err = composeCmd.Run()
	if err != nil {
		fmt.Printf("Error running compose command: %v\n", err)
		os.Exit(1)
	}
}

func ensureComposeFilePresent() error {
	composeFiles := []string{
		"compose.yaml",
		"compose.yml",
		"docker-compose.yaml",
		"docker-compose.yml",
	}

	for _, file := range composeFiles {
		if _, err := os.Stat(file); err == nil {
			return nil
		}
	}

	return errors.New("Error: No compose file (compose.yaml, compose.yml, docker-compose.yaml, docker-compose.yml) found in the current directory.")
}

func resolveComposeProvider(action string) (*composeProvider, error) {
	return resolveComposeProviderWithLookPath(action, exec.LookPath)
}

func resolveComposeProviderWithLookPath(action string, lookPath lookPathFunc) (*composeProvider, error) {
	args, err := composeArgs(action)
	if err != nil {
		return nil, err
	}

	if path, err := lookPath("podman-compose"); err == nil {
		return &composeProvider{
			path: path,
			args: args,
		}, nil
	}

	if path, err := lookPath("podman"); err == nil {
		return &composeProvider{
			path:              path,
			args:              append([]string{"compose"}, args...),
			needsPodmanSocket: true,
		}, nil
	}

	if path, err := lookPath("docker-compose"); err == nil {
		return &composeProvider{
			path: path,
			args: args,
		}, nil
	}

	return nil, errors.New("Error: No compose provider found in PATH.\nPlease install 'podman-compose' or configure 'podman compose' (with a docker-compose provider).")
}

func composeArgs(action string) ([]string, error) {
	switch action {
	case "up":
		return []string{"up", "-d"}, nil
	case "down":
		return []string{"down"}, nil
	default:
		return nil, fmt.Errorf("Error: Unsupported compose action %q.", action)
	}
}

func ensureComposeProviderReady(provider *composeProvider) error {
	if !provider.needsPodmanSocket {
		return nil
	}

	socketPath := podmanSocketPath(os.Getenv, currentUserUID())
	return ensurePodmanSocket(socketPath, fileExists, exec.LookPath, runStreamingCommand)
}

func podmanSocketPath(getenv func(string) string, uid string) string {
	if runtimeDir := getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "podman", "podman.sock")
	}

	if uid == "" {
		return ""
	}

	return filepath.Join("/run/user", uid, "podman", "podman.sock")
}

func currentUserUID() string {
	currentUser, err := user.Current()
	if err != nil {
		return ""
	}

	return currentUser.Uid
}

func ensurePodmanSocket(socketPath string, exists pathExistsFunc, lookPath lookPathFunc, run commandRunner) error {
	if socketPath == "" {
		return errors.New("Error: Could not determine the Podman API socket path for this user session.")
	}

	if exists(socketPath) {
		return nil
	}

	if _, err := lookPath("systemctl"); err != nil {
		return fmt.Errorf("Error: Podman compose needs the user socket at %s, but it is not present and 'systemctl' is unavailable.\nRun 'systemctl --user enable --now podman.socket' manually.", socketPath)
	}

	fmt.Printf("Podman API socket not detected at %s. Starting podman.socket...\n", socketPath)
	if err := run("systemctl", "--user", "start", "podman.socket"); err != nil {
		return fmt.Errorf("Error: Failed to start podman.socket for compose support: %w\nRun 'systemctl --user enable --now podman.socket' manually and try again.", err)
	}

	if !exists(socketPath) {
		return fmt.Errorf("Error: Podman socket is still unavailable at %s after starting podman.socket.\nCheck 'systemctl --user status podman.socket --no-pager' for details.", socketPath)
	}

	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runStreamingCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// handlePodmanPassthrough routes all unrecognized commands directly to the native podman CLI.
func handlePodmanPassthrough(args []string) {
	cmd := exec.Command("podman", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		fmt.Printf("Error executing podman: %v\n", err)
		os.Exit(1)
	}
}

// printUsage displays CLI help details.
func printUsage() {
	fmt.Println("Podder - Podman GUI Wrapper & CLI Compose Helper")
	fmt.Println("\nUsage:")
	fmt.Println("  podder          Launch the graphical user interface (GUI)")
	fmt.Println("  podder up       Run 'compose up -d' in the current directory")
	fmt.Println("  podder down     Run 'compose down' in the current directory")
	fmt.Println("  podder help     Show this help message")
	fmt.Println("\nNative Podman Passthrough:")
	fmt.Println("  podder <cmd>    Any other command (e.g. 'pull', 'ps', 'build') is passed")
	fmt.Println("                  directly to the native podman CLI.")
}
