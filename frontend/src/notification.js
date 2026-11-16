export function createNotification(document, timers = globalThis) {
    const banner = document.getElementById('notification');
    const icon = document.getElementById('notification-icon');
    const messageText = document.getElementById('notification-text');
    const close = document.getElementById('notification-close');
    let timeout = null;

    function dismiss() {
        if (timeout !== null) timers.clearTimeout(timeout);
        timeout = null;
        banner?.classList.remove('active');
    }
    close?.addEventListener('click', dismiss);

    return function showNotification(message, isError = false, isSuccess = false) {
        if (!banner || !icon || !messageText) return;
        dismiss();
        banner.className = 'notification-banner active';
        if (isError) {
            banner.classList.add('error');
            icon.innerHTML = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#f43f5e" stroke-width="2.5"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>';
        } else if (isSuccess) {
            banner.classList.add('success');
            icon.innerHTML = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.5"><polyline points="20 6 9 17 4 12"/></svg>';
        } else {
            icon.innerHTML = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#6366f1" stroke-width="2.5" style="animation: spin 1s linear infinite;"><circle cx="12" cy="12" r="10"/><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83"/></svg>';
        }
        messageText.textContent = message;
        if (isSuccess || isError) {
            timeout = timers.setTimeout(dismiss, isError ? 7000 : 4000);
        }
    };
}
