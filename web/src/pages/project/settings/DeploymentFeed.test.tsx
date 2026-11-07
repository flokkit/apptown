// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../../data'
import type { DeploymentView } from '../../../wire.gen'
import { DeploymentFeed, feedWords } from './DeploymentFeed'
import { DeploymentRow, EnvCard } from './Deployments'
import { renderToStaticMarkup } from 'react-dom/server'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

it('words each feed and keeps promotion actions off list rows', () => {
  expect(feedWords({ feed: 'main', promotionSource: '' })).toBe('Follows main')
  expect(feedWords({ feed: 'manual', promotionSource: '' })).toBe('By hand')
  const client = newQueryClient()
  const e = { status: { word: 'unknown', reason: '', stale: false }, id: 'prod', name: 'production', kind: 'standing', feed: 'promotion', promotionSource: 'Staging', promotionBlocked: 'Source runs nothing. Deploy to Staging first.', target: { kind: 'docker-host' } } as DeploymentView
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><DeploymentRow slug="shop" env={e} /></MemoryRouter></QueryClientProvider>)
  const el = document.createElement('div')
  el.innerHTML = html
  expect(el.textContent).toContain('Promoted from Staging')
  expect(el.textContent).toContain(e.promotionBlocked)
  expect(el.querySelectorAll('button')).toHaveLength(0)
  expect(el.textContent).not.toContain('Deploy latest')
  client.clear()
})

it.each(['card'])('names the source last successful release on the Promote %s control', view => {
  const client = newQueryClient()
  const e = { status: { word: 'unknown', reason: '', stale: false }, id: 'prod', name: 'production', kind: 'standing', feed: 'promotion', promotionSource: 'Staging', promotionRelease: 17, target: { kind: 'docker-host' } } as DeploymentView
  const el = document.createElement('div')
  el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>{view === 'row' ? <DeploymentRow slug="shop" env={e} /> : <EnvCard slug="shop" env={e} onChanged={() => {}} />}</MemoryRouter></QueryClientProvider>)
  const button = [...el.querySelectorAll('button')].find(b => b.textContent === 'Promote release 17')
  expect(button).toBeDefined()
  expect(button?.title).toBe('Deploys release 17, the release Staging last deployed successfully.')
  expect(button?.disabled).toBe(false)
  client.clear()
})

it.each(['card'])('binds the Promote %s request to the displayed bundle and shows a stale refusal', async view => {
  const bundle = '00000000-0000-4000-8000-000000000017'
  const sent: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    if (init?.method === 'POST') {
      sent.push(JSON.parse(String(init.body)))
      return new Response('{"error":"staging now runs release 18; promote again"}', { status: 409 })
    }
    return new Response('{"error":"not available in this test"}', { status: 403 })
  }))
  const e = { status: { word: 'unknown', reason: '', stale: false }, id: 'prod', name: 'production', kind: 'standing', feed: 'promotion', promotionSource: 'Staging', promotionRelease: 17, promotionBundleId: bundle, target: { kind: 'docker-host' } } as DeploymentView
  const el = document.createElement('div')
  const root = createRoot(el)
  const client = newQueryClient()
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter>{view === 'row' ? <DeploymentRow slug="shop" env={e} /> : <EnvCard slug="shop" env={e} onChanged={() => {}} />}</MemoryRouter></QueryClientProvider>))
    await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Promote release 17')!.click() })
    expect(sent).toEqual([{ expectedSourceBundleId: bundle }])
    await vi.waitFor(() => expect(el.textContent).toContain('staging now runs release 18; promote again'))
  } finally {
    await act(async () => root.unmount())
    client.clear(); vi.unstubAllGlobals()
  }
})

it('keeps the draft and starting revision through refresh and failed saves', async () => {
  const path = '/projects/shop/deployments'
  let current = [{ id: 'prod', name: 'production', kind: 'standing', feed: 'manual', feedRevision: 4 }, { id: 'stage', name: 'staging', displayLabel: 'Staging', kind: 'standing', feed: 'main', feedRevision: 1 }]
  const sent: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    if (_url === '/api/v1/authz/roles') return new Response(JSON.stringify({ roles: [{ name: 'release-manager', title: 'Release manager', scope: 'project' }], capabilities: {} }))
    if (init?.method === 'PUT') {
      sent.push(JSON.parse(String(init.body)))
      return new Response('{"error":"deployment feed changed; read its settings again"}', { status: 409 })
    }
    return new Response(JSON.stringify(current))
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
  try {
    await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter><DeploymentFeed slug="shop" name="production" /></MemoryRouter></QueryClientProvider>) })
    await act(async () => { await client.refetchQueries({ queryKey: getKey(path) }); await new Promise(resolve => setTimeout(resolve, 0)) })
    await change('Release feed', 'promotion')
    await change('Source deployment', 'staging')
    current = [{ ...current[0], feed: 'main', feedRevision: 5 }, current[1]]
    await act(async () => { await client.refetchQueries({ queryKey: getKey(path) }); await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(el.querySelector<HTMLSelectElement>('[aria-label="Release feed"]')!.value).toBe('promotion')
    expect(el.textContent).toContain('The settings changed while you were editing')
    expect([...el.querySelectorAll('button')].find(b => b.textContent === 'Save settings')?.disabled).toBe(true)
    await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Keep my edits on the current settings')!.click() })
    await act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    await vi.waitFor(() => expect(sent).toEqual([{ revision: 5, feed: 'promotion', from: 'staging', approvalRole: '' }]))
    expect(el.querySelector<HTMLSelectElement>('[aria-label="Source deployment"]')!.value).toBe('staging')
    await vi.waitFor(() => expect([...el.querySelectorAll('button')].find(b => b.textContent === 'Discard changes')?.disabled).toBe(false))
    await act(async () => { [...el.querySelectorAll('button')].find(b => b.textContent === 'Discard changes')!.click() })
    await vi.waitFor(() => expect(el.querySelector<HTMLSelectElement>('[aria-label="Release feed"]')!.value).toBe('main'))
  } finally {
    await act(async () => root.unmount())
    client.clear(); el.remove(); vi.unstubAllGlobals()
  }
})

