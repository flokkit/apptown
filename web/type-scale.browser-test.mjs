import { productionCSS } from './production-css.mjs'
// Run with APPTOWN_PLAYWRIGHT_MODULE pointing to the installed Playwright module.
// Uses the shipped CSS, without a server or seeded application records.
import assert from 'node:assert/strict'
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE)
const css = await productionCSS()
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  await page.setContent(`<style>@layer tokens, base, components, pages, utilities;\n${css}</style>
    <input aria-label="Text"><input type="search" aria-label="Search">
    <textarea aria-label="Code"></textarea><textarea class="prose" aria-label="Prose"></textarea>
    <select aria-label="Select"><option>Choice</option></select>
    <input aria-label="Inline" style="font-size: var(--fs-xs)">
    <div class="ch-sendback"><textarea aria-label="Send back"></textarea></div>
    <div class="console"><div class="chat-input"><textarea aria-label="Composer"></textarea></div></div>
    <div id="page-spacing" class="evals-note"></div>
    <div id="spacing-slice" class="evals-note mt-2"></div>
    <table class="stack"><tbody><tr><td>Retained table layout</td></tr></tbody></table>
    <div class="stack" id="column-stack"><span>One</span><span>Two</span></div>
    ${[1, 2, 3, 4, 5, 6].map(step => `<div class="row row-${step}" data-gap="${step}"><span>One</span><span>Two</span></div><div class="stack stack-${step}" data-gap="${step}"><span>One</span><span>Two</span></div>`).join('')}
    <div class="md"><h1>Heading</h1><h2>Subheading</h2><h3>Lead</h3></div>
    <aside class="sidebar open"><div class="sidebar-inner">
      <div class="popover"><div class="popover-content"><div>
        <div class="account-switches"><div class="theme-switch size-switch" role="group" aria-label="Text size">
          <button>Small</button><button>Medium</button><button>Large</button>
        </div></div>
      </div></div></div>
    </div></aside>`)
  const results = []
  for (const [size, root] of [['small', 14], ['medium', 16], ['large', 18]]) {
    await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
    const measured = await page.evaluate(() => ({
      root: parseFloat(getComputedStyle(document.documentElement).fontSize),
      body: parseFloat(getComputedStyle(document.body).fontSize),
      pageSpacing: parseFloat(getComputedStyle(document.querySelector('#page-spacing')).marginTop),
      spacing: parseFloat(getComputedStyle(document.querySelector('#spacing-slice')).marginTop),
      fields: [...document.querySelectorAll('input, textarea, select')].map(field => ({
        name: field.getAttribute('aria-label'), pixels: parseFloat(getComputedStyle(field).fontSize),
      })),
    }))
    assert.equal(measured.root, root)
    assert.equal(measured.body, root)
    assert.ok(Math.abs(measured.pageSpacing - 6 * root / 14) < 0.001, `${size}: extracted page spacing follows the viewer size`)
    assert.ok(Math.abs(measured.spacing - 8 * root / 14) < 0.001, `${size}: utility margin follows the viewer size`)
    for (const field of measured.fields) assert.equal(field.pixels, Math.max(root, 16), `${size} ${field.name}`)
    for (const gap of await page.locator('[data-gap]').evaluateAll(elements => elements.map(element => ({
      step: Number(element.dataset.gap), gap: parseFloat(getComputedStyle(element).gap),
    })))) assert.ok(Math.abs(gap.gap - [4, 8, 12, 16, 24, 32][gap.step - 1] * root / 14) < 0.001, `${size}: gap step ${gap.step}`)
    results.push({ size, ...measured })
  }
  // Small keeps the desktop reference, including steps between the original named tokens.
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.evaluate(() => { document.documentElement.dataset.size = 'small' })
  const desktop = await page.evaluate(() => [...document.querySelectorAll('.md h1, .md h2, .md h3')]
    .map(element => parseFloat(getComputedStyle(element).fontSize)))
  assert.deepEqual(desktop, [22, 19, 15])
  const layouts = await page.evaluate(() => ({
    table: getComputedStyle(document.querySelector('table.stack')).display,
    stack: getComputedStyle(document.querySelector('#column-stack')).display,
    pageMargin: parseFloat(getComputedStyle(document.querySelector('#spacing-slice')).marginTop),
  }))
  assert.equal(layouts.table, 'table', 'column stacks preserve desktop tables')
  assert.equal(layouts.stack, 'grid', 'column stacks use the existing utility')
  assert.equal(layouts.pageMargin, 8, 'utility spacing wins over page spacing')
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: 1000 })
    for (const size of ['small', 'medium', 'large']) {
      await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
      const fits = await page.evaluate(() => {
        const panel = document.querySelector('.popover').getBoundingClientRect()
        const buttons = [...document.querySelectorAll('.size-switch button')]
        const top = buttons[0].getBoundingClientRect().top
        // Inside the panel and on one line: the panel grows to fit the choices.
        return buttons.every(button => {
          const rect = button.getBoundingClientRect()
          return rect.left >= panel.left && rect.right <= panel.right && rect.top === top
        })
      })
      assert.ok(fits, `size control fits account panel on one line at ${width}px, ${size}`)
    }
  }
  console.log(JSON.stringify({ phone: results, smallDesktopHeadings: desktop }, null, 2))
} finally {
  await browser.close()
}
