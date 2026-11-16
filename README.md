# Podder - Podman GUI Control Panel & CLI Compose Helper

Podder is a sleek, lightweight, and modern desktop application and CLI tool for managing local Podman containers, images, and compose setups. It is built on Go and Wails v3, featuring a premium dark glassmorphic interface and raw web technologies (HTML, CSS, Vanilla JS) for low resource footprint and native speeds.

---

## Features

- **Dashboard**: High-level host metrics (OS/Kernel details, CPUs, memory status, uptime) and container stats widgets.
- **Explicit Application Launchers**: Configure HTTP(S) endpoints for any container without claiming workload ownership. Running containers show Open Web or Choose Endpoint, with saved preferences, Copy URL fallback, stale-container review, and optional Start & Open readiness checks. Provisioned Xpra viewers and explicit terminal commands are supported.
- **Disposable Sessions**: Separate rootless OCI/GitHub launch flow, explicit temporary/config/data policies, journaled ownership, scoped cleanup, recipes, favorites and headless commands.
- **Provisioned GUI / Terminal / SSH Access**: Authenticated Xpra HTML5 viewers, consented terminal argv and explicit remote SSH endpoint forwarding without changing workload ownership.
- **Container Control & Provenance Badges**: List all containers (running/stopped), start, stop, restart, and remove them directly from the UI, with orchestrator-aware provenance classification (`Compose`, `Quadlet`, `Pod`, `Podder`, `Ad-Hoc`).
- **Port Administration & Socket Discovery**: Dedicated Ports tab showing published container mappings and active host listening sockets (`ss`), with conflict indicators, reconciliation status, and endpoint copying helpers.
- **Pure-Go Conflict Matrix & Exposure Safeguards**: Real-time port availability validation, protocol boundary checks (independent TCP/UDP), wildcard exposure warnings, and automated next-free-port suggestions.
- **External Port Registry & Reconciliation**: Optional, read-only Git-managed inventory (`ports.yaml`) support with live status reconciliation (`MATCH`, `UNDECLARED`, `DECLARED_MISSING`, `RESERVED_FREE`, `RESERVED_IN_USE`, `PLANNED`).
- **Podder Declarative Workload Specifications**: Atomic persistence to `$XDG_CONFIG_HOME/podder/services/<name>.json`, automatic label injection (`io.podder.managed=true`), and built-in declarative deployer.
- **Safe Port Mutation Transactions**: Atomic port editing engine with 5-step transaction lifecycle (`Preflight -> Snapshot -> Rename & Launch -> Socket Health Verification -> Commit / Auto-Rollback`).
- **Quadlet & Compose Observation and Guidance**: Discovers the systemd `.container` unit or Docker/Podman Compose project backing a workload and generates a ready-to-paste, service-scoped `PublishPort=`/`ports:` snippet reflecting the exact requested port change. Automatic in-place mutation of Quadlet units and Compose files is intentionally disabled — Podder cannot yet prove it preserves every construct in those files (interpolation, anchors, drop-ins, long-form attributes) and their rollback/verification guarantees across providers, so the unit file or compose project always remains authoritative and Podder never writes to it directly. See "Ownership Matrix" below.
- **Workload Adoption**: One-click conversion of ad-hoc running containers into persistent Podder-managed declarative workloads with inspect-to-spec capture and safety analysis.
- **Podman Network Browser & IPAM Inspector**: Dedicated Networks view discovering local bridge/macvlan/ipvlan networks, subnets, gateways, internal isolation flags, DNS resolution status, and active container IP address allocations.
- **Real-Time Logs**: View streaming container stdout/stderr in a scrollable terminal-style modal.
- **Image Management & Multi-Port Run Modal**: List local images, pull new ones from public registries, run containers with structured multi-port and bind-mount configurations, and delete unneeded images.
- **Global CLI Command**: Act as a compose provider. Running `pod up` or `pod down` in a folder containing a compose file triggers your compose provider.
- **Podman Socket Preflight**: Before Podder runs `podman compose`, it checks for the rootless Podman API socket and tries to start `podman.socket` automatically when the socket is missing.

---

## Ownership Matrix

Podder treats a workload differently depending on who actually owns its configuration. It never recreates or force-mutates a workload it doesn't own:

