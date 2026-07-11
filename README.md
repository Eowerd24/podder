# Podder - Podman GUI Control Panel & CLI Compose Helper

Podder is a sleek, lightweight, and modern desktop application and CLI tool for managing local Podman containers, images, and compose setups. It is built on Go and Wails v3, featuring a premium dark glassmorphic interface and raw web technologies (HTML, CSS, Vanilla JS) for low resource footprint and native speeds.

---

## Features

- **Dashboard**: High-level host metrics (OS/Kernel details, CPUs, memory status, uptime) and container stats widgets.
- **Container Control**: List all containers (running/stopped), start, stop, restart, and remove them directly from the UI.
- **Real-Time Logs**: View streaming container stdout/stderr in a scrollable terminal-style modal.
- **Image Management**: List local images, pull new ones from public registries, run containers from images via quick forms, and delete unneeded images.
- **Global CLI Command**: Act as a compose provider. Running `pod up` or `pod down` in a folder containing a compose file triggers your compose provider.

---

## Installation & Setup

### 1. Prerequisites

Ensure you have **Go (v1.22+)** and **Node.js** installed.

On Debian/Ubuntu systems, install the GTK4 and WebKitGTK 6.0 development headers required by Wails:
```bash
sudo apt update
sudo apt install -y libgtk-4-dev libwebkitgtk-6.0-dev build-essential pkg-config
```

### 2. Install Wails v3 CLI
Install the Wails v3 toolchain command `wails3`:
```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
```
Ensure your `GOPATH/bin` (typically `~/go/bin` or `~/go-workspace/bin`) is added to your shell's `PATH`.

### 3. Build Podder
Clone the repository and compile the native executable:
```bash
git clone https://github.com/Eowerd24/podder.git
cd podder
npm install --prefix frontend
wails3 build
```
This produces a compiled binary at `bin/podder`.

### 4. Create Global Shell Shortcuts
To launch the GUI using `podder` or run compose setups using `pod up`/`pod down` from any directory, link the binary to your local user bin path:
```bash
ln -sf $(pwd)/bin/podder ~/.local/bin/podder
ln -sf $(pwd)/bin/podder ~/.local/bin/pod
```

---

## Getting Started (Wails3 Commands)

1. **Development Mode**:
   To run the application with live hot-reloading for both Go backend and frontend files:
   ```bash
   wails3 dev
   ```

2. **Production Build**:
   To rebuild the release binary:
   ```bash
   wails3 build
   ```

3. **Explore Wails3 Documentation**:
   Visit [v3.wails.io](https://v3.wails.io/) for Wails v3 guides, API references, and templates.

   ---

**How to install Go language with a Bash script** 

Another alternative to installing Go is to use a simple Bash script. It will download and install Go language under of your own user account.

Note that a system-wide installation might be better for some things (for example, better protected from accidental modifications etc.), but this was a bit simpler to setup.

For this example, we are using:

    https://github.com/canha/golang-tools-install-script

Create directory

mkdir -p ~/git/GitHub/canha

Clone Git repository

cd ~/git/GitHub/canha
git clone https://github.com/canha/golang-tools-install-script 
cd golang-tools-install-script/

Install a 64-bit version

bash goinstall.sh --64

Script downloads the version specified in the Bash script (at the moment 1.9.2) and installs it to ~/.go directory.
Check that it was added to your shell config

cat ~/.bashrc

Reload your shell

source ~/.bashrc

Try if it works

go help

That should show a quick help for the go command.

---

## Project Structure

- `main.go`: The entrypoint of the Go backend which configures and loads the Wails application window.
- `podman.go`: Exposes Go services to the web interface (system stats, image list, container actions).
- `frontend/`:
  - `index.html`: Dashboard layout structure.
  - `src/main.js`: Listens to user interactions and communicates with the bound Go services.
  - `public/style.css`: Custom-built visual styling rules.
