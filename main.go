package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails uses Go's `embed` package to embed the frontend files into the binary.
// Any files in the frontend/dist folder will be embedded into the binary and
// made available to the frontend.
// See https://pkg.go.dev/embed for more information.

//go:embed all:frontend/dist
var assets embed.FS

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
	// 1. Detect compose file in current directory
	composeFiles := []string{
		"compose.yaml",
		"compose.yml",
		"docker-compose.yaml",
		"docker-compose.yml",
	}

	found := false
	for _, file := range composeFiles {
		if _, err := os.Stat(file); err == nil {
			found = true
			break
		}
	}

	if !found {
		fmt.Printf("Error: No compose file (compose.yaml, compose.yml, docker-compose.yaml, docker-compose.yml) found in the current directory.\n")
		os.Exit(1)
	}

	// 2. Resolve compose tool
	var composeCmd *exec.Cmd

	// Check if podman-compose exists
	if _, err := exec.LookPath("podman-compose"); err == nil {
		if action == "up" {
			composeCmd = exec.Command("podman-compose", "up", "-d")
		} else {
			composeCmd = exec.Command("podman-compose", "down")
		}
	} else if _, err := exec.LookPath("docker-compose"); err == nil {
		// Check if docker-compose exists
		if action == "up" {
			composeCmd = exec.Command("docker-compose", "up", "-d")
		} else {
			composeCmd = exec.Command("docker-compose", "down")
		}
	} else {
		// Fallback to "podman compose"
		if _, err := exec.LookPath("podman"); err == nil {
			if action == "up" {
				composeCmd = exec.Command("podman", "compose", "up", "-d")
			} else {
				composeCmd = exec.Command("podman", "compose", "down")
			}
		}
	}

	if composeCmd == nil {
		fmt.Println("Error: No compose provider found in PATH.")
		fmt.Println("Please install 'podman-compose' or configure 'podman compose' (with a docker-compose provider).")
		os.Exit(1)
	}

	// Stream stdout, stderr and stdin directly
	composeCmd.Stdout = os.Stdout
	composeCmd.Stderr = os.Stderr
	composeCmd.Stdin = os.Stdin

	fmt.Printf("Executing: %s %s...\n", composeCmd.Path, strings.Join(composeCmd.Args[1:], " "))
	err := composeCmd.Run()
	if err != nil {
		fmt.Printf("Error running compose command: %v\n", err)
		os.Exit(1)
	}
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
