import { productionCSS } from './production-css.mjs'
// Run with APPTOWN_PLAYWRIGHT_MODULE pointing to the installed Playwright module.
// Uses the shipped CSS, without a server or seeded application records.
import assert from 'node:assert/strict'
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE)
const css = await productionCSS()
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  await page.setContent(`<style>@layer tokens, base, type, components, pages, utilities;\n${css}</style>
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
  // Small keeps the desktop reference; document headings use the five-size scale.
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.evaluate(() => { document.documentElement.dataset.size = 'small' })
  const desktop = await page.evaluate(() => [...document.querySelectorAll('.md h1, .md h2, .md h3')]
    .map(element => parseFloat(getComputedStyle(element).fontSize)))
  assert.deepEqual(desktop, [26, 18, 14])
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

  // Text roles: each role class computes its tokens for the properties the
  // role owns, and every element mapped to the role computes the same. Form
  // controls keep their size (and a button its line height) from forms.css.
  const roles = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  await roles.setContent(`<style>@layer tokens, base, type, components, pages, utilities;\n${css}</style>
    <div data-role="title"><span class="text-title">T</span><h1>T</h1><div class="project-heading">T</div></div>
    <div data-role="heading"><span class="text-heading">H</span><h2>H</h2></div>
    <div data-role="subheading"><span class="text-subheading">S</span><h3>S</h3><h4>S</h4><h5>S</h5><h6>S</h6></div>
    <div data-role="name"><span class="text-name">N</span><strong>N</strong><b>N</b></div>
    <div data-role="body"><span class="text-body">B</span></div>
    <div data-role="label"><span class="text-label">L</span><dl><dt>L</dt><dd>v</dd></dl>
      <table><thead><tr><th id="plain-head">L</th></tr></thead><tbody><tr><td>v</td></tr></tbody></table>
      <table class="record-table"><thead><tr><th id="record-head">L</th></tr></thead><tbody><tr><td class="phone-name"><a href="#n" id="table-name"><span class="muted" id="muted-in-table-name">#12 </span>Name</a></td><td data-label="Kind" id="phone-label">value</td><td class="phone-name" id="plain-table-name">Scheduled backup<div class="small" id="name-line">every night</div></td><td class="phone-name technical" id="technical-name">9bc25749f</td></tr></tbody></table></div>
    <div data-role="group"><span class="group-label">G</span></div>
    <div data-role="small"><span class="small">s</span></div>
    <div data-role="muted"><span class="muted">m</span></div>
    <div data-role="code"><span class="text-code">c</span><code>c</code><pre>c</pre><kbd>c</kbd><samp>c</samp></div>
    <div class="list-row"><div class="list-identity"><strong id="row-name">Row</strong></div></div>
    <h2>Head <span class="muted" id="muted-in-heading">3</span></h2><strong>Name <span class="muted" id="muted-in-name">#12</span></strong>
    <div id="controls"><button>Go</button><input aria-label="Text"><select aria-label="Select"><option>One</option></select>
      <textarea aria-label="Code"></textarea><textarea class="prose" aria-label="Prose"></textarea><input class="text-code" aria-label="Command"></div>
    <div class="console"><div class="chat-input"><textarea aria-label="Composer"></textarea></div></div>`)
  const owned = {
    title: { fontFamily: '--font-heading', fontSize: '--fs-xl', fontWeight: '--fw-strong', lineHeight: '--lh-tight', letterSpacing: '--ls-title', color: '--text' },
    heading: { fontFamily: '--font-heading', fontSize: '--fs-lg', fontWeight: '--fw-strong', lineHeight: '--lh-tight', color: '--text' },
    subheading: { fontFamily: '--font-heading', fontSize: '--fs-base', fontWeight: '--fw-strong', color: '--text' },
    name: { fontWeight: '--fw-strong' },
    body: { fontFamily: '--font-sans', fontSize: '--fs-base', fontWeight: '--fw-regular', lineHeight: '--lh-body', color: '--text' },
    label: { fontFamily: '--font-sans', fontSize: '--fs-sm', fontWeight: '--fw-regular', color: '--muted' },
    group: { fontFamily: '--font-sans', fontSize: '--fs-xs', fontWeight: '--fw-strong', letterSpacing: '--ls-caps', textTransform: 'uppercase', color: '--muted' },
    small: { fontSize: '--fs-xs' },
    muted: { color: '--muted' },
    code: { fontFamily: '--font-mono', fontSize: '--fs-xs' },
  }
  const kebab = name => name.replace(/[A-Z]/g, c => '-' + c.toLowerCase())
  for (const width of [390, 1440]) {
    await roles.setViewportSize({ width, height: 1000 })
    for (const theme of ['light', 'dark']) for (const size of ['small', 'medium', 'large']) {
      const where = `${width}px ${theme} ${size}`
      const found = await roles.evaluate(({ owned, theme, size }) => {
        document.documentElement.dataset.theme = theme
        document.documentElement.dataset.size = size
        const kebab = name => name.replace(/[A-Z]/g, c => '-' + c.toLowerCase())
        // The token's own computed value, from a probe styled with it.
        const expected = props => {
          const probe = document.createElement('span')
          for (const [prop, value] of Object.entries(props)) probe.style.setProperty(kebab(prop), value.startsWith('--') ? `var(${value})` : value)
          document.body.append(probe)
          const style = getComputedStyle(probe)
          const result = Object.fromEntries(Object.keys(props).map(prop => [prop, style[prop]]))
          probe.remove()
          return result
        }
        const read = (el, props, pseudo) => { const style = getComputedStyle(el, pseudo); return Object.fromEntries(Object.keys(props).map(prop => [prop, style[prop]])) }
        const out = {}
        for (const [role, props] of Object.entries(owned)) {
          const box = document.querySelector(`[data-role="${role}"]`)
          out[role] = { expected: expected(props), elements: [...box.querySelectorAll(':scope > span, h1, h2, h3, h4, h5, h6, .project-heading, strong, b, dt, th, code, pre, kbd, samp')].map(el => ({ name: el.id || el.className || el.tagName, value: read(el, props) })) }
        }
        out.body = { ...out.body, elements: [...out.body.elements, { name: 'body', value: read(document.body, owned.body) }] }
        if (innerWidth <= 800) out.label.elements.push({ name: 'phone value label', value: read(document.querySelector('#phone-label'), owned.label, '::before') })
        const weight = sel => getComputedStyle(document.querySelector(sel)).fontWeight
        const px = (el, prop) => parseFloat(getComputedStyle(el)[prop])
        const controls = Object.fromEntries([...document.querySelectorAll('#controls > *, .chat-input textarea')].map(el => [el.getAttribute('aria-label') || el.tagName, {
          family: getComputedStyle(el).fontFamily, weight: getComputedStyle(el).fontWeight, size: px(el, 'fontSize'), lineHeight: px(el, 'lineHeight'),
        }]))
        return {
          roles: out, controls,
          regular: expected({ fontWeight: '--fw-regular' }).fontWeight,
          weights: { mutedInHeading: weight('#muted-in-heading'), mutedInName: weight('#muted-in-name'), mutedInTableName: weight('#muted-in-table-name'), tableName: weight('#table-name'), plainTableName: weight('#plain-table-name'), nameLine: weight('#name-line'), technicalName: weight('#technical-name'), medium: expected({ fontWeight: '--fw-medium' }).fontWeight, rowName: weight('#row-name') },
          tokens: {
            fontFamily: expected({ fontFamily: '--font-sans' }).fontFamily, mono: expected({ fontFamily: '--font-mono' }).fontFamily,
            xs: parseFloat(expected({ fontSize: '--fs-xs' }).fontSize), base: parseFloat(expected({ fontSize: '--fs-base' }).fontSize),
            field: parseFloat(expected({ fontSize: '--fs-field' }).fontSize),
          },
        }
      }, { owned, theme, size })
      for (const [role, { expected, elements }] of Object.entries(found.roles)) {
        for (const element of elements) assert.deepEqual(element.value, expected, `${where}: ${element.name} computes the ${role} role`)
      }
      assert.equal(found.roles.label.elements.length, width <= 800 ? 5 : 4, `${where}: label elements are present`)
      assert.equal(found.weights.mutedInHeading, found.regular, `${where}: muted words in a heading are regular`)
      assert.equal(found.weights.mutedInName, found.regular, `${where}: muted words in a name are regular`)
      assert.equal(found.weights.mutedInTableName, found.regular, `${where}: muted words in a table's name link are regular`)
      assert.equal(found.weights.tableName, found.weights.rowName, `${where}: a table's name and a row's name have one weight`)
      assert.equal(found.weights.plainTableName, found.weights.rowName, `${where}: a plain-text table name uses the Name role too`)
      assert.equal(found.weights.technicalName, found.weights.medium, `${where}: a technical table name is medium, mono having no strong face`)
      assert.equal(found.weights.nameLine, found.regular, `${where}: a line under a table's name keeps the body weight`)
      const c = found.controls, t = found.tokens
      for (const name of ['BUTTON', 'Text', 'Select', 'Prose', 'Composer']) {
        assert.equal(c[name].family, t.fontFamily, `${where}: ${name} uses the body family`)
        assert.equal(c[name].weight, found.regular, `${where}: ${name} uses the body weight`)
      }
      for (const name of ['Code', 'Command']) assert.equal(c[name].family, t.mono, `${where}: ${name} uses the code family`)
      assert.ok(Math.abs(c.BUTTON.lineHeight - 1.3 * c.BUTTON.size) < 0.01, `${where}: a button keeps its 1.3 line height`)
      if (width > 800) {
        for (const name of ['Text', 'Select', 'Prose']) assert.equal(c[name].size, t.base, `${where}: ${name} is body size`)
        for (const name of ['Code', 'Command']) assert.equal(c[name].size, t.xs, `${where}: ${name} is code size`)
      } else {
        for (const name of ['Text', 'Select', 'Prose', 'Code', 'Command', 'Composer']) assert.equal(c[name].size, t.field, `${where}: ${name} keeps the phone field size`)
      }
    }
  }
  console.log(JSON.stringify({ phone: results, smallDesktopHeadings: desktop }, null, 2))
} finally {
  await browser.close()
}
