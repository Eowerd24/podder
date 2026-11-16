import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { setupLauncherUI } from './launcher-ui.js';

// Minimal DOM adapter tests actual UI control flow without new dependencies.
class Element {
    constructor() { this.children = []; this.listeners = {}; this.value = ''; this.checked = false; this.classes = new Set(); this.classList = { add: c => this.classes.add(c), remove: c => this.classes.delete(c) }; }
    append(...children) { for (const c of children) { c.parent = this; this.children.push(c); } }
    replaceChildren(...children) { this.children = []; this.append(...children); }
    remove() { this.parent.children = this.parent.children.filter(c => c !== this); }
    setAttribute() {}
    addEventListener(name, fn) { this.listeners[name] = fn; }
    async click() { await this.listeners.click?.({ currentTarget: this }); }
    querySelectorAll() { return this.children.flatMap(c => [...(c.type === 'radio' ? [c] : []), ...c.querySelectorAll()]); }
    focus() {} select() {}
}
function harness() {
    const html = fs.readFileSync(new URL('../index.html', import.meta.url), 'utf8');
    const elements = new Map();
    const doc = { getElementById(id) { assert.ok(html.includes(`id="${id}"`), `missing HTML element ${id}`); if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); }, createElement() { return new Element(); } };
    globalThis.document = doc;
    const m = { hostIP: '127.0.0.1', hostPort: 8080, containerPort: 80, protocol: 'tcp' };
    const association = { key: 'container:full-id', containerID: 'full-id', containerName: '<script>app</script>' };
    const endpoints = [{ id: 'a', url: 'http://127.0.0.1:8080/' }, { id: 'b', url: 'http://127.0.0.1:8080/admin' }];
    const options = { association, mappings: [m], running: true, message: '<untrusted>', stale: [], endpoints, definition: { schemaVersion: 1, kind: 'web', endpoints: [{ id: 'a', scheme: 'http', mapping: m, path: '/' }, { id: 'b', scheme: 'http', mapping: m, path: '/admin' }], association } };
    const calls = [], notices = [];
    const api = {
        GetLauncherOptions: async () => structuredClone(options),
        SaveLauncherDefinition: async (id, definition) => { calls.push(['save', id, definition]); options.definition = definition; },
        OpenWebEndpoint: async (id, endpoint) => { calls.push(['open', id, endpoint]); return { opened: false, url: endpoints.find(e => e.id === endpoint).url, message: 'Copy fallback' }; },
        GetWebEndpointURL: async () => endpoints[0].url,
        StartAndOpenWebEndpoint: async (id, endpoint) => { calls.push(['start', id, endpoint]); return { opened: true, message: 'Started and opened' }; },
    };
    const ui = setupLauncherUI(api, (...args) => notices.push(args), async () => calls.push(['refresh']));
    return { el: id => doc.getElementById(id), ui, calls, notices, options };
}
test('chooser remembers selection and exposes selectable copy fallback', async () => {
    const h = harness(); await h.ui.open('full-id');
    assert.ok(h.el('launcher-choose-modal').classes.has('active'));
    h.el('launcher-choice').value = '1'; h.el('launcher-remember').checked = true;
    await h.el('launcher-choose-open').click();
    assert.equal(h.calls[0][2].preferredEndpointID, 'b');
    assert.deepEqual(h.calls.at(-1), ['open', 'full-id', 'b']);
    assert.equal(h.el('launcher-result-url').value, 'http://127.0.0.1:8080/admin');
});
test('editor treats metadata as text and none clears web behavior', async () => {
    const h = harness(); await h.ui.configure('full-id');
    assert.equal(h.el('launcher-name').textContent, '<script>app</script>');
    assert.equal(h.el('launcher-message').textContent, '<untrusted>');
    assert.equal(h.el('launcher-endpoints').children.length, 2);
    h.el('launcher-kind').value = 'none'; await h.el('launcher-save').click();
    const saved = h.calls[0][2]; assert.equal(saved.kind, 'none'); assert.deepEqual(saved.endpoints, []); assert.equal(saved.openAfterStart, false);
    assert.deepEqual(saved.association, h.options.association);
});
test('stale metadata requires review and save against new association', async () => {
    const h = harness(); h.options.definition = null;
    h.options.stale = [{ schemaVersion: 1, kind: 'none', endpoints: [], association: { ...h.options.association, containerID: 'older-id' } }];
    await h.ui.configure('full-id'); assert.equal(h.calls.length, 0);
    await h.el('launcher-stale').children[0].click(); assert.equal(h.calls.length, 0);
    await h.el('launcher-save').click(); assert.equal(h.calls[0][2].association.containerID, 'full-id');
});
test('explicit Start & Open uses separate API and refreshes state', async () => {
    const h = harness(); h.options.running = false; h.options.definition.preferredEndpointID = 'a'; h.options.definition.openAfterStart = true;
    await h.ui.open('full-id', 'start'); assert.deepEqual(h.calls[0], ['start', 'full-id', 'a']); assert.deepEqual(h.calls[1], ['refresh']);
});
