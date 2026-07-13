import * as Podman from "../bindings/changeme/podmanservice.js";
import { Call as WailsCall } from "@wailsio/runtime";

// Active Tab state
let currentTab = 'dashboard';
let activeLogContainerId = null;
let logRefreshInterval = null;

const containerViewMeta = {
    all: {
        title: 'Containers',
        subtitle: 'Browse every local container or narrow the list to running or stopped instances.'
    },
    running: {
        title: 'Running Containers',
        subtitle: 'Focused view showing only actively running containers from the host.'
    },
    stopped: {
        title: 'Stopped Containers',
        subtitle: 'Focused view showing containers that are currently exited or otherwise not running.'
    }
};

// Initialize on DOM load
window.addEventListener('DOMContentLoaded', () => {
    updateContainerViewMeta(getSelectedContainerFilter());
    resetRunModal();

    // Initial data load
    refreshAll();
    
    // Auto-refresh every 5 seconds for dashboard and containers
    setInterval(() => {
        if (currentTab === 'dashboard') {
            loadSystemInfo();
        } else if (currentTab === 'containers') {
            loadContainers();
        }
    }, 5000);
});

// Switch active tab
window.switchTab = (tabId) => {
    currentTab = tabId;
    
    // Update navigation styles
    document.querySelectorAll('.tab-btn').forEach(btn => {
        btn.classList.remove('active');
    });
    
    // Find matching button and add active class
    const tabIndexMap = { 'dashboard': 0, 'containers': 1, 'images': 2 };
    const targetButton = document.querySelectorAll('.tab-btn')[tabIndexMap[tabId]];
    if (targetButton) {
        targetButton.classList.add('active');
    }
    
    // Show/hide content panels
    document.querySelectorAll('.tab-content').forEach(content => {
        content.classList.remove('active');
    });
    const targetContent = document.getElementById(`tab-${tabId}`);
    if (targetContent) {
        targetContent.classList.add('active');
    }
    
    // Load fresh data for the switched tab
    refreshAll();
};

// Global refresh
window.refreshAll = async () => {
    try {
        if (currentTab === 'dashboard') {
            await loadSystemInfo();
        } else if (currentTab === 'containers') {
            await loadContainers();
        } else if (currentTab === 'images') {
            await loadImages();
        }
    } catch (err) {
        showNotification(err.message || err, true);
    }
};

// --- API Calls & UI Renderers ---

// Load System Host Info
async function loadSystemInfo() {
    try {
        const info = await Podman.GetSystemInfo();
        if (!info) return;
        
        // Update stats widgets
        document.getElementById('stat-containers-total').textContent = info.totalContainers;
        document.getElementById('stat-containers-running').textContent = info.runningContainers;
        document.getElementById('stat-containers-stopped').textContent = info.stoppedContainers;
        document.getElementById('stat-images-total').textContent = info.totalImages;
        
        // Update table details
        document.getElementById('info-os').textContent = info.distribution || info.os || '-';
        document.getElementById('info-kernel').textContent = info.kernel || '-';
        document.getElementById('info-cpus').textContent = info.cpus || '-';
        document.getElementById('info-memory').textContent = formatBytes(info.memTotal);
        document.getElementById('info-uptime').textContent = info.uptime || '-';
        document.getElementById('info-version').textContent = info.podmanVersion || '-';
    } catch (err) {
        console.error("Failed to load system info:", err);
    }
}

