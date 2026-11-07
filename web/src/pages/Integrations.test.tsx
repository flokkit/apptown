// @ts-expect-error -- this package has no Node types; only this read is used.
import { readdirSync } from 'node:fs'
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import type { ConnectorEntry, IntegrationEntry, IntegrationHealth } from '../wire.gen'
import Integrations from './Integrations'
import { TechnicalDetailSwitch } from './change/TechnicalDetail'
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
const checked = (ok: boolean, detail: string): IntegrationHealth => ({ freshnessHelp: 'Connection readings are marked old after 75 minutes.', automatic:true,stateLabel: ok ? 'passed' : 'failed', ok, detail, checked: true, checkedAt: fiveMinutesAgo })
const binding = (name: string, capability: string, provider: string, health: IntegrationHealth, extra: Partial<IntegrationEntry> = {}): IntegrationEntry => ({
  id: `00000000-${String(name.length).padStart(4, '0')}-7000-8000-000000000000`, name, displayLabel: name, capability, provider, config: {}, version: 1, updatedAt: '2026-10-01T00:00:00Z', managed: false, configHash: 'abc123def456', usedBy: { projects: [], deployments: [] }, health, testable: false, ...extra,
})
const gitlab = binding('gitlab', 'scm', 'gitlab', checked(true, 'authenticated as forge-bot'), { managed: true, config: { tokenFrom: 'GITLAB_TOKEN', auth: 'PRIVATE-TOKEN' } })
const scanner = binding('django-check', 'scanner', 'tool', checked(false, 'no image configured'), {
  config: { description: 'Django deploy checks', image: '' },
  usedBy: { projects: [{ slug: 'shop', name: 'Shop', capability: 'scanner' }, { slug: 'blog', name: 'Blog', capability: 'scanner' }], deployments: [{ project: 'shop', name: 'prod', target: 'lan-host' }] },
})
const sso = binding('authentik', 'sso', 'service', { freshnessHelp: 'Connection readings are marked old after 75 minutes.', ok: false, detail: '', checked: false, automatic:false,stateLabel: 'unknown' }, { config: { url: 'https://auth.example' } })
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
            <Route path="/services" element={<Integrations />} />
            <Route path="/services/:name" element={<Integrations />} />
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

  it('shows credential users from existing integration reads and keeps Add in the section heading', async () => {
    const model = binding('models', 'model', 'pi', checked(true, ''), {
      displayLabel: 'Model account', config: { secretEnv: ['GITLAB_TOKEN'], endpoint: 'UNREFERENCED' },
    })
    const custom = binding('custom', 'sso', 'service', checked(true, ''), { config: { password: 'CUSTOM_KEY' } })
    const calls = stubAppTown({
      'GET /integrations': json({ integrations: [gitlab, model, custom] }),
      'GET /integrations/catalog': json({ catalog: [...catalog.filter((c) => c.capability !== 'sso'), spec('sso', 'service', 'SSO', {
        fields: [{ key: 'password', label: 'Password', type: 'secretRef' }],
      })] }),
      'GET /secrets/names': json({ names: ['GITLAB_TOKEN', 'CUSTOM_KEY', 'UNREFERENCED'], stores: {
        GITLAB_TOKEN: 'secrets.env', CUSTOM_KEY: 'secrets.env', UNREFERENCED: 'secrets.env',
      } }),
    })
    const page = await mount('/integrations/secrets')
    await vi.waitFor(() => expect(page.text()).toContain('GITLAB_TOKEN'))
    const section = page.el.querySelector('section[aria-label="Credentials AppTown uses"]')!
    expect(section.querySelector('.section-head button')!.textContent).toBe('Add a credential')
    const rows = [...section.querySelectorAll('[role=listitem]')]
    const users = [...rows[0].querySelectorAll('a')]
    expect(users.map((a) => [a.textContent, a.getAttribute('href')])).toEqual([
      ['gitlab', '/integrations/gitlab'], ['Model account', '/integrations/models'],
    ])
    expect(rows[1].textContent).toContain('Used by custom')
    expect(rows[2].textContent).not.toContain('Used by')
    expect(new Set(calls.map((c) => c.path))).toEqual(new Set(['/integrations', '/integrations/catalog', '/secrets/names']))
    page.done()
  })

  // Each row says what the service is and whether it works; a service the
  // AppTown has no check for reads as unknown, never as working.
  // Integrations lists what AppTown runs on, Services what projects attach;
  // each says where something is failing before its page is opened.
  it('lists each kind in its area, with what is connected and failing', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [gitlab, scanner, sso, quiet] }), 'GET /integrations/catalog': json({ catalog }) })
    const rowIn = (p: { el: HTMLElement }) => (name: string) => [...p.el.querySelectorAll('[role=listitem].int-row')].find((r) => r.querySelector('.int-name')!.textContent === name)
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('Source control'))
    const row = rowIn(page)
    expect(page.el.querySelector('h1')!.textContent).toBe('Integrations')
    expect(row('Error reporting')).toBeUndefined()
    // The state, not a count.
    expect(row('Source control')!.textContent).toContain('working')
    expect(row('Source control')!.textContent).not.toMatch(/\d+ connected/)
    expect(row('Scanners')!.querySelector('.chip')!.className).toContain('fail')
    expect(row('Package registries')!.textContent).toContain('working')
    const services = await mount('/services')
    await vi.waitFor(() => expect(services.text()).toContain('Error reporting'))
    expect(services.el.querySelector('h1')!.textContent).toBe('Services')
    expect(rowIn(services)('Scanners')).toBeUndefined()
    // A kind defined at run time counts under Shared services.
    // A service that cannot be checked on its own says so; it is neither working nor a problem.
    expect(rowIn(services)('Shared services')!.textContent).toContain('no automatic check')
    expect(rowIn(services)('Error reporting')!.textContent).toContain('nothing connected')
    services.done()
    row('Scanners')!.querySelector<HTMLElement>('.int-name')!.click()
    await vi.waitFor(() => expect(page.where()).toBe('/integrations/scanner'))
    page.done()
  })

  it('opens an older link to a kind on its area page', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [gitlab] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/errors')
    await vi.waitFor(() => expect(page.where()).toBe('/services/errors'))
    const shared = await mount('/integrations/services')
    await vi.waitFor(() => expect(shared.where()).toBe('/services/shared'))
    // Deploy connectors and cloud accounts are listed with the targets built on them.
    const deploy = await mount('/integrations/deploy')
    await vi.waitFor(() => expect(deploy.where()).toBe('/integrations/deploy'))
    page.done(); shared.done(); deploy.done()
  })

  // Each row says what the service is and whether it works; a service the
  // AppTown has no check for reads as unknown, never as working.
  it('shows one kind per page and keeps working, failing and unchecked apart', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [gitlab, scanner, sso, quiet] }), 'GET /integrations/catalog': json({ catalog }) })
    const at = async (path: string) => { const p = await mount(path); await vi.waitFor(() => expect(p.el.querySelector('[role=listitem].int-row')).not.toBeNull()); return p }
    const rowIn = (p: { el: HTMLElement }) => (name: string) => [...p.el.querySelectorAll('[role=listitem].int-row')].find((r) => r.textContent!.includes(name))!
    const scm = await at('/integrations/scm')
    expect(scm.el.querySelector('h1')!.textContent).toBe('Source control')
    expect(scm.text()).not.toContain('django-check')
    const sc = await at('/integrations/scanner')
    const shared = await at('/services/shared')
    const reg = await at('/integrations/registry')
    // A kind with nothing configured lists nothing: Connect at the top is the one way to add.
    const errs = await mount('/services/errors')
    await vi.waitFor(() => expect(errs.text()).toContain('No connections yet. Use Connect to add one.'))
    expect(errs.el.querySelector('[role=listitem].int-row')).toBeNull()
    const row = (name: string) => [scm, sc, shared, reg].map((p) => rowIn(p)(name)).find(Boolean)!
    expect(row('gitlab').querySelector('.chip')!.className).toContain('pass')
    expect(row('gitlab').textContent).toContain('working')
    expect(row('gitlab').textContent).toContain('GitLab.')
    // Every group shows its rows; a failure is never folded away, and only it carries Fix.
    expect(sc.el.querySelector('button.group-toggle')).toBeNull()
    expect(row('django-check').querySelector('.chip')!.className).toContain('fail')
    expect(row('django-check').querySelector('.list-action')!.textContent).toBe('Fix')
    expect(row('gitlab').querySelector('.list-action')!.textContent).toBe('')
    expect(row('django-check').textContent).toContain('Django deploy checks')
    expect(row('authentik').querySelector('.chip')!.className).toBe('chip unknown')
    expect(row('authentik').textContent).toContain('unknown')
    expect(row('authentik').textContent).not.toContain('working')
    expect(row('authentik').textContent).toContain('unknown')
    expect(row('cache').textContent).toContain('working')
    expect(row('gitlab').textContent).toContain('5m ago')
    // What names each one, counted; nothing named reads as a dash, not zero.
    expect(row('django-check').querySelector('.int-usedby')).toBeNull()
    expect(row('gitlab').querySelector('.int-usedby')).toBeNull()
    expect(row('authentik').querySelector('.status-help')).toBeNull()
    expect(sc.el.querySelector('section[aria-label="Connections"] .section-count')).toBeNull()
    expect(sc.el.querySelector('section[aria-label="Connections"] .section-note')?.textContent).toContain('1 need attention')
    // With one kind connected, Shared services adds no sub-heading repeating the page.
    expect(shared.el.querySelector('.group-label')).toBeNull()
    for (const p of [scm, sc, shared, reg, errs]) p.done()
  })

  it('says nothing is connected only when AppTown said so', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: null }), 'GET /integrations/catalog': json({ catalog: [] }) })
    const page = await mount('/integrations/scm')
    await vi.waitFor(() => expect(page.text()).toContain('No connections yet. Use Connect to add one.'))
    page.done()
  })

  it('shows a failed read as a failure, not as an empty list', async () => {
    stubAppTown({ 'GET /integrations': json({ error: 'bad request' }, 400), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('The integrations could not be read: bad request'))
    expect(page.text()).not.toContain('Nothing connected yet')
    page.done()
  })

  it('shows a refused read as not available, not as a failure or an empty list', async () => {
    stubAppTown({ 'GET /integrations': json({ error: 'forbidden' }, 403), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('Not available to you'))
    expect(page.el.querySelector('.error')).toBeNull()
    expect(page.text()).not.toContain('Nothing connected yet')
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
    const page = await mount('/integrations/scm', client)
    await vi.waitFor(() => expect(page.text()).toContain('working'))
    fail = true
    await act(async () => { await client.invalidateQueries({ queryKey: getKey('/integrations') }) })
    await vi.waitFor(() => expect(page.text()).toContain('unknown'))
    expect(page.el.querySelector('[role=listitem].int-row .chip')!.className).toContain('unknown')
    expect(page.text()).toContain('unknown')
    expect(page.text()).toContain(gitlab.health.freshnessHelp)
    expect(page.text()).not.toContain('every five minutes')
    expect(page.el.querySelector('.chip.unknown')?.getAttribute('aria-label')).toContain('(AppTown unreachable)')
    page.done()
  })

  it('opens an integration on its own page', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [gitlab, scanner] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/scm')
    await vi.waitFor(() => expect(page.text()).toContain('gitlab'))
    // The row itself opens it, not only its name.
    await act(async () => { ([...page.el.querySelectorAll('[role=listitem].int-row')].find((r) => r.textContent!.includes('gitlab')) as HTMLElement).querySelector<HTMLElement>('.int-when')!.click() })
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
    const connection = page.el.querySelector('section[aria-label="Connection"]')!
    const fact = (k: string) => [...connection.querySelectorAll('dt')].find((d) => d.textContent === k)?.nextElementSibling?.textContent
    expect(fact('tokenFrom')).toBe('GITLAB_TOKEN')
    expect(fact('auth')).toBe('PRIVATE-TOKEN')
    expect(connection.textContent).toContain('abc123def456')
    // Nothing folded away on this page.
    expect(page.el.querySelector('details:not(.status-help)')).toBeNull()
    // AppTown-managed: no removing it.
    expect(page.button('Remove')).toBeUndefined()
    page.done()
  })

  it('names the projects and deployments that use it, linked', async () => {
    stubAppTown({ 'GET /integrations': json({ integrations: [scanner, gitlab] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/django-check')
    await vi.waitFor(() => expect(page.text()).toContain('Used by'))
    const used = page.el.querySelector('section[aria-label="Used by"]')!
    const links = [...used.querySelectorAll('a')].map((a) => [(a.classList.contains('int-name') ? a : a.querySelector('.int-name') ?? a).textContent, a.getAttribute('href')])
    expect(links).toEqual([['2 projects', '/integrations/django-check/projects']])
    expect(used.textContent).not.toContain('Shop')
    page.done()
    const other = await mount('/integrations/gitlab')
    await vi.waitFor(() => expect(other.text()).toContain('Used by'))
    // Source control serves every project without being named by one.
    expect(other.el.querySelector('section[aria-label="Used by"]')!.textContent).toContain('Every project.')
    other.done()
  })

  // An unchecked reading says what is true of its kind: AI models are not
  // checked on every reading, but Test connection asks them; a kind with
  // neither says it cannot be checked.
  it('says an unchecked kind that Test connection asks can be asked', async () => {
    const llm = binding('openrouter', 'llm', 'openrouter', { freshnessHelp: 'Connection readings are marked old after 75 minutes.', ok: false, detail: '', checked: false, automatic:false,stateLabel: 'unknown' }, { testable: true })
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

  it('removes it after asking, and returns to its kind', async () => {
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
    await vi.waitFor(() => expect(page.where()).toBe('/integrations/scanner'))
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
  const page = await mount('/integrations/scm')
  try {
    await vi.waitFor(() => expect(page.text()).toContain('not configured'))
    expect(page.el.querySelector('section[aria-label="Connections"] .section-note')?.textContent ?? '').not.toContain('need attention')
    expect(page.el.querySelector('section[aria-label="Connections"] .section-count')).toBeNull()
    expect(page.el.querySelector('[role=listitem].int-row')?.textContent).not.toContain('not checked yet')
    // Neutral: a grey dot, never a colour.
    expect(page.el.querySelector('[role=listitem].int-row .chip')?.className).toBe('chip ')
    expect(page.el.querySelector('[role=listitem].int-row .chip.fail')).toBeNull()
  } finally { page.done(); vi.unstubAllGlobals() }
})

it('keeps a last check age and result visible even when it failed or became old', async () => {
 const {lastChecked}=await import('./Integrations')
 const now=Date.parse('2026-11-01T00:00:00Z')
 expect(lastChecked({ freshnessHelp: 'Connection readings are marked old after 75 minutes.',automatic:true,stateLabel:'passed',ok:true,checked:true,checkedAt:'2026-10-31T23:55:00Z',detail:''},now)).toBe('checked 5m ago')
 expect(lastChecked({ freshnessHelp: 'Connection readings are marked old after 75 minutes.',automatic:true,stateLabel:'failed',ok:false,checked:true,stale:true,checkedAt:'2026-10-31T23:30:00Z',detail:'credentials refused'},now)).toBe('last seen 30m ago')
})


it('formats the age of an expired reading from the owner classification', async () => {
 const {lastChecked}=await import('./Integrations')
 expect(lastChecked({ freshnessHelp: 'Connection readings are marked old after 75 minutes.',automatic:true,stateLabel:'unknown',stale:true,lastResult:'failed',ok:false,checked:true,checkedAt:'2026-10-31T12:00:00Z',detail:'Reading expired.'},Date.parse('2026-11-01T00:00:00Z'))).toBe('last seen 12h ago')
})


it('shows an event time when a server check is slightly ahead of the reader clock', async () => {
 const {lastChecked}=await import('./Integrations')
 const when=new Date(2026,9,30,17,26).toISOString()
 expect(lastChecked({ freshnessHelp: 'Connection readings are marked old after 75 minutes.',automatic:true,stateLabel:'passed',ok:true,checked:true,checkedAt:when,detail:''},Date.parse(when)-1000)).toBe('checked just now')
})

it('uses owner freshness even when the browser clock is far ahead', async () => {
 const {healthState}=await import('./integrations/kinds')
 const h=checked(true,'Connection answered')
 const future=Date.parse(fiveMinutesAgo)+24*3600000
 expect(healthState(h,future)).toBe('passed')
})

it('shows a manual-only integration neutrally and shows an old test age once', async () => {
 const old={...sso,id:'old-sso',name:'old-sso',health:{...sso.health,checked:true,stateLabel:'passed' as const,ok:true,stale:true,checkedAt:fiveMinutesAgo}}
 stubAppTown({'GET /integrations':json({integrations:[sso,old]}),'GET /integrations/catalog':json({catalog})})
 const page=await mount('/services/shared')
 try {
  // Connected services link to their page; an unconnected kind's Connect row does not.
  const bound=()=>[...page.el.querySelectorAll('[role=listitem].int-row')].filter(r=>r.querySelector('a.int-name'))
  await vi.waitFor(()=>expect(bound()).toHaveLength(2))
  const rows=bound()
  expect(rows[0].querySelector('.chip')?.textContent).toBe('● unknown')
  expect(rows[0].querySelector('.chip')?.className).toBe('chip unknown')
  expect(rows[1].querySelector('.chip')?.textContent).toContain('old · working')
  expect(rows[1].textContent?.match(/last seen/g)).toHaveLength(1)
 } finally {page.done()}
})

// Each menu page's "?" says what that kind is for, not the area's list.
it('gives every menu page its own help, naming a shipped guide', async () => {
  const guides = new Set((readdirSync('../stock/docs/guides') as string[]).map((f) => f.replace(/\.md$/, '')))
  const { INTEGRATION_MENU } = await import('./integrations/kinds')
  const { KIND_HELP } = await import('./Integrations')
  for (const it of INTEGRATION_MENU.flatMap((g) => g.items)) {
    expect(KIND_HELP[it.key], it.key).toBeDefined()
    expect(guides, it.key).toContain(KIND_HELP[it.key].guide)
  }
})

// A connector with no presets (OpenRouter) is still offered in Connect.
it('offers a connector that has no presets', async () => {
  const { CatalogPicker } = await import('./integrations/Catalog')
  const picked = vi.fn()
  const el = document.createElement('div'); document.body.appendChild(el)
  const root = createRoot(el)
  await act(async () => { root.render(<MemoryRouter><CatalogPicker catalog={[spec('llm', 'openrouter', 'AI models (OpenRouter)')]} onPick={picked} onOpen={() => {}} onClose={() => {}} /></MemoryRouter>) })
  const tile = [...document.querySelectorAll<HTMLElement>('.cat-tile')].find((t) => t.textContent!.includes('AI models (OpenRouter)'))!
  await act(async () => { tile.click() })
  expect(picked).toHaveBeenCalledWith(expect.objectContaining({ provider: 'openrouter' }), undefined)
  act(() => root.unmount()); el.remove()
})

// A kind with one working connection and one never configured reads working.
it('judges a kind on what is configured, not on what was never set up', async () => {
  const notSetUp = { ...gitlab, id: 'g2', name: 'infisical', capability: 'scm', health: { checked: false, ok: false, detail: '', automatic: true, stateLabel: 'not set up' as const } }
  stubAppTown({ 'GET /integrations': json({ integrations: [gitlab, notSetUp] }), 'GET /integrations/catalog': json({ catalog }) })
  const page = await mount('/integrations')
  await vi.waitFor(() => expect(page.text()).toContain('Source control'))
  const row = [...page.el.querySelectorAll('[role=listitem].int-row')].find((r) => r.querySelector('.int-name')!.textContent === 'Source control')!
  expect(row.textContent).toContain('working')
  page.done()
})

// A row is working or a problem: a check that cannot run on its own does not
// hold it back, and deployment targets count the targets themselves.
it('reads a row as working or a problem, counting targets', async () => {
  const quietCheck = { ...scanner, id: 's2', name: 'in-app', health: { checked: false, ok: false, detail: '', automatic: false, stateLabel: 'unknown' as const } }
  const passing = { ...scanner, id: 's3', name: 'gitleaks', health: checked(true, 'image present') }
  stubAppTown({
    'GET /integrations': json({ integrations: [passing, quietCheck] }), 'GET /integrations/catalog': json({ catalog }),
    'GET /targets': json({ targets: [{ id: 't', name: 'home', statusLabel: 'failed', offers: [], kindInfo: { family: 'docker' }, address: {} }], groups: [] }),
  })
  const page = await mount('/integrations')
  await vi.waitFor(() => expect(page.text()).toContain('Scanners'))
  const row = (name: string) => [...page.el.querySelectorAll('[role=listitem].int-row')].find((r) => r.querySelector('.int-name')!.textContent === name)!
  expect(row('Scanners').textContent).toContain('working')
  expect(page.text()).not.toContain('Deployment targets')
  page.done()
})


describe('model provider settings', () => {
  afterEach(() => vi.unstubAllGlobals())
  const modelSpec = spec('model', 'pi', 'Model provider', { fields: [
    { key: 'secretEnv', label: 'Credentials', type: 'secretRef' },
    { key: 'models', label: 'Enabled models', type: 'modelList' },
    { key: 'model', label: 'Default model', type: 'string' },
    { key: 'concurrency', label: 'Calls at once', type: 'number' },
    { key: 'baseUrl', label: 'Base URL', type: 'string' },
    { key: 'api', label: 'API dialect', type: 'select' },
    { key: 'apiKeyFrom', label: 'Endpoint API key', type: 'secretRef' },
    { key: 'modelsJson', label: 'Models (JSON array)', type: 'string' },
  ] })

  it('shows built-in fields and requires an enabled default before saving', async () => {
    const saved: unknown[] = []
    stubAppTown({
      'GET /integrations': json({ integrations: [binding('openai', 'model', 'pi', checked(true, 'OpenAI listed 140 models'), {
        config: { secretEnv: ['OPENAI_API_KEY'], models: ['current', 'fast'], model: 'openai/stale' },
      })] }),
      'GET /integrations/catalog': json({ catalog: [modelSpec] }),
      'GET /secrets/names': json({ names: ['OPENAI_API_KEY'] }),
      'POST /integrations': (_m, body) => { saved.push(body); return json({ ok: true })() },
    })
    const page = await mount('/integrations/openai')
    await vi.waitFor(() => expect(page.button('Edit')).toBeDefined())
    await act(async () => page.button('Edit')!.click())
    expect(page.text()).toContain('Credentials')
    for (const label of ['Base URL', 'API dialect', 'Endpoint API key', 'Models (JSON array)']) expect(page.text()).not.toContain(label)
    expect(page.button('Save')!.disabled).toBe(true)
    const choose = page.el.querySelector<HTMLSelectElement>('[aria-label="Default model"]')!
    expect([...choose.options].map(o => o.value)).toEqual(['', 'openai/current', 'openai/fast'])
    await act(async () => { choose.value = 'openai/current'; choose.dispatchEvent(new Event('change', { bubbles: true })) })
    expect(page.button('Save')!.disabled).toBe(false)
    await act(async () => page.button('Save')!.click())
    expect(saved).toEqual([expect.objectContaining({ config: expect.objectContaining({ model: 'openai/current' }) })])
    page.done()
  })

  it('shows custom endpoint fields without built-in credentials or enabled models', async () => {
    const el = document.createElement('div'); const root = createRoot(el)
    await act(async () => root.render(<QueryClientProvider client={newQueryClient()}><ConnectorForm spec={modelSpec} icon="gpu" initialName="local" initialConfig={{ api: 'openai-completions' }} onDone={() => {}} /></QueryClientProvider>))
    for (const label of ['Base URL', 'API dialect', 'Endpoint API key', 'Models (JSON array)']) expect(el.textContent).toContain(label)
    expect([...el.querySelectorAll('.form-grid > span > label')].map(l => l.textContent)).not.toContain('Enabled models')
    expect(el.textContent).not.toContain('Credentials')
    act(() => root.unmount())
  })

  it('offers discovered defaults for a custom endpoint when the enabled list is empty', async () => {
    stubAppTown({
      'GET /secrets/names': json({ names: [] }),
      'POST /integrations/discover': json({ models: ['live-model'] }),
    })
    const el = document.createElement('div'); const root = createRoot(el)
    await act(async () => root.render(<QueryClientProvider client={newQueryClient()}><ConnectorForm spec={modelSpec} icon="gpu" initialName="local" initialConfig={{ baseUrl: "http://localhost:1234" }} onDone={() => {}} /></QueryClientProvider>))
    await act(async () => [...el.querySelectorAll('button')].find(b => b.textContent === 'Browse default models…')!.click())
    expect([...el.querySelector<HTMLSelectElement>('[aria-label="Default model"]')!.options].map(o => o.value)).toEqual(['', 'local/live-model'])
    act(() => root.unmount())
  })

  it.each(['openai', 'anthropic', 'openrouter'])('requires enabling models on the %s provider page and editor', async name => {
    stubAppTown({
      'GET /integrations': json({ integrations: [binding(name, 'model', 'pi', checked(true, 'Catalog read'), { config: { model: `${name}/default`, models: [] } })] }),
      'GET /integrations/catalog': json({ catalog: [modelSpec] }),
      'GET /secrets/names': json({ names: [] }),
    })
    const page = await mount(`/integrations/${name}`)
    await vi.waitFor(() => expect(page.button('Edit')).toBeDefined())
    expect(page.text()).toContain('Enable at least one model')
    expect(page.el.querySelector('.int-state .chip')?.className).toContain('pending')
    expect(page.el.querySelector('.int-state')?.textContent).not.toContain('working')
    await act(async () => page.button('Edit')!.click())
    expect(page.button('Save')!.disabled).toBe(true)
    expect(page.button('Browse default models…')).toBeUndefined()
    expect(page.el.querySelector('[aria-label="Fuzzy selector"]')).toBeNull()
    expect([...page.el.querySelector<HTMLSelectElement>('[aria-label="Default model"]')!.options].map(o => o.value)).toEqual([''])
    page.done()
  })

  it('shows labelled, set facts and one status; raw settings require Techno Goggles', async () => {
    stubAppTown({
      'GET /integrations': json({ integrations: [binding('openai', 'model', 'pi', checked(true, 'OpenAI listed 140 models'), {
        config: { secretEnv: ['OPENAI_API_KEY'], models: ['current'], model: 'openai/current', concurrency: 3, apiKeyFrom: [], api: '', baseUrl: '', modelsJson: '' },
      })] }),
      'GET /integrations/catalog': json({ catalog: [modelSpec] }),
    })
    const page = await mount('/integrations/openai')
    await vi.waitFor(() => expect(page.text()).toContain('OPENAI_API_KEY'))
    expect(page.text()).toContain('The models agents call, from OpenAI.')
    expect([...page.el.querySelectorAll('dt')].map(d => d.textContent)).toEqual(['Name', 'Credentials', 'Enabled models', 'Default model', 'Calls at once'])
    for (const raw of ['secretEnv', 'apiKeyFrom', 'modelsJson', 'Configuration hash', 'not set']) expect(page.text()).not.toContain(raw)
    expect(page.el.querySelectorAll('.int-state .chip')).toHaveLength(1)
    expect(page.text().match(/OpenAI listed 140 models/g)).toHaveLength(1)
    expect(page.el.querySelector('svg[aria-label="openai"]')).not.toBeNull()
    const controls = document.createElement('div'); const controlRoot = createRoot(controls)
    await act(async () => controlRoot.render(<QueryClientProvider client={newQueryClient()}><TechnicalDetailSwitch /></QueryClientProvider>))
    await act(async () => controls.querySelector('button')!.click())
    expect(page.text()).toContain('secretEnv')
    expect(page.text()).toContain('Configuration hash')
    await act(async () => controls.querySelector('button')!.click())
    act(() => controlRoot.unmount())
    page.done()
  })
})
