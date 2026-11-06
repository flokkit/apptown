// Credential confirmations stay readable without changing table row geometry.
import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { build } from 'esbuild'
import { productionCSS } from './production-css.mjs'
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE || 'playwright')
const bundle = await build({
  bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src', import.meta.url).pathname, contents: `
    import {createRoot} from 'react-dom/client';
    import {MemoryRouter} from 'react-router-dom';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {TokensCard,AgentsCard} from './pages/AccountAccess';
    window.fetch=async path=>new Response(JSON.stringify(String(path).includes('/delegations')?{delegations:[{id:'d',clientName:'ChatGPT',user:'sam',scopes:['apptown:user:read'],createdAt:'2026-11-05T01:00:00Z'}]}:{tokens:[{Name:'tom-laptop',User:'sam',Role:'admin',Purpose:'cli'}]}));
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><main style={{padding:18}}><AgentsCard canEdit/><TokensCard/></main></MemoryRouter></QueryClientProvider>);
  ` },
})
const css = await productionCSS()
const out = process.env.PEOPLE_CONFIRM_OUT
if (out) await mkdir(out, { recursive: true })
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage()
  await page.route('http://probe.test/**', r => r.fulfill({ contentType: 'text/html', body: '<html></html>' }))
  for (const width of [1440, 390]) for (const size of ['medium', 'large']) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto('http://probe.test/')
    await page.setContent(`<style>${css}</style><div id="root"></div>`)
    await page.evaluate(({size,theme}) => { document.documentElement.dataset.size = size; document.documentElement.dataset.theme = theme }, {size,theme})
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    for (const action of ['revoke', 'disconnect']) {
      const trigger = page.getByRole('button', { name: action, exact: true })
      const row = trigger.locator('xpath=ancestor::tr')
      const before = await row.boundingBox()
      await trigger.click()
      const clipped = await page.locator('td.record-action').evaluateAll(cells => cells.filter(c => c.scrollWidth > c.clientWidth + 1).map(c => c.textContent))
      assert.deepEqual(clipped, [], `Confirmation clipped at ${width}px`)
      const panel = page.locator('.popover')
      await panel.waitFor()
      assert.match(await panel.innerText(), action === 'revoke' ? /Revoke token tom-laptop\? Anything using it stops working\./ : /Disconnect ChatGPT \(as sam\)\? It stops working at once; they can sign in again\./)
      assert.ok(await panel.evaluate(panel => [...panel.querySelectorAll('p, button')].every(el => {
        const box = el.getBoundingClientRect()
        return el.scrollWidth <= el.clientWidth + 1 && box.left >= 0 && box.right <= innerWidth && box.top >= 0 && box.bottom <= innerHeight
      })), `Full question and controls fit: ${action}, ${width}, ${size}, ${theme}`)
      assert.deepEqual(await row.boundingBox(), before, 'Confirmation preserves row identity geometry')
      assert.ok(await row.locator('td:first-child').evaluate(el => el.scrollWidth <= el.clientWidth + 1), 'Row name stays readable')
      assert.ok(await row.locator('td:nth-child(2)').evaluate(el => el.scrollWidth <= el.clientWidth + 1), 'Account stays readable')
      if (out) await page.screenshot({ path: `${out}/${action}-${width}-${size}-${theme}.png`, fullPage: true })
      await panel.getByRole('button', { name: 'No', exact: true }).click()
      await panel.waitFor({ state: 'detached' })
      assert.ok(await trigger.evaluate(el => el === document.activeElement), 'Cancel restores trigger focus')
    }
  }
} finally { await browser.close() }
console.log('Credential confirmations fit at desktop/phone widths, Medium/Large, light/dark')