// Load Containers List
window.loadContainers = async () => {
    const listContainer = document.getElementById('containers-list');
    if (!listContainer) return;
    
    try {
        const filterType = getSelectedContainerFilter();
        updateContainerViewMeta(filterType);
        const showAll = (filterType === 'all' || filterType === 'stopped');
        
        const allContainers = await Podman.ListContainers(showAll);
        
        let containers = allContainers || [];
        if (filterType === 'running') {
            containers = containers.filter(c => c.State && c.State.toLowerCase() === 'running');
        } else if (filterType === 'stopped') {
            containers = containers.filter(c => !c.State || c.State.toLowerCase() !== 'running');
        }

        if (!containers || containers.length === 0) {
            listContainer.innerHTML = `
                <div class="empty-state" style="grid-column: 1 / -1;">
                    <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <rect x="2" y="2" width="20" height="20" rx="2.18" ry="2.18"/><line x1="7" y1="2" x2="7" y2="22"/><line x1="17" y1="2" x2="17" y2="22"/><line x1="2" y1="12" x2="22" y2="12"/>
                    </svg>
                    <h3>No containers found</h3>
                    <p>Get started by running a container from the Images tab or clicking Run Container above.</p>
                </div>
            `;
            return;
        }
        
        listContainer.innerHTML = containers.map(c => {
            const name = c.Names && c.Names.length > 0 ? c.Names[0] : 'Unnamed';
            const state = c.State ? c.State.toLowerCase() : 'unknown';
            const isRunning = state === 'running';
            
            let statusClass = 'exited';
            if (isRunning) statusClass = 'running';
            else if (state === 'paused' || state === 'restarting') statusClass = 'paused';
            
            const shortId = c.Id ? c.Id.substring(0, 12) : '-';
            const command = c.Command ? c.Command.join(' ') : '-';
            
            return `
                <div class="container-card">
                    <div>
                        <div class="card-header-row">
                            <div class="card-title">${name}</div>
                            <span class="status-badge ${statusClass}">
                                <span style="width: 6px; height: 6px; border-radius: 50%; background: currentColor;"></span>
                                ${state}
                            </span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">ID</span>
                            <span class="card-detail-value">${shortId}</span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Image</span>
                            <span class="card-detail-value" title="${c.Image}">${c.Image}</span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Status</span>
                            <span class="card-detail-value" style="font-family: inherit; font-size: 13px; color: var(--text-muted);">${c.Status || '-'}</span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Command</span>
                            <span class="card-detail-value" title="${command}">${command}</span>
                        </div>
                    </div>
                    <div class="card-actions-row">
                        <button class="btn btn-secondary btn-icon" onclick="viewLogs('${c.Id}', '${name}')" title="View Logs">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="8" y1="6" x2="21" y2="6"/><line x1="8" y1="12" x2="21" y2="12"/><line x1="8" y1="18" x2="21" y2="18"/><line x1="3" y1="6" x2="3.01" y2="6"/><line x1="3" y1="12" x2="3.01" y2="12"/><line x1="3" y1="18" x2="3.01" y2="18"/></svg>
                        </button>
                        ${isRunning ? `
                            <button class="btn btn-secondary btn-icon" onclick="stopContainer('${c.Id}')" title="Stop Container">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="4" width="16" height="16" rx="2" ry="2"/></svg>
                            </button>
                        ` : `
                            <button class="btn btn-secondary btn-icon" onclick="startContainer('${c.Id}')" title="Start Container">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polygon points="5 3 19 12 5 21 5 3"/></svg>
                            </button>
                        `}
                        <button class="btn btn-secondary btn-icon" onclick="restartContainer('${c.Id}')" title="Restart Container">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67"/></svg>
                        </button>
                        <button class="btn btn-danger btn-icon" onclick="removeContainer('${c.Id}')" title="Remove Container">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><line x1="10" y1="11" x2="10" y2="17"/><line x1="14" y1="11" x2="14" y2="17"/></svg>
                        </button>
                    </div>
                </div>
            `;
        }).join('');
        
    } catch (err) {
        showNotification(`Failed to load containers: ${err.message || err}`, true);
    }
};

