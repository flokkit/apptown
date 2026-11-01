// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import type { ConnectorEntry, IntegrationEntry, IntegrationHealth } from '../wire.gen'
import Integrations from './Integrations'
import { ConnectorForm } from './integrations/ConnectorForm'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

describe('connector form', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // A number field sends a JSON number, and nothing when it is empty: the
  // AppTown refuses "2" or "" where a model binding's concurrency belongs.
  it('sends a number field as a number, and leaves an empty one out', async () => {
    const sent: { config: Record<string, unknown> }[] = []
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      sent.push(JSON.parse(String(init?.body)))
      return new Response(JSON.stringify({ ok: true, health: { ok: true, detail: 'credentials resolved' } }))
    }))
    const spec = { capability: 'model', provider: 'pi', title: 'Model provider (pi)', description: '', multi: true, managed: false, icon: 'model', hasDocs: false,
      fields: [{ key: 'concurrency', label: 'Steps at once', type: 'number', placeholder: '8' }] }
    const el = document.createElement('div')
    const root = createRoot(el)
    await act(async () => { root.render(<QueryClientProvider client={newQueryClient()}><ConnectorForm spec={spec} icon="model" initialName="gpu" initialConfig={{}} onDone={() => {}} /></QueryClientProvider>) })
    const input = el.querySelector('input[type=number]') as HTMLInputElement
    const type = async (value: string) => act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const save = async () => act(async () => { [...el.querySelectorAll('button')].find((b) => b.textContent === 'Save')!.click() })
    await type('2')
    await save()
    expect(sent[0].config).toEqual({ concurrency: 2 })
    await type('')
    await save()
    expect(sent[1].config).toEqual({})
    act(() => root.unmount())
  })
})

// --- The list and one integration's page ---

// Readings AppTown took five minutes ago, by its own clock.
const fiveMinutesAgo = new Date(Date.now() - 5 * 60_000).toISOString()
const checked = (ok: boolean, detail: string): IntegrationHealth => ({ automatic:true,stateLabel: ok ? 'passed' : 'failed', ok, detail, checked: true, checkedAt: fiveMinutesAgo })
const binding = (name: string, capability: string, provider: string, health: IntegrationHealth, extra: Partial<IntegrationEntry> = {}): IntegrationEntry => ({
  id: `00000000-${String(name.length).padStart(4, '0')}-7000-8000-000000000000`, name, displayLabel: name, capability, provider, config: {}, version: 1, updatedAt: '2026-10-01T00:00:00Z', managed: false, configHash: 'abc123def456', usedBy: { projects: [], deployments: [] }, health, testable: false, ...extra,
})
const gitlab = binding('gitlab', 'scm', 'gitlab', checked(true, 'authenticated as forge-bot'), { managed: true, config: { tokenFrom: 'GITLAB_TOKEN', auth: 'PRIVATE-TOKEN' } })
const scanner = binding('django-check', 'scanner', 'tool', checked(false, 'no image configured'), {
  config: { description: 'Django deploy checks', image: '' },
  usedBy: { projects: [{ slug: 'shop', name: 'Shop', capability: 'scanner' }, { slug: 'blog', name: 'Blog', capability: 'scanner' }], deployments: [{ project: 'shop', name: 'prod', target: 'lan-host' }] },
})
const sso = binding('authentik', 'sso', 'service', { ok: false, detail: '', checked: false, automatic:false,stateLabel: 'unknown' }, { config: { url: 'https://auth.example' } })
// Checked, with nothing to add: working, whatever the detail says.
const quiet = binding('cache', 'registry', 'npm', checked(true, ''))
const spec = (capability: string, provider: string, title: string, extra: Partial<ConnectorEntry> = {}): ConnectorEntry => ({
  capability, provider, title, description: `${title}. More words.`, multi: true, managed: false, fields: [], hasDocs: false, ...extra,
})
const catalog = [spec('scm', 'gitlab', 'GitLab', { multi: false, managed: true }), spec('scanner', 'tool', 'Scanner (tool)'), spec('sso', 'service', 'SSO (OIDC)'),
  spec('errors', 'glitchtip', 'Error reporting (GlitchTip)')]

type Reply = (method: string, body: unknown) => Response | Promise<Response>
function stubAppTown(routes: Record<string, Reply>) {
  const calls: { method: string; path: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const path = url.replace(/^\/api\/v1/, '')
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ method, path, body })
    const r = routes[`${method} ${path}`]
    return r ? r(method, body) : new Response(JSON.stringify({ error: `no route ${method} ${path}` }), { status: 404 })
  }))
  return calls
}
const json = (v: unknown, status = 200) => () => new Response(JSON.stringify(v), { status })

