// Actual built UI and browser enforcement, with local network witnesses in Go.
import assert from 'node:assert/strict';

export async function checkResources(page, { apptown, other, consoleID, reporting }) {
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aXioAAAAASUVORK5CYII=', 'base64');
  const tour = 'https://apptown.dev/tour/v1.0.0-rc.9/policy-proof.png';
  await page.route(tour, route => route.fulfill({ contentType: 'image/png', body: png }));
  await page.route('**/api/v1/docs/guides/browser-policy', route => route.fulfill({ json: {
    slug: 'browser-policy', content: `# Resource policy

![Local](/api/v1/console/${consoleID}/uploads/picture-2.png)
![Tour](${tour})
![Data](data:image/png;base64,${png.toString('base64')})
![Remote](${other}/blocked/markdown.png)
<picture><source srcset="${other}/blocked/source.png"><img alt="Alternative" src="${tour}" srcset="${other}/blocked/srcset.png 2x"></picture>

\`\`\`mermaid
flowchart LR
  Request --> Evidence
\`\`\`
` } }));
  await page.goto(apptown + '/docs/guides/browser-policy');
  // Mermaid creates an empty SVG before layout; wait for the completed graph.
  await page.waitForFunction(() => {
    const svg = document.querySelector('.md-doc pre.mermaid svg');
    const box = svg?.getBoundingClientRect();
    return svg?.querySelector('.node') && box.width > 0 && box.height > 0;
  });
  const diagram = await page.locator('.md-doc svg').boundingBox();
  assert.ok(diagram?.width > 0 && diagram?.height > 0, 'Mermaid must render under CSP');
  for (const alt of ['Local', 'Tour', 'Data', 'Alternative']) {
    await page.waitForFunction(alt => document.querySelector(`img[alt="${alt}"]`)?.naturalWidth > 0, alt);
  }
  assert.equal(await page.getByRole('link', { name: 'Remote (external image)', exact: true }).getAttribute('href'), other + '/blocked/markdown.png');
  assert.equal(await page.locator('.md-doc img[srcset], .md-doc source').count(), 0);
  const fonts = await page.evaluate(async () => {
    const loaded = await Promise.all(['IBM Plex Sans', 'IBM Plex Mono', 'Source Serif 4'].map(f => document.fonts.load(`16px "${f}"`)));
    return loaded.map(faces => faces.length > 0 && faces.every(f => f.status === 'loaded'));
  });
  assert.deepEqual(fonts, [true, true, true], 'bundled fonts must load');
  // Capture a real SDK envelope; the Go fixture verifies its body arrived.
  const envelope = page.waitForResponse(r => r.url().startsWith(reporting + '/api/7/envelope/') && r.request().method() === 'POST');
  await page.evaluate(() => { setTimeout(() => { throw new Error('resource-policy-browser-proof'); }, 0); });
  assert.equal((await envelope).status(), 200);

  await page.route('**/policy-eval.js', route => route.fulfill({ contentType: 'text/javascript', body:
    "window.localPolicyExecuted=true; try { eval('window.evalPolicyExecuted=true'); } catch { window.evalPolicyDenied=true; }" }));
  // Evaluate creates DOM resources, not an exemption for the resources themselves.
  const refusals = await page.evaluate(async other => {
    const violations = [];
    document.addEventListener('securitypolicyviolation', e => violations.push(e.effectiveDirective));
    const external = (tag, attrs) => new Promise(resolve => {
      const el = document.createElement(tag); Object.assign(el, attrs);
      el.onload = () => resolve('loaded'); el.onerror = () => resolve('blocked'); document.head.append(el);
    });
    const inline = document.createElement('script'); inline.textContent = 'window.inlinePolicyExecuted=true'; document.head.append(inline);
    // Eval must be called by an actual script, not Playwright's exempt evaluator.
    await external('script', { src: '/policy-eval.js' });
    const results = await Promise.all([
      external('script', { src: other + '/blocked/script.js' }),
      external('link', { rel: 'stylesheet', href: other + '/blocked/style.css' }),
      new FontFace('BlockedFont', `url(${other}/blocked/font.woff2)`).load().then(() => 'loaded', () => 'blocked'),
      fetch(other + '/blocked/connect').then(() => 'loaded', () => 'blocked'),
      external('img', { src: other + '/blocked/image.png' }),
    ]);
    return { results, inlineExecuted: !!window.inlinePolicyExecuted, localExecuted: !!window.localPolicyExecuted, evalDenied: !!window.evalPolicyDenied, evalExecuted: !!window.evalPolicyExecuted, violations };
  }, other);
  assert.deepEqual(refusals.results, ['blocked', 'blocked', 'blocked', 'blocked', 'blocked']);
  assert.equal(refusals.inlineExecuted, false);
  assert.equal(refusals.localExecuted, true);
  assert.equal(refusals.evalDenied, true);
  assert.equal(refusals.evalExecuted, false);
  for (const directive of ['script-src', 'script-src-elem', 'style-src-elem', 'font-src', 'connect-src', 'img-src']) {
    assert.ok(refusals.violations.includes(directive), `missing actual CSP violation ${directive}`);
  }
  // An approved image origin cannot redirect the request to another origin.
  await page.route('https://apptown.dev/tour/policy-redirect.png', route => route.fulfill({ status: 302, headers: { location: other + '/blocked/redirect.png' } }));
  assert.equal(await page.evaluate(() => new Promise(resolve => {
    const img = new Image(); img.onload = () => resolve('loaded'); img.onerror = () => resolve('blocked');
    img.src = 'https://apptown.dev/tour/policy-redirect.png'; document.body.append(img);
  })), 'blocked');

  await page.evaluate(() => localStorage.setItem('chat-zoom', 'terminal'));
  await page.goto(apptown + `/projects/p/console?session=${consoleID}`);
  await page.locator('.xterm-screen').waitFor();
  const terminal = await page.locator('.xterm-screen').boundingBox();
  assert.ok(terminal?.width > 0 && terminal?.height > 0, 'xterm needs its styles');
  await page.waitForFunction(() => document.querySelector('.xterm-rows')?.textContent.includes('terminal-ready'));
  await page.goto(apptown + '/');
  console.log('PASS: built Markdown image policy, Mermaid, xterm, local fonts, reporting envelope and CSP resource refusals');
}
