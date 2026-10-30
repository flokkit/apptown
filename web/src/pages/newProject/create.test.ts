import { afterEach, describe, expect, it, vi } from 'vitest'
import { buildFirstVersion, continueCreation, createProject, leaveSubmittedCreation, type CreationContext, type ProgressLine } from './create'
import { newDraft } from './draft'
import { captureCreation, readCreation, retainCreation } from './retained'

const actor = '00000000-0001-7000-8000-000000000000'
const project = '00000000-0002-7000-8000-000000000000'
const other = '00000000-0003-7000-8000-000000000000'
const destination = { remote: false, namespaceId: '' }
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
type Call = { method: string; path: string; body: unknown }
function memory(): Storage {
  const values = new Map<string, string>()
  return { get length() { return values.size }, clear: () => values.clear(), key: (n) => [...values.keys()][n] ?? null,
    getItem: (k) => values.get(k) ?? null, setItem: (k, v) => { values.set(k, v) }, removeItem: (k) => { values.delete(k) } }
}
function harness(reply?: (call: Call) => Response | undefined | Promise<Response | undefined>) {
  vi.stubGlobal('localStorage', memory())
  const calls: Call[] = []
  const lines: ProgressLine[] = []
  const ctx: CreationContext = { actorId: actor, storage: memory(), assertActive: () => {} }
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const call = { method: init?.method ?? 'GET', path: url.replace(/^\/api\/v1/, ''), body: typeof init?.body === 'string' ? JSON.parse(init.body) : init?.body }
    calls.push(call)
    const result = await reply?.(call)
    if (result) return result
    if (call.path === '/authz/me') return json({ actorId: actor, user: 'same-name', instance: {} })
    if (call.path === '/projects' && call.method === 'POST') return json({ id: project, slug: 'canonical', name: 'Lunch orders' })
    if (call.path === '/projects/canonical') return json({ id: project, slug: 'canonical', lifecycle: { state: 'active' } })
    if (call.path.endsWith('/manifest')) return json({ project: { id: project, slug: 'canonical', name: 'Lunch orders' } })
    if (call.path.endsWith('/services')) return json({ needs: [] })
    if (call.path.endsWith('/pack')) return json({ guidanceImports: 1, workflowsInstalled: ['land'], missing: [] })
    if (call.path.endsWith('/repo/remote')) return json({ remote: 'https://git.example/team/repo.git' })
    return json({ defaultBranch: 'main', secrets: [] })
  }))
  const draft = { ...newDraft(), name: 'Lunch orders', description: 'Order lunches. Keep dietary requirements private.' }
  return { ctx, calls, lines, draft, run: () => createProject(draft, destination, (line) => lines.push(line), ctx) }
}
afterEach(() => vi.unstubAllGlobals())

