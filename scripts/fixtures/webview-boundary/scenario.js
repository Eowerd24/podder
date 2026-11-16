const report = (value) => window.webkit.messageHandlers.external.postMessage('REPORT ' + JSON.stringify(value));
let callSequence = 0;
const payload = (suffix) => ({object: 0, method: 0, args: {'call-id': label + suffix + (++callSequence), methodName: 'main.Probe.Ping', args: [label + suffix]}});

async function request(mode) {
    const headers = mode === 'json' ? {'Content-Type': 'application/json', 'x-wails-client-id': 'harness'} : {'Content-Type': 'text/plain'};
    try {
        const response = await fetch(target + '/wails/runtime', {method: 'POST', headers, body: JSON.stringify(payload(mode))});
        const body = await response.text();
        report({label, mode, status: response.status, body});
        if (label === 'trusted' && response.status !== (mode === 'json' ? 200 : 415)) throw new Error('unexpected trusted response ' + response.status);
    } catch (error) {
        report({label, mode, fetchError: String(error)});
        if (label === 'trusted') throw error;
    }
}

async function imageGet() {
    const p = payload('get');
    const query = new URLSearchParams({object: String(p.object), method: String(p.method), args: JSON.stringify(p.args)});
    await new Promise((resolve) => {
        const img = new Image();
        img.onload = img.onerror = () => resolve();
        img.src = target + '/wails/runtime?' + query;
        document.body.appendChild(img);
    });
    report({label, mode: 'image-settled'});
}

async function chunks() {
    const body = JSON.stringify(payload('chunk'));
    const half = Math.floor(body.length / 2);
    const id = 'chunk-' + (++callSequence);
    for (let i = 0; i < 2; i++) {
        const response = await fetch(target + '/wails/runtime', {method: 'POST', headers: {'Content-Type': 'application/json', 'x-wails-client-id': 'harness', 'x-wails-chunk-id': id, 'x-wails-chunk-index': String(i), 'x-wails-chunk-total': '2'}, body: i === 0 ? body.slice(0, half) : body.slice(half)});
        const text = await response.text();
        report({label, mode: 'chunk' + i, status: response.status, body: text});
        if (response.status !== 200) throw new Error('unexpected chunk response ' + response.status);
    }
}

async function exercise() {
    report({label, href: location.href, origin: location.origin, top: window === top});
    await request('json');
    await request('plain');
    await imageGet();
    if (label === 'trusted') await chunks();
}

async function forbiddenFrames() {
    const violations = await new Promise((resolve) => {
        let count = 0;
        function violation(event) {
            if (event.effectiveDirective !== 'frame-src' || !event.blockedURI.startsWith(remote)) return;
            report({label, directive: event.effectiveDirective, blockedURI: event.blockedURI});
            if (++count === 2) {
                document.removeEventListener('securitypolicyviolation', violation);
                resolve(count);
            }
        }
        document.addEventListener('securitypolicyviolation', violation);
        for (const sandbox of [false, true]) {
            const f = document.createElement('iframe');
            if (sandbox) f.sandbox = 'allow-scripts';
            f.src = remote + (sandbox ? '/sandbox' : '/frame');
            document.body.appendChild(f);
        }
    });
    report({label, forbiddenFramesBlocked: violations});
}

function foreignFrames() {
    for (const kind of ['frame-after', 'sandbox-after', 'trusted-after']) {
        const f = document.createElement('iframe');
        if (kind === 'sandbox-after') f.sandbox = 'allow-scripts';
        f.src = kind === 'trusted-after' ? target + '/' : remote + '/' + kind;
        document.body.appendChild(f);
    }
}
