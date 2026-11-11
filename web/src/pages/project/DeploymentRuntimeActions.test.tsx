// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import type { DeploymentView } from '../../wire.gen'
import { DeploymentRuntimeActions } from './DeploymentRuntimeActions'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const env: DeploymentView = { status: { word: 'working', reason: '', stale: false },
  id: 'main-id', name: 'main', displayLabel: 'Lunch Orders', kind: 'standing', backups: true, generation: 2,
  runtimeState: 'running', runtimeActor: 'Priya', runtimeControls: { deploy: '', start: '', stop: '', restart: '' }, runtimeActions: ['start', 'stop', 'restart'],
  servingRelease: null, promotionBundleId: null, promotionRelease: 0, promotionSource: '',
  approvalRole: '', feedRevision: 1, feedConfirmed: true, readinessLabel: 'deployed', restoreLabel: 'unknown',
  targetId: null, url: '', running: true, releaseRun: null, updatedAt: '', targetName: '', observation: 'running', observationStale: false, protected: false,
}

const cleanups: (() => void)[] = []
afterEach(async () => { for (const clean of cleanups.splice(0)) await act(clean); vi.restoreAllMocks() })
async function render(value = env, current = true, onPending: (state: string | undefined) => void = () => {}) {
  const client = newQueryClient(), host = document.createElement('div'), root = createRoot(host)
  document.body.append(host)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><DeploymentRuntimeActions slug="lunch" env={value} current={current} reload={async () => {}} onPending={onPending} /></MemoryRouter></QueryClientProvider>))
  cleanups.push(() => { root.unmount(); client.clear(); host.remove() })
  return host
}
function button(host: HTMLElement, label: string) { return [...host.querySelectorAll('button')].find(b => b.textContent === label)! }
it.each(['start', 'restart'] as const)('%s acts on the first click', async action => {
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{"ok":true}', { headers: { 'Content-Type': 'application/json' } }))
  const pending = vi.fn()
  const host = await render({ ...env, runtimeState: action === 'start' ? 'stopped' : 'running' }, true, pending)
  await act(async () => button(host, action === 'start' ? '▶ Start' : '↻ Restart').click())
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(pending.mock.calls.map(args => args[0])).toEqual([action === 'start' ? 'starting' : 'restarting', undefined])
  expect(String(fetch.mock.calls[0][0])).toContain(`/projects/lunch/deployments/main/${action}`)
})
it('Stop asks once in place, using the display name, before acting', async () => {
  const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{"ok":true}', { headers: { 'Content-Type': 'application/json' } }))
  const host = await render()
  await act(async () => button(host, '■ Stop').click())
  expect(fetch).not.toHaveBeenCalled()
  expect(host.textContent).toContain("Stop Lunch Orders? People can't use it until you start it again.")
  await act(async () => button(host, 'Stop').click())
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(String(fetch.mock.calls[0][0])).toContain('/main/stop')
})
// Only the acts a target can ever take are buttons: a bucket website has
// none to show, App Platform only Restart. A disabled button that can never
// work is noise, and its sentence repeats what the missing button says.
it('shows no runtime buttons for a bucket website', async () => {
  const reason = 'Bucket websites cannot be started, stopped or restarted'
  const host = await render({ ...env, runtimeActions: [], runtimeControls: { deploy: '', start: reason, stop: reason, restart: reason } })
  expect(host.querySelectorAll('button')).toHaveLength(0)
  expect(host.textContent).not.toContain(reason)
})
it('shows only Restart on App Platform', async () => {
  const reason = 'App Platform cannot keep an app stopped'
  const host = await render({ ...env, runtimeActions: ['restart'], runtimeControls: { deploy: '', start: reason, stop: reason, restart: '' } })
  expect([...host.querySelectorAll('button')].map(b => b.textContent)).toEqual(['↻ Restart'])
  expect(button(host, '↻ Restart').disabled).toBe(false)
  expect(host.textContent).not.toContain(reason)
})
it('keeps a refused act that can apply, with why it is refused now', async () => {
  const reason = 'The latest deploy failed. Deploy a release successfully before starting or restarting.'
  const host = await render({ ...env, runtimeControls: { deploy: '', start: reason, stop: '', restart: reason } })
  expect(button(host, '↻ Restart').disabled).toBe(true)
  expect(button(host, '■ Stop').disabled).toBe(false)
  expect(host.textContent).toContain(reason)
})
it.each(['starting', 'stopping', 'restarting'])('shows %s and disables duplicate actions', async runtimeState => {
  const host = await render({ ...env, runtimeState })
  expect([...host.querySelectorAll('button')].every(b => b.disabled)).toBe(true)
})
it('disables actions on a stale deployment reading', async () => {
  const host = await render(env, false)
  expect([...host.querySelectorAll('button')].every(b => b.disabled)).toBe(true)
})

it('offers the same action for an unconfirmed stop, keeping its error visible', async () => {
  const host = await render({ ...env, runtimeState: 'stopping', runtimeError: 'Connection lost' })
  expect(host.textContent).toContain('Connection lost')
  expect(button(host, '■ Stop').disabled).toBe(false)
  expect(host.querySelectorAll('button')).toHaveLength(1)
})
