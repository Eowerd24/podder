import test from 'node:test';
import assert from 'node:assert/strict';
import { createNotification } from './notification.js';

function harness() {
    const elements = new Map();
    for (const id of ['notification', 'notification-icon', 'notification-text', 'notification-close']) {
        const classes = new Set();
        elements.set(id, { classes, set className(value) { classes.clear(); for (const name of value.split(/\s+/)) if (name) classes.add(name); }, classList: { add: value => classes.add(value), remove: value => classes.delete(value) }, addEventListener(name, handler) { this[name] = handler; } });
    }
    let nextID = 0;
    const pending = new Map();
    const timers = { setTimeout(handler, delay) { const id = ++nextID; pending.set(id, { handler, delay }); return id; }, clearTimeout(id) { pending.delete(id); } };
    const show = createNotification({ getElementById: id => elements.get(id) }, timers);
    return { show, el: id => elements.get(id), pending };
}

test('returned launch failures show as dismissible errors with a finite timeout', () => {
    const h = harness();
    h.show('Container launch failed', true);
    assert.equal(h.el('notification-text').textContent, 'Container launch failed');
    assert.equal(h.el('notification').classes.has('error'), true);
    assert.equal([...h.pending.values()][0].delay, 7000);
    h.el('notification-close').click();
    assert.equal(h.el('notification').classes.has('active'), false);
    assert.equal(h.pending.size, 0);
});

test('new notifications cancel old timers and successful completion expires', () => {
    const h = harness();
    h.show('Launching');
    assert.equal(h.pending.size, 0);
    h.show('First failure', true);
    const oldTimerID = [...h.pending.keys()][0];
    h.show('Running', false, true);
    assert.equal(h.pending.size, 1);
    assert.equal([...h.pending.values()][0].delay, 4000);
    assert.equal(h.el('notification-text').textContent, 'Running');
    assert.equal(h.el('notification').classes.has('success'), true);
    // The cancelled callback must not be scheduled to hide a newer message.
    assert.equal(h.pending.has(oldTimerID), false);
    [...h.pending.values()][0].handler();
    assert.equal(h.el('notification').classes.has('active'), false);
});
