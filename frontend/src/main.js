import * as Podman from "../bindings/changeme/podmanservice.js";
import { Call as WailsCall } from "@wailsio/runtime";

// --- Trust boundary: escaping helpers for untrusted data ---
//
// Podman labels, container/image names, Compose project/service metadata,
// registry YAML strings, process names, and network names all originate
// outside Podder's control (a container's own labels, a homelab-wide
// registry file, host process listings, ...) and must never be rendered
// as raw HTML. Prefer textContent/dataset/addEventListener for dynamic
// content; where a template literal must interpolate untrusted text into
// markup, always pass it through escapeHtml first. Do not rely on
// ad hoc `.replace(/"/g, '&quot;')` calls — that only covers straight
// double quotes and leaves '<', '>', '&', and single quotes as breakout
// vectors.
function escapeHtml(value) {
    if (value === null || value === undefined) return '';
    return String(value)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

// Alias documenting intent at attribute-value call sites; escapeHtml's
// output (which escapes '"', "'", '&', '<', '>') is already safe inside a
// double- or single-quoted HTML attribute.
function escapeAttr(value) {
    return escapeHtml(value);
}

// Encodes a value for safe embedding inside a single-quoted JS string
// literal that itself sits inside a double-quoted HTML onclick attribute
// (e.g. onclick="fn('${...}')"). HTML-escaping the outer attribute alone is
// NOT sufficient here: an event-handler attribute's value is HTML-decoded
// by the browser BEFORE being compiled as JS, so an HTML entity for a
// single quote (e.g. &#39;) decodes right back into a literal ' before the
// JS parser ever sees it, and can still terminate the inline string early.
// This backslash-escapes the JS string content first (so a literal quote
// in the value can't break the JS string), then HTML-escapes the result
// (so it can't break the HTML attribute either) — either step alone is
// insufficient for this specific nested context.
function jsStringLiteral(value) {
    const jsEscaped = String(value === null || value === undefined ? '' : value)
        .replace(/\\/g, '\\\\')
        .replace(/'/g, "\\'")
        .replace(/\n/g, '\\n')
        .replace(/\r/g, '\\r')
        .replace(/\u2028/g, '\\u2028')
        .replace(/\u2029/g, '\\u2029');
    return escapeHtml(jsEscaped);
}

// Encodes a JSON-serializable value for safe embedding as an HTML
// attribute value (e.g. inside onclick="fn(...)"), escaping every
// character that could break out of the attribute or reopen markup —
// unlike a bare `.replace(/"/g, '&quot;')`, which leaves '<', '>', '&',
// and single quotes unescaped.
function jsonToSafeAttr(value) {
    return escapeHtml(JSON.stringify(value));
}

// Case-insensitive fragments of environment variable / spec field names
// treated as sensitive. Matching values are masked by default wherever a
// spec or adoption preview is rendered, since a stored spec's Env map can
// contain real credentials. The underlying value is never altered on disk
// — only its on-screen rendering is masked.
const SENSITIVE_KEY_PATTERNS = /password|token|secret|api[_-]?key|private[_-]?key|credential/i;

function maskSensitiveValue() {
    return '••••••••';
}

// Returns a deep-cloned spec/assessment object with values masked for any
// key matching SENSITIVE_KEY_PATTERNS (currently just the `env` map, the
// one place a spec carries values that can plausibly be credentials).
function withMaskedSecrets(spec) {
    if (!spec || typeof spec !== 'object') return spec;
    const clone = JSON.parse(JSON.stringify(spec));
    if (clone.env && typeof clone.env === 'object') {
        for (const key of Object.keys(clone.env)) {
            if (SENSITIVE_KEY_PATTERNS.test(key)) {
                clone.env[key] = maskSensitiveValue();
            }
        }
    }
    if (clone.proposedSpec) {
        clone.proposedSpec = withMaskedSecrets(clone.proposedSpec);
    }
    return clone;
}

// Active Tab state
let currentTab = 'dashboard';
let activeLogContainerId = null;
let logRefreshInterval = null;

// Ports Tab state
let currentPortFilter = 'all';
let portSearchText = '';
let cachedPortOverview = null;

// Run modal structured port rows
let runPortRows = [];
let nextPortRowId = 1;

// Edit Ports modal structured port rows
let editPortRows = [];
let nextEditPortRowId = 1;
let currentEditProvenance = null;
let currentEditSnippet = '';

const containerViewMeta = {
    all: {
        title: 'Containers',
        subtitle: 'Browse every local container or narrow the list by state or management provenance.'
    },
    running: {
        title: 'Running Containers',
        subtitle: 'Focused view showing only actively running containers from the host.'
    },
    stopped: {
        title: 'Stopped Containers',
        subtitle: 'Focused view showing containers that are currently exited or otherwise not running.'
    },
    'prov-compose': {
        title: 'Compose Workloads',
        subtitle: 'Containers managed by Docker Compose or Podman Compose project stacks.'
    },
    'prov-quadlet': {
        title: 'Quadlet / Systemd Services',
        subtitle: 'Containers managed natively via systemd unit definitions (.container).'
    },
    'prov-podder': {
        title: 'Podder-Managed Services',
        subtitle: 'Containers deployed declaratively and managed directly through Podder.'
    },
    'prov-pod': {
        title: 'Pod Member Containers',
        subtitle: 'Containers sharing network namespaces inside multi-container Pods.'
    },
    'prov-adhoc': {
        title: 'Ad-Hoc Containers',
        subtitle: 'Imperatively launched containers without an external orchestrator or declarative spec.'
    }
};

// Initialize on DOM load
window.addEventListener('DOMContentLoaded', () => {
    updateContainerViewMeta(getSelectedContainerFilter());
    resetRunModal();

    // Initial data load
    refreshAll();
    
    // Auto-refresh every 5 seconds
    setInterval(() => {
        if (currentTab === 'dashboard') {
            loadSystemInfo();
        } else if (currentTab === 'containers') {
            loadContainers();
        } else if (currentTab === 'ports') {
            loadPorts();
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
    const tabIndexMap = { 'dashboard': 0, 'containers': 1, 'images': 2, 'ports': 3 };
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
        } else if (currentTab === 'ports') {
            await loadPorts();
        } else if (currentTab === 'networks') {
            await loadNetworks();
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
        const showAll = (filterType !== 'running');
        
        const allContainers = await Podman.ListContainers(showAll);
        
        let containers = allContainers || [];
        if (filterType === 'running') {
            containers = containers.filter(c => c.State && c.State.toLowerCase() === 'running');
        } else if (filterType === 'stopped') {
            containers = containers.filter(c => !c.State || c.State.toLowerCase() !== 'running');
        } else if (filterType === 'prov-compose') {
            containers = containers.filter(c => c.provenance && c.provenance.type === 'compose');
        } else if (filterType === 'prov-quadlet') {
            containers = containers.filter(c => c.provenance && c.provenance.type === 'quadlet');
        } else if (filterType === 'prov-podder') {
            containers = containers.filter(c => c.provenance && c.provenance.type === 'podder');
        } else if (filterType === 'prov-pod') {
            containers = containers.filter(c => c.provenance && c.provenance.type === 'pod');
        } else if (filterType === 'prov-adhoc') {
            containers = containers.filter(c => !c.provenance || c.provenance.type === 'adhoc');
        }

        if (!containers || containers.length === 0) {
            listContainer.innerHTML = `
                <div class="empty-state" style="grid-column: 1 / -1;">
                    <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <rect x="2" y="2" width="20" height="20" rx="2.18" ry="2.18"/><line x1="7" y1="2" x2="7" y2="22"/><line x1="17" y1="2" x2="17" y2="22"/><line x1="2" y1="12" x2="22" y2="12"/>
                    </svg>
                    <h3>No containers match your filter</h3>
                    <p>Try switching the container filter or click Run Container to launch a new instance.</p>
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
            
            // Format published port mappings
            const mappings = c.PortMappings || c.Ports || [];
            let portsHtml = '<span class="card-detail-value" style="color: var(--text-muted); font-size: 13px;">None</span>';
            if (mappings.length > 0) {
                portsHtml = `
                    <div class="card-ports-container">
                        ${mappings.map(m => {
                            const bind = m.hostIP || '0.0.0.0';
                            const proto = (m.protocol || 'tcp').toUpperCase();
                            const isLoopback = bind.startsWith('127.') || bind === '::1' || bind === 'localhost';
                            const expClass = isLoopback ? 'exp-loopback' : (bind === '0.0.0.0' || bind === '*' ? 'exp-wildcard' : 'exp-specific');
                            return `
                                <div class="port-badge-row">
                                    <span class="port-proto-tag ${proto.toLowerCase() === 'udp' ? 'udp' : 'tcp'}">${escapeHtml(proto)}</span>
                                    <span class="port-mapping-text ${expClass}">${escapeHtml(bind)}:${escapeHtml(m.hostPort)} &rarr; ${escapeHtml(m.containerPort)}</span>
                                </div>
                            `;
                        }).join('')}
                    </div>
                `;
            }

            // Workload Provenance Badge
            const prov = c.provenance || { type: 'adhoc', displayType: 'Ad-Hoc', name: 'Ad-Hoc CLI' };
            let provClass = prov.type || 'adhoc';
            let provText = prov.displayType || 'Ad-Hoc';
            if (prov.service) {
                provText = `${prov.displayType}: ${prov.service}`;
            } else if (prov.name && prov.name !== 'Ad-Hoc CLI' && prov.name !== 'Podder Service') {
                provText = `${prov.displayType}: ${prov.name}`;
            }

            // prov/mappings are untrusted (container labels): encode as a
            // safe JS object-literal argument, and encode the name/id as
            // safe single-quoted JS string arguments — see jsStringLiteral.
            const safeProvJson = jsonToSafeAttr(prov);
            const safeMappingsJson = jsonToSafeAttr(mappings);
            const idArg = jsStringLiteral(c.Id);
            const nameArg = jsStringLiteral(name);

            return `
                <div class="container-card">
                    <div>
                        <div class="card-header-row">
                            <div>
                                <div class="card-title">${escapeHtml(name)}</div>
                                <span class="prov-badge ${escapeAttr(provClass)}" title="${escapeAttr(prov.guidance || '')}">${escapeHtml(provText)}</span>
                            </div>
                            <span class="status-badge ${statusClass}">
                                <span style="width: 6px; height: 6px; border-radius: 50%; background: currentColor;"></span>
                                ${escapeHtml(state)}
                            </span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">ID</span>
                            <span class="card-detail-value">${escapeHtml(shortId)}</span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Image</span>
                            <span class="card-detail-value" title="${escapeAttr(c.Image)}">${escapeHtml(c.Image)}</span>
                        </div>
                        ${c.PodName ? `
                            <div class="card-detail-item">
                                <span class="card-detail-label">Pod</span>
                                <span class="card-detail-value" style="color: #a78bfa;">${escapeHtml(c.PodName)}</span>
                            </div>
                        ` : ''}
                        <div class="card-detail-item">
                            <span class="card-detail-label">Published Ports</span>
                            ${portsHtml}
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Status</span>
                            <span class="card-detail-value" style="font-family: inherit; font-size: 13px; color: var(--text-muted);">${escapeHtml(c.Status || '-')}</span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Command</span>
                            <span class="card-detail-value" title="${escapeAttr(command)}">${escapeHtml(command)}</span>
                        </div>
                    </div>
                    <div class="card-actions-row">
                        <button class="btn btn-secondary btn-icon" onclick="viewLogs('${idArg}', '${nameArg}')" title="View Logs">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="8" y1="6" x2="21" y2="6"/><line x1="8" y1="12" x2="21" y2="12"/><line x1="8" y1="18" x2="21" y2="18"/><line x1="3" y1="6" x2="3.01" y2="6"/><line x1="3" y1="12" x2="3.01" y2="12"/><line x1="3" y1="18" x2="3.01" y2="18"/></svg>
                        </button>
                        <button class="btn btn-secondary btn-icon" onclick="openEditPortsModal('${idArg}', '${nameArg}', ${safeProvJson}, ${safeMappingsJson})" title="Edit Port Mappings">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
                        </button>
                        ${prov.type === 'adhoc' ? `
                            <button class="btn btn-secondary btn-icon" onclick="openAdoptModal('${idArg}', '${nameArg}')" title="Adopt Workload (Create Declarative Spec)">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2l3.09 6.26L22 9.27l-5 4.87 1.18 6.88L12 17.77l-6.18 3.25L7 14.14 2 9.27l6.91-1.01L12 2z"/></svg>
                            </button>
                        ` : ''}
                        ${prov.type === 'podder' ? `
                            <button class="btn btn-secondary btn-icon" onclick="viewContainerSpec('${nameArg}')" title="View Declarative Spec">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/></svg>
                            </button>
                        ` : ''}
                        ${isRunning ? `
                            <button class="btn btn-secondary btn-icon" onclick="stopContainer('${idArg}')" title="Stop Container">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="4" width="16" height="16" rx="2" ry="2"/></svg>
                            </button>
                        ` : `
                            <button class="btn btn-secondary btn-icon" onclick="startContainer('${idArg}')" title="Start Container">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polygon points="5 3 19 12 5 21 5 3"/></svg>
                            </button>
                        `}
                        <button class="btn btn-secondary btn-icon" onclick="restartContainer('${idArg}')" title="Restart Container">
                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67"/></svg>
                        </button>
                        <button class="btn btn-danger btn-icon" onclick="removeContainer('${idArg}')" title="Remove Container">
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

// --- Ports Tab View & Handlers ---

window.loadPorts = async () => {
    const portsContainer = document.getElementById('ports-list');
    if (!portsContainer) return;

    try {
        const overview = await Podman.GetPortOverview();
        if (!overview) return;
        cachedPortOverview = overview;

        // Update stats
        document.getElementById('stat-ports-published').textContent = overview.summary.totalPublishedMappings;
        document.getElementById('stat-ports-listeners').textContent = overview.summary.totalHostListeners;
        document.getElementById('stat-ports-unique').textContent = overview.summary.uniquePorts;
        document.getElementById('stat-ports-conflicts').textContent = overview.summary.totalConflicts;

        // Render registry reconciliation banner if enabled
        renderRegistryStatusBar(overview.summary);

        renderPortItems();
    } catch (err) {
        showNotification(`Failed to load ports overview: ${err.message || err}`, true);
    }
};

function renderRegistryStatusBar(summary) {
    const bar = document.getElementById('registry-status-bar');
    if (!bar) return;

    if (!summary.registryLoaded) {
        bar.style.display = 'none';
        bar.innerHTML = '';
        return;
    }

    bar.style.display = 'flex';
    bar.innerHTML = `
        <div class="reg-summary-item">
            <span class="reg-dot active"></span>
            <strong>Registry Active:</strong> <code>${escapeHtml(summary.registryPath)}</code>
        </div>
        <div class="reg-summary-metrics">
            <span class="reg-metric match" title="Observed runtime workloads matching registry definition">
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="20 6 9 17 4 12"/></svg>
                ${summary.registryMatch} Match
            </span>
            <span class="reg-metric undeclared" title="Running workloads not found in registry inventory">
                ${summary.registryUndeclared} Undeclared
            </span>
            <span class="reg-metric missing" title="Services defined in registry that are not currently running">
                ${summary.registryMissing} Missing
            </span>
            <span class="reg-metric reserved" title="Ports reserved in registry">
                ${summary.registryReserved} Reserved
            </span>
            <button class="btn btn-secondary btn-xs" onclick="openSettingsModal()" style="margin-left: 8px;">Config</button>
        </div>
    `;
}

function renderPortItems() {
    const container = document.getElementById('ports-list');
    if (!container || !cachedPortOverview) return;

    let items = cachedPortOverview.items || [];

    // Filter by source / category
    if (currentPortFilter === 'podman') {
        items = items.filter(i => i.source === 'podman');
    } else if (currentPortFilter === 'host') {
        items = items.filter(i => i.source === 'host-listener');
    } else if (currentPortFilter === 'registry') {
        items = items.filter(i => i.source === 'registry-declared' || (i.reconciliationStatus && i.reconciliationStatus === 'MATCH'));
    } else if (currentPortFilter === 'conflicts') {
        items = items.filter(i => i.status === 'CONFLICT');
    }

    // Filter by search query
    if (portSearchText) {
        const q = portSearchText.toLowerCase();
        items = items.filter(i => 
            (i.owner && i.owner.toLowerCase().includes(q)) ||
            (i.registryId && i.registryId.toLowerCase().includes(q)) ||
            (i.purpose && i.purpose.toLowerCase().includes(q)) ||
            (i.bindAddress && i.bindAddress.toLowerCase().includes(q)) ||
            (i.hostPort && String(i.hostPort).includes(q)) ||
            (i.containerPort && String(i.containerPort).includes(q)) ||
            (i.protocol && i.protocol.toLowerCase().includes(q)) ||
            (i.exposure && i.exposure.toLowerCase().includes(q)) ||
            (i.scope && i.scope.toLowerCase().includes(q)) ||
            (i.status && i.status.toLowerCase().includes(q)) ||
            (i.reconciliationStatus && i.reconciliationStatus.toLowerCase().includes(q))
        );
    }

    if (items.length === 0) {
        container.innerHTML = `
            <div class="empty-state" style="grid-column: 1 / -1;">
                <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>
                </svg>
                <h3>No port entries match your filter</h3>
                <p>Try selecting 'All' or clearing the search filter.</p>
            </div>
        `;
        return;
    }

    const hasRegistry = cachedPortOverview.summary && cachedPortOverview.summary.registryLoaded;

    container.innerHTML = `
        <div class="ports-table-wrapper">
            <table class="ports-table">
                <thead>
                    <tr>
                        <th>Source</th>
                        <th>Owner / Workload</th>
                        <th>Host Endpoint</th>
                        <th>Target Container</th>
                        <th>Scope / Exposure</th>
                        ${hasRegistry ? `<th>Reconciliation</th>` : ''}
                        <th>Status</th>
                        <th style="text-align: right;">Actions</th>
                    </tr>
                </thead>
                <tbody>
                    ${items.map(item => {
                        const bind = item.bindAddress || '0.0.0.0';
                        const endpointStr = `${bind}:${item.hostPort}`;
                        const proto = (item.protocol || 'TCP').toUpperCase();
                        
                        let sourceBadge = `<span class="source-badge podman">PODMAN</span>`;
                        if (item.source === 'host-listener') {
                            sourceBadge = `<span class="source-badge host">HOST</span>`;
                        } else if (item.source === 'registry-declared') {
                            sourceBadge = `<span class="source-badge registry">REGISTRY</span>`;
                        }

                        let expBadge = `<span class="exp-badge wildcard">WILDCARD</span>`;
                        const scopeVal = (item.scope || item.exposure || '').toLowerCase();
                        if (scopeVal === 'loopback') {
                            expBadge = `<span class="exp-badge loopback">LOOPBACK</span>`;
                        } else if (scopeVal === 'specific-ip') {
                            expBadge = `<span class="exp-badge specific">SPECIFIC IP</span>`;
                        } else if (scopeVal === 'lan') {
                            expBadge = `<span class="exp-badge lan">LAN</span>`;
                        } else if (scopeVal === 'management') {
                            expBadge = `<span class="exp-badge management">MANAGEMENT</span>`;
                        }

                        let statusBadge = `<span class="status-pill active">ACTIVE</span>`;
                        if (item.status === 'CONFLICT') {
                            statusBadge = `<span class="status-pill conflict" title="${escapeAttr(item.conflictNote || 'Conflict')}">CONFLICT</span>`;
                        } else if (item.status === 'STOPPED_CONFIGURED') {
                            statusBadge = `<span class="status-pill stopped">CONFIGURED</span>`;
                        } else if (item.status === 'DECLARED_MISSING') {
                            statusBadge = `<span class="status-pill missing">MISSING</span>`;
                        } else if (item.status === 'RESERVED_FREE') {
                            statusBadge = `<span class="status-pill reserved">RESERVED</span>`;
                        } else if (item.status === 'RESERVED_IN_USE') {
                            statusBadge = `<span class="status-pill conflict">RESERVED (IN USE)</span>`;
                        } else if (item.status === 'PLANNED') {
                            statusBadge = `<span class="status-pill planned">PLANNED</span>`;
                        }

                        // Reconciliation badge
                        let reconcileBadge = '';
                        if (hasRegistry) {
                            const rec = item.reconciliationStatus || 'UNDECLARED';
                            if (rec === 'MATCH') {
                                reconcileBadge = `<span class="reconcile-pill match" title="Matches declared entry in external registry">MATCH</span>`;
                            } else if (rec === 'UNDECLARED') {
                                reconcileBadge = `<span class="reconcile-pill undeclared" title="Running workload not registered in ports.yaml">UNDECLARED</span>`;
                            } else if (rec === 'DECLARED_MISSING') {
                                reconcileBadge = `<span class="reconcile-pill missing" title="Declared service is not currently active on host">MISSING</span>`;
                            } else if (rec === 'RESERVED_FREE') {
                                reconcileBadge = `<span class="reconcile-pill reserved" title="Declared reservation (free)">RESERVED</span>`;
                            } else if (rec === 'RESERVED_IN_USE') {
                                reconcileBadge = `<span class="reconcile-pill conflict" title="Declared reservation occupied by socket">IN USE</span>`;
                            } else if (rec === 'PLANNED') {
                                reconcileBadge = `<span class="reconcile-pill planned" title="Planned future service">PLANNED</span>`;
                            } else {
                                reconcileBadge = `<span class="reconcile-pill host">HOST</span>`;
                            }
                        }

                        // Provenance pill for table
                        let provBadge = '';
                        if (item.provenance && item.provenance.type) {
                            provBadge = `<span class="prov-mini-badge ${escapeAttr(item.provenance.type)}" title="${escapeAttr(item.provenance.guidance || '')}">${escapeHtml(item.provenance.displayType)}</span>`;
                        }

                        const targetStr = item.isContainer && item.containerPort ? `${item.containerPort}/${proto}` : '&mdash;';
                        const isHttpCandidate = proto === 'TCP' && (scopeVal === 'loopback' || scopeVal === 'specific-ip' || scopeVal === 'wildcard' || scopeVal === 'lan');
                        const urlHost = (bind === '0.0.0.0' || bind === '*' || bind === '') ? 'localhost' : bind;
                        const candidateUrl = `http://${urlHost}:${item.hostPort}`;

                        const safeProvJson = jsonToSafeAttr(item.provenance || {});
                        const ownerArg = jsStringLiteral(item.owner);
                        const containerIdArg = jsStringLiteral(item.containerId);
                        const endpointArg = jsStringLiteral(endpointStr);
                        const urlArg = jsStringLiteral(candidateUrl);

                        return `
                            <tr>
                                <td>${sourceBadge}</td>
                                <td>
                                    <div style="display: flex; align-items: center; gap: 6px;">
                                        <div style="font-weight: 600; color: var(--text-primary); font-size: 14px;">${escapeHtml(item.owner)}</div>
                                        ${provBadge}
                                    </div>
                                    ${item.purpose ? `<div style="font-size: 11px; color: var(--text-muted); margin-top: 2px;">${escapeHtml(item.purpose)}</div>` : ''}
                                    ${item.isContainer && item.containerId ? `<div style="font-size: 11px; color: var(--text-muted); font-family: var(--font-mono);">${escapeHtml(item.containerId.substring(0, 12))}</div>` : ''}
                                    ${item.registryId && item.source !== 'registry-declared' ? `<div style="font-size: 10px; color: var(--color-emerald); font-family: var(--font-mono);">reg: ${escapeHtml(item.registryId)}</div>` : ''}
                                </td>
                                <td>
                                    <span class="endpoint-code"><span class="proto-tag ${proto.toLowerCase() === 'udp' ? 'udp' : 'tcp'}">${escapeHtml(proto)}</span> ${escapeHtml(endpointStr)}</span>
                                    ${item.applicationProtocol ? `<div style="font-size: 10px; color: var(--text-muted); margin-top: 2px;">app: ${escapeHtml(item.applicationProtocol)}</div>` : ''}
                                </td>
                                <td>
                                    <span style="font-family: var(--font-mono); font-size: 13px;">${targetStr}</span>
                                </td>
                                <td>${expBadge}</td>
                                ${hasRegistry ? `<td>${reconcileBadge}</td>` : ''}
                                <td>
                                    ${statusBadge}
                                    ${item.conflictNote ? `<div style="font-size: 11px; color: var(--color-rose); margin-top: 4px;">${escapeHtml(item.conflictNote)}</div>` : ''}
                                </td>
                                <td style="text-align: right;">
                                    <div style="display: inline-flex; gap: 6px;">
                                        ${item.isContainer && item.containerId ? `
                                            <button class="btn btn-secondary btn-xs" onclick="openEditPortsModal('${containerIdArg}', '${ownerArg}', ${safeProvJson}, [])" title="Edit Container Ports">
                                                Edit Ports
                                            </button>
                                        ` : ''}
                                        <button class="btn btn-secondary btn-xs" onclick="copyText('${endpointArg}', 'Endpoint copied!')" title="Copy Host Endpoint">
                                            Copy Endpoint
                                        </button>
                                        ${isHttpCandidate ? `
                                            <button class="btn btn-secondary btn-xs" onclick="copyText('${urlArg}', 'URL copied!')" title="Copy URL">
                                                Copy URL
                                            </button>
                                        ` : ''}
                                    </div>
                                </td>
                            </tr>
                        `;
                    }).join('')}
                </tbody>
            </table>
        </div>
    `;
}

window.setPortFilter = (filter) => {
    currentPortFilter = filter;
    document.querySelectorAll('.filter-btn-group .filter-btn').forEach(btn => {
        btn.classList.remove('active');
    });
    const activeBtn = document.getElementById(`port-filter-${filter}`);
    if (activeBtn) activeBtn.classList.add('active');
    renderPortItems();
};

window.handlePortSearch = () => {
    const input = document.getElementById('port-search-input');
    portSearchText = input ? input.value.trim() : '';
    renderPortItems();
};

window.copyText = (text, successMsg = "Copied to clipboard!") => {
    if (navigator.clipboard) {
        navigator.clipboard.writeText(text).then(() => {
            showNotification(successMsg, false, true);
        }).catch(err => {
            showNotification(`Failed to copy: ${err}`, true);
        });
    } else {
        const textarea = document.createElement('textarea');
        textarea.value = text;
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand('copy');
        document.body.removeChild(textarea);
        showNotification(successMsg, false, true);
    }
};

// --- Settings & External Registry Handlers ---

window.openSettingsModal = async () => {
    try {
        const settings = await Podman.GetSettings();
        const enabled = settings && settings.portRegistry && settings.portRegistry.enabled;
        const path = settings && settings.portRegistry && settings.portRegistry.path ? settings.portRegistry.path : '';

        const enabledCheckbox = document.getElementById('setting-registry-enabled');
        if (enabledCheckbox) enabledCheckbox.checked = !!enabled;

        const pathInput = document.getElementById('setting-registry-path');
        if (pathInput) pathInput.value = path;

        toggleRegistrySettings(enabled);

        const banner = document.getElementById('registry-test-result');
        if (banner) {
            banner.style.display = 'none';
            banner.innerHTML = '';
        }

        openModal('settings-modal');
    } catch (err) {
        showNotification(`Failed to read settings: ${err}`, true);
    }
};

window.toggleRegistrySettings = (enabled) => {
    const fields = document.getElementById('registry-config-fields');
    if (fields) {
        fields.style.display = enabled ? 'block' : 'none';
    }
};

window.pickRegistryFile = async () => {
    try {
        const selected = await Podman.SelectRegistryFile();
        if (selected) {
            document.getElementById('setting-registry-path').value = selected;
            await testRegistryFile();
        }
    } catch (err) {
        showNotification(`Error selecting registry file: ${err}`, true);
    }
};

window.testRegistryFile = async () => {
    const banner = document.getElementById('registry-test-result');
    if (!banner) return;

    const path = document.getElementById('setting-registry-path').value.trim();
    if (!path) {
        banner.style.display = 'block';
        banner.className = 'registry-status-banner error';
        banner.innerHTML = `<strong>Error:</strong> Please specify a path to your ports.yaml file.`;
        return;
    }

    try {
        banner.style.display = 'block';
        banner.className = 'registry-status-banner loading';
        banner.innerHTML = `Testing registry file...`;

        const result = await Podman.LoadPortRegistry(path);
        if (!result || !result.loaded) {
            banner.className = 'registry-status-banner error';
            banner.innerHTML = `<strong>Failed to load registry:</strong> ${escapeHtml(result ? result.error : 'Unknown error')}`;
        } else {
            banner.className = 'registry-status-banner success';
            banner.innerHTML = `
                <div style="font-weight: 700; color: var(--color-emerald); margin-bottom: 4px;">&check; Registry Validated (V${escapeHtml(result.version)})</div>
                <div>Loaded <strong>${escapeHtml(result.totalEntries)}</strong> declared port entries from <code>${escapeHtml(result.path)}</code></div>
                ${result.warnings && result.warnings.length > 0 ? `<div style="margin-top: 6px; color: var(--color-amber, #f59e0b); font-size: 12px;">${result.warnings.map(w => escapeHtml(w)).join('<br>')}</div>` : ''}
            `;
        }
    } catch (err) {
        banner.className = 'registry-status-banner error';
        banner.innerHTML = `<strong>Error:</strong> ${escapeHtml(String(err))}`;
    }
};

window.saveAppSettings = async () => {
    const enabled = document.getElementById('setting-registry-enabled').checked;
    const path = document.getElementById('setting-registry-path').value.trim();

    const settings = {
        portRegistry: {
            enabled: enabled,
            path: path
        }
    };

    try {
        await Podman.SaveSettings(settings);
        closeModal('settings-modal');
        showNotification("Settings saved successfully", false, true);
        if (currentTab === 'ports') {
            loadPorts();
        }
    } catch (err) {
        showNotification(`Failed to save settings: ${err}`, true);
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
                            <div class="card-title" style="max-width: 100%; font-size: 15px; font-family: var(--font-mono);">${escapeHtml(tag)}</div>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Image ID</span>
                            <span class="card-detail-value">${escapeHtml(shortId)}</span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Virtual Size</span>
                            <span class="card-detail-value">${escapeHtml(size)}</span>
                        </div>
                        <div class="card-detail-item">
                            <span class="card-detail-label">Created At</span>
                            <span class="card-detail-value">${escapeHtml(created)}</span>
                        </div>
                    </div>
                    <div class="card-actions-row">
                        <button class="btn btn-secondary" onclick="openRunModal('${jsStringLiteral(tag)}')">
                            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polygon points="5 3 19 12 5 21 5 3"/></svg>
                            Run Image
                        </button>
                        <button class="btn btn-danger btn-icon" onclick="removeImage('${jsStringLiteral(img.Id)}')" title="Delete Image">
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
        
        const isScrolledToBottom = logBox.scrollHeight - logBox.clientHeight <= logBox.scrollTop + 50;
        
        logBox.textContent = logs || "(No logs)";
        
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

// --- Structured Run Container Modal & Port Management ---

window.addRunPortRow = (hostIP = '127.0.0.1', hostPort = '', containerPort = '', protocol = 'tcp') => {
    const rowId = nextPortRowId++;
    runPortRows.push({
        id: rowId,
        hostIP: hostIP,
        hostPort: hostPort,
        containerPort: containerPort,
        protocol: protocol,
        statusText: '',
        statusLevel: 'ok'
    });
    renderRunPortRows();
    updateRunExposureWarning();
};

window.removeRunPortRow = (id) => {
    runPortRows = runPortRows.filter(r => r.id !== id);
    renderRunPortRows();
    updateRunExposureWarning();
};

window.updateRunPortField = async (id, field, value) => {
    const row = runPortRows.find(r => r.id === id);
    if (!row) return;
    row[field] = value;
    
    // Live validation if hostPort and containerPort are set
    const hPort = parseInt(row.hostPort, 10);
    const cPort = parseInt(row.containerPort, 10);
    if (hPort > 0 && cPort > 0) {
        try {
            const validation = await Podman.ValidatePortMapping({
                hostIP: row.hostIP,
                hostPort: hPort,
                containerPort: cPort,
                protocol: row.protocol
            });
            if (validation && !validation.valid) {
                const errCheck = validation.checks.find(c => !c.passed);
                row.statusText = errCheck ? errCheck.message : 'Port collision detected';
                row.statusLevel = 'error';
            } else {
                row.statusText = 'Available';
                row.statusLevel = 'ok';
            }
        } catch (e) {
            console.error("Port validation error:", e);
        }
    } else {
        row.statusText = '';
        row.statusLevel = 'ok';
    }

    renderRunPortRows();
    updateRunExposureWarning();
};

window.findFreePortForRow = async (id) => {
    const row = runPortRows.find(r => r.id === id);
    if (!row) return;
    try {
        showNotification("Searching for next available port...", false);
        const freePort = await Podman.FindFreePort(3000, row.protocol, row.hostIP);
        if (freePort > 0) {
            row.hostPort = freePort;
            row.statusText = `Free port suggested: ${freePort}`;
            row.statusLevel = 'ok';
            showNotification(`Found free port ${freePort}/${row.protocol.toUpperCase()}`, false, true);
            renderRunPortRows();
            updateRunExposureWarning();
        }
    } catch (err) {
        showNotification(`Could not find free port: ${err}`, true);
    }
};

function renderRunPortRows() {
    const container = document.getElementById('run-ports-container');
    if (!container) return;

    if (runPortRows.length === 0) {
        container.innerHTML = `
            <div class="empty-port-hint">
                No ports published. Click <strong>+ Add Port</strong> to publish container services to the host.
            </div>
        `;
        return;
    }

    container.innerHTML = runPortRows.map(row => {
        return `
            <div class="port-input-row" id="port-row-${row.id}">
                <div class="port-field-group" style="flex: 1.5;">
                    <span class="field-mini-label">Bind IP</span>
                    <input type="text" class="form-input" value="${escapeAttr(row.hostIP)}" placeholder="127.0.0.1" onchange="updateRunPortField(${row.id}, 'hostIP', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 1;">
                    <span class="field-mini-label">Host Port</span>
                    <input type="number" class="form-input" value="${escapeAttr(row.hostPort)}" placeholder="8080" min="1" max="65535" oninput="updateRunPortField(${row.id}, 'hostPort', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 1;">
                    <span class="field-mini-label">Target Port</span>
                    <input type="number" class="form-input" value="${escapeAttr(row.containerPort)}" placeholder="80" min="1" max="65535" oninput="updateRunPortField(${row.id}, 'containerPort', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 0.8;">
                    <span class="field-mini-label">Proto</span>
                    <select class="form-input" onchange="updateRunPortField(${row.id}, 'protocol', this.value)">
                        <option value="tcp" ${row.protocol === 'tcp' ? 'selected' : ''}>TCP</option>
                        <option value="udp" ${row.protocol === 'udp' ? 'selected' : ''}>UDP</option>
                    </select>
                </div>
                <div style="display: flex; gap: 4px; align-items: flex-end; padding-bottom: 2px;">
                    <button type="button" class="btn btn-secondary btn-xs" onclick="findFreePortForRow(${row.id})" title="Suggest next free port">
                        Auto Free
                    </button>
                    <button type="button" class="btn btn-danger btn-xs" onclick="removeRunPortRow(${row.id})" title="Remove port mapping">
                        &times;
                    </button>
                </div>
            </div>
            ${row.statusText ? `
                <div class="row-validation-msg ${escapeAttr(row.statusLevel)}">${escapeHtml(row.statusText)}</div>
            ` : ''}
        `;
    }).join('');
}

function updateRunExposureWarning() {
    const warningBox = document.getElementById('run-exposure-warning');
    if (!warningBox) return;

    const hasWildcard = runPortRows.some(r => {
        const bind = (r.hostIP || '').trim();
        return bind === '0.0.0.0' || bind === '::' || bind === '*' || bind === '';
    });

    if (hasWildcard) {
        warningBox.style.display = 'block';
        warningBox.innerHTML = `
            <div class="warning-title">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
                Wildcard / External Network Exposure
            </div>
            <div>One or more mappings use <code>0.0.0.0</code> (all interfaces). This allows external network connections according to your host firewall rules. Use <code>127.0.0.1</code> if you only need local access.</div>
        `;
    } else {
        warningBox.style.display = 'none';
        warningBox.innerHTML = '';
    }
}

// --- Run Container Submit ---

window.submitRunContainer = async () => {
    const image = document.getElementById('run-image').value.trim();
    const name = document.getElementById('run-name').value.trim();
    const command = document.getElementById('run-command').value.trim();
    const hostPath = document.getElementById('run-host-path').value.trim();
    const containerPath = document.getElementById('run-container-path').value.trim();
    const mountReadOnly = document.getElementById('run-mount-readonly').checked;
    // The UI setting explicitly controls whether the workload becomes
    // Podder-managed — never inferred from whether port mappings were set.
    const saveSpecCheckbox = document.getElementById('run-save-spec');
    const managed = !!(saveSpecCheckbox && saveSpecCheckbox.checked);

    if (!image) {
        showNotification("Image name is required.", true);
        return;
    }

    if (managed && !name) {
        showNotification("A container name is required to save this as a Podder-managed workload.", true);
        return;
    }

    if ((hostPath && !containerPath) || (!hostPath && containerPath)) {
        showNotification("Both host content and container mount path are required when using a bind mount.", true);
        return;
    }

    // Build structured port mappings
    const structuredPortMappings = [];
    for (const row of runPortRows) {
        const hPort = parseInt(row.hostPort, 10);
        const cPort = parseInt(row.containerPort, 10);
        if (cPort > 0) {
            structuredPortMappings.push({
                hostIP: row.hostIP.trim(),
                hostPort: isNaN(hPort) ? 0 : hPort,
                containerPort: cPort,
                protocol: (row.protocol || 'tcp').toLowerCase()
            });
        }
    }

    const binds = [];
    if (hostPath && containerPath) {
        binds.push({ hostPath, containerPath, readOnly: mountReadOnly });
    }

    try {
        showNotification("Creating and starting container...", false);
        closeModal('run-modal');

        const result = await Podman.CreateContainer({
            image,
            name,
            managed,
            portMappings: structuredPortMappings,
            binds,
            env: {},
            command,
            entrypoint: []
        });

        if (result && result.managed) {
            showNotification("Container created and saved as a Podder-managed workload.", false, true);
        } else {
            showNotification("Container created and running successfully", false, true);
        }

        resetRunModal();
        switchTab('containers');
    } catch (err) {
        showNotification(`Failed to run container: ${err}`, true);
    }
};

// --- Edit Ports / Port Mutation Modal ---

window.setEditPortsMode = (mode) => {
    const btnInplace = document.getElementById('btn-mode-inplace');
    const btnManual = document.getElementById('btn-mode-manual');
    const interactiveArea = document.getElementById('edit-ports-interactive-area');
    const guidanceBox = document.getElementById('edit-ports-guidance-box');
    const submitBtn = document.getElementById('btn-submit-port-mutation');

    if (mode === 'inplace') {
        if (btnInplace) btnInplace.classList.add('active');
        if (btnManual) btnManual.classList.remove('active');
        if (interactiveArea) interactiveArea.style.display = 'block';
        if (guidanceBox) guidanceBox.style.display = 'none';
        if (submitBtn) submitBtn.style.display = 'inline-block';
    } else {
        if (btnInplace) btnInplace.classList.remove('active');
        if (btnManual) btnManual.classList.add('active');
        if (interactiveArea) interactiveArea.style.display = 'none';
        if (guidanceBox) guidanceBox.style.display = 'block';
        if (submitBtn) submitBtn.style.display = 'none';
    }
};

window.openEditPortsModal = async (containerId, containerName, provenance = {}, portMappings = []) => {
    currentEditProvenance = provenance || { type: 'adhoc' };
    currentEditSnippet = '';
    
    document.getElementById('edit-ports-container-id').value = containerId;
    document.getElementById('edit-ports-service-name').value = containerName || '';
    document.getElementById('edit-ports-unit-name').value = currentEditProvenance.unitName || currentEditProvenance.name || '';
    document.getElementById('edit-ports-title').textContent = `Edit Ports: ${containerName || (containerId || '').substring(0, 12)}`;

    // Render provenance pill in modal header (label/type/guidance are
    // untrusted — sourced from container labels — so build it via
    // textContent/dataset rather than innerHTML string interpolation).
    const provPillHost = document.getElementById('edit-ports-provenance');
    provPillHost.textContent = '';
    const provPill = document.createElement('span');
    provPill.className = `prov-badge ${currentEditProvenance.type || 'adhoc'}`;
    provPill.textContent = currentEditProvenance.displayType || 'Ad-Hoc';
    provPillHost.appendChild(provPill);

    const modeBar = document.getElementById('edit-ports-mode-bar');
    const guidanceBox = document.getElementById('edit-ports-guidance-box');
    const guidanceText = document.getElementById('edit-ports-guidance-text');
    const snippetWrapper = document.getElementById('edit-ports-snippet-wrapper');
    const snippetText = document.getElementById('edit-ports-snippet-text');
    const interactiveArea = document.getElementById('edit-ports-interactive-area');
    const fileInfo = document.getElementById('edit-ports-file-info');
    const stepsBox = document.getElementById('edit-ports-steps-box');
    const submitBtn = document.getElementById('btn-submit-port-mutation');

    stepsBox.style.display = 'none';
    document.getElementById('edit-ports-steps-list').innerHTML = '';
    modeBar.style.display = 'none';
    fileInfo.style.display = 'none';
    fileInfo.textContent = '';

    // Initialize rows from current mappings
    editPortRows = [];
    nextEditPortRowId = 1;

    if (currentEditProvenance.type === 'pod') {
        guidanceBox.style.display = 'block';
        guidanceText.textContent = `This container is part of Pod '${currentEditProvenance.podName || 'pod'}'. Ports in Podman belong to the Pod itself and cannot be edited on member containers.`;
        snippetWrapper.style.display = 'none';
        interactiveArea.style.display = 'none';
        submitBtn.style.display = 'none';
        openModal('edit-ports-modal');
        return;
    }

    // Direct mutation of an ad-hoc (or otherwise unrecognized/ambiguous)
    // container is permanently disabled: Podder has no authoritative spec
    // for it and cannot prove it can reproduce it. There is no
    // confirmation checkbox that makes recreating it safe — adopt the
    // workload first.
    if (currentEditProvenance.type === 'adhoc' || currentEditProvenance.type === 'ambiguous' || !currentEditProvenance.type) {
        guidanceBox.style.display = 'block';
        guidanceText.textContent = 'This container is not safely reproducible by Podder. Adopt it into Podder before editing its deployment configuration.';
        snippetWrapper.style.display = 'none';
        interactiveArea.style.display = 'none';
        submitBtn.style.display = 'none';
        openModal('edit-ports-modal');
        return;
    }

    if (currentEditProvenance.type === 'quadlet') {
        modeBar.style.display = 'block';
        setEditPortsMode('inplace');
        submitBtn.style.display = 'inline-block';
        submitBtn.textContent = 'Apply to .container & Restart Unit';

        try {
            const quadletDetails = await Podman.InspectQuadlet(currentEditProvenance.unitName || currentEditProvenance.name);
            if (quadletDetails && quadletDetails.exists) {
                fileInfo.style.display = 'block';
                setFileInfoLine(fileInfo, 'Unit File:', quadletDetails.filePath);
                if (quadletDetails.portMappings && quadletDetails.portMappings.length > 0) {
                    portMappings = quadletDetails.portMappings;
                }
            }
        } catch (e) {
            console.warn("Could not inspect quadlet file directly:", e);
        }

        // Prepare manual snippet
        currentEditSnippet = `[Container]\n${portMappings.map(m => `PublishPort=${m.hostIP || '127.0.0.1'}:${m.hostPort}:${m.containerPort}/${m.protocol || 'tcp'}`).join('\n')}`;
        snippetWrapper.style.display = 'block';
        snippetText.textContent = currentEditSnippet;
        guidanceText.textContent = `Update the PublishPort lines in your .container file and reload systemd ('systemctl --user daemon-reload && systemctl --user restart ${currentEditProvenance.unitName || 'service'}').`;
    } else if (currentEditProvenance.type === 'compose') {
        modeBar.style.display = 'block';
        setEditPortsMode('inplace');
        submitBtn.style.display = 'inline-block';
        submitBtn.textContent = 'Apply to compose.yml & Compose Up';

        try {
            const composeDetails = await Podman.InspectCompose(containerId);
            if (composeDetails && composeDetails.composeFile) {
                fileInfo.style.display = 'block';
                setFileInfoLine(fileInfo, 'Compose File:', composeDetails.composeFile, `service: ${composeDetails.service}`);
                if (composeDetails.portMappings && composeDetails.portMappings.length > 0) {
                    portMappings = composeDetails.portMappings;
                }
            }
        } catch (e) {
            console.warn("Could not inspect compose file directly:", e);
        }

        // Prepare manual snippet
        currentEditSnippet = `services:\n  ${currentEditProvenance.service || 'app'}:\n    ports:\n${portMappings.map(m => `      - "${m.hostIP || '127.0.0.1'}:${m.hostPort}:${m.containerPort}/${m.protocol || 'tcp'}"`).join('\n')}`;
        snippetWrapper.style.display = 'block';
        snippetText.textContent = currentEditSnippet;
        guidanceText.textContent = `Update the ports definition in your compose file and re-run 'pod up'.`;
    } else {
        submitBtn.style.display = 'inline-block';
        submitBtn.textContent = 'Mutate Ports (Atomic Transaction)';
    }

    if (portMappings && portMappings.length > 0) {
        portMappings.forEach(m => {
            editPortRows.push({
                id: nextEditPortRowId++,
                hostIP: m.hostIP || '127.0.0.1',
                hostPort: m.hostPort || '',
                containerPort: m.containerPort || '',
                protocol: (m.protocol || 'tcp').toLowerCase(),
                statusText: '',
                statusLevel: 'ok'
            });
        });
    } else {
        addEditPortRow();
    }
    renderEditPortRows();
    updateEditExposureWarning();

    openModal('edit-ports-modal');
};

window.addEditPortRow = (hostIP = '127.0.0.1', hostPort = '', containerPort = '', protocol = 'tcp') => {
    const rowId = nextEditPortRowId++;
    editPortRows.push({
        id: rowId,
        hostIP: hostIP,
        hostPort: hostPort,
        containerPort: containerPort,
        protocol: protocol,
        statusText: '',
        statusLevel: 'ok'
    });
    renderEditPortRows();
    updateEditExposureWarning();
};

window.removeEditPortRow = (id) => {
    editPortRows = editPortRows.filter(r => r.id !== id);
    renderEditPortRows();
    updateEditExposureWarning();
};

window.updateEditPortField = async (id, field, value) => {
    const row = editPortRows.find(r => r.id === id);
    if (!row) return;
    row[field] = value;

    const containerId = document.getElementById('edit-ports-container-id').value;
    const hPort = parseInt(row.hostPort, 10);
    const cPort = parseInt(row.containerPort, 10);
    if (hPort > 0 && cPort > 0) {
        try {
            const validation = await Podman.ValidatePortMapping({
                hostIP: row.hostIP,
                hostPort: hPort,
                containerPort: cPort,
                protocol: row.protocol,
                containerId: containerId
            });
            if (validation && !validation.valid) {
                const errCheck = validation.checks.find(c => !c.passed);
                row.statusText = errCheck ? errCheck.message : 'Port collision detected';
                row.statusLevel = 'error';
            } else {
                row.statusText = 'Available';
                row.statusLevel = 'ok';
            }
        } catch (e) {
            console.error("Port validation error:", e);
        }
    } else {
        row.statusText = '';
        row.statusLevel = 'ok';
    }

    renderEditPortRows();
    updateEditExposureWarning();
};

window.findFreePortForEditRow = async (id) => {
    const row = editPortRows.find(r => r.id === id);
    if (!row) return;
    try {
        showNotification("Searching for next available port...", false);
        const freePort = await Podman.FindFreePort(3000, row.protocol, row.hostIP);
        if (freePort > 0) {
            row.hostPort = freePort;
            row.statusText = `Free port suggested: ${freePort}`;
            row.statusLevel = 'ok';
            showNotification(`Found free port ${freePort}/${row.protocol.toUpperCase()}`, false, true);
            renderEditPortRows();
            updateEditExposureWarning();
        }
    } catch (err) {
        showNotification(`Could not find free port: ${err}`, true);
    }
};

function renderEditPortRows() {
    const container = document.getElementById('edit-ports-rows-container');
    if (!container) return;

    if (editPortRows.length === 0) {
        container.innerHTML = `
            <div class="empty-port-hint">
                No ports published. Click <strong>+ Add Port</strong> to configure port mappings.
            </div>
        `;
        return;
    }

    container.innerHTML = editPortRows.map(row => {
        return `
            <div class="port-input-row" id="edit-port-row-${row.id}">
                <div class="port-field-group" style="flex: 1.5;">
                    <span class="field-mini-label">Bind IP</span>
                    <input type="text" class="form-input" value="${escapeAttr(row.hostIP)}" placeholder="127.0.0.1" onchange="updateEditPortField(${row.id}, 'hostIP', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 1;">
                    <span class="field-mini-label">Host Port</span>
                    <input type="number" class="form-input" value="${escapeAttr(row.hostPort)}" placeholder="8080" min="1" max="65535" oninput="updateEditPortField(${row.id}, 'hostPort', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 1;">
                    <span class="field-mini-label">Target Port</span>
                    <input type="number" class="form-input" value="${escapeAttr(row.containerPort)}" placeholder="80" min="1" max="65535" oninput="updateEditPortField(${row.id}, 'containerPort', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 0.8;">
                    <span class="field-mini-label">Proto</span>
                    <select class="form-input" onchange="updateEditPortField(${row.id}, 'protocol', this.value)">
                        <option value="tcp" ${row.protocol === 'tcp' ? 'selected' : ''}>TCP</option>
                        <option value="udp" ${row.protocol === 'udp' ? 'selected' : ''}>UDP</option>
                    </select>
                </div>
                <div style="display: flex; gap: 4px; align-items: flex-end; padding-bottom: 2px;">
                    <button type="button" class="btn btn-secondary btn-xs" onclick="findFreePortForEditRow(${row.id})" title="Suggest next free port">
                        Auto Free
                    </button>
                    <button type="button" class="btn btn-danger btn-xs" onclick="removeEditPortRow(${row.id})" title="Remove port mapping">
                        &times;
                    </button>
                </div>
            </div>
            ${row.statusText ? `
                <div class="row-validation-msg ${escapeAttr(row.statusLevel)}">${escapeHtml(row.statusText)}</div>
            ` : ''}
        `;
    }).join('');
}

function updateEditExposureWarning() {
    const warningBox = document.getElementById('edit-ports-exposure-warning');
    if (!warningBox) return;

    const hasWildcard = editPortRows.some(r => {
        const bind = (r.hostIP || '').trim();
        return bind === '0.0.0.0' || bind === '::' || bind === '*' || bind === '';
    });

    if (hasWildcard) {
        warningBox.style.display = 'block';
        warningBox.innerHTML = `
            <div class="warning-title">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
                Wildcard / External Network Exposure
            </div>
            <div>One or more mappings use <code>0.0.0.0</code> (all interfaces). Use <code>127.0.0.1</code> for local host access.</div>
        `;
    } else {
        warningBox.style.display = 'none';
        warningBox.innerHTML = '';
    }
}

// Renders "<label> <code>path</code> (extra)" via safe DOM construction —
// path is a filesystem path derived from container/Compose labels and must
// never be interpolated into innerHTML.
function setFileInfoLine(el, label, path, extra) {
    el.textContent = '';
    const strong = document.createElement('strong');
    strong.textContent = label + ' ';
    const code = document.createElement('code');
    code.textContent = path || '';
    el.appendChild(strong);
    el.appendChild(code);
    if (extra) {
        el.appendChild(document.createTextNode(' (' + extra + ')'));
    }
}

window.copyEditPortsSnippet = () => {
    if (currentEditSnippet) {
        copyText(currentEditSnippet, "Configuration snippet copied!");
    }
};

window.submitPortMutation = async () => {
    const containerId = document.getElementById('edit-ports-container-id').value;
    const serviceName = document.getElementById('edit-ports-service-name').value;
    const unitName = document.getElementById('edit-ports-unit-name').value;
    const isQuadlet = currentEditProvenance && currentEditProvenance.type === 'quadlet';
    const isCompose = currentEditProvenance && currentEditProvenance.type === 'compose';

    const structuredPorts = [];
    for (const row of editPortRows) {
        const hPort = parseInt(row.hostPort, 10);
        const cPort = parseInt(row.containerPort, 10);
        if (hPort > 0 && cPort > 0) {
            structuredPorts.push({
                hostIP: row.hostIP.trim(),
                hostPort: hPort,
                containerPort: cPort,
                protocol: (row.protocol || 'tcp').toLowerCase()
            });
        }
    }

    const submitBtn = document.getElementById('btn-submit-port-mutation');
    const originalText = submitBtn.innerHTML;

    try {
        submitBtn.disabled = true;
        submitBtn.innerHTML = `
            <svg class="animate-spin" style="animation: spin 1s linear infinite;" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="12" cy="12" r="10"/><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83"/></svg>
            Executing Mutation...
        `;

        const stepsBox = document.getElementById('edit-ports-steps-box');
        const stepsList = document.getElementById('edit-ports-steps-list');
        stepsBox.style.display = 'block';
        stepsList.innerHTML = `<div style="color: var(--text-muted);">Starting mutation transaction...</div>`;

        let res = null;

        if (isQuadlet) {
            res = await Podman.MutateQuadletPorts(unitName || serviceName, structuredPorts);
        } else if (isCompose) {
            res = await Podman.MutateComposePorts(containerId, structuredPorts);
        } else {
            res = await Podman.MutateContainerPorts({
                containerId: containerId,
                serviceName: serviceName,
                newPorts: structuredPorts
            });
        }

        if (!res) {
            showNotification("Mutation failed: Empty response from engine", true);
            return;
        }

        // Render steps log. Step/message text originates from the backend
        // transaction, but may echo container/file identifiers derived
        // from labels — escape defensively rather than trust it.
        if (res.steps && res.steps.length > 0) {
            stepsList.innerHTML = res.steps.map(s => {
                const color = s.passed ? 'var(--color-emerald)' : 'var(--color-rose)';
                const icon = s.passed ? '&check;' : '&times;';
                return `<div style="color: ${color};"><strong>${icon} [${escapeHtml(s.step)}]:</strong> ${escapeHtml(s.message)}</div>`;
            }).join('');
        }

        if (res.requiresExternal) {
            const guidanceBox = document.getElementById('edit-ports-guidance-box');
            const guidanceText = document.getElementById('edit-ports-guidance-text');
            const snippetWrapper = document.getElementById('edit-ports-snippet-wrapper');
            const snippetText = document.getElementById('edit-ports-snippet-text');

            guidanceBox.style.display = 'block';
            guidanceText.textContent = res.guidance || 'External orchestrator action required.';
            
            if (res.composeSnippet || res.quadletSnippet) {
                currentEditSnippet = res.composeSnippet || res.quadletSnippet;
                snippetWrapper.style.display = 'block';
                snippetText.textContent = currentEditSnippet;
            }
            showNotification("Orchestrator action required (see guidance above)", false, true);
            return;
        }

        if (res.success) {
            showNotification("Port mappings mutated successfully!", false, true);
            setTimeout(() => {
                closeModal('edit-ports-modal');
                if (currentTab === 'containers') loadContainers();
                if (currentTab === 'ports') loadPorts();
            }, 1200);
        } else if (res.manualRecoveryRequired) {
            showNotification(`ROLLBACK FAILED / MANUAL RECOVERY REQUIRED: ${res.rollbackReason || 'see transaction log above'}`, true);
        } else if (res.rolledBack) {
            showNotification(`Mutation aborted & rolled back: ${res.rollbackReason}`, true);
        } else {
            showNotification("Port mutation could not be completed.", true);
        }
    } catch (err) {
        showNotification(`Mutation Error: ${err}`, true);
    } finally {
        submitBtn.disabled = false;
        submitBtn.innerHTML = originalText;
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

window.viewContainerSpec = async (serviceName) => {
    try {
        showNotification(`Fetching declarative spec for ${serviceName}...`, false);
        const spec = await Podman.GetSpec(serviceName);
        if (!spec) {
            showNotification(`No declarative spec found for service: ${serviceName}`, true);
            return;
        }
        document.getElementById('spec-modal-title').textContent = `Declarative Spec: ${serviceName}`;
        document.getElementById('spec-text').textContent = JSON.stringify(withMaskedSecrets(spec), null, 2);
        openModal('spec-modal');
        showNotification("Spec loaded", false, true);
    } catch (err) {
        showNotification(`Failed to load spec: ${err}`, true);
    }
};

// --- Workload Adoption Handlers ---

window.openAdoptModal = async (containerId, containerName) => {
    document.getElementById('adopt-container-id').value = containerId;
    document.getElementById('adopt-modal-title').textContent = `Adopt Workload: ${containerName}`;
    document.getElementById('adopt-service-name').value = containerName;

    const loadingDiv = document.getElementById('adopt-assessment-loading');
    const contentDiv = document.getElementById('adopt-assessment-content');
    const warningsBox = document.getElementById('adopt-warnings-box');
    const previewPre = document.getElementById('adopt-spec-preview');
    const submitBtn = document.getElementById('btn-submit-adopt');

    loadingDiv.style.display = 'block';
    contentDiv.style.display = 'none';
    warningsBox.style.display = 'none';
    submitBtn.disabled = true;

    openModal('adopt-modal');

    try {
        const assessment = await Podman.InspectContainerForAdoption(containerId);
        loadingDiv.style.display = 'none';

        if (!assessment) {
            showNotification("Failed to inspect container for adoption", true);
            closeModal('adopt-modal');
            return;
        }

        contentDiv.style.display = 'block';
        previewPre.textContent = JSON.stringify(withMaskedSecrets(assessment.proposedSpec), null, 2);

        if (!assessment.canAdopt) {
            warningsBox.style.display = 'block';
            warningsBox.className = 'exposure-warning-banner';
            warningsBox.innerHTML = `<strong>Adoption Blocked:</strong> ${escapeHtml((assessment.blockers || []).join(' '))}`;
            submitBtn.disabled = true;
        } else {
            submitBtn.disabled = false;
            if (assessment.warnings && assessment.warnings.length > 0) {
                warningsBox.style.display = 'block';
                warningsBox.className = 'exposure-warning-banner';
                warningsBox.innerHTML = `<strong>Adoption Notes:</strong><ul style="margin: 4px 0 0 16px; padding: 0;">${assessment.warnings.map(w => `<li>${escapeHtml(w)}</li>`).join('')}</ul>`;
            } else {
                warningsBox.style.display = 'none';
            }
        }
    } catch (err) {
        loadingDiv.style.display = 'none';
        showNotification(`Adoption Inspection Error: ${err}`, true);
        closeModal('adopt-modal');
    }
};

window.submitAdoption = async () => {
    const containerId = document.getElementById('adopt-container-id').value;
    const serviceName = document.getElementById('adopt-service-name').value.trim();

    if (!serviceName) {
        showNotification("Please provide a valid service name.", true);
        return;
    }

    const submitBtn = document.getElementById('btn-submit-adopt');
    const originalText = submitBtn.innerHTML;

    try {
        submitBtn.disabled = true;
        submitBtn.innerHTML = `
            <svg class="animate-spin" style="animation: spin 1s linear infinite;" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="12" cy="12" r="10"/><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83"/></svg>
            Adopting...
        `;

        const res = await Podman.AdoptContainer(containerId, serviceName);
        if (res && res.success) {
            showNotification(res.message || "Workload adopted into Podder!", false, true);
            closeModal('adopt-modal');
            loadContainers();
        } else {
            showNotification("Adoption failed.", true);
        }
    } catch (err) {
        showNotification(`Adoption Error: ${err}`, true);
    } finally {
        submitBtn.disabled = false;
        submitBtn.innerHTML = originalText;
    }
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
    
    banner.className = "notification-banner active";
    
    if (isError) {
        banner.classList.add('error');
        icon.innerHTML = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#f43f5e" stroke-width="2.5"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>`;
    } else if (isSuccess) {
        banner.classList.add('success');
        icon.innerHTML = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.5"><polyline points="20 6 9 17 4 12"/></svg>`;
    } else {
        icon.innerHTML = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#6366f1" stroke-width="2.5" style="animation: spin 1s linear infinite;"><circle cx="12" cy="12" r="10"/><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83"/></svg>`;
    }
    
    text.textContent = message;
    
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

    const saveSpecCheckbox = document.getElementById('run-save-spec');
    if (saveSpecCheckbox) {
        saveSpecCheckbox.checked = true;
    }

    runPortRows = [];
    nextPortRowId = 1;
    renderRunPortRows();
    updateRunExposureWarning();
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

// --- Networks Tab View & Handlers ---

let allNetworks = [];

window.loadNetworks = async () => {
    const listContainer = document.getElementById('networks-list');
    if (!listContainer) return;

    try {
        listContainer.innerHTML = '<div style="grid-column: 1 / -1; text-align: center; padding: 40px; color: var(--text-muted);">Discovering Podman networks & IPAM allocations...</div>';

        allNetworks = await Podman.ListNetworks() || [];

        // Update stats
        const totalNets = allNetworks.length;
        const bridgeNets = allNetworks.filter(n => n.driver === 'bridge').length;
        const internalNets = allNetworks.filter(n => n.internal).length;
        const totalEndpoints = allNetworks.reduce((acc, n) => acc + (n.connectedContainers ? n.connectedContainers.length : 0), 0);

        const statTotal = document.getElementById('stat-networks-total');
        const statBridge = document.getElementById('stat-networks-bridge');
        const statInternal = document.getElementById('stat-networks-internal');
        const statEndpoints = document.getElementById('stat-networks-endpoints');

        if (statTotal) statTotal.textContent = totalNets;
        if (statBridge) statBridge.textContent = bridgeNets;
        if (statInternal) statInternal.textContent = internalNets;
        if (statEndpoints) statEndpoints.textContent = totalEndpoints;

        renderNetworksList(allNetworks);
    } catch (err) {
        showNotification(`Failed to load networks: ${err}`, true);
        listContainer.innerHTML = `<div style="grid-column: 1 / -1; text-align: center; padding: 40px; color: var(--color-rose);">Error loading networks: ${escapeHtml(String(err))}</div>`;
    }
};

window.renderNetworksList = (networks) => {
    const listContainer = document.getElementById('networks-list');
    if (!listContainer) return;

    if (!networks || networks.length === 0) {
        listContainer.innerHTML = `
            <div style="grid-column: 1 / -1; text-align: center; padding: 40px; color: var(--text-muted);">
                No Podman networks found. Click <strong>Create Network</strong> to define one.
            </div>
        `;
        return;
    }

    listContainer.innerHTML = networks.map(net => {
        const isDefault = (net.name === 'podman' || net.name === 'default');
        const subnetsHtml = (net.subnets && net.subnets.length > 0)
            ? net.subnets.map(s => `<code>${escapeHtml(s.subnet)}</code>${s.gateway ? ` <span style="color: var(--text-muted); font-size: 11px;">(gw: ${escapeHtml(s.gateway)})</span>` : ''}`).join(', ')
            : '<span style="color: var(--text-muted);">Auto / None</span>';

        const endpointsHtml = (net.connectedContainers && net.connectedContainers.length > 0)
            ? net.connectedContainers.map(c => `
                <div style="display: flex; justify-content: space-between; align-items: center; padding: 4px 8px; background: rgba(255, 255, 255, 0.03); border-radius: var(--radius-sm); font-size: 12px; margin-top: 4px;">
                    <span style="font-weight: 500; color: var(--text-main);">${escapeHtml(c.name)}</span>
                    <span style="font-family: var(--font-mono); color: #6ee7b7;">${escapeHtml(c.ipv4Address || c.ipv6Address || '-')}</span>
                </div>
            `).join('')
            : '<div style="font-size: 12px; color: var(--text-muted); font-style: italic; padding: 4px 0;">No active endpoints</div>';

        return `
            <div class="card">
                <div class="card-header">
                    <div>
                        <div style="display: flex; align-items: center; gap: 8px;">
                            <span class="card-title">${escapeHtml(net.name)}</span>
                            <span class="prov-badge ${net.driver === 'bridge' ? 'podder' : 'adhoc'}">${escapeHtml(net.driver || 'bridge')}</span>
                            ${isDefault ? `<span class="prov-badge quadlet" style="font-size: 10px;">DEFAULT</span>` : ''}
                        </div>
                        <div class="card-subtitle">ID: ${escapeHtml((net.id || '').substring(0, 12))}</div>
                    </div>
                    <div style="display: flex; gap: 4px;">
                        ${!isDefault ? `
                            <button class="btn btn-danger btn-icon" onclick="removeNetwork('${jsStringLiteral(net.name)}')" title="Remove Network">
                                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
                            </button>
                        ` : ''}
                    </div>
                </div>

                <div class="card-details">
                    <div class="card-detail-item">
                        <span class="card-detail-label">Subnets & Gateways</span>
                        <span class="card-detail-value" style="font-family: var(--font-mono); font-size: 12px;">${subnetsHtml}</span>
                    </div>
                    <div class="card-detail-item">
                        <span class="card-detail-label">Features</span>
                        <div style="display: flex; gap: 6px; flex-wrap: wrap;">
                            <span style="font-size: 11px; padding: 2px 6px; border-radius: 4px; background: ${net.dnsEnabled ? 'rgba(16, 185, 129, 0.15)' : 'rgba(255, 255, 255, 0.05)'}; color: ${net.dnsEnabled ? '#6ee7b7' : 'var(--text-muted)'};">
                                DNS ${net.dnsEnabled ? 'Enabled' : 'Disabled'}
                            </span>
                            <span style="font-size: 11px; padding: 2px 6px; border-radius: 4px; background: ${net.internal ? 'rgba(139, 92, 246, 0.15)' : 'rgba(255, 255, 255, 0.05)'}; color: ${net.internal ? '#c4b5fd' : 'var(--text-muted)'};">
                                ${net.internal ? 'Internal / Isolated' : 'External Access'}
                            </span>
                            ${net.ipv6Enabled ? `
                                <span style="font-size: 11px; padding: 2px 6px; border-radius: 4px; background: rgba(56, 189, 248, 0.15); color: #7dd3fc;">
                                    IPv6
                                </span>
                            ` : ''}
                        </div>
                    </div>
                    <div class="card-detail-item" style="flex-direction: column; align-items: stretch;">
                        <span class="card-detail-label" style="margin-bottom: 2px;">Connected Containers (${net.connectedContainers ? net.connectedContainers.length : 0})</span>
                        ${endpointsHtml}
                    </div>
                </div>
            </div>
        `;
    }).join('');
};

window.handleNetworkSearch = () => {
    const term = (document.getElementById('network-search-input')?.value || '').toLowerCase().trim();
    if (!term) {
        renderNetworksList(allNetworks);
        return;
    }

    const filtered = allNetworks.filter(net => {
        const name = (net.name || '').toLowerCase();
        const driver = (net.driver || '').toLowerCase();
        const subnets = (net.subnets || []).map(s => s.subnet + ' ' + (s.gateway || '')).join(' ').toLowerCase();
        const containers = (net.connectedContainers || []).map(c => c.name + ' ' + (c.ipv4Address || '')).join(' ').toLowerCase();
        return name.includes(term) || driver.includes(term) || subnets.includes(term) || containers.includes(term);
    });

    renderNetworksList(filtered);
};

window.openCreateNetworkModal = () => {
    document.getElementById('net-create-name').value = '';
    document.getElementById('net-create-driver').value = 'bridge';
    document.getElementById('net-create-subnet').value = '';
    document.getElementById('net-create-gateway').value = '';
    document.getElementById('net-create-dns').checked = true;
    document.getElementById('net-create-internal').checked = false;
    openModal('create-network-modal');
};

window.submitCreateNetwork = async () => {
    const name = document.getElementById('net-create-name').value.trim();
    const driver = document.getElementById('net-create-driver').value;
    const subnet = document.getElementById('net-create-subnet').value.trim();
    const gateway = document.getElementById('net-create-gateway').value.trim();
    const dns = document.getElementById('net-create-dns').checked;
    const internal = document.getElementById('net-create-internal').checked;

    if (!name) {
        showNotification("Please specify a network name.", true);
        return;
    }

    const submitBtn = document.getElementById('btn-submit-create-network');
    const originalText = submitBtn.innerHTML;

    try {
        submitBtn.disabled = true;
        submitBtn.innerHTML = `Creating...`;
        await Podman.CreateNetwork(name, driver, subnet, gateway, internal, dns);
        showNotification(`Network '${name}' created successfully!`, false, true);
        closeModal('create-network-modal');
        loadNetworks();
    } catch (err) {
        showNotification(`Failed to create network: ${err}`, true);
    } finally {
        submitBtn.disabled = false;
        submitBtn.innerHTML = originalText;
    }
};

window.removeNetwork = async (name) => {
    if (!confirm(`Are you sure you want to remove network '${name}'?`)) {
        return;
    }
    try {
        showNotification(`Removing network ${name}...`, false);
        await Podman.RemoveNetwork(name);
        showNotification(`Network '${name}' removed successfully!`, false, true);
        loadNetworks();
    } catch (err) {
        showNotification(`Failed to remove network: ${err}`, true);
    }
};
