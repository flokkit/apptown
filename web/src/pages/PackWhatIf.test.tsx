// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../data'
import { settled } from '../settled'
import { WhatIfPanel } from './PackWhatIf'

// Record ids are UUIDv7 strings.
const WHAT_IF = '00000000-0007-7000-8000-000000000000'
const NEW_WHAT_IF = '00000000-0009-7000-8000-000000000000'
const LEDGER = '00000000-0001-7000-8000-000000000000'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// The what-if panel runs on the eligible projects by default, names them,
// and says nothing is there only after a read that answered: a failed read
// is unknown, not empty. The picker still reaches projects on the pack at
// any version and projects not on it. The newest what-if shows as its
// counts and a link to its page.
describe('what-if panel', () => {
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

  // mount shows the panel; its reads are still on their way.
  const mount = async (answer: (path: string, init?: RequestInit) => Response) => {
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => answer(String(url), init)))
    const client = newQueryClient()
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => {
      root.render(<QueryClientProvider client={client}><MemoryRouter><WhatIfPanel pack="team" version={2} /></MemoryRouter></QueryClientProvider>)
    })
    return { el, client, done: () => { act(() => root.unmount()); client.clear() } }
  }
  // render shows the panel once its reads have answered.
  const render = async (answer: (path: string, init?: RequestInit) => Response) => {
    const m = await mount(answer)
    await settled(m.client)
    return m
  }
  const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status })
  const fleet = { packs: [{ pack: 'team', enabled: false, recommended: 1, versions: [{ version: 1, projects: ['ledger'], at: '' }] }] }
  const button = (el: HTMLElement, text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent?.startsWith(text)) as HTMLButtonElement

  it('runs on the eligible projects without naming them', async () => {
    const posted: unknown[] = []
    const { el, client, done } = await render((p, init) => {
      if (init?.method === 'POST') { posted.push(JSON.parse(String(init.body))); return json({ id: NEW_WHAT_IF }, 202) }
      return json({ whatIf: null, eligible: ['ledger', 'till'] })
    })
    expect(el.textContent).toContain('Eligible (on an earlier version):ledgertill')
    expect(el.textContent).toContain('None run on this version.')
    const run = button(el, 'Run on')
    expect(run.textContent).toBe('Run on 2 eligible projects')
    await act(async () => { run.click() })
    await settled(client)
    expect(posted).toEqual([{ pack: 'team', version: 2, rubrics: false, spendLimitMicros: 0, advanceWorkflows: false }])
    done()
  })

  // "Each project's own pack at its newest": the tick lists the explicit
  // versions AppTown offers, the eligible list is asked again with them (a
  // project current on this pack but behind on its own is counted), and the
  // run sends them as they are.
  it('moves each project\'s own pack to its newest with this one', async () => {
    const posted: unknown[] = []
    const asked: string[] = []
    const { el, client, done } = await render((p, init) => {
      if (init?.method === 'POST') { posted.push(JSON.parse(String(init.body))); return json({ id: NEW_WHAT_IF }, 202) }
      asked.push(p)
      return p.includes('also=')
        ? json({ whatIf: null, eligible: ['ledger', 'till'], ownPacksNewest: [] })
        : json({ whatIf: null, eligible: [], ownPacksNewest: [{ pack: 'rails', version: 38 }, { pack: 'django-htmx', version: 43 }] })
    })
    expect(button(el, 'Run on').disabled).toBe(true)
    expect(el.textContent).toContain('rails v38, django-htmx v43: for projects already on them')
    const tick = [...el.querySelectorAll('input[type=checkbox]')].find((i) => i.parentElement?.textContent?.includes("Also move each project's own pack")) as HTMLInputElement
    await act(async () => { tick.click() })
    await settled(client)
    expect(asked.some((p) => p.includes('also=' + encodeURIComponent('rails@38,django-htmx@43')))).toBe(true)
    const run = button(el, 'Run on')
    expect(run.textContent).toBe('Run on 2 eligible projects')
    await act(async () => { run.click() })
    await settled(client)
    expect(posted).toEqual([{ pack: 'team', version: 2, rubrics: false, spendLimitMicros: 0, advanceWorkflows: false, also: [{ pack: 'rails', version: 38 }, { pack: 'django-htmx', version: 43 }] }])
    done()
  })

  // Rubrics on the eligible default: the calls line counts the eligible
  // projects, and the run sends the limit in micro-dollars without naming
  // projects.
  it('runs on the eligible projects with rubrics under a limit', async () => {
    const posted: unknown[] = []
    const { el, client, done } = await render((p, init) => {
      if (init?.method === 'POST') { posted.push(JSON.parse(String(init.body))); return json({ id: NEW_WHAT_IF }, 202) }
      return json({ whatIf: null, eligible: ['ledger', 'till', 'shop'] })
    })
    const rubrics = [...el.querySelectorAll('input[type=checkbox]')].find((i) => i.parentElement?.textContent?.includes('Include model rubrics')) as HTMLInputElement
    await act(async () => { rubrics.click() })
    expect(el.textContent).toContain('3 projects, up to 6 calls')
    const limit = el.querySelector('input[aria-label="spend limit in dollars"]') as HTMLInputElement
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(limit, '2.50')
      limit.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => { button(el, 'Run on').click() })
    await settled(client)
    expect(posted).toEqual([{ pack: 'team', version: 2, rubrics: true, spendLimitMicros: 2500000, advanceWorkflows: false }])
    done()
  })

  it('cannot run when no project is eligible', async () => {
    const { el, done } = await render(() => json({ whatIf: null, eligible: [] }))
    expect(el.textContent).toContain('Eligible (on an earlier version):none')
    expect(button(el, 'Run on').disabled).toBe(true)
    done()
  })

  it('says unknown, not none, when its read fails', async () => {
    // A poll does not retry: one failed answer is the panel's state.
    const { el, done } = await render(() => json({ error: 'database unavailable' }, 500))
    expect(el.textContent).toContain('database unavailable')
    expect(el.textContent).toContain('could not be read')
    expect(el.textContent).not.toContain('None run on this version')
    expect(el.textContent).not.toContain('none')
    done()
  })

  // The newest what-if is its counts by answer and a link to its page; one
  // waiting for the queue holds the start.
  it('shows the newest what-if as counts and holds the start while it waits', async () => {
    const whatIf = { id: WHAT_IF, pack: 'team', version: 2, requestedBy: 'bls', state: 'queued', projectsTotal: 0, projectsDone: 0, createdAt: '2026-10-05T00:00:00Z', finishedAt: '', rows: [],
      projects: [{ projectId: LEDGER, projectSlug: 'ledger', outcome: 'not-evaluated', newFailures: [], notAssessed: [], fixed: 0 }] }
    const { el, done } = await render(() => json({ whatIf, eligible: ['ledger'] }))
    expect(el.textContent).toContain('queued')
    expect(el.textContent).toContain('1 not evaluated')
    expect(el.querySelector('a')?.getAttribute('href')).toBe(`/conformance/what-ifs/${WHAT_IF}`)
    expect(button(el, 'Run on').disabled).toBe(true)
    done()
  })

  // Choosing: projects on the pack at any version, a project not on it
  // added, and rubrics only with a spend limit; the run names them.
  it('runs on chosen projects, one not on the pack, with rubrics under a limit', async () => {
    const posted: unknown[] = []
    const { el, client, done } = await render((p, init) => {
      if (init?.method === 'POST') { posted.push(JSON.parse(String(init.body))); return json({ id: NEW_WHAT_IF }, 202) }
      if (p.includes('/wow')) return json(fleet)
      if (p.includes('/projects')) return json([{ slug: 'ledger', defaultBranch: 'main' }, { slug: 'shop', defaultBranch: 'main' }, { slug: 'empty', defaultBranch: '' }])
      return json({ whatIf: null, eligible: [] })
    })
    await act(async () => { button(el, 'choose projects').click() })
    await settled(client)
    const select = el.querySelector('select[aria-label="add a project not on this pack"]') as HTMLSelectElement
    expect([...select.options].map((o) => o.value)).toEqual(['', 'shop'])
    await act(async () => { select.value = 'shop'; select.dispatchEvent(new Event('change', { bubbles: true })) })
    const rubrics = [...el.querySelectorAll('input[type=checkbox]')].find((i) => i.parentElement?.textContent?.includes('Include model rubrics')) as HTMLInputElement
    await act(async () => { rubrics.click() })
    expect(el.textContent).toContain('2 projects, up to 4 calls')
    const run = button(el, 'Run on')
    expect(run.textContent).toBe('Run on 2 projects')
    await act(async () => { run.click() })
    await settled(client)
    expect(posted).toEqual([{ pack: 'team', version: 2, projects: ['ledger', 'shop'], rubrics: true, spendLimitMicros: 500000, advanceWorkflows: false }])
    done()
  })
})
