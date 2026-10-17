// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, useEffect, useRef } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider, QueryObserver, focusManager, useQueryClient } from '@tanstack/react-query'
import { post } from './api'
import { MAX_BACKOFF, consecutiveFailures, identityChanged, newQueryClient, pollPace, retryDelay, useAction, useGet, useGetText, getKey } from './data'

// React's act() needs to know it runs in a test environment.
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('consecutiveFailures', () => {
  it('counts failed reads since the last good one, however each fetch counts its own', () => {
    const query = {}
    let errors = 0, errorAt = 0, dataAt = 0, clock = 0
    const fail = () => { errors++; errorAt = ++clock; return consecutiveFailures(query, { errorUpdateCount: errors, errorUpdatedAt: errorAt, dataUpdatedAt: dataAt }) }
    const succeed = () => { dataAt = ++clock; return consecutiveFailures(query, { errorUpdateCount: errors, errorUpdatedAt: errorAt, dataUpdatedAt: dataAt }) }
    expect(succeed()).toBe(0)
    expect([fail(), fail(), fail()]).toEqual([1, 2, 3])
    expect(succeed()).toBe(0)
    expect(fail()).toBe(1)
    // A five-second poll failing four times in a row waits 10, 20, 40, then 60 s.
    const waits = [1, 2, 3, 4].map((n) => pollPace(5000, n))
    expect(waits).toEqual([10_000, 20_000, 40_000, MAX_BACKOFF])
  })
})

describe('pollPace', () => {
  it('keeps the page pace while reads succeed', () => {
    expect(pollPace(5000, 0)).toBe(5000)
  })
  it('doubles per consecutive failure, up to a minute', () => {
    expect(pollPace(5000, 1)).toBe(10_000)
    expect(pollPace(5000, 3)).toBe(40_000)
    expect(pollPace(5000, 4)).toBe(MAX_BACKOFF)
    expect(pollPace(5000, 20)).toBe(MAX_BACKOFF)
  })
  it('does not poll without a pace', () => {
    expect(pollPace(0, 0)).toBe(false)
    expect(pollPace(-1, 2)).toBe(false)
  })
})

describe('newQueryClient', () => {
  it('reads fresh, refreshes a visible tab, never polls a hidden one, and keeps answers for a revisit', () => {
    const d = newQueryClient().getDefaultOptions().queries!
    expect(d.staleTime).toBe(0)
    expect(d.gcTime).toBe(5 * 60_000)
    expect(d.refetchOnWindowFocus).toBe(true)
    expect(d.refetchIntervalInBackground).toBe(false)
  })
})

