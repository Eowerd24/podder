import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { setupSessionUI } from './session-ui.js';

// Controller/DOM-adapter evidence only: no browser rendering or native acceptance.
class Element {
    constructor(tag = 'input', doc) { this.tag = tag; this.doc = doc; this.children = []; this.listeners = {}; this.dataset = {}; this.value = ''; this._checked = false; this.disabled = false; this.classes = new Set(); this.classList = { add: c => this.classes.add(c), remove: c => this.classes.delete(c) }; }
    get checked() { return this._checked; }
    set checked(value) { if (value && this.type === 'radio' && this.name) for (const other of this.doc.all) if (other !== this && other.type === 'radio' && other.name === this.name) other._checked = false; this._checked = Boolean(value); }
    append(...children) { for (const child of children) { child.parent = this; this.children.push(child); } }
    replaceChildren(...children) { for (const child of this.children) child.parent = null; this.children = []; this.append(...children); }
    remove() { this.parent.children = this.parent.children.filter(child => child !== this); this.parent = null; }
    setAttribute(key, value) { this[key] = value; }
    addEventListener(event, fn) { (this.listeners[event] ||= []).push(fn); }
    matches(selector) { return selector.split(',').some(s => { s = s.trim(); return s.startsWith('[data-') ? Object.hasOwn(this.dataset, s.slice(6, -1)) : this.tag === s; }); }
    closest(selector) { return this.matches(selector) ? this : this.parent?.closest(selector); }
    querySelectorAll(selector) { return this.children.flatMap(child => [...(child.matches(selector) ? [child] : []), ...child.querySelectorAll(selector)]); }
    set innerHTML(html) { this.replaceChildren(); parseHTML(html, this, this.doc); }
    async fire(name) {
        if (this.disabled) return;
        const event = { target: this, currentTarget: this, stopPropagation() { this.stopped = true; } };
        // Capture the propagation path before handlers remove/reparent elements.
        const path = []; for (let node = this; node; node = node.parent) path.push(node);
        const pending = [];
        for (const node of path) { event.currentTarget = node; for (const fn of [...(node.listeners[name] || []), ...(node['on' + name] ? [node['on' + name]] : [])]) pending.push(fn(event)); if (event.stopped) break; }
        await Promise.all(pending);
    }
    async click() { if (this.disabled) return; if (this.type === 'radio' || this.type === 'checkbox') { this.checked = this.type === 'radio' || !this.checked; await this.fire('input'); await this.fire('change'); } await this.fire('click'); }
    async edit(value) { if (this.disabled) return; if (this.type === 'checkbox') this.checked = value; else this.value = value; await this.fire('input'); await this.fire('change'); }
}
function parseHTML(html, root, doc) {
    const stack = [root];
    for (const token of html.matchAll(/<\/?[a-zA-Z][^>]*>/g)) {
        const tag = token[0].match(/^<\/?([\w-]+)/)[1].toLowerCase();
        if (token[0].startsWith('</')) { const i = stack.findLastIndex(e => e.tag === tag); if (i > 0) stack.length = i; continue; }
        const node = doc.createElement(tag);
        for (const attr of token[0].matchAll(/([\w-]+)(?:="([^"]*)"|='([^']*)')?/g)) {
            const key = attr[1], value = attr[2] ?? attr[3] ?? '';
            if (key.startsWith('data-')) node.dataset[key.slice(5)] = value;
            else if (['id', 'type', 'name', 'value'].includes(key)) node[key] = value;
            else if (['hidden', 'disabled', 'checked'].includes(key)) node[key] = true;
        }
        if (node.id) doc.elements.set(node.id, node);
        stack.at(-1).append(node);
        if (!['input', 'br', 'hr', 'meta', 'link', 'img'].includes(tag)) stack.push(node);
    }
}
const map = (hostPort, containerPort) => ({ hostIP: '127.0.0.1', hostPort, containerPort, protocol: 'tcp', rangeSize: 1 });
function recipe() {
    return { schemaVersion: 1, name: 'favorite-app', image: 'sha256:immutable', policy: 'data', favorite: true, keepID: true, portMappings: [map(50001, 5432), map(50002, 8080)], web: { id: 'admin', scheme: 'https', path: '/admin', mapping: map(50002, 8080) }, externalBinds: [{ hostPath: '/home/test/config', containerPath: '/external/config', readOnly: true }, { hostPath: '/home/test/data', containerPath: '/external/data', readOnly: false }], allowExternalBinds: true, allowNetworkExposure: true, config: { target: '/config', readWrite: false }, data: { target: '/data', readWrite: false }, requiredEnv: ['TOKEN'], requireCommand: true, command: [], saveCommand: false };
}
function harness(saved = recipe(), overrides = {}) {
    const html = fs.readFileSync(new URL('../index.html', import.meta.url), 'utf8');
    const calls = [], notices = [];
    const doc = { elements: new Map(), all: [], getElementById(id) { assert.ok(this.elements.has(id), `missing HTML element ${id}`); return this.elements.get(id); }, createElement(tag) { const node = new Element(tag, this); this.all.push(node); return node; } };
    parseHTML(html, doc.createElement('document'), doc);
    globalThis.document = doc;
    function validate(spec, saving = false) {
        if (spec.externalBinds.length && !spec.allowExternalBinds) throw new Error('Approve external mounts.');
        if (spec.portMappings.some(m => m.hostIP === '0.0.0.0') && !spec.allowNetworkExposure) throw new Error('Approve network exposure.');
        if (!saving && spec.requiredEnv.some(key => !spec.env[key])) throw new Error('Re-enter environment values.');
        if (!saving && spec.requireCommand && !spec.command.length) throw new Error('Re-enter command.');
    }
    const api = {
        ListDisposableSessions: async () => [], ListSessionRecipes: async () => [structuredClone(saved)], SessionStarterProfiles: async () => [],
        PreviewDisposableSession: async spec => { validate(spec); calls.push(['preview', structuredClone(spec)]); return { summary: ['Reviewed'], argv: ['run', '[REDACTED]'], spec: { launchID: 'reviewed-id' } }; },
        LaunchDisposableSession: async spec => { validate(spec); calls.push(['launch', structuredClone(spec)]); return { state: 'running' }; },
        SaveSessionRecipe: async spec => { validate(spec, true); calls.push(['save', structuredClone(spec)]); },
        ImportSessionRecipe: async () => ({ ...structuredClone(saved), allowExternalBinds: false, allowNetworkExposure: false }),
        ...overrides,
    };
    const ui = setupSessionUI(api, (...args) => notices.push(args), {}), el = id => doc.getElementById(id);
    const load = async () => { await ui.refresh(); await el('session-recipes').children[0].children[0].click(); };
    const secrets = () => { el('session-env').value = 'TOKEN=UI_SECRET_SENTINEL'; el('session-command').value = '["app", "serve"]'; };
    return { el, ui, api, calls, notices, load, secrets };
}
test('controller load edit preview launch save preserves second web mapping, all mounts, favorite and metadata', async () => {
    const original = recipe(), h = harness(original); await h.load();
    const rows = h.el('session-ports').children;
    assert.equal(rows[0].webSelected(), false); assert.equal(rows[1].webSelected(), true);
    assert.equal(h.el('session-external-host').value, original.externalBinds[0].hostPath);
    assert.equal(h.el('session-external-saved').children[0].children[0].value, original.externalBinds[1].hostPath);
    assert.equal(h.el('session-external-approve').checked, false); assert.equal(h.el('session-exposure').checked, false);
    await h.el('session-name').edit('edited-app'); h.secrets();
    await h.el('session-preview').click(); assert.equal(h.calls.length, 0);
    h.el('session-external-approve').checked = true;
    await h.el('session-preview').click(); await h.el('session-launch').click(); await h.el('session-save-recipe').click();
    assert.deepEqual(h.calls.map(call => call[0]), ['preview', 'launch', 'save']);
    for (const [, spec] of h.calls) {
        assert.equal(spec.name, 'edited-app'); assert.equal(spec.favorite, true); assert.equal(spec.keepID, true);
        assert.deepEqual(spec.web, original.web); assert.deepEqual(spec.portMappings, original.portMappings);
        assert.deepEqual(spec.externalBinds, original.externalBinds); assert.deepEqual(spec.data, original.data);
        assert.equal(spec.env.TOKEN, 'UI_SECRET_SENTINEL'); assert.deepEqual(spec.requiredEnv, ['TOKEN']);
    }
    assert.equal(h.calls[1][1].launchID, 'reviewed-id'); assert.ok(!h.el('session-preview-output').textContent.includes('UI_SECRET_SENTINEL'));
});
test('preview filesystem witnesses and canonical bind paths reach launch unchanged', async () => {
    const canonical = [{ hostPath: '/physical/config', containerPath: '/external/config', readOnly: true }, { hostPath: '/physical/data', containerPath: '/external/data', readOnly: false }];
    const approvals = canonical.map((bind, index) => ({ hostPath: bind.hostPath, containerPath: bind.containerPath, readOnly: bind.readOnly, objectIdentity: `dev:inode-${index}` }));
    const h = harness(recipe(), {
        PreviewDisposableSession: async spec => { h.calls.push(['preview', structuredClone(spec)]); return { summary: ['Reviewed'], argv: ['run'], spec: { launchID: 'witnessed-id', externalBinds: canonical, externalBindApprovals: approvals } }; },
        LaunchDisposableSession: async spec => { h.calls.push(['launch', structuredClone(spec)]); return { state: 'running' }; },
    });
    await h.load(); approve(h); await h.el('session-preview').click(); await h.el('session-launch').click();
    const launch = h.calls.find(call => call[0] === 'launch')[1];
    assert.equal(launch.launchID, 'witnessed-id'); assert.deepEqual(launch.externalBinds, canonical); assert.deepEqual(launch.externalBindApprovals, approvals);
});
test('failed launch receipt is reported as an error while cleanup remains available', async () => {
    const state = { id: 'failed-id', state: 'cleanup_pending', message: 'Container launch failed; verify before cleanup.', spec: recipe() };
    const h = harness(recipe(), {
        LaunchDisposableSession: async () => state,
        ListDisposableSessions: async () => [state],
    });
    await h.load(); approve(h);
    await h.el('session-preview').click();
    await h.el('session-launch').click();
    assert.deepEqual(h.notices.at(-1), [state.message, true, false]);
    assert.equal(h.el('session-list').querySelectorAll('[data-action]').some(button => button.dataset.action === 'cleanup'), true);
});
test('controller never substitutes another service for a missing or edited saved mapping', async () => {
    const original = recipe(); original.portMappings = [original.portMappings[0]];
    const h = harness(original); await h.load(); h.secrets(); h.el('session-external-approve').checked = true;
    await h.el('session-preview').click(); await h.el('session-save-recipe').click(); assert.equal(h.calls.length, 0);
    const row = h.el('session-ports').children[0]; row.children[4].checked = true; await row.children[4].fire('change');
    await h.el('session-preview').click(); assert.equal(h.calls.length, 1);
    await row.children[1].edit('50123'); assert.equal(row.webSelected(), false);
    await h.el('session-launch').click(); assert.equal(h.calls.length, 1);
    await h.el('session-preview').click(); await h.el('session-save-recipe').click(); assert.equal(h.calls.length, 1);
    await row.children[4].fire('change'); await h.el('session-preview').click(); assert.equal(h.calls.at(-1)[1].web.mapping.hostPort, 50123);
});
test('controller removing selected mapping requires another explicit mapping choice', async () => {
    const h = harness(); await h.load(); h.secrets(); h.el('session-external-approve').checked = true;
    await h.el('session-ports').children[1].children[5].click(); await h.el('session-preview').click();
    assert.equal(h.calls.length, 0); assert.match(h.notices.at(-1)[0], /exact TCP mapping/);
});
test('reopened/imported drafts require secrets, command and renewed host/network consent', async () => {
    const original = recipe(); original.portMappings[1].hostIP = '0.0.0.0'; original.web.mapping.hostIP = '0.0.0.0';
    const h = harness(original); await h.load(); assert.equal(h.el('session-env').value, 'TOKEN='); assert.equal(h.el('session-command').value, '');
    h.el('session-external-approve').checked = true; h.el('session-exposure').checked = true;
    await h.el('session-preview').click(); assert.equal(h.calls.length, 0);
    h.el('session-env').value = 'TOKEN=UI_SECRET_SENTINEL'; await h.el('session-preview').click(); assert.equal(h.calls.length, 0);
    h.secrets(); await h.el('session-preview').click(); assert.equal(h.calls.length, 1);
    await h.el('session-import').click(); assert.equal(h.el('session-external-approve').checked, false); assert.equal(h.el('session-exposure').checked, false);
    assert.equal(h.el('session-external-host').value, original.externalBinds[0].hostPath);
    h.secrets(); await h.el('session-preview').click(); assert.equal(h.calls.length, 1);
    h.el('session-external-approve').checked = true; await h.el('session-preview').click(); assert.equal(h.calls.length, 1);
    h.el('session-exposure').checked = true; await h.el('session-preview').click(); assert.equal(h.calls.length, 2);
});
test('controller displays external mount text literally and preserves edits/removal of additional mounts', async () => {
    const original = recipe(); original.externalBinds[1].hostPath = '/home/test/<script>literal</script>';
    const h = harness(original); await h.load(); h.secrets(); h.el('session-external-approve').checked = true;
    const extra = h.el('session-external-saved').children[0]; assert.equal(extra.children[0].value, original.externalBinds[1].hostPath);
    await extra.children[1].edit('/edited-target'); await extra.children[2].edit(false);
    await h.el('session-preview').click(); assert.equal(h.calls.at(-1)[1].externalBinds[1].containerPath, '/edited-target'); assert.equal(h.calls.at(-1)[1].externalBinds[1].readOnly, true);
    await extra.children[3].click(); await h.el('session-save-recipe').click(); assert.equal(h.calls.at(-1)[1].externalBinds.length, 1);
});

