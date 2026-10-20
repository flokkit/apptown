// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createProject, type ProgressLine } from './create'
import { newDraft } from './draft'

// A way of working whose starter already declares the picked services is
// not a skip: creating the project says the services are there, from the
// starter, and asks the server for no edit with nothing to change. (The
// desktop trial, 20 Oct 2026, said "services skipped" with Postgres picked.)
describe('creating a project whose starter declares its services', () => {
  afterEach(() => { vi.unstubAllGlobals() })
  const run = async (declared: { capability: string; params?: Record<string, string> }[]) => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      calls.push(`${method} ${url}`)
      if (url.endsWith('/pack')) return new Response(JSON.stringify({ guidanceImports: 1, workflowsInstalled: ['land'], missing: [] }))
      if (url.endsWith('/services') && method === 'GET') return new Response(JSON.stringify({ needs: declared, contract: 'app.yaml', declaration: 'v1' }))
      if (url.endsWith('/services')) return new Response(JSON.stringify({ error: 'these are the services declared now; nothing to change' }), { status: 422 })
      return new Response('{}')
    }))
    const lines: ProgressLine[] = []
    const d = { ...newDraft(), name: 'Lunch Orders', pack: 'django-htmx', services: [{ capability: 'postgres' }] }
    await createProject(d, { remote: false, namespaceId: '' }, (l) => lines.push(l))
    return { lines, calls }
  }
  it('says the starter declares them, and edits nothing', async () => {
    const { lines, calls } = await run([{ capability: 'postgres', params: { version: '16' } }])
    expect(lines.map((l) => l.text)).toContain('✓ services: postgres (declared by the starter)')
    expect(lines.filter((l) => l.warn)).toEqual([])
    expect(calls.filter((c) => c.startsWith('POST') && c.endsWith('/services'))).toEqual([])
  })
  it('still adds services the starter does not declare', async () => {
    const { lines, calls } = await run([])
    expect(calls.filter((c) => c.startsWith('POST') && c.endsWith('/services')).length).toBe(1)
    expect(lines.some((l) => l.warn && l.text.includes('services not added'))).toBe(true)
  })
})
