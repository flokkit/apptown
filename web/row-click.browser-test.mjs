// A list row that opens on click reads like a table's linked row, under
// production CSS: inside a section's surface its hover runs edge to edge,
// however the page wraps the row, and its name is bold with no underline while
// a later link to elsewhere keeps its underline.
import assert from 'node:assert/strict'
import { productionCSS } from './production-css.mjs'
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE || 'playwright')
const css = await productionCSS()
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage()
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    await page.setContent(`<style>${css}</style><main class="main" style="padding:20px">
      <div class="section-body" id="wrapped"><div class="int-kind-group"><div role="list">
        <div class="int-row" role="listitem"><div class="list-row list-row-link int-list-row">
          <div class="list-identity"><div class="int-who"><span>I</span>
            <div><a class="int-name" href="/integrations/mail">Mail</a><div class="muted small">Send email</div></div>
          </div></div><div class="list-state">working</div><div class="list-action"></div>
        </div></div>
      </div></div></div>
      <div class="section-body" id="direct">
        <div class="list-row list-row-link card"><div class="list-identity"><a id="name" href="/projects/shop">Shop</a> · deployment on <a id="other" href="/targets/docker">docker</a></div><div class="list-state"></div><div class="list-action"></div></div>
      </div></main>`)
    const got = await page.evaluate(() => {
      const inset = (id) => {
        const surface = document.getElementById(id).getBoundingClientRect()
        const row = document.querySelector(`#${id} .list-row`).getBoundingClientRect()
        return { left: Math.round(row.left - surface.left - 1), right: Math.round(surface.right - row.right - 1) }
      }
      const style = (id) => { const s = getComputedStyle(document.getElementById(id)); return { weight: s.fontWeight, line: s.textDecorationLine } }
      const name = document.querySelector('#wrapped .int-name').getBoundingClientRect().left - document.querySelector('#wrapped').getBoundingClientRect().left
      return { wrapped: inset('wrapped'), direct: inset('direct'), name: style('name'), other: style('other'), nameInset: Math.round(name) }
    })
    for (const k of ['wrapped', 'direct']) assert.ok(Math.abs(got[k].left) <= 1 && Math.abs(got[k].right) <= 1, `${k} row reaches the surface edges at ${width}px: ${JSON.stringify(got[k])}`)
    assert.ok(got.nameInset >= 16, `the row's content stays inset at ${width}px: ${got.nameInset}`)
    assert.equal(got.name.line, 'none', `the row's name has no underline at ${width}px`)
    assert.ok(Number(got.name.weight) >= 600, `the row's name is bold at ${width}px: ${got.name.weight}`)
    assert.equal(got.other.line, 'underline', `a link to elsewhere keeps its underline at ${width}px`)
  }
} finally { await browser.close() }