describe('useGet', () => {
  const mounted: { el: HTMLElement; root: ReturnType<typeof createRoot> }[] = []
  afterEach(() => {
    for (const m of mounted.splice(0)) { act(() => m.root.unmount()); m.el.remove() }
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })
  const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

  function mount(ui: React.ReactNode) {
    const el = document.createElement('div')
    document.body.appendChild(el)
    const root = createRoot(el)
    mounted.push({ el, root })
    act(() => { root.render(<QueryClientProvider client={newQueryClient()}>{ui}</QueryClientProvider>) })
    return el
  }

  function Show({ path }: { path: string }) {
    const { data, err } = useGet<{ n: number }>(path)
    return <span>{err ? `error: ${err}` : data ? `n=${data.n}` : 'loading'}</span>
  }

  it('shares one request between readers of the same path', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ n: 7 }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    const el = mount(<><Show path="/thing" /><Show path="/thing" /></>)
    await vi.waitFor(() => expect(el.textContent).toBe('n=7n=7'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('shows a failed read as an error, never as an empty answer', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'the store is down' }), { status: 503, statusText: 'Service Unavailable' })))
    const el = mount(<Show path="/broken" />)
    // Still reading while the server error is asked again on the schedule.
    await advance(retryDelay(0) + retryDelay(1) - 1)
    expect(el.textContent).toBe('loading')
    await advance(1)
    await vi.waitFor(() => expect(el.textContent).toContain('error: '))
    expect(el.textContent).not.toContain('n=')
  })

  function Poll({ path }: { path: string }) {
    const { data } = useGet<{ n: number }>(path, { poll: 1000 })
    return <span>{data ? `n=${data.n}` : 'loading'}</span>
  }

  it('backs a failing poll off further with each failure in a row', async () => {
    vi.useFakeTimers()
    try {
      const fetchMock = vi.fn(async () => new Response(JSON.stringify({ error: 'down' }), { status: 500 }))
      vi.stubGlobal('fetch', fetchMock)
      focusManager.setFocused(true)
      mount(<Poll path="/failing" />)
      // Failures in a row wait 2 s, 4 s, then 8 s: fetches at 0, 2, 6 and 14 s.
      // A pace that never grew would have fetched every 2 s, seven times by 13 s.
      for (let i = 0; i < 26; i++) await act(async () => { await vi.advanceTimersByTimeAsync(500) })
      expect(fetchMock.mock.calls.length).toBe(3)
      await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
      expect(fetchMock.mock.calls.length).toBe(4)
    } finally {
      focusManager.setFocused(undefined)
      vi.useRealTimers()
    }
  })

  function Fresh({ path }: { path: string }) {
    const { data, current: fresh, err } = useGet<{ v: number }>(path)
    return <span>{data ? `v=${data.v}` : 'none'} {fresh ? 'fresh' : 'held'}{err ? ' failed' : ''}</span>
  }

  // An answer another reader already held is shown but is not fresh, and a
  // failed refresh does not make it fresh: only a good answer this page read.
  it('calls an answer fresh only after a successful read since mount', async () => {
    vi.useFakeTimers()
    const client = newQueryClient()
    const held = { v: 1 }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'down' }), { status: 500 })))
    const el = document.createElement('div')
    const root = createRoot(el)
    // Another open reader keeps the held answer alive, as the cache now does
    // only while something reads it.
    const unsubscribe = new QueryObserver(client, { queryKey: getKey('/fresh'), enabled: false }).subscribe(() => {})
    client.setQueryData(getKey('/fresh'), held)
    await act(async () => { root.render(<QueryClientProvider client={client}><Fresh path="/fresh" /></QueryClientProvider>) })
    await advance(retryDelay(0) + retryDelay(1) - 1)
    expect(el.textContent).toBe('v=1 held')
    await advance(1)
    await vi.waitFor(() => expect(el.textContent).toContain('failed'))
    expect(el.textContent).toBe('v=1 held failed')
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ v: 2 }), { status: 200 })))
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/fresh') }) })
    await vi.waitFor(() => expect(el.textContent).toBe('v=2 fresh'))
    act(() => root.unmount())
    unsubscribe()
    client.clear()
  })

  // The clock is frozen, so every answer carries the mount's own timestamp: a
  // rule comparing times would never call one fresh. The path then changes
  // without a remount to one another reader already holds; that answer is
  // shown but not fresh until this reader's own read returns, even when it
  // returns the same value.
  it('judges freshness by answers read, not by the clock, per path', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-09-28T00:00:00Z'))
    const client = newQueryClient()
    let release: (r: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn((url: string) => url.endsWith('/a')
      ? Promise.resolve(new Response(JSON.stringify({ v: 1 })))
      : new Promise<Response>((resolve) => { release = resolve })))
    const el = document.createElement('div')
    const root = createRoot(el)
    const render = (path: string) => act(async () => { root.render(<QueryClientProvider client={client}><Fresh path={path} /></QueryClientProvider>) })
    const unsubscribe = new QueryObserver(client, { queryKey: getKey('/b'), enabled: false }).subscribe(() => {})
    try {
      await render('/a')
      await vi.waitFor(() => expect(el.textContent).toBe('v=1 fresh'))
      client.setQueryData(getKey('/b'), { v: 9 })
      await render('/b')
      expect(el.textContent).toBe('v=9 held')
      await act(async () => { release(new Response(JSON.stringify({ v: 9 }))) })
      await vi.waitFor(() => expect(el.textContent).toBe('v=9 fresh'))
    } finally {
      act(() => root.unmount())
      unsubscribe()
      client.clear()
      vi.useRealTimers()
    }
  })

  it('returns a recovered poll to the page pace', async () => {
    vi.useFakeTimers()
    try {
      let n = 0
      const fetchMock = vi.fn(async () => ++n <= 2
        ? new Response(JSON.stringify({ error: 'down' }), { status: 500 })
        : new Response(JSON.stringify({ n }), { status: 200 }))
      vi.stubGlobal('fetch', fetchMock)
      focusManager.setFocused(true)
      mount(<Poll path="/recovering" />)
      // Two failures wait 2 s then 4 s: fetches at 0, 2 and 6 s, the last one
      // good. Back at the 1 s pace it fetches at 7, 8 and 9 s; a pace still
      // backed off would not fetch again until 14 s.
      for (let i = 0; i < 19; i++) await act(async () => { await vi.advanceTimersByTimeAsync(500) })
      expect(fetchMock.mock.calls.length).toBe(6)
    } finally {
      focusManager.setFocused(undefined)
      vi.useRealTimers()
    }
  })

  function Read({ path, text }: { path: string; text?: boolean }) {
    const json = useGet<{ v: number }>(text ? null : path)
    const plain = useGetText(text ? path : null)
    const err = text ? plain.err : json.err
    return <span>{err || 'waiting'}</span>
  }

  // A refusal or a missing record is shown after one request; a server
  // error is asked again, at most twice, then shown. Text reads alike.
  it.each([
    ['a 404 JSON read', '/gone', false, 404, 1],
    ['a 404 text read', '/gone.diff', true, 404, 1],
    ['a 503 JSON read', '/busy', false, 503, 3],
    ['a 503 text read', '/busy.diff', true, 503, 3],
  ] as const)('retries %s only when asking again could help', async (_, path, text, status, requests) => {
    vi.useFakeTimers()
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ error: `status ${status}` }), { status }))
    vi.stubGlobal('fetch', fetchMock)
    const el = mount(<Read path={path} text={text} />)
    // Each retry waits its turn on the client's schedule, and the read is
    // still waiting until the last request fails.
    for (let attempt = 0; attempt < requests - 1; attempt++) {
      await advance(retryDelay(attempt) - 1)
      expect(fetchMock.mock.calls.length).toBe(attempt + 1)
      expect(el.textContent).toBe('waiting')
      await advance(1)
    }
    await vi.waitFor(() => expect(el.textContent).toBe(`status ${status}`))
    expect(fetchMock.mock.calls.length).toBe(requests)
    // Nothing more is asked once the failure is shown.
    await advance(retryDelay(0) + retryDelay(1) + retryDelay(2))
    expect(fetchMock.mock.calls.length).toBe(requests)
  })

  it('polls at the page pace while visible and not while hidden', async () => {
    vi.useFakeTimers()
    let n = 0
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ n: ++n }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    focusManager.setFocused(true)
    try {
      const el = mount(<Poll path="/polled" />)
      await advance(0)
      expect(el.textContent).toBe('n=1')
      // The page pace is one second: two more reads in two seconds.
      await advance(2000)
      expect(el.textContent).toBe('n=3')
      focusManager.setFocused(false)
      const seen = fetchMock.mock.calls.length
      await advance(2500)
      expect(fetchMock.mock.calls.length).toBe(seen)
    } finally {
      focusManager.setFocused(undefined)
    }
  })
})