it('applies saved feed settings without replacing runtime facts with the settings response', async () => {
  const path = '/projects/shop/deployments'
  const deployments = [{ id: 'prod', name: 'production', kind: 'standing', feed: 'promotion', feedRevision: 7, promotedFromId: 'stage', promotionSource: 'Staging', readiness: 'reconciled', url: 'https://current.example' }, { id: 'stage', name: 'staging', displayLabel: 'Staging', kind: 'standing', feed: 'main', feedRevision: 1 }]
  let saving = false
  let releaseRead: (() => void) | undefined
  vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
    if (_url === '/api/v1/authz/roles') return new Response(JSON.stringify({ roles: [{ name: 'release-manager', title: 'Release manager', scope: 'project' }], capabilities: {} }))
    if (init?.method === 'PUT') {
      saving = true
      return new Response(JSON.stringify({ feed: 'manual', feedRevision: 8, feedConfirmed: false, readiness: 'uncertain', url: 'https://older.example' }))
    }
    if (saving) await new Promise<void>(resolve => { releaseRead = resolve })
    return new Response(JSON.stringify(deployments))
  }))
  const el = document.createElement('div')
  const root = createRoot(el)
  const client = newQueryClient()
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><DeploymentFeed slug="shop" name="production" /></MemoryRouter></QueryClientProvider>))
    await act(async () => { await client.refetchQueries({ queryKey: getKey(path) }); await new Promise(resolve => setTimeout(resolve, 0)) })
    await act(async () => {
      const select = el.querySelector<HTMLSelectElement>('[aria-label="Release feed"]')!
      select.value = 'manual'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await act(async () => { el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    await vi.waitFor(() => expect(client.getQueryData<DeploymentView[]>(getKey(path))?.[0].feedRevision).toBe(8))
    const saved = client.getQueryData<DeploymentView[]>(getKey(path))![0]
    expect(saved.feed).toBe('manual')
    expect(saved.promotedFromId).toBeUndefined()
    expect(saved.readiness).toBe('reconciled')
    expect(saved.url).toBe('https://current.example')
  } finally {
    await act(async () => { releaseRead?.(); root.unmount() })
    client.clear(); vi.unstubAllGlobals()
  }
})

// Serving evidence belongs to this deployment, independently of its feed or source.
it.each(['main', 'manual', 'promotion'].flatMap(feed => ['row', 'card'].map(view => [feed, view])))('shows the own serving release for a confirmed %s feed in the %s', (feed, view) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/shop/releases'), [
    { deployment: 'production', number: 8, serving: false, state: 'failed' },
    { deployment: 'staging', number: 17, serving: true },
    { deployment: 'production', number: 7, serving: true },
  ])
  const e = { id: 'prod', name: 'production', kind: 'standing', feed, status: { word: 'working', reason: '', stale: false }, observation: 'running', servingRelease: { number: 7, releasedAt: '2026-11-06T12:00:00Z' }, feedConfirmed: true, readiness: 'reconciled', promotionSource: 'Staging', promotionRelease: 17 } as DeploymentView
  {
    const el = document.createElement('div')
    el.innerHTML = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>{view === 'row' ? <DeploymentRow slug="shop" env={e} /> : <EnvCard slug="shop" env={e} onChanged={() => {}} />}</MemoryRouter></QueryClientProvider>)
    expect(el.textContent).toContain('Running release 7')
    expect(el.textContent).not.toContain('No serving release recorded')
    expect(el.textContent).not.toContain('Confirm follows main')
    expect(el.textContent).not.toContain('Automatic deploys are paused')
    expect(el.textContent).not.toContain('Running release 17')
  }
  client.clear()
})

