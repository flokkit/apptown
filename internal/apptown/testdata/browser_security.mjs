// Optional real-browser tier. Requires Playwright + Chromium; no live AppTown.
import assert from 'node:assert/strict';
import { checkResources } from './browser_resources.mjs';
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE || 'playwright');
const apptown = process.env.APPTOWN_BROWSER_URL;
const origins = [process.env.APPTOWN_SIBLING_URL, process.env.APPTOWN_OTHER_URL];
const callback = process.env.APPTOWN_OTHER_URL + '/callback';
const change = process.env.APPTOWN_BROWSER_CHANGE;
const consoleID = process.env.APPTOWN_BROWSER_CONSOLE;
const browser = await chromium.launch({
  headless: true,
  // The fixture's self-signed TLS certificate also covers WSS connections.
  args: ['--ignore-certificate-errors', '--no-proxy-server', '--host-resolver-rules=MAP *.example.test 127.0.0.1, MAP *.other.test 127.0.0.1'],
});
const context = await browser.newContext({ ignoreHTTPSErrors: true, acceptDownloads: true });
context.setDefaultTimeout(10000);
const page = await context.newPage();
page.setDefaultTimeout(10000);
try {
  await page.goto(apptown + '/');
  const login = await page.evaluate(async () => {
    const r = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'boss', password: 'boss-password' }) });
    return r.status;
  });
  assert.equal(login, 200);
  const cookie = (await context.cookies(apptown)).find(c => c.name === '__Host-apptown_session');
  assert.ok(cookie?.secure && cookie.httpOnly && cookie.path === '/' && !cookie.domain.startsWith('.'));

  // The permitted terminal uses the actual handler and carries raw input.
  const terminal = await page.evaluate(consoleID => new Promise(resolve => {
    const ws = new WebSocket(location.origin.replace('https:', 'wss:') + `/api/v1/console/${consoleID}/pty`);
    let replay = '';
    ws.onmessage = async e => {
      const text = typeof e.data === 'string' ? e.data : await e.data.text();
      if (!replay) {
        replay = text;
        ws.send(new TextEncoder().encode('legitimate-terminal-input'));
        return;
      }
      if (text === 'terminal-input-recorded') {
        ws.close();
        resolve(replay);
      }
    };
    ws.onerror = () => resolve('failed');
  }), consoleID);
  assert.equal(terminal, 'terminal-ready');

  await checkResources(page, { apptown, other: origins[1], consoleID, reporting: process.env.APPTOWN_REPORTING_URL });

  const client = await page.evaluate(async callback => {
    const r = await fetch('/oauth/register', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ client_name: 'Browser fixture', redirect_uris: [callback] }) });
    if (r.status !== 201) throw new Error('registration failed');
    return (await r.json()).client_id;
  }, callback);
  const request = { response_type: 'code', client_id: client, redirect_uri: callback,
    code_challenge_method: 'S256', code_challenge: 'browser-fixture-challenge', scope: 'apptown:user:read', state: 'original' };
  const consentURL = apptown + '/oauth/authorize?' + new URLSearchParams(request);

  // Login from OAuth itself must work under script-src self, before a cookie
  // exists. Preauthenticated consent alone does not exercise its script.
  const fresh = await browser.newContext({ ignoreHTTPSErrors: true });
  fresh.setDefaultTimeout(10000);
  const oauth = await fresh.newPage();
  await oauth.goto(consentURL);
  await oauth.locator('input[name="username"]').fill('boss');
  await oauth.locator('input[name="password"]').fill('wrong-password');
  await oauth.getByRole('button', { name: 'Sign in', exact: true }).click();
  await oauth.locator('#err').filter({ hasText: /./ }).waitFor();
  await oauth.locator('input[name="password"]').fill('boss-password');
  await oauth.getByRole('button', { name: 'Sign in', exact: true }).click();
  await oauth.getByRole('button', { name: 'Allow', exact: true }).waitFor();
  await oauth.getByRole('button', { name: 'Cancel', exact: true }).click();
  await oauth.waitForURL(url => url.href.startsWith(callback + '?'));
  assert.equal(new URL(oauth.url()).searchParams.get('error'), 'access_denied');
  await oauth.goto(consentURL);
  await oauth.getByRole('button', { name: 'Allow', exact: true }).click();
  await oauth.waitForURL(url => url.href.startsWith(callback + '?'));
  assert.ok(new URL(oauth.url()).searchParams.get('code'));
  await fresh.close();

  for (const origin of origins) {
    const attacker = await context.newPage();
    await attacker.goto(origin);
    // Record actual browser headers/status: the sibling attack carries the
    // victim's cookie and Sec-Fetch-Site=same-site, yet must still be refused.
    // Top-level form navigation makes the server denial observable without
    // CORS hiding a fetch response. text/plain form encoding can carry valid
    // JSON: the inserted '=' becomes an irrelevant JSON string value.
    const mutationRequest = context.waitForEvent('request', { predicate: r => r.url() === apptown + '/api/v1/projects' && r.method() === 'POST' });
    const mutationPopup = attacker.waitForEvent('popup');
    await attacker.evaluate(apptown => {
      const form = document.createElement('form');
      form.method = 'POST'; form.enctype = 'text/plain'; form.target = '_blank'; form.action = apptown + '/api/v1/projects';
      const input = document.createElement('input');
      input.name = '{"slug":"csrf-created","name":"apptownd","padding":"'; input.value = '"}';
      form.append(input); document.body.append(form); form.submit();
    }, apptown);
    const posted = await mutationRequest;
    assert.equal(JSON.parse(posted.postData()).slug, 'csrf-created');
    const mutationPage = await mutationPopup;
    await mutationPage.waitForLoadState();
    assert.equal((await posted.response()).status(), 403);
    const headers = await posted.allHeaders();
    if (origin.includes('preview.example.test')) {
      assert.equal(headers['sec-fetch-site'], 'same-site');
      assert.ok(headers.cookie?.includes('__Host-apptown_session='), 'sibling test must carry the real victim cookie');
    }
    await mutationPage.close();

    const wsResponse = attacker.waitForEvent('websocket').then(ws => new Promise(resolve => {
      ws.on('socketerror', () => resolve('refused'));
      ws.on('framereceived', () => resolve('opened'));
    }));
    const wsResult = await attacker.evaluate(({ apptown, consoleID }) => new Promise(resolve => {
      const ws = new WebSocket(apptown.replace('https:', 'wss:') + `/api/v1/console/${consoleID}/pty`);
      ws.onopen = () => { ws.send(new TextEncoder().encode('apptownd-terminal-input')); ws.close(); resolve('opened'); };
      ws.onerror = () => resolve('refused');
    }), { apptown, consoleID });
    assert.equal(wsResult, 'refused');
    assert.equal(await wsResponse, 'refused');

    // A cross-origin form targets a new tab (not an iframe), so framing
    // protection cannot mask a missing consent/CSRF defense.
    const popupPromise = attacker.waitForEvent('popup');
    await attacker.evaluate(({ apptown, request }) => {
      const form = document.createElement('form');
      form.method = 'POST'; form.action = apptown + '/oauth/authorize'; form.target = '_blank';
      for (const [name, value] of Object.entries({ ...request, decision: 'allow' })) {
        const input = document.createElement('input'); input.name = name; input.value = value; form.append(input);
      }
      document.body.append(form); form.submit();
    }, { apptown, request });
    const popup = await popupPromise;
    await popup.waitForLoadState();
    assert.match(await popup.locator('body').innerText(), /open AppTown directly/);
    assert.ok(!popup.url().includes('code='));
    await popup.close();

    // Sensitive pages cannot be displayed inside an attacker's frame.
    const frameDenied = attacker.waitForEvent('console', { predicate: msg => /frame-ancestors|X-Frame-Options/.test(msg.text()) });
    await attacker.evaluate(url => {
      const iframe = document.createElement('iframe'); iframe.src = url; document.body.append(iframe);
    }, consentURL);
    await frameDenied;
    assert.equal(await attacker.locator('iframe').contentFrame().getByRole('button', { name: 'Allow', exact: true }).count(), 0);
    await attacker.close();
  }

  // Honest consent reaches the registered HTTPS client callback; changes
  // and replay do not. Every origin is served by the local TLS fixture.
  await page.goto(consentURL);
  const form = await page.locator('form[action="/oauth/authorize"]').evaluate(form => Object.fromEntries(new FormData(form)));
  assert.ok(form.consent_token);
  const changed = await page.evaluate(async form => {
    const r = await fetch('/oauth/authorize', { method: 'POST', body: new URLSearchParams({ ...form, state: 'apptownd', decision: 'allow' }) });
    return r.status;
  }, form);
  assert.equal(changed, 403);
  const authorization = page.waitForResponse(r => r.url() === apptown + '/oauth/authorize' && r.request().method() === 'POST');
  await page.getByRole('button', { name: 'Allow', exact: true }).click();
  const authorized = await authorization;
  assert.equal(authorized.status(), 302, 'honest consent: ' + await authorized.text().catch(() => 'no response body'));
  await page.waitForURL(url => url.href.startsWith(callback + '?')).catch(async err => {
    throw new Error(`callback navigation: ${page.url()} ${await page.locator('body').innerText()}`, { cause: err });
  });
  assert.ok(new URL(page.url()).searchParams.get('code'));
  await page.goto(apptown + '/');
  const replay = await page.evaluate(async form => {
    const r = await fetch('/oauth/authorize', { method: 'POST', body: new URLSearchParams({ ...form, decision: 'allow' }) });
    return r.status;
  }, form);
  assert.equal(replay, 403);

  // Real uploads, misleading extensions and console uploads must download.
  const active = `<script>fetch('/api/v1/projects',{method:'POST',body:JSON.stringify({slug:'file-executed',name:'bad'})})</script>`;
  for (const name of ['attack.html', 'pretend.png', 'attack.svg']) {
    assert.equal(await page.evaluate(async ({ name, active, change }) => {
      const data = new FormData(); data.append('file', new Blob([active]), name);
      return (await fetch(`/api/v1/changes/${change}/attachments`, { method: 'POST', body: data })).status;
    }, { name, active, change }), 201);
    const download = page.waitForEvent('download');
    await page.evaluate(url => { const a = document.createElement('a'); a.href = url; document.body.append(a); a.click(); },
      `/api/v1/changes/${change}/attachments/${name}`);
    assert.equal((await download).suggestedFilename(), name);
  }
  const download = page.waitForEvent('download');
  await page.evaluate(consoleID => { const a = document.createElement('a'); a.href = `/api/v1/console/${consoleID}/uploads/picture-1.png`; document.body.append(a); a.click(); }, consoleID);
  assert.equal((await download).suggestedFilename(), 'picture-1.png');
  assert.equal(await page.evaluate(consoleID => new Promise(resolve => {
    const img = new Image(); img.onload = () => resolve(img.naturalWidth); img.onerror = () => resolve(0);
    img.src = `/api/v1/console/${consoleID}/uploads/picture-2.png`; document.body.append(img);
  }), consoleID), 1);
  console.log('PASS: secure cookies, same-site and cross-site CSRF/PTY/consent denial, framing, honest consent, replay, downloads and inline raster images');
} finally {
  await context.close();
  await browser.close();
}