// Load Images List
async function loadImages() {
    const listContainer = document.getElementById('images-list');
    if (!listContainer) return;
    
    try {
        const images = await Podman.ListImages();
        
        if (!images || images.length === 0) {
            listContainer.innerHTML = `
                <div class="empty-state" style="grid-column: 1 / -1;">
                    <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
                    </svg>
                    <h3>No images found</h3>
                    <p>Pull an image from a registry above to get started.</p>
                </div>
            `;
            return;
        }
        
        listContainer.innerHTML = images.map(img => {
            const tag = img.Names && img.Names.length > 0 ? img.Names[0] : 'None';
            const shortId = img.Id ? img.Id.substring(0, 12) : '-';
            const size = formatBytes(img.Size);
            const created = img.CreatedAt ? new Date(img.CreatedAt).toLocaleDateString() : '-';
            
            return `
                <div class="image-card">
                    <div>
                        <div class="card-header-row" style="margin-bottom: 16px;">
                            <div class="card-title" style="max-width: 100%; font-size: 15px; font-family: var(--font-mono);">${tag}</div>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Image ID</span>
                            <span class="card-detail-value">${shortId}</span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Virtual Size</span>
                            <span class="card-detail-value">${size}</span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Created At</span>
                            <span class="card-detail-value">${created}</span>
                        </div>
                    </div>
                    <div class="card-actions-row">
                        <button class="btn btn-secondary" onclick="openRunModal('${tag}')">
                            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polygon points="5 3 19 12 5 21 5 3"/></svg>
                            Run Image
                        </button>
                        <button class="btn btn-danger btn-icon" onclick="removeImage('${img.Id}')" title="Delete Image">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><line x1="10" y1="11" x2="10" y2="17"/><line x1="14" y1="11" x2="14" y2="17"/></svg>
                        </button>
                    </div>
                </div>
            `;
        }).join('');
        
    } catch (err) {
        showNotification(`Failed to load images: ${err.message || err}`, true);
    }
}

// --- Container Actions ---

window.startContainer = async (id) => {
    try {
        showNotification("Starting container...", false);
        await Podman.StartContainer(id);
        showNotification("Container started successfully", false, true);
        loadContainers();
    } catch (err) {
        showNotification(`Error starting container: ${err}`, true);
    }
};

window.stopContainer = async (id) => {
    try {
        showNotification("Stopping container...", false);
        await Podman.StopContainer(id);
        showNotification("Container stopped successfully", false, true);
        loadContainers();
    } catch (err) {
        showNotification(`Error stopping container: ${err}`, true);
    }
};

window.restartContainer = async (id) => {
    try {
        showNotification("Restarting container...", false);
        await Podman.RestartContainer(id);
        showNotification("Container restarted successfully", false, true);
        loadContainers();
    } catch (err) {
        showNotification(`Error restarting container: ${err}`, true);
    }
};

window.removeContainer = async (id) => {
    if (!confirm("Are you sure you want to force remove this container?")) return;
    try {
        showNotification("Removing container...", false);
        await Podman.RemoveContainer(id);
        showNotification("Container removed successfully", false, true);
        loadContainers();
    } catch (err) {
        showNotification(`Error removing container: ${err}`, true);
    }
};

// --- Logs Modal ---

window.viewLogs = async (id, name) => {
    activeLogContainerId = id;
    document.getElementById('logs-modal-title').textContent = `Logs for: ${name}`;
    document.getElementById('logs-text').textContent = "Fetching logs...";
    
    openModal('logs-modal');
    await refreshLogs();
    
    // Auto-refresh logs every 3 seconds while modal is open
    logRefreshInterval = setInterval(refreshLogs, 3000);
};

window.refreshLogs = async () => {
    if (!activeLogContainerId) return;
    try {
        const logs = await Podman.GetContainerLogs(activeLogContainerId);
        const logBox = document.getElementById('logs-text');
        
        // Save scroll height to determine if user has scrolled up
        const isScrolledToBottom = logBox.scrollHeight - logBox.clientHeight <= logBox.scrollTop + 50;
        
        logBox.textContent = logs || "(No logs)";
        
        // Auto scroll to bottom if they were already at the bottom
        if (isScrolledToBottom) {
            logBox.scrollTop = logBox.scrollHeight;
        }
    } catch (err) {
        document.getElementById('logs-text').textContent = `Failed to get logs: ${err}`;
    }
};

// --- Image Actions ---

window.pullImage = async () => {
    const input = document.getElementById('pull-image-name');
    const name = input.value.trim();
    if (!name) {
        showNotification("Please specify an image name to pull.", true);
        return;
    }
    
    const btn = document.getElementById('btn-pull-image');
    const originalText = btn.innerHTML;
    
    try {
        btn.disabled = true;
        btn.innerHTML = `
            <svg class="animate-spin" style="animation: spin 1s linear infinite;" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="12" cy="12" r="10"/><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/></svg>
            Pulling...
        `;
        
        showNotification(`Pulling image: ${name}...`, false);
        await Podman.PullImage(name);
        showNotification(`Successfully pulled image: ${name}`, false, true);
        input.value = "";
        loadImages();
    } catch (err) {
        showNotification(`Error pulling image: ${err}`, true);
    } finally {
        btn.disabled = false;
        btn.innerHTML = originalText;
    }
};

