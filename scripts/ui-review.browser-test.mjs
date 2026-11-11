// The review images retain page coordinates and never substitute evidence.
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE);
const root = await mkdtemp(join(tmpdir(), 'ui-review-test-'));
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ deviceScaleFactor: 1 });
const captures = join(root, 'captures'), out = join(root, 'sheets');
const run = (...args) => spawnSync(process.execPath, [resolve('scripts/ui-review.mjs'), ...args], { encoding: 'utf8' });
const band = y => [Math.floor(y / 100) * 7 % 256, Math.floor(y / 100) * 11 % 256, Math.floor(y / 100) * 17 % 256];
async function inspect(path, x = 0, y = 0) {
  const data = await readFile(path);
  return page.evaluate(async ({ data, x, y }) => {
    const im = new Image(); im.src = `data:image/jpeg;base64,${data}`; await im.decode();
    const c = document.createElement('canvas'); c.width = im.width; c.height = im.height;
    const ctx = c.getContext('2d'); ctx.drawImage(im, 0, 0);
    return { width: im.width, height: im.height, rgb: [...ctx.getImageData(x, y, 1, 1).data].slice(0, 3) };
  }, { data: data.toString('base64'), x, y });
}
const near = (actual, expected) => actual.forEach((v, i) => assert.ok(Math.abs(v - expected[i]) < 12, `${actual} != ${expected}`));
try {
  for (const [name, width, height] of [['phone', 390, 3000], ['long', 390, 6000], ['desktop', 1440, 1200]]) {
    await mkdir(join(captures, name), { recursive: true });
    await page.setViewportSize({ width, height });
    await page.setContent(`<style>body{margin:0}</style>${Array.from({ length: height / 100 }, (_, n) => `<div style="height:100px;background:rgb(${band(n * 100)})"></div>`).join('')}`);
    await page.screenshot({ path: join(captures, name, 'phone-light.png') });
  }
  await writeFile(join(captures, 'pages.json'), '{}');
  let r = run('sheets', captures, out); assert.equal(r.status, 0, r.stderr);
  assert.deepEqual(JSON.parse(await readFile(join(out, 'index.json'))), {
    desktop: ['phone-light-1.jpg'], long: ['phone-light-1.jpg', 'phone-light-2.jpg'], phone: ['phone-light-1.jpg'],
  });
  let im = await inspect(join(out, 'phone/phone-light-1.jpg'), 414 + 30, 28 + 20);
  assert.equal(im.width, 1218); assert.equal(im.height, 1328); near(im.rgb, band(1300));
  near((await inspect(join(out, 'phone/phone-light-1.jpg'), 828 + 30, 28 + 320)).rgb, band(2900));
  assert.equal((await inspect(join(out, 'long/phone-light-1.jpg'))).width, 1632);
  assert.equal((await inspect(join(out, 'long/phone-light-2.jpg'))).width, 390);
  assert.equal((await inspect(join(out, 'desktop/phone-light-1.jpg'))).width, 1440);
  const finding = y => ({ page: 'phone', variant: 'phone-light', y, kind: 'product', category: 'layout', severity: 'high', what: 'Visible problem.', element: 'row' });
  const a = join(root, 'a.json'), b = join(root, 'b.json'), crops = join(root, 'report');
  await writeFile(a, JSON.stringify([finding([1300, 1400]), finding([2950, 3000])]));
  await writeFile(b, JSON.stringify([finding([0, 2000]), { ...finding([0, 100]), variant: 'missing' }]));
  r = run('crops', captures, crops, a, b); assert.equal(r.status, 1); assert.match(r.stderr, /f004.*missing/);
  const findings = JSON.parse(await readFile(join(crops, 'findings.json')));
  assert.deepEqual(findings.map(f => f.id), ['f001', 'f002', 'f003', 'f004']);
  assert.equal(findings[3].crop, undefined);
  im = await inspect(join(crops, findings[0].crop), 30, 10);
  assert.equal(im.width, 390); assert.equal(im.height, 220); near(im.rgb, band(1200));
  assert.equal((await inspect(join(crops, findings[1].crop))).height, 200);
  assert.equal((await inspect(join(crops, findings[2].crop))).height, 900);
  const bad = finding([0, 100]); delete bad.severity;
  await writeFile(b, JSON.stringify([bad]));
  r = run('crops', captures, crops, b); assert.equal(r.status, 1); assert.ok(r.stderr.includes(`${b}[0]`)); assert.match(r.stderr, /severity/);
  await writeFile(b, '[{"page": "phone",]');
  r = run('crops', captures, crops, a, b); assert.equal(r.status, 1); assert.ok(r.stderr.startsWith(`${b}: `), r.stderr);
  await writeFile(join(captures, 'phone/broken.png'), 'not a PNG');
  r = run('sheets', captures, out); assert.equal(r.status, 1); assert.match(r.stderr, /broken.png/);
  const unset = spawnSync(process.execPath, ['scripts/ui-review.mjs', 'sheets', captures, out], { encoding: 'utf8', env: { ...process.env, APPTOWN_PLAYWRIGHT_MODULE: '' } });
  assert.match(unset.stderr, /set APPTOWN_PLAYWRIGHT_MODULE/);
  console.log('UI review sheets and crops passed');
} finally { await browser.close(); await rm(root, { recursive: true, force: true }); }
