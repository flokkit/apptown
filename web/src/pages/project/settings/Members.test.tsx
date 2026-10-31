// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { settled } from '../../../settled'
import { newQueryClient } from '../../../data'
import { MembersTab } from './Members'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const roles = { roles: [{ name: 'project-contributor', title: 'Project contributor', description: '', scope: 'project', surfaced: true, capabilities: [] }], capabilities: {} }

// AppTown's people and groups are instance information. A project owner
// who may manage members but not read that list names the person instead,
// and the refusal is not shown as a failure.
describe('adding a member without AppTown directory', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // Either AppTown has said the person cannot read the directory (it is
  // then not asked for), or the read is refused when asked.
  it.each([
    ['said beforehand', { 'institution:read-structure': false }],
    ['refused when read', {}],
  ])('names the person when the directory is not available (%s)', async (_how, instance) => {
    const sent: { url: string; body: unknown }[] = []
    const asked: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url)
      asked.push(u)
      if (init?.method === 'POST') {
        sent.push({ url: u, body: JSON.parse(String(init.body)) })
        return new Response(JSON.stringify({ ok: true }))
      }
      if (u.endsWith('/users') || u.endsWith('/groups')) {
        return new Response(JSON.stringify({ error: 'member may not see users' }), { status: 403 })
      }
      if (u.includes('/authz/me')) return new Response(JSON.stringify({ user: 'member', instance, project: { 'project:manage-members': true } }))
      if (u.includes('/authz/roles')) return new Response(JSON.stringify(roles))
      if (u.includes('/authz/explain')) return new Response(JSON.stringify({ user: 'member', capability: 'change:approve', decision: { allow: true }, whoCan: ['member'] }))
      if (u.includes('/members')) return new Response(JSON.stringify({ roles: [], open: false }))
      return new Response(JSON.stringify({ slug: 'mine', requireIndependentApproval: false }))
    }))
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={newQueryClient()}><MembersTab slug="mine" /></QueryClientProvider>) })
    const box = await vi.waitFor(() => {
      const input = el.querySelector('input[aria-label="who to add"]') as HTMLInputElement | null
      expect(input).not.toBeNull()
      return input!
    })
    expect(el.textContent).not.toContain('may not see users')
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(box, 'kim')
      box.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Add')!.click() })
    await vi.waitFor(() => expect(sent).toEqual([{ url: '/api/v1/projects/mine/members', body: { grantee: 'kim', role: 'project-contributor' } }]))
    expect(asked.some((u) => u.endsWith('/users'))).toBe(Object.keys(instance).length === 0)
    act(() => root.unmount())
  })
})

it('groups a person’s roles and omits disabled accounts from the picker', async () => {
 const grant = { subjectType: 'person', subject: 'sam', roleTitle: 'Project owner', role: 'project-owner', id: 'owner' }
 vi.stubGlobal('fetch', vi.fn(async (url: string) => {
  const data = url.includes('/authz/me') ? { instance: { 'institution:read-structure': true }, project: { 'project:manage-members': true } }
   : url.endsWith('/users') ? { users: [{ username: 'sam', disabled: false }, { username: 'disabled-reader', disabled: true }] }
   : url.endsWith('/groups') ? { groups: [{ name: 'all-authenticated-users' }] }
   : url.includes('/authz/roles') ? roles
   : url.includes('/authz/explain') ? { whoCan: ['sam'] }
   : url.endsWith('/members') ? { roles: [grant, { ...grant, id: 'approver', role: 'project-approver', roleTitle: 'Project approver' }], open: false }
   : { requireIndependentApproval: false }
  return new Response(JSON.stringify(data))
 }))
 const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><MembersTab slug="shop" /></QueryClientProvider>))
  await settled(client)
  expect(el.querySelectorAll('strong')).toHaveLength(1)
  expect(el.textContent).toContain('owner, approver')
  const options = [...el.querySelectorAll('option')].map(o => o.textContent)
  expect(options).not.toContain('disabled-reader')
  expect(options).toContain('Everyone signed in')
 } finally { await act(async () => root.unmount()); client.clear() }
})
