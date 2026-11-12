// Actual page components and production CSS, with disposable API responses.
import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
import { build } from 'esbuild'
import { productionCSS } from './production-css.mjs'
import { fontsReady } from './fonts-ready.mjs'
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE || 'playwright')
const bundle = await build({
  bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src', import.meta.url).pathname, contents: `
    import {createRoot} from 'react-dom/client'
    import {MemoryRouter,Routes,Route} from 'react-router-dom'
    import {QueryClientProvider} from '@tanstack/react-query'
    import {newQueryClient} from './data'
    import {ProjectPage} from './pages/conformance/ProjectPage'
    import {RemediationPage} from './pages/conformance/RemediationPage'
    import {EvalScorecardPage} from './pages/Evals'
    import {SecretsTab} from './pages/project/settings/Secrets'
    import Features from './pages/Features'
    const scene = location.pathname.slice(1)
    const lanes = {conforms:0,excepted:0,findings:0,flagged:1,unassessed:0,broken:1,na:0}
    const verdict = {projectSlug:'shop',evaluator:'llm',severity:'medium',block:'tools',version:2,check:'root-toolchain',detail:'No toolchain is declared',statusLabel:'possible problem',state:'flagged',lane:'flagged'}
    const view = {projects:[{slug:'shop',name:'Shop',standing:'review-broken',sentence:'Review incomplete',lanes}],byProject:{shop:[{...verdict,id:'flag'},{...verdict,id:'broken',lane:'broken',state:'error',statusLabel:'unknown',check:'toolchain-version',detail:'The check did not finish.'}]},exceptions:{},adjudications:{},priorConfirmations:{},checkDefs:{},openRemediations:[]}
    const features = [
      ['ways','Ways of working','always','Packs, workflows, guidance and policy pages'],
      ['usage','Usage','always','Usage and disk pages'],
      ['previews','Previews','always','Change previews'],
      ['complaints','Complaints','always','Reports about AppTown'],
      ['conformance','Conformance','standard','Reviews, verdicts and remediation'],
      ['backups','Backups','standard','Backup copies and restoration'],
      ['chats','Chats','standard','Interactive change chats'],
      ['worker','Worker','standard','Worker settings'],
      ['models','Models','standard','Model setup pages'],
      ['updates','Updates','standard','Update checks and settings'],
      ['evals','Evals','beta','Model and workflow evaluations'],
      ['what-ifs','What-ifs','beta','Proposed guidance comparisons'],
      ['restore-rehearsals','Restore rehearsals','beta','Rehearsal and restored-copy environments'],
      ['conformance-estate','Conformance estate view','beta','Estate conformance inventory'],
      ['app-platform','App Platform targets','beta','DigitalOcean App Platform targets'],
      ['portainer','Portainer targets','beta','Portainer targets'],
      ['bucket-site','Bucket-site targets','beta','S3 bucket sites'],
      ['mcp','MCP for outside agents','beta','Outside agents using the MCP endpoint'],
      ['memory','Project memory','beta','Project notes and memory pages'],
    ].map(([area,name,tier,covers])=>({area,name,tier,covers,enabled:tier!=='beta',docs:'/docs/guides/features'}))
    window.fetch=async path=>{
      const url=String(path)
      let data={}
      if(url.endsWith('/authz/me')) data={instance:{'identity:manage-auth':true,'secret:reveal':true},project:{'secret:bind':true}}
      else if(url.endsWith('/features')) data=features
      else if(url.endsWith('/scans')) data={scans:{main:'main',source:[],running:[],historical:[],known:false,stale:false,lastScan:'0001-01-01T00:00:00Z'},recent:[]}
      else if(url.endsWith('/remediations/job')) data={remediation:{id:'job',number:2,status:'running',statusLabel:'working',sentence:'Fix the toolchain checks',counts:{total:1,stuck:1},actor:'Sam',mode:'attended',createdAt:'2026-11-07T01:00:00Z'},occupancy:{kind:'remediation',workers:2,busy:0,queued:0,holding:[]},attempts:{},items:[{id:'stuck',projectSlug:'shop',state:'stuck',statusLabel:'failed',check:'root-toolchain',attempt:1,reason:'The build failed.'}]}
      else if(url.endsWith('/evals/eval')) data={id:'eval',number:4,startedBy:'Sam',startedAt:'2026-11-07T01:00:00Z',meaning:'Start an evaluation in throwaway copies.',subjectKind:'model',subject:'frontier',budgetMicros:5000000,state:'done',statusLabel:'finished',costMicros:1250000,incomplete:0,tallies:[],comparable:0,notComparable:0,arms:[],armCosts:{},complaints:[]}
      else if(url.endsWith('/secrets')) data={declared:true,spec:'secretspec.toml',project:'Shop',secrets:[{name:'API_KEY',consumers:['web'],allParts:true}],stores:[{name:'internal',title:'Built-in store',available:true}],deployments:[{name:'main',chosen:'default',profile:'default',missing:[],secrets:[{name:'API_KEY',required:true,bound:true,store:'internal',version:'1'}]}]}
      else if(url.endsWith('/reveal')) return new Response(JSON.stringify({error:'The secret store is unavailable. Ask an administrator to check its connection.'}),{status:503})
      return new Response(JSON.stringify(data))
    }
    const page = scene.startsWith('conformance-')?<ProjectPage view={view} slug="shop" readOnly={false} onRunOne={()=>{}}/>:scene.startsWith('remediation-')?<RemediationPage id="job"/>:scene==='secrets-error'?<SecretsTab slug="shop"/>:scene==='features'?<Features/>:<Routes><Route path="/evals/:id" element={<EvalScorecardPage/>}/></Routes>
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={newQueryClient()}><MemoryRouter initialEntries={[scene==='eval-delete'?'/evals/eval':'/']}><div className="layout"><aside className="sidebar"/><main className="main">{page}</main></div></MemoryRouter></QueryClientProvider>)
  ` },
})
const css = await productionCSS()
const out = process.env.DIALOGS_OUT
if (out) await mkdir(out, { recursive: true })
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage()
  const nativeDialogs = []
  page.on('dialog', dialog => { nativeDialogs.push(dialog.type()); void dialog.dismiss() })
  await page.route('http://probe.test/**', route => route.fulfill({ contentType: 'text/html', body: '<html></html>' }))
  for (const width of [1440, 390]) for (const size of ['medium', 'large']) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 1000 })
    for (const scene of ['features', 'conformance-confirm', 'conformance-risk', 'remediation-retry', 'remediation-skip', 'eval-delete', 'secrets-error']) {
      await page.goto('http://probe.test/' + scene)
      await page.setContent(`<style>${css}</style><div id="root"></div>`)
      await page.evaluate(({size,theme}) => { document.documentElement.dataset.size=size; document.documentElement.dataset.theme=theme }, {size,theme})
      await page.addScriptTag({ content: bundle.outputFiles[0].text })
      await fontsReady(page)
      const action = { 'conformance-confirm': 'Confirm finding', 'conformance-risk': 'Accept risk', 'remediation-retry': 'Retry', 'remediation-skip': 'Skip', 'eval-delete': 'Delete this eval', 'secrets-error': 'Reveal…' }[scene]
      if (action) await page.getByRole('button', { name: action, exact: true }).first().click()
      if (scene === 'secrets-error') {
        await page.getByRole('button', { name: 'Yes', exact: true }).click()
        await page.getByRole('alert').waitFor()
      }
      if (scene === 'conformance-risk') {
        assert.match(await page.locator('main').innerText(), /this check version, until revoked/)
        assert.ok(await page.getByRole('button', { name: 'Accept risk', exact: true }).isDisabled())
      }
      if (scene === 'features') {
        await page.getByRole('heading', { name: 'Beta', exact: true }).waitFor()
        // The guide is behind the page's ? help, not a link written on the page.
        assert.equal(await page.locator('main a').count(), 0)
        assert.equal(await page.getByRole('button', { name: 'About Features', exact: true }).count(), 1)
        assert.equal(await page.getByRole('button', { name: /Ways of working: turn/ }).count(), 0)
      }
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Page overflow: ${scene} at ${width}, ${size}`)
      assert.deepEqual(await page.locator('main input, main button').evaluateAll(els => els.filter(el => {
        const r=el.getBoundingClientRect(); return r.width && (r.left<0 || r.right>innerWidth+1)
      }).map(el => el.textContent || el.getAttribute('aria-label'))), [], `Clipped controls: ${scene} at ${width}, ${size}`)
      if (out) await page.screenshot({ path: `${out}/${scene}-${width}-${size}-${theme}.png`, fullPage: true })
    }
  }
  assert.deepEqual(nativeDialogs, [])
} finally { await browser.close() }
console.log('In-page decisions, errors and Features fit desktop and phone, Medium/Large, light/dark')
