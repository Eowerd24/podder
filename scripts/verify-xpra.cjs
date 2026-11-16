// Optional live Xpra acceptance. Use only with a separately approved fixture.
// Password is read from a private test file, never a URL or command argument.
const fs = require('node:fs');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.PODDER_PLAYWRIGHT_MODULE || 'playwright');
const password = fs.readFileSync(process.env.PODDER_GUI_PASSWORD_FILE, 'utf8').trim();
(async () => {
    let lastPage;
    const browser = await chromium.launch({ headless: true, chromiumSandbox: true });
    try {
        async function connect(secret, expectDenial = false) {
            const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
            const page = await context.newPage(); lastPage = page;
            await page.goto(new URL('/connect.html', process.env.PODDER_GUI_URL).href);
            await page.locator('#username').fill('xpra');
            await page.locator('#password').fill(secret);
            await page.evaluate(() => { document.getElementById('reconnect').checked = false; });
            const authenticationFailure = expectDenial ? page.waitForEvent('console', { predicate: message => /closing:.*authentication failed/i.test(message.text()), timeout: 20000 }) : null;
            await Promise.all([page.waitForURL('**/index.html**'), page.locator('#main_action_button').click()]);
            assert(!page.url().includes(password), 'password must not enter browser URL');
            return { page, context, authenticationFailure };
        }
        const denied = await connect('incorrect-test-password', true);
        await denied.authenticationFailure; // actual server challenge rejection, before the client's redirect
        await denied.context.close();
        const accepted = await connect(password);
        await accepted.page.waitForFunction(() => typeof client !== 'undefined' && client.connected && Object.keys(client.id_to_window).length > 0, null, { timeout: 30000 });
        await accepted.page.evaluate(() => client.set_focus(Object.values(client.id_to_window)[0]));
        await accepted.page.keyboard.type("printf '\\033]0;PODDER_GUI_AUTH_OK\\007'", { delay: 10 });
        await accepted.page.keyboard.press('Enter');
        await accepted.page.waitForFunction(() => Object.values(client.id_to_window).some(w => w.title.includes('PODDER_GUI_AUTH_OK')), null, { timeout: 10000 });
        if (process.env.PODDER_GUI_SCREENSHOT) await accepted.page.screenshot({ path: process.env.PODDER_GUI_SCREENSHOT });
        console.log('PASS: wrong password rejected; authenticated Xpra window rendered; keyboard input executed inside the isolated GUI fixture.');
        await accepted.context.close();
    } catch (err) { if (lastPage && process.env.PODDER_GUI_SCREENSHOT) await lastPage.screenshot({ path: process.env.PODDER_GUI_SCREENSHOT + ".failure.png" }).catch(() => {}); throw err; } finally { await browser.close(); }
})().catch(err => { console.error(String(err.stack || err).split(password).join('[redacted]')); process.exitCode = 1; });
