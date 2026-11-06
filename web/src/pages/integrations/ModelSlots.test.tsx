// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { ModelProfilePage, ModelProfiles } from './ModelSlots'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => { vi.unstubAllGlobals() })

const row = (slot: string, patterns: Record<string, string>, fallback?: string) => ({ slot, gloss: `${slot} work`, patterns, fallback, consumers: slot === 'fast' ? ['approval briefs', 'project descriptions'] : [] })

// An unset job says the model it actually gets and where from: its
// fallback task, Main · strong in the end, then standard's chain.
it('says what an unset task gets, on each profile', async () => {
  const matrix = {
    profiles: ['standard', 'high'], spendNote: '', overrides: [],
    projects: [{ slug: 'a', name: 'A', profile: 'high' }],
    slots: [row('main-strong', { standard: 'm/base' }), row('review', { standard: 'm/review' }, 'secondary-strong'), row('worker', {}, 'review'), row('plan', {}), row('main-fast', {}), row('secondary-strong', {}), row('secondary-fast', {}, 'main-fast'), row('security', {}, 'review'), row('fast', {}, 'main-fast'), row('scout', {}, 'fast'), row('inspect', {}, 'scout')],
  }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.includes('/model-slots') ? matrix : { models: [], unlisted: [] }))))
  const client = newQueryClient()
  const el = document.createElement('div'); document.body.appendChild(el)
  const root = createRoot(el)
  const at = (path: string) => act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter key={path} initialEntries={[path]}><Routes>
    <Route path="/integrations/profiles" element={<ModelProfiles title="Model profiles" />} />
    <Route path="/integrations/profiles/:profile" element={<ModelProfilePage />} />
  </Routes></MemoryRouter></QueryClientProvider>) })
  // The list says how each profile differs from standard and who uses it.
  await at('/integrations/profiles')
  await vi.waitFor(() => expect(el.querySelectorAll('[role=listitem]')).toHaveLength(2))
  const rows = [...el.querySelectorAll('[role=listitem]')].map((r) => r.textContent)
  expect(rows[0]).toContain('The default profile')
  expect(rows[1]).toContain('Same as standard')
  expect(rows[1]).not.toContain('A')
  expect(el.querySelector('a[href="/integrations/profiles/high/projects"]')?.textContent).toBe('1 project')
  const unset = (job: string) => el.querySelector<HTMLSelectElement>(`select[aria-label="${job} model"]`)!.options[0].textContent
  await at('/integrations/profiles/standard')
  await vi.waitFor(() => expect(el.querySelector('select')).not.toBeNull())
  expect(unset('Plan')).toBe('Same as Main · strong (m/base)')
  expect(unset('Worker')).toBe('Same as Review (m/review)')
  expect(unset('Main · strong')).toBe('Not set')
  for (const label of ['Main · fast', 'Second opinion · strong']) {
    expect(unset(label)).toBe('Same as Main · strong (m/base)')
  }
  expect(unset('Second opinion · fast')).toBe('Same as Main · fast → Main · strong (m/base)')
  expect(unset('Security')).toBe('Same as Review (m/review)')
  expect(unset('Fast')).toBe('Same as Main · fast → Main · strong (m/base)')
  expect(unset('Scout')).toBe('Same as Fast → Main · strong (m/base)')
  expect(unset('Inspect')).toBe('Same as Scout → Main · strong (m/base)')
  expect(el.querySelector('select[aria-label="Fast model"]')!.closest('tr')!.textContent).toContain('Used by: approval briefs, project descriptions')
  matrix.slots[1].patterns.standard = ''
  await act(async () => { await client.invalidateQueries() })
  await vi.waitFor(() => expect(unset('Review')).toBe('Same as Second opinion · strong → Main · strong (m/base)'))
  expect(unset('Security')).toBe('Same as Review → Main · strong (m/base)')
  matrix.slots[1].patterns.standard = 'm/review'
  await act(async () => { await client.invalidateQueries() })
  // Another profile's unset jobs use standard's models, and it names its projects.
  await at('/integrations/profiles/high')
  await vi.waitFor(() => expect(el.querySelector('h1')!.textContent).toBe('high'))
  expect(unset('Review')).toBe('Same as standard (m/review)')
  expect(unset('Worker')).toBe("Same as standard's Review (m/review)")
  expect(el.querySelector('section[aria-label="Projects"]')).toBeNull()
  expect(el.querySelector('a[href$="/high/projects"]')?.textContent).toBe('1 project')
  // A profile's basic choice beats a task cell in standard, including the label.
  matrix.slots[0].patterns.high = 'm/high'
  await act(async () => { await client.invalidateQueries() })
  await vi.waitFor(() => expect(unset('Review')).toBe('Same as Second opinion · strong → Main · strong (m/high)'))
  expect(unset('Worker')).toBe('Same as Review → Main · strong (m/high)')
  act(() => root.unmount()); el.remove(); client.clear()
})

// A previously stored choice remains selected even when curation excludes it.
it('marks a stored model outside the enabled list in red', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.includes('/model-slots')
      ? { profiles: ['standard'], slots: [row('main-strong', { standard: 'openai/old-model' })], projects: [] }
      : { models: [{ pattern: 'openai/enabled-model', binding: 'openai', source: 'curated' }], unlisted: [] }))))
  const client = newQueryClient()
  const el = document.createElement('div'); document.body.appendChild(el)
  const root = createRoot(el)
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><ModelProfilePage /></MemoryRouter></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.querySelector('.error.small')?.textContent).toBe('not enabled on openai'))
    const select = el.querySelector('select')!
    expect(select.selectedOptions[0].textContent).toBe('openai/old-model')
    expect([...select.options].map(o => o.textContent)).toContain('openai/enabled-model')
  } finally {
    act(() => root.unmount()); el.remove(); client.clear()
  }
})
