// Production deployment header, with deterministic provider states. Screenshots
// are component fixtures; provider effects are covered by the Go adapter tests.
import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import { build } from 'esbuild'
import { productionCSS } from './production-css.mjs'
import { fontsReady } from './fonts-ready.mjs'
import { chromium } from 'playwright'
const out = process.env.APPTOWN_RUNTIME_OUT
const css = await productionCSS()
const fixture = await build({
  absWorkingDir: new URL('.', import.meta.url).pathname, bundle: true, write: false, format: 'iife', jsx: 'automatic', loader: { '.css': 'empty' },
  stdin: { loader: 'tsx', resolveDir: new URL('./src/', import.meta.url).pathname, contents: `
    import { createRoot } from 'react-dom/client'
    import { MemoryRouter } from 'react-router-dom'
    import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
    import { getKey } from './data'
    import { DeploymentDeploys } from './pages/project/DeploymentDeploys'
    import { ProjectListRow } from './ProjectListRow'
    import { DeploymentRow } from './pages/project/settings/Deployments'
    import { FleetDeploymentStatus } from './deploymentStatus'
    const root = createRoot(document.getElementById('root'))
    const baseRelease = {number: 12, deployment: 'main', state: 'released', releasedAt: new Date().toISOString(), createdAt: new Date().toISOString(), changes: []}
    window.drawRuntime = (state) => {
      const retryScene = ['failed-release', 'interrupted', 'failed-picker'].includes(state)
      const release = { ...baseRelease, id: 'release-8', number: retryScene ? 8 : 12, bundleId: 'bundle-8', state: state === 'interrupted' ? 'releasing' : retryScene ? 'failed' : 'released', releasedAt: retryScene ? undefined : baseRelease.releasedAt, lastStage: state === 'interrupted' ? 'starting' : undefined, failedStage: state !== 'interrupted' && retryScene ? 'starting' : undefined, progressUncertain: state === 'interrupted', interruptedByRestart: state === 'interrupted', failure: state !== 'interrupted' && retryScene ? 'The provider could not start the app.' : undefined }
      window.runtimeRelease = release
      window.runtimeBundles = retryScene ? [{ id: 'bundle-8', contractRevision: '80cd31e29817', createdAt: baseRelease.createdAt }] : []

      const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, refetchOnWindowFocus: false } } })
      client.setQueryData(getKey('/projects/lunch/releases'), [release])
      client.setQueryData(getKey('/projects/lunch/release-bundles'), window.runtimeBundles)
      client.setQueryData(getKey('/projects/lunch/deployments/main/diagnostics'), { parts: [], containers: true, reading: { unknown: false, stale: false } })
      const reason = state === 'bucket' ? 'Bucket websites cannot be started, stopped or restarted' : 'App Platform cannot keep an app stopped'
      const unsupported = state === 'bucket' || state === 'app-platform'
      const env = { status: { word: ['starting', 'stopping', 'restarting'].includes(state) ? 'deploying' : state === 'stopped' ? 'stopped' : state === 'unknown-need' ? 'unknown' : 'working', reason: state === 'unknown-need' ? 'storage (s3) did not answer in time' : state === 'stopped' ? 'stopped by Priya' : ['starting', 'stopping', 'restarting'].includes(state) ? state : '', checkedAt: new Date().toISOString(), retryAt: state === 'unknown-need' ? new Date(Date.now()+15000).toISOString() : undefined, stale: state === 'unknown-need' }, id: 'main-id', name: 'main', displayLabel: 'Lunch Orders', kind: 'standing', generation: 2,
        runtimeState: ['bucket', 'app-platform', 'confirm', 'failed-release', 'interrupted', 'failed-picker'].includes(state) ? 'running' : state,
        observation: state === 'stopped' ? 'stopped' : 'running', runtimeActor: 'Priya', runtimeAt: new Date().toISOString(),
        runtimeControls: {start: unsupported ? reason : '', stop: unsupported ? reason : '', restart: state === 'bucket' ? reason : ''}, servingRelease: release }
      window.runtimeEnv = env
      client.setQueryData(getKey('/projects/lunch/deployments'), [env])
      root.render(<QueryClientProvider key={state} client={client}><MemoryRouter><main className="main">{state === 'unknown-need' && <>
        <h2>Projects</h2><ProjectListRow project={{...env, slug: 'lunch', name: 'Lunch Orders', hasDeployments: true, status: {...env.status, reason: 'main: storage (s3) did not answer in time'}}} />
        <h2>Project Deployments</h2><DeploymentRow slug="lunch" env={env} />
        <h2>Fleet Deployments</h2><div id="fleet-status"><FleetDeploymentStatus slug="lunch" id={env.id} /></div>
      </>}<DeploymentDeploys slug="lunch" name="main" displayLabel="Lunch Orders" env={env} current reload={async()=>{}} /></main></MemoryRouter></QueryClientProvider>)
    }
  ` },
})
const browser = await chromium.launch({ headless: true })
if (out) await mkdir(out, { recursive: true })
const results = []
try {
  for (const theme of ['light', 'dark']) for (const width of [1440, 390]) for (const state of ['running', 'stopped', 'starting', 'stopping', 'restarting', 'confirm', 'bucket', 'app-platform', 'unknown-need', 'failed-release', 'interrupted', 'failed-picker']) {
    const page = await browser.newPage({ viewport: { width, height: 900 } })
    await page.route('http://layout.test/**', route => route.fulfill({ contentType: 'text/html', body: `<html data-theme="${theme}"><meta name="viewport" content="width=device-width,initial-scale=1"><style>${css}</style><div id="root"></div></html>` }))
    await page.route('http://layout.test/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname
      const body = path.endsWith('/deployments') ? await page.evaluate(() => [window.runtimeEnv]) : path.endsWith('/diagnostics') ? { parts: [], containers: true, reading: { unknown: false, stale: false } }
        : path.endsWith('/releases') ? await page.evaluate(() => [window.runtimeRelease]) : path.endsWith('/release-bundles') ? await page.evaluate(() => window.runtimeBundles) : []
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
    })
    await page.goto('http://layout.test/')
    await page.addScriptTag({ content: fixture.outputFiles[0].text })
    await fontsReady(page)
    await page.evaluate(state => window.drawRuntime(state), state)
    await page.locator('.deployment-page-heading').waitFor()
    if (state === 'failed-picker') await page.getByRole('button', { name: 'Deploy a release…', exact: true }).click()
    if (state === 'interrupted') await page.getByText(/Interrupted by a restart — checking/).waitFor()
    if (state === 'failed-release') assert.equal(await page.getByRole('button', { name: 'Try again', exact: true }).count(), 1)
    if (state === 'failed-picker') await page.getByText('Release 8 failed: The provider could not start the app.', { exact: true }).waitFor()
    if (state === 'confirm') await page.getByRole('button', { name: '■ Stop', exact: true }).click()
    const header = page.locator('.deployment-page-heading')
    const text = await header.innerText()
    if (state === 'running') { assert.match(text, /■ Stop/); assert.match(text, /↻ Restart/); assert.match(text, /working/) }
    if (state === 'unknown-need') { await page.getByText('Waiting for the deployment status', { exact: true }).waitFor({ state: 'detached' }); assert.match(await page.locator('#fleet-status').innerText(), /storage \(s3\) did not answer in time/); assert.match(await page.locator('.project-state .chip').innerText(), /unknown$/); assert.match(await page.locator('.list-state .chip').first().innerText(), /unknown$/); assert.match(text, /unknown/); assert.match(text, /storage \(s3\) did not answer in time/); assert.match(text, /checked/); assert.match(text, /next check/) }
    if (state === 'stopped') { assert.match(text, /▶ Start/); assert.match(text, /stopped by Priya/); assert.match(await page.locator('main').innerText(), /stopped · will run release 12 when started/); assert.ok(!(await page.locator('main').innerText()).includes("It's live!")) }
    if (state === 'confirm') assert.match(text, /Stop Lunch Orders\? People can't use it until you start it again\./)
    if (['starting', 'stopping', 'restarting', 'bucket'].includes(state)) assert.equal(await header.locator('button:enabled').count(), 0)
    if (state === 'app-platform') assert.equal(await header.locator('button:enabled').count(), 1)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${state} overflow at ${width}`)
    const bounds = await page.locator('.deployment-deploy-card').boundingBox()
    const path = `${state}-${width}-${theme}.png`
    if (out && ['failed-release', 'interrupted', 'failed-picker'].includes(state)) await page.screenshot({ path: `${out}/${path}`, fullPage: true })
    else if (out) await page.screenshot({ path: `${out}/${path}`, clip: { x: 0, y: 0, width, height: Math.ceil(bounds.y) } })
    results.push({ state, width, theme, path, text })
    await page.close()
  }
} finally { await browser.close() }
if (out) await writeFile(`${out}/index.json`, JSON.stringify(results, null, 2))
console.log(`Deployment headers: ${results.length} states and widths passed`)