describe('reads and acts by resource', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  function harness() {
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    const reads: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      reads.push(`${init?.method ?? 'GET'} ${url.replace('/api/v1', '')}`)
      return new Response(JSON.stringify({ n: reads.length }))
    }))
    return { client, el, root, reads }
  }

  function Reader({ path }: { path: string }) {
    const { data } = useGet<{ n: number }>(path)
    return <span>{data ? 'y' : 'n'}</span>
  }

  let act_: ReturnType<typeof useAction>['run'] | null = null
  function Actor({ affects }: { affects?: string[] }) {
    act_ = useAction(undefined, affects ? { affects } : {}).run
    return null
  }

  it('keys a path by its segments, so a resource covers what is under it', () => {
    expect(getKey('/projects/shop/settings')).toEqual(['get', 'projects', 'shop', 'settings'])
    expect(getKey('/projects?limit=6')).toEqual(['get', 'projects', '?limit=6'])
  })

  // An act refreshes the resources it names and what is under them, not
  // every read on the page.
  it('refreshes what an act affects and nothing else', async () => {
    const h = harness()
    await act(async () => {
      h.root.render(<QueryClientProvider client={h.client}>
        <Reader path="/projects/shop" /><Reader path="/projects/shop/services" /><Reader path="/changes/5" />
        <Actor affects={['/projects/shop']} />
      </QueryClientProvider>)
    })
    await vi.waitFor(() => expect(h.el.textContent).toBe('yyy'))
    h.reads.length = 0
    await act(async () => { await act_!(async () => 'done') })
    await vi.waitFor(() => expect(h.reads.sort()).toEqual(['GET /projects/shop', 'GET /projects/shop/services']))
    act(() => h.root.unmount())
  })

  // A double click must not send an act twice.
  it('ignores a second run while one is under way', async () => {
    const h = harness()
    await act(async () => { h.root.render(<QueryClientProvider client={h.client}><Actor affects={[]} /></QueryClientProvider>) })
    let release!: () => void
    let sent = 0
    const slow = () => { sent++; return new Promise<string>((r) => { release = () => r('ok') }) }
    let first: Promise<unknown>, second: unknown
    await act(async () => { first = act_!(slow); second = await act_!(slow) })
    await act(async () => { release(); await first })
    expect(sent).toBe(1)
    expect(second).toBeUndefined()
    act(() => h.root.unmount())
  })

  // One person's views never fill with what was read for another: a change
  // of identity drops what is held and throws away an answer still on its way.
  it('drops held answers and discards a read in flight when the identity changes', async () => {
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    const pending: ((r: Response) => void)[] = []
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((r) => { pending.push(r) })))
    // Idle readers keep the held answers alive, as open views do.
    const keep = ['/projects/held', '/auth/me'].map((p) => new QueryObserver(client, { queryKey: getKey(p), enabled: false }).subscribe(() => {}))
    client.setQueryData(getKey('/projects/held'), { n: 1 })
    client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: 'alice' })
    const shown: string[] = []
    function Slow() { const { data } = useGet<{ who: string }>('/projects/slow'); if (data) shown.push(data.who); return null }
    await act(async () => { root.render(<QueryClientProvider client={client}><Slow /></QueryClientProvider>) })
    await vi.waitFor(() => expect(pending.length).toBe(1))
    await act(async () => { identityChanged(client) })
    expect(client.getQueryData(getKey('/projects/held'))).toBeUndefined()
    expect(client.getQueryData(getKey('/auth/me'))).toEqual({ mode: 'internal', user: 'alice' })
    // The reader asks again for the new identity; the old answer lands late.
    await vi.waitFor(() => expect(pending.length).toBe(2))
    await act(async () => { pending[0](new Response('{"who":"alice"}')) })
    await act(async () => { pending[1](new Response('{"who":"bob"}')) })
    await vi.waitFor(() => expect(shown).toContain('bob'))
    expect(shown).not.toContain('alice')
    keep.forEach((stop) => stop())
    act(() => root.unmount())
  })

  // A read refused for want of a session means the session ended: the page
  // asks who this is again, which shows sign-in and changes the identity.
  it('asks who this is again when a read is refused for want of a session', async () => {
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    const asked: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      asked.push(url.replace('/api/v1', ''))
      return url.endsWith('/auth/me')
        ? new Response('{"mode":"internal"}')
        : new Response(JSON.stringify({ error: 'sign in' }), { status: 401 })
    }))
    await act(async () => {
      root.render(<QueryClientProvider client={client}><Reader path="/auth/me" /><Reader path="/projects" /></QueryClientProvider>)
    })
    await vi.waitFor(() => expect(asked.filter((p) => p === '/auth/me').length).toBeGreaterThanOrEqual(2))
    act(() => root.unmount())
  })

  // A failed refresh keeps the last answer on screen, says it may be old, and
  // stops it seeding a form; a read hands its cancellation to fetch.
  it('says a shown answer may be old when its refresh failed, and reads cancellably', async () => {
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    let fail = false
    const inits: (RequestInit | undefined)[] = []
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      inits.push(init)
      return fail ? new Response(JSON.stringify({ error: 'down' }), { status: 400 }) : new Response('{"n":1}')
    }))
    let seen: ReturnType<typeof useGet<{ n: number }>> | null = null
    function Probe() { seen = useGet<{ n: number }>('/thing'); return null }
    await act(async () => { root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>) })
    await vi.waitFor(() => expect(seen!.current).toBe(true))
    expect(inits[0]?.signal).toBeInstanceOf(AbortSignal)
    fail = true
    await act(async () => { await seen!.reload() })
    await vi.waitFor(() => expect(seen!.refreshFailed).toBe(true))
    expect(seen!.data).toEqual({ n: 1 })
    expect(seen!.err).toBe('down (showing the last answer read)')
    expect(seen!.current).toBe(false)
    act(() => root.unmount())
  })
})