| Ownership | Podder's role |
|---|---|
| **Podder-managed** | Transactional direct management: create, deploy, and mutate ports through the declarative `ContainerSpec`, with prepare/verify/commit and automatic rollback on failure. |
| **Compose-managed** | The compose project file remains authoritative. Podder observes the project/service and produces a safe, service-scoped `ports:` snippet to paste in yourself — it does not edit or apply the compose file automatically. |
| **Quadlet/systemd-managed** | The `.container` unit file remains authoritative. Podder observes it and produces a `PublishPort=` snippet — it does not edit the unit file or restart the service automatically. |
| **Pod member** | Ownership is pod-level. Podder never recreates an individual pod member as if it were a standalone container. |
| **Ad-hoc** | Ordinary start/stop/restart/remove controls apply. Destructive, replay-based operations (port mutation, `DeploySpec`) are only available after explicitly adopting the container into a Podder-managed spec. |

### Managed specification compatibility

Adoption preserves existing bind-mount `nosuid` and `nodev` restrictions in
managed schema 4 specifications. Replay retains them and verifies the effective
mount restrictions after startup. Schema 2 and 3 specifications remain readable,
preserve their ownership identities and gain no new mount flags. Older Podder
builds reject schema 4 rather than silently losing these restrictions. Disposable
session schema 1 does not accept the new flags.

---

## Open Web / Configure Application

In Containers, open **Application → Configure Application**. Choose **Web**,
add a published TCP mapping, select HTTP or HTTPS and an optional absolute path,
then Save. Choose only mappings you know serve a web application: Podder never
infers HTTP from a port number or image name. Use **None / background service**
to disable the launch action.

Running web containers show **Open Web**, or **Choose Endpoint** when several
endpoints have no preferred choice. The chooser can remember your preference.
**Application → Copy Web URL** always provides a selectable URL; a failed browser
handoff or readiness check also offers copying. Optional **Start & Open** waits
up to 15 seconds for HTTP(S), with normal certificate validation and no redirected
readiness requests. Normal Start remains available and does not auto-open.

Launcher settings are private local metadata under
`$XDG_CONFIG_HOME/podder/launchers/` (normally `~/.config/podder/launchers/`).
Verified Podder services retain their association across recreation. Other
workloads use their full container ID; a same-name replacement requires explicit
review and re-association. No Compose/Quadlet declarations, labels, managed
specs, published ports or auto-removal behavior are changed by launcher settings.

Local opening supports published TCP mappings on a verified local Podman host.
Wildcard bindings use loopback; IPv6 stays bracketed. UDP and unpublished ports
never become browser actions. Paths exclude credentials, queries and fragments.
HTTP readiness (status below 500) is an availability check, not application health.
If browser dispatch fails or no tab appears, use Copy Web URL.

For **GUI**, provision an authenticated Xpra HTML5 server separately, select GUI,
record its session identity and approve its published viewer endpoint. Podder
opens the viewer; it never forwards the host X11/Wayland socket or configures
Xpra authentication for you. **Terminal** stores explicit container command argv
and requires consent at each launch. Linux supports x-terminal-emulator, Konsole
and GNOME Terminal; Windows supports Windows Terminal when installed. Native
macOS terminal launching is unavailable.

## Disposable sessions and GitHub builds

The **Sessions** tab is separate from Run Container / Save as Managed. Choose an
OCI image, name, explicit ports, config/data mount targets and persistence policy:

- **Temporary:** container auto-removes; owned temporary directories and launcher
  metadata are removed only after confirmed scoped cleanup. No reusable recipe.
- **Keep configuration:** save a recipe and only the explicitly selected config
  mount; the data mount is temporary.
- **Keep configuration + data:** save the recipe and both explicit mount targets.

Bindings default to loopback; Add port proposes a free high port and launch
rechecks collisions. LAN/wildcard bindings and external directories require
separate approval. Config is read-only unless Allow config writes is checked.
For images that run as your host UID, the optional **Map my user ID** setting
adds rootless `--userns=keep-id` so their process can access private owned mounts.
It can change the image's effective user; select it deliberately after reviewing
the image. It is off by default and does not affect managed services.
External directories, preexisting images and build caches are never deleted.
Managed services keep their existing behavior and never acquire `--rm`.