function Where() { return <div data-testid="where">{useLocation().pathname}</div> }

async function mount(at: string, client: QueryClient = newQueryClient()) {
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[at]}>
          <Routes>
            <Route path="/integrations" element={<Integrations />} />
            <Route path="/integrations/:name" element={<Integrations />} />
          </Routes>
          <Where />
        </MemoryRouter>
      </QueryClientProvider>)
  })
  const text = () => el.textContent ?? ''
  const where = () => el.querySelector('[data-testid=where]')!.textContent
  const button = (label: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === label)
  const done = () => { act(() => root.unmount()); el.remove(); client.clear() }
  return { el, text, where, button, done }
}

describe('the integrations list', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // Each row says what the service is and whether it works; a service the
  // AppTown has no check for reads as unknown, never as working.
  it('groups services by kind and keeps working, failing and unchecked apart', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [gitlab, scanner, sso, quiet] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('gitlab'))
    const groups = [...page.el.querySelectorAll('.int-group')].map((g) => g.textContent)
    expect(groups[0]).toContain('Source control')
    expect(groups[1]).toContain('Scanners')
    const row = (name: string) => [...page.el.querySelectorAll('tr.int-row')].find((r) => r.textContent!.includes(name))!
    expect(row('gitlab').querySelector('.chip')!.className).toContain('pass')
    expect(row('gitlab').textContent).toContain('working')
    expect(row('django-check')).toBeUndefined()
    const collapsed = [...page.el.querySelectorAll('button')].find(b => b.textContent?.startsWith('Scanners ('))!
    expect(collapsed.getAttribute('aria-expanded')).toBe('false')
    expect(collapsed.textContent).toContain('Scanners (1) · 1 failed')
    await act(async () => { [...page.el.querySelectorAll('button')].find(b => b.textContent?.startsWith('Scanners ('))!.click() })
    expect(row('django-check').querySelector('.chip')!.className).toContain('fail')
    expect(row('django-check').textContent).not.toContain('Django deploy checks')
    expect(page.el.querySelector('th')?.textContent).toBe('Service')
    expect(row('authentik').querySelector('.chip')!.className).toBe('chip ')
    expect(row('authentik').textContent).toContain('no automatic check')
    expect(row('authentik').textContent).not.toContain('working')
    expect(row('authentik').textContent).toContain('no automatic check')
    expect(row('cache').textContent).toContain('working')
    expect(row('gitlab').textContent).toContain('5m ago')
    // What names each one, counted; nothing named reads as a dash, not zero.
    expect(row('django-check').querySelector('.int-usedby')).toBeNull()
    expect(row('gitlab').querySelector('.int-usedby')).toBeNull()
    // A kind the catalogue offers with nothing connected can be connected from its place.
    expect(row('Error reporting (GlitchTip)').textContent).toContain('not set up')
    expect(page.text()).toContain('4 connected · 1 need attention · 1 not tested')
    page.done()
  })

  it('says nothing is connected only when AppTown said so', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: null }), 'GET /integrations/catalog': json({ catalog: [] }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('No services are connected yet.'))
    page.done()
  })

  it('shows a failed read as a failure, not as an empty list', async () => {
    stubAppTown({ 'GET /integrations': json({ error: 'bad request' }, 400), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('The integrations could not be read: bad request'))
    expect(page.text()).not.toContain('No services are connected')
    page.done()
  })

  it('shows a refused read as not available, not as a failure or an empty list', async () => {
    stubAppTown({ 'GET /integrations': json({ error: 'forbidden' }, 403), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('Not available to you'))
    expect(page.el.querySelector('.error')).toBeNull()
    expect(page.text()).not.toContain('No services are connected')
    page.done()
  })

  // When checking again fails, the states shown are the last answer, and say so.
  it('marks every state as old when checking again fails', async () => {
    let fail = false
    stubAppTown({
      'GET /integrations': () => fail ? new Response(JSON.stringify({ error: 'AppTown unreachable' }), { status: 400 }) : new Response(JSON.stringify({ integrations: [gitlab] })),
      'GET /integrations/catalog': json({ catalog }),
    })
    const client = newQueryClient()
    const page = await mount('/integrations', client)
    await vi.waitFor(() => expect(page.text()).toContain('working'))
    fail = true
    await act(async () => { await client.invalidateQueries({ queryKey: getKey('/integrations') }) })
    await vi.waitFor(() => expect(page.text()).toContain('unknown'))
    expect(page.el.querySelector('tr.int-row .chip')!.className).toContain('unknown')
    expect(page.text()).toContain('unknown')
    expect(page.el.querySelector('.chip.unknown')?.getAttribute('aria-label')).toContain('(AppTown unreachable)')
    page.done()
  })

  it('opens an integration on its own page', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [gitlab, scanner] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('gitlab'))
    await act(async () => { ([...page.el.querySelectorAll('tr.int-row')].find((r) => r.textContent!.includes('gitlab')) as HTMLElement).click() })
    expect(page.where()).toBe('/integrations/gitlab')
    expect(page.el.querySelector('h1')!.textContent).toBe('gitlab')
    page.done()
  })
})