window.removeImage = async (id) => {
    if (!confirm("Are you sure you want to remove this image?")) return;
    try {
        showNotification("Removing image...", false);
        await Podman.RemoveImage(id);
        showNotification("Image removed successfully", false, true);
        loadImages();
    } catch (err) {
        showNotification(`Error removing image: ${err}`, true);
    }
};

// --- Run Container Form ---

window.submitRunContainer = async () => {
    const image = document.getElementById('run-image').value.trim();
    const name = document.getElementById('run-name').value.trim();
    const ports = document.getElementById('run-ports').value.trim();
    const command = document.getElementById('run-command').value.trim();
    const hostPath = document.getElementById('run-host-path').value.trim();
    const containerPath = document.getElementById('run-container-path').value.trim();
    const mountReadOnly = document.getElementById('run-mount-readonly').checked;
    
    if (!image) {
        showNotification("Image name is required.", true);
        return;
    }

    if ((hostPath && !containerPath) || (!hostPath && containerPath)) {
        showNotification("Both host content and container mount path are required when using a bind mount.", true);
        return;
    }
    
    try {
        showNotification("Creating container...", false);
        closeModal('run-modal');
        await WailsCall.ByName(
            "main.PodmanService.RunContainer",
            image,
            name,
            ports,
            command,
            hostPath,
            containerPath,
            mountReadOnly
        );
        showNotification("Container created and running successfully", false, true);

        resetRunModal();
        switchTab('containers');
    } catch (err) {
        showNotification(`Failed to run container: ${err}`, true);
    }
};

// --- Modals Management ---

window.openModal = (modalId) => {
    document.getElementById(modalId).classList.add('active');
};

window.openBuildModal = () => {
    document.getElementById('build-tag').value = '';
    openModal('build-modal');
};

window.openRunModal = (imageName = '') => {
    resetRunModal();
    document.getElementById('run-image').value = imageName;
    openModal('run-modal');
};

window.pickRunHostPath = async (kind) => {
    try {
        const selectedPath = await WailsCall.ByName("main.PodmanService.SelectHostPath", kind);
        if (!selectedPath) {
            return;
        }

        document.getElementById('run-host-path').value = selectedPath;

        const containerPathInput = document.getElementById('run-container-path');
        if (containerPathInput && !containerPathInput.value.trim()) {
            containerPathInput.value = defaultContainerMountPath(kind, selectedPath);
        }

        const readOnlyCheckbox = document.getElementById('run-mount-readonly');
        if (readOnlyCheckbox && kind === 'image') {
            readOnlyCheckbox.checked = true;
        }

        showNotification(`Selected ${kind === 'folder' ? 'folder' : 'image file'} for bind mount.`, false, true);
    } catch (err) {
        showNotification(`Failed to select host path: ${err}`, true);
    }
};

window.closeModal = (modalId) => {
    document.getElementById(modalId).classList.remove('active');
    if (modalId === 'logs-modal') {
        activeLogContainerId = null;
        if (logRefreshInterval) {
            clearInterval(logRefreshInterval);
            logRefreshInterval = null;
        }
    }
};

// Close modal if clicking backdrop
document.querySelectorAll('.modal').forEach(modal => {
    modal.addEventListener('click', (e) => {
        if (e.target === modal) {
            closeModal(modal.id);
        }
    });
});

// --- Toast / Notification Banner helper ---
let notificationTimeout = null;