Preview shows structured command arguments, masked environment values, mount
paths and retention decisions. Environment values are never stored; saved recipes
list required keys for re-entry. Command argv is also omitted unless you explicitly
confirm it contains no secrets and choose to save it. Images are pinned to their
resolved local ID; a recipe whose pinned image is missing requires restoring that
image or explicitly reviewing a new source, never silently following a changed tag.
Favorites and import/export are available in Sessions. Imports are reviewable
drafts: saved mount configuration stays visible, while host-directory and
network-exposure approvals must be renewed. Reopening preserves favorites and
the exact web mapping; a missing or edited mapping needs a new explicit choice.
GitHub metadata containing URL credentials or parameters is rejected before
saving, previewing or exporting; unsafe legacy metadata is retained for manual
review and cannot be exported automatically.

Cleanup is bound to the session's original local Podman store. A changed or
unverifiable store, or an older journal without that identity, blocks automatic
cleanup. Do not edit a journal to associate it with an arbitrary current store.
Active builders hold an OS lock across processes; cancel from the owning process
and wait for completion. After a crash, independently verify all builders have
exited and explicitly acknowledge interrupted-build recovery before cleanup.
Changed runroots (including after reboot) can require manual recovery. Disposable
session mutation fails closed on platforms without the required identity/locking.

