// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../../data'
import { ServicesTab } from './Services'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const path = '/projects/demo/services'
const catalog = { dependencies: [{ capability: 'postgres', title: 'Postgres', local: { image: 'postgres:16' } }, { capability: 'redis', title: 'Redis', local: { image: 'redis:7' } }] }
type Declared = { needs: { capability: string; params?: Record<string, string> }[]; contract: string; declaration: string }

function mountServices(client: ReturnType<typeof newQueryClient>) {
  const el = document.createElement('div')
  const root = createRoot(el)
  act(() => { root.render(<QueryClientProvider client={client}><MemoryRouter><ServicesTab slug="demo" /></MemoryRouter></QueryClientProvider>) })
  const tile = (title: string) => [...el.querySelectorAll('[role=button]')].find((t) => t.textContent?.includes(title)) as HTMLElement
  const button = (text: string) => [...el.querySelectorAll('button')].find((b) => b.textContent === text)
  return { el, tile, button, unmount: () => act(() => root.unmount()) }
}

// serve answers reads with `server()` and saves with `save()`, recording what
// each save sent.
function serve(server: () => Declared, save: () => Response = () => new Response(JSON.stringify({ changeId: '00000000-0007-7000-8000-000000000000' }))) {
  const sent: unknown[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') { sent.push(JSON.parse(String(init.body))); return save() }
    return new Response(JSON.stringify(url.endsWith('/dependencies') ? catalog : server()))
  }))
  return sent
}

// Reproduction from the 28 September review: a revisited page is handed the
// cached services first. The form must start from the fresh answer, and the
// declaration it names must be that answer's.
it('revisiting services edits the refreshed contract and names its declaration', async () => {
  const client = newQueryClient()
  client.setQueryData(getKey(path), { needs: [], contract: 'app.yaml', declaration: 'old' })
  const current: Declared = { needs: [{ capability: 'postgres' }], contract: 'app.yaml', declaration: 'now' }
  const sent = serve(() => current)
  const f = mountServices(client)
  try {
    await vi.waitFor(() => expect(f.tile('Postgres')?.getAttribute('aria-pressed')).toBe('true'))
    // Nothing edited, nothing to save.
    expect(f.button('Save as a change')!.disabled).toBe(true)
    await act(async () => { f.tile('Redis').click() })
    await act(async () => { f.button('Save as a change')!.click() })
    expect(sent).toEqual([{ needs: [{ capability: 'postgres' }, { capability: 'redis', params: {} }], title: 'Update services', expectedDeclaration: 'now' }])
  } finally {
    f.unmount()
    client.clear()
    vi.unstubAllGlobals()
  }
})

// With an older answer held and the refresh refused, there is nothing safe to
// edit: the page says the services could not be read and offers no Save.
it('a failed refresh of the services offers no form to save', async () => {
  const client = newQueryClient()
  client.setQueryData(getKey(path), { needs: [], contract: 'app.yaml', declaration: 'old' })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url.endsWith('/dependencies')
    ? new Response(JSON.stringify(catalog))
    : new Response(JSON.stringify({ error: 'not allowed' }), { status: 403 })))
  const f = mountServices(client)
  try {
    await vi.waitFor(() => expect(f.el.textContent).toContain("The project's services could not be read: not allowed"))
    expect(f.button('Save as a change')).toBeUndefined()
  } finally {
    f.unmount()
    client.clear()
    vi.unstubAllGlobals()
  }
})

it('keeps an edited services form through a background refresh', async () => {
  const client = newQueryClient()
  let server: Declared = { needs: [{ capability: 'postgres' }], contract: 'app.yaml', declaration: 'b' }
  const sent = serve(() => server)
  const f = mountServices(client)
  try {
    await vi.waitFor(() => expect(f.tile('Redis')).toBeTruthy())
    await act(async () => { f.tile('Redis').click() })
    server = { needs: [], contract: 'app.yaml', declaration: 'c' }
    await act(async () => { await client.invalidateQueries({ queryKey: ['get'] }) })
    await vi.waitFor(() => expect(client.getQueryData(getKey(path))).toEqual(server))
    expect(f.tile('Postgres').getAttribute('aria-pressed')).toBe('true')
    expect(f.tile('Redis').getAttribute('aria-pressed')).toBe('true')
    await act(async () => { f.button('Save as a change')!.click() })
    expect(sent[0]).toMatchObject({ needs: [{ capability: 'postgres' }, { capability: 'redis' }], expectedDeclaration: 'b' })
  } finally {
    f.unmount()
    client.clear()
    vi.unstubAllGlobals()
  }
})

