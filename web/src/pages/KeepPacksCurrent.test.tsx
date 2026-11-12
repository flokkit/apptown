// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import { KeepPacksCurrent } from './KeepPacksCurrent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
it('previews a separate pack category, explains catch-up, and enables the shown meaning', async () => {
 const calls: [string, unknown][] = []
 let enabled = false
 const rule = { ruleId: 'packs-rule', revision: 1, enabled: true, issuer: { name: 'Sam' }, question: { definition: { scope: 'instance', categories: { waysOfWorking: true } } } }
 const projectRule = { ...rule, ruleId: 'project-only-rule', question: { definition: { ...rule.question.definition, scope: 'projects' } } }
 vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
  if (init?.method === 'POST') {
   const body = JSON.parse(String(init.body)); calls.push([url, body])
   if (url.endsWith('/preview')) return new Response(JSON.stringify({ definition: body, digest: 'shown-meaning', shown: 'Moves projects to newly published pack versions and releases them.', portfolio: [{ projectId: 'old', project: 'old-app', covered: false, needsPackUpgrade: ['language'], onlyChecksStarts: true, deployments: [] }] }))
   enabled = !url.endsWith('/disable'); return new Response('{}')
  }
  return new Response(JSON.stringify(url.endsWith('/authz/me') ? { instance: { 'instance:manage-standing-approval': true } } : enabled ? [{ rule: projectRule }, { rule }] : [{ rule: projectRule }]))
 }))
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 const button = (text: string) => [...el.querySelectorAll('button')].find(b => b.textContent === text)!
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><KeepPacksCurrent /></MemoryRouter></QueryClientProvider>))
  await settled(client)
  await act(async () => button('Preview').click())
  expect(el.textContent).toContain('The janitor will move these projects when Keep packs current is on.')
  expect(el.textContent).toContain('only checks that it starts')
  expect(calls[0][1]).toMatchObject({ categories: { waysOfWorking: true, dependencies: '' }, schedules: [{ workflow: 'janitor-adopt', crons: {} }] })
  await act(async () => button('Enable').click())
  await settled(client)
  expect(calls[1][1]).toMatchObject({ shownDigest: 'shown-meaning', expectedRevision: 0 })
  expect(el.textContent).toContain('Enabled by Sam')
  await act(async () => button('Revoke').click())
  expect(calls[2]).toEqual(['/api/v1/standing-approvals/packs-rule/disable', { expectedRevision: 1 }])
 } finally { await act(async () => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