function showNotification(message, isError = false, isSuccess = false) {
    const banner = document.getElementById('notification');
    const icon = document.getElementById('notification-icon');
    const text = document.getElementById('notification-text');
    
    if (!banner || !text || !icon) return;
    
    // Clear styles
    banner.className = "notification-banner active";
    
    if (isError) {
        banner.classList.add('error');
        icon.innerHTML = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#f43f5e" stroke-width="2.5"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>`;
    } else if (isSuccess) {
        banner.classList.add('success');
        icon.innerHTML = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.5"><polyline points="20 6 9 17 4 12"/></svg>`;
    } else {
        // Standard info / loading spinner
        icon.innerHTML = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#6366f1" stroke-width="2.5" style="animation: spin 1s linear infinite;"><circle cx="12" cy="12" r="10"/><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83"/></svg>`;
    }
    
    text.textContent = message;
    
    // Auto clear standard alerts after 5 seconds, keep errors slightly longer, loading notifications persist until replaced
    if (notificationTimeout) {
        clearTimeout(notificationTimeout);
    }
    
    if (isSuccess || isError) {
        notificationTimeout = setTimeout(() => {
            banner.classList.remove('active');
        }, isError ? 7000 : 4000);
    }
}

// --- Navigation Helpers ---

window.navigateAndFilterContainers = (filterType) => {
    const filterDropdown = document.getElementById('container-filter');
    if (filterDropdown) {
        filterDropdown.value = filterType;
    }
    updateContainerViewMeta(filterType);
    switchTab('containers');
};

window.handleContainerFilterChange = () => {
    loadContainers();
};

// --- Compose Actions ---

window.runComposeDialog = async (action) => {
    try {
        showNotification(`Opening file browser to select compose ${action === 'up' ? 'startup' : 'teardown'} directory...`, false);
        const result = await Podman.SelectAndRunCompose(action);
        
        if (result === "Cancelled by user.") {
            showNotification("Compose action cancelled.", false, true);
            return;
        }
        
        showNotification(`Compose ${action} executed successfully!\n${result}`, false, true);
        
        // Refresh containers list automatically
        if (currentTab === 'containers') {
            loadContainers();
        } else if (currentTab === 'dashboard') {
            loadSystemInfo();
        }
    } catch (err) {
        showNotification(`Compose Error: ${err}`, true);
    }
};

window.submitBuildImage = async () => {
    const tag = document.getElementById('build-tag').value.trim();
    if (!tag) {
        showNotification("Image tag is required.", true);
        return;
    }
    
    closeModal('build-modal');
    
    try {
        showNotification("Opening file browser to select Dockerfile directory...", false);
        const result = await Podman.BuildImageFromDirectory(tag);
        
        if (result === "Cancelled by user.") {
            showNotification("Build cancelled.", false, true);
            return;
        }
        
        showNotification(`Build completed successfully!\n${result}`, false, true);
        if (currentTab === 'images') {
            loadImages();
        }
    } catch (err) {
        showNotification(`Build Error: ${err}`, true);
    }
};

// --- Utilities ---

function formatBytes(bytes) {
    if (!bytes || bytes === 0) return '0 Bytes';
    const k = 1024;
    const dm = 2;
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
}

function getSelectedContainerFilter() {
    const filterDropdown = document.getElementById('container-filter');
    return filterDropdown ? filterDropdown.value : 'all';
}

function updateContainerViewMeta(filterType) {
    const view = containerViewMeta[filterType] || containerViewMeta.all;
    const titleElement = document.getElementById('container-view-title');
    const subtitleElement = document.getElementById('container-view-subtitle');

    if (titleElement) {
        titleElement.innerHTML = `
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><rect x="2" y="2" width="20" height="20" rx="2" ry="2"/><line x1="12" y1="2" x2="12" y2="22"/></svg>
            ${view.title}
        `;
    }

    if (subtitleElement) {
        subtitleElement.textContent = view.subtitle;
    }
}

function resetRunModal() {
    const defaults = {
        'run-image': '',
        'run-name': '',
        'run-ports': '',
        'run-host-path': '',
        'run-container-path': '',
        'run-command': ''
    };

    Object.entries(defaults).forEach(([id, value]) => {
        const element = document.getElementById(id);
        if (element) {
            element.value = value;
        }
    });

    const readOnlyCheckbox = document.getElementById('run-mount-readonly');
    if (readOnlyCheckbox) {
        readOnlyCheckbox.checked = true;
    }
}

function defaultContainerMountPath(kind, selectedPath) {
    if (kind === 'image') {
        return `/app/input/${basename(selectedPath)}`;
    }
    return '/app/host';
}

function basename(path) {
    return path.split(/[\\/]/).pop() || 'selected-file';
}
