# PLAN: Implementing CLI Commands for Podman GUI & Compose Control

## Objective
Enhance the built binary to function both as a GUI application (when run with no arguments) and as a CLI wrapper for compose commands (when run as `podder up` / `podder down` or `pod up` / `pod down` in a directory with a compose file). Expose `podder` and `pod` globally in the user's path.

## Scope
- **Files to modify**:
  - `main.go`: Parse command-line arguments to intercept `up`/`down` commands and route them to compose executors.
- **Symlinks to create**:
  - `/home/sarge/.local/bin/podder` -> `/home/sarge/Downloads/podder/bin/podder`
  - `/home/sarge/.local/bin/pod` -> `/home/sarge/Downloads/podder/bin/podder`

## Implementation Approach

### 1. Argument Parsing in `main.go`
- Add imports: `"fmt"`, `"os"`, `"os/exec"`, `"path/filepath"`.
- At the start of `main()`, check `os.Args`.
- If `len(os.Args) > 1`:
  - Read `os.Args[1]`.
  - If `os.Args[1] == "up"` or `os.Args[1] == "down"`:
    - Call a helper function `handleComposeCommand(os.Args[1])` to run the compose tool and exit.
  - If `os.Args[1] == "help"` or `--help` or `-h`:
    - Print a clean help manual and exit.
  - Otherwise, print an unknown command error and exit.
- If `len(os.Args) == 1`, proceed with launching the Wails GUI window.

### 2. Compose Execution Helper (`main.go`)
- Check for existence of compose configuration files in the current working directory:
  - `compose.yaml`, `compose.yml`, `docker-compose.yaml`, `docker-compose.yml`.
  - If none are found, report: `"No compose file (compose.yaml, compose.yml, docker-compose.yaml, docker-compose.yml) found in the current directory."` and exit with 1.
- Resolve the compose provider executable:
  - Look up `podman-compose` in the system path.
  - If not found, look up `docker-compose` in the system path.
  - If not found, look up `podman compose` (by checking if `podman` works with `compose`).
  - If no compose provider is available, print:
    `"Error: No compose provider found in path. Please install 'podman-compose' or 'docker-compose'."` and exit with 1.
- Run the compose provider with the command (`up -d` for "up", or `down` for "down").
- Bind the provider process's stdout/stderr and stdin directly to `os.Stdout`, `os.Stderr`, and `os.Stdin` to allow interactive output and real-time streaming.

### 3. Path Linking
- Create symlinks `/home/sarge/.local/bin/podder` and `/home/sarge/.local/bin/pod` pointing to the built binary, ensuring the commands can be run from any directory.

## Testing Strategy
- Compile the code: `wails3 build`.
- Create a test directory with a simple `compose.yaml` (running a small service like Alpine).
- Test executing `podder up` and `podder down` from that directory.
- Test running `pod up` and `pod down` (verifying symlink).
- Test running `podder` and `pod` with no arguments (verifying GUI launch behavior).

## Risks & Mitigations
- **Risk**: GUI launch tries to compile or run, but is missing X11/display connection.
  - *Mitigation*: CLI compose commands exit immediately without initializing the Wails GUI application. They execute in head/terminal mode.

## Rollback Plan
- Delete the symlinks.
- Revert changes to `main.go`.
