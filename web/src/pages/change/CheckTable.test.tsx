// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import type { Step } from '../../api'
import type { WaitingOn } from '../../wire.gen'
import { getKey, newQueryClient } from '../../data'
import { CheckTable } from './CheckTable'
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const step = (id: string, status: Step['status'], attempt = 2) => ({ id, runId: 'run', stage: 'checks', name: id, type: 'scan', status, attempt, config: {}, evidence: {}, isCheck: true, checkAttention: true, statusLabel: status === 'pass' ? 'passed' : status === 'running' ? 'working' : 'waiting for checks', overrideBy: '', overrideReason: '', finishedAt: '', startedAt: new Date(Date.now() - 7000).toISOString() } as Step)
const waiting = { kind: 'work', run: 'run', step: 'queued', attempt: 2, queue: { binding: 'checks-model', ahead: 3, says: 'waiting for a slot on checks-model, 3 ahead' } } as WaitingOn
function draw(steps: Step[], client = newQueryClient(), waitingOn = waiting) {
 return <MemoryRouter><QueryClientProvider client={client}><CheckTable steps={steps} technical={false} waitingOn={waitingOn} /></QueryClientProvider></MemoryRouter>
}
it('reads the last live line for a running scan in Checks, alongside elapsed time', async () => {
 vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ running: true, lines: ['starting', 'scanning the committed tree'], total: 2 }))))
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 try {
  await act(async () => root.render(draw([step('scan', 'running')], client)))
  await vi.waitFor(async () => { await act(async () => {}); expect(el.textContent).toContain('scanning the committed tree') })
  expect(fetch).toHaveBeenCalledWith('/api/v1/steps/scan/live?from=0', expect.anything())
  expect(el.textContent).not.toContain('starting')
  expect(el.querySelector('.ch-check-duration')?.textContent).toMatch(/\ds/)
 } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
it.each([null, []])('does not call a scan failed or done before its first progress line: %j', lines => {
 const client = newQueryClient()
 client.setQueryData(getKey('/steps/scan/live?from=0'), { running: true, lines, total: 0 })
 try {
  const html = renderToStaticMarkup(draw([step('scan', 'running')], client))
  expect(html).not.toMatch(/could not be read|passed|failed/)
  expect(html).toContain('running')
 } finally { client.clear() }
})
it('shows the server queue words only on the identified run, step and attempt', () => {
 const queued = step('queued', 'pending')
 const el = document.createElement('div')
 for (const [steps, count] of [
  [[queued, step('later', 'pending')], 1],
  [[{ ...queued, attempt: 1 }], 0],
  [[{ ...queued, runId: 'other-run' }], 0],
  [[{ ...queued, status: 'pass' }], 0],
 ] as [Step[], number][]) {
  el.innerHTML = renderToStaticMarkup(draw(steps))
  expect(el.textContent!.split(waiting.queue!.says).length - 1).toBe(count)
 }
})

it('recognizes an initial queued attempt when the owner omits zero attempt numbers', () => {
 const el = document.createElement('div')
 el.innerHTML = renderToStaticMarkup(draw([{ ...step('queued', 'pending'), attempt: undefined }], newQueryClient(), { ...waiting, attempt: undefined }))
 expect(el.textContent).toContain(waiting.queue!.says)
})
