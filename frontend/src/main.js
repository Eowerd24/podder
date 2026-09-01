import * as Podman from "../bindings/github.com/Eowerd24/podder/podmanservice.js";
import {
    escapeHtml,
    escapeAttr,
    jsStringLiteral,
    withMaskedSecrets,
    describeBindAddress,
    formatPortRangeSuffix,
    RECONCILIATION_STATUS_META,
    STATUS_PILL_CLASS_FOR_RECONCILE_CLASS,
} from "./trust.js";

// Trust-boundary escaping/masking helpers (escapeHtml, escapeAttr,
// jsStringLiteral, withMaskedSecrets, ...) live in
// ./trust.js -- a DOM-free module kept separate specifically so it can be
// unit tested with a plain Node test runner (see trust.test.js) without
// needing a full browser/DOM environment. Podman labels, container/image
// names, Compose project/service metadata, registry YAML strings, process
// names, and network names all originate outside Podder's control and must
// never be rendered as raw HTML -- prefer textContent/dataset/
// addEventListener for dynamic content; where a template literal must
// interpolate untrusted text into markup, always pass it through
// escapeHtml first.

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
    
    // Auto-refresh every 5 seconds. Reuses refreshAll()'s own per-tab
    // dispatch (the same one used on tab switch) instead of a separate,
    // easily-out-of-sync list of tabs, so every tab that refreshAll()
    // supports gets live updates here too.
    setInterval(() => {
        refreshAll();
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
    const tabIndexMap = { 'dashboard': 0, 'containers': 1, 'images': 2, 'ports': 3, 'networks': 4 };
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
                            const bindInfo = describeBindAddress(m.hostIP);
                            const proto = (m.protocol || 'tcp').toUpperCase();
                            const isLoopback = bindInfo.category === 'loopback4' || bindInfo.category === 'loopback6';
                            const isWildcard = bindInfo.category === 'wildcard4' || bindInfo.category === 'wildcard6' || bindInfo.category === 'default';
                            const expClass = isLoopback ? 'exp-loopback' : (isWildcard ? 'exp-wildcard' : 'exp-specific');
                            const hostSpan = formatPortRangeSuffix(m.hostPort, m.rangeSize);
                            const containerSpan = formatPortRangeSuffix(m.containerPort, m.rangeSize);
                            return `
                                <div class="port-badge-row">
                                    <span class="port-proto-tag ${proto.toLowerCase() === 'udp' ? 'udp' : 'tcp'}">${escapeHtml(proto)}</span>
                                    <span class="port-mapping-text ${expClass}" title="${escapeAttr(bindInfo.detail)}">${escapeHtml(bindInfo.display)}:${escapeHtml(hostSpan)} &rarr; ${escapeHtml(containerSpan)}</span>
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

            // Name/id are untrusted (container labels/user-chosen names):
            // encode as safe single-quoted JS string arguments — see
            // jsStringLiteral. The port editor no longer accepts
            // caller-supplied provenance/mappings at all (it always
            // refetches fresh state by ID), so nothing here needs
            // JSON-object encoding any more.
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
                        <button class="btn btn-secondary btn-icon" onclick="openEditPortsModal('${idArg}', '${nameArg}')" title="Edit Port Mappings">
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

    const warnings = summary.registryWarnings || [];
    const hasWarnings = warnings.length > 0;

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
            <span class="reg-metric drift" title="Lifecycle-aware problems: a deprecated/retired declaration still running, a declared/observed bind mismatch, or an endpoint occupied by an unexpected workload owner">
                ${summary.registryDrift || 0} Drift
            </span>
            <button class="btn btn-secondary btn-xs" onclick="openSettingsModal()" style="margin-left: 8px;">Config</button>
        </div>
    `;

    // Registry loaded cleanly vs loaded for observation with invalid
    // entries are two different operator-facing states: the latter still
    // populates this display (tolerant), but create/mutate/adopt/free-port
    // selection are BLOCKED until the invalid entries are fixed (see
    // LoadPortRegistryStrict). This must never look like an ordinary
    // metric -- it is a safety notice.
    if (hasWarnings) {
        const notice = document.createElement('div');
        notice.className = 'reg-summary-item';
        notice.style.color = 'var(--color-amber, #f59e0b)';
        notice.style.width = '100%';
        notice.title = warnings.join('\n');
        notice.textContent = `Registry loaded for observation with ${warnings.length} invalid ${warnings.length === 1 ? 'entry' : 'entries'}. Safety-sensitive operations (create/mutate/adopt/free-port selection) are blocked until fixed.`;
        bar.appendChild(notice);
    }
}

function renderPortItems() {
    const container = document.getElementById('ports-list');
    if (!container || !cachedPortOverview) return;

    let items = cachedPortOverview.items || [];

    // Filter by source / category. "Registry" means ANY row associated
    // with a registry record -- not just an ordinary MATCH -- so a
    // mismatch/drift/lifecycle row is never invisible merely because it
    // isn't a clean match. registryId is set on every reconciled runtime
    // row (match, bind mismatch, owner mismatch/unknown, ...) and on every
    // registry-declared standalone row, so this is the same property the
    // backend already treats as authoritative for "this row is
    // registry-associated" -- see PortOverviewItem.RegistryID.
    if (currentPortFilter === 'podman') {
        items = items.filter(i => i.source === 'podman');
    } else if (currentPortFilter === 'host') {
        items = items.filter(i => i.source === 'host-listener');
    } else if (currentPortFilter === 'registry') {
        items = items.filter(i => !!i.registryId);
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
                        const bindInfo = describeBindAddress(item.bindAddress);
                        const hostSpan = formatPortRangeSuffix(item.hostPort, item.rangeSize);
                        const endpointStr = `${bindInfo.display}:${hostSpan}`;
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
                        } else if (scopeVal === 'public') {
                            expBadge = `<span class="exp-badge wildcard">PUBLIC</span>`;
                        } else if (scopeVal === 'cluster') {
                            expBadge = `<span class="exp-badge wildcard">CLUSTER</span>`;
                        }

                        let statusBadge = `<span class="status-pill active">ACTIVE</span>`;
                        if (item.status === 'CONFLICT') {
                            statusBadge = `<span class="status-pill conflict" title="${escapeAttr(item.conflictNote || 'Conflict')}">CONFLICT</span>`;
                        } else if (item.status === 'STOPPED_CONFIGURED') {
                            statusBadge = `<span class="status-pill stopped">CONFIGURED</span>`;
                        } else {
                            // Every other item.status value used for a
                            // standalone registry-declared row shares the
                            // exact same string as reconciliationStatus
                            // (see GetPortOverview's unmatched-declared
                            // loop) -- render it from the same explicit
                            // lifecycle table rather than falling back to a
                            // generic/blank "ACTIVE" badge for any status
                            // this block doesn't individually enumerate.
                            const statusMeta = RECONCILIATION_STATUS_META[item.status];
                            if (statusMeta) {
                                const cls = STATUS_PILL_CLASS_FOR_RECONCILE_CLASS[statusMeta.cls] || 'stopped';
                                statusBadge = `<span class="status-pill ${cls}" title="${escapeAttr(item.conflictNote || statusMeta.tooltip)}">${escapeHtml(statusMeta.label)}</span>`;
                            }
                        }

                        // Reconciliation badge -- covers EVERY lifecycle
                        // reconciliation status the backend can report (see
                        // RECONCILIATION_STATUS_META above); a status this
                        // table doesn't recognize falls back to a generic
                        // HOST pill rather than silently disappearing.
                        let reconcileBadge = '';
                        if (hasRegistry) {
                            const rec = item.reconciliationStatus || 'UNDECLARED';
                            const meta = RECONCILIATION_STATUS_META[rec];
                            if (meta) {
                                const tooltip = item.conflictNote || meta.tooltip;
                                reconcileBadge = `<span class="reconcile-pill ${meta.cls}" title="${escapeAttr(tooltip)}">${escapeHtml(meta.label)}</span>`;
                            } else {
                                reconcileBadge = `<span class="reconcile-pill host">HOST</span>`;
                            }
                        }

                        // Provenance pill for table
                        let provBadge = '';
                        if (item.provenance && item.provenance.type) {
                            provBadge = `<span class="prov-mini-badge ${escapeAttr(item.provenance.type)}" title="${escapeAttr(item.provenance.guidance || '')}">${escapeHtml(item.provenance.displayType)}</span>`;
                        }

                        const targetSpan = formatPortRangeSuffix(item.containerPort, item.rangeSize);
                        const targetStr = item.isContainer && item.containerPort ? `${targetSpan}/${proto}` : '&mdash;';
                        const isHttpCandidate = proto === 'TCP' && (scopeVal === 'loopback' || scopeVal === 'specific-ip' || scopeVal === 'wildcard' || scopeVal === 'lan') && (!item.rangeSize || item.rangeSize <= 1);
                        const urlHost = (bindInfo.category === 'wildcard4' || bindInfo.category === 'wildcard6' || bindInfo.category === 'default') ? 'localhost' : bindInfo.display;
                        const candidateUrl = `http://${urlHost}:${item.hostPort}`;

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
                                            <button class="btn btn-secondary btn-xs" onclick="openEditPortsModal('${containerIdArg}', '${ownerArg}')" title="Edit Container Ports">
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

// Compose Trusted Roots state -- an operator-approved allowlist of
// filesystem roots Podder is permitted to automatically read a Compose
// project file from. A container's own working_dir/config_files labels are
// never, by themselves, treated as authorization -- see
// AppSettings.ComposeTrustedRoots / FindComposeFile.
let composeTrustedRootRows = [];
let nextComposeTrustedRootRowId = 1;

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

        const trustedRoots = (settings && Array.isArray(settings.composeTrustedRoots)) ? settings.composeTrustedRoots : [];
        composeTrustedRootRows = trustedRoots.map(root => ({ id: nextComposeTrustedRootRowId++, path: root }));
        renderComposeTrustedRootRows();

        openModal('settings-modal');
    } catch (err) {
        showNotification(`Failed to read settings: ${err}`, true);
    }
};

