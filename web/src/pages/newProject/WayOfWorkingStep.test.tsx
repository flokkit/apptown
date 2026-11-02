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
      await act(async () => { button('Take ').click() })
      expect(actions.find(a => a.type === 'packs')).toMatchObject({ packs: ['go'] })
    }
  } finally {
    await act(async () => root.unmount())
    client.clear()
  }
})
