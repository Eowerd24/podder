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

## Installation

The easiest way to install Podder is to download the pre-compiled executable directly from the GitHub Releases. You do **not** need to install Go or Node.js.

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

*(Ubuntu/Debian users: Ensure you have the standard WebKit library installed via `sudo apt install libwebkitgtk-6.0-4`)*

---

## Contributing & Building from Source

If you want to modify the code or build Podder from scratch, you will need **Go (v1.22+)** and **Node.js** installed.

### 1. Install Dependencies
On Debian/Ubuntu systems, install the GTK and WebKitGTK development headers required by Wails:
```bash
sudo apt update
sudo apt install -y libgtk-4-dev libwebkitgtk-6.0-dev build-essential pkg-config
```

### 2. Install Wails v3 CLI
```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@latest
```

### 3. Build & Run
Clone the repository and run the development server:
```bash
git clone https://github.com/Eowerd24/podder.git
cd podder
wails3 dev
```
To compile a native release binary, run `wails3 build`.

3. **Explore Wails3 Documentation**:
   Visit [v3.wails.io](https://v3.wails.io/) for Wails v3 guides, API references, and templates.

   ---

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