describe('what an act refreshes', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // An act refreshes the families it wrote to, and a step's family reaches
  // its change; reads of other families on the page are left alone.
  it('refreshes the families an act wrote to and the ones they reach', async () => {
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    const reads: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (!init?.method || init.method === 'GET') reads.push(url.replace('/api/v1', ''))
      return new Response('{"n":1}')
    }))
    function Reader({ path }: { path: string }) { const { data } = useGet<{ n: number }>(path); return <span>{data ? 'y' : 'n'}</span> }
    let run: ReturnType<typeof useAction>['run'] | null = null
    function Actor() { run = useAction().run; return null }
    await act(async () => {
      root.render(<QueryClientProvider client={client}><Reader path="/changes/5" /><Reader path="/guidance/blocks" /><Actor /></QueryClientProvider>)
    })
    await vi.waitFor(() => expect(el.textContent).toBe('yy'))
    reads.length = 0
    await act(async () => { await run!(() => post('/steps/12/approve', { decision: 'approve' })) })
    await vi.waitFor(() => expect(reads).toEqual(['/changes/5']))
    act(() => root.unmount())
  })
})

describe('a revisit', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // Coming back to a page shows what was held at once and reads again; the
  // held answer is not current, so no form seeds from it until the new
  // answer arrives.
  it('shows the held answer at once, reads again, and is current only after its own read', async () => {
    const client = newQueryClient()
    let answer = 1
    let release: (() => void) | null = null
    vi.stubGlobal('fetch', vi.fn(async () => {
      if (answer > 1) await new Promise<void>((r) => { release = r })
      return new Response(JSON.stringify({ n: answer }))
    }))
    const seen: { n: number | null; current: boolean }[] = []
    function Page() { const q = useGet<{ n: number }>('/projects/shop'); seen.push({ n: q.data?.n ?? null, current: q.current }); return null }
    const first = createRoot(document.createElement('div'))
    await act(async () => { first.render(<QueryClientProvider client={client}><Page /></QueryClientProvider>) })
    await vi.waitFor(() => expect(seen.at(-1)).toEqual({ n: 1, current: true }))
    act(() => first.unmount())
    answer = 2
    seen.length = 0
    const again = createRoot(document.createElement('div'))
    await act(async () => { again.render(<QueryClientProvider client={client}><Page /></QueryClientProvider>) })
    expect(seen[0]).toEqual({ n: 1, current: false })
    await vi.waitFor(() => expect(release).not.toBeNull())
    await act(async () => { release!() })
    await vi.waitFor(() => expect(seen.at(-1)).toEqual({ n: 2, current: true }))
    act(() => again.unmount())
  })
})

