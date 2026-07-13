# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Changed
- Podder now prefers Podman-native compose execution before falling back to plain `docker-compose`.
- `pod up` / `podder up` now preflight the rootless Podman API socket and attempt `systemctl --user start podman.socket` automatically when `podman compose` needs it.

## [1.0.0] - 2026-07-11

### Added
- Local Go (v1.22.5) and Wails v3 CLI installation configuration.
- `PodmanService` backend in Go (`podman.go`) executing Podman CLI commands safely via `exec.Command` and returning structured JSON data.
- Full UI frontend in HTML/CSS/JS with a beautiful dark glassmorphic design system.
- Dashboard tab rendering container stats and host operating system details.
- Containers tab with controls to Start, Stop, Restart, and Remove containers.
- Terminal-like logs viewer modal with real-time refresh (3s polling).
- Run Container modal with inputs for image, name, ports, and command.
- Images tab allowing users to view local images, pull new images, run them, or delete them.
- Toast notifications for reporting action progress and errors.
- Unit test suite (`podman_test.go`) validating container, image, and system info JSON parsing.
- Comprehensive `.gitignore` configuration for Go, Wails v3, Node.js, and IDE files.
- Command-line argument parsing in `main.go` supporting `podder up` / `podder down` commands for executing `compose` commands.
- Symlinks for `podder` and `pod` globally exposed in the user's path (`/home/sarge/.local/bin/`).

