// WebKitGTK custom schemes may ignore frame-ancestors response headers.
// Do not initialize Podder or import its privileged runtime in a child frame.
export async function startPodder(view, document, load) {
    let topLevel = false;
    try {
        topLevel = view.self === view.top;
    } catch {
        // An unavailable frame identity cannot establish a trusted window.
    }
    if (!topLevel) {
        document.documentElement.textContent = 'Open Podder in its application window.';
        return false;
    }
    await load();
    return true;
}

// A dynamic import can finish after DOMContentLoaded. Defer even the ready
// case until the importing module finishes defining its controllers.
export function onDocumentReady(document, initialize) {
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initialize, { once: true });
    } else {
        queueMicrotask(initialize);
    }
}