describe('the retained local creation request', () => {
  it('uses the returned canonical identity, distinct operation IDs and full submitted setup', async () => {
    const h = harness()
    h.draft.packs = ['server']
    h.draft.services = [{ capability: 'postgres', params: { version: '16' } }]
    await createProject(h.draft, { remote: true, namespaceId: '42' }, (line) => h.lines.push(line), h.ctx)
    const first = h.calls.find((c) => c.path === '/projects')!.body as Record<string, unknown>
    expect(first).toMatchObject({ requestId: h.draft.creationRequestId, automaticAddress: true, slug: 'lunch-orders', blurb: h.draft.description, packs: ['server'], setup: {
      origin: 'empty', services: h.draft.services, upstream: { enabled: true, namespaceId: 42, repositoryName: 'lunch-orders', attachmentRequestId: h.draft.attachmentRequestId },
    } })
    expect(h.calls.filter((c) => c.method === 'POST' && c.path !== '/projects').every((c) => c.path.startsWith('/projects/canonical/'))).toBe(true)
    expect(h.calls.find((c) => c.path.endsWith('/repo/remote'))?.body).toMatchObject({ requestId: h.draft.attachmentRequestId, repositoryName: 'lunch-orders' })
    expect(readCreation(h.ctx.storage, actor)?.project).toEqual({ id: project, slug: 'canonical' })
  })
  it('replays the identical request after a lost response and refuses changed input', async () => {
    let first = true
    const h = harness((c) => { if (c.path === '/projects' && first) { first = false; throw new Error('response lost') }; return undefined })
    await expect(h.run()).rejects.toThrow('response lost')
    expect(readCreation(h.ctx.storage, actor)?.pending).toBe('project')
    await expect(createProject({ ...h.draft, description: 'Different software' }, destination, () => {}, h.ctx)).rejects.toThrow('different request')
    await continueCreation(h.ctx, () => {})
    const requests = h.calls.filter((c) => c.path === '/projects')
    expect(requests).toHaveLength(2)
    expect(requests[0].body).toEqual(requests[1].body)
  })
  it('keeps a created identity when canonical resolution fails and does not use the proposal', async () => {
    let unavailable = true
    const h = harness((c) => c.path === '/projects/canonical' && unavailable ? json({ error: 'canonical unavailable' }, 503) : undefined)
    await expect(h.run()).rejects.toThrow('canonical unavailable')
    expect(h.calls.filter((c) => c.method === 'POST')).toHaveLength(1)
    unavailable = false
    await continueCreation(h.ctx, () => {})
    expect(h.calls.filter((c) => c.path === '/projects')).toHaveLength(1)
  })
  it('follows a canonical rename while refusing a different project UUID', async () => {
    const renamed = harness((c) => c.path === '/projects/canonical' ? json({ id: project, slug: 'renamed', lifecycle: { state: 'active' } }) : undefined)
    await renamed.run()
    expect(renamed.calls.some((c) => c.method === 'POST' && c.path === '/projects/renamed/repo')).toBe(true)
    expect(readCreation(renamed.ctx.storage, actor)?.project).toEqual({ id: project, slug: 'renamed' })
    const foreign = harness((c) => c.path === '/projects/canonical' ? json({ id: other, slug: 'foreign', lifecycle: { state: 'active' } }) : undefined)
    await expect(foreign.run()).rejects.toThrow('same active project')
    expect(foreign.calls.filter((c) => c.method === 'POST')).toHaveLength(1)
  })
  it('permits correcting an initial refusal but never forgets a refused replay of an unknown creation', async () => {
    const h = harness((c) => c.path === '/projects' ? json({ error: 'explicit address taken' }, 409) : undefined)
    await expect(h.run()).rejects.toThrow('explicit address taken')
    expect(readCreation(h.ctx.storage, actor)?.pending).toBe('rejected')
    await leaveSubmittedCreation(h.ctx)
    expect(readCreation(h.ctx.storage, actor)).toBeNull()
    let first = true
    const lost = harness((c) => { if (c.path === '/projects') { if (first) { first = false; throw new Error('lost') }; return json({ error: 'access revoked' }, 403) } })
    await expect(lost.run()).rejects.toThrow('lost')
    await expect(continueCreation(lost.ctx, () => {})).rejects.toThrow('access revoked')
    await expect(leaveSubmittedCreation(lost.ctx)).rejects.toThrow('original creation request')
  })
})

describe('unknown external setup outcomes', () => {
  it.each(['repository', 'pack', 'upstream'] as const)('does not repeat an uncertain %s effect', async (step) => {
    const path = { repository: '/repo/from-url', pack: '/pack', upstream: '/repo/remote' }[step]
    const h = harness((c) => c.path.endsWith(path) ? json({ error: 'outcome unavailable' }, 503) : undefined)
    h.draft.origin = 'url'; h.draft.sourceURL = 'https://git.example/source.git'; h.draft.packs = ['server']
    const where = { remote: step === 'upstream', namespaceId: '42' }
    await expect(createProject(h.draft, where, () => {}, h.ctx)).rejects.toThrow('outcome unavailable')
    expect(readCreation(h.ctx.storage, actor)?.pending).toBe(step)
    const unresolved = await continueCreation(h.ctx, () => {}).catch((e: Error) => e)
    expect(h.calls.filter((c) => c.method === 'POST' && c.path.endsWith(path))).toHaveLength(1)
    expect(unresolved).toBeInstanceOf(Error)
    expect((unresolved as Error).message).toContain('will not be repeated automatically')
    expect(h.calls.filter((c) => c.path === '/projects')).toHaveLength(1)
  })
  it('does not replace services after an unavailable or differing recovery read', async () => {
    let unavailable = true
    const h = harness((c) => c.path.endsWith('/services') && c.method === 'GET' && unavailable ? json({ error: 'declaration unavailable' }, 503) : undefined)
    h.draft.services = [{ capability: 'postgres' }]
    await expect(h.run()).rejects.toThrow('declaration unavailable')
    expect(readCreation(h.ctx.storage, actor)?.pending).toBe('services-read')
    unavailable = false
    await expect(continueCreation(h.ctx, () => {})).rejects.toThrow('will not overwrite')
    expect(h.calls.filter((c) => c.method === 'POST' && c.path.endsWith('/services'))).toEqual([])
  })
  it('uses current declaration equality to resolve a lost service response without another write', async () => {
    let applied = false
    const h = harness((c) => {
      if (!c.path.endsWith('/services')) return undefined
      if (c.method === 'POST') { applied = true; return json({ error: 'response unavailable' }, 503) }
      return json({ needs: applied ? [{ capability: 'postgres', params: {} }] : [] })
    })
    h.draft.services = [{ capability: 'postgres' }]
    await expect(h.run()).rejects.toThrow('response unavailable')
    expect(readCreation(h.ctx.storage, actor)?.pending).toBe('services')
    await continueCreation(h.ctx, () => {})
    expect(h.calls.filter((c) => c.method === 'POST' && c.path.endsWith('/services'))).toHaveLength(1)
    expect(readCreation(h.ctx.storage, actor)?.pending).toBeNull()
  })
  it('keeps the existing starter declaration without an unnecessary edit', async () => {
    const h = harness((c) => c.path.endsWith('/services') ? json({ needs: [{ capability: 'postgres', params: { version: '16' } }] }) : undefined)
    h.draft.services = [{ capability: 'postgres', params: { version: '16' } }]
    await h.run()
    expect(h.calls.filter((c) => c.method === 'POST' && c.path.endsWith('/services'))).toEqual([])
    expect(h.lines.some((line) => line.text.includes('declared by the starter'))).toBe(true)
  })
  it('requires matching reselected bundle bytes before an unstarted import', async () => {
    const h = harness()
    const original = new File(['history'], 'original.bundle')
    const saved = await captureCreation(actor, { ...h.draft, origin: 'bundle', bundleFile: original }, destination)
    retainCreation(h.ctx.storage, { ...saved, project: { id: project, slug: 'canonical' }, pending: 'canonical' })
    h.ctx.file = new File(['different'], 'original.bundle')
    await expect(continueCreation(h.ctx, () => {})).rejects.toThrow('does not match')
    expect(h.calls.filter((c) => c.method === 'POST')).toEqual([])
    h.ctx.file = new File(['history'], 'renamed.bundle')
    await continueCreation(h.ctx, () => {})
    expect(h.calls.filter((c) => c.path.includes('/repo/from-bundle'))).toHaveLength(1)
  })
})