function deferred() { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; }
function approve(h) { h.secrets(); h.el('session-external-approve').checked = true; h.el('session-exposure').checked = true; }
test('adapter bubbles input/change, groups radios, and prevents disabled control actions', async () => {
    const h = harness(); await h.load(); approve(h);
    const rows = h.el('session-ports').children;
    await rows[0].children[4].click(); assert.equal(rows[1].webSelected(), false);
    await h.el('session-preview').click();
    await h.el('session-name').edit('changed'); await h.el('session-launch').click(); assert.equal(h.calls.length, 1);
    h.el('session-preview').disabled = true; await h.el('session-preview').click(); assert.equal(h.calls.length, 1);
    h.el('session-name').disabled = true; await h.el('session-name').edit('ignored'); assert.equal(h.el('session-name').value, 'changed');
});
test('delayed previews cannot restore authority after edits, consent, mappings, mounts, reopen or import', async t => {
    for (const mutation of ['name', 'consent', 'network', 'mapping', 'selection', 'remove-port', 'mount', 'remove-mount', 'reopen', 'import']) await t.test(mutation, async () => {
        const pending = deferred(); const h = harness(recipe(), { PreviewDisposableSession: () => pending.promise }); await h.load(); approve(h);
        const request = h.el('session-preview').click();
        const rows = h.el('session-ports').children;
        switch (mutation) {
            case 'name': await h.el('session-name').edit('new-name'); break;
            case 'consent': await h.el('session-external-approve').click(); break;
            case 'network': await h.el('session-exposure').click(); break;
            case 'mapping': await rows[1].children[1].edit('50123'); break;
            case 'selection': await rows[0].children[4].click(); break;
            case 'remove-port': await rows[1].children[5].click(); break;
            case 'mount': await h.el('session-external-saved').children[0].children[1].edit('/changed'); break;
            case 'remove-mount': await h.el('session-external-saved').children[0].children[3].click(); break;
            case 'reopen': await h.load(); break;
            case 'import': await h.el('session-import').click(); break;
        }
        pending.resolve({ spec: { launchID: 'old' }, summary: ['OLD PREVIEW'], argv: [] }); await request;
        await h.el('session-launch').click(); assert.equal(h.calls.filter(c => c[0] === 'launch').length, 0);
        assert.equal(h.el('session-launch').hidden, true); assert.ok(!h.el('session-preview-output').textContent.includes('OLD PREVIEW'));
    });
});
test('delayed source review cannot restore build authority after edit or reopen', async t => {
    for (const mutation of ['edit', 'reopen', 'import', 'consent']) await t.test(mutation, async () => {
        const original = recipe(); original.github = { url: 'https://github.com/example/app', commit: 'b'.repeat(40) };
        const pending = deferred(); let builds = 0;
        const h = harness(original, { ReviewGitHubSession: () => pending.promise, StartReviewedSessionBuild: async () => { builds++; } }); await h.load(); approve(h);
        const request = h.el('session-preview').click();
        if (mutation === 'edit') await h.el('session-commit').edit('c'.repeat(40));
        if (mutation === 'reopen') await h.load();
        if (mutation === 'import') await h.el('session-import').click();
        if (mutation === 'consent') await h.el('session-external-approve').click();
        pending.resolve({ id: 'old-review', spec: original, state: 'draft', message: 'OLD REVIEW' }); await request;
        h.el('session-build-approve').checked = true; await h.el('session-build').click();
        assert.equal(builds, 0); assert.equal(h.el('session-build-controls').hidden, true);
    });
});
test('built-image load preview launch save uses immutable provenance and every recipe field', async () => {
    const original = recipe(); original.github = { url: 'https://github.com/example/app', commit: 'b'.repeat(40), subdirectory: 'app', containerfile: 'Containerfile' }; original.preset = 'olivetin-hello';
    const built = { id: 'built-id', state: 'built', spec: original, imageID: 'sha256:' + 'f'.repeat(64) };
    const h = harness(original, { ListDisposableSessions: async () => [built], LaunchBuiltSession: async (...args) => { h.calls.push(['built-launch', args]); return { state: 'running' }; }, LaunchBuiltSessionWithConsent: async (...args) => { h.calls.push(['built-launch', args]); return { state: 'running' }; } });
    await h.ui.refresh(); await h.el('session-list').querySelectorAll('[data-session]')[0].click();
    assert.equal(h.el('session-name').disabled, true); await h.el('session-name').edit('discarded');
    h.secrets(); await h.el('session-external-approve').edit(true); await h.el('session-exposure').edit(true);
    await h.el('session-preview').click(); await h.el('session-launch').click(); await h.el('session-save-recipe').click();
    const saved = h.calls.find(c => c[0] === 'save')?.[1]; assert.ok(saved);
    const expected = { ...original, image: built.imageID, env: { TOKEN: 'UI_SECRET_SENTINEL' }, command: ['app', 'serve'] };
    assert.deepEqual(saved, expected);
});