function renderComposeTrustedRootRows() {
    const container = document.getElementById('compose-trusted-roots-list');
    if (!container) return;

    if (composeTrustedRootRows.length === 0) {
        container.innerHTML = `<div class="empty-port-hint">No trusted roots configured. Automatic Compose file reading is disabled.</div>`;
        return;
    }

    container.innerHTML = composeTrustedRootRows.map(row => `
        <div class="port-input-row" id="compose-root-row-${row.id}">
            <div class="port-field-group" style="flex: 4;">
                <input type="text" class="form-input" value="${escapeAttr(row.path)}" placeholder="/home/user/projects" onchange="updateComposeTrustedRootRow(${row.id}, this.value)"/>
            </div>
            <div style="display: flex; gap: 4px; align-items: center;">
                <button type="button" class="btn btn-danger btn-xs" onclick="removeComposeTrustedRootRow(${row.id})" title="Remove trusted root">&times;</button>
            </div>
        </div>
    `).join('');
}

window.addComposeTrustedRootRow = (path = '') => {
    composeTrustedRootRows.push({ id: nextComposeTrustedRootRowId++, path });
    renderComposeTrustedRootRows();
};

window.removeComposeTrustedRootRow = (id) => {
    composeTrustedRootRows = composeTrustedRootRows.filter(r => r.id !== id);
    renderComposeTrustedRootRows();
};