describe('current identity and persistence before effects', () => {
  it('does not create without retaining its request first', async () => {
    const h = harness()
    h.ctx.storage.setItem = () => { throw new Error('storage unavailable') }
    await expect(h.run()).rejects.toThrow('storage unavailable')
    expect(h.calls.filter((c) => c.method === 'POST')).toEqual([])
  })
  it('does not treat an unavailable current identity as permission to create', async () => {
    const h = harness((c) => c.path === '/authz/me' ? json({ error: 'identity unavailable' }, 503) : undefined)
    await expect(h.run()).rejects.toThrow('identity unavailable')
    expect(h.calls.filter((c) => c.method === 'POST')).toEqual([])
  })
  it('does not resume another actor’s saved request', async () => {
    const h = harness((c) => c.path === '/authz/me' ? json({ actorId: other, user: 'same-name' }) : undefined)
    const saved = await captureCreation(actor, h.draft, destination)
    retainCreation(h.ctx.storage, saved)
    const result = await continueCreation(h.ctx, () => {}).catch((e: Error) => e)
    expect(h.calls.filter((c) => c.method === 'POST')).toEqual([])
    expect((result as Error).message).toContain('account changed')
  })
  it('stops an old actor’s reply before any further setup call', async () => {
    let deliver!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => { deliver = resolve })
    const h = harness((c) => c.path === '/projects' ? pending : undefined)
    let active = true
    h.ctx.assertActive = () => { if (!active) throw new Error('identity changed') }
    const run = h.run()
    while (!h.calls.some((c) => c.path === '/projects')) await new Promise((resolve) => setTimeout(resolve, 0))
    active = false
    deliver(json({ id: project, slug: 'canonical' }))
    await expect(run).rejects.toThrow('identity changed')
    expect(h.calls.filter((c) => c.method === 'POST')).toHaveLength(1)
    expect(readCreation(h.ctx.storage, actor)?.pending).toBe('project')
  })
  it('creates the first version only on explicit request and returns its admitted change', async () => {
    const h = harness((c) => c.path.endsWith('/changes') ? json({ change: { id: other } }) : undefined)
    await h.run()
    expect(h.calls.some((c) => c.path.endsWith('/changes'))).toBe(false)
    const result = await buildFirstVersion(h.ctx)
    expect(result).toMatchObject({ slug: 'canonical', change: { change: { id: other } } })
    expect(h.calls.find((c) => c.path.endsWith('/changes'))?.body).toMatchObject({ body: h.draft.description })
  })
  it('keeps unknown first-version admission separate and never duplicates it', async () => {
    const h = harness((c) => c.path.endsWith('/changes') ? json({ error: 'change response lost' }, 503) : undefined)
    await h.run()
    await expect(buildFirstVersion(h.ctx)).rejects.toThrow('change response lost')
    const result = await buildFirstVersion(h.ctx).catch((e: Error) => e)
    const calls = h.calls.filter((c) => c.path.endsWith('/changes'))
    expect(calls).toHaveLength(1)
    expect((result as Error).message).toContain('project’s changes')
    expect(calls[0].body).toMatchObject({ body: h.draft.description })
  })
})
