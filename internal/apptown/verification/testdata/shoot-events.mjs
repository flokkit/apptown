// Exercise the whole capture driver with a DevTools peer that sends resource
// events. Chromium and disk effects are replaced at the module boundary.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { EventEmitter } from 'node:events'
import vm from 'node:vm'

let report
const child = new EventEmitter()
child.stderr = new EventEmitter()
child.kill = () => {}
class DevTools {
  constructor() { queueMicrotask(() => this.onopen()) }
  close() {}
  send(raw) {
    const { id, method } = JSON.parse(raw)
    queueMicrotask(() => {
      const result = method === 'Target.createTarget' ? { targetId: 'tab' }
        : method === 'Target.attachToTarget' ? { sessionId: 'session' }
        : method === 'Page.getFrameTree' ? { frameTree: { frame: { id: 'main' } } }
        : method === 'Page.captureScreenshot' ? { data: 'picture' } : {}
      this.onmessage({ data: JSON.stringify({ id, result }) })
      if (method !== 'Page.navigate') return
      const event = (method, params) => this.onmessage({ data: JSON.stringify({ method, params, sessionId: 'session' }) })
      for (const [requestId, canceled] of [['cancelled', true], ['broken', false]]) {
        event('Network.requestWillBeSent', { requestId, type: 'Image', request: { url: `https://example.test/${requestId}.png?secret=hidden` } })
        event('Network.loadingFailed', { requestId, canceled, errorText: canceled ? 'net::ERR_ABORTED' : 'net::ERR_FAILED' })
      }
      event('Network.requestWillBeSent', { requestId: 'http', type: 'Stylesheet', request: { url: 'https://example.test/style.css' } })
      event('Network.responseReceived', { requestId: 'http', type: 'Stylesheet', response: { status: 404 } })
      event('Network.loadingFailed', { requestId: 'http', canceled: false, errorText: 'net::ERR_FAILED' })
      // A stylesheet refused on its MIME check: the status can arrive before
      // the request, and the load is reported as cancelled.
      event('Network.responseReceivedExtraInfo', { requestId: 'early', statusCode: 404 })
      event('Network.requestWillBeSent', { requestId: 'early', type: 'Stylesheet', request: { url: 'https://example.test/early.css' } })
      event('Network.loadingFailed', { requestId: 'early', canceled: true, errorText: 'net::ERR_ABORTED' })
      event('Page.loadEventFired', {})
    })
  }
}
const context = vm.createContext({
  WebSocket: DevTools, URL, Date,
  process: { argv: ['node', 'shoot.mjs', '/report.json', 'desktop', '1280', '800', '0', 'https://example.test/'], exit: () => {} },
  setTimeout: (fn, ms) => { if (ms < 10000) queueMicrotask(fn) },
})
const modules = {
  'node:child_process': { spawn: () => { queueMicrotask(() => child.stderr.emit('data', 'DevTools listening on ws://browser')); return child } },
  'node:fs': { mkdtempSync: () => '/profile', mkdirSync: () => {}, writeFileSync: (_, body) => { report = JSON.parse(body) } },
  'node:os': { tmpdir: () => '/tmp' },
  'node:path': { join: (...parts) => parts.join('/'), dirname: () => '/' },
}
const script = new vm.SourceTextModule(readFileSync(new URL('../shoot.mjs', import.meta.url), 'utf8'), { context })
await script.link((name) => {
  const exports = modules[name]
  return new vm.SyntheticModule(Object.keys(exports), function () {
    for (const [key, value] of Object.entries(exports)) this.setExport(key, value)
  }, { context })
})
await script.evaluate()
assert.deepEqual(report.pages[0].failedResources, [
  { type: 'Image', url: 'example.test/broken.png', error: 'net::ERR_FAILED' },
  { type: 'Stylesheet', url: 'example.test/style.css', status: 404 },
  { type: 'Stylesheet', url: 'example.test/early.css', status: 404 },
])
