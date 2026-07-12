# About Podder

## What is Podder?

**Podder** is a lightweight, modern desktop application and CLI tool for managing Podman containers with ease. Built with **Go** and **Wails v3**, Podder provides both a sleek graphical interface and powerful command-line capabilities for developers and DevOps professionals.

## The Vision

We believe container management shouldn't be complicated. Podder strips away the complexity and gives you an elegant, responsive desktop experience combined with CLI commands that integrate seamlessly into your development workflow.

## Key Capabilities

- **Dashboard**: Real-time host metrics (OS, Kernel, CPU, memory, uptime) and container statistics
- **Container Management**: Start, stop, restart, and remove containers with a single click
- **Real-Time Logs**: Stream container logs in a terminal-style interface
- **Image Management**: Pull images from registries, run containers, and manage local images
- **Compose Integration**: Act as a compose provider—run `pod up` or `pod down` in any directory with a compose file
- **Native Podman Support**: Pass any podman command directly through the CLI

## Why Podder?

- **Fast**: Built on Go for performance-critical operations
- **Modern UI**: Responsive desktop interface powered by Wails v3
- **Developer-Friendly**: Works seamlessly in your existing workflow
- **Lightweight**: Minimal dependencies, easy installation
- **Open Source**: Community-driven and transparent development

## Tech Stack

- **Backend**: Go (47.1% of codebase)
- **Frontend**: JavaScript (11.2%) + HTML (7.6%) + CSS (6.3%)
- **Build Tools**: Wails v3, Node.js
- **Supporting**: NSIS (6.4%) for Windows installers

## Getting Started

Install Podder with a single command:
```bash
sudo curl -L -o /usr/local/bin/pod https://github.com/Eowerd24/podder/releases/latest/download/podder && sudo chmod +x /usr/local/bin/pod
```

Then simply type `pod` to open the GUI or use CLI commands directly.

## Contributing

Podder is open to contributions. Whether you're fixing bugs, adding features, or improving documentation—your help makes Podder better for everyone. Check out the [main README](README.md) for building from source.

---

**Podder**: Container Management, Simplified.
