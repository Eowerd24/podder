import { escapeHtml } from './trust.js';

export function launcherAction(options) {
    if (!options?.running) return '';
    if (options.definition?.kind === 'terminal' && options.definition.terminal?.command?.length) return 'Open Terminal';
    if (!['web', 'gui'].includes(options.definition?.kind) || !options.endpoints?.length) return '';
    if (options.definition.kind === 'gui' && !options.definition.gui?.authenticationConfirmed) return '';
    return options.endpoints.length > 1 && !options.definition.preferredEndpointID ? 'Choose Endpoint' : options.definition.kind === 'gui' ? 'Launch App' : 'Open Web';
}
export function selectedEndpoint(options) {
    const endpoints = options?.endpoints || [];
    return endpoints.find(e => e.id === options?.definition?.preferredEndpointID) || (endpoints.length === 1 ? endpoints[0] : null);
}
// Render URLs only as escaped, selectable text. Browser navigation is backend-only.
export function endpointOptionsHTML(endpoints) {
    return (endpoints || []).map((e, i) => `<option value="${i}">${escapeHtml(e.url)}</option>`).join('');
}
export function safeWebURL(value) {
    try {
        const u = new URL(value);
        return ['http:', 'https:'].includes(u.protocol) && !u.username && !u.password && !/[\u0000-\u0020\\]/.test(value);
    } catch { return false; }
}
