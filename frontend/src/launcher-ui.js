import { launcherAction, selectedEndpoint, endpointOptionsHTML, safeWebURL } from './launcher.js';

// New UI uses DOM properties/listeners for all external metadata.
export function setupLauncherUI(Podman, notify, refresh) {
    let current = null;
    let containerID = '';
    let chooser = null;
    const el = id => document.getElementById(id);
    const show = id => el(id).classList.add('active');
    const hide = id => el(id).classList.remove('active');
    const report = e => notify(String(e.message || e), true);
    const guard = fn => async event => { const button = event?.currentTarget; if (button) button.disabled = true; try { await fn(); } catch (e) { report(e); } finally { if (button) button.disabled = false; } };

    function addRow(endpoint = null) {
        const row = document.createElement('div');
        row.className = 'launcher-endpoint-row';
        const mapping = document.createElement('select'); mapping.className = 'form-select'; mapping.setAttribute('aria-label', 'Published TCP mapping');
        const choices = current.mappings || [];
        for (const [index, m] of choices.entries()) {
            const option = document.createElement('option'); option.value = index;
            option.textContent = `${m.hostIP || 'default bind'}:${m.hostPort} → ${m.containerPort}/tcp (confirm HTTP)`;
            mapping.append(option);
        }
        if (endpoint) {
            const index = choices.findIndex(m => m.hostIP === endpoint.mapping.hostIP && m.hostPort === endpoint.mapping.hostPort && m.containerPort === endpoint.mapping.containerPort);
            if (index >= 0) mapping.value = index;
            else { const missing = document.createElement('option'); missing.value = '-1'; missing.textContent = 'Saved mapping missing — choose a current mapping'; mapping.append(missing); mapping.value = '-1'; }
        }
        const scheme = document.createElement('select'); scheme.className = 'form-select'; scheme.setAttribute('aria-label', 'HTTP scheme');
        for (const name of ['http', 'https']) { const o = document.createElement('option'); o.value = name; o.textContent = name.toUpperCase(); scheme.append(o); }
        scheme.value = endpoint?.scheme || 'http';
        const path = document.createElement('input'); path.className = 'form-input'; path.placeholder = '/'; path.value = endpoint?.path || '/'; path.setAttribute('aria-label', 'Application path');
        const preferred = document.createElement('input'); preferred.type = 'radio'; preferred.name = 'launcher-preferred'; preferred.title = 'Preferred endpoint'; preferred.setAttribute('aria-label', 'Preferred endpoint');
        preferred.checked = Boolean(endpoint && endpoint.id === current.definition?.preferredEndpointID);
        const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'btn btn-secondary'; remove.textContent = 'Remove'; remove.addEventListener('click', () => row.remove());
        row.append(mapping, scheme, path, preferred, remove);
        row.readEndpoint = () => ({ id: endpoint?.id || `endpoint-${crypto.randomUUID()}`, mapping: choices[Number(mapping.value)], scheme: scheme.value, path: path.value });
        row.isPreferred = () => preferred.checked;
        el('launcher-endpoints').append(row);
    }
    function loadDefinition(definition) {
        current.definition = definition;
        el('launcher-kind').value = definition?.kind || 'none';
        el('launcher-after-start').checked = Boolean(definition?.openAfterStart);
        el('launcher-gui-identity').value = definition?.gui?.sessionIdentity || '';
        el('launcher-gui-auth').checked = Boolean(definition?.gui?.authenticationConfirmed);
        el('launcher-terminal-program').value = definition?.terminal?.program || 'x-terminal-emulator';
        el('launcher-terminal-command').value = JSON.stringify(definition?.terminal?.command || ['/bin/sh']);
        el('launcher-endpoints').replaceChildren();
        for (const endpoint of definition?.endpoints || []) addRow(endpoint);
        updateKind();
    }
    function updateKind() { el('launcher-web-fields').hidden = !['web', 'gui'].includes(el('launcher-kind').value); el('launcher-gui-fields').hidden = el('launcher-kind').value !== 'gui'; el('launcher-terminal-fields').hidden = el('launcher-kind').value !== 'terminal'; }
    el('launcher-kind').addEventListener('change', updateKind);
    el('launcher-add-endpoint').addEventListener('click', () => addRow());
    el('launcher-clear-preferred').addEventListener('click', () => { for (const r of el('launcher-endpoints').querySelectorAll('input[type=radio]')) r.checked = false; });
    el('launcher-save').addEventListener('click', guard(async () => {
        const kind = el('launcher-kind').value;
        const endpoints = []; let preferredEndpointID = '';
        if (['web', 'gui'].includes(kind)) for (const row of el('launcher-endpoints').children) {
            const e = row.readEndpoint();
            if (!e.mapping) throw new Error('Choose a current published TCP mapping.');
            endpoints.push(e); if (row.isPreferred()) preferredEndpointID = e.id;
        }
        await Podman.SaveLauncherDefinition(containerID, { schemaVersion: 1, kind, association: current.association, endpoints, preferredEndpointID, openAfterStart: ['web', 'gui'].includes(kind) && el('launcher-after-start').checked,
            gui: kind === 'gui' ? { provider: 'xpra-html5', sessionIdentity: el('launcher-gui-identity').value.trim(), authenticationConfirmed: el('launcher-gui-auth').checked } : null,
            terminal: kind === 'terminal' ? { program: el('launcher-terminal-program').value, command: JSON.parse(el('launcher-terminal-command').value) } : null });
        hide('launcher-modal'); await refresh(); notify('Application settings saved.');
    }));
    const configure = async id => {
        const options = await Podman.GetLauncherOptions(id);
        containerID = id; current = options;
        el('launcher-name').textContent = options.association.containerName;
        el('launcher-message').textContent = options.message;
        loadDefinition(options.definition);
        el('launcher-stale').replaceChildren();
        for (const previous of options.stale || []) {
            const button = document.createElement('button'); button.type = 'button'; button.className = 'btn btn-secondary';
            button.textContent = `Review settings from older container ${previous.association.containerID.slice(0, 12)}`;
            button.addEventListener('click', () => { loadDefinition(previous); el('launcher-message').textContent = 'Review mappings and click Save to associate these settings with this container.'; });
            el('launcher-stale').append(button);
        }
        show('launcher-modal');
    };
    async function displayResult(result) {
        el('launcher-result-message').textContent = result.message;
        el('launcher-result-url').value = safeWebURL(result.url) ? result.url : '';
        show('launcher-result-modal');
    }
    el('launcher-copy-url').addEventListener('click', guard(async () => {
        const input = el('launcher-result-url'); input.focus(); input.select();
        if (!safeWebURL(input.value)) throw new Error('No valid web URL available.');
        try { await navigator.clipboard.writeText(input.value); notify('URL copied.'); }
        catch { notify('Select the URL and copy it manually.'); }
    }));
    const execute = async (id, endpointID, action) => {
        if (action === 'copy') { await displayResult({ url: await Podman.GetWebEndpointURL(id, endpointID), message: 'Copy this configured URL.' }); return; }
        notify(action === 'start' ? 'Starting container and waiting up to 15 seconds for HTTP(S)…' : 'Checking HTTP(S) endpoint…');
        const settings = await Podman.GetLauncherOptions(id);
        const result = action === 'start' ? await Podman.StartAndOpenWebEndpoint(id, endpointID) : settings.definition?.kind === 'gui' ? await Podman.OpenGUIEndpoint(id, endpointID) : await Podman.OpenWebEndpoint(id, endpointID);
        if (!result.opened) await displayResult(result); else notify(result.message);
        if (action === 'start') await refresh();
    };
    const open = async (id, action = 'open') => {
        const options = await Podman.GetLauncherOptions(id);
        if (options.definition?.kind === 'terminal') {
            if (window.confirm('Open the configured terminal program and execute these explicit command arguments inside this container?\n' + JSON.stringify(options.definition.terminal?.command))) {
                await Podman.OpenConfiguredTerminal(id, true); notify('Terminal launch requested.');
            }
            return;
        }
        const endpoint = selectedEndpoint(options);
        if (endpoint) return execute(id, endpoint.id, action);
        if (!options.running || !options.endpoints?.length) throw new Error(options.message);
        chooser = { id, options, action };
        el('launcher-choice').innerHTML = endpointOptionsHTML(options.endpoints);
        el('launcher-remember').checked = false;
        show('launcher-choose-modal');
    };
    el('launcher-choose-open').addEventListener('click', guard(async () => {
        const c = chooser; const endpoint = c.options.endpoints[Number(el('launcher-choice').value)];
        if (!endpoint) throw new Error('Choose an endpoint.');
        if (el('launcher-remember').checked) { await Podman.SaveLauncherDefinition(c.id, { ...c.options.definition, association: c.options.association, preferredEndpointID: endpoint.id }); await refresh(); }
        hide('launcher-choose-modal'); await execute(c.id, endpoint.id, c.action);
    }));
    return { configure, open, launcherAction };
}
