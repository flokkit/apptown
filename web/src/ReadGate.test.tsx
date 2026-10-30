// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient, retryDelay, useGet } from './data'
import { ReadGate, StaleScope } from './ReadGate'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const reply = (status: number, body: unknown) => vi.fn(async () => new Response(JSON.stringify(body), { status }))
const never = () => vi.fn(() => new Promise<Response>(() => {}))

function Names() {
  const q = useGet<string[]>('/names')
  return <ReadGate q={q} what="the names">{(names) => names.length === 0 ? <p>No names yet.</p> : <ul>{names.map((n) => <li key={n}>{n}</li>)}</ul>}</ReadGate>
}

// One way a read can go, and what the gate must then show. `refresh` is a
// second answer, read after the first is shown, on the client's real retry
// schedule.
type Row = {
  name: string
  first: () => ReturnType<typeof vi.fn>
  refresh?: () => ReturnType<typeof vi.fn>
  text?: string
  shows?: string[]
  hides?: string[]
  // The .error element's text, or null when there must be none.
  error?: string | null
  failure?: string
  // The out-of-date mark's text, and what its accessible name must say.
  mark?: string
  label?: string[]
}

const rows: Row[] = [
  { name: 'keeps the answer shown when a refresh fails, and marks it as possibly old',
    first: () => reply(200, ['ada', 'grace']), refresh: () => reply(502, { error: 'AppTown is restarting' }),
    shows: ['grace'],
    // A compact mark with the time of the last good read, not a sentence.
    mark: '● unknown',
    // The reason rides in the mark's accessible name, quoted as AppTown
    // gave it, not with the read's note about the answer shown.
    label: ['(AppTown is restarting)', '11 Oct, 09:41'],
    hides: ['showing the last answer read'],
    error: null },
  { name: 'shows a failed first read as an error, never as an empty answer',
    first: () => reply(404, { error: 'no such list' }), error: '● unknownThe names could not be read: no such list', failure: 'The names could not be read: no such list', mark: '● unknown', hides: ['No names yet'] },
  { name: 'says reading while the first answer is on its way', first: never, text: 'Loading…' },
  { name: 'says a refused read is not available, which is not an error',
    first: () => reply(403, { error: 'forbidden' }), text: 'Not available to you: the names.', error: null },
]

function expectShown(el: HTMLElement, row: Row) {
  if (row.text !== undefined) expect(el.textContent).toBe(row.text)
  for (const s of row.shows ?? []) expect(el.textContent).toContain(s)
  for (const s of row.hides ?? []) expect(el.textContent).not.toContain(s)
  if (row.error === null) expect(el.querySelector('.error')).toBeNull()
  else if (row.error !== undefined) expect(el.querySelector('.error')?.textContent).toBe(row.error)
  if (row.failure) expect(el.querySelector('.error .chip')?.getAttribute('title')).toBe(row.failure)
  const mark = el.querySelector('.chip.unknown')
  if (row.mark === undefined) expect(mark).toBeNull()
  else expect(mark?.textContent).toBe(row.mark)
  for (const s of row.label ?? []) expect(mark?.getAttribute('aria-label')).toContain(s)
  for (const s of row.label ?? []) expect(mark?.getAttribute('title')).toContain(s)
}

const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

// The display rules of design/architecture/web-data.md, held once for every
// page that gates on a read.
describe('ReadGate', () => {
  let done = () => {}
  // A fixed local morning, so the mark's time is known.
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date(2026, 9, 11, 9, 41)) })
  afterEach(() => { done(); done = () => {}; vi.useRealTimers(); vi.unstubAllGlobals() })

  it.each(rows.map((r) => [r.name, r] as const))('%s', async (_, row) => {
    vi.stubGlobal('fetch', row.first())
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={client}><Names /></QueryClientProvider>) })
    done = () => { act(() => root.unmount()); client.clear() }
    if (row.refresh) {
      await vi.waitFor(() => expect(el.textContent).toContain('grace'))
      const fetch = row.refresh()
      vi.stubGlobal('fetch', fetch)
      let refreshed!: Promise<unknown>
      await act(async () => { refreshed = client.refetchQueries({ queryKey: getKey('/names') }).catch(() => {}) })
      // While the failed refresh is asked again, the answer stays and is not
      // yet called old.
      await advance(retryDelay(0) + retryDelay(1) - 1)
      expect(fetch).toHaveBeenCalledTimes(2)
      expect(el.textContent).toContain('grace')
      expect(el.querySelector('.stale-note .chip.unknown')).toBeNull()
      await advance(1)
      await act(async () => { await refreshed })
      expect(fetch).toHaveBeenCalledTimes(3)
    }
    await vi.waitFor(() => expectShown(el, row))
  })
})

function Places() {
  const q = useGet<string[]>('/places')
  return <ReadGate q={q} what="the places">{(places) => <p>{places.join(', ')}</p>}</ReadGate>
}

function Broken() {
  const q = useGet<string[]>('/broken')
  return <ReadGate q={q} what="the broken list">{() => <p>never shown</p>}</ReadGate>
}

// One notice for the page, not the sentence in every section: each stale
// section carries only its mark, and a section with no answer at all still
// says it failed rather than being counted as old.
describe('StaleScope', () => {
  let done = () => {}
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date(2026, 9, 11, 9, 41)) })
  afterEach(() => { done(); done = () => {}; vi.useRealTimers(); vi.unstubAllGlobals() })

  it('says once that sections are out of date, and clears when they read again', async () => {
    let down = false
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (String(url).includes('/broken')) return new Response(JSON.stringify({ error: 'no such list' }), { status: 404 })
      if (down) return new Response(JSON.stringify({ error: 'AppTown is restarting' }), { status: 502 })
      return new Response(JSON.stringify(String(url).includes('/names') ? ['ada', 'grace'] : ['Wellington']), { status: 200 })
    }))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={client}><StaleScope><Names /><Places /><Broken /></StaleScope></QueryClientProvider>)
    })
    done = () => { act(() => root.unmount()); client.clear() }
    await vi.waitFor(() => expect(el.textContent).toContain('Wellington'))
    await vi.waitFor(() => expect(el.querySelector('.error .chip')?.getAttribute('title')).toContain('no such list'))
    // Nothing is old yet, so the page says nothing about it.
    expect(el.querySelector('.stale-notice')).toBeNull()

    const refresh = async () => {
      let refreshed!: Promise<unknown>
      await act(async () => {
        refreshed = Promise.all(['/names', '/places'].map((p) => client.refetchQueries({ queryKey: getKey(p) }).catch(() => {})))
      })
      await advance(retryDelay(0) + retryDelay(1))
      await act(async () => { await refreshed })
    }
    down = true
    await refresh()
    await vi.waitFor(() => expect(el.querySelectorAll('.stale-note .chip.unknown')).toHaveLength(2))
    const notices = el.querySelectorAll('.stale-notice')
    expect(notices).toHaveLength(1)
    expect(notices[0].textContent).toBe('Refresh failed. Last answer shown.')
    // The notice stands above the sections, and the reason is not repeated
    // as text in each one.
    expect(el.firstElementChild).toBe(notices[0])
    expect(el.querySelectorAll('details.status-help:not([open])')).toHaveLength(3)
    // The section that never had an answer is still an error, not old.
    expect(el.querySelector('.error')?.textContent).toBe('● unknownThe broken list could not be read: no such list')
    expect(el.textContent).toContain('grace')

    down = false
    await refresh()
    await vi.waitFor(() => expect(el.querySelector('.stale-note .chip.unknown')).toBeNull())
    expect(el.querySelector('.stale-notice')).toBeNull()
  })
})
