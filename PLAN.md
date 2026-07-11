# PLAN: Implementing Podman GUI Wrapper Features

## Objective
Implement a sleek, lightweight, and modern GUI application using Wails v3 to manage Podman containers, images, and system status.

## Scope
- **Files to create**:
  - `podman.go`: Contains Go methods for calling Podman CLI and parsing JSON outputs. Exposes Wails service.
- **Files to modify**:
  - `main.go`: Register `PodmanService` and clean up `GreetService`.
  - `frontend/index.html`: Fully design a premium, modern dashboard.
  - `frontend/src/main.js`: Link dashboard elements to Wails backend APIs.
  - `frontend/public/style.css`: Implement a beautiful, custom dark-themed design system.
- **Dependencies to add**: None.

## Implementation Approach

### 1. Go Backend (`podman.go`)
Define `PodmanService` with methods:
- `GetSystemInfo() (*SystemInfo, error)`: Parses `podman info --format json`
- `ListContainers(all bool) ([]Container, error)`: Parses `podman ps -a --format json`
- `StartContainer(id string) error`: Runs `podman start [id]`
- `StopContainer(id string) error`: Runs `podman stop [id]`
- `RestartContainer(id string) error`: Runs `podman restart [id]`
- `RemoveContainer(id string) error`: Runs `podman rm -f [id]`
- `GetContainerLogs(id string) (string, error)`: Runs `podman logs --tail 200 [id]`
- `ListImages() ([]Image, error)`: Parses `podman images --format json`
- `PullImage(name string) error`: Runs `podman pull [name]`
- `RemoveImage(id string) error`: Runs `podman rmi -f [id]`
- `RunContainer(image string, name string, ports string, cmd string) error`: Runs a container with user inputs.

To prevent command injection, arguments will be passed directly as slice elements to `exec.Command` (no shell parsing, no `sh -c`).

### 2. Frontend Stylesheet (`frontend/public/style.css`)
- Rebuild standard stylesheet from scratch.
- Modern dark mode with dark navy background (`#0B0D19`), glowing borders (`rgba(99, 102, 241, 0.15)`), cards (`rgba(30, 41, 59, 0.4)` with backdrop-filter blur), and high-contrast styling.
- Rich CSS transition timings, micro-interactions, responsive CSS grid/flexbox layouts.

### 3. Frontend Markup & Logic (`frontend/index.html` & `frontend/src/main.js`)
- Tabbed interface: Dashboard (system stats), Containers, Images.
- Container management: Clickable buttons for Start/Stop/Restart/Delete, and View Logs modal.
- Image management: Listing images, Pull Image form, Run Container modal, and Delete image button.
- Clean alerts/notifications for errors or action progress.

## Testing Strategy
- Run the app via `wails3 dev` or compile using `wails3 build` and run locally.
- Test edge cases:
  - Error when pulling an invalid image (handled gracefully in UI).
  - Empty image/container states shown cleanly in UI.
  - Real-time refresh of logs and status.

## Risks & Mitigations
- **Risk**: Sluggish CLI invocation slowing down the UI.
  - *Mitigation*: Run operations asynchronously where appropriate, use lightweight `--format json` flags, and keep Go parsing CPU usage minimal.
- **Risk**: UI styling doesn't feel "premium".
  - *Mitigation*: Use a coordinated palette (deep indigo `#4f46e5`, teal `#0d9488`, slate gray, dark glass elements) with rounded corners (`12px`/`16px`) and subtle drop-shadows.

## Rollback Plan
- Revert files using `git checkout` or delete the created files.
