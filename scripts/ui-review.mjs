// Render review evidence with the camera's locked Chromium, without a server.
import { readFile, writeFile, readdir, mkdir, access } from 'node:fs/promises';
import { join } from 'node:path';

const SEGMENT = 1300, GAP = 24, HEADER = 28;
const escapeHTML = value => String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const component = value => typeof value === 'string' && value.length > 0 && !/[\\/]/.test(value) && !['.', '..'].includes(value);

async function main() {
  const [command, source, output, ...files] = process.argv.slice(2);
  if (!['sheets', 'crops'].includes(command) || !source || !output || (command === 'crops' && !files.length) || (command === 'sheets' && files.length)) {
    throw new Error('usage: node scripts/ui-review.mjs sheets <captures> <out> | crops <captures> <out> <findings.json>...');
  }
  if (!process.env.APPTOWN_PLAYWRIGHT_MODULE) throw new Error("set APPTOWN_PLAYWRIGHT_MODULE to Playwright's index.mjs (web/node_modules/playwright/index.mjs after npm ci in web/)");
  // Validate all batches before producing crops so a malformed batch is actionable.
  const findings = [];
  for (const file of files) {
    let batch;
    try { batch = JSON.parse(await readFile(file, 'utf8')); } catch (err) { throw new Error(`${file}: ${err.message}`); }
    if (!Array.isArray(batch)) throw new Error(`${file}: expected a JSON array`);
    for (const [index, item] of batch.entries()) {
      const valid = {
        page: component(item?.page), variant: component(item?.variant),
        y: Array.isArray(item?.y) && item.y.length === 2 && item.y.every(Number.isFinite) && item.y[0] >= 0 && item.y[1] >= item.y[0],
        kind: ['product', 'scene'].includes(item?.kind), severity: ['high', 'medium', 'low'].includes(item?.severity),
        category: typeof item?.category === 'string' && !!item.category,
        what: typeof item?.what === 'string' && !!item.what, element: typeof item?.element === 'string' && !!item.element,
      };
      for (const [field, ok] of Object.entries(valid)) if (!ok) throw new Error(`${file}[${index}]: missing or invalid ${field}`);
      const { id: _id, crop: _crop, ...finding } = item;
      findings.push({ ...finding, id: `f${String(findings.length + 1).padStart(3, '0')}` });
    }
  }
  const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE);
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ deviceScaleFactor: 1 });
    let imagePath, imageURL, serial = 0;
    await page.route('http://ui-review.invalid/shot.png?*', async route => route.fulfill({ contentType: 'image/png', body: await readFile(imagePath) }));
    async function load(path) {
      imagePath = path; imageURL = `http://ui-review.invalid/shot.png?${++serial}`;
      await page.setContent(`<img id="shot" src="${imageURL}">`);
      try {
        return await page.locator('#shot').evaluate(async im => { await im.decode(); return { width: im.naturalWidth, height: im.naturalHeight }; });
      } catch { throw new Error(`cannot read PNG: ${path}`); }
    }
    async function render(width, height, html, path) {
      await page.setViewportSize({ width, height });
      await page.setContent(`<style>*{box-sizing:border-box}body{margin:0;background:#808080;font:12px sans-serif}header{height:${HEADER}px;background:#ffffa0;padding:6px;white-space:nowrap;overflow:hidden}.column{flex:none}.slice{overflow:hidden;position:relative}img{position:absolute;left:0}</style>${html}`);
      await page.locator('img').evaluateAll(ims => Promise.all(ims.map(im => im.decode())));
      await page.screenshot({ path, type: 'jpeg', quality: 82 });
    }
    await mkdir(output, { recursive: true });
    if (command === 'sheets') {
      const index = {};
      let count = 0;
      for (const entry of (await readdir(source, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
        if (!entry.isDirectory()) continue;
        const name = entry.name;
        for (const shot of (await readdir(join(source, name))).sort().filter(f => f.endsWith('.png'))) {
          const variant = shot.slice(0, -4);
          const { width, height } = await load(join(source, name, shot));
          const columns = Math.max(1, Math.floor((1800 + GAP) / (width + GAP)));
          await mkdir(join(output, name), { recursive: true });
          for (let start = 0, n = 1; start < height; start += columns * SEGMENT, n++) {
            const segments = [];
            for (let y = start; y < Math.min(height, start + columns * SEGMENT); y += SEGMENT) {
              segments.push(`<div class="column" style="width:${width}px"><header>${escapeHTML(name)} ${escapeHTML(variant)} y ${y}-${Math.min(y + SEGMENT, height)}</header><div class="slice" style="height:${Math.min(SEGMENT, height - y)}px"><img src="${imageURL}" style="top:-${y}px"></div></div>`);
            }
            const file = `${variant}-${n}.jpg`;
            await render(segments.length * width + (segments.length - 1) * GAP, HEADER + SEGMENT, `<div style="display:flex;gap:${GAP}px">${segments.join('')}</div>`, join(output, name, file));
            (index[name] ??= []).push(file); count++;
          }
        }
      }
      await writeFile(join(output, 'index.json'), JSON.stringify(index, null, 2) + '\n');
      console.log(`${count} sheets`);
    } else {
      await mkdir(join(output, 'crops'), { recursive: true });
      const missing = [];
      for (const finding of findings) {
        const path = join(source, finding.page, `${finding.variant}.png`);
        try { await access(path); } catch (err) { if (err.code !== 'ENOENT') throw err; missing.push(`${finding.id}: ${finding.page}/${finding.variant}`); continue; }
        const { width, height } = await load(path);
        const size = Math.min(height, 900, Math.max(200, Math.min(height, finding.y[1] + 60) - Math.max(0, finding.y[0] - 60)));
        const top = Math.max(0, Math.min(height - size, Math.floor(finding.y[0] - 60)));
        finding.crop = `crops/${finding.id}.jpg`;
        await render(width, size, `<img src="${imageURL}" style="top:-${top}px">`, join(output, finding.crop));
      }
      await writeFile(join(output, 'findings.json'), JSON.stringify(findings, null, 2) + '\n');
      console.log(`${findings.length} findings; ${missing.length} without a screenshot`);
      if (missing.length) { console.error(missing.join('\n')); process.exitCode = 1; }
    }
  } finally { await browser.close(); }
}
main().catch(err => { console.error(err.message); process.exitCode = 1; });
