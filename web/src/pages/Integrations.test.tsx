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
  // forge refuses "2" or "" where a model binding's concurrency belongs.
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

// Readings the forge took five minutes ago, by its own clock.
const fiveMinutesAgo = new Date(Date.now() - 5 * 60_000).toISOString()
const checked = (ok: boolean, detail: string): IntegrationHealth => ({ ok, detail, checked: true, checkedAt: fiveMinutesAgo })
const binding = (name: string, capability: string, provider: string, health: IntegrationHealth, extra: Partial<IntegrationEntry> = {}): IntegrationEntry => ({
  id: `00000000-${String(name.length).padStart(4, '0')}-7000-8000-000000000000`, name, capability, provider, config: {}, version: 1, updatedAt: '2026-10-01T00:00:00Z', managed: false, configHash: 'abc123def456', usedBy: { projects: [], deployments: [] }, health, testable: false, ...extra,
})
const gitlab = binding('gitlab', 'scm', 'gitlab', checked(true, 'authenticated as forge-bot'), { managed: true, config: { tokenFrom: 'GITLAB_TOKEN', auth: 'PRIVATE-TOKEN' } })
const scanner = binding('django-check', 'scanner', 'tool', checked(false, 'no image configured'), {
  config: { description: 'Django deploy checks', image: '' },
  usedBy: { projects: [{ slug: 'shop', name: 'Shop', capability: 'scanner' }, { slug: 'blog', name: 'Blog', capability: 'scanner' }], deployments: [{ project: 'shop', name: 'prod', target: 'lan-host' }] },
})
const sso = binding('authentik', 'sso', 'service', { ok: false, detail: '', checked: false }, { config: { url: 'https://auth.example' } })
// Checked, with nothing to add: working, whatever the detail says.
const quiet = binding('cache', 'registry', 'npm', checked(true, ''))
const spec = (capability: string, provider: string, title: string, extra: Partial<ConnectorEntry> = {}): ConnectorEntry => ({
  capability, provider, title, description: `${title}. More words.`, multi: true, managed: false, fields: [], hasDocs: false, ...extra,
})
const catalog = [spec('scm', 'gitlab', 'GitLab', { multi: false, managed: true }), spec('scanner', 'tool', 'Scanner (tool)'), spec('sso', 'service', 'SSO (OIDC)'),
  spec('errors', 'glitchtip', 'Error reporting (GlitchTip)')]

