// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import { WayOfWorkingStep } from './WayOfWorkingStep'
import { newDraft, type DraftAct } from './draft'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
afterEach(() => vi.unstubAllGlobals())

it.each([false, true])('preselects monthly updates and allows unticking them (interview: %s)', async (interview) => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/packs/interview') ? { decided: true, packs: ['go'], why: 'Go service' } :
    url.endsWith('/packs') ? ['go', 'custom-maintenance'].map(name => ({ name, suggested: name === 'custom-maintenance', enabled: true, requires: [], services: [] })) : { dependencies: [] }
  ))))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const actions: DraftAct[] = []
  const dispatch = (a: DraftAct) => { actions.push(a) }
  const draft = { ...newDraft(), name: 'Shop', description: 'A shop', choosing: !interview }
  const button = (part: string) => [...el.querySelectorAll('button')].find(b => b.textContent?.includes(part))!
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><WayOfWorkingStep d={draft} act={dispatch} /></MemoryRouter></QueryClientProvider>) })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 30)) })
    if (interview) {
      await act(async () => { button('Next →').click() })
      expect(actions.find(a => a.type === 'packs')).toMatchObject({ packs: ['go', 'custom-maintenance'] })
    } else {
      const card = (name: string) => [...el.querySelectorAll<HTMLElement>('[aria-pressed]')].find(e => e.textContent?.includes(name))!
      expect(card('custom-maintenance').getAttribute('aria-pressed')).toBe('true')
      await act(async () => { card('go').click() }); await act(async () => { card('custom-maintenance').click() })
      expect(card('custom-maintenance').getAttribute('aria-pressed')).toBe('false')
      await act(async () => { button('Use 1 pack').click() })
      expect(actions.find(a => a.type === 'packs')).toMatchObject({ packs: ['go'] })
    }
  } finally {
    await act(async () => root.unmount())
    client.clear()
  }
})

// Each pack says what it is in one line; choosing no packs is a choice
// like the others, with the same mark; the button says how many it takes.
it('describes each pack, offers no packs as a marked choice and counts the packs taken', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/packs') ? [
      { name: 'godot-3d', description: 'Godot 3D games with export checks.', suggested: false, enabled: true, requires: [], services: [] },
      { name: 'dependency-updates', description: 'Monthly dependency updates\n\nUpdates a project\'s dependencies once a month through the\ndependency-uplift workflow.', suggested: true, enabled: true, requires: [], services: [] },
    ] : { dependencies: [] }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  const actions: DraftAct[] = []
  const draft = { ...newDraft(), name: 'Eggs', description: 'A game', choosing: true }
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><WayOfWorkingStep d={draft} act={(a: DraftAct) => { actions.push(a) }} /></MemoryRouter></QueryClientProvider>) })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 30)) })
    const card = (name: string) => [...el.querySelectorAll<HTMLElement>('.pack-card')].find(e => e.textContent?.includes(name))!
    expect(card('godot-3d').textContent).toContain('Godot 3D games with export checks.')
    // A description's short first paragraph does not run into the next.
    expect([...card('dependency-updates').querySelectorAll('p')].map(p => p.textContent)).toEqual(['Monthly dependency updates', 'Updates a project\'s dependencies once a month through the\ndependency-uplift workflow.'])
    const none = card('No packs')
    expect(none.getAttribute('aria-pressed')).toBe('false')
    expect(none.querySelector('.pick-mark')?.textContent).toBe('+')
    expect(el.textContent).not.toContain('none (bare)')
    const primary = () => el.querySelector<HTMLButtonElement>('.row.mt-3 button.primary')!
    expect(primary().textContent).toBe('Use 1 pack')
    await act(async () => { card('godot-3d').click() })
    expect(primary().textContent).toBe('Use 2 packs')
    await act(async () => { none.click() })
    expect(card('No packs').getAttribute('aria-pressed')).toBe('true')
    expect(card('No packs').querySelector('.pick-mark')?.textContent).toBe('✓')
    expect(primary().textContent).toBe('Use no packs')
    await act(async () => { primary().click() })
    expect(actions.find(a => a.type === 'packs')).toMatchObject({ packs: [] })
  } finally { await act(async () => root.unmount()); client.clear() }
})