// Main's services moved while the person edited: the save is refused, the
// page reads them again and shows what changed, and keeping the person's
// edits applies only what they changed to the services as they are now.
it('on a moved declaration keeps the picks and puts only the person\'s edits on top', async () => {
  const client = newQueryClient()
  let server: Declared = { needs: [{ capability: 'postgres', params: { version: '16' } }], contract: 'app.yaml', declaration: 'b' }
  let conflict = true
  const sent = serve(() => server, () => conflict
    ? new Response(JSON.stringify({ error: 'moved' }), { status: 409, headers: { 'X-AppTown-Deny-Code': 'settings-moved' } })
    : new Response(JSON.stringify({ changeId: '00000000-0009-7000-8000-000000000000' })))
  const f = mountServices(client)
  try {
    await vi.waitFor(() => expect(f.tile('Redis')).toBeTruthy())
    await act(async () => { f.tile('Redis').click() })
    // Someone lands a Postgres upgrade meanwhile.
    server = { needs: [{ capability: 'postgres', params: { version: '17' } }], contract: 'app.yaml', declaration: 'c' }
    await act(async () => { f.button('Save as a change')!.click() })
    await vi.waitFor(() => expect(f.el.textContent).toContain('Declared now: postgres 17 (it was postgres 16 when you started). Keeping your edits gives postgres 17, redis.'))
    expect(f.el.textContent).toContain("The app's services changed on main after you started")
    expect(f.tile('Redis').getAttribute('aria-pressed')).toBe('true')
    await act(async () => { f.button('Keep my edits on top of theirs')!.click() })
    conflict = false
    await act(async () => { f.button('Save as a change')!.click() })
    expect(sent[1]).toEqual({ needs: [{ capability: 'postgres', params: { version: '17' } }, { capability: 'redis', params: {} }], title: 'Update services', expectedDeclaration: 'c' })
  } finally {
    f.unmount()
    client.clear()
    vi.unstubAllGlobals()
  }
})

it('keeping my edits puts only the parameters I changed on top of theirs', async () => {
  const client = newQueryClient()
  let server: Declared = { needs: [{ capability: 'postgres', params: { version: '16', database: 'old_db' } }], contract: 'app.yaml', declaration: 'b' }
  let conflict = true
  const sent = serve(() => server, () => conflict
    ? new Response(JSON.stringify({ error: 'moved' }), { status: 409, headers: { 'X-AppTown-Deny-Code': 'settings-moved' } })
    : new Response(JSON.stringify({ changeId: '00000000-0009-7000-8000-000000000000' })))
  const f = mountServices(client)
  try {
    await vi.waitFor(() => expect(f.el.querySelector('input')).toBeTruthy())
    const input = f.el.querySelector('input')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, '17')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(f.button('Save as a change')!.disabled).toBe(false)
    server = { needs: [{ capability: 'postgres', params: { version: '16', database: 'new_db' } }], contract: 'app.yaml', declaration: 'c' }
    await act(async () => { f.button('Save as a change')!.click() })
    await vi.waitFor(() => expect(f.button('Keep my edits on top of theirs')?.disabled).toBe(false))
    await act(async () => { f.button('Keep my edits on top of theirs')!.click() })
    conflict = false
    await act(async () => { f.button('Save as a change')!.click() })
    expect(sent[1]).toMatchObject({ needs: [{ capability: 'postgres', params: { version: '17', database: 'new_db' } }], expectedDeclaration: 'c' })
  } finally { f.unmount(); client.clear(); vi.unstubAllGlobals() }
})

// A need is fulfilled by the target or through an integration, read from its
// catalogue entry; an integration delivered when attached is not a need.
it('says what fulfils each need and lists only declarable kinds', async () => {
  const { declarable, fulfilledBy } = await import('./Services')
  const pg = { capability: 'postgres', title: 'Postgres', local: { image: 'postgres:16' }, cloud: { digitalocean: { kind: 'managed-database' } } }
  const llm = { capability: 'llm', title: 'AI models', local: { image: '' }, register: { connector: 'openrouter' } }
  const errors = { capability: 'errors', title: 'Error reporting', local: { image: '' }, integration: true, register: { connector: 'glitchtip' } }
  expect(fulfilledBy(pg)).toEqual(['Docker host or Portainer: runs beside the app', 'DigitalOcean: managed database'])
  expect(fulfilledBy(llm)).toEqual(['through the OpenRouter integration (attach it under Connected to)'])
  expect([pg, llm, errors].filter(declarable).map((k) => k.capability)).toEqual(['postgres', 'llm'])
})

// A tile shows facts: an Experimental badge only on an experimental kind, and
// the kind in one line (its summary, else its description); the whole
// description is only the tooltip.
it('a tile shows the Experimental badge from data and the kind in one line', async () => {
  const client = newQueryClient()
  const kinds = { dependencies: [
    { capability: 'postgres', title: 'Postgres', summary: 'A database of its own.', description: 'A relational database. The app connects with DATABASE_URL.', local: { image: 'postgres:16' } },
    { capability: 'redis', title: 'Redis', status: 'experimental', summary: 'A cache.', description: 'Experimental: no app has shipped with it yet. An in-memory cache.', local: { image: 'redis:7' } },
    { capability: 'custom', title: 'Custom', description: 'A kind with no summary.', local: { image: 'custom:1' } },
  ] }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.endsWith('/dependencies') ? kinds
    : { needs: [], contract: 'app.yaml', declaration: 'd' }))))
  const f = mountServices(client)
  try {
    await vi.waitFor(() => expect(f.tile('Redis')).toBeTruthy())
    expect(f.tile('Redis').textContent).toContain('Experimental')
    expect(f.tile('Postgres').textContent).not.toContain('Experimental')
    expect(f.tile('Redis').textContent).toContain('A cache.')
    expect(f.tile('Redis').textContent).not.toContain('no app has shipped')
    expect(f.tile('Postgres').querySelector('[title^="A relational database"]')).toBeTruthy()
    expect(f.tile('Custom').textContent).toContain('A kind with no summary.')
  } finally {
    f.unmount()
    client.clear()
    vi.unstubAllGlobals()
  }
})
