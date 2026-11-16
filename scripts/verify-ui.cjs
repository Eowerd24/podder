// Renderer integration test. Uses an existing Playwright install; no production dependency.
// PODDER_PLAYWRIGHT_MODULE=/absolute/path/to/playwright node scripts/verify-ui.cjs
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.PODDER_PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(__dirname, '../frontend');
const fixture = `
const mapping={hostIP:'127.0.0.1',hostPort:49160,containerPort:80,protocol:'tcp'};
const association={key:'container:full-web-id',containerID:'full-web-id',containerName:'Web <safe>'};
let definition={schemaVersion:1,kind:'web',association,endpoints:[{id:'web',scheme:'http',mapping,path:'/'}]};
const containers=[{Id:'full-web-id',Names:['Web <safe>'],Image:'nginx',State:'running',Command:[],PortMappings:[mapping],provenance:{type:'adhoc'}},{Id:'db-id',Names:['Database'],State:'running',PortMappings:[{...mapping,hostPort:5432,containerPort:5432}],Command:[]}];
let sessions=[];let tunnels=[];window.__podderTestCalls=[];
const call=(name,...args)=>window.__podderTestCalls.push({name,args});
const options=()=>({association,definition,mappings:[mapping],endpoints:['web','gui'].includes(definition.kind)?[{id:'web',url:'http://127.0.0.1:49160/'}]:[],running:true,message:'Configured <safe>',stale:[]});
export async function GetSystemInfo(){return {totalContainers:2,runningContainers:2,memTotal:1000};}
export async function ListContainers(){return containers;}
export async function GetLauncherOverview(){return {'full-web-id':options()};}
export async function GetLauncherOptions(){return options();}
export async function SaveLauncherDefinition(id,d){definition=d;call('saveLauncher',id,d);}
export async function OpenWebEndpoint(){return {opened:false,url:'http://127.0.0.1:49160/',message:'Browser unavailable: copy URL'};}
export async function OpenGUIEndpoint(){call('openGUI');return {opened:true,url:'http://127.0.0.1:49160/',message:'Viewer opened'};}
export async function GetWebEndpointURL(){return 'http://127.0.0.1:49160/';}
export async function OpenConfiguredTerminal(id,confirmed){call('terminal',id,confirmed);}
export async function FindFreePort(){return 49160;}
export async function ListDisposableSessions(){return sessions;}
export async function ListSessionRecipes(){return [];}
export async function SessionStarterProfiles(){return [];}
export async function PreviewDisposableSession(s){call('preview',s);return {spec:{...s,launchID:'aaaaaaaaaaaaaaaaaaaaaaaaaa'},argv:['run','-d','--rm','--mount','owned-dir'],summary:['Preview retention']};}
export async function LaunchDisposableSession(s){call('launch',s);const state={id:'aaaaaaaaaaaaaaaaaaaaaaaaaa',state:'running',containerID:'full-web-id',spec:s,retained:[],message:'Session running'};sessions=[state];return state;}
export async function PreviewSessionCleanup(id){return {id,containerID:'full-web-id',temporaryDirectory:'/owned/session',retained:[],warning:'Only owned temporary artifacts'};}
export async function StopAndCleanupSession(id,confirmed,remove){call('cleanup',id,confirmed,remove);sessions=[{...sessions[0],state:'stopped',message:'Owned resources cleaned'}];return sessions[0];}
export async function ReviewGitHubSession(s){call('review',s);return {id:'bbbbbbbbbbbbbbbbbbbbbbbbbb',state:'draft',spec:s,buildDefinition:'FROM scratch',buildDigest:'hash',message:'Review immutable source'};}
export async function StartReviewedSessionBuild(id,confirm,network){call('build',id,confirm,network);sessions=[{id,state:'built',imageID:'sha256:image',spec:{schemaVersion:1,name:'github-demo',image:'sha256:image',policy:'temporary',portMappings:[],requiredEnv:[],config:{},data:{}},retained:[],message:'Built'}];return sessions[0];}
export async function LaunchBuiltSession(id,env,command){call('launchBuilt',id,env,command);sessions[0].state='running';return sessions[0];}
export async function SaveRemoteProfile(p){call('remoteSave',p);}
export async function ListRemoteProfiles(){return [{name:'lab',user:'app',host:'lab.test'}];}
export async function ListRemoteContainers(){return [containers[0]];}
export async function GetRemoteLauncherOptions(){return options();}
export async function SaveRemoteLauncherDefinition(profile,id,d){definition=d;call('remoteLauncher',profile,id);}
export async function OpenRemoteWebEndpoint(profile,id,endpoint,consent){call('remoteOpen',profile,id,endpoint,consent);tunnels=[{id:'tunnel-id',profile:'lab',url:'http://127.0.0.1:55555/',state:'active'}];return {opened:true,url:tunnels[0].url,message:'Forwarded'};}
export async function ListRemoteTunnels(){return tunnels;}
export async function CloseRemoteTunnel(id){call('closeTunnel',id);tunnels=[];}
`;
const server = http.createServer((req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname.endsWith('/podmanservice.js')) { res.setHeader('content-type', 'text/javascript'); res.end(fixture); return; }
    const relative = pathname === '/' ? 'index.html' : pathname.slice(1);
    let target = path.resolve(root, relative);
    if (!target.startsWith(root + path.sep)) { res.writeHead(403).end(); return; }
    if (!fs.existsSync(target)) target = path.resolve(root, 'public', relative);
    if (!fs.existsSync(target) || !fs.statSync(target).isFile()) { res.writeHead(404).end(); return; }
    const type = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml' }[path.extname(target)] || 'application/octet-stream';
    res.setHeader('content-type', type); fs.createReadStream(target).pipe(res);
});
(async () => {
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const browser = await chromium.launch({ headless: true, chromiumSandbox: true });
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = []; page.on('pageerror', err => errors.push(String(err)));
    page.on('dialog', dialog => dialog.accept());
    try {
        await page.goto(`http://127.0.0.1:${server.address().port}`);
        await page.getByRole('button', { name: 'Containers', exact: true }).click();
        await page.getByRole('button', { name: 'Open Web', exact: true }).waitFor();
        assert.equal(await page.getByRole('button', { name: 'Open Web', exact: true }).count(), 1, 'database must not acquire browser action');
        await page.getByRole('button', { name: 'Open Web', exact: true }).click();
        await page.locator('#launcher-result-modal.active').waitFor(); assert.equal(await page.locator('#launcher-result-url').inputValue(), 'http://127.0.0.1:49160/');
        await page.locator('#launcher-result-modal').getByRole('button', { name: 'Close', exact: true }).click();
        await page.locator('.launcher-menu summary').first().click();
        await page.getByRole('button', { name: 'Configure Application' }).first().click();
        await page.locator('#launcher-modal.active').waitFor();
        await page.locator('#launcher-kind').selectOption('gui'); await page.locator('#launcher-gui-identity').fill('reviewed-xpra'); await page.locator('#launcher-gui-auth').check(); await page.locator('#launcher-save').click();
        await page.getByRole('button', { name: 'Launch App', exact: true }).click();
        await page.getByRole('button', { name: 'Sessions', exact: true }).click();
        await page.locator('#session-new').click(); await page.locator('#session-name').fill('demo'); await page.locator('#session-image').fill('nginx:alpine');
        await page.locator('#session-add-port').click(); await page.locator('#session-web').check(); await page.locator('#session-keep-id').check(); await page.locator('#session-preview').click();
        await page.locator('#session-launch:not([hidden])').waitFor(); assert.match(await page.locator('#session-preview-output').textContent(), /--rm/);
        await page.locator('#session-name').fill('revised'); assert.equal(await page.locator('#session-launch').isVisible(), false, 'edited form invalidates preview');
        await page.locator('#session-preview').click(); await page.locator('#session-launch').click();
        await page.getByRole('button', { name: 'Stop / cleanup', exact: true }).click(); await page.locator('#session-cleanup-confirm').check(); await page.locator('#session-cleanup-apply').click();
        await page.locator('#session-list').getByText('Owned resources cleaned', { exact: true }).waitFor();
        await page.locator('#session-new').click(); await page.locator('#session-source').selectOption('github'); await page.locator('#session-name').fill('github-demo'); await page.locator('#session-repo').fill('https://github.com/example/demo'); await page.locator('#session-commit').fill('b'.repeat(40)); await page.locator('#session-preview').click();
        await page.getByText('FROM scratch', { exact: false }).waitFor(); await page.locator('#session-build-approve').check(); await page.locator('#session-build').click();
        await page.getByRole('button', { name: 'Launch built image', exact: true }).click(); await page.locator('#session-preview').click(); await page.locator('#session-launch').click();
        const calls = await page.evaluate(() => window.__podderTestCalls);
        assert.equal(calls.find(c => c.name === 'launch').args[0].keepID, true, 'explicit UID mapping reaches the launch request');
        assert.equal(calls.filter(c => c.name === 'launch').length, 1, 'built image must use separate launch API exactly once'); assert.equal(calls.filter(c => c.name === 'launchBuilt').length, 1);
        await page.getByRole('button', { name: 'Remote', exact: true }).click(); await page.getByText('Add / replace SSH profile', { exact: true }).click(); await page.locator('#remote-name').fill('lab'); await page.locator('#remote-host').fill('lab.test'); await page.locator('#remote-user').fill('app'); await page.locator('#remote-socket').fill('/run/user/1000/podman/podman.sock'); await page.locator('#remote-known-hosts').fill('/trust/known_hosts'); await page.locator('#remote-save').click(); await page.locator('#remote-observe').click(); await page.getByRole('button', { name: 'Configure / Open application', exact: true }).click();
        await page.locator('#remote-kind').selectOption('web'); await page.locator('#remote-application-save').click(); await page.locator('#remote-open').click();
        await page.locator('#remote-application-modal').getByRole('button', { name: '×' }).click(); await page.getByRole('button', { name: 'Close / dismiss tunnel' }).click();
        assert.equal((await page.evaluate(() => window.__podderTestCalls)).find(c => c.name === 'remoteSave').args[0].knownHostsFile, '/trust/known_hosts');
        assert.deepEqual(errors, [], 'renderer should have no uncaught errors');
        if (process.env.PODDER_UI_SCREENSHOT) await page.screenshot({ path: process.env.PODDER_UI_SCREENSHOT, fullPage: true });
        console.log('PASS: real renderer, escaped metadata, explicit Open/GUI, session preview invalidation, launch/cleanup, GitHub review/build/launch, SSH UI. External operations mocked.');
    } finally { await browser.close(); server.close(); }
})().catch(err => { console.error(err); server.close(); process.exitCode = 1; });