describe('one integration', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // The page names where each credential is read from, never a value, and
  // when the health it shows was read.
  it('shows its state, when it was checked, its credential sources and its settings', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [gitlab] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/gitlab')
    await vi.waitFor(() => expect(page.text()).toContain('authenticated as forge-bot'))
    expect(page.text()).toContain('checked 5m ago')
    const sections = [...page.el.querySelectorAll('section')]
    const credentials = sections.find((s) => s.querySelector('h2')?.textContent === 'Credentials')!
    expect(credentials.textContent).toContain('tokenFrom')
    expect(credentials.textContent).toContain('GITLAB_TOKEN')
    const config = sections.find((s) => s.querySelector('h2')?.textContent === 'Configuration')!
    expect(config.textContent).toContain('PRIVATE-TOKEN')
    expect(config.textContent).not.toContain('tokenFrom')
    expect(page.text()).toContain('abc123def456')
    // AppTown-managed: no removing it.
    expect(page.button('Remove')).toBeUndefined()
    page.done()
  })

  it('names the projects and deployments that use it, linked', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [scanner, gitlab] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/django-check')
    await vi.waitFor(() => expect(page.text()).toContain('Used by'))
    const used = [...page.el.querySelectorAll('section')].find((s) => s.querySelector('h2')?.textContent === 'Used by')!
    const links = [...used.querySelectorAll('a')].map((a) => [a.textContent, a.getAttribute('href')])
    expect(links).toEqual([['Shop', '/projects/shop'], ['Blog', '/projects/blog'], ['shop/prod', '/projects/shop/settings/deployments'], ['lan-host', '/infrastructure/lan-host']])
    page.done()
    const other = await mount('/integrations/gitlab')
    await vi.waitFor(() => expect(other.text()).toContain('Used by'))
    expect(other.text()).toContain('Old resources may still be kept')
    other.done()
  })

  // An unchecked reading says what is true of its kind: AI models are not
  // checked on every reading, but Test connection asks them; a kind with
  // neither says it cannot be checked.
  it('says an unchecked kind that Test connection asks can be asked', async () => {
    const llm = binding('openrouter', 'llm', 'openrouter', { ok: false, detail: '', checked: false, automatic:false,stateLabel: 'unknown' }, { testable: true })
    stubAppTown({ 'GET /integrations': json({ integrations: [llm, sso] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/openrouter')
    await vi.waitFor(() => expect(page.text()).toContain('Test connection asks it now'))
    expect(page.text()).not.toContain('no live check')
    page.done()
    const other = await mount('/integrations/authentik')
    await vi.waitFor(() => expect(other.text()).toContain('no live check for this kind'))
    other.done()
  })

  it('tests the saved connection, shows the answer and refreshes shared health', async () => {
    const calls = stubAppTown({
      'GET /integrations': json({ integrations: [scanner] }), 'GET /integrations/catalog': json({ catalog }),
      'POST /integrations/django-check/test': json(checked(true, 'image pulled')),
    })
    const page = await mount('/integrations/django-check')
    await vi.waitFor(() => expect(page.button('Test connection')).toBeDefined())
    await act(async () => { page.button('Test connection')!.click() })
    await vi.waitFor(() => expect(page.text()).toContain('working'))
    expect(page.text()).toContain('image pulled')
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({})
    expect(calls.filter(c => c.path === '/integrations' && c.method === 'GET').length).toBeGreaterThan(1)
    page.done()
  })

  it('removes it after asking, and returns to the list', async () => {
    let gone = false
    const calls = stubAppTown({
      'GET /integrations': () => new Response(JSON.stringify({ integrations: gone ? [] : [scanner] })),
      'GET /integrations/catalog': json({ catalog }),
      'DELETE /integrations/django-check': () => { gone = true; return new Response('{"ok":true}') },
    })
    const page = await mount('/integrations/django-check')
    await vi.waitFor(() => expect(page.button('Remove')).toBeDefined())
    await act(async () => { page.button('Remove')!.click() })
    expect(page.text()).toContain('Remove “django-check”?')
    await act(async () => { page.button('Yes')!.click() })
    await vi.waitFor(() => expect(page.where()).toBe('/integrations'))
    expect(calls.some((c) => c.method === 'DELETE' && c.path === '/integrations/django-check')).toBe(true)
    page.done()
  })

  it('says so when no integration has the name', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [gitlab] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/nope')
    await vi.waitFor(() => expect(page.text()).toContain('No integration is called “nope”'))
    page.done()
  })
})

it.each([false, true])('keeps an unconfigured integration neutral and out of connected and attention counts (phone=%s)', async narrow => {
  vi.stubGlobal('matchMedia', () => ({ matches: narrow, addEventListener() {}, removeEventListener() {} }))
  stubAppTown({ 'GET /integrations': json({ integrations: [{ ...gitlab, health: { checked: false, ok: false, detail: 'Set credentials', automatic:true,stateLabel: 'not set up' } }] }), 'GET /integrations/catalog': json({ catalog }) })
  const page = await mount('/integrations')
  try {
    await vi.waitFor(() => expect(page.text()).toContain('not set up'))
    expect(page.el.querySelector('.int-summary')?.textContent).not.toContain('need attention')
    expect(page.el.querySelector('.int-summary')?.textContent).toContain('0 connected')
    expect(page.el.querySelector('tr.int-row')?.textContent).not.toContain('not checked yet')
    expect(page.el.querySelector('tr.int-row .chip [aria-hidden]')).toBeNull()
    expect(page.el.querySelector('tr.int-row .chip.fail')).toBeNull()
  } finally { page.done(); vi.unstubAllGlobals() }
})

it('keeps a last check age and result visible even when it failed or became old', async () => {
 const {lastChecked}=await import('./Integrations')
 const now=Date.parse('2026-11-01T00:00:00Z')
 expect(lastChecked({automatic:true,stateLabel:'passed',ok:true,checked:true,checkedAt:'2026-10-31T23:55:00Z',detail:''},now)).toBe('checked 5m ago')
 expect(lastChecked({automatic:true,stateLabel:'failed',ok:false,checked:true,stale:true,checkedAt:'2026-10-31T23:30:00Z',detail:'credentials refused'},now)).toBe('last seen 30m ago')
})


it('formats the age of an expired reading from the owner classification', async () => {
 const {lastChecked}=await import('./Integrations')
 expect(lastChecked({automatic:true,stateLabel:'unknown',stale:true,lastResult:'failed',ok:false,checked:true,checkedAt:'2026-10-31T12:00:00Z',detail:'Reading expired.'},Date.parse('2026-11-01T00:00:00Z'))).toBe('last seen 12h ago')
})


it('shows an event time when a server check is slightly ahead of the reader clock', async () => {
 const {lastChecked}=await import('./Integrations')
 const when=new Date(2026,9,30,17,26).toISOString()
 expect(lastChecked({automatic:true,stateLabel:'passed',ok:true,checked:true,checkedAt:when,detail:''},Date.parse(when)-1000)).toBe('checked just now')
})

it('uses owner freshness even when the browser clock is far ahead', async () => {
 const {healthState,healthIsOld}=await import('./integrations/kinds')
 const h=checked(true,'Connection answered')
 const future=Date.parse(fiveMinutesAgo)+24*3600000
 expect(healthState(h,future)).toBe('passed')
 expect(healthIsOld(h,future)).toBe(false)
 expect(healthIsOld({...h,stale:true},0)).toBe(true)
})

it('shows a manual-only integration neutrally and shows an old test age once', async () => {
 const old={...sso,id:'old-sso',name:'old-sso',health:{...sso.health,checked:true,stateLabel:'passed' as const,ok:true,stale:true,checkedAt:fiveMinutesAgo}}
 stubAppTown({'GET /integrations':json({integrations:[sso,old]}),'GET /integrations/catalog':json({catalog})})
 const page=await mount('/integrations')
 try {
  await vi.waitFor(()=>expect(page.el.querySelectorAll('tr.int-row:not(.int-unbound)')).toHaveLength(2))
  const rows=page.el.querySelectorAll('tr.int-row:not(.int-unbound)')
  expect(rows[0].querySelector('.chip')?.textContent).toBe('no automatic check')
  expect(rows[0].querySelector('.chip')?.className).toBe('chip ')
  expect(rows[1].querySelector('.chip')?.textContent).toContain('old · working')
  expect(rows[1].textContent?.match(/last seen/g)).toHaveLength(1)
 } finally {page.done()}
})