window.updateComposeTrustedRootRow = (id, value) => {
    const row = composeTrustedRootRows.find(r => r.id === id);
    if (row) row.path = value;
};

window.pickComposeTrustedRoot = async () => {
    try {
        const selected = await Podman.SelectHostPath('folder');
        if (selected) {
            addComposeTrustedRootRow(selected);
        }
    } catch (err) {
        showNotification(`Error selecting folder: ${err}`, true);
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
    const trustedRoots = composeTrustedRootRows
        .map(r => (r.path || '').trim())
        .filter(p => p !== '');

    const settings = {
        portRegistry: {
            enabled: enabled,
            path: path
        },
        composeTrustedRoots: trustedRoots
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
    if (!confirm("Are you sure you want to remove this container? If it's running, it will be stopped first.")) return;
    try {
        showNotification("Removing container...", false);
        // Gracefully stop (if running) then remove, rather than forcibly
        // killing whatever the container is doing mid-operation. Internal
        // transaction cleanup (rollback, candidate cleanup) is the only
        // caller that still needs the unconditional force-remove semantic.
        await Podman.StopAndRemoveContainer(id);
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

window.addRunPortRow = (hostIP = '', hostPort = '', containerPort = '', protocol = 'tcp', rangeSize = 1) => {
    const rowId = nextPortRowId++;
    runPortRows.push({
        id: rowId,
        hostIP: hostIP,
        hostPort: hostPort,
        containerPort: containerPort,
        protocol: protocol,
        rangeSize: rangeSize,
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
    const rSize = parseInt(row.rangeSize, 10) || 1;
    if (hPort > 0 && cPort > 0) {
        try {
            const validation = await Podman.ValidatePortMapping({
                hostIP: row.hostIP,
                hostPort: hPort,
                containerPort: cPort,
                protocol: row.protocol,
                rangeSize: rSize > 1 ? rSize : 0
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

    updateRunPortRowStatus(id);
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
        const rangeSize = parseInt(row.rangeSize, 10) || 1;
        const rangePreview = rangeSize > 1 ? `<div class="row-validation-msg ok" style="margin-top: 2px;">Range: ${escapeHtml(formatPortRangeSuffix(row.hostPort || 0, rangeSize))} &rarr; ${escapeHtml(formatPortRangeSuffix(row.containerPort || 0, rangeSize))}</div>` : '';
        return `
            <div class="port-input-row" id="port-row-${row.id}">
                <div class="port-field-group" style="flex: 1.5;">
                    <span class="field-mini-label">Bind IP</span>
                    <input type="text" class="form-input" value="${escapeAttr(row.hostIP)}" placeholder="blank = default" onchange="updateRunPortField(${row.id}, 'hostIP', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 1;">
                    <span class="field-mini-label">Host Port</span>
                    <input type="number" class="form-input" value="${escapeAttr(row.hostPort)}" placeholder="${isRunSaveAsManagedChecked() ? '8080 (required)' : '8080 (blank = auto-assign)'}" min="1" max="65535" oninput="updateRunPortField(${row.id}, 'hostPort', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 1;">
                    <span class="field-mini-label">Target Port</span>
                    <input type="number" class="form-input" value="${escapeAttr(row.containerPort)}" placeholder="80" min="1" max="65535" oninput="updateRunPortField(${row.id}, 'containerPort', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 0.7;">
                    <span class="field-mini-label" title="Number of consecutive ports in this mapping, starting at Host/Target Port">Count / Range Size</span>
                    <input type="number" class="form-input" value="${escapeAttr(rangeSize)}" min="1" max="65535" oninput="updateRunPortField(${row.id}, 'rangeSize', this.value)"/>
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
            ${rangePreview}
            <div class="row-validation-msg ${escapeAttr(row.statusLevel)}" id="port-status-${row.id}"${row.statusText ? '' : ' style="display:none;"'}>${escapeHtml(row.statusText)}</div>
        `;
    }).join('');
}

// isRunSaveAsManagedChecked reports whether the Run modal's "save as
// Podder-managed" checkbox is currently checked. A managed workload
// requires an explicit, stable host port (no unpredictable auto-assigned
// endpoint), while an unmanaged/ad-hoc container may leave Host Port blank
// to let Podman auto-assign one — the field's placeholder reflects whichever
// policy currently applies.
function isRunSaveAsManagedChecked() {
    const cb = document.getElementById('run-save-spec');
    return !!(cb && cb.checked);
}

// updateRunPortRowStatus updates a single row's validation message in place,
// without touching any <input> element — unlike renderRunPortRows(), which
// replaces the whole row list's innerHTML and would destroy (and drop focus
// from) whichever field the user is actively typing in.
function updateRunPortRowStatus(id) {
    const row = runPortRows.find(r => r.id === id);
    if (!row) return;
    const el = document.getElementById(`port-status-${id}`);
    if (!el) return;
    el.className = `row-validation-msg ${row.statusLevel || 'ok'}`;
    el.textContent = row.statusText || '';
    el.style.display = row.statusText ? '' : 'none';
}

function updateRunExposureWarning() {
    const warningBox = document.getElementById('run-exposure-warning');
    if (!warningBox) return;

    const hasWildcard = runPortRows.some(r => {
        const cat = describeBindAddress(r.hostIP).category;
        return cat === 'wildcard4' || cat === 'wildcard6' || cat === 'default';
    });

    if (hasWildcard) {
        warningBox.style.display = 'block';
        warningBox.innerHTML = `
            <div class="warning-title">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
                Wildcard / All Local Interfaces
            </div>
            <div>One or more mappings bind all local interfaces (an omitted address, <code>0.0.0.0</code>, or <code>::</code>) — reachable from any host that can route to this machine, subject to firewall rules. This is not necessarily Internet-public. Use <code>127.0.0.1</code> if you only need local access.</div>
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
        const rSize = parseInt(row.rangeSize, 10);
        if (cPort > 0) {
            structuredPortMappings.push({
                hostIP: row.hostIP.trim(),
                hostPort: isNaN(hPort) ? 0 : hPort,
                containerPort: cPort,
                protocol: (row.protocol || 'tcp').toLowerCase(),
                rangeSize: (!isNaN(rSize) && rSize > 1) ? rSize : 0
            });
        }
    }

    // A Podder-managed workload must name an explicit, stable host port —
    // an unpredictable Podman-auto-assigned endpoint is not appropriate for
    // a declarative managed service. The backend enforces this too; this
    // check just surfaces it before a round trip.
    if (managed && structuredPortMappings.some(m => !m.hostPort)) {
        showNotification("Every port mapping needs an explicit Host Port to save this as a Podder-managed workload (auto-assigned ports are only supported for unmanaged containers).", true);
        return;
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
//
// There is deliberately only ONE way to populate this modal's editable
// state: a fresh Podman.GetContainerPortEditState(containerId) call, made
// here every time the modal opens. Earlier revisions accepted cached
// provenance/portMappings from the caller (the container-card action always
// passed the full list; the Ports-tab action passed an empty array), which
// meant the SAME container could show either its real published ports or an
// empty, silently-misleading editor depending on which button was clicked.
// Caller-supplied data is no longer accepted at all for this purpose — see
// ContainerPortEditState in mutations.go.

// renderReadOnlyGuidance renders the Compose/Quadlet "inspection + guidance
// only" panel. Podder never mutates or restarts these workloads
// automatically (see MutateComposePorts/MutateQuadletPorts, which remain
// defensive read-only guards) -- this panel must never look like a
// mutation control. Unit names/file paths are untrusted (container-label
// derived), so every value is set via textContent, never interpolated into
// markup.
function renderReadOnlyGuidance(kind, details) {
    const guidanceText = document.getElementById('edit-ports-guidance-text');
    const fileInfo = document.getElementById('edit-ports-file-info');

    guidanceText.textContent = '';
    const title = document.createElement('div');
    title.style.fontWeight = '650';
    title.style.marginBottom = '6px';
    title.textContent = kind === 'quadlet' ? 'Quadlet-managed workload' : 'Compose-managed workload';
    guidanceText.appendChild(title);

    const note = document.createElement('div');
    note.textContent = details.note;
    guidanceText.appendChild(note);

    // Compose provenance (working_dir/config_files) is untrusted,
    // container-supplied label content -- containment inside the claimed
    // working directory is NOT authorization to read it automatically. The
    // UI must clearly distinguish what the container CLAIMS from what
    // Podder actually VERIFIED and read (AppSettings.ComposeTrustedRoots).
    if (kind === 'compose' && details.trusted === false) {
        const untrusted = document.createElement('div');
        untrusted.style.marginTop = '6px';
        untrusted.style.color = 'var(--color-amber, #f59e0b)';
        untrusted.textContent = 'Compose provenance detected, but Podder will not read the reported file automatically because its path is not trusted.' +
            (details.untrustedReason ? ` (${details.untrustedReason})` : ' Configure an approved Compose trusted root in Settings to allow automatic reading.');
        guidanceText.appendChild(untrusted);
    }

    fileInfo.textContent = '';
    fileInfo.style.display = 'block';
    if (kind === 'quadlet') {
        appendGuidanceLine(fileInfo, 'Unit:', details.unitValue);
        if (details.filePath) appendGuidanceLine(fileInfo, 'Unit File:', details.filePath);
    } else {
        appendGuidanceLine(fileInfo, 'Compose path claimed by container:', details.claimedPath || '(none reported)');
        appendGuidanceLine(fileInfo, 'Verified local Compose file:', details.trusted && details.filePath ? details.filePath : '(not read -- see notice above)');
        appendGuidanceLine(fileInfo, 'Service:', details.service);
    }
}

function appendGuidanceLine(container, label, value) {
    const row = document.createElement('div');
    row.style.marginTop = '4px';
    const strong = document.createElement('strong');
    strong.textContent = label + ' ';
    const code = document.createElement('code');
    code.textContent = value || '';
    row.appendChild(strong);
    row.appendChild(code);
    container.appendChild(row);
}

// regenerateEditSnippetFromRows produces the Compose/Quadlet configuration
// snippet for the ports currently shown in the row editor. This ALWAYS goes
// through the backend's canonical Go port-formatting machinery
// (PreviewComposeSnippet/PreviewQuadletSnippet, which call the same
// FormatPublishSpec used by real mutation guidance) -- the frontend must
// never independently reimplement Podman/Compose/Quadlet port syntax, which
// is how omitted-bind handling, IPv6 bracketing, port ranges, and protocol
// formatting drift between a hand-built "preview" string and the real one.
async function regenerateEditSnippetFromRows() {
    const isQuadlet = currentEditProvenance && currentEditProvenance.type === 'quadlet';
    const isCompose = currentEditProvenance && currentEditProvenance.type === 'compose';
    if (!isQuadlet && !isCompose) return;

    const snippetWrapper = document.getElementById('edit-ports-snippet-wrapper');
    const snippetText = document.getElementById('edit-ports-snippet-text');

    const mappings = editPortRows
        .filter(r => parseInt(r.containerPort, 10) > 0)
        .map(r => {
            const rSize = parseInt(r.rangeSize, 10);
            return {
                hostIP: (r.hostIP || '').trim(),
                hostPort: parseInt(r.hostPort, 10) || 0,
                containerPort: parseInt(r.containerPort, 10),
                protocol: (r.protocol || 'tcp').toLowerCase(),
                rangeSize: (!isNaN(rSize) && rSize > 1) ? rSize : 0
            };
        });

    try {
        if (isQuadlet) {
            currentEditSnippet = await Podman.PreviewQuadletSnippet(mappings);
        } else {
            const serviceName = (currentEditProvenance.service || document.getElementById('edit-ports-service-name').value || '').trim();
            if (!serviceName) {
                snippetWrapper.style.display = 'none';
                return;
            }
            currentEditSnippet = await Podman.PreviewComposeSnippet(serviceName, mappings);
        }
        snippetWrapper.style.display = 'block';
        snippetText.textContent = currentEditSnippet;
    } catch (e) {
        console.error("Could not generate configuration snippet:", e);
    }
}

window.openEditPortsModal = async (containerId, containerNameHint) => {
    currentEditProvenance = { type: 'adhoc' };
    currentEditSnippet = '';
    editPortRows = [];
    nextEditPortRowId = 1;

    const loadErrorBox = document.getElementById('edit-ports-load-error');
    const loadErrorText = document.getElementById('edit-ports-load-error-text');
    const guidanceBox = document.getElementById('edit-ports-guidance-box');
    const guidanceText = document.getElementById('edit-ports-guidance-text');
    const snippetWrapper = document.getElementById('edit-ports-snippet-wrapper');
    const interactiveArea = document.getElementById('edit-ports-interactive-area');
    const fileInfo = document.getElementById('edit-ports-file-info');
    const stepsBox = document.getElementById('edit-ports-steps-box');
    const submitBtn = document.getElementById('btn-submit-port-mutation');
    const provPillHost = document.getElementById('edit-ports-provenance');
    const rowsContainer = document.getElementById('edit-ports-rows-container');

    document.getElementById('edit-ports-container-id').value = containerId;
    document.getElementById('edit-ports-title').textContent = `Edit Ports: ${containerNameHint || (containerId || '').substring(0, 12)}`;
    provPillHost.textContent = '';

    // Reset to a fail-closed loading state FIRST: nothing here is editable
    // until fresh backend state is confirmed. A caller must never be able
    // to make a real workload look like "no ports configured" simply by
    // this call racing ahead of the fetch below.
    loadErrorBox.style.display = 'none';
    guidanceBox.style.display = 'none';
    snippetWrapper.style.display = 'none';
    fileInfo.style.display = 'none';
    fileInfo.textContent = '';
    stepsBox.style.display = 'none';
    document.getElementById('edit-ports-steps-list').innerHTML = '';
    interactiveArea.style.display = 'none';
    submitBtn.style.display = 'none';
    submitBtn.disabled = true;
    rowsContainer.innerHTML = `<div class="empty-port-hint">Loading current configuration&hellip;</div>`;

    openModal('edit-ports-modal');

    let state = null;
    let loadError = null;
    try {
        state = await Podman.GetContainerPortEditState(containerId);
    } catch (e) {
        loadError = e;
    }

    if (loadError || !state) {
        loadErrorBox.style.display = 'block';
        loadErrorText.textContent = `Could not load this container's current configuration, so mutation is disabled rather than showing a possibly-misleading blank editor. (${loadError || 'no data returned'})`;
        rowsContainer.innerHTML = '';
        return;
    }

    const containerName = state.containerName || containerNameHint || '';
    currentEditProvenance = state.provenance || { type: 'adhoc' };
    let portMappings = state.portMappings || [];

    document.getElementById('edit-ports-service-name').value = containerName;
    document.getElementById('edit-ports-unit-name').value = currentEditProvenance.unitName || currentEditProvenance.name || '';
    document.getElementById('edit-ports-title').textContent = `Edit Ports: ${containerName || (containerId || '').substring(0, 12)}`;

    // Render provenance pill in modal header (label/type/guidance are
    // untrusted — sourced from container labels — so build it via
    // textContent/dataset rather than innerHTML string interpolation).
    const provPill = document.createElement('span');
    provPill.className = `prov-badge ${currentEditProvenance.type || 'adhoc'}`;
    provPill.textContent = currentEditProvenance.displayType || 'Ad-Hoc';
    provPillHost.appendChild(provPill);

    if (currentEditProvenance.type === 'pod') {
        guidanceBox.style.display = 'block';
        guidanceText.textContent = `This container is part of Pod '${currentEditProvenance.podName || 'pod'}'. Ports in Podman belong to the Pod itself and cannot be edited on member containers.`;
        rowsContainer.innerHTML = '';
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
        rowsContainer.innerHTML = '';
        return;
    }

    if (currentEditProvenance.type === 'quadlet') {
        guidanceBox.style.display = 'block';
        interactiveArea.style.display = 'block';
        submitBtn.style.display = 'inline-block';
        submitBtn.disabled = false;
        submitBtn.textContent = 'Generate PublishPort Snippet';

        let unitPath = '';
        try {
            const quadletDetails = await Podman.InspectQuadlet(currentEditProvenance.unitName || currentEditProvenance.name);
            if (quadletDetails && quadletDetails.exists) {
                unitPath = quadletDetails.filePath;
                if (quadletDetails.portMappings && quadletDetails.portMappings.length > 0) {
                    portMappings = quadletDetails.portMappings;
                }
            }
        } catch (e) {
            // Fresh runtime state (from GetContainerPortEditState above) is
            // already loaded into portMappings; a failed unit-file inspect
            // just means we fall back to that instead of the file's own
            // (possibly more precise) view -- never to a blank/cached list.
            console.warn("Could not inspect quadlet file directly; using last known runtime configuration:", e);
        }

        renderReadOnlyGuidance('quadlet', {
            unitValue: currentEditProvenance.unitName || currentEditProvenance.name || '(unknown)',
            filePath: unitPath,
            note: 'Podder will not modify or restart this unit automatically.'
        });
    } else if (currentEditProvenance.type === 'compose') {
        guidanceBox.style.display = 'block';
        interactiveArea.style.display = 'block';
        submitBtn.style.display = 'inline-block';
        submitBtn.disabled = false;
        submitBtn.textContent = 'Generate Updated Ports Snippet';

        let composeFile = '';
        let claimedPath = '';
        let trusted = false;
        let untrustedReason = '';
        try {
            const composeDetails = await Podman.InspectCompose(containerId);
            if (composeDetails) {
                claimedPath = composeDetails.claimedConfigFile || composeDetails.workingDir || '';
                trusted = !!composeDetails.trusted;
                untrustedReason = composeDetails.untrustedReason || '';
                if (trusted && composeDetails.composeFile) {
                    composeFile = composeDetails.composeFile;
                    if (composeDetails.portMappings && composeDetails.portMappings.length > 0) {
                        portMappings = composeDetails.portMappings;
                    }
                }
            }
        } catch (e) {
            // Fresh runtime state (from GetContainerPortEditState above) is
            // already loaded into portMappings; a failed inspect just means
            // we fall back to that instead of the file's own (possibly more
            // precise) view -- never to a blank/cached list.
            console.warn("Could not inspect compose file directly; using last known runtime configuration:", e);
        }

        renderReadOnlyGuidance('compose', {
            claimedPath: claimedPath,
            filePath: composeFile,
            trusted: trusted,
            untrustedReason: untrustedReason,
            service: currentEditProvenance.service || '(unknown)',
            note: 'Podder will not modify this Compose project automatically.'
        });
    } else {
        interactiveArea.style.display = 'block';
        submitBtn.style.display = 'inline-block';
        submitBtn.disabled = false;
        submitBtn.textContent = 'Mutate Ports (Atomic Transaction)';
    }

    if (portMappings && portMappings.length > 0) {
        portMappings.forEach(m => {
            editPortRows.push({
                id: nextEditPortRowId++,
                // The exact backend-observed HostIP is preserved as-is,
                // including "" (omitted/default) -- defaulting it to
                // '127.0.0.1' here would silently show a loopback bind for
                // a mapping that may actually be wildcard/omitted, exactly
                // the kind of declared-vs-displayed discrepancy this
                // hardening pass closes.
                hostIP: m.hostIP || '',
                hostPort: m.hostPort || '',
                containerPort: m.containerPort || '',
                protocol: (m.protocol || 'tcp').toLowerCase(),
                rangeSize: (m.rangeSize && m.rangeSize > 1) ? m.rangeSize : 1,
                statusText: '',
                statusLevel: 'ok'
            });
        });
    } else {
        addEditPortRow();
    }
    renderEditPortRows();
    updateEditExposureWarning();
    await regenerateEditSnippetFromRows();
};

window.addEditPortRow = (hostIP = '', hostPort = '', containerPort = '', protocol = 'tcp', rangeSize = 1) => {
    const rowId = nextEditPortRowId++;
    editPortRows.push({
        id: rowId,
        hostIP: hostIP,
        hostPort: hostPort,
        containerPort: containerPort,
        protocol: protocol,
        rangeSize: rangeSize,
        statusText: '',
        statusLevel: 'ok'
    });
    renderEditPortRows();
    updateEditExposureWarning();
    regenerateEditSnippetFromRows();
};

window.removeEditPortRow = (id) => {
    editPortRows = editPortRows.filter(r => r.id !== id);
    renderEditPortRows();
    updateEditExposureWarning();
    regenerateEditSnippetFromRows();
};

window.updateEditPortField = async (id, field, value) => {
    const row = editPortRows.find(r => r.id === id);
    if (!row) return;
    row[field] = value;

    const containerId = document.getElementById('edit-ports-container-id').value;
    const hPort = parseInt(row.hostPort, 10);
    const cPort = parseInt(row.containerPort, 10);
    const rSize = parseInt(row.rangeSize, 10) || 1;
    if (hPort > 0 && cPort > 0) {
        try {
            const validation = await Podman.ValidatePortMapping({
                hostIP: row.hostIP,
                hostPort: hPort,
                containerPort: cPort,
                protocol: row.protocol,
                rangeSize: rSize > 1 ? rSize : 0,
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

    updateEditPortRowStatus(id);
    updateEditExposureWarning();
    regenerateEditSnippetFromRows();
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
            regenerateEditSnippetFromRows();
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
        const rangeSize = parseInt(row.rangeSize, 10) || 1;
        const rangePreview = rangeSize > 1 ? `<div class="row-validation-msg ok" style="margin-top: 2px;">Range: ${escapeHtml(formatPortRangeSuffix(row.hostPort || 0, rangeSize))} &rarr; ${escapeHtml(formatPortRangeSuffix(row.containerPort || 0, rangeSize))}</div>` : '';
        return `
            <div class="port-input-row" id="edit-port-row-${row.id}">
                <div class="port-field-group" style="flex: 1.5;">
                    <span class="field-mini-label">Bind IP</span>
                    <input type="text" class="form-input" value="${escapeAttr(row.hostIP)}" placeholder="blank = default" onchange="updateEditPortField(${row.id}, 'hostIP', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 1;">
                    <span class="field-mini-label">Host Port</span>
                    <input type="number" class="form-input" value="${escapeAttr(row.hostPort)}" placeholder="8080" min="1" max="65535" oninput="updateEditPortField(${row.id}, 'hostPort', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 1;">
                    <span class="field-mini-label">Target Port</span>
                    <input type="number" class="form-input" value="${escapeAttr(row.containerPort)}" placeholder="80" min="1" max="65535" oninput="updateEditPortField(${row.id}, 'containerPort', this.value)"/>
                </div>
                <div class="port-field-group" style="flex: 0.7;">
                    <span class="field-mini-label" title="Number of consecutive ports in this mapping, starting at Host/Target Port">Count / Range Size</span>
                    <input type="number" class="form-input" value="${escapeAttr(rangeSize)}" min="1" max="65535" oninput="updateEditPortField(${row.id}, 'rangeSize', this.value)"/>
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
            ${rangePreview}
            <div class="row-validation-msg ${escapeAttr(row.statusLevel)}" id="edit-port-status-${row.id}"${row.statusText ? '' : ' style="display:none;"'}>${escapeHtml(row.statusText)}</div>
        `;
    }).join('');
}

// updateEditPortRowStatus updates a single row's validation message in
// place, without touching any <input> element — unlike renderEditPortRows(),
// which replaces the whole row list's innerHTML and would destroy (and drop
// focus from) whichever field the user is actively typing in.
function updateEditPortRowStatus(id) {
    const row = editPortRows.find(r => r.id === id);
    if (!row) return;
    const el = document.getElementById(`edit-port-status-${id}`);
    if (!el) return;
    el.className = `row-validation-msg ${row.statusLevel || 'ok'}`;
    el.textContent = row.statusText || '';
    el.style.display = row.statusText ? '' : 'none';
}

function updateEditExposureWarning() {
    const warningBox = document.getElementById('edit-ports-exposure-warning');
    if (!warningBox) return;

    const hasWildcard = editPortRows.some(r => {
        const cat = describeBindAddress(r.hostIP).category;
        return cat === 'wildcard4' || cat === 'wildcard6' || cat === 'default';
    });

    if (hasWildcard) {
        warningBox.style.display = 'block';
        warningBox.innerHTML = `
            <div class="warning-title">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>
                Wildcard / All Local Interfaces
            </div>
            <div>One or more mappings bind all local interfaces (an omitted address, <code>0.0.0.0</code>, or <code>::</code>) — reachable from any host that can route to this machine, subject to firewall rules. This is not necessarily Internet-public. Use <code>127.0.0.1</code> for local-host-only access.</div>
        `;
    } else {
        warningBox.style.display = 'none';
        warningBox.innerHTML = '';
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
    const isQuadlet = currentEditProvenance && currentEditProvenance.type === 'quadlet';
    const isCompose = currentEditProvenance && currentEditProvenance.type === 'compose';

    const structuredPorts = [];
    for (const row of editPortRows) {
        const hPort = parseInt(row.hostPort, 10);
        const cPort = parseInt(row.containerPort, 10);
        const rSize = parseInt(row.rangeSize, 10);
        if (cPort > 0) {
            structuredPorts.push({
                hostIP: row.hostIP.trim(),
                hostPort: isNaN(hPort) ? 0 : hPort,
                containerPort: cPort,
                protocol: (row.protocol || 'tcp').toLowerCase(),
                rangeSize: (!isNaN(rSize) && rSize > 1) ? rSize : 0
            });
        }
    }

    // Editing ports here always targets an existing (Podder-managed,
    // Quadlet, or Compose) declarative workload, never an auto-assigned
    // ad-hoc endpoint — every mapping needs an explicit Host Port. The
    // backend enforces this too; this just surfaces it before a round trip.
    if (structuredPorts.some(m => !m.hostPort)) {
        showNotification("Every port mapping needs an explicit Host Port; auto-assigned ports are not supported when editing an existing workload's ports.", true);
        return;
    }

    const submitBtn = document.getElementById('btn-submit-port-mutation');

    // Compose/Quadlet are INSPECTION + GUIDANCE ONLY: Podder never mutates
    // or restarts these workloads automatically. This regenerates the
    // paste-ready snippet for the ports currently shown in the editor --
    // it never calls a mutation transaction and never claims one ran.
    if (isQuadlet || isCompose) {
        try {
            submitBtn.disabled = true;
            await regenerateEditSnippetFromRows();
            showNotification("Snippet regenerated below for the ports shown above. Podder has not modified the unit/compose file.", false, true);
        } catch (err) {
            showNotification(`Could not generate snippet: ${err}`, true);
        } finally {
            submitBtn.disabled = false;
        }
        return;
    }

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

        const res = await Podman.MutateContainerPorts({
            containerId: containerId,
            serviceName: serviceName,
            newPorts: structuredPorts
        });

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

        // Defensive fallback: the frontend routes Compose/Quadlet workloads
        // to the snippet-only path above before ever reaching this branch,
        // but if the backend orchestrator guard still reports
        // requiresExternal for some other provenance, surface its guidance
        // rather than silently reporting a generic failure.
        if (res.requiresExternal) {
            showNotification(res.guidance || "This workload requires an external orchestrator action; automatic mutation is not available.", true);
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
        const selectedPath = await Podman.SelectHostPath(kind);
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
    if (!bytes) return '0 Bytes';
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
