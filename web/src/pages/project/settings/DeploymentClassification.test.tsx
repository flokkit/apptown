// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { newQueryClient } from '../../../data'
import { DeploymentClassification } from './DeploymentClassification'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const flyRefusal = 'fly-syd serves the app at its fly.dev address to anyone, with no AppTown sign-in in front of it, so it cannot keep a deployment to the organisation audience'
const meaning = 'I state that Web serves only the project\'s team and holds test data that AppTown may discard on a release'

function view(proposed?: object) {
  return {
    audience: 'public', data: 'production', effect: 'Who may deploy changes at once.',
    audiences: [{ value: 'team' }, { value: 'organisation', refusal: flyRefusal }, { value: 'partners' }, { value: 'public' }],
    dataClasses: [{ value: 'synthetic' }, { value: 'non-production' }, { value: 'production' }],
    ...(proposed ? { proposed } : {}),
  }
}

it('shows refused values at the field, what lowering removes, and saves only with the stated meaning', async () => {
  const sent: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'PUT') {
      sent.push(JSON.parse(String(init.body)))
      return new Response('{}')
    }
    if (url.includes('?')) return new Response(JSON.stringify(view({ audience: 'team', data: 'synthetic', lowers: true, adds: [], removes: ['Only people with release permission can deploy here'], meaning })))
    return new Response(JSON.stringify(view()))
  }))
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  const client = newQueryClient()
  const change = async (label: string, value: string) => act(async () => {
    const select = el.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!
    select.value = value
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  const save = () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Save audience and data')!
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><DeploymentClassification slug="shop" name="web" onChanged={() => {}} /></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('select[aria-label="Audience"]')).not.toBeNull())
    const organisation = el.querySelector<HTMLOptionElement>('select[aria-label="Audience"] option[value="organisation"]')!
    expect(organisation.disabled).toBe(true)
    expect(el.textContent).toContain('Not here: ' + flyRefusal)
    expect(el.textContent).toContain('This deployment is protected. Protected means')
    await change('Audience', 'team')
    await change('Data', 'synthetic')
    await vi.waitFor(() => expect(el.textContent).toContain(meaning))
    expect(el.textContent).toContain('Removes')
    expect(save().disabled).toBe(true)
    await act(async () => { el.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click() })
    expect(save().disabled).toBe(false)
    await act(async () => { save().click() })
    await vi.waitFor(() => expect(sent).toEqual([{ audience: 'team', data: 'synthetic', was: { audience: 'public', data: 'production' }, meaning }]))
  } finally {
    await act(async () => root.unmount())
    el.remove(); client.clear(); vi.unstubAllGlobals()
  }
})


it('refreshes a refused stale save so the person can keep their edits on current values', async () => {
  let audience = 'team'
  const sent: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'PUT') {
      sent.push(JSON.parse(String(init.body)))
      return new Response(JSON.stringify({ error: 'the audience or data changed while you were editing; review the current values and save again (now organisation audience, production data)' }), { status: 409 })
    }
    const base = { ...view(), audience }
    if (url.includes('?')) return new Response(JSON.stringify({ ...base, proposed: { audience: 'public', data: 'production', lowers: false, adds: [], removes: [] } }))
    return new Response(JSON.stringify(base))
  }))
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  const client = newQueryClient()
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><DeploymentClassification slug="shop" name="web" onChanged={() => {}} /></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('select[aria-label="Audience"]')).not.toBeNull())
    await act(async () => {
      const select = el.querySelector<HTMLSelectElement>('select[aria-label="Audience"]')!
      select.value = 'public'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    const save = () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Save audience and data')!
    await vi.waitFor(() => expect(save().disabled).toBe(false))
    // Another person saves after our last GET, before our PUT.
    audience = 'organisation'
    await act(async () => { save().click() })
    await vi.waitFor(() => expect(el.textContent).toContain('now organisation audience'))
    expect(sent).toEqual([{ audience: 'public', data: 'production', was: { audience: 'team', data: 'production' }, meaning: '' }])
    await vi.waitFor(() => expect([...el.querySelectorAll('button')].some(b => b.textContent === 'Keep my edits on the current values')).toBe(true))
    expect(save().disabled).toBe(true)
  } finally {
    await act(async () => root.unmount())
    el.remove(); client.clear(); vi.unstubAllGlobals()
  }
})
