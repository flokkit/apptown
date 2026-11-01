const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE)
import { mkdir, writeFile } from 'node:fs/promises'
const phase = process.argv[2] || 'after'
const base = process.env.STATUS_FIXTURE_URL
if (!base || new URL(base).hostname !== '127.0.0.1') throw new Error('STATUS_FIXTURE_URL must be a local fixture server')
const out = new URL('file:///Users/bls/wa/forge-isolated-orchestration/work/trustworthy-status/shots/')
await mkdir(out, { recursive:true })
const browser = await chromium.launch({headless:true})
const errors=[]
for (const width of [1440,390]) for (const theme of ['light','dark']) {
 const context = await browser.newContext({viewport:{width,height:width===390?844:1000},timezoneId:'Australia/Melbourne'})
 const page=await context.newPage()
 page.on('pageerror',e=>errors.push(e.message))
 for (const view of ['attention','capacity','resources','resources-invalidated','integrations','security']) {
  await page.goto(`${base}/test/status-fixture.html?view=${view}&theme=${theme}`)
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(500)
  if (phase === 'after') {
   const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)
   if (overflow) throw new Error(`${view} overflows at ${width}px in ${theme}`)
   if (await page.locator('time').innerText() !== '30 Oct, 17:26') throw new Error('event time is not in the reader zone')
   if (view === 'attention') {
    for (const group of ['Integrations','App checks']) await page.getByRole('region',{name:group,exact:true}).waitFor()
    if (await page.locator('.attention-badge').getAttribute('aria-label') !== '2 to fix') throw new Error('Attention scope count is wrong')
   }
   if (view === 'capacity') {
    await page.getByText('9 running',{exact:true}).waitFor()
    await page.getByText(/4.2 GB requested; host total 1.9 GB/).waitFor()
    await page.getByText(/not measured on attached hosts/).waitFor()
   }
   if (view === 'resources') {
    if (await page.getByText('present',{exact:true}).count() !== 4) throw new Error('confirmed resources lost presence')
    if (await page.locator('.chip').filter({hasText:'not applicable'}).count() !== 3) throw new Error('non-running resources need no health check')
   }
   if (view === 'resources-invalidated') {
    if (await page.getByText(/^present/).count()) throw new Error('Invalidated records cannot confirm presence')
    if (await page.locator('.chip.pass').count()) throw new Error('Invalidated records cannot establish health')
    if (!await page.locator('.chip.unknown').count()) throw new Error('Invalidated inventory must stay unknown')
   }
   if (view === 'integrations') {
    const row = page.locator('tr.int-row').filter({hasText:'infisical'})
    if (await row.locator('.chip.fail').count()) throw new Error('unused Infisical is red')
    await row.getByText('not set up',{exact:true}).first().waitFor()
    await page.locator('tr.int-row').filter({hasText:'gitlab'}).locator('.chip.pending').filter({hasText:'old · working'}).waitFor()
   }
   if (view === 'security') {
    await page.getByText("One scan report couldn't be read; security checks are incomplete.").waitFor()
    await page.getByText('Ask a project member with permission to open a change to re-run the scan.').waitFor()
    const details=page.locator('details').filter({has:page.getByText('Technical details',{exact:true})})
    if (await details.getAttribute('open') !== null) throw new Error('parser diagnostics must start folded')
   }
  }
  await page.screenshot({path:new URL(`${phase}-${view}-${width}-${theme}.png`,out).pathname,fullPage:true})
 }
 await context.close()
}
await writeFile(new URL(`${phase}-errors.json`,out),JSON.stringify(errors,null,2))
await browser.close()
if(errors.length) throw new Error(errors.join('\n'))
