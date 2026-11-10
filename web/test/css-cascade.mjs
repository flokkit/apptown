// APPTOWN_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node web/test/css-cascade.mjs
// Exercise the real layers and entrypoint order without a server or fixture data.
import assert from 'node:assert/strict'
import { productionCSS } from '../production-css.mjs'
import { fontsReady } from '../fonts-ready.mjs'

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
    </div>
    <table class="record-table"><tbody><tr><td class="phone-name">shop</td><td class="phone-value record-wide" data-label="Deployment">main</td><td class="phone-value" data-label="Detail">details</td></tr></tbody></table>`)
  await fontsReady(page)
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
        actionRight: document.querySelector('.list-action').getBoundingClientRect().right,
        rowRight: document.querySelector('.project-row').getBoundingClientRect().right,
        rowPaddingRight: parseFloat(getComputedStyle(document.querySelector('.project-row')).paddingRight),
        controlHeights: [...document.querySelectorAll('.people-form > *')].map(el => el.getBoundingClientRect().height),
        rootSize: parseFloat(getComputedStyle(document.documentElement).fontSize),
        buttonHeight: document.querySelector('.people-form button').getBoundingClientRect().height,
        headingMargins: [...document.querySelectorAll('.ch-page h2')].map(h => getComputedStyle(h).marginBottom),
        wideOnlyCell: getComputedStyle(document.querySelector('td.record-wide')).display,
      }
    })
    const phone = width === 390
    const label = `${width} ${theme}`
    assert.equal(actual.columns.length, phone ? 1 : 3, `${label}: shared list columns`)
    // Project rows keep one fixed action column, so Open app and Change line
    // up whether or not a project has an app; the buttons sit at its end.
    if (!phone) assert.ok(parseFloat(actual.columns[2]) >= actual.actionWidth, `${label}: action track holds its content`)
    if (!phone) assert.ok(Math.abs(actual.actionRight - actual.rowRight + actual.rowPaddingRight) < 1, `${label}: actions end at the row's end`)
    assert.equal(actual.gap, phone ? '8px' : '12px', `${label}: shared list gap`)
    assert.equal(actual.padding, phone ? '12px' : '16px', `${label}: shared list padding`)
    assert.ok(Math.abs(actual.buttonHeight - actual.rootSize * 40 / 14) < 1, `${label}: invite controls follow text size`)
    assert.ok(actual.controlHeights.every(height => height === actual.buttonHeight), `${label}: invite controls have equal heights`)
    assert.deepEqual(actual.headingMargins, ['0px', '0px'], `${label}: change heading margins`)
    // A wide-only column would otherwise share its phone cell with the details.
    assert.equal(actual.wideOnlyCell, phone ? 'none' : 'table-cell', `${label}: wide-only table cell`)
  }
  console.log('CSS cascade: project lists, invite controls, change headings and wide-only cells match at both widths and themes')
} finally {
  await browser.close()
}
