// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import ChangePage from './ChangePage'
import { newQueryClient } from '../data'
import { withFeatures } from '../features.fixture'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('shows one unknown tests result when the landing repeats the working check', async () => {
 Element.prototype.scrollIntoView = () => {}
 const tests = (runId: string) => ({ id: `${runId}-tests`, runId, stage: 'checks', name: 'tests', type: 'tests', status: 'pass', statusLabel: 'passed', isCheck: true, checkAttention: true, evidence: {}, config: {} })
 const detail = { run: { id: '002', status: 'done', currentStage: 'checks' }, stages: ['checks'], steps: [tests('002')], next: [], artifacts: [], events: [] }
 const view = { change: { id: 'X', number: 1, title: 'Update the calendar', body: 'Update the calendar', state: 'shipped' }, projectSlug: 's', runs: [{ id: '001', role: 'working' }, { id: '002', role: 'landing' }], detail, waitingOn: { kind: 'closed', says: 'landed', tone: 'ok', you: 'no', who: [] }, goesTo: { branch: 'main', deployments: [] }, checks: { started: true, tone: 'unknown', text: 'Checks not all known' }, evidence: { landingReleases: [], releases: [], targets: [] }, commits: [], adrs: [], next: [] }
 vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string) => new Response(JSON.stringify(
  String(url) === '/api/v1/changes/X' ? view : String(url) === '/api/v1/runs/001/trace' ? { ...detail, run: { ...detail.run, id: '001' }, steps: [tests('001')] } : {}
 )))))
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/changes/X?tab=overview']}><Routes><Route path="/changes/:id" element={<ChangePage />} /></Routes></MemoryRouter></QueryClientProvider>))
  await vi.waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/runs/001/trace', expect.anything()))
  await vi.waitFor(() => expect(el.textContent?.match(/Tests: number unknown/g)).toHaveLength(1))
 } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
