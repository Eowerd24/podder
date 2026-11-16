import { escapeHtml } from './trust.js';

export function parseSessionEnv(text) {
    const result = {};
    for (const line of text.split('\n')) {
        if (!line.trim()) continue;
        const split = line.indexOf('=');
        if (split < 1) throw new Error('Environment entries need KEY=value.');
        const key = line.slice(0, split).trim();
        if (Object.hasOwn(result, key)) throw new Error(`Duplicate environment key: ${key}`);
        Object.defineProperty(result, key, { value: line.slice(split + 1), enumerable: true, writable: true, configurable: true });
    }
    return result;
}
export function parseSessionCommand(text) {
    if (!text.trim()) return [];
    const command = JSON.parse(text);
    if (!Array.isArray(command) || command.some(arg => typeof arg !== 'string')) throw new Error('Command must be a JSON array of strings.');
    return command;
}
export function setupSessionUI(api, notify, launcherUI) {
    const el = id => document.getElementById(id);
    let revision = 0;
    let previewSpec = null;
    let reviewedID = '';
    let builtID = '';
    let builtSpec = null;
    let preset = '';
    let requiredEnv = [];
    let requireCommand = false;
    let favorite = false;
    let dataReadWrite = true;
    let webMetadata = {};
    let extraBinds = [];
    const mappingKey = m => JSON.stringify([m.hostIP, Number(m.hostPort), Number(m.containerPort), m.protocol || 'tcp', Number(m.rangeSize || 1)]);
    let rows = [];
    const show = id => el(id).classList.add('active');
    const hide = id => el(id).classList.remove('active');
    const guard = fn => async event => { const b = event?.currentTarget; if (b) b.disabled = true; try { await fn(); } catch (err) { notify(String(err.message || err), true); } finally { if (b) b.disabled = false; } };
    function invalidate() { revision++; previewSpec = null; reviewedID = ''; el('session-build-approve').checked = false; el('session-preview-output').textContent = ''; el('session-build-controls').hidden = true; el('session-launch').hidden = true; }
    el('session-form').addEventListener('input', invalidate);
    el('session-form').addEventListener('change', invalidate);
    function addPort(mapping = { hostIP: '127.0.0.1', hostPort: 49160, containerPort: 80, protocol: 'tcp' }) {
        const row = document.createElement('div'); row.className = 'session-port-row';
        for (const [key, label] of [['hostIP', 'Host address'], ['hostPort', 'Host port'], ['containerPort', 'Container port']]) {
            const input = document.createElement('input'); input.className = 'form-input'; input.value = mapping[key]; input.placeholder = label; input.setAttribute('aria-label', label); input.dataset.key = key;
            if (key !== 'hostIP') { input.type = 'number'; input.min = '1'; input.max = '65535'; }
            row.append(input);
        }
        const protocol = document.createElement('select'); protocol.className = 'form-input'; protocol.dataset.key = 'protocol'; protocol.setAttribute('aria-label', 'Protocol');
        for (const value of ['tcp', 'udp']) { const o = document.createElement('option'); o.value = value; o.textContent = value.toUpperCase(); protocol.append(o); } protocol.value = mapping.protocol || 'tcp'; row.append(protocol);
        const web = document.createElement('input'); web.type = 'radio'; web.name = 'session-web-port'; web.title = 'Web endpoint'; web.setAttribute('aria-label', 'Use this mapping for web endpoint'); row.append(web);
        const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'btn btn-secondary'; remove.textContent = 'Remove'; remove.addEventListener('click', () => { rows = rows.filter(r => r !== row); row.remove(); invalidate(); }); row.append(remove);
        row.mapping = () => ({ ...mapping, ...Object.fromEntries([...row.querySelectorAll('[data-key]')].map(input => [input.dataset.key, input.dataset.key.endsWith('Port') ? Number(input.value) : input.value])) });
        for (const input of row.querySelectorAll('[data-key]')) input.addEventListener('input', () => { web.checked = false; row.webMapping = ''; invalidate(); });
        row.webSelected = () => web.checked;
        row.selectWeb = () => { web.checked = true; row.webMapping = mappingKey(row.mapping()); };
        web.addEventListener('change', () => { for (const other of rows) other.children[4].checked = false; row.selectWeb(); invalidate(); });
        rows.push(row); el('session-ports').append(row); return row;
    }
    function readSpec() {
        const command = parseSessionCommand(el('session-command').value);
        const spec = { schemaVersion: 1, favorite, keepID: el('session-keep-id').checked, preset, requiredEnv, requireCommand, name: el('session-name').value.trim(), image: el('session-image').value.trim(), policy: el('session-policy').value, portMappings: rows.map(row => row.mapping()), command, env: parseSessionEnv(el('session-env').value), config: { target: el('session-config-target').value.trim(), readWrite: el('session-config-rw').checked }, data: { target: el('session-data-target').value.trim(), readWrite: dataReadWrite }, externalBinds: [], allowExternalBinds: el('session-external-approve').checked, allowNetworkExposure: el('session-exposure').checked, saveCommand: el('session-save-command').checked, web: null, github: null };
        if (el('session-external-host').value.trim()) spec.externalBinds.push({ hostPath: el('session-external-host').value.trim(), containerPath: el('session-external-target').value.trim(), readOnly: !el('session-external-rw').checked });
        spec.externalBinds.push(...extraBinds.map(row => row.bind()));
        if (el('session-web').checked) { const row = rows.find(r => r.webSelected()); if (!row || row.mapping().protocol !== 'tcp' || row.webMapping !== mappingKey(row.mapping())) throw new Error('Select and review the exact TCP mapping for the web application.'); spec.web = { ...webMetadata, id: webMetadata.id || 'web', scheme: el('session-scheme').value, path: el('session-web-path').value || '/', mapping: row.mapping() }; }
        if (el('session-source').value === 'github') { spec.github = { url: el('session-repo').value.trim(), commit: el('session-commit').value.trim(), subdirectory: el('session-subdir').value.trim(), containerfile: el('session-containerfile').value.trim() }; spec.image = 'review-pending'; }
        return spec;
    }
    function readBuiltSpec() {
        // Keep the immutable reviewed source and every recipe field. Only launch
        // material and current consent are editable in this form.
        return { ...structuredClone(builtSpec), env: parseSessionEnv(el('session-env').value), command: parseSessionCommand(el('session-command').value), allowExternalBinds: el('session-external-approve').checked, allowNetworkExposure: el('session-exposure').checked };
    }
    function fill(spec = null) {
        reviewedID = ''; builtID = ''; builtSpec = null; favorite = Boolean(spec?.favorite); dataReadWrite = spec?.data?.readWrite ?? true; webMetadata = { ...spec?.web }; preset = spec?.preset || ''; requiredEnv = spec?.requiredEnv || []; requireCommand = Boolean(spec?.requireCommand); for (const input of el('session-form').querySelectorAll('input, select, textarea, button')) input.disabled = false; invalidate(); el('session-preview-output').textContent = ''; el('session-build-controls').hidden = true; rows = []; el('session-ports').replaceChildren();
        el('session-name').value = spec?.name || 'session'; el('session-image').value = spec?.image || ''; el('session-policy').value = spec?.policy || 'temporary';
        el('session-command').value = spec?.command?.length ? JSON.stringify(spec.command) : ''; el('session-env').value = (spec?.requiredEnv || []).map(key => `${key}=`).join('\n');
        el('session-config-target').value = spec?.config?.target || ''; el('session-config-rw').checked = Boolean(spec?.config?.readWrite); el('session-data-target').value = spec?.data?.target || '';
        const firstBind = spec?.externalBinds?.[0];
        el('session-external-host').value = firstBind?.hostPath || ''; el('session-external-target').value = firstBind?.containerPath || ''; el('session-external-rw').checked = firstBind ? !firstBind.readOnly : false;
        extraBinds = []; el('session-external-saved').replaceChildren();
        for (const bind of spec?.externalBinds?.slice(1) || []) {
            const row = document.createElement('div');
            const host = document.createElement('input'); host.value = bind.hostPath; host.setAttribute('aria-label', 'Saved host directory');
            const target = document.createElement('input'); target.value = bind.containerPath; target.setAttribute('aria-label', 'Saved container target');
            const writable = document.createElement('input'); writable.type = 'checkbox'; writable.checked = !bind.readOnly; writable.setAttribute('aria-label', 'Allow writes to saved directory');
            const remove = document.createElement('button'); remove.type = 'button'; remove.textContent = 'Remove mount'; remove.addEventListener('click', () => { extraBinds = extraBinds.filter(b => b !== row); row.remove(); invalidate(); });
            row.append(host, target, writable, remove); row.bind = () => ({ ...bind, hostPath: host.value.trim(), containerPath: target.value.trim(), readOnly: !writable.checked }); extraBinds.push(row); el('session-external-saved').append(row);
        } el('session-external-approve').checked = false; el('session-exposure').checked = false;
        el('session-keep-id').checked = Boolean(spec?.keepID); el('session-save-command').checked = Boolean(spec?.saveCommand); el('session-web').checked = Boolean(spec?.web); el('session-scheme').value = spec?.web?.scheme || 'http'; el('session-web-path').value = spec?.web?.path || '/';
        el('session-source').value = spec?.github ? 'github' : 'image'; el('session-github-fields').hidden = !spec?.github; for (const [field, key] of [['repo', 'url'], ['commit', 'commit'], ['subdir', 'subdirectory'], ['containerfile', 'containerfile']]) el(`session-${field}`).value = spec?.github?.[key] || '';
        for (const mapping of spec?.portMappings || []) { const row = addPort(mapping); if (spec?.web && mappingKey(mapping) === mappingKey(spec.web.mapping)) row.selectWeb(); }
        el('session-required').textContent = spec?.requireCommand ? 'Re-enter the command arguments. Environment values must also be re-entered.' : 'Environment values are never saved. Re-enter required values before relaunch.';
        show('session-modal');
    }
    el('session-source').addEventListener('change', () => { invalidate(); el('session-github-fields').hidden = el('session-source').value !== 'github'; });
    el('session-new').addEventListener('click', () => { fill(); el('session-github-fields').hidden = true; });
    el('session-add-port').addEventListener('click', guard(async () => { const start = Math.max(49160, ...rows.map(r => r.mapping().hostPort + 1)); if (start > 65535) throw new Error('No higher host port is available; edit the existing mappings.'); invalidate(); const requestRevision = revision; const port = await api.FindFreePort(start, 'tcp', '127.0.0.1'); if (revision !== requestRevision) return; addPort({ hostIP: '127.0.0.1', hostPort: port, containerPort: 80, protocol: 'tcp' }); invalidate(); }));
    el('session-preview').addEventListener('click', guard(async () => {
        invalidate(); const requestRevision = revision;
        const spec = builtID ? { ...readBuiltSpec(), launchID: builtID } : readSpec();
        if (spec.github && !builtID) {
            notify('Fetching the pinned public source for review; no build code is executed.');
            const review = await api.ReviewGitHubSession(spec); if (revision !== requestRevision) return; reviewedID = review.id;
            el('session-preview-output').textContent = `${review.message}\n${review.spec.github.url}\nCommit: ${review.spec.github.commit}\nState: ${review.state}\n\n${review.buildDefinition || review.buildLog || ''}`;
            el('session-build-controls').hidden = review.state !== 'draft'; el('session-build-approve').checked = false; return;
        }
		const preview = await api.PreviewDisposableSession(spec); if (revision !== requestRevision) return; previewSpec = { ...spec, launchID: preview.spec.launchID, externalBinds: preview.spec.externalBinds ?? spec.externalBinds, externalBindApprovals: preview.spec.externalBindApprovals ?? [] };
        el('session-preview-output').textContent = `${preview.summary.join('\n')}\n\nCommand argv (secrets masked):\n${JSON.stringify(preview.argv, null, 2)}`;
        el('session-launch').hidden = false;
    }));
    el('session-launch').addEventListener('click', guard(async () => {
        if (!previewSpec) throw new Error('Generate a fresh launch preview.');
        const state = builtID
			? await api.LaunchBuiltSessionWithConsent(builtID, previewSpec.env, previewSpec.command, { allowExternalBinds: previewSpec.allowExternalBinds, allowNetworkExposure: previewSpec.allowNetworkExposure, externalBindApprovals: previewSpec.externalBindApprovals || [] })
            : await api.LaunchDisposableSession(previewSpec);
        hide('session-modal');
        notify(state.message || `Session ${state.state}`, state.state !== 'running', state.state === 'running');
        await refresh();
    }));
    el('session-build').addEventListener('click', guard(async () => {
        if (!reviewedID || !el('session-build-approve').checked) throw new Error('Review the build definition and approve repository execution.');
        await api.StartReviewedSessionBuild(reviewedID, true, el('session-build-network').checked); hide('session-modal'); notify('Build started. Sessions shows progress and cancellation.'); await refresh();
    }));
    el('session-save-recipe').addEventListener('click', guard(async () => { await api.SaveSessionRecipe(builtID ? readBuiltSpec() : readSpec()); notify('Reusable recipe saved without environment values.'); await refresh(); }));
    el('session-import').addEventListener('click', guard(async () => { invalidate(); const requestRevision = revision; const draft = await api.ImportSessionRecipe(el('session-transfer').value); if (revision !== requestRevision) return; fill(draft); el('session-github-fields').hidden = !draft.github; notify('Imported draft ready for review. Save or preview to continue.'); }));
    async function refresh() {
        const [sessions, saved, starters] = await Promise.all([api.ListDisposableSessions(), api.ListSessionRecipes(), api.SessionStarterProfiles()]);
        el('session-list').innerHTML = sessions.map((s, index) => `<article class="container-card"><h3>${escapeHtml(s.spec.name)} <small>${escapeHtml(s.state)}</small></h3><p>${escapeHtml(s.message || '')}</p><p>${escapeHtml(s.containerID?.slice(0, 12) || s.id)}</p><div class="card-actions-row">
            ${s.state === 'running' ? `<button class="btn btn-secondary" data-session="${index}" data-action="logs">Logs</button><button class="btn btn-secondary" data-session="${index}" data-action="configure">Application</button>${s.spec.web ? `<button class="btn btn-primary" data-session="${index}" data-action="open">Open Web</button>` : ''}` : ''}
            ${s.state === 'building' ? `<button class="btn btn-secondary" data-session="${index}" data-action="cancel">Cancel build</button>` : ''}
            ${s.state === 'built' ? `<button class="btn btn-primary" data-session="${index}" data-action="launch-built">Launch built image</button>` : ''}
            ${['build_failed', 'build_cancelled', 'draft'].includes(s.state) && s.buildDigest ? `<button class="btn btn-secondary" data-session="${index}" data-action="retry">Review / build</button>` : ''}
            ${s.state !== 'building' && s.state !== 'untracked' && (s.state !== 'stopped' || s.imageOwned) ? `<button class="btn btn-danger" data-session="${index}" data-action="cleanup">Stop / cleanup</button>` : ''}
            </div>${s.buildLog ? `<details><summary>Build log</summary><pre>${escapeHtml(s.buildLog)}</pre></details>` : ''}<details><summary>Retained resources</summary><pre>${escapeHtml((s.retained || []).join('\n') || 'No retained mount directories.')}</pre></details></article>`).join('') || '<p>No disposable sessions.</p>';
        el('session-list').onclick = async event => {
            const button = event.target.closest('[data-session]'); if (!button) return; button.disabled = true;
            try {
                const state = sessions[Number(button.dataset.session)];
                switch (button.dataset.action) {
                    case 'logs': window.viewLogs(state.containerID, state.spec.name); break;
                    case 'configure': await launcherUI.configure(state.containerID); break;
                    case 'open': await launcherUI.open(state.containerID); break;
                    case 'cancel': await api.CancelSessionBuild(state.id); notify('Cancellation requested.'); break;
                    case 'cleanup': {
                        const preview = await api.PreviewSessionCleanup(state.id);
                        el('session-cleanup-output').textContent = `${preview.warning}\n\nContainer: ${preview.containerID || 'absent'}\nRemove temporary workspace: ${preview.temporaryDirectory}\n\nRetained:\n${preview.retained?.join('\n') || 'No mount directories'}\n${preview.ownedImage ? `Optional owned image: ${preview.ownedImage}` : 'Images retained'}`;
                        el('session-cleanup-launch').checked = false; el('session-cleanup-launch-row').hidden = !preview.needLaunchConfirmation; el('session-cleanup-image').checked = false; el('session-cleanup-build').checked = false; el('session-cleanup-build-row').hidden = !preview.needBuildConfirmation; el('session-cleanup-confirm').checked = false;
                        el('session-cleanup-apply').onclick = guard(async () => { if (!el('session-cleanup-confirm').checked) throw new Error('Confirm the listed cleanup.'); if (preview.needBuildConfirmation) { if (!el('session-cleanup-build').checked) throw new Error('Verify the interrupted builder has stopped before cleanup.'); await api.AcknowledgeInterruptedSessionBuild(state.id, true); } if (preview.needLaunchConfirmation) { if (!el('session-cleanup-launch').checked) throw new Error('Verify all interrupted launch commands have exited before cleanup.'); await api.AcknowledgeInterruptedSessionLaunch(state.id, true); } const result = await api.StopAndCleanupSession(state.id, true, el('session-cleanup-image').checked); hide('session-cleanup-modal'); notify(result.message, result.state === 'cleanup_failed'); await refresh(); }); show('session-cleanup-modal'); break;
                    }
                    case 'retry': fill(state.spec); reviewedID = state.id; el('session-preview-output').textContent = `${state.message}\n${state.buildDefinition}`; el('session-build-controls').hidden = false; el('session-build-approve').checked = false; break;
                    case 'launch-built': {
                        fill(state.spec); builtID = state.id; builtSpec = { ...structuredClone(state.spec), image: state.imageID };
                        el('session-source').value = 'image'; el('session-github-fields').hidden = true;
                        for (const input of el('session-form').querySelectorAll('input, select, textarea, button')) if (!['session-env', 'session-command', 'session-external-approve', 'session-exposure'].includes(input.id)) input.disabled = true;
                        el('session-preview-output').textContent = 'Built immutable image ready. Re-enter required environment/command values and renew host/network consent, then preview before launch. Other settings use the reviewed source configuration.';
                        break;
                    }
                }
            } catch (err) { notify(String(err.message || err), true); } finally { button.disabled = false; }
        };
        const all = [...saved, ...starters];
        el('session-recipes').innerHTML = all.map((r, i) => `<div class="session-recipe"><button class="btn btn-secondary" data-recipe="${i}">${r.favorite ? '★ ' : ''}${escapeHtml(r.name)}${i >= saved.length ? ' (starter)' : ''}</button>${i < saved.length ? `<button class="btn btn-secondary" data-export="${i}">Export</button><button class="btn btn-secondary" data-favorite="${i}">${r.favorite ? 'Unfavorite' : 'Favorite'}</button>` : ''}</div>`).join('');
        el('session-recipes').onclick = async event => { try { const b = event.target.closest('button'); if (!b) return; if (b.dataset.recipe !== undefined) { fill(all[Number(b.dataset.recipe)]); el('session-github-fields').hidden = !all[Number(b.dataset.recipe)].github; } if (b.dataset.export !== undefined) el('session-transfer').value = await api.ExportSessionRecipe(saved[Number(b.dataset.export)].name); if (b.dataset.favorite !== undefined) { const r = saved[Number(b.dataset.favorite)]; await api.SetSessionRecipeFavorite(r.name, !r.favorite); await refresh(); } } catch (err) { notify(String(err.message || err), true); } };
    }
    return { refresh };
}
