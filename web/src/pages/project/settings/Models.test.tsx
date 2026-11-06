// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../../data'
import Project from '../../Project'
import { effective } from '../../../parts/modelProfiles'
import type { ModelSlotMatrix } from '../../../wire.gen'
import { ModelProfilePage } from '../../integrations/ModelSlots'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => vi.unstubAllGlobals())

// The project owns profile assignment; the profile page only opens that editor.
async function mount(path: string, allowed = true, configured = true) {
  let profile = 'standard'
  const writes: unknown[] = []
  const matrix = (): ModelSlotMatrix => ({ spendNote: '', profiles: ['standard', 'high'], projects: [{ slug: 'a', name: 'Alpha', profile }], slots: [
    { slot: 'main-strong', base: true, gloss: 'Main model', patterns: { standard: 'vendor/base', high: 'vendor/high' } },
    { slot: 'chat', base: false, gloss: 'The helper people talk to', patterns: {}, fallback: 'main-strong' },
    { slot: 'review', base: false, gloss: 'A second set of eyes on finished work', patterns: { standard: 'vendor/review' }, fallback: 'secondary-strong' },
    { slot: 'checks', base: false, gloss: 'AI conformance checks', patterns: {}, fallback: 'review' },
    { slot: 'custom', base: false, gloss: 'A custom task', patterns: { standard: 'vendor/custom' } },
  ] })
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'PUT') {
      writes.push(JSON.parse(String(init.body)))
      profile = JSON.parse(String(init.body)).profile
      return new Response('{"ok":true}')
    }
    if (url.endsWith('/projects/a/model-profile')) {
      const m = matrix()
      return new Response(JSON.stringify({ profile, profiles: m.profiles, tasks: m.slots.map(row => ({ slot: row.slot, gloss: row.gloss, models: Object.fromEntries(m.profiles.map(p => {
        const value = effective(m.slots, row.slot, p)
        return [p, { pattern: configured ? value.model : '', slot: value.from, profile: value.profile }]
      })) })) }))
    }
    if (url.endsWith('/model-slots') && path.startsWith('/projects/')) return new Response('{}', { status: 403 })
    return new Response(JSON.stringify(url.endsWith('/projects/a') ? { id: 'a', slug: 'a', name: 'Alpha' }
      : url.includes('/authz/me') ? { project: { 'project:configure-model-profile': allowed } }
      : url.endsWith('/model-slots') ? (configured ? matrix() : { ...matrix(), slots: matrix().slots.map(row => ({ ...row, patterns: {} })) }) : url.endsWith('/projects') || url.endsWith('/deployments') ? [] : { models: [], unlisted: [] }))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Routes>
    <Route path="/projects/:slug/:tab/:stab" element={<Project />} />
    <Route path="/integrations/profiles/:profile" element={<ModelProfilePage />} />
  </Routes></MemoryRouter></QueryClientProvider>))
  return { el, writes, cleanup: () => { act(() => root.unmount()); el.remove(); client.clear() } }
}
const select = (el: HTMLElement) => el.querySelector<HTMLSelectElement>('select[aria-label="Model profile"]')!
const apply = (el: HTMLElement) => [...el.querySelectorAll('button')].find(b => b.textContent === 'Use profile')!
const choose = async (el: HTMLElement, value: string) => act(async () => {
  select(el).value = value
  select(el).dispatchEvent(new Event('change', { bubbles: true }))
})

it('shows the current profile and the effective model for every task, and switches here', async () => {
  const { el, writes, cleanup } = await mount('/projects/a/settings/models')
  try {
    await vi.waitFor(() => expect(select(el)?.value).toBe('standard'))
    expect(el.textContent).toContain('This project uses standard.')
    const rows = [...el.querySelectorAll('tbody tr')].map(r => [...r.querySelectorAll('td')].map(c => c.textContent))
    expect(rows).toEqual(expect.arrayContaining([
      [expect.stringContaining('Main · strong'), 'vendor/base'],
      [expect.stringContaining('Chat'), 'vendor/base · from Main · strong'],
      [expect.stringContaining('Review'), 'vendor/review'],
      [expect.stringContaining('Checks'), 'vendor/review · from Review'],
      [expect.stringContaining('Custom'), 'vendor/custom'],
    ]))
    expect(el.querySelector('button[aria-label="About Models"]')).not.toBeNull()
    await choose(el, 'high')
    expect([...el.querySelectorAll('tbody tr')].find(r => r.querySelector('strong')?.textContent === 'Chat')?.querySelectorAll('td')[1].textContent).toBe('vendor/high · from Main · strong')
    await vi.waitFor(() => expect(apply(el).disabled).toBe(false))
    await act(async () => apply(el).click())
    await vi.waitFor(() => expect(el.textContent).toContain('This project uses high.'))
    expect(writes).toEqual([{ profile: 'high' }])
    expect(vi.mocked(fetch).mock.calls.some(([url, init]) => String(url).endsWith('/projects/a/model-profile') && init?.method === 'PUT')).toBe(true)
  } finally { cleanup() }
})

it('disables switching and explains the missing authority', async () => {
  const { el, writes, cleanup } = await mount('/projects/a/settings/models?profile=high', false)
  try {
    await vi.waitFor(() => expect(select(el)?.value).toBe('high'))
    expect(select(el).disabled).toBe(true)
    expect(apply(el).disabled).toBe(true)
    expect(el.textContent).toContain('You need permission to change this project’s model profile.')
    await act(async () => apply(el).click())
    expect(writes).toEqual([])
  } finally { cleanup() }
})

it('opens the project editor from a profile’s more menu with that profile selected', async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  const { el, writes, cleanup } = await mount('/integrations/profiles/high')
  try {
    const more = await vi.waitFor(() => {
      const b = el.querySelector<HTMLButtonElement>('button[aria-label="More actions for Alpha"]')
      expect(b).not.toBeNull(); return b!
    })
    expect(el.querySelector('select[aria-label="project"]')).toBeNull()
    await act(async () => more.click())
    const move = [...document.querySelectorAll('button')].find(b => b.textContent?.startsWith('Move project here'))!
    expect(move).toBeDefined()
    await act(async () => move.click())
    await vi.waitFor(() => expect(select(el)?.value).toBe('high'))
    expect(el.querySelector('h1')?.textContent).toBe('Models')
    expect(el.textContent).toContain('This project uses standard.')
    expect(writes).toEqual([])
  } finally { cleanup() }
})

it('says when no model is set without repeating the task name', async () => {
  const { el, cleanup } = await mount('/projects/a/settings/models', true, false)
  try {
    await vi.waitFor(() => expect(el.querySelectorAll('tbody tr').length).toBe(5))
    for (const row of el.querySelectorAll('tbody tr')) expect(row.querySelectorAll('td')[1].textContent).toBe('No model set')
  } finally { cleanup() }
})
