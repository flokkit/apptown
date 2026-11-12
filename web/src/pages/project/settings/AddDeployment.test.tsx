// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../../data'
import type { OfferedTarget } from '../../../wire.gen'
import { AddDeployment } from './Deployments'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const target = (name: string, extra = {}) => ({ name, kind: 'docker-host', kindInfo: { title: 'Docker host' }, capabilities: {}, offer: { implicit: true, group: '*' }, reason: '', bound: 0, ...extra }) as OfferedTarget

it('puts the target first, constrains offer choices, clears fixed fields and keeps protection space', async () => {
  const client = newQueryClient()
  client.setQueryData(getKey('/authz/me'), { instance: {} })
  client.setQueryData(getKey('/projects/shop/final-copies'), { copies: [] })
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'Offer changed; choose another target.' }), { status: 409 })))
  const el = document.createElement('div'), root = createRoot(el)
  const choices = [target('full', { reason: 'full has reached its limit' }), target('local'), target('fly', { offer: { group: '*', caps: { audiences: ['public'], data: ['synthetic'] } } })]
  const select = (label: string) => el.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!
  const change = async (label: string, value: string) => act(async () => { select(label).value = value; select(label).dispatchEvent(new Event('change', { bubbles: true })) })
  const create = () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Create')!
  const name = () => el.querySelector<HTMLInputElement>('input[aria-label="Deployment name"]')!
  const enterName = async (value: string) => act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(name(), value)
    name().dispatchEvent(new Event('input', { bubbles: true }))
  })
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><AddDeployment slug="shop" choices={choices} existing={[]} onDone={() => {}} /></MemoryRouter></QueryClientProvider>))
    expect(el.querySelector('input, select')).toBe(select('Target'))
    expect(select('Target').value).toBe('local')
    expect([...select('Target').options].map(o => o.value)).toEqual(['local', 'fly', 'full'])
    expect(select('Target').options[2].disabled).toBe(true)
    expect(el.textContent).toContain('full has reached its limit')
    expect(el.textContent).toContain('Missing a target? Ask a workspace admin to make it available to this project.')
    const slot = el.querySelector('.deployment-protected-slot')!
    expect(slot.getAttribute('aria-hidden')).toBe('true')
    await act(async () => create().click())
    expect(el.textContent).toContain('give it a name')
    await enterName('web')
    expect(el.textContent).not.toContain('give it a name')
    await change('Target', 'fly')
    expect([...select('Audience').options].map(o => [o.text, o.selected])).toEqual([['Public', true]])
    expect([...select('Data').options].map(o => [o.text, o.selected])).toEqual([['Test data', true]])
    expect(el.textContent).toContain('for Public, Test data')
    expect(el.querySelector('.deployment-protected-slot')).toBe(slot)
    expect(slot.getAttribute('aria-hidden')).toBe('false')
    expect(slot.compareDocumentPosition(create()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    await act(async () => create().click())
    expect(el.textContent).toContain('Offer changed')
    await change('Target', 'local')
    expect(el.textContent).not.toContain('Offer changed')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('shows reasons when all targets are unavailable and links administrators to Targets', async () => {
 const client = newQueryClient()
 client.setQueryData(getKey('/authz/me'), { instance: { 'platform:manage-deploy-targets': true } })
 client.setQueryData(getKey('/projects/shop/final-copies'), { copies: [] })
 const el = document.createElement('div'), root = createRoot(el)
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><AddDeployment slug="shop" choices={[target('offline', { reason: 'offline needs a check' })]} existing={[]} onDone={() => {}} /></MemoryRouter></QueryClientProvider>))
  expect(el.textContent).toContain('offline needs a check')
  expect(el.querySelector('a')?.getAttribute('href')).toBe('/targets')
  expect([...el.querySelectorAll('button')].find(b => b.textContent === 'Create')?.disabled).toBe(true)
 } finally { act(() => root.unmount()); client.clear() }
})

it('keeps promotion source and App Platform Start from in the creation request', async () => {
 const client = newQueryClient(), sent: unknown[] = []
 client.setQueryData(getKey('/authz/me'), { instance: {} })
 client.setQueryData(getKey('/projects/shop/final-copies'), { copies: [{ backup: 'backup-1', deployment: 'old', takenAt: '2026-11-01T00:00:00Z', verified: true, bytes: 10 }] })
 vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
  if (init?.method === 'POST') sent.push(JSON.parse(String(init.body)))
  return new Response(JSON.stringify(url.endsWith('/final-copies') ? { copies: [{ backup: 'backup-1', deployment: 'old', takenAt: '2026-11-01T00:00:00Z', verified: true, bytes: 10 }] } : {}))
 }))
 const el = document.createElement('div'), root = createRoot(el)
 const select = async (label: string, value: string) => act(async () => {
  const field = el.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!
  field.value = value; field.dispatchEvent(new Event('change', { bubbles: true }))
 })
 try {
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><AddDeployment slug="shop" choices={[target('app-platform', { kind: 'do-app' })]} existing={[{ id: 'source', name: 'staging', kind: 'standing' } as import('../../../wire.gen').DeploymentView]} onDone={() => {}} /></MemoryRouter></QueryClientProvider>))
  await act(async () => {
   const input = el.querySelector('input')!
   Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'production')
   input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await select('Release feed', 'promotion')
  const create = [...el.querySelectorAll('button')].find(b => b.textContent === 'Create')!
  expect(create.disabled).toBe(true)
  await select('Source deployment', 'staging')
  await select('Start from', 'backup-1')
  expect(create.disabled).toBe(false)
  await act(async () => create.click())
  expect(sent).toEqual([{ name: 'production', target: 'app-platform', feed: 'promotion', from: 'staging', audience: 'team', data: 'non-production', startFrom: 'backup-1' }])
 } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
