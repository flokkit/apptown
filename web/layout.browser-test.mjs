import { productionCSS } from './production-css.mjs'
import assert from 'node:assert/strict'
import { mkdir, readFile } from 'node:fs/promises'
import { build } from 'esbuild'
import { rowFontDifferences } from './row-font-sizes.mjs'
import { wrappedRowActions } from './row-action-lines.mjs'
import { splitWords } from './word-splits.mjs'
// People navigation is a route contract, independent of rendered tab state.
const app = await readFile(new URL('./src/App.tsx', import.meta.url), 'utf8')
for (const path of ['/people/accounts', '/people/accounts/:name', '/people/groups', '/people/groups/:name', '/people/roles', '/people/sign-in', '/people/agents-and-tokens']) assert.ok(app.includes(`path="${path}"`), `missing People route: ${path}`)
assert.ok(!app.includes('path="/people"') && !app.includes('/people?tab='), 'old People tab route remains')
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE)
// WCAG contrast of two computed rgb() colours, ignoring alpha.
const rgb = c => c.match(/[\d.]+/g).slice(0, 3).map(Number)
const lum = c => { const [r, g, b] = rgb(c).map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 }); return 0.2126 * r + 0.7152 * g + 0.0722 * b }
const contrast = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05) }
const hexToRgb = h => { const n = parseInt(h.replace('#', ''), 16); return `rgb(${n >> 16}, ${(n >> 8) & 255}, ${n & 255})` }
const css = await productionCSS()
const fixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { useState } from 'react'
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { ProjectListRow } from './ProjectListRow'
    import { DailyChart } from './pages/Usage'
    import { Disclosure } from './Popover'
    import { Status } from './Status'
    import { SplitButton } from './SplitButton'
    import { MoreMenu, FormMenu } from './MoreMenu'
    import { ListRow } from './ListRow'
    import { ListTable } from './ListTable'
    import { LibraryRow, LibraryRows } from './LibraryRow'
    import AgentActivity from './AgentActivity'
    import { Tabs } from './Tabs'
    import { renderMd } from './md'
    // The change page's tabs, switchable, so the current tab can be followed.
    function ChangeTabs() {
      const [current, setCurrent] = useState('Overview')
      return <Tabs className="tabs ch-tabs">{['Overview','Code','Conversation','Details'].map(label=><a key={label} className={'tab'+(label===current?' active':'')} aria-current={label===current?'page':undefined} href="#change" onClick={e=>{e.preventDefault();setCurrent(label)}}>{label}</a>)}</Tabs>
    }
    const long = 'A very long project identity with a long-unbroken-identifier-abcdefghijklmnopqrstuvwxyz0123456789'
    createRoot(document.getElementById('root')).render(<MemoryRouter>
      <main style={{padding: 12}}>
        <div className="row"><span className="row-status"><Status word="unknown" help="No runtime reading." /></span><span className="row-status"><Status word="unknown" help="No current inventory confirms presence. Ask an administrator to check this target." /></span></div>
        <div className="project-rows">{['Short description', long].map((blurb,i)=><ProjectListRow key={i} project={{slug:'project-'+i,name:i?'A project with more words':'Short name',blurb,hasDeployments:false,canChange:false,observation:'unknown'}} />)}</div>
        <ListTable label="Wrapping records" rows={["Short",long]} rowKey={r=>r} columns={[{key:"name",label:"Name",phone:"name",render:r=><a href="#record">{r}</a>},{key:"detail",label:"Description",phone:"detail",render:r=>r}]} />
        <LibraryRows><LibraryRow name="Short policy" to="/policies/short" /><LibraryRow name={long} to="/policies/long" /></LibraryRows>
        <DailyChart days={Array.from({length:90},(_,i)=>new Date(Date.UTC(2026,7,i+1)).toISOString().slice(0,10))} perDay={new Map([['2026-10-29',{step:100,console:50,other:10,cost:0}]])} />
        <SplitButton compact icon="" label="Change" onMain={()=>{document.body.dataset.main='yes'}} items={[{label:'Catalog change',hint:'Choose a prepared change'}]} />
        <div className="ch-page" style={{paddingInline:6}}><ChangeTabs /></div>
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
        <section className="content-fixtures">
          <button className="primary disabled-fixture" disabled>Save pack</button>
          <button className="subtle danger quiet-danger-fixture">revoke</button>
          <p className="link-fixture">This address does not identify a page. <a href="#projects">Find your project</a></p>
          <div className="md guide-fixture" dangerouslySetInnerHTML={{__html: renderMd('Run this on the host:\\n\\n\\u0060\\u0060\\u0060\\nsudo tailscale serve --bg http://127.0.0.1:7071 --set-path /preview-proxy-for-the-whole-team --set-header X-Forwarded-Host=apptown.example.org --verbose\\n\\u0060\\u0060\\u0060\\n\\nKeep local Docker enabled and leave \\u0060--worker-url\\u0060 unset: a worker moves previews to the host too, for code you trust.')}} />
          <pre className="source source-fixture">{'metadata:\\n  name: adopt-integration\\n  description: Connect the app to a newly attached cross-cutting service and prove the app still runs, minimally, without changing anything else in the repository.'}</pre>
          <button className="card infra-card wiz-kind wiz-fixture"><span className="infra-head">Docker host</span><span>Docker over SSH.</span></button>
        </section>
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
    import { withFeatures } from './features.fixture'
    window.fetch = withFeatures(async path => {
      const data = String(path).endsWith('/conformance') ? await new Promise(resolve => {
        window.finishConformance = () => resolve({openRemediations:[],byProject:{},lens:{kind:'current'},projects:[],reviews:[{status:'done',projects:3},{status:'done',projects:3}],summary:{findings:0}})
      }) : {whatIfs:[]}
      return new Response(JSON.stringify(data), {headers:{'Content-Type':'application/json'}})
    })
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><main style={{padding:18}}><Conformance tab="what-ifs" /></main></MemoryRouter></QueryClientProvider>)
  ` },
})
// Real current-state rows: status words must stay whole beside wrapping names.
const conformanceProjectFixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
    import { ProjectPage } from './pages/conformance/ProjectPage'
    const checks = ['short', 'root-toolchain', 'long-unbroken-check-identifier-abcdefghijklmnopqrstuvwxyz0123456789']
    const view = {projects:[{slug:'shop',name:'Shop',sentence:'Review incomplete',lanes:{broken:3}}],
      byProject:{shop:checks.map(check=>({id:check,projectSlug:'shop',evaluator:'llm',lane:'broken',state:'error',statusLabel:'unknown',severity:'medium',block:'tools',version:2,check,detail:'check did not finish: token limit reached'}))},
      exceptions:{},adjudications:{},priorConfirmations:{},checkDefs:{},openRemediations:[],reviews:[]}
    window.fetch = async () => new Response('{}')
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><main style={{padding:18}}><ProjectPage view={view} slug="shop" readOnly={false} onRunOne={()=>{}} /></main></MemoryRouter></QueryClientProvider>)
  ` },
})
const targetFixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
    import { InfrastructureNew } from './pages/InfrastructureNew'
    import { withFeatures } from './features.fixture'
    window.fetch = withFeatures(async () => new Response('{}'))
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><main style={{padding:18}}><InfrastructureNew /></main></MemoryRouter></QueryClientProvider>)
  ` },
})
// The number of lines an element's text runs to, by its line boxes.
const textLines = el => { const range = document.createRange(); range.selectNodeContents(el); return new Set([...range.getClientRects()].filter(r => r.width > 0).map(r => Math.round(r.bottom))).size }
// Phone list shapes from real pages: a deployment's parts with their CPU and
// memory, its technical parts table with a long state, an activity row
// styled by its page, a title with its status and button, and a table of
// row headers inside the change page's wrappers.
const phoneListsFixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
    import { DeploymentContainers } from './pages/project/DeploymentContainers'
    import { DiagnosticsDetail } from './pages/project/settings/Diagnostics'
    import { ListRow } from './ListRow'
    import { ListTable } from './ListTable'
    import { Status } from './Status'
    const reading = {waiting:false,stale:false,unknown:false,observedAt:'2026-11-09T13:36:00Z'}
    const facts = {id:'c1',status:'running',running:true,oomKilled:false,restarts:0,startedAt:'2026-11-09T13:36:00Z',health:'not configured'}
    const diagnostics = {reading,deployment:'main',generation:10,desiredGeneration:10,commit:'40ce2c8fed79aa',runtime:{state:'running',observedAt:'2026-11-09T22:52:00Z',stale:false},containers:true,
      parts:[{part:'web',public:true,generation:10,presence:'running',facts,stale:false},{part:'postgres',need:true,generation:10,presence:'running',facts,stale:false}],
      events:[],eventsKnown:true,eventsSource:'docker',logMaxLines:200,logMaxBytes:65536}
    const used = (part, need, usedBytes) => ({part,public:!need,need,memory:'256 MB',cpu:'',overrideLabel:{},overrideApplied:false,observed:{part,need,usedBytes,limitBytes:268435456,cpuCores:0,cpuLimit:0,image:'sha256:b56a936be1430000',known:true}})
    const resources = {reading,recorded:true,parts:[used('web',false,65956249),used('postgres',true,25375539)],limits:[],limitsKnown:true,limitsSource:'docker'}
    window.fetch = async path => new Response(JSON.stringify(String(path).endsWith('/resources') ? resources : String(path).endsWith('/diagnostics') ? diagnostics : {}), {headers:{'Content-Type':'application/json'}})
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><main className="main" style={{padding:16}}>
      <div id="parts"><DeploymentContainers slug="if-only" name="main" /></div>
      <div id="technical-parts"><DiagnosticsDetail d={diagnostics} busy={false} onLog={() => {}} /></div>
      <div id="grid-list" style={{display:'grid'}}><section><ListTable label="Connections" rows={['ChatGPT']} rowKey={r=>r} columns={[{key:'agent',label:'Agent',phone:'name',render:r=>r},{key:'permissions',label:'Permissions',render:()=><><span>Can create and edit changes</span><code className="tech-line">apptown:user:read, apptown:change:create, apptown:change:write</code></>}]} /></section></div>
      <section id="activity"><ListRow to="/changes/14" className="card row application-overview-card" state={<span className="row-status"><Status word="deployed" /></span>} identity={<><strong>#14 Publish the winter ride times</strong><span className="muted small">Release 16 · main</span></>} /></section>
      <div id="title" className="row"><h1 className="grow">built-in-docker</h1><Status observation word="failed" /><button className="primary">Check target</button></div>
      <div className="ch-page work-page"><section className="ch-change-card"><table id="row-headers" className="ch-facts-table"><tbody><tr><th>Instructions</th><td>Publish the winter ride times on the rides page</td></tr><tr><th>Attachments</th><td>none</td></tr></tbody></table></section></div>
    </main></MemoryRouter></QueryClientProvider>)
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
    const attention = {items:[{key:'workers/a',title:'Worker unavailable',detail:'Work is waiting.',href:'/targets',statusLabel:'failed',severity:'warning',observedAt:'2026-11-10T00:00:00Z',stale:false}],sources:[]}
    window.fetch = async path => new Response(JSON.stringify(String(path).endsWith('/auth/me') ? me : String(path).endsWith('/authz/me') ? {instance:{'platform:manage-deploy-targets':true}} : attention))
    client.setQueryData(getKey('/auth/me'), me)
    client.setQueryData(getKey('/authz/me'), {instance:{'platform:manage-deploy-targets':true}})
    client.setQueryData(getKey('/attention'), attention)
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/club-newsletter/memory/decisions']}><TopBar me={me} narrow /></MemoryRouter></QueryClientProvider>)
  ` },
})
// Overlays on a phone: help and status details open as sized dialogs, row
// menus keep their place and close when their control scrolls away, dialog
// buttons and the close button stay on screen, and Decide is a bottom bar.
// window.overlayCase picks one scene so only one dialog is open at a time.
const overlayFixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { useEffect } from 'react'
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter, useLocation } from 'react-router-dom'
    import { HelpButton, PageHead } from './PageHead'
    import { Status } from './Status'
    import { MoreMenu, FormMenu } from './MoreMenu'
    import { LibraryRow, LibraryRows } from './LibraryRow'
    import { Modal } from './Modal'
    import { Tabs } from './Tabs'
    import { TurnDecision } from './pages/change/Turn'
    function Where() { const at = useLocation(); useEffect(() => { document.body.dataset.path = at.pathname }, [at]); return null }
    const copies = Array.from({length: 30}, (_, i) => <label key={i} className="row row-2"><input type="radio" name="copy" /> Copy {i}</label>)
    const scenes = {
      page: <>
        <div className="page-head"><div className="page-head-row"><h1>Things</h1><HelpButton about="Things" guide="things"><p>What things are.</p></HelpButton></div></div>
        <p><Status word="unknown" help="No runtime reading." /></p>
        <LibraryRows><LibraryRow name="A policy with a name" purpose="What it is for." to="/policies/one" menu={<MoreMenu label="Policy" items={[{label:'Copy',hint:'Make your own copy',onClick:()=>{}}]} />} /></LibraryRows>
        <div style={{height: 3000}} />
      </>,
      'page-head-actions': <main style={{padding:16}}><PageHead page="people" sub="Groups" title="release-managers-x" actions={<FormMenu className="more-menu" trigger={<button aria-label="Group actions">⋯</button>}><button>Delete group</button></FormMenu>} /></main>,
      'modal-long': <Modal title="Restore Production from a backup" onClose={()=>{}} actions={<><button className="danger">Restore</button><button>Cancel</button></>}>{copies}</Modal>,
      'modal-search': <Modal title="New integration" onClose={()=>{}} head={<input placeholder="Search…" aria-label="search" style={{ width: 220 }} />}><p>Tiles.</p></Modal>,
      'modal-tabs': <Modal title={<h2>GitLab docs</h2>} onClose={()=>{}} head={<Tabs className="tabs"><button className="tab active">Developer</button><button className="tab">Admin</button></Tabs>}><p>Docs.</p></Modal>,
      decide: <div className="ch-page">{Array.from({length: 40}, (_, i) => <p key={i}>Line {i} of the change.</p>)}<p id="last-line">The last line of the change.</p>
        <TurnDecision active actions={['approve']}><p>Accept this change?</p></TurnDecision></div>,
    }
    createRoot(document.getElementById('root')).render(<MemoryRouter><Where /><main style={{padding: 12}}>{scenes[window.overlayCase]}</main></MemoryRouter>)
  ` },
})
// Phone round 2: scenes for regressions found in the phone camera after the
// first five phone fixes. window.roundCase picks one scene; the fleet scene
// holds its per-row reads until window.release() so the test can measure a
// row before and after they arrive.
const roundFixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter, Link } from 'react-router-dom'
    import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
    import { DeploymentTable } from './pages/DeploymentTable'
    import { DeployScene } from './pages/change/DeployScene'
    import { Modal } from './Modal'
    import { Section } from './Section'
    import { ListTable } from './ListTable'
    import { ListRow } from './ListRow'
    import { LibraryRow, LibraryRows } from './LibraryRow'
    import { HelpButton } from './PageHead'
    import { ReleaseRows } from './pages/project/Overview'
    import { CapacityTable } from './pages/integrations/CapacityBars'
    import { InfrastructureNew } from './pages/InfrastructureNew'
    import { AdminNetworksCard } from './pages/People'
    import { ServiceKindsPanel } from './pages/integrations/ServiceKinds'
    import { DisplayLabelEdit } from './DisplayLabel'
    import { Switch } from './Switch'
    import { MoreMenu } from './MoreMenu'
    import { Ident } from './Ident'
    import { ChecksRow } from './pages/project/settings/Working'
    const ok = { word: 'working', reason: '' }
    const row = (app, slug, deployment, label) => ({ app, slug, deployment, label, url: 'https://' + deployment + '.example.test', targetName: 'built-in-docker', observation: 'running', status: ok })
    const rows = [row('Club Newsletter', 'club-newsletter', 'production', 'Production'), row('Club Newsletter', 'club-newsletter', 'staging', 'staging'), row('Lunch Orders', 'lunch-orders', 'main', 'main')]
    const tenant = (project, deployment) => ({ kind: 'deployment', name: project + '/' + deployment, project, deployment, containers: 1, used: { known: true, value: 62900000 }, cpuUsed: { known: true, value: 0.02 }, promised: { known: true, value: 0 }, unlimited: 0, cpus: 0, cpuUnlimited: 0, cpuUnread: 0 })
    const answers = path => path.includes('/authz/me') ? { user: 'sam', instance: {}, project: { 'deployment:operate': true, 'deployment:deploy': true } }
      : path.includes('/capacity') ? { target: 'built-in-docker', kind: 'docker-host', elastic: false, stale: false, words: '', tenants: rows.map(r => tenant(r.slug, r.deployment)) }
      : path.includes('/backups') ? { backups: rows.map(r => ({ project: r.slug, deployment: r.deployment, deploymentLabel: r.label, takenAt: '2026-11-08T11:55:00Z', status: 'ok', statusLabel: 'ok', id: r.deployment, projectId: null, trigger: 'manual', actor: 'sam', path: '', bytes: 1, items: [], store: 'host' })), keep: 7, where: '', retention: '' }
      : path.includes('/features') ? [{ area: 'backups', name: 'Backups', enabled: true }, { area: 'deployments', name: 'Deployments', enabled: true }]
      : path.includes('/auth/admin-networks') ? { networks: [], you: '10.0.0.5', youInside: true }
      : path.includes('/services') ? { services: [{ capability: 'hilltop-club-sign-in', title: 'Club sign-in', connector: {}, resource: {}, yaml: '', updatedAt: '2026-11-09T00:00:00Z' }, { capability: 'registry', title: 'Container registry', connector: {}, resource: {}, yaml: '', updatedAt: '2026-11-09T00:00:00Z' }] }
      : {}
    const held = []
    window.release = () => { window.released = true; held.splice(0).forEach(go => go()) }
    window.fetch = async input => {
      const path = String(input)
      const answer = () => new Response(JSON.stringify(answers(path)), { headers: { 'Content-Type': 'application/json' } })
      if (!window.released && /\\/authz\\/me|\\/capacity|\\/backups/.test(path)) return new Promise(done => held.push(() => done(answer())))
      return answer()
    }
    const long = i => '8 Nov, 11:' + (50 - i) + ' · scheduled · ' + (500 + i) + ' KB · this host'
    const scenes = {
      fleet: <Section title="All deployments"><DeploymentTable rows={rows} withApp /></Section>,
      stages: <div style={{ width: 300 }}><div className="deployment-deploy-card"><DeployScene compactStages failedStage="starting" stage="starting" outcome="failed" /></div></div>,
      choices: <Modal title="Restore Production from a backup" fit onClose={() => {}}>
        <fieldset className="stack stack-2">{[0, 1, 2, 3, 4, 5].map(i => <label key={i} className="row row-2 round-choice"><input type="radio" name="copy" /><span>{long(i)}</span></label>)}
          <label className="row round-choice"><input type="radio" name="target" /><span><strong>Default target · This computer</strong><br /><span className="muted small">Runs apps in Docker on the computer AppTown runs on.</span><code className="tech-line muted">built-in-docker</code></span></label>
        </fieldset></Modal>,
      nested: <Section title="Connected coding agents"><div className="people-agents-card-div"><div className="agent-group"><div className="host-row"><strong>Home Assistant as sam</strong><span>Connected</span></div>
          <ListTable label="nested" rows={['Home Assistant']} rowKey={r => r} columns={[{ key: 'n', label: 'Agent', phone: 'name', render: r => r }, { key: 'a', label: 'Account', render: () => 'sam' }]} /></div></div></Section>,
      direct: <Section title="Direct"><ListTable label="direct" rows={['Home Assistant']} rowKey={r => r} columns={[{ key: 'n', label: 'Agent', phone: 'name', render: r => r }, { key: 'a', label: 'Account', render: () => 'sam' }]} /></Section>,
      help: <div className="page-head"><div className="page-head-row"><span className="page-head-title"><h1>Club Newsletter</h1><HelpButton about="Overview" guide="overview"><p>What the overview shows.</p></HelpButton></span></div></div>,
      overview: <div><ListRow to="/changes/1" className="card row application-overview-card" state={<span className="row-status">deployed</span>} identity={<>
          <Link to="/changes/1" className="row grow application-overview-link"><strong>Put up the October newsletter and 9 more</strong><span className="muted small">9 Nov, 22:14</span></Link>
          <span className="muted small"><Link to="/r">Release 18</Link> · members-preview</span></>} /></div>,
      releases: <ReleaseRows slug="club-newsletter" releases={[24, 23, 22, 21, 20].map(n => ({ number: n, serving: n === 23, releasedAt: '2026-11-09T22:0' + (n % 10) + ':00Z', createdAt: '2026-11-09T22:00:00Z' }))} />,
      capacity: <div>
        <div className="cap-bar" style={{ width: 300 }}><span className="cap-seg cap-free" style={{ flex: '0 1 100%' }}><span className="cap-in">1 CPU not allocated</span></span></div>
        <div className="cap-details" role="region" aria-label="App resource table" tabIndex={0}><h2>App resource details</h2><CapacityTable heads={['containers', 'memory used', 'memory limits', 'without a set limit', 'CPU limits']}
          rows={['other containers', 'club-newsletter/Production', 'club-newsletter/members-preview', 'club-newsletter/archive-migration'].map((name, i) => ({ name, kind: 'deployment', cells: [String(11 - i), '62.9 MB · read 2 minutes ago, before the last restart', '256 MB each for 3 containers', '0 of 3 containers', '1 CPU each, 2 without a limit'] }))} /></div></div>,
      links: <div>
        <div className="ch-overview-line"><div className="ch-value">Checks passed</div><Link className="ch-trailer" id="trailer" to="/x">details →</Link></div>
        <Section title="" label="Secrets" action={<button className="link" id="add-secret">Add secret</button>}><p>None.</p></Section>
        <table><tbody><tr><th>Request</th><td><Link id="value-link" to="/r">full request</Link></td></tr></tbody></table>
        <ListTable label="names" rows={['lunch-orders']} rowKey={r => r} columns={[{ key: 'n', label: 'Name', phone: 'name', render: r => <Link id="table-name" to="/p">{r}</Link> }, { key: 's', label: 'State', render: () => 'ok' }]} />
        <div className="list-row project-row"><div className="list-identity"><Link id="mark-link" className="project-mark-link" to="/p">L</Link><Link id="project-name" className="project-name" to="/p"><strong>Lunch Orders</strong></Link></div></div>
        <LibraryRows><LibraryRow name="server-base" purpose="A server." to="/packs/server-base" /></LibraryRows>
        <div className="tabs"><Link id="tab" className="tab" to="/t">Overview</Link></div>
        <Link id="btn-link" className="btn" to="/b">Open app</Link>
      </div>,
      // Phone round 3.
      heading: <div><h1 id="long-name">Projects adopting <Ident>requireindependentproductionapproval</Ident></h1><h1 id="short-name">Projects adopting the policy <Ident>build-requires-scan</Ident></h1></div>,
      newTarget: <InfrastructureNew />,
      networks: <AdminNetworksCard canEdit />,
      slots: <Section title="Tasks"><ListTable label="Tasks" className="slots-table model-slots-model-profile-page-slots-table" rows={['Review']} rowKey={r => r} columns={[
          { key: 'task', label: 'Task', phone: 'name', className: 'model-slots-stack-lead', render: r => <><strong>{r}</strong><div className="muted small">A second set of eyes on finished work</div><div className="muted small">Used by: 8 workflows (e.g. library/adopt-integration)</div></> },
          { key: 'model', label: 'Model', className: 'model-slots-td', render: () => <div><select aria-label="Review model" className="model-slots-cell-select"><option>Same as Main · strong (fake/model)</option></select><div className="muted small">Same as Main · strong (fake/model)</div></div> },
          { key: 'spent', label: 'Spent (7 days)', className: 'small muted model-slots-td-2', render: () => '' }]} /></Section>,
      switch: <Section title="Approval"><div className="members-policy"><label className="small" id="switch-label"><Switch label="Require approval" checked={false} onChange={() => {}} />{' '}Require another person to approve the author's change</label></div></Section>,
      menu: <MoreMenu label="Workflow" items={[{ label: 'Copy', hint: 'Make your own editable copy of this workflow, so changes to it never alter the stock one.', onClick: () => {} }]} />,
      inset: <Section title="Remediations"><ListRow className="card row conformance-list-row" identity={<span id="inset-identity">#1 fix all findings</span>} state="running" /></Section>,
      names: <div>
        <ListTable label="Features" rows={['Backups']} rowKey={r => r} columns={[{ key: 'n', label: 'Area', phone: 'name', render: r => <><strong id="feature-name">{r}</strong><div className="muted small" id="name-note">Copies of each app's data</div></> }, { key: 'c', label: 'What it covers', render: () => 'Copies' }]} />
        <ListTable label="Accounts" rows={['sam']} rowKey={r => r} columns={[{ key: 'n', label: 'Name', phone: 'name', render: r => <Link id="account-name" to="/people/accounts/sam">{r}</Link> }, { key: 'c', label: 'Role', render: () => 'Member' }]} />
        <p>Plain with <strong id="para-strong">bold</strong> words.</p></div>,
      head: <div className="deployment-page-heading"><div className="deployment-page-title"><h1>members-preview</h1><div className="deployment-page-facts muted small">no address yet · No serving release recorded · built-in-docker</div></div>
        <div className="deployment-page-actions"><div className="deployment-menu row row-2"><button aria-label="Deployment menu" className="more-menu-button">⋯</button></div></div></div>,
      label: <Section title="Settings"><DisplayLabelEdit value="design-preview" machineKey="design-preview" path="/targets/design-preview/label" /><dl className="facts"><dt id="next-fact">Kind</dt><dd>Docker host</dd></dl></Section>,
      kinds: <ServiceKindsPanel />,
      checks: <Section title="What that gives it"><div role="list"><ChecksRow slug="club-newsletter" /></div></Section>,
      select: <div><label>Rule<select id="rule-select"><option>step X requires step Y in an earlier stage</option></select></label>
        <div className="row mb-3"><label>Change <select aria-label="Change to link"><option>#15 Check the October newsletter for broken links before it goes out</option></select></label><button>Link change</button></div></div>,
    }
    createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient()}><MemoryRouter><main className="main" style={{ padding: 12 }}>{scenes[window.roundCase]}</main></MemoryRouter></QueryClientProvider>)
  ` },
})
const browser = await chromium.launch({ headless: true })
const out = process.env.APPTOWN_LAYOUT_OUT
if (out) await mkdir(out, { recursive: true })
let page
const consoleLines = []
// A hang reports like a failure: at four minutes, say whether the page
// answers, with its console and a screenshot, then fail (the make target's
// deadline stops the process a minute later if even this cannot run).
const report = async () => {
  if (!page) return
  const answers = await Promise.race([page.evaluate(() => document.activeElement?.outerHTML.slice(0, 200) ?? 'none').then(a => `page answers; focus on ${a}`), new Promise(r => setTimeout(() => r('page does not answer a script within 5 s'), 5000))])
  console.error(`layout: at failure, ${answers}`)
  console.error(`layout: console:\n${consoleLines.slice(-40).join('\n') || '(none)'}`)
  const shot = `${process.env.TMPDIR || '/tmp'}/layout-failure-${process.pid}.png`
  await page.screenshot({ path: shot, timeout: 5000 }).then(() => console.error(`layout: screenshot ${shot}`), () => console.error('layout: no screenshot'))
}
const watchdog = setTimeout(async () => { console.error('layout: still running after 240 s'); await report(); process.exit(1) }, 240_000)
try {
  page = await browser.newPage()
  page.on('console', m => consoleLines.push(`${m.type()}: ${m.text()}`))
  page.on('pageerror', e => consoleLines.push(`pageerror: ${e.message}`))
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
  // A technical id in a desktop table wraps inside its cell: one unbroken
  // line would size the column and push the page sideways.
  for (const width of [820, 900, 1024]) {
    await page.setViewportSize({ width, height: 844 })
    await page.evaluate(() => { const t = document.createElement('table'); t.id = 'tech-fixture'; t.innerHTML = '<tbody><tr><td>Observe target</td><td>waiting</td><td><code class="tech-line muted">protection-observation/01a1c0f7-f732-717b-bf23-3bc2cfec567f/0bfd216d8671931e214963dad76672e5b1978f80b6dae4cf07a49bab12b518bb</code></td></tr></tbody>'; document.querySelector('main').append(t) })
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `technical line keeps the page within ${width}px`)
    await page.evaluate(() => document.getElementById('tech-fixture').remove())
  }
  // On a phone a technical id wraps inside its value rather than scrolling
  // in a box of its own, and a capacity table keeps headings over their
  // figures.
  await page.setViewportSize({ width: 390, height: 844 })
  await page.evaluate(() => {
    const box = document.createElement('div'); box.id = 'phone-content-fixtures'
    box.innerHTML = '<p><code class="tech-line muted">protection-observation/01a1c0f7-f732-717b-bf23-3bc2cfec567f/0bfd216d8671931e214963dad76672e5b1978f80b6dae4cf07a49bab12b518bb</code></p>'
      + '<div class="cap-details" tabindex="0"><table class="record-table small cap-table"><thead><tr><th>Name</th><th>Memory</th><th>CPU</th><th>Disk</th></tr></thead><tbody><tr><td class="phone-name">lunch-orders/main</td><td class="phone-value" data-label="Memory">120 MB</td><td class="phone-state record-fit" data-label="CPU">2%</td><td class="phone-value" data-label="Disk">1.2 GB</td></tr></tbody></table></div>'
    box.innerHTML += '<div role="list"><div role="listitem"><div class="list-row pack-row"><div class="list-identity"><strong>adopt-integration</strong></div><div class="list-state">v1</div><div class="list-action"></div><div class="list-detail muted small"><div class="library-detail"><p>lifecycle · wire</p><pre class="source">description: Connect the app to a newly attached integration, following the integration guidance, and check that it builds and runs.</pre></div></div></div></div></div>'
    document.querySelector('main').append(box)
  })
  // Source in a list row's detail scrolls in its box; it never widens the row.
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'workflow source in a row detail keeps the page within the phone')
  assert.ok(await page.locator('#phone-content-fixtures .list-detail').evaluate(el => {
    const row = el.closest('.list-row')
    const style = getComputedStyle(row)
    const available = row.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight)
    return el.getBoundingClientRect().width >= available - 1
  }), 'workflow detail uses the available phone row width so source remains readable')
  assert.ok(await page.locator('#phone-content-fixtures .tech-line').evaluate(el => el.scrollWidth <= el.clientWidth + 1), 'technical id wraps inside its value on a phone')
  assert.deepEqual(await page.locator('#phone-content-fixtures .cap-table').evaluate(t => [...t.tHead.rows[0].cells].map((th, i) => Math.round(th.getBoundingClientRect().left - t.tBodies[0].rows[0].cells[i].getBoundingClientRect().left))), [0, 0, 0, 0], 'capacity headings sit over their figures on a phone')
  assert.ok(await page.locator('#phone-content-fixtures .cap-table td').evaluateAll(cells => cells.every(cell => ['none', 'normal'].includes(getComputedStyle(cell, '::before').content))), 'capacity cells draw no phone label: their headings name the figures')
  await page.evaluate(() => document.getElementById('phone-content-fixtures').remove())
  for (const width of [390, 800, 1440]) {
    await page.setViewportSize({ width, height: 844 })
    for (const size of ['small', 'medium', 'large']) {
      await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
      assert.equal(await page.locator('.ch-tabs').evaluate(strip => new Set([...strip.children].map(tab => Math.round(tab.getBoundingClientRect().top))).size), 1, `change tabs stay on one row at ${width}, ${size}`)
      // The current tab is always wholly in view, whether or not the row scrolls.
      for (const label of ['Overview', 'Details', 'Overview']) {
        await page.locator('.ch-tabs a.tab', { hasText: label }).click()
        assert.ok(await page.locator('.ch-tabs').evaluate(strip => { const s = strip.getBoundingClientRect(), t = strip.querySelector('[aria-current="page"]').getBoundingClientRect(); return t.left >= s.left - 0.5 && t.right <= s.right + 0.5 }), `current change tab ${label} wholly in view at ${width}, ${size}`)
      }
      assert.ok(await page.locator('.console-head .agent-activity .chip').evaluate(el => el.scrollWidth <= el.clientWidth + 1), `chat activity label stays complete at ${width}, ${size}`)
      assert.ok(await page.locator('.console-head').evaluate(head => [...head.children].every(el => { const r = el.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth })), `chat header fits at ${width}, ${size}`)
      assert.ok(await page.locator('.ch-decision-choices').evaluate(element => { const widths = [...element.children].map(child => child.getBoundingClientRect().width); return Math.max(...widths) - Math.min(...widths) < 1 }), `equal decision choices at ${width}, ${size}`)
      assert.deepEqual(await page.evaluate(wrappedRowActions), [], `one-line action labels at ${width}, ${size}`)
      assert.ok(await page.locator('.int-list-row button').isVisible(), 'integration action remains visible')
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `page fits at ${width}, ${size}`)
      for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        const at = `${width}, ${size}, ${theme}`
        // A disabled button is greyed by its colours, and its words stay readable.
        const disabled = await page.locator('.disabled-fixture').evaluate(el => { const s = getComputedStyle(el); return { opacity: s.opacity, color: s.color, background: s.backgroundColor } })
        assert.equal(disabled.opacity, '1', `disabled button is not faded at ${at}`)
        assert.ok(contrast(disabled.color, disabled.background) >= 4.5, `disabled button words readable at ${at}: ${JSON.stringify(disabled)}`)
        // A quiet destructive act is red, and on a phone it has an edge to show it is a button.
        const quiet = await page.locator('.quiet-danger-fixture').evaluate(el => { const s = getComputedStyle(el); return { color: s.color, border: s.borderTopColor, bad: getComputedStyle(document.documentElement).getPropertyValue('--bad').trim() } })
        assert.equal(quiet.color, hexToRgb(quiet.bad), `quiet danger button is red at ${at}`)
        if (width <= 800) assert.notEqual(quiet.border, 'rgba(0, 0, 0, 0)', `quiet button has an edge on phones at ${at}`)
        // A link in a sentence is underlined; the words around it are not links.
        assert.ok((await page.locator('.link-fixture a').evaluate(el => getComputedStyle(el).textDecorationLine)).includes('underline'), `link in a sentence is underlined at ${at}`)
      }
      await page.evaluate(() => { delete document.documentElement.dataset.theme })
      if (width <= 800) {
        // Code on its own page keeps every line whole and scrolls inside its box.
        for (const box of ['.guide-fixture pre', '.source-fixture']) {
          const code = await page.locator(box).evaluate(el => ({ wrap: getComputedStyle(el).whiteSpace, scrolls: el.scrollWidth > el.clientWidth, overflow: getComputedStyle(el).overflowX, cue: getComputedStyle(el).backgroundImage.includes('radial-gradient') }))
          assert.ok(['pre', 'nowrap'].includes(code.wrap) && code.scrolls && code.overflow === 'auto' && code.cue, `${box} scrolls sideways unwrapped at ${width}, ${size}: ${JSON.stringify(code)}`)
        }
        // A short flag in a sentence never breaks after its hyphen.
        assert.equal(await page.locator('.guide-fixture p code').filter({ hasText: '--worker-url' }).evaluate(el => el.getClientRects().length), 1, `inline flag on one line at ${width}, ${size}`)
        // A choice among kinds shows it can be tapped: an edge and a chevron.
        const kind = await page.locator('.wiz-fixture').evaluate(el => ({ chevron: getComputedStyle(el, '::after').content, border: getComputedStyle(el).borderTopWidth }))
        assert.ok(kind.chevron.includes('›') && kind.border !== '0px', `target kind looks like a choice at ${width}, ${size}: ${JSON.stringify(kind)}`)
      }
      // A status shows its word; its explanation is its details, opened on hover or tap.
      assert.ok(await page.locator('[data-help="No runtime reading."]').isVisible(), `status word with its explanation as details at ${width}, ${size}`)
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
        // A tap outside closes it: on a point the person can see, outside the
        // panel. Clicking main's own corner scrolled the page to reach it
        // while the open panel held it at its trigger, and on a busy machine
        // the scroll never settled (three landing gates timed out here).
        const box = await panel.boundingBox()
        const viewport = page.viewportSize()
        const outsideY = box && box.y < viewport.height / 2 ? viewport.height - 4 : 4
        await page.mouse.click(4, outsideY)
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
    await page.locator('.topbar a[href^="/attention"]').last().waitFor()
    // A phone bar is one 44px line: the menu, a back link to the parent, one status dot and Find.
    assert.ok(await page.locator('.topbar').evaluate(bar => bar.getBoundingClientRect().height <= 44.5), `phone bar stays one line at ${size}: ${await page.locator('.topbar').evaluate(bar => bar.getBoundingClientRect().height)}`)
    assert.deepEqual(await page.locator('nav[aria-label="Breadcrumb"] a').evaluateAll(links => links.map(a => [a.getAttribute('href'), a.textContent])), [['/projects/club-newsletter/memory', '‹Memory']], `phone trail is one back link at ${size}`)
    assert.ok(await page.locator('nav[aria-label="Breadcrumb"] a').evaluate(a => a.scrollWidth <= a.clientWidth + 1), `back link is whole at ${size}`)
    assert.equal(await page.locator('.topbar a[href^="/attention"]').count(), 1, `one status dot at ${size}`)
    assert.ok(await page.locator('.topbar a[href^="/attention"]').evaluate(a => { const r = a.getBoundingClientRect(); return r.width >= 44 && r.height >= 44 }), `status dot is a full-size tap target at ${size}`)
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
  for (const width of [390, 800, 1440]) {
    for (const size of ['small', 'medium', 'large']) {
      await page.setViewportSize({ width, height: 844 })
      await page.goto('http://layout.test/')
      await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
      await page.addScriptTag({ content: conformanceProjectFixture.outputFiles[0].text })
      await page.getByRole('button', { name: 'Re-check', exact: true }).last().waitFor()
      assert.equal(await page.locator('.chip.unknown').count(), 3)
      assert.ok(await page.locator('.chip.unknown').evaluateAll(chips => chips.every(chip => {
        const box = chip.getBoundingClientRect()
        return box.left >= 0 && box.right <= innerWidth && chip.scrollWidth <= chip.clientWidth + 2
      })), `current check chips stay whole at ${width}, ${size}`)
      assert.ok(await page.locator('.project-page-check-row-div').evaluateAll(details => details.every(detail =>
        detail.scrollWidth <= detail.clientWidth + 1
      )), `current check evidence wraps at ${width}, ${size}`)
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `current checks fit at ${width}, ${size}`)
      if (out) for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
        await page.screenshot({ path: `${out}/conformance-project-${width}-${size}-${theme}.png`, fullPage: true })
      }
    }
  }
  for (const size of ['medium', 'large']) {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('http://layout.test/')
    await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
    await page.addScriptTag({ content: conformanceFixture.outputFiles[0].text })
    await page.waitForFunction(() => !!window.finishConformance)
    // Conformance's pages are in the sidebar; the page must still fit a
    // phone once its counts load.
    await page.evaluate(() => window.finishConformance())
    await page.locator('.page-head h1').first().waitFor()
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
      assert.equal(await page.locator('.wiz-steps li').evaluateAll(steps => new Set(steps.map(step => Math.round(step.getBoundingClientRect().top))).size), 1, `target steps stay on one row, in order, at ${width}, ${size}`)
      if (out) await page.screenshot({ path: `${out}/target-connection-${width}-${size}.png`, fullPage: true })
    }
  }
  for (const width of [390, 1440]) {
    for (const size of ['small', 'medium', 'large']) {
      await page.setViewportSize({ width, height: 844 })
      await page.goto('http://layout.test/')
      await page.evaluate(size => { document.documentElement.dataset.size = size }, size)
      await page.addScriptTag({ content: phoneListsFixture.outputFiles[0].text })
      await page.locator('#parts .container-gauge').last().waitFor()
      // Each record's cells: none drawn over another, the name never
      // squeezed, and on a phone each value under its column's label.
      const records = await page.locator('#parts tbody tr, #technical-parts tbody tr').evaluateAll(rows => rows.map(row => {
        const cells = [...row.children].filter(cell => getComputedStyle(cell).display !== 'none' && cell.getBoundingClientRect().height > 0)
        const boxes = cells.map(cell => cell.getBoundingClientRect())
        const overlaps = boxes.some((a, i) => boxes.some((b, j) => j > i && a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1))
        const name = row.querySelector('.phone-name')
        const words = name.textContent.trim().split(/\s+/).length
        const range = document.createRange()
        range.selectNodeContents(name)
        const lines = new Set([...range.getClientRects()].filter(r => r.width > 0).map(r => Math.round(r.bottom))).size
        const labels = cells.map(cell => getComputedStyle(cell, '::before').content).filter(c => c && c !== 'none' && c !== 'normal').map(c => JSON.parse(c))
        const overflows = cells.some(cell => cell.scrollWidth > cell.clientWidth + 1)
        return { overlaps, overflows, nameShare: name.getBoundingClientRect().width / row.getBoundingClientRect().width, words, lines, labels, oneLine: new Set(boxes.map(b => Math.round(b.top))).size === 1 }
      }))
      assert.equal(records.length, 4)
      for (const r of records) {
        assert.ok(!r.overlaps, `no cell is drawn over another at ${width}, ${size}`)
        if (width === 390) assert.ok(!r.overflows, `every value wraps within its line at 390, ${size}`)
        if (width === 390) {
          assert.ok(r.nameShare >= 0.6, `a record's name keeps at least 60% of the row at 390, ${size}: ${r.nameShare}`)
          assert.ok(r.lines <= r.words, `a record's name breaks only between words at 390, ${size}: ${r.lines} lines for ${r.words} words`)
        } else assert.deepEqual(r.labels, [], 'desktop rows show column headers, not labels')
      }
      if (width === 390) {
        assert.deepEqual(records[0].labels.filter(l => ['CPU', 'Memory'].includes(l)), ['CPU', 'Memory'], 'each part says which figure is CPU and which is memory')
        assert.ok(await page.locator('#activity .list-row').evaluate(row => row.querySelector('.list-state').getBoundingClientRect().top >= row.querySelector('.list-identity').getBoundingClientRect().bottom - 1), `an activity row's status sits under its title at 390, ${size}`)
        assert.ok(await page.locator('#activity strong').evaluate(textLines) <= 2, `an activity title is not squeezed at 390, ${size}`)
        assert.equal(await page.locator('#title h1').evaluate(textLines), 1, `a title keeps its words on one line at 390, ${size}`)
        for (const th of await page.locator('#row-headers th').all()) assert.equal(await th.evaluate(textLines), 1, `row headers keep whole words at 390, ${size}`)
      } else assert.ok(await page.locator('#activity .list-state').evaluate(state => state.getBoundingClientRect().left < state.parentElement.querySelector('.list-identity').getBoundingClientRect().left), 'desktop activity rows lead with their status')
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `phone list fixture fits at ${width}, ${size}`)
      if (out) await page.screenshot({ path: `${out}/phone-lists-${width}-${size}.png`, fullPage: true })
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
  // Overlays at phone width (390) against the old popover behaviour; desktop
  // keeps the popovers asserted above.
  const overlay = async (scene, size, width = 390) => {
    await page.setViewportSize({ width, height: 844 })
    await page.goto('http://layout.test/')
    await page.evaluate(([scene, size]) => { window.overlayCase = scene; document.documentElement.dataset.size = size }, [scene, size])
    await page.addScriptTag({ content: overlayFixture.outputFiles[0].text })
  }
  const inView = box => box.x >= 0 && box.y >= 0 && box.x + box.width <= 390 + 0.5 && box.y + box.height <= 844 + 0.5
  for (const size of ['small', 'medium', 'large']) {
    await overlay('page', size)
    await page.getByRole('button', { name: 'About Things', exact: true }).click()
    const help = page.getByRole('dialog')
    await help.waitFor()
    const helpBox = await help.locator('.modal-panel').or(help).first().boundingBox()
    assert.ok(inView(helpBox) && helpBox.height < 844 * 0.8, `help is a sized dialog on screen at 390, ${size}: ${JSON.stringify(helpBox)}`)
    assert.ok(await help.getByRole('button', { name: 'close' }).isVisible(), `help dialog has a close button at ${size}`)
    await page.keyboard.press('Escape')
    await help.waitFor({ state: 'detached' })
    assert.ok(await page.getByRole('button', { name: 'About Things', exact: true }).evaluate(el => el === document.activeElement), `focus returns to the ? at ${size}`)
    await page.getByRole('button', { name: 'About Things', exact: true }).click()
    await page.getByRole('dialog').getByRole('link', { name: 'Read more' }).click()
    await page.waitForFunction(() => document.body.dataset.path === '/docs/guides/things', null, { timeout: 2000 }).catch(() => {})
    assert.equal(await page.evaluate(() => document.body.dataset.path), '/docs/guides/things', `Read more in the help dialog navigates at ${size}`)
    await overlay('page', size)
    await page.locator('.chip-button').first().click()
    const details = page.getByRole('dialog')
    await details.waitFor()
    assert.ok((await details.innerText()).includes('No runtime reading.'), `status details open as a dialog at 390, ${size}`)
    assert.ok(inView(await details.locator('.modal-panel').or(details).first().boundingBox()), `status dialog on screen at ${size}`)
    await page.keyboard.press('Escape')
    const row = page.locator('.pack-row')
    const [identity, action, rowBox] = await Promise.all([row.locator('.list-identity').boundingBox(), row.locator('.list-action').boundingBox(), row.boundingBox()])
    assert.ok(Math.abs(identity.y - action.y) <= 2 && Math.abs(action.x + action.width - (rowBox.x + rowBox.width)) <= 6, `library row menu sits top right at ${size}: ${JSON.stringify({ identity, action, rowBox })}`)
    const more = page.locator('.more-menu-button')
    const moreLook = await more.evaluate(el => { const r = el.getBoundingClientRect(), style = getComputedStyle(el); return { width: r.width, height: r.height, border: style.borderTopColor } })
    assert.ok(moreLook.width >= 44 && moreLook.height >= 44 && moreLook.border !== 'rgba(0, 0, 0, 0)', `⋯ is a 44px button with a border at ${size}: ${JSON.stringify(moreLook)}`)
    await more.click()
    const item = page.locator('.popover .more-menu-list > button').first()
    assert.equal(await item.evaluate(el => getComputedStyle(el).color), await page.evaluate(() => { const probe = document.createElement('span'); probe.style.color = 'var(--text)'; document.body.append(probe); const c = getComputedStyle(probe).color; probe.remove(); return c }), `menu items read in the text colour at ${size}`)
    await page.evaluate(() => window.scrollTo(0, 2500))
    await page.locator('.popover').waitFor({ state: 'detached', timeout: 2000 })
    await overlay('modal-long', size)
    for (const name of ['Restore', 'Cancel']) assert.ok(inView(await page.getByRole('dialog').getByRole('button', { name, exact: true }).boundingBox()), `${name} is on screen in a long dialog at ${size}`)
    for (const scene of ['modal-long', 'modal-search', 'modal-tabs']) {
      await overlay(scene, size)
      const dialog = page.getByRole('dialog')
      const close = await dialog.getByRole('button', { name: 'close' }).boundingBox()
      const title = await dialog.locator('h2').first().boundingBox()
      assert.ok(inView(close) && close.width >= 44 && close.height >= 44 && Math.abs(close.y + close.height / 2 - (title.y + title.height / 2)) <= 14, `${scene}: ✕ beside the title at ${size}: ${JSON.stringify({ close, title })}`)
      for (const control of await dialog.locator('.modal-head input, .modal-head .tab').all()) assert.ok(inView(await control.boundingBox()), `${scene}: header controls on screen at ${size}`)
      // The title keeps a readable width rather than being squeezed by the header controls.
      const titleBox = await dialog.locator('.modal-title').boundingBox()
      assert.ok(titleBox.width >= 120, `${scene}: the title keeps its width beside the ✕ at ${size}: ${JSON.stringify(titleBox)}`)
    }
    await overlay('decide', size)
    const bar = page.locator('.ch-turn-floater')
    await bar.waitFor()
    const barLook = await bar.evaluate(el => { const r = el.getBoundingClientRect(); return { left: r.left, width: r.width, background: getComputedStyle(el).backgroundColor } })
    assert.ok(barLook.left <= 0.5 && barLook.width >= 389.5 && barLook.background !== 'rgba(0, 0, 0, 0)', `Decide is an opaque full-width bar at ${size}: ${JSON.stringify(barLook)}`)
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
    const [last, barBox] = await Promise.all([page.locator('#last-line').boundingBox(), bar.boundingBox()])
    assert.ok(last.y + last.height <= barBox.y + 0.5, `the last line scrolls clear of the Decide bar at ${size}: ${JSON.stringify({ last, barBox })}`)
    if (out) await page.screenshot({ path: `${out}/overlay-decide-390-${size}.png` })
  }
  // Wide screens keep the help popover.
  await overlay('page', 'small', 1440)
  await page.getByRole('button', { name: 'About Things', exact: true }).click()
  assert.ok(await page.locator('.page-help-pop').isVisible() && !(await page.getByRole('dialog').count()), 'desktop keeps the help popover')
  await overlay('page-head-actions', 'medium')
  const [headingRow, headingAction] = await Promise.all([page.locator('.page-head-row').boundingBox(), page.getByRole('button', {name:'Group actions'}).boundingBox()])
  assert.ok(Math.abs(headingAction.x + headingAction.width - (headingRow.x + headingRow.width)) <= 1, `a wrapped page action stays on the right: ${JSON.stringify({headingRow, headingAction})}`)
  // Phone round 2. Every check is collected, so one run shows each that fails.
  const misses = []
  const want = (ok, message) => { if (!ok) misses.push(message) }
  const round = async (scene, size, width = 390, theme = 'light') => {
    await page.setViewportSize({ width, height: 844 })
    await page.goto('http://layout.test/')
    await page.evaluate(([scene, size, theme]) => { window.roundCase = scene; document.documentElement.dataset.size = size; document.documentElement.dataset.theme = theme }, [scene, size, theme])
    await page.addScriptTag({ content: roundFixture.outputFiles[0].text })
  }
  // The distinct lines a word's letters sit on (not its rects: inline markup
  // can give several rects on one line).
  const wordLines = (word, within = 'main') => page.evaluate(([word, within]) => {
    const root = document.querySelector(within)
    const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
    for (let node; (node = walk.nextNode());) {
      const at = node.data.search(new RegExp('(^|[^A-Za-z])' + word + '($|[^A-Za-z])'))
      if (at < 0) continue
      const start = node.data.indexOf(word, at)
      const range = document.createRange(); range.setStart(node, start); range.setEnd(node, start + word.length)
      return new Set([...range.getClientRects()].filter(r => r.width > 0).map(r => Math.round(r.top))).size
    }
    return -1
  }, [word, within])
  const underlined = sel => page.locator(sel).first().evaluate(el => getComputedStyle(el).textDecorationLine.includes('underline'))
  for (const [size, width] of [['medium', 390], ['large', 390], ['small', 1440]]) {
    const where = `${width}, ${size}`
    // Deployment rows keep their shape when their late reads arrive.
    await round('fleet', size, width)
    await page.getByText('Production', { exact: true }).waitFor()
    const shape = () => page.locator('.fleet-table tbody tr').evaluateAll(rows => rows.map(tr => {
      const top = tr.getBoundingClientRect().top
      const textTop = el => { const r = document.createRange(); r.selectNodeContents(el); return Math.round((r.getClientRects()[0]?.top ?? 0) - top) }
      return { height: Math.round(tr.getBoundingClientRect().height), name: textTop(tr.querySelector('td.phone-name strong')), value: textTop(tr.querySelector('td[data-label="Deployment"] a')) }
    }))
    const before = await shape()
    await page.evaluate(() => window.release())
    await page.waitForFunction(() => document.querySelectorAll('.fleet-table .more-menu-button').length === 3 && document.body.innerText.includes('8 Nov'))
    const after = await shape()
    want(before.every((b, i) => Math.abs(b.height - after[i].height) <= 1 && Math.abs(b.name - after[i].name) <= 1 && Math.abs(b.value - after[i].value) <= 1), `deployment rows keep their shape when late reads arrive at ${where}: ${JSON.stringify({ before, after })}`)
    if (width === 390) {
      want(await wordLines('Production') === 1, `"Production" is whole at ${where}`)
      want(await page.locator('.fleet-table td[data-label="Deployment"]').first().evaluate(td => { const b = getComputedStyle(td, '::before'); return parseFloat(b.height) < 2 * parseFloat(b.fontSize) }), `the "Deployment" label is one line at ${where}`)
    }
    if (out) await page.screenshot({ path: `${out}/round-fleet-${width}-${size}.png`, fullPage: true })
    if (width !== 390) continue
    // The compact stage strip keeps its words whole.
    await round('stages', size)
    want(await wordLines('Start') === 1 && await wordLines('stopped') === 1, `"Start" and "stopped" are whole at ${where}`)
    want(await page.locator('.deploy-stages-compact li').evaluateAll(items => items.every(li => [...li.children].every(c => c.getBoundingClientRect().right <= li.getBoundingClientRect().right + 0.5))), `each stage's words stay in its own column at ${where}`)
    if (out) await page.screenshot({ path: `${out}/round-stages-${size}.png` })
    // A choice's words stay beside its button.
    await round('choices', size)
    await page.getByRole('dialog').waitFor()
    want(await page.locator('label.round-choice').evaluateAll(labels => labels.every(l => {
      const radio = l.querySelector('input').getBoundingClientRect()
      const r = document.createRange(); r.selectNodeContents(l.querySelector('span'))
      return r.getClientRects()[0].top < radio.bottom
    })), `every choice's text starts beside its radio at ${where}`)
    // A table in a group inside a section stays inside the group.
    await round('nested', size)
    if (out) await page.screenshot({ path: `${out}/round-nested-${size}.png` })
    want(await page.locator('.section-body').evaluate(b => getComputedStyle(b.querySelector('table')).backgroundImage.includes(getComputedStyle(b).backgroundColor)), `a table's scroll cue is drawn in its section's colour at ${where}`)
    want(await page.locator('.agent-group').evaluate(g => { const box = g.getBoundingClientRect(), t = g.querySelector('table').getBoundingClientRect(); return t.left >= box.left - 0.5 && t.right <= box.right + 0.5 }), `a nested table stays inside its group at ${where}`)
    await round('direct', size)
    if (out) await page.screenshot({ path: `${out}/round-direct-${size}.png` })
    want(await page.locator('.section-body').evaluate(b => { const box = b.getBoundingClientRect(), t = b.querySelector('table').getBoundingClientRect(); return t.left <= box.left + 1.5 && t.right >= box.right - 1.5 }), `a section's own table still reaches the panel's edges at ${where}`)
    // The help "?" is a 32px circle with a 44px target, and its focus ring clears the title.
    await round('help', size)
    const help = page.getByRole('button', { name: 'About Overview', exact: true })
    await help.focus()
    const look = await help.evaluate(el => {
      const r = el.getBoundingClientRect(), s = getComputedStyle(el), h1 = document.querySelector('h1').getBoundingClientRect()
      const grow = s.outlineStyle === 'none' ? 0 : parseFloat(s.outlineWidth) + parseFloat(s.outlineOffset)
      const cx = r.left + r.width / 2, cy = r.top + r.height / 2
      return { width: r.width, circle: (b => parseFloat(b.width) + parseFloat(b.borderLeftWidth) + parseFloat(b.borderRightWidth))(getComputedStyle(el, '::before')) || 0, clear: r.left - grow >= h1.right, target: [[cx - 21, cy], [cx + 21, cy], [cx, cy - 21], [cx, cy + 21]].every(([x, y]) => el.contains(document.elementFromPoint(x, y))) }
    })
    want(look.width >= 44 && look.circle >= 32 && look.clear && look.target, `the ? is a 32px circle, a 44px target, and its ring clears the title at ${where}: ${JSON.stringify(look)}`)
    // An overview row's title has the row's width; the release goes under it.
    await round('overview', size)
    want(await page.locator('.application-overview-card').evaluate(row => row.querySelector('.application-overview-link').getBoundingClientRect().width >= 0.9 * row.getBoundingClientRect().width), `an overview title has the row's width at ${where}`)
    // One release per line.
    await round('releases', size)
    want(await page.locator('.release-strip li').evaluateAll(items => new Set(items.map(li => Math.round(li.getBoundingClientRect().top))).size === items.length && items.length === 5), `one release per line at ${where}`)
    // Capacity headers stay whole (the table scrolls); bar labels are not cut.
    await round('capacity', size)
    want(await wordLines('containers', '.cap-table thead') === 1, `"containers" is one line at ${where}`)
    if (out) await page.screenshot({ path: `${out}/round-capacity-${size}.png` })
    want(await page.locator('.cap-in').evaluate(el => el.scrollHeight <= el.clientHeight && el.getBoundingClientRect().bottom <= el.closest('.cap-bar').getBoundingClientRect().bottom + 0.5), `a bar label is not cut at ${where}`)
  }
  // Wide screens: an overview row still leads with its state; releases run on.
  await round('overview', 'small', 1440)
  want(await page.locator('.application-overview-card').evaluate(row => row.querySelector('.list-state').getBoundingClientRect().left < row.querySelector('.list-identity').getBoundingClientRect().left), 'at 1440 the overview row leads with its state')
  await round('releases', 'small', 1440)
  want(await page.locator('.release-strip li').evaluateAll(items => items.length >= 2 && Math.round(items[0].getBoundingClientRect().top) === Math.round(items[1].getBoundingClientRect().top)), 'at 1440 releases run on one line')
  // Links are underlined unless they are the item, at both widths.
  for (const width of [390, 1440]) {
    await round('links', 'medium', width)
    for (const sel of ['#trailer', '#add-secret', '#value-link']) want(await underlined(sel), `${sel} is underlined at ${width}`)
    for (const sel of ['#table-name', '#project-name', '#mark-link', '.pack-name', '#tab', '#btn-link']) want(!(await underlined(sel)), `${sel} is not underlined at ${width}`)
  }
  // The camera's word-split detector finds a word broken inside itself and
  // leaves alone one too wide for its line, and code.
  await round('direct', 'medium')
  await page.evaluate(() => {
    const probe = document.createElement('div')
    probe.innerHTML = `<p style="width:60px;overflow-wrap:anywhere">Production</p><p style="width:30px;overflow-wrap:anywhere">${'W'.repeat(60)}</p><p style="width:200px;overflow-wrap:anywhere">Production staging</p><p style="width:40px;overflow-wrap:anywhere"><code>Production</code></p>`
    document.querySelector('main').append(probe)
  })
  const split = await page.evaluate(splitWords)
  want(split.length === 1 && split[0].word === 'Production', `the word-split detector finds exactly the split word: ${JSON.stringify(split)}`)
  assert.deepEqual(misses, [], 'phone round 2')
  // Phone round 3. Collected like round 2.
  const lines = sel => page.locator(sel).evaluateAll(els => els.map(el => { const r = document.createRange(); r.selectNodeContents(el); return new Set([...r.getClientRects()].filter(x => x.width > 0).map(x => Math.round(x.top))).size }))
  const placeholderFits = sel => page.locator(sel).evaluate(input => {
    const s = getComputedStyle(input), probe = document.createElement('span')
    probe.textContent = input.placeholder; probe.style.font = s.font; probe.style.position = 'absolute'; probe.style.whiteSpace = 'pre'
    document.body.append(probe); const w = probe.getBoundingClientRect().width; probe.remove()
    return { text: w, room: input.clientWidth - parseFloat(s.paddingLeft) - parseFloat(s.paddingRight) }
  })
  for (const size of ['medium', 'large']) {
    const where = `390, ${size}`
    // A short name moves whole; a long one breaks inside and stays on screen.
    await round('fleet', size)
    await page.getByText('Production', { exact: true }).waitFor()
    const idents = await lines('.fleet-table .ident')
    want(idents.length === 3 && idents.every(n => n === 1), `each target name is one line at ${where}: ${JSON.stringify(idents)}`)
    await round('heading', size)
    const long = await page.locator('#long-name .ident').evaluate(el => ({ lines: (r => (r.selectNodeContents(el), new Set([...r.getClientRects()].map(x => Math.round(x.top))).size))(document.createRange()), inside: el.scrollWidth <= el.clientWidth + 0.5, page: document.documentElement.scrollWidth <= innerWidth }))
    want(long.lines > 1 && long.inside && long.page, `a long name wraps inside the line at ${where}: ${JSON.stringify(long)}`)
    want((await lines('#short-name .ident'))[0] === 1, `a short name in a heading moves whole at ${where}`)
    // Fields grow with the text: the placeholder is not cut.
    await round('newTarget', size)
    await page.locator('.wiz-kind').first().click()
    for (const name of ['SSH key secret', 'address', 'name']) {
      const fit = await placeholderFits(`.wiz-fields label:has-text("${name}") input`)
      want(fit.text <= fit.room + 1, `the ${name} placeholder fits at ${where}: ${JSON.stringify(fit)}`)
    }
    await round('networks', size)
    const net = await placeholderFits('input[aria-label="Network"]')
    want(net.text <= net.room + 1, `the network placeholder fits at ${where}: ${JSON.stringify(net)}`)
    want(await page.locator('input[aria-label="Network"]').evaluate(i => i.getBoundingClientRect().right <= i.closest('.people-agents-card-div').getBoundingClientRect().right + 0.5 && document.documentElement.scrollWidth <= innerWidth), `the network field stays inside its card at ${where}`)
    // A select in a stacked table is one line and reaches the row's edge.
    await round('slots', size)
    const slot = await page.locator('.slots-table tbody tr').evaluate(tr => { const sel = tr.querySelector('select'), row = tr.getBoundingClientRect(), s = getComputedStyle(tr); return { gap: row.right - parseFloat(s.paddingRight) - sel.getBoundingClientRect().right, wraps: getComputedStyle(sel).whiteSpace } })
    want(slot.gap <= 2 && slot.wraps !== 'normal', `a slot's select is one line and reaches the row edge at ${where}: ${JSON.stringify(slot)}`)
    want(await page.locator('.section-body').evaluate(b => { const box = b.getBoundingClientRect(), t = b.querySelector('table').getBoundingClientRect(); return t.left <= box.left + 1.5 && t.right >= box.right - 1.5 }), `the slots table reaches the panel's edges at ${where}`)
    // A switch's words stay beside it.
    await round('switch', size)
    want(await page.locator('#switch-label').evaluate(l => { const sw = l.querySelector('.setting-switch').getBoundingClientRect(); return [...l.childNodes].filter(n => n.nodeType === 3 && n.data.trim()).every(n => { const r = document.createRange(); r.selectNodeContents(n); return [...r.getClientRects()].filter(x => x.width > 0).every(x => x.left >= sw.right - 0.5) }) }), `a switch's label lines start right of it at ${where}`)
    // A menu fits its words, not the screen.
    await round('menu', size)
    await page.getByRole('button', { name: 'Workflow' }).click()
    want(await page.locator('.popover').evaluate(p => p.getBoundingClientRect().width <= 20 * parseFloat(getComputedStyle(document.documentElement).fontSize) + 20), `a menu is its 20rem list and padding at ${where}`)
    // A section row keeps its inset.
    await round('inset', size)
    want(await page.locator('.section-body').evaluate(b => document.getElementById('inset-identity').getBoundingClientRect().left - b.getBoundingClientRect().left >= 12), `a section row's text keeps its inset at ${where}`)
    // The deployment menu sits beside the title.
    await round('head', size)
    want(await page.evaluate(() => document.querySelector('.deployment-menu button').getBoundingClientRect().top < document.querySelector('.deployment-page-heading h1').getBoundingClientRect().bottom), `the ⋯ is on the title's line at ${where}`)
    // Edit display name has room after the ID, or a line of its own.
    await round('label', size)
    const gaps = await page.evaluate(() => { const code = document.querySelector('.display-label-edit code').getBoundingClientRect(), link = document.querySelector('.display-label-edit button').getBoundingClientRect(), next = document.getElementById('next-fact').getBoundingClientRect(); return { after: link.left - code.right, under: link.top - code.bottom, below: next.top - link.bottom } })
    // Beside the ID with a gap, or (where the text is wider) on its own line under it.
    want(gaps.after >= 8 || gaps.under >= 0, `Edit display name sits apart from the ID at ${where}: ${JSON.stringify(gaps)}`)
    // Edit and Delete stay together.
    await round('kinds', size)
    await page.getByText('Club sign-in', { exact: true }).waitFor()
    want(await page.locator('.card').first().evaluate(c => { const [e, d] = [...c.querySelectorAll('button')].filter(b => /^(Edit|Delete)$/.test(b.textContent.trim())).map(b => Math.round(b.getBoundingClientRect().top)); return e === d }), `Edit and Delete share a line at ${where}`)
    // The Checks title and its caption do not run together.
    await round('checks', size)
    want(await page.locator('.wow-list-row').evaluate(row => { const t = row.querySelector('strong').getBoundingClientRect(), c = row.querySelector('.muted.small').getBoundingClientRect(); return c.left >= t.right + 4 || c.top >= t.bottom - 1 }), `the Checks caption stands apart from its title at ${where}`)
    // A select leaves room for its arrow.
    await round('select', size)
    want(parseFloat(await page.locator('#rule-select').evaluate(s => getComputedStyle(s).paddingInlineEnd)) >= 24, `a select keeps room for its arrow at ${where}`)
    want(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `a long option does not widen the page at ${where}`)
  }
  // The menu cap holds on a wide screen too.
  await round('menu', 'small', 1440)
  await page.getByRole('button', { name: 'Workflow' }).click()
  want(await page.locator('.popover').evaluate(p => p.getBoundingClientRect().width <= 20 * parseFloat(getComputedStyle(document.documentElement).fontSize) + 20), 'a menu is its 20rem list and padding at 1440')
  // Row names: one weight whether a link or bold text; bold text in prose is the strong token.
  for (const theme of ['dark', 'light']) {
    await round('names', 'medium', 390, theme)
    const w = await page.evaluate(() => ['feature-name', 'account-name', 'para-strong', 'name-note'].map(id => getComputedStyle(document.getElementById(id)).fontWeight))
    want(w[0] === w[1] && w[2] === '600' && w[3] === '400', `row names share a weight, a line under a name keeps the body weight and strong is 600 in ${theme}: ${JSON.stringify(w)}`)
  }
  // The off switch is light against its panel; on is coloured.
  for (const theme of ['light', 'dark']) {
    await round('switch', 'medium', 390, theme)
    const off = await page.locator('.setting-switch').evaluate(el => [getComputedStyle(el).backgroundColor, getComputedStyle(el.closest('.section-body') || document.body).backgroundColor])
    want(contrast(off[0], off[1]) < 1.5, `the off switch is light against its panel in ${theme}: ${JSON.stringify(off)}`)
  }
  // The word-split detector counts a split name, and not one that moved whole.
  await round('direct', 'medium')
  await page.evaluate(() => { const p = document.createElement('p'); p.style.width = '140px'; p.innerHTML = 'Running on <span class="ident" style="display:inline">built-in-docker</span> <span class="ident">lunch-orders</span>'; document.querySelector('main').append(p) })
  const names = await page.evaluate(splitWords)
  want(names.length === 1 && names[0].word === 'built-in-docker', `the detector finds exactly the split name: ${JSON.stringify(names)}`)
  // The drawer has one spacer: the one inside SidebarNav, before its foot.
  want(!/<SidebarNav \/>\s*<div className="spacer" \/>/.test(app), 'the drawer has no second spacer after SidebarNav')
  assert.deepEqual(misses, [], 'phone round 3')
  console.log('layout: label fixtures, rows, disclosures and phone composer passed')
} catch (error) {
  // A step that timed out says whether the page itself was stuck (a script
  // cannot run in it) or only slow, with its console and a screenshot.
  await report()
  throw error
} finally { clearTimeout(watchdog); await browser.close() }