The OliveTin starter explicitly uses port 1337 and `/config`, according to the
[official container documentation](https://docs.olivetin.app/install/podmandocker.html).
It creates a minimal Say hello configuration only when config.yaml is absent,
without overwriting existing config or mounting a host container socket.
Review the starter and image before launch.

For **GitHub**, choose a public HTTPS repository and full commit SHA. Preview
checks out the immutable source without executing setup scripts and displays its
Containerfile/Dockerfile. Explicitly approve the reviewed build to execute it.
Builds run rootlessly with a unique tag, bounded logs and cancellation. Denied
RUN networking is currently unsupported and fails before any builder is started. Podman build `--network none` is not an enforcement boundary: a
Containerfile can override it with `RUN --network=host`. Builds require separate,
explicit network approval, which permits RUN host networking too. Image pulls,
remote ADD URLs and other builder fetch channels are distinct from RUN consent;
this integration does not offer an offline-build guarantee.
Private repos, branch tracking and inferred build definitions are unavailable.
Launch built image has its own preview. Image cleanup requires a new image ID,
matching session build label/tag and proof that no other container/tag uses it.

After a stop, crash or app restart, session journals reconcile against exact
runtime identity and labels. Cleanup previews only owned resources, verifies
removal, and reports retained resources or retryable failures. Interrupted builds
require explicit verification that no builder still uses the workspace; containers
with session labels but no journal remain observation-only. Closing Podder leaves
detached containers running. Visible SSH helpers and active build commands are
cancelled at GUI exit. `--rm` does not erase images, host files, volumes, external
logs, caches or operating-system traces.

## Remote connections and headless use

The **Remote** tab stores explicit SSH profiles for already provisioned rootless
Podman sockets. It observes remote containers and stores separate launcher
preferences. It never edits remote workload definitions or installs a service.
Opening a configured HTTP/Xpra endpoint requires approval of a visible SSH tunnel
bound to workstation loopback. Close it in Remote, or exit Podder. Known host keys
and non-interactive credentials must already exist. Profiles can reference an
existing identity file and a dedicated known-hosts file. Both observation and
forwarding use strict, non-interactive SSH and never rewrite host-key files or
execute ambient SSH configuration hooks. TLS validation is never
bypassed. Forwarded HTTPS requires a certificate valid for the forwarded URL;
HTTP inside encrypted SSH is available for suitable provisioned services.

Failed/cancelled requested operations keep useful recovery JSON on stdout and
return nonzero; successful operations with warnings still return success.

`podder session` provides JSON-based headless list, recipes, starters, preview,
launch, stop, import/export and GitHub review/build/launch-built commands. Inputs
come from stdin, and execution/deletion requires `--confirm`. For example:

```bash
podder session preview < session.json
podder session launch --confirm < session.json
podder session list
podder session stop SESSION_ID                 # cleanup preview
podder session stop SESSION_ID --confirm       # stop and scoped cleanup
podder session export RECIPE_NAME > recipe.json
podder session import < recipe.json            # inspect draft
podder session import --confirm < recipe.json  # save without executing
```

For security reporting and current validation limits, see [Security](SECURITY.md).

---

## Installation

The easiest way to install Podder is to download the pre-compiled executable directly from the GitHub Releases. You do **not** need to install Go or Node.js.

Release version: `1.5.6`.

### Option A: 1-Line Installation (Recommended)
You can download the latest binary directly into your path and make it executable with a single command:
```bash
sudo curl -L -o /usr/local/bin/pod https://github.com/Eowerd24/podder/releases/latest/download/podder && sudo chmod +x /usr/local/bin/pod
```

### Option B: Manual Installation
1. **Download the latest release:**
   Download the `podder` executable from the [GitHub Releases page](https://github.com/Eowerd24/podder/releases).
2. **Make it executable & move to your PATH:**
   ```bash
   chmod +x podder
   sudo mv podder /usr/local/bin/pod
   ```

### Usage:
- Type `pod` anywhere in your terminal to open the GUI.
- Type `pod up` in a directory with a `docker-compose.yml` to spin up containers.
- Type `pod pull ubuntu` (or any podman command) to pass native commands directly to Podman.

When `pod up` needs the Podman Docker-compatible API socket, Podder will try `systemctl --user start podman.socket` automatically. If you want that socket to be reliably available after login or reboot, enable it once yourself:

```bash
systemctl --user enable --now podman.socket
sudo loginctl enable-linger "$USER"
```

*(Ubuntu/Debian users: Ensure you have the standard WebKit library installed via `sudo apt install libwebkitgtk-6.0-4`)*

---

## Contributing & Building from Source

If you want to modify the code or build Podder from scratch, you will need **Go 1.26.11 (matching `go.mod`)** and **Node.js** installed.

### 1. Install Dependencies
On Debian/Ubuntu systems, install the GTK and WebKitGTK development headers required by Wails:
```bash
sudo apt update
sudo apt install -y libgtk-4-dev libwebkitgtk-6.0-dev build-essential pkg-config
```

### 2. Install Wails v3 CLI
```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
```

### 3. Build & Run
Clone the repository and run the development server:
```bash
git clone https://github.com/Eowerd24/podder.git
cd podder
wails3 dev
```
To compile a native release binary, run `wails3 build`. Source packaging metadata targets `1.5.6`, including generated platform assets.

3. **Explore Wails3 Documentation**:
   Visit [v3.wails.io](https://v3.wails.io/) for Wails v3 guides, API references, and templates.

   ---

### Rootless Podman compatibility tests

The optional [compatibility workflow](.github/workflows/podman-compatibility.yml)
runs three configured targets: `debian13`, `fedora45` and
`fedora45-upstream6.2`. The first two install their distribution's Podman package;
the third builds the pinned upstream 6.2.0 source inside Fedora 45. A configured
target is not a validated version: use each run's result JSON for its actual
Podman version, runtime configuration, required test outcomes and cleanup status.

Local VM validation on **2026-11-16** passed all **16 required top-level cases
and 25 child cases per target**, with zero failures, skips or missing cases:

| Guest target | Podman | crun | Netavark / aardvark-dns | SELinux |
|---|---|---|---|---|
| Debian 13 | 5.4.2 | 1.21 | 1.14 / 1.14 | Not present |
| Fedora 45 package | 6.1.3 | 1.29.1 | 2.1.0 / 2.1.0 | Enforcing |
| Fedora 45 upstream build | 6.2.0 | 1.29.1 | 2.1.0 / 2.1.0 | Enforcing |

All three used the same compiled test executable. The upstream row built the
full engine and matching helpers from [commit 055de04](https://github.com/podman-container-tools/podman/commit/055de046937d29450f310fce0fe3071485d8bed9)
inside the guest. Debian used kernel `6.12.116+deb13-cloud-amd64`; Fedora used
`7.2.11-300.fc45`. The default-stack case exercised rootless overlay storage,
systemd, cgroup v2 and pasta networking; other cases used the isolated fixtures
described below. These are local backend integration results; the manually
dispatched hosted workflow has not been validated by these runs.

Local runs require Linux amd64 with accessible KVM (`/dev/kvm`), Python 3,
`qemu-system-x86_64`, `qemu-img`, `genisoimage`, `ssh` and `scp`, plus Go 1.26.11
and the GTK 4/WebKitGTK 6 development headers used to compile the test executable.
Guests download packages during provisioning. Each guest defaults to 4 GiB RAM;
run targets sequentially on hosts with limited memory.

Run the lightweight runner boundary tests, then compile the backend integration
suite. The temporary frontend asset placeholder satisfies embedding requirements;
this suite does not exercise the UI.

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/test_podman_compat.py
PODDER_COMPAT_WORK=$(mktemp -d)
mkdir -p frontend/dist
touch frontend/dist/.build-placeholder
go test -c -tags integration -o "$PODDER_COMPAT_WORK/podder.test" .
rm frontend/dist/.build-placeholder

# Repeat with fedora45 and fedora45-upstream6.2, one target at a time.
PODDER_COMPAT_TARGET=debian13
python3 scripts/podman-compat.py \
  --target "$PODDER_COMPAT_TARGET" \
  --test-binary "$PODDER_COMPAT_WORK/podder.test" \
  --go-dir "$(go env GOROOT)" \
  --output "$PODDER_COMPAT_WORK/$PODDER_COMPAT_TARGET.json"
```

Keep result files outside the source tree. The runner boots disposable VMs and
never uses the host Podman executable or container store. It records source and
test-executable snapshot digests, and normally stops guests and removes their
disks and SSH keys. For local diagnosis, `--keep-vm` retains the private VM
workspace and prints an explicit `--cleanup` command; run that command afterward.

The default suite requires 16 top-level cases; missing cases or unexpected skips
fail the run. `TestPodmanCompatibility` exercises overlay storage, systemd,
cgroup v2, Netavark and pasta with HTTP checks across adoption, port mutation and
redeployment, including closure of the old port and bridge attachment discovery.
The remaining cases use isolated VFS/cgroupfs fixtures and file events for
adoption rejection, private environment transport, filesystem identity, rollback
and process lifetime coverage. These fixture results do not substitute for the
default-stack case, native UI checks or physical power-loss testing.

The manually dispatched CI matrix uploads only compact result JSON. VM disks,
keys and private diagnostic logs are not published. The normal build workflow
also runs the Python boundary tests without booting a VM.

### Alternative: Install Go via Bash Script

If you prefer an isolated user-space installation rather than a system-wide package, you can use the [Golang Tools Install Script](https://github.com/canha/golang-tools-install-script).

**1. Clone the repository:**
```bash
mkdir -p ~/git/GitHub/canha
cd ~/git/GitHub/canha
git clone https://github.com/canha/golang-tools-install-script
cd golang-tools-install-script/
```

**2. Run the installer (64-bit):**
```bash
bash goinstall.sh --64
```
*(This automatically downloads Go into your `~/.go` directory and appends the necessary paths to your `.bashrc`).*

**3. Reload your shell and verify:**
```bash
source ~/.bashrc
go version
```

---

## Project Structure

- `main.go`: The entrypoint of the Go backend which configures and loads the Wails application window.
- `podman.go`: Exposes Go services to the web interface (system stats, image list, container actions).
- `frontend/`:
  - `index.html`: Dashboard layout structure.
  - `src/main.js`: Listens to user interactions and communicates with the bound Go services.
  - `public/style.css`: Custom-built visual styling rules.

Port-registry reservation addresses accept IP literals (including bracketed IPv6),
`localhost` (defined as `127.0.0.1` only), `*`, or an empty default bind. Hostnames,
CIDRs, zone identifiers and address/port pairs are unsupported and block safety
validation when the registry is enabled. IPv4 and IPv6 loopback remain distinct;
omission, IPv4 wildcard and IPv6 wildcard retain their declaration distinctions.
Wildcard reservations conservatively conflict with every bind address.

Port editors submit an exact container ID, expected service and observed revision.
Close/reopen invalidates pending loads. Managed create, deploy, adoption, spec
writes and port transactions serialize within a configuration store. External
engine users can still race Podder; transactions use exact observed IDs and
recheck ownership before cleanup. Ambiguous candidates and blocked restorations
are retained and reported for explicit recovery.

SSH forwarding lifetime enforcement is currently supported on Linux. The SSH
helper stays in the foreground and is tied to its creating OS thread with a
kernel parent-death signal; normal close still cancels SSH, whose exit is reaped by the owning goroutine.
Other platforms reject forwarding until equivalent lifetime handling is available.