it('allows saving an unchanged unconfirmed follows-main setting and applies its confirmation', async () => {
  const path = '/projects/shop/deployments'
  let deployment = { id: 'prod', name: 'production', kind: 'standing', feed: 'main', feedRevision: 4, feedConfirmed: false }
  const sent: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith('/authz/roles')) return new Response(JSON.stringify({ roles: [], capabilities: {} }))
    if (init?.method === 'PUT') {
      sent.push(JSON.parse(String(init.body)))
      deployment = { ...deployment, feedConfirmed: true, feedRevision: 5 }
      return new Response(JSON.stringify(deployment))
    }
    return new Response(JSON.stringify([deployment]))
  }))
  const el = document.createElement('div')
  const root = createRoot(el)
  const client = newQueryClient()
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><DeploymentFeed slug="shop" name="production" /></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('button[type="submit"]')).not.toBeNull())
    const save = el.querySelector<HTMLButtonElement>('button[type="submit"]')!
    expect(save.disabled).toBe(false)
    await act(async () => el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    expect(sent).toEqual([{ revision: 4, feed: 'main', from: '', approvalRole: '' }])
    expect(client.getQueryData<DeploymentView[]>(getKey(path))?.[0].feedConfirmed).toBe(true)
  } finally {
    await act(async () => root.unmount())
    client.clear(); vi.unstubAllGlobals()
  }
})

it.each(['row', 'card'])('reports no serving release only for this deployment in the %s', view => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/shop/releases'), [{ deployment: 'staging', number: 17, serving: true }, { deployment: 'production', number: 8, serving: false }])
  const e = { name: 'production', kind: 'standing', feed: 'main', feedConfirmed: true, status: { word: 'stopped', reason: '', stale: false }, observation: 'stopped', servingRelease: null } as DeploymentView
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>{view === 'row' ? <DeploymentRow slug="shop" env={e} /> : <EnvCard slug="shop" env={e} onChanged={() => {}} />}</MemoryRouter></QueryClientProvider>)
  expect(html).toContain('No serving release recorded')
  expect(html).not.toContain('Running release')
  client.clear()
})


// A successful history read can carry an unknown observation.
it.each(['row', 'card'])('preserves unknown serving evidence in the %s', view => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/shop/releases'), [
    { deployment: 'production', number: 7, state: 'released', serving: false, observed: 'unknown', reason: 'host observation unavailable' },
  ])
  const e = { name: 'production', kind: 'standing', feed: 'main', feedConfirmed: true, status: { word: 'unknown', reason: 'host observation unavailable', stale: false }, observation: 'unknown', observationIssue: 'host observation unavailable', servingRelease: { number: 7 } } as DeploymentView
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>{view === 'row' ? <DeploymentRow slug="shop" env={e} /> : <EnvCard slug="shop" env={e} onChanged={() => {}} />}</MemoryRouter></QueryClientProvider>)
  client.clear()
  expect(html).not.toContain('No serving release recorded')
  expect(html).toContain('unknown')
  expect(html).toContain('host observation unavailable')
})

// ListReleases returns only the newest 50 project releases. Production release
// 7 still serves, but staging releases 8..57 fill that entire response.
it.each(['row', 'card'])('does not infer absence from truncated history in the %s', view => {
  const client = newQueryClient()
  client.setQueryData(getKey('/projects/shop/releases'), Array.from({ length: 50 }, (_, i) => (
    { deployment: 'staging', number: 57 - i, state: 'released', serving: i === 0 }
  )))
  const e = { name: 'production', kind: 'standing', feed: 'promotion', feedConfirmed: false, status: { word: 'working', reason: '', stale: false }, observation: 'running', servingRelease: { number: 7, releasedAt: '2026-11-06T12:00:00Z' } } as DeploymentView
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>{view === 'row' ? <DeploymentRow slug="shop" env={e} /> : <EnvCard slug="shop" env={e} onChanged={() => {}} />}</MemoryRouter></QueryClientProvider>)
  client.clear()
  expect(html).not.toContain('No serving release recorded')
  expect(html).toContain('Running release 7')
  expect(html).toContain('/projects/shop/releases/7')
})


it.each(['row', 'card'].flatMap(view => ['unknown', 'unreachable', 'stale'].map(state => [view, state])))('keeps %s serving evidence unknown when runtime is %s', (view, state) => {
  const client = newQueryClient()
  const e = { name: 'production', kind: 'standing', status: { word: 'unknown', reason: '', stale: true }, observation: state === 'stale' ? 'running' : state, observationStale: state === 'stale', servingRelease: null } as DeploymentView
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>{view === 'row' ? <DeploymentRow slug="shop" env={e} /> : <EnvCard slug="shop" env={e} onChanged={() => {}} />}</MemoryRouter></QueryClientProvider>)
  expect(html).toContain('Serving release unknown')
  expect(html).not.toContain('No serving release recorded')
  client.clear()
})

it.each(['row', 'card'])('keeps the recorded release when stopped in the %s', view => {
  const client = newQueryClient()
  const e = { name: 'production', kind: 'standing', status: { word: 'stopped', reason: '', stale: false }, observation: 'stopped', servingRelease: { number: 7, releasedAt: '2026-11-06T12:00:00Z' } } as DeploymentView
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter>{view === 'row' ? <DeploymentRow slug="shop" env={e} /> : <EnvCard slug="shop" env={e} onChanged={() => {}} />}</MemoryRouter></QueryClientProvider>)
  expect(html).toContain('Recorded release 7')
  expect(html).not.toContain('Running release')
  expect(html).not.toContain('No serving release recorded')
  client.clear()
})
