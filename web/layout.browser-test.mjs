import { productionCSS } from './production-css.mjs'
import assert from 'node:assert/strict'
import { mkdir, readFile } from 'node:fs/promises'
import { build } from 'esbuild'
import { rowFontDifferences } from './row-font-sizes.mjs'
import { wrappedRowActions } from './row-action-lines.mjs'
// People navigation is a route contract, independent of rendered tab state.
const app = await readFile(new URL('./src/App.tsx', import.meta.url), 'utf8')
for (const path of ['/people/accounts', '/people/accounts/:name', '/people/groups', '/people/groups/:name', '/people/roles', '/people/sign-in', '/people/agents-and-tokens']) assert.ok(app.includes(`path="${path}"`), `missing People route: ${path}`)
assert.ok(!app.includes('path="/people"') && !app.includes('/people?tab='), 'old People tab route remains')
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE)
const css = await productionCSS()
const fixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { ProjectListRow } from './ProjectListRow'
    import { DailyChart } from './pages/Usage'
    import { Disclosure } from './Popover'
    import { Status } from './Status'
    import { SplitButton } from './SplitButton'
    import { MoreMenu } from './MoreMenu'
    import { ListRow } from './ListRow'
    import { ListTable } from './ListTable'
    import { LibraryRow, LibraryRows } from './LibraryRow'
    import AgentActivity from './AgentActivity'
    import { Tabs } from './Tabs'
    const long = 'A very long project identity with a long-unbroken-identifier-abcdefghijklmnopqrstuvwxyz0123456789'
    createRoot(document.getElementById('root')).render(<MemoryRouter>
      <main style={{padding: 12}}>
        <div className="row"><span className="row-status"><Status word="unknown" help="No runtime reading." /></span><span className="row-status"><Status word="unknown" help="No current inventory confirms presence. Ask an administrator to check this target." /></span></div>
        <div className="project-rows">{['Short description', long].map((blurb,i)=><ProjectListRow key={i} project={{slug:'project-'+i,name:i?'A project with more words':'Short name',blurb,hasDeployments:false,canChange:false,observation:'unknown'}} />)}</div>
        <ListTable label="Wrapping records" rows={["Short",long]} rowKey={r=>r} columns={[{key:"name",label:"Name",phone:"name",render:r=><a href="#record">{r}</a>},{key:"detail",label:"Description",phone:"detail",render:r=>r}]} />
        <LibraryRows><LibraryRow name="Short policy" to="/policies/short" /><LibraryRow name={long} to="/policies/long" /></LibraryRows>
        <DailyChart days={Array.from({length:90},(_,i)=>new Date(Date.UTC(2026,7,i+1)).toISOString().slice(0,10))} perDay={new Map([['2026-10-29',{step:100,console:50,other:10,cost:0}]])} />
        <SplitButton compact icon="" label="Change" onMain={()=>{document.body.dataset.main='yes'}} items={[{label:'Catalog change',hint:'Choose a prepared change'}]} />
        <div className="ch-page" style={{paddingInline:6}}><Tabs className="tabs ch-tabs">{['Overview','Code','Conversation','Details'].map((label,i)=><a key={label} className={'tab'+(i===0?' active':'')} href="#change">{label}</a>)}</Tabs></div>
        <div className="row console-head" style={{marginBottom:8}}><h1 style={{margin:0}}>Project chat</h1><span className="badge here">questions &amp; setup</span><span className="console-head-space" style={{flex:1}} /><AgentActivity connected busy={false} /><div className="head-menu"><button aria-label="Chat menu">⋯</button></div></div>
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
    </MemoryRouter>)` },
})
// Render the real People page so its phone overrides and token options are covered.
const peopleFixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
    import People from './pages/People'
    import { TokensCard } from './pages/AccountAccess'
    window.fetch = async path => new Response(JSON.stringify(String(path).includes('/authz/me')
      ? {instance:{'identity:manage-auth':true}} : {users:[{username:'sam'}],tokens:[],delegations:[],groups:[],roles:[],grants:[]}), {headers:{'Content-Type':'application/json'}})
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/people/agents-and-tokens']}><main style={{padding:12}}><People view="agents-and-tokens" /><TokensCard personal /></main></MemoryRouter></QueryClientProvider>)
  ` },
})
const conformanceFixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
    import Conformance from './pages/Conformance'
    window.fetch = async path => {
      const data = String(path).endsWith('/conformance') ? await new Promise(resolve => {
        window.finishConformance = () => resolve({openRemediations:[],byProject:{},lens:{kind:'current'},projects:[],reviews:[{status:'done',projects:3},{status:'done',projects:3}],summary:{findings:0}})
      }) : {whatIfs:[]}
      return new Response(JSON.stringify(data), {headers:{'Content-Type':'application/json'}})
    }
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><main style={{padding:18}}><Conformance tab="what-ifs" /></main></MemoryRouter></QueryClientProvider>)
  ` },
})
const targetFixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
    import { InfrastructureNew } from './pages/InfrastructureNew'
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><main style={{padding:18}}><InfrastructureNew /></main></MemoryRouter></QueryClientProvider>)
  ` },
})
const trailFixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { QueryClientProvider } from '@tanstack/react-query'
    import { newQueryClient, getKey } from './data'
    import TopBar from './TopBar'
    const me = {mode:'internal',user:'sam'}, client = newQueryClient()
    window.fetch = async path => new Response(JSON.stringify(String(path).endsWith('/auth/me') ? me : String(path).endsWith('/authz/me') ? {instance:{'platform:manage-deploy-targets':true}} : {items:[],sources:[]}))
    client.setQueryData(getKey('/auth/me'), me)
    client.setQueryData(getKey('/authz/me'), {instance:{'platform:manage-deploy-targets':true}})
    client.setQueryData(getKey('/attention'), {items:[],sources:[]})
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/club-newsletter/memory/decisions']}><TopBar me={me} narrow /></MemoryRouter></QueryClientProvider>)
  ` },
})
const browser = await chromium.launch({ headless: true })
const out = process.env.APPTOWN_LAYOUT_OUT
if (out) await mkdir(out, { recursive: true })
try {
  const page = await browser.newPage()
  await page.route('http://layout.test/**', route => route.fulfill({ contentType: 'text/html', body: `<style>@layer tokens, base, components, pages, utilities; ${css}</style><div id="root"></div>` }))
  await page.goto('http://layout.test/')
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
  await page.locator('.project-name').last().evaluate(el => {el.style.fontSize='var(--fs-xl)'})
  assert.ok((await page.evaluate(rowFontDifferences)).length > 0, 'font detector rejects different row title sizes')
  await page.locator('.project-name').last().evaluate(el => {el.style.removeProperty('font-size')})
  await page.locator('.pack-row .list-identity').last().evaluate(el => {el.style.fontSize='var(--fs-xl)'})
  assert.ok((await page.evaluate(rowFontDifferences)).some(d => d.row === '.list-row'), 'font detector compares rows inside library wrappers')
  await page.locator('.pack-row .list-identity').last().evaluate(el => {el.style.removeProperty('font-size')})
  await page.locator('.record-table tbody tr:last-child td:first-child').evaluate(el => {el.style.fontSize='var(--fs-xl)'})
  assert.ok((await page.evaluate(rowFontDifferences)).some(d => d.row === '.record-table tbody tr'), 'font detector compares the same column across record rows')
  await page.locator('.record-table tbody tr:last-child td:first-child').evaluate(el => {el.style.removeProperty('font-size')})
  for (const width of [390, 800, 1440]) {
    await page.setViewportSize({ width, height: 844 })
    for (const size of ['small', 'medium', 'large']) {
      await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
      assert.equal(await page.locator('.ch-tabs').evaluate(strip => new Set([...strip.children].map(tab => Math.round(tab.getBoundingClientRect().top))).size), 1, `change tabs stay on one row at ${width}, ${size}`)
      assert.ok(await page.locator('.console-head .agent-activity .chip').evaluate(el => el.scrollWidth <= el.clientWidth + 1), `chat activity label stays complete at ${width}, ${size}`)
      assert.ok(await page.locator('.console-head').evaluate(head => [...head.children].every(el => { const r = el.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth })), `chat header fits at ${width}, ${size}`)
      assert.ok(await page.locator('.ch-decision-choices').evaluate(element => { const widths = [...element.children].map(child => child.getBoundingClientRect().width); return Math.max(...widths) - Math.min(...widths) < 1 }), `equal decision choices at ${width}, ${size}`)
      assert.deepEqual(await page.evaluate(wrappedRowActions), [], `one-line action labels at ${width}, ${size}`)
      assert.ok(await page.locator('.int-list-row button').isVisible(), 'integration action remains visible')
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `page fits at ${width}, ${size}`)
      assert.ok(await page.getByText('No runtime reading.', {exact:true}).isVisible(), 'status explanation stays inline')
      assert.ok(await page.locator('.project-description').evaluateAll(els => els.every(el => el.scrollWidth <= el.clientWidth + 1)), `free text wraps at ${width}, ${size}`)
      assert.deepEqual(await page.evaluate(rowFontDifferences), [], `same text sizes between rows at ${width}, ${size}`)
      assert.equal(await page.evaluate(() => getComputedStyle(document.documentElement).webkitTextSizeAdjust), '100%', 'iOS does not inflate individual rows')
      assert.ok(await page.locator('.usage-chart').evaluate(plot => {
        const bounds = plot.getBoundingClientRect()
        return [...plot.children].every(bar => { const r = bar.getBoundingClientRect(); return r.left >= bounds.left && r.right <= bounds.right + 0.5 })
      }), `all 90 daily bars fit the plot at ${width}, ${size}`)
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
      if (width <= 800) assert.ok(await page.locator('table[aria-label="Wrapping records"] td').evaluateAll(cells => cells.every(cell => cell.scrollWidth <= cell.clientWidth + 1)), `record text wraps at ${width}, ${size}`)
      if (out) for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        await page.screenshot({ path: `${out}/${width}-${size}-${theme}.png`, fullPage: true })
      }
    }
  }
  for (const size of ['small', 'medium', 'large']) {
    await page.setViewportSize({width:390,height:844})
    await page.goto('http://layout.test/')
    await page.evaluate(size => {document.documentElement.dataset.size=size},size)
    await page.addScriptTag({content:trailFixture.outputFiles[0].text})
    await page.locator('.attention-light').last().waitFor()
    assert.ok(await page.locator('.topbar').evaluate(bar => [...bar.children].every(el => {const r=el.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth+1})), `deep phone trail keeps controls on screen at ${size}: ${JSON.stringify(await page.locator('.topbar').evaluate(bar => [...bar.children].map(el=>({cls:el.className, left:el.getBoundingClientRect().left,right:el.getBoundingClientRect().right}))))}`)
  }
  for (const width of [390, 800, 1440]) {
    await page.setViewportSize({ width, height: 844 })
    for (const size of ['small', 'medium', 'large']) {
      await page.goto('http://layout.test/')
      await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
      await page.addScriptTag({ content: peopleFixture.outputFiles[0].text })
      await page.getByRole('button', { name: 'Mint token', exact: true }).waitFor()
      assert.ok(await page.getByRole('region', { name: 'Access tokens' }).locator('input, select, button').evaluateAll(controls => controls.length === 3 && controls.every(control => {
        const r = control.getBoundingClientRect()
        return r.left >= 0 && r.right <= innerWidth && r.width > 0
      })), `all token controls fit at ${width}, ${size}`)
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `People page fits at ${width}, ${size}`)
      if (out) await page.screenshot({ path: `${out}/people-${width}-${size}.png`, fullPage: true })
    }
  }
  for (const size of ['medium', 'large']) {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('http://layout.test/')
    await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
    await page.addScriptTag({ content: conformanceFixture.outputFiles[0].text })
    await page.waitForFunction(() => !!window.finishConformance)
    await page.waitForFunction(() => {
      const strip = document.querySelector('.conformance-tabs'), active = strip?.querySelector('.active')
      if (!active) return false
      const box = strip.getBoundingClientRect(), item = active.getBoundingClientRect()
      return item.left >= box.left - 1 && item.right <= box.right + 1
    })
    await page.evaluate(() => window.finishConformance())
    await page.getByRole('link', { name: 'Fleet reviews (2)', exact: true }).waitFor()
    await page.waitForFunction(() => {
      const strip = document.querySelector('.conformance-tabs'), active = strip.querySelector('.active')
      const box = strip.getBoundingClientRect(), item = active.getBoundingClientRect()
      return item.left >= box.left - 1 && item.right <= box.right + 1
    }, null, { timeout: 2000 })
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Conformance fits after count loads at 390, ${size}`)
    if (out) await page.screenshot({ path: `${out}/conformance-390-${size}.png`, fullPage: true })
  }
  for (const width of [390, 800, 1440]) {
    for (const size of ['small', 'medium', 'large']) {
      await page.setViewportSize({ width, height: 844 })
      await page.goto('http://layout.test/')
      await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
      await page.addScriptTag({ content: targetFixture.outputFiles[0].text })
      await page.getByRole('button', { name: /Docker host/ }).click()
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `target connection page fits at ${width}, ${size}`)
      assert.ok(await page.locator('.wiz-steps li, .wiz-fields input').evaluateAll(els => els.every(el => {
        const r = el.getBoundingClientRect()
        return r.left >= 0 && r.right <= innerWidth
      })), `target connection steps and fields fit at ${width}, ${size}`)
      if (out) await page.screenshot({ path: `${out}/target-connection-${width}-${size}.png`, fullPage: true })
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
