// shoot.mjs: AppTown's screenshots of a preview (verification/screenshots.go).
// Runs in the agent image with Node's built-in WebSocket and the image's
// Chromium headless shell, driven over the DevTools protocol. Arguments:
// the report path, then for each page: name, width, height, phone (0/1),
// URL. A page is captured only if its top frame never leaves the preview's
// origin before the capture finishes; otherwise the report says where it
// went. The report is JSON: {pages: [{name, url, finalUrl, png?, reason?}]}.
import { spawn } from 'node:child_process'
import { mkdtempSync, writeFileSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'

const [reportPath, ...rest] = process.argv.slice(2)
const pages = []
for (let i = 0; i + 5 <= rest.length; i += 5) {
  pages.push({ name: rest[i], width: +rest[i + 1], height: +rest[i + 2], phone: rest[i + 3] === '1', url: rest[i + 4] })
}
// The budgets (screenshots.go): 10 s a page, 50 s for them all, under the
// executor's 60 s for the container.
const PAGE_MS = 10000
const SETTLE_MS = 500
const ALL_DEADLINE = Date.now() + 50000
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const withTimeout = (p, ms, what) => Promise.race([p, sleep(ms).then(() => { throw new Error(what) })])

const profile = mkdtempSync(join(tmpdir(), 'shots-'))
const browser = spawn('chromium-headless-shell', ['--no-sandbox', '--disable-gpu', '--hide-scrollbars',
  '--remote-debugging-address=127.0.0.1', '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'],
  { stdio: ['ignore', 'ignore', 'pipe'] })
const wsURL = await withTimeout(new Promise((resolve, reject) => {
  let buf = ''
  browser.stderr.on('data', (d) => {
    buf += d
    const m = buf.match(/DevTools listening on (ws:\/\/\S+)/)
    if (m) resolve(m[1])
  })
  browser.on('exit', (c) => reject(new Error(`the browser exited (${c})`)))
}), 20000, 'the browser did not start')

const ws = new WebSocket(wsURL)
await new Promise((r, j) => { ws.onopen = r; ws.onerror = () => j(new Error('cannot reach the browser')) })
let nextId = 1
const waiting = new Map()
const listeners = new Set()
ws.onmessage = (ev) => {
  const msg = JSON.parse(ev.data)
  if (msg.id && waiting.has(msg.id)) {
    const { resolve, reject } = waiting.get(msg.id)
    waiting.delete(msg.id)
    msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result)
  } else if (msg.method) {
    for (const l of listeners) l(msg)
  }
}
const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
  const id = nextId++
  waiting.set(id, { resolve, reject })
  ws.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }))
})

// shoot pictures one page within one deadline (deadline, a time in ms):
// every phase, from opening a tab to the picture, shares what is left of
// it. Every request for the top frame's document is watched, each redirect
// hop included: a page that passes through another origin, even one that
// comes back, is not pictured.
async function shoot(p, deadline) {
  const origin = new URL(p.url).origin
  const out = { name: p.name, url: p.url, finalUrl: '' }
  const left_ = () => deadline - Date.now()
  const within = (promise, what) => left_() <= 0 ? Promise.reject(new Error(what)) : withTimeout(promise, left_(), what)
  let targetId = ''
  let sessionId = ''
  let mainFrame = ''
  let left = ''
  let loaded = false
  const offOrigin = (u) => {
    if (left || !u || u === 'about:blank') return
    try { if (new URL(u).origin !== origin) left = u } catch { left = u }
  }
  const onEvent = (msg) => {
    if (msg.sessionId !== sessionId) return
    if (msg.method === 'Network.requestWillBeSent' && msg.params.type === 'Document' && msg.params.frameId === mainFrame) offOrigin(msg.params.request.url)
    if (msg.method === 'Page.frameNavigated' && !msg.params.frame.parentId) { out.finalUrl = msg.params.frame.url; offOrigin(msg.params.frame.url) }
    if (msg.method === 'Page.loadEventFired') loaded = true
  }
  listeners.add(onEvent)
  try {
    ;({ targetId } = await within(send('Target.createTarget', { url: 'about:blank' }), 'the browser did not open a tab'))
    ;({ sessionId } = await within(send('Target.attachToTarget', { targetId, flatten: true }), 'the browser did not open a tab'))
    await within(send('Page.enable', {}, sessionId), 'the browser did not answer')
    await within(send('Network.enable', {}, sessionId), 'the browser did not answer')
    mainFrame = (await within(send('Page.getFrameTree', {}, sessionId), 'the browser did not answer')).frameTree.frame.id
    await within(send('Emulation.setDeviceMetricsOverride', { width: p.width, height: p.height, deviceScaleFactor: 1, mobile: p.phone }, sessionId), 'the browser did not answer')
    const nav = await within(send('Page.navigate', { url: p.url }, sessionId), 'the page did not answer in time')
    if (nav.errorText) { out.reason = left ? `left the preview for ${left}` : `the page did not load: ${nav.errorText}`; return out }
    while (!loaded && !left && left_() > 0) await sleep(100)
    if (left) { out.reason = `left the preview for ${left}`; return out }
    if (!loaded) { out.reason = 'the page did not finish loading in time'; return out }
    await sleep(Math.min(SETTLE_MS, Math.max(0, left_())))
    if (left) { out.reason = `left the preview for ${left}`; return out }
    const shot = await within(send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }, sessionId), 'the picture was not taken in time')
    if (left) { out.reason = `left the preview for ${left}`; return out }
    out.png = shot.data
    return out
  } catch (e) {
    out.reason = String(e.message || e)
    return out
  } finally {
    listeners.delete(onEvent)
    if (targetId) send('Target.closeTarget', { targetId }).catch(() => {})
  }
}

// One deadline for the whole capture, under the executor's own, and one per
// page within it. The report is written after every page, so pictures
// already taken survive a capture that runs out of time.
const results = []
const write = () => { mkdirSync(dirname(reportPath), { recursive: true }); writeFileSync(reportPath, JSON.stringify({ pages: results })) }
for (const p of pages) {
  if (Date.now() >= ALL_DEADLINE) { results.push({ name: p.name, url: p.url, finalUrl: '', reason: 'not reached: the capture ran out of time' }); continue }
  results.push(await shoot(p, Math.min(Date.now() + PAGE_MS, ALL_DEADLINE)))
  write()
}
write()
ws.close()
browser.kill('SIGKILL')
process.exit(0)