type Reply = (method: string, body: unknown) => Response | Promise<Response>
function stubForge(routes: Record<string, Reply>) {
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
  // forge has no check for reads as unknown, never as working.
  it('groups services by kind and keeps working, failing and unchecked apart', async () => {
    stubForge({ 'GET /integrations': json({ integrations: [gitlab, scanner, sso, quiet] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('gitlab'))
    const groups = [...page.el.querySelectorAll('.int-group')].map((g) => g.textContent)
    expect(groups[0]).toContain('Source control')
    expect(groups[1]).toContain('Scanners')
    const row = (name: string) => [...page.el.querySelectorAll('tr.int-row')].find((r) => r.textContent!.includes(name))!
    expect(row('gitlab').querySelector('.chip')!.className).toContain('pass')
    expect(row('gitlab').textContent).toContain('Working')
    expect(row('django-check').querySelector('.chip')!.className).toContain('fail')
    expect(row('django-check').textContent).toContain('Django deploy checks')
    expect(row('authentik').querySelector('.chip')!.className).toContain('unknown')
    expect(row('authentik').textContent).toContain('Not checked')
    expect(row('authentik').textContent).not.toContain('Working')
    expect(row('authentik').textContent).toContain('no check')
    expect(row('cache').textContent).toContain('Working')
    expect(row('gitlab').textContent).toContain('5m ago')
    // What names each one, counted; nothing named reads as a dash, not zero.
    expect(row('django-check').querySelector('.int-usedby')!.textContent).toBe('2 projects · 1 deployment')
    expect(row('gitlab').querySelector('.int-usedby')!.textContent).toBe('—')
    // A kind the catalogue offers with nothing connected can be connected from its place.
    expect(row('Error reporting (GlitchTip)').textContent).toContain('Not connected')
    expect(page.text()).toContain('4 connected · 1 not working · 1 with no live check')
    page.done()
  })

  it('says nothing is connected only when the forge said so', async () => {
    stubForge({ 'GET /integrations': json({ integrations: null }), 'GET /integrations/catalog': json({ catalog: [] }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('No services are connected yet.'))
    page.done()
  })

  it('shows a failed read as a failure, not as an empty list', async () => {
    stubForge({ 'GET /integrations': json({ error: 'bad request' }, 400), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('The integrations could not be read: bad request'))
    expect(page.text()).not.toContain('No services are connected')
    page.done()
  })

  it('shows a refused read as not available, not as a failure or an empty list', async () => {
    stubForge({ 'GET /integrations': json({ error: 'forbidden' }, 403), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations')
    await vi.waitFor(() => expect(page.text()).toContain('Not available to you'))
    expect(page.el.querySelector('.error')).toBeNull()
    expect(page.text()).not.toContain('No services are connected')
    page.done()
  })

  // When checking again fails, the states shown are the last answer, and say so.
  it('marks every state as old when checking again fails', async () => {
    let fail = false
    stubForge({
      'GET /integrations': () => fail ? new Response(JSON.stringify({ error: 'forge unreachable' }), { status: 400 }) : new Response(JSON.stringify({ integrations: [gitlab] })),
      'GET /integrations/catalog': json({ catalog }),
    })
    const client = newQueryClient()
    const page = await mount('/integrations', client)
    await vi.waitFor(() => expect(page.text()).toContain('Working'))
    fail = true
    await act(async () => { await client.invalidateQueries({ queryKey: getKey('/integrations') }) })
    await vi.waitFor(() => expect(page.text()).toContain('Working at last check'))
    expect(page.el.querySelector('tr.int-row .chip')!.className).toContain('stale')
    expect(page.text()).toContain('out of date')
    expect(page.el.querySelector('.out-of-date')?.getAttribute('aria-label')).toContain('(forge unreachable)')
    page.done()
  })

  it('opens an integration on its own page', async () => {
    stubForge({ 'GET /integrations': json({ integrations: [gitlab, scanner] }), 'GET /integrations/catalog': json({ catalog }) })
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
    stubForge({ 'GET /integrations': json({ integrations: [gitlab] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/gitlab')
    await vi.waitFor(() => expect(page.text()).toContain('authenticated as forge-bot'))
    expect(page.text()).toMatch(/Checked by AppTown 5m ago \(\d{1,2}:\d\d/)
    const sections = [...page.el.querySelectorAll('section')]
    const credentials = sections.find((s) => s.querySelector('h2')?.textContent === 'Credentials')!
    expect(credentials.textContent).toContain('tokenFrom')
    expect(credentials.textContent).toContain('GITLAB_TOKEN')
    const config = sections.find((s) => s.querySelector('h2')?.textContent === 'Configuration')!
    expect(config.textContent).toContain('PRIVATE-TOKEN')
    expect(config.textContent).not.toContain('tokenFrom')
    expect(page.text()).toContain('abc123def456')
    // Forge-managed: no removing it.
    expect(page.button('Remove')).toBeUndefined()
    page.done()
  })

  it('names the projects and deployments that use it, linked', async () => {
    stubForge({ 'GET /integrations': json({ integrations: [scanner, gitlab] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/django-check')
    await vi.waitFor(() => expect(page.text()).toContain('Used by'))
    const used = [...page.el.querySelectorAll('section')].find((s) => s.querySelector('h2')?.textContent === 'Used by')!
    const links = [...used.querySelectorAll('a')].map((a) => [a.textContent, a.getAttribute('href')])
    expect(links).toEqual([['Shop', '/projects/shop'], ['Blog', '/projects/blog'], ['shop/prod', '/projects/shop/settings/deployments'], ['lan-host', '/infrastructure/lan-host']])
    page.done()
    const other = await mount('/integrations/gitlab')
    await vi.waitFor(() => expect(other.text()).toContain('Used by'))
    expect(other.text()).toContain('No project attaches it')
    other.done()
  })

  // An unchecked reading says what is true of its kind: AI models are not
  // checked on every reading, but Test connection asks them; a kind with
  // neither says it cannot be checked.
  it('says an unchecked kind that Test connection asks can be asked', async () => {
    const llm = binding('openrouter', 'llm', 'openrouter', { ok: false, detail: '', checked: false }, { testable: true })
    stubForge({ 'GET /integrations': json({ integrations: [llm, sso] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/openrouter')
    await vi.waitFor(() => expect(page.text()).toContain('Test connection asks it now'))
    expect(page.text()).not.toContain('no live check')
    page.done()
    const other = await mount('/integrations/authentik')
    await vi.waitFor(() => expect(other.text()).toContain('no live check for this kind'))
    other.done()
  })

  it('tests the connection and shows the answer', async () => {
    const calls = stubForge({
      'GET /integrations': json({ integrations: [scanner] }), 'GET /integrations/catalog': json({ catalog }),
      'POST /integrations/test': json(checked(true, 'image pulled')),
    })
    const page = await mount('/integrations/django-check')
    await vi.waitFor(() => expect(page.button('Test connection')).toBeDefined())
    await act(async () => { page.button('Test connection')!.click() })
    await vi.waitFor(() => expect(page.text()).toContain('Test passed'))
    expect(page.text()).toContain('image pulled')
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ capability: 'scanner', provider: 'tool', config: scanner.config })
    page.done()
  })

  it('removes it after asking, and returns to the list', async () => {
    let gone = false
    const calls = stubForge({
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
    stubForge({ 'GET /integrations': json({ integrations: [gitlab] }), 'GET /integrations/catalog': json({ catalog }) })
    const page = await mount('/integrations/nope')
    await vi.waitFor(() => expect(page.text()).toContain('No integration is called “nope”'))
    page.done()
  })
})
