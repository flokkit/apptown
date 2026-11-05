// APPTOWN_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node web/test/css-cascade.mjs
// Exercise the real layers and entrypoint order without a server or fixture data.
import assert from 'node:assert/strict'
import { productionCSS } from '../production-css.mjs'

const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE)
const css = await productionCSS()
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage()
  await page.setContent(`<style>${css}</style>
    <div class="project-row list-row">
      <div class="list-identity">A project name</div>
      <div class="list-state">Ready</div>
      <div class="list-action"><button>Open</button></div>
    </div>
    <div class="people-form"><input><select><option>Member</option></select><button>Invite</button></div>
    <div class="ch-page">
      <div class="ch-overview"><section class="ch-decision"><h2>Your decision</h2></section></div>
      <section class="ch-phase"><h2>Work</h2></section>
    </div>`)
  for (const width of [1440, 390]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 844 })
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    const actual = await page.evaluate(() => {
      const row = getComputedStyle(document.querySelector('.project-row'))
      return {
        columns: row.gridTemplateColumns.split(' '),
        gap: row.rowGap,
        padding: row.paddingTop,
        actionWidth: document.querySelector('.list-action').getBoundingClientRect().width,
        buttonHeight: document.querySelector('.people-form button').getBoundingClientRect().height,
        headingMargins: [...document.querySelectorAll('.ch-page h2')].map(h => getComputedStyle(h).marginBottom),
      }
    })
    const phone = width === 390
    const label = `${width} ${theme}`
    assert.equal(actual.columns.length, phone ? 1 : 3, `${label}: shared list columns`)
    if (!phone) assert.ok(Math.abs(parseFloat(actual.columns[2]) - actual.actionWidth) < 1, `${label}: action track fits its content`)
    assert.equal(actual.gap, phone ? '8px' : '12px', `${label}: shared list gap`)
    assert.equal(actual.padding, phone ? '12px' : '16px', `${label}: shared list padding`)
    assert.equal(actual.buttonHeight, phone ? 44 : 40, `${label}: invite button height`)
    assert.deepEqual(actual.headingMargins, ['0px', '0px'], `${label}: change heading margins`)
  }
  console.log('CSS cascade: project lists, invite controls and change headings match at both widths and themes')
} finally {
  await browser.close()
}
