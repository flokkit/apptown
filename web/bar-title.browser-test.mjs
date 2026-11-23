// The page bar's title on a phone (design/subsystems/ui-coherence.md): the
// bar-title size, at most two lines, and the "?" kept with the title's last
// word with a 44px tap area. Wider screens keep the page-title size and the
// "?" beside the title as before. Run in Chromium and in WebKit, the engine
// of every iPhone browser: the "?" stays with its word through line-breaking
// rules that the two engines apply separately.
import { productionCSS } from './production-css.mjs'
import { fontsReady } from './fonts-ready.mjs'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
const playwright = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE)
const css = await productionCSS()

// window.renderBar draws the bar for one title, in a fresh bar each time. head cases go through PageHead (its "?" and,
// on a beta page, the Beta chip after it); the others build the bar from
// PageBar with a "?" and, when asked, a status after the title.
const fixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { QueryClientProvider } from '@tanstack/react-query'
    import { newQueryClient } from './data'
    import { ShellBar, PageBar } from './PageBar'
    import { PageHead, HelpButton } from './PageHead'
    import { BetaPage } from './features'
    import { Status } from './Status'
    import { enabledFeatures } from './features.fixture'
    const answers = {'/auth/me': {mode:'internal',user:'sam'}, '/authz/me': {instance:{'platform:manage-deploy-targets':true}}, '/features': enabledFeatures, '/attention': {items:[{key:'workers/a',title:'Worker unavailable',detail:'Work is waiting.',href:'/targets',statusLabel:'failed',severity:'warning',observedAt:'2026-11-10T00:00:00Z',stale:false}],sources:[]}}
    const client = newQueryClient()
    window.fetch = async path => new Response(JSON.stringify(answers[String(path).replace(/^.*\\/api\\/v1/, '')] ?? {}))
    const root = createRoot(document.getElementById('root'))
    window.renderBar = c => {
      const bar = c.chat ? <div className="console"><PageBar className="chat-bar" titleClass="chat-title" title={c.title} after={<span className="chat-kind">questions &amp; setup</span>} /></div> : c.head
        ? <BetaPage.Provider value={!!c.beta}><PageHead page="people" title={c.title} /></BetaPage.Provider>
        : <PageBar title={c.title} help={<HelpButton about={c.title} guide="people"><p>What this is.</p></HelpButton>} after={c.status && <Status word="failed" />} />
      root.render(<QueryClientProvider client={client}><MemoryRouter><div className="layout"><main className="main">
        <ShellBar key={c.name} lead={<button className="subtle page-bar-menu" aria-label="open the menu">☰</button>}>{bar}</ShellBar>
      </main></div></MemoryRouter></QueryClientProvider>)
    }
  ` },
})

const long = 'Kids chore chart, pocket money tracker and the family holiday planning notes for next year'
const cases = [
  { name: 'one word', title: 'Home', head: true, clamped: false },
  { name: 'page name', title: 'Workflows', head: true, clamped: false },
  { name: 'two-word page', title: 'All deployments', head: true },
  { name: 'three-word page', title: 'Agents and tokens', head: true },
  { name: 'beta page', title: 'Agents and tokens', head: true, beta: true },
  { name: 'record name', title: 'Club Newsletter and friends' },
  { name: 'record with status', title: 'Club Newsletter and friends', status: true },
  { name: 'long', title: long, clamped: true },
  { name: 'long with status', title: long, status: true, clamped: true },
  { name: 'long beta page', title: long, head: true, beta: true, clamped: true },
]

// What the bar shows, read in the page. The title's own lines are counted
// with the clamp mark removed: that is the title as it would wrap.
const read = () => {
  const block = document.querySelector('header.page-bar .page-bar-title'), h1 = block.querySelector('h1')
  const q = block.querySelector('button.page-help-q'), qr = q.getBoundingClientRect()
  const clamped = block.hasAttribute('data-clamped')
  block.removeAttribute('data-clamped')
  const natural = h1.getClientRects().length
  if (clamped) block.setAttribute('data-clamped', '')
  const lh = parseFloat(getComputedStyle(h1).lineHeight), hr = h1.getBoundingClientRect()
  // The last visible word: the title's last word, or, cut, the h1's second line.
  let word
  if (clamped) word = { top: hr.bottom - lh, bottom: hr.bottom, right: hr.right }
  else {
    const text = [...h1.childNodes].filter(n => n.nodeType === 3).at(-1), r = document.createRange()
    const at = text.data.trimEnd().lastIndexOf(' ') + 1
    r.setStart(text, at); r.setEnd(text, text.data.length)
    word = r.getClientRects()[r.getClientRects().length - 1].toJSON()
  }
  const x = qr.left + qr.width / 2, y = qr.top + qr.height / 2
  // Above the screen's top edge there is nothing to tap: probe within it.
  const hits = [-21, 21].map(dy => q.contains(document.elementFromPoint(x, Math.max(0.5, y + dy))))
  const facts = [...block.querySelectorAll(':scope > :not(h1, .page-bar-help)')].map(f => f.getBoundingClientRect().toJSON())
  // A text line's box is its font's content area, a pixel or two taller than
  // the line, so boxes on neighbouring lines may touch by that much.
  const overlaps = (a, b) => a.left < b.right - 2 && b.left < a.right - 2 && a.top < b.bottom - 2 && b.top < a.bottom - 2
  return {
    clamped, natural, lines: clamped ? Math.round(hr.height / lh) : natural, title: h1.getAttribute('title'), text: h1.textContent,
    sameLine: y > word.top && y < word.bottom && qr.left >= word.right - 1,
    glyph: qr.height, tap: hits.every(Boolean), onScreen: qr.left >= 0 && qr.right <= innerWidth && qr.top >= 0,
    facts: facts.every(f => f.left >= 0 && f.right <= innerWidth + 1 && !overlaps(f, qr) && ![...h1.getClientRects()].some(line => overlaps(f, line))),
  }
}

let failures = 0
const want = (ok, message) => { if (!ok) { failures++; console.error('not ok: ' + message) } }
for (const engine of ['chromium', 'webkit']) {
  const browser = await playwright[engine].launch({ headless: true })
  try {
    const page = await browser.newPage()
    await page.route('http://layout.test/**', route => route.fulfill({ contentType: 'text/html', body: `<style>@layer tokens, base, type, components, pages, utilities; ${css}</style><div id="root"></div>` }))
    await page.goto('http://layout.test/')
    await page.addScriptTag({ content: fixture.outputFiles[0].text })
    await fontsReady(page)
    const open = async (c, size) => {
      await page.evaluate(([c, size]) => { document.documentElement.dataset.size = size; window.renderBar(c) }, [c, size])
      await page.locator('header.page-bar h1', { hasText: c.title }).waitFor()
      await page.locator('header.page-bar a[href^="/attention"]').last().waitFor()
      // The clamp is measured in a layout effect and again on resize.
      await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))))
    }
    for (const [width, height] of [[360, 800], [390, 844]]) {
      await page.setViewportSize({ width, height })
      for (const size of ['small', 'medium', 'large']) for (const c of cases) {
        await open(c, size)
        const r = await page.evaluate(read)
        const at = `${c.name} at ${width}px ${size} in ${engine}: ${JSON.stringify(r)}`
        want(r.sameLine, `the "?" is on the line of the title's last word, after it, ${at}`)
        want(r.lines <= 2, `the title takes at most two lines, ${at}`)
        want(r.clamped === r.natural > 2, `the title is cut only when it takes more than two lines, ${at}`)
        if (c.clamped !== undefined) want(r.clamped === c.clamped, `${c.clamped ? '' : 'not '}cut, ${at}`)
        want(r.clamped ? r.title === r.text : r.title === null, `a cut title carries its whole text as its title, ${at}`)
        want(r.glyph < 20, `the "?" is a small circle, ${at}`)
        want(r.tap, `a tap 21px above or below the "?" still reaches it, ${at}`)
        want(r.onScreen, `the "?" is on screen, ${at}`)
        want(r.facts, `the facts after the title are on screen and uncovered, ${at}`)
      }
    }
    // A chat's bar lays out its own title (console.css): none of the phone
    // title rules reach it.
    await page.setViewportSize({ width: 390, height: 844 })
    await open({ name: 'chat', title: 'Project chat', chat: true }, 'medium')
    const chat = await page.evaluate(() => {
      const block = document.querySelector('header.page-bar .page-bar-title'), h1 = block.querySelector('h1')
      return { h1: getComputedStyle(h1).display, block: getComputedStyle(block).display, kind: (k => parseFloat(k.lineHeight) / parseFloat(k.fontSize))(getComputedStyle(block.querySelector('.chat-kind'))), body: (b => parseFloat(b.lineHeight) / parseFloat(b.fontSize))(getComputedStyle(document.body)), clamped: block.hasAttribute('data-clamped') }
    })
    want(chat.h1 !== 'inline' && chat.block === 'contents' && Math.abs(chat.kind - chat.body) < 0.01 && !chat.clamped, `the chat's bar keeps its own title layout in ${engine}: ${JSON.stringify(chat)}`)
    // Wide: the page-title size, the 24px "?", and the Beta chip a flex gap after it.
    await page.setViewportSize({ width: 1440, height: 900 })
    for (const c of cases.filter(c => c.beta)) {
      await open(c, 'medium')
      const r = await page.evaluate(() => {
        const block = document.querySelector('header.page-bar .page-bar-title'), h1 = block.querySelector('h1')
        const q = block.querySelector('button.page-help-q').getBoundingClientRect(), beta = block.querySelector('.beta-chip').getBoundingClientRect()
        const probe = document.createElement('div'); probe.style.fontSize = 'var(--fs-xl)'; document.body.append(probe)
        const xl = getComputedStyle(probe).fontSize; probe.remove()
        return { size: getComputedStyle(h1).fontSize, xl, q: [q.width, q.height], gap: beta.left - q.right, flexGap: parseFloat(getComputedStyle(block).columnGap), clamped: block.hasAttribute('data-clamped') }
      })
      const at = `${c.name} at 1440px in ${engine}: ${JSON.stringify(r)}`
      want(r.size === r.xl, `wide titles keep the page-title size, ${at}`)
      want(r.q[0] === 24 && r.q[1] === 24, `wide "?" keeps its 24px circle, ${at}`)
      want(Math.abs(r.gap - r.flexGap) < 0.5, `wide Beta chip sits a flex gap after the "?", ${at}`)
      want(!r.clamped, `wide titles are not cut, ${at}`)
    }
  } finally { await browser.close() }
}
assert.equal(failures, 0, `${failures} bar title checks failed`)
console.log('bar title: phone size, two lines, "?" with its word, in Chromium and WebKit')
