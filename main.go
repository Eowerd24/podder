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
	// CLI Arguments Parsing
	if len(os.Args) > 1 {
		cmd := os.Args[1]
		if cmd == "up" || cmd == "down" {
			handleComposeCommand(cmd)
			return
		}
		if cmd == "help" || cmd == "--help" || cmd == "-h" {
			printUsage()
			return
		}
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
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

// printUsage displays CLI help details.
func printUsage() {
	fmt.Println("Podder - Podman GUI Wrapper & CLI Compose Helper")
	fmt.Println("\nUsage:")
	fmt.Println("  podder          Launch the graphical user interface (GUI)")
	fmt.Println("  podder up       Run 'compose up -d' in the current directory")
	fmt.Println("  podder down     Run 'compose down' in the current directory")
	fmt.Println("  podder help     Show this help message")
}
