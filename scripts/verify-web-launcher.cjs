// Real renderer/controller checks; backend calls are mocked. See the separately
// gated TestApprovedNativeWebDispatch for real engine and browser dispatch.
// PODDER_PLAYWRIGHT_MODULE=/absolute/path/to/playwright node scripts/verify-web-launcher.cjs
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.PODDER_PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(__dirname, '../frontend');
const fixture = `
const mapping={hostIP:'127.0.0.1',hostPort:49160,containerPort:8080,protocol:'tcp'};
const association={key:'container:full-web-id',containerID:'full-web-id',containerName:'Web <safe>'};
const endpoints=[{id:'first',scheme:'http',mapping,path:'/first'},{id:'second',scheme:'http',mapping,path:'/second'}];
const state=window.__webFixture={running:true,opened:false,calls:[],stale:[],definition:{schemaVersion:1,kind:'web',association,endpoints}};
const url=e=>'http://127.0.0.1:49160'+e.path;
const options=()=>({association,definition:state.definition,mappings:[mapping],running:state.running,message:'Configure an explicit endpoint.',stale:state.stale,endpoints:state.definition?state.definition.endpoints.map(e=>({id:e.id,url:url(e)})):[]});
export async function GetSystemInfo(){return {totalContainers:1,runningContainers:1};}
export async function ListContainers(){return [{Id:'full-web-id',Names:['Web <safe>'],State:state.running?'running':'exited',Image:'local-fixture',Command:[],PortMappings:[mapping],provenance:{type:'adhoc'}}];}
export async function GetLauncherOverview(){return {'full-web-id':options()};}
export async function GetLauncherOptions(){return structuredClone(options());}
export async function SaveLauncherDefinition(id,d){state.calls.push({action:'save',id,definition:d});state.definition=d;}
export async function GetWebEndpointURL(id,e){state.calls.push({action:'copy',id,endpoint:e});return url(endpoints.find(x=>x.id===e));}
export async function OpenWebEndpoint(id,e){state.calls.push({action:'open',id,endpoint:e});return {opened:state.opened,url:url(endpoints.find(x=>x.id===e)),message:state.opened?'Sent to the default browser.':'Browser could not be opened. Copy the URL instead.'};}
export async function StartAndOpenWebEndpoint(id,e){state.calls.push({action:'start',id,endpoint:e});state.running=true;return {opened:state.opened,url:url(endpoints.find(x=>x.id===e)),message:state.opened?'Sent to the default browser.':'Container was started, but its HTTP(S) endpoint was not ready within 15 seconds. Check Logs or copy the URL.'};}
`;
const server = http.createServer((req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname.endsWith('/podmanservice.js')) { res.setHeader('content-type', 'text/javascript'); res.end(fixture); return; }
    const relative = pathname === '/' ? 'index.html' : pathname.slice(1);
    let target = path.resolve(root, relative);
    if (!target.startsWith(root + path.sep)) { res.writeHead(403).end(); return; }
    if (!fs.existsSync(target)) target = path.resolve(root, 'public', relative);
    if (!fs.existsSync(target) || !fs.statSync(target).isFile()) { res.writeHead(404).end(); return; }
    res.setHeader('content-type', { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.jpg': 'image/jpeg', '.png': 'image/png', '.ttf': 'font/ttf' }[path.extname(target)] || 'application/octet-stream');
    fs.createReadStream(target).pipe(res);
});
(async () => {
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const browser = await chromium.launch({ headless: true, chromiumSandbox: true });
    try {
        const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
        const errors = [];
        page.on('pageerror', error => errors.push(String(error)));
        await page.addInitScript(() => Object.defineProperty(navigator, 'clipboard', { value: { writeText: async text => { if (window.__denyClipboard) throw Error('Denied'); window.__copiedURL = text; } } }));
        await page.goto(`http://127.0.0.1:${server.address().port}`);
        await page.getByRole('button', { name: 'Containers', exact: true }).click();
        await page.getByRole('button', { name: 'Choose Endpoint', exact: true }).click();
        await page.locator('#launcher-choice').selectOption('1');
        await page.locator('#launcher-remember').check();
        await page.locator('#launcher-choose-open').click();
        await page.locator('#launcher-result-modal.active').waitFor();
        assert.equal(await page.locator('#launcher-result-url').inputValue(), 'http://127.0.0.1:49160/second');
        let calls = await page.evaluate(() => window.__webFixture.calls);
        assert.equal(calls[0].definition.preferredEndpointID, 'second');
        assert.deepEqual(calls[1], { action: 'open', id: 'full-web-id', endpoint: 'second' });
        await page.locator('#launcher-copy-url').click();
        assert.equal(await page.evaluate(() => window.__copiedURL), 'http://127.0.0.1:49160/second');
        await page.evaluate(() => { window.__denyClipboard = true; });
        await page.locator('#launcher-copy-url').click();
        assert.equal(await page.locator('#launcher-result-url').evaluate(input => input.value.slice(input.selectionStart, input.selectionEnd)), 'http://127.0.0.1:49160/second');
        await page.locator('#launcher-result-modal').getByRole('button', { name: 'Close', exact: true }).click();
        await page.evaluate(async () => { window.__webFixture.opened = true; await window.refreshAll(); });
        await page.getByRole('button', { name: 'Open Web', exact: true }).click();
        await page.locator('#notification-text').getByText('Sent to the default browser.', { exact: true }).waitFor();
        assert.equal(await page.locator('#launcher-result-modal').isVisible(), false);
        await page.evaluate(async () => { window.__webFixture.running = false; window.__webFixture.definition.openAfterStart = true; await window.refreshAll(); });
        await page.getByRole('button', { name: 'Start & Open', exact: true }).click();
        await page.getByRole('button', { name: 'Open Web', exact: true }).waitFor();
        calls = await page.evaluate(() => window.__webFixture.calls);
        assert.equal(calls.filter(call => call.action === 'start').length, 1);
        assert.equal(calls.find(call => call.action === 'start').endpoint, 'second');
        await page.evaluate(async () => { window.__webFixture.running = false; window.__webFixture.opened = false; await window.refreshAll(); });
        await page.getByRole('button', { name: 'Start & Open', exact: true }).click();
        await page.locator('#launcher-result-modal.active').waitFor();
        assert.match(await page.locator('#launcher-result-message').textContent(), /not ready within 15 seconds/);
        assert.equal(await page.locator('#launcher-result-url').inputValue(), 'http://127.0.0.1:49160/second');
        await page.locator('#launcher-result-modal').getByRole('button', { name: 'Close', exact: true }).click();
        await page.evaluate(async () => { const s=window.__webFixture; s.stale=[{...s.definition,association:{...s.definition.association,key:'container:old-id',containerID:'old-id'}}]; s.definition=null; s.calls=[]; await window.refreshAll(); });
        assert.equal(await page.getByRole('button', { name: 'Open Web', exact: true }).count(), 0);
        await page.locator('.launcher-menu summary').click();
        await page.getByRole('button', { name: 'Configure Application', exact: true }).click();
        await page.getByRole('button', { name: 'Review settings from older container old-id', exact: true }).click();
        assert.equal((await page.evaluate(() => window.__webFixture.calls)).length, 0);
        await page.locator('#launcher-save').click();
        calls = await page.evaluate(() => window.__webFixture.calls);
        assert.equal(calls.length, 1);
        assert.equal(calls[0].action, 'save');
        assert.equal(calls[0].definition.association.containerID, 'full-web-id');
        assert.deepEqual(errors, []);
        console.log('PASS: real renderer endpoint choice/preference, Open Web, Start & Open, browser/readiness failure, clipboard/manual-copy fallback, explicit stale association. Backend mocked; browser sandbox enabled.');
    } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(() => server.close());
