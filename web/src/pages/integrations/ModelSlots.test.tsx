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

const row = (slot: string, patterns: Record<string, string>, fallback?: string) => ({ slot, gloss: `${slot} work`, patterns, fallback })

// An unset job says the model it actually gets and where from: its
// fallback job, Default in the end, and on another profile standard's cell.
it('says what an unset task gets, on each profile', async () => {
  const matrix = {
    profiles: ['standard', 'high'], spendNote: '', overrides: [],
    projects: [{ slug: 'a', name: 'A', profile: 'high' }],
    slots: [row('default', { standard: 'm/base' }), row('review', { standard: 'm/review' }), row('worker', {}, 'review'), row('plan', {})],
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
  expect(rows[1]).toContain('A')
  const unset = (job: string) => el.querySelector<HTMLSelectElement>(`select[aria-label="${job} model"]`)!.options[0].textContent
  await at('/integrations/profiles/standard')
  await vi.waitFor(() => expect(el.querySelector('select')).not.toBeNull())
  expect(unset('Plan')).toBe('Same as Main · strong (m/base)')
  expect(unset('Worker')).toBe('Same as Review (m/review)')
  expect(unset('Main · strong')).toBe('Not set')
  // Another profile's unset jobs use standard's models, and it names its projects.
  await at('/integrations/profiles/high')
  await vi.waitFor(() => expect(el.querySelector('h1')!.textContent).toBe('high'))
  expect(unset('Review')).toBe('Same as standard (m/review)')
  expect(unset('Worker')).toBe('Same as Review (m/review)')
  expect(el.querySelector('section[aria-label="Projects"]')!.textContent).toContain('A')
  act(() => root.unmount()); el.remove(); client.clear()
})