test('saving a built source never turns its recipe into an OCI-only source', async () => {
    const original = recipe(); original.externalBinds = []; original.allowExternalBinds = false; original.allowNetworkExposure = false;
    original.github = { url: 'https://github.com/example/app', commit: 'b'.repeat(40), subdirectory: 'app', containerfile: 'Containerfile' };
    const h = harness(original, { ListDisposableSessions: async () => [{ id: 'built-id', state: 'built', spec: original, imageID: 'sha256:' + 'f'.repeat(64) }] });
    await h.ui.refresh(); await h.el('session-list').querySelectorAll('[data-session]')[0].click();
    h.secrets(); await h.el('session-save-recipe').click();
    assert.deepEqual(h.calls.find(c => c[0] === 'save')[1].github, original.github);
});
test('current deferred review builds only its reviewed source and edits demand a new review', async () => {
    const original = recipe(); original.github = { url: 'https://github.com/example/app', commit: 'b'.repeat(40) };
    let pending = deferred(); const reviews = [], builds = [];
    const h = harness(original, { ReviewGitHubSession: spec => { reviews.push(structuredClone(spec)); return pending.promise; }, StartReviewedSessionBuild: async (...args) => builds.push(args) });
    await h.load(); approve(h); await h.el('session-name').edit('reviewed-name');
    let request = h.el('session-preview').click(); pending.resolve({ id: 'current', spec: reviews[0], state: 'draft', message: 'CURRENT' }); await request;
    assert.equal(h.el('session-build-controls').hidden, false); await h.el('session-build').click(); assert.equal(builds.length, 0);
    await h.el('session-build-approve').click(); await h.el('session-build').click(); assert.deepEqual(builds, [['current', true, false]]);
    await h.el('session-commit').edit('c'.repeat(40)); await h.el('session-build-approve').click(); await h.el('session-build').click(); assert.equal(builds.length, 1);
    pending = deferred(); request = h.el('session-preview').click(); pending.resolve({ id: 'new', spec: reviews[1], state: 'draft', message: 'NEW' }); await request;
    await h.el('session-build-approve').click(); await h.el('session-build').click(); assert.deepEqual(builds.at(-1), ['new', true, false]); assert.equal(reviews[1].github.commit, 'c'.repeat(40));
});
test('delayed imports and port discovery cannot replace an edited or reopened form', async () => {
    const imported = deferred(), port = deferred();
    const h = harness(recipe(), { ImportSessionRecipe: () => imported.promise, FindFreePort: () => port.promise }); await h.load(); approve(h);
    const importRequest = h.el('session-import').click(); await h.el('session-name').edit('current-name');
    imported.resolve({ ...recipe(), name: 'stale-import' }); await importRequest; assert.equal(h.el('session-name').value, 'current-name');
    const addRequest = h.el('session-add-port').click(); await h.load(); port.resolve(55000); await addRequest; assert.equal(h.el('session-ports').children.length, 2);
});
test('favorite actions use actual delegated buttons and keep metadata when reopening', async () => {
    const actions = []; let favorite = true; const h = harness(recipe(), { ListSessionRecipes: async () => [{ ...recipe(), favorite }], SetSessionRecipeFavorite: async (...args) => { actions.push(args); favorite = args[1]; } }); await h.load();
    await h.el('session-recipes').querySelectorAll('[data-favorite]')[0].click(); assert.deepEqual(actions, [['favorite-app', false]]);
    await h.el('session-recipes').querySelectorAll('[data-recipe]')[0].click(); approve(h); await h.el('session-save-recipe').click(); assert.equal(h.calls.at(-1)[1].favorite, false);
});

test('interrupted-launch cleanup needs its own acknowledgment through real cleanup handlers', async () => {
    const actions = [], state = { id: 'pending-id', state: 'cleanup_pending', spec: recipe() };
    const h = harness(recipe(), {
        ListDisposableSessions: async () => [state],
        PreviewSessionCleanup: async () => ({ needLaunchConfirmation: true, retained: [] }),
        AcknowledgeInterruptedSessionLaunch: async (...args) => actions.push(['ack', ...args]),
        StopAndCleanupSession: async (...args) => { actions.push(['cleanup', ...args]); return { state: 'stopped' }; },
    });
    await h.ui.refresh(); await h.el('session-list').querySelectorAll('[data-session]')[0].click();
    assert.equal(h.el('session-cleanup-launch-row').hidden, false);
    await h.el('session-cleanup-confirm').click(); await h.el('session-cleanup-apply').click(); assert.deepEqual(actions, []);
    await h.el('session-cleanup-launch').click(); await h.el('session-cleanup-apply').click();
    assert.deepEqual(actions, [['ack', 'pending-id', true], ['cleanup', 'pending-id', true, false]]);
    await h.el('session-list').querySelectorAll('[data-session]')[0].click(); assert.equal(h.el('session-cleanup-launch').checked, false);
});