describe('another identity on screen', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // A reader already showing one person's answer shows the next person's
  // after the identity changes, never the first's (the independent
  // reviewer's review of 28 September reproduced the old answer staying on screen).
  it('replaces answers already on screen', async () => {
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    let who = 'alice'
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ who }))))
    function Show() { const q = useGet<{ who: string }>('/projects/private'); return <span>{q.data?.who ?? 'loading'}</span> }
    await act(async () => { root.render(<QueryClientProvider client={client}><Show /></QueryClientProvider>) })
    await vi.waitFor(() => expect(el.textContent).toBe('alice'))
    who = 'bob'
    await act(async () => { identityChanged(client) })
    await vi.waitFor(() => expect(el.textContent).toBe('bob'))
    act(() => root.unmount())
  })
})

// The app remounts its pages for a new identity before the reset runs, so a
// new reader first sees the old answer count; its first answer for the new
// identity must still be current, or an editor would never open (the
// independent reviewer reproduced that ordering on 28 September).
it('lets a reader remounted for a new identity call its first answer current', async () => {
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  let who = 'alice'
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ who }))))
  function Reader() { const q = useGet<{ who: string }>('/projects/private'); return <span>{q.data?.who ?? '-'} {q.current ? 'current' : 'held'}</span> }
  function Page({ identity }: { identity: string }) {
    const client = useQueryClient()
    const last = useRef(identity)
    useEffect(() => {
      if (last.current !== identity) identityChanged(client)
      last.current = identity
    }, [identity, client])
    return <Reader key={identity} />
  }
  await act(async () => { root.render(<QueryClientProvider client={client}><Page identity="alice" /></QueryClientProvider>) })
  await vi.waitFor(() => expect(el.textContent).toBe('alice current'))
  who = 'bob'
  await act(async () => { root.render(<QueryClientProvider client={client}><Page identity="bob" /></QueryClientProvider>) })
  await vi.waitFor(() => expect(el.textContent).toBe('bob current'))
  act(() => root.unmount())
  vi.unstubAllGlobals()
})
