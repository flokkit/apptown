// Run with APPTOWN_PLAYWRIGHT_MODULE pointing to the installed Playwright module.
// Uses the shipped CSS, without a server or seeded application records.
import assert from 'node:assert/strict'
import { readFile, readdir } from 'node:fs/promises'
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE)
const source = new URL('./src/', import.meta.url)
async function styles(directory) {
  const entries = await readdir(directory, { withFileTypes: true })
  const files = await Promise.all(entries.map(entry => entry.isDirectory()
    ? styles(new URL(`${entry.name}/`, directory))
    : entry.name.endsWith('.css') ? readFile(new URL(entry.name, directory), 'utf8') : ''))
  return files.join('\n')
}
const css = await styles(source)
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
    <div class="md"><h1>Heading</h1><h2>Subheading</h2><h3>Lead</h3></div>
    <aside class="sidebar open"><div class="sidebar-inner">
      <details open class="account-corner"><summary>Account</summary><div>
        <div class="account-switches"><div class="theme-switch size-switch" role="group" aria-label="Text size">
          <button>Small</button><button>Medium</button><button>Large</button>
        </div></div>
      </div></details>
    </div></aside>`)
  const results = []
  for (const [size, root] of [['small', 14], ['medium', 16], ['large', 18]]) {
    await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
    const measured = await page.evaluate(() => ({
      root: parseFloat(getComputedStyle(document.documentElement).fontSize),
      body: parseFloat(getComputedStyle(document.body).fontSize),
      fields: [...document.querySelectorAll('input, textarea, select')].map(field => ({
        name: field.getAttribute('aria-label'), pixels: parseFloat(getComputedStyle(field).fontSize),
      })),
    }))
    assert.equal(measured.root, root)
    assert.equal(measured.body, root)
    for (const field of measured.fields) assert.equal(field.pixels, Math.max(root, 16), `${size} ${field.name}`)
    results.push({ size, ...measured })
  }
  // Small keeps the desktop reference, including steps between the original named tokens.
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.evaluate(() => { document.documentElement.dataset.size = 'small' })
  const desktop = await page.evaluate(() => [...document.querySelectorAll('.md h1, .md h2, .md h3')]
    .map(element => parseFloat(getComputedStyle(element).fontSize)))
  assert.deepEqual(desktop, [22, 19, 15])
  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: 1000 })
    for (const size of ['small', 'medium', 'large']) {
      await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
      const fits = await page.evaluate(() => {
        const panel = document.querySelector('.account-corner > div').getBoundingClientRect()
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
