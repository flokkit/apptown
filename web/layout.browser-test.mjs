import assert from 'node:assert/strict'
import { readFile, readdir, mkdir } from 'node:fs/promises'
import { build } from 'esbuild'
import { wrappedRowActions } from './row-action-lines.mjs'
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE)
async function styles(directory) {
  const entries = await readdir(directory, { withFileTypes: true })
  return (await Promise.all(entries.map(entry => entry.isDirectory() ? styles(new URL(`${entry.name}/`, directory))
    : entry.name.endsWith('.css') ? readFile(new URL(entry.name, directory), 'utf8') : ''))).join('\n')
}
const css = await styles(new URL('./src/', import.meta.url))
const fixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic',
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { Disclosure } from './Popover'
    import { SplitButton } from './SplitButton'
    import { MoreMenu } from './MoreMenu'
    import { ListRow } from './ListRow'
    const long = 'A very long project identity with a long-unbroken-identifier-abcdefghijklmnopqrstuvwxyz0123456789'
    createRoot(document.getElementById('root')).render(<>
      <main style={{padding: 12}}>
        <div className="project-rows"><div className="project-row"><div className="project-identity"><div className="project-copy">{long}</div></div><div className="project-state">Waiting for you</div><div className="project-action"><SplitButton compact icon="" label="Change" onMain={()=>{document.body.dataset.main='yes'}} items={[{label:'Catalog change',hint:'Choose a prepared change'}]} /></div></div></div>
        <div className="ch-decision-choices"><button>Accept</button><button>Send back…</button><button>Abandon change</button></div>
        <section><ListRow identity={long} state="Waiting for you" action={<><button>Review change</button><MoreMenu label="Project" items={[{label:'Archive project',hint:'Keep its history'}]} /></>} /></section>
        <section role="list"><div className="int-row"><ListRow className="int-list-row" identity={long} state="Not checked yet" action={<button>Check connection</button>} /></div></section>
        <section><ListRow className="deployment-row" identity={long} state="Running" detail={long} action={<button>Open deployment</button>} /></section>
        <div className="ch-details"><div className="ch-detail-row"><time>Today</time><strong>Checks</strong><div className="ch-detail-facts ch-check-facts"><table className="ch-items-table"><tbody><tr><th>{long}</th><td><div className="ch-check-result">Passed</div></td><td className="ch-check-duration">2s</td><td className="ch-check-actions"><div className="ch-check-links"><button className="link">Re-check</button><a href="#record">View record</a></div></td></tr></tbody></table></div></div></div>
        <MoreMenu label="Long project" items={Array.from({length:60},(_,index)=>({label:"Long action "+index,hint:"A long explanatory hint with an-unbroken-identifier-abcdefghijklmnopqrstuvwxyz0123456789",onClick:()=>{}}))} />
        <Disclosure className="account-corner" placement="above" closeOnAction={false} trigger={<button>Account</button>}><a href="#account">Your account</a><button>Theme</button></Disclosure>
        <Disclosure className="account-actions" closeOnAction={false} trigger={<button>Manage</button>}><label>Role<select><option>Member</option></select></label><button>Set password</button></Disclosure>
        <Disclosure className="head-menu" trigger={<button>Session details</button>}><button>Start fresh</button></Disclosure>
        <Disclosure className="account-menu" closeOnAction={false} trigger={<button>Find</button>}>{close=><div className="topbar-find"><input aria-label="Find" onKeyDown={e=>{if(e.key==='Enter'){e.preventDefault();document.body.dataset.found=e.currentTarget.value;close()}}}/><button onClick={close}>First match</button></div>}</Disclosure>
      </main>
    </>)` },
})
const browser = await chromium.launch({ headless: true })
const out = process.env.APPTOWN_LAYOUT_OUT
if (out) await mkdir(out, { recursive: true })
try {
  const page = await browser.newPage()
  await page.setContent(`<style>@layer tokens, base, components, pages, utilities; ${css}</style><div id="root"></div>`)
  await page.addScriptTag({ content: fixture.outputFiles[0].text })
  await page.getByRole('button', { name: 'Review change', exact: true }).waitFor()
  await page.locator('.split-button > button').click()
  assert.equal(await page.evaluate(() => document.body.dataset.main), 'yes')
  assert.equal(await page.locator('.popover').count(), 0, 'primary action stays separate')
  // The detector ignores padding and hints but rejects an actually wrapped label.
  await page.evaluate(() => {
    const fixtures = document.createElement('div')
    fixtures.id = 'line-fixtures'
    fixtures.innerHTML = '<div class="list-action"><button style="padding-block:40px">Padded label</button><button>Label<span class="btn-sub">Hint on another line</span></button><button style="white-space:normal;width:60px">Wrapped action label</button></div>'
    document.body.append(fixtures)
  })
  const wrapped = await page.evaluate(wrappedRowActions)
  assert.equal(wrapped.length, 1)
  assert.equal(wrapped[0].label, 'Wrapped action label')
  assert.ok(wrapped[0].lines > 1)
  await page.evaluate(() => document.getElementById('line-fixtures').remove())
  for (const width of [390, 800, 1440]) {
    await page.setViewportSize({ width, height: 844 })
    for (const size of ['small', 'medium', 'large']) {
      await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
      assert.ok(await page.locator('.ch-decision-choices').evaluate(element => { const widths = [...element.children].map(child => child.getBoundingClientRect().width); return Math.max(...widths) - Math.min(...widths) < 1 }), `equal decision choices at ${width}, ${size}`)
      assert.deepEqual(await page.evaluate(wrappedRowActions), [], `one-line action labels at ${width}, ${size}`)
      assert.ok(await page.locator('.int-list-row button').isVisible(), 'integration action remains visible')
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `page fits at ${width}, ${size}`)
      for (const name of ['Change: more options', 'More actions for Project', 'More actions for Long project', 'Session details', 'Account', 'Manage', 'Find']) {
        const trigger = page.getByRole('button', { name, exact: true })
        await trigger.click()
        const panel = page.locator('.popover')
        await panel.waitFor()
        assert.ok(await panel.evaluate(element => {
          const r = element.getBoundingClientRect()
          const color = getComputedStyle(element).backgroundColor
          return r.left >= 0 && r.top >= 0 && r.right <= innerWidth && r.bottom <= innerHeight && color !== 'rgba(0, 0, 0, 0)' && element.contains(document.activeElement)
        }), `opaque, focused, visible ${name} at ${width}, ${size}`)
        if (name === 'More actions for Long project') assert.ok(await panel.evaluate(element => element.scrollHeight > element.clientHeight), 'long disclosure scrolls within viewport')
        if (name === 'Account') assert.equal(await panel.locator('a').getAttribute('href'), '#account')
        if (name === 'Find') {
          await page.keyboard.type('long project')
          assert.equal(await panel.locator('input').inputValue(), 'long project')
          await page.keyboard.press('Enter')
          assert.equal(await page.evaluate(() => document.body.dataset.found), 'long project')
        } else await page.keyboard.press('Escape')
        await panel.waitFor({ state: 'detached' })
        assert.ok(await trigger.evaluate(element => element === document.activeElement), `focus returns to ${name}`)
        await trigger.click()
        await page.locator('main').click({ position: { x: 2, y: 2 } })
        await panel.waitFor({ state: 'detached' })
      }
      if (out) for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        await page.screenshot({ path: `${out}/${width}-${size}-${theme}.png`, fullPage: true })
      }
    }
  }
  // The existing visual viewport variables keep the composer in the shortened
  // keyboard viewport; controls retain touch targets rather than shrinking.
  await page.setViewportSize({ width: 390, height: 844 })
  await page.setContent(`<style>@layer tokens, base, components, pages, utilities; ${css}</style>
    <div class="layout" style="--viewport-h:450px"><main class="main"><div class="console"><div class="console-controls controls-open"><div class="console-head"><h1>Change chat</h1><button>Open preview</button><button>Check and land</button><button>Restart preview</button></div></div><div class="chat-pane"><div class="chat-scroll" style="flex:1;min-height:0;overflow:auto">Conversation</div><div class="chat-input row"><textarea aria-label="Message"></textarea><button>Send</button></div></div></div></main></div>`)
  for (const size of ['small', 'medium', 'large']) {
    await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
    await page.getByRole('textbox').focus()
    assert.ok(await page.evaluate(() => [...document.querySelectorAll('.console button')].every(button => { const r = button.getBoundingClientRect(); return r.height >= 44 && r.width >= 44 })))
    assert.ok(await page.getByRole('textbox').evaluate(element => { const r = element.getBoundingClientRect(); return r.bottom <= 450 && r.height >= 44 && r.width >= 100 }))
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
  }
  await page.emulateMedia({ reducedMotion: 'reduce' })
  assert.equal(await page.evaluate(() => { const sidebar = document.createElement('nav'); sidebar.className = 'sidebar open'; document.body.append(sidebar); return getComputedStyle(sidebar).transitionDuration }), '0s')
  console.log('layout: label fixtures, rows, disclosures and phone composer passed')
} finally { await browser.close() }
