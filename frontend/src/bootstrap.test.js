import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { startPodder, onDocumentReady } from './bootstrap-policy.js';

test('top-level Podder initializes normally', async () => {
    const view = {};
    view.self = view.top = view;
    const document = { documentElement: { textContent: 'normal UI' } };
    let initialized = 0;
    assert.equal(await startPodder(view, document, async () => { initialized++; }), true);
    assert.equal(initialized, 1);
    assert.equal(document.documentElement.textContent, 'normal UI');
});

for (const kind of ['foreign parent', 'same-origin parent', 'opaque parent', 'unavailable top']) {
    test(`${kind} cannot initialize Podder or leave interactive UI`, async () => {
        const view = { self: {} };
        if (kind === 'unavailable top') {
            Object.defineProperty(view, 'top', { get() { throw new Error('unavailable'); } });
        } else {
            view.top = {};
        }
        const document = { documentElement: { textContent: '<privileged-controls>' } };
        let initialized = 0;
        assert.equal(await startPodder(view, document, async () => { initialized++; }), false);
        assert.equal(initialized, 0);
        assert.doesNotMatch(document.documentElement.textContent, /privileged-controls/);
    });
}

test('entry module defers the privileged imports until after frame authorization', async () => {
    const entry = await readFile(new URL('./bootstrap.js', import.meta.url), 'utf8');
    const index = await readFile(new URL('../index.html', import.meta.url), 'utf8');
    assert.match(index, /src="\/src\/bootstrap\.js"/);
    assert.doesNotMatch(index, /src="\/src\/main\.js"/);
    assert.match(entry, /startPodder\(window, document, \(\) => import\('\.\/main\.js'\)\)/);
    assert.doesNotMatch(entry, /from ['"].*(?:main|runtime|bindings)/);
});

test('early main import initializes once after DOM readiness', () => {
    const document = new EventTarget();
    document.readyState = 'loading';
    let initialized = 0;
    onDocumentReady(document, () => { initialized++; });
    assert.equal(initialized, 0);
    document.dispatchEvent(new Event('DOMContentLoaded'));
    document.dispatchEvent(new Event('DOMContentLoaded'));
    assert.equal(initialized, 1);
});

for (const readyState of ['interactive', 'complete']) {
    test(`late main import initializes after module evaluation (${readyState})`, async () => {
        const document = { readyState };
        let controllersReady = false;
        let initialized = 0;
        onDocumentReady(document, () => {
            assert.equal(controllersReady, true);
            initialized++;
        });
        assert.equal(initialized, 0);
        controllersReady = true;
        await Promise.resolve();
        assert.equal(initialized, 1);
    });
}
