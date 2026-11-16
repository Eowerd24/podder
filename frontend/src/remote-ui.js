import { escapeHtml } from './trust.js';
export function setupRemoteUI(api, notify) {
    const el = id => document.getElementById(id);
    let profiles = []; let currentProfile = ''; let options = null; let containerID = '';
    const guard = fn => async event => { const b = event?.currentTarget; if (b) b.disabled = true; try { await fn(); } catch (err) { notify(String(err.message || err), true); } finally { if (b) b.disabled = false; } };
    el('remote-save').addEventListener('click', guard(async () => {
        await api.SaveRemoteProfile({ name: el('remote-name').value.trim(), host: el('remote-host').value.trim(), user: el('remote-user').value.trim(), port: Number(el('remote-port').value), socket: el('remote-socket').value.trim(), identityFile: el('remote-identity').value.trim(), knownHostsFile: el('remote-known-hosts').value.trim() }); await refresh(); notify('Remote profile saved. No remote setup was performed.');
    }));
    el('remote-observe').addEventListener('click', guard(async () => {
        currentProfile = el('remote-profiles').value; const containers = await api.ListRemoteContainers(currentProfile);
        el('remote-containers').innerHTML = containers.map((c, i) => `<article class="container-card"><h3>${escapeHtml(c.Names?.[0] || c.Id.slice(0, 12))}</h3><p>${escapeHtml(c.State)} · ${escapeHtml(c.provenance?.displayType || '')}</p><p>Remote workload configuration is observation-only.</p><button class="btn btn-secondary" data-remote-container="${i}">Configure / Open application</button></article>`).join('') || '<p>No remote containers.</p>';
        el('remote-containers').onclick = async event => { const button = event.target.closest('[data-remote-container]'); if (!button) return; try {
            const c = containers[Number(button.dataset.remoteContainer)]; containerID = c.Id; options = await api.GetRemoteLauncherOptions(currentProfile, c.Id);
            el('remote-endpoint-status').textContent = options.message;
            el('remote-mapping').replaceChildren(); for (const [i, mapping] of options.mappings.entries()) { const o = document.createElement('option'); o.value = i; o.textContent = `${mapping.hostIP || 'remote wildcard'}:${mapping.hostPort} → ${mapping.containerPort}/tcp`; el('remote-mapping').append(o); }
            const d = options.definition; const endpoint = d?.endpoints?.[0]; const index = options.mappings.findIndex(m => m.hostIP === endpoint?.mapping.hostIP && m.hostPort === endpoint?.mapping.hostPort && m.containerPort === endpoint?.mapping.containerPort); if (index >= 0) el('remote-mapping').value = index;
            el('remote-kind').value = d?.kind || 'none'; el('remote-scheme').value = endpoint?.scheme || 'http'; el('remote-path').value = endpoint?.path || '/'; el('remote-gui-id').value = d?.gui?.sessionIdentity || ''; el('remote-gui-auth').checked = Boolean(d?.gui?.authenticationConfirmed);
            el('remote-open').disabled = !options.running || !options.endpoints?.length;
            el('remote-application-modal').classList.add('active');
        } catch (err) { notify(String(err.message || err), true); } };
    }));
    el('remote-application-save').addEventListener('click', guard(async () => {
        if (!options) return;
        const kind = el('remote-kind').value; const mapping = options.mappings[Number(el('remote-mapping').value)]; if (kind !== 'none' && !mapping) throw new Error('Select one published TCP mapping you know serves HTTP(S).');
        const definition = { schemaVersion: 1, association: options.association, kind, endpoints: kind === 'none' ? [] : [{ id: 'remote-web', scheme: el('remote-scheme').value, mapping, path: el('remote-path').value || '/' }], gui: kind === 'gui' ? { provider: 'xpra-html5', sessionIdentity: el('remote-gui-id').value.trim(), authenticationConfirmed: el('remote-gui-auth').checked } : null };
        await api.SaveRemoteLauncherDefinition(currentProfile, containerID, definition); options = await api.GetRemoteLauncherOptions(currentProfile, containerID); el('remote-open').disabled = !options.running || !options.endpoints?.length; notify('Remote application settings saved.');
    }));
    el('remote-open').addEventListener('click', guard(async () => {
        if (!window.confirm('Open an explicit SSH tunnel to this remote application? It binds only to workstation loopback, remains visible here, and closes on app exit.')) return;
        const fresh = await api.GetRemoteLauncherOptions(currentProfile, containerID); const endpoint = fresh.endpoints[0]; if (!endpoint) throw new Error(fresh.message);
        const result = await api.OpenRemoteWebEndpoint(currentProfile, containerID, endpoint.id, true); notify(result.message, !result.opened); if (result.url) { el('remote-copy-url').value = result.url; el('remote-copy-url').focus(); el('remote-copy-url').select(); } await refreshTunnels();
    }));
    async function refreshTunnels() {
        const tunnels = await api.ListRemoteTunnels();
        el('remote-tunnels').innerHTML = tunnels.map((t, i) => `<p>${escapeHtml(t.profile)} · ${escapeHtml(t.state)} · ${escapeHtml(t.url)} ${escapeHtml(t.message || '')} <button class="btn btn-secondary" data-tunnel="${i}">Close / dismiss tunnel</button></p>`).join('') || '<p>No active forwarding helpers.</p>';
        el('remote-tunnels').onclick = async event => { const b = event.target.closest('[data-tunnel]'); if (!b) return; try { await api.CloseRemoteTunnel(tunnels[Number(b.dataset.tunnel)].id); await refreshTunnels(); } catch (err) { notify(String(err.message || err), true); } };
    }
    async function refresh() {
        profiles = await api.ListRemoteProfiles(); const selected = el('remote-profiles').value; el('remote-profiles').replaceChildren();
        for (const profile of profiles) { const o = document.createElement('option'); o.value = profile.name; o.textContent = `${profile.name}: ${profile.user}@${profile.host}`; el('remote-profiles').append(o); } if (profiles.some(p => p.name === selected)) el('remote-profiles').value = selected; await refreshTunnels();
    }
    return { refresh };
}
