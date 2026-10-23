// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { newQueryClient } from '../data'
import type { DocsIndex } from '../wire.gen'
import DocsPage from './Docs'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

vi.mock('mermaid', () => ({ default: { initialize: vi.fn(), run: vi.fn(async () => {}) } }))

const index: DocsIndex = {
  groups: [
    { title: 'Start here', guides: [{ slug: 'quickstart', title: 'Quickstart' }] },
    { title: 'How AppTown works', guides: [{ slug: 'architecture', title: 'How AppTown works' }] },
  ],
  integrations: [{ capability: 'llm', provider: 'openrouter', title: 'AI models (OpenRouter)' }],
}

let where = ''
function Where() { where = useLocation().pathname; return null }

async function render(answer: (url: string) => Response, start = '/docs') {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => answer(url)))
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => {
    root.render(
      <QueryClientProvider client={newQueryClient()}>
        <MemoryRouter initialEntries={[start]}>
          <Routes>
            <Route path="/docs" element={<><DocsPage /><Where /></>} />
            <Route path="/docs/:section/:slug" element={<><DocsPage /><Where /></>} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )
  })
  return { el, root }
}

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status })

describe('the docs page', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  // Guides appear under their group headings in the index's order, and the
  // page opens on the first guide of the first group, not the first file.
  it('shows group headings in order and opens on Start here', async () => {
    const { el, root } = await render((url) => url.endsWith('/docs') ? json(index)
      : url.endsWith('/docs/guides/quickstart') ? json({ slug: 'quickstart', content: '# Quickstart\n\nInstall AppTown.' })
      : json({ error: 'no' }, 404))
    await vi.waitFor(() => expect(where).toBe('/docs/guides/quickstart'))
    const headings = [...el.querySelectorAll('nav .docs-group-title')].map((h) => h.textContent)
    expect(headings).toEqual(['Start here', 'How AppTown works', 'Integrations'])
    const labels = [...el.querySelectorAll('select optgroup')].map((g) => g.getAttribute('label'))
    expect(labels).toEqual(['Start here', 'How AppTown works', 'Integrations'])
    await vi.waitFor(() => expect(el.querySelector('.md-doc')?.textContent).toContain('Install AppTown.'))
    act(() => root.unmount())
  })

  // A shipped doc links another by its file, for GitHub; the page opens
  // that doc's route instead, from a guide and from either integration tab.
  it('points links to shipped docs at their routes', async () => {
    const guide = '[a](split-install.md) [b](../integrations/llm-openrouter.admin.md) [c](https://example.com/x.md)'
    const integ = { admin: '[d](../guides/secrets.md)', developer: '[e](model-pi.developer.md)' }
    const hrefs = (el: HTMLElement) => [...el.querySelectorAll('.md-doc a')].map((a) => a.getAttribute('href'))
    const answer = (url: string) => url.endsWith('/docs') ? json(index)
      : url.endsWith('/docs/guides/quickstart') ? json({ slug: 'quickstart', content: guide })
      : url.endsWith('/integrations/docs/llm/openrouter') ? json(integ)
      : json({ error: 'no' }, 404)
    const g = await render(answer, '/docs/guides/quickstart')
    await vi.waitFor(() => expect(hrefs(g.el)).toEqual(['/docs/guides/split-install', '/docs/llm/openrouter', 'https://example.com/x.md']))
    act(() => g.root.unmount())
    const i = await render(answer, '/docs/llm/openrouter')
    await vi.waitFor(() => expect(hrefs(i.el)).toEqual(['/docs/model/pi']))
    act(() => i.el.querySelectorAll<HTMLButtonElement>('.tabs .tab')[1].click())
    await vi.waitFor(() => expect(hrefs(i.el)).toEqual(['/docs/guides/secrets']))
    act(() => i.root.unmount())
  })

  // A failed first read says so; it is not shown as an empty list.
  it('says when the list cannot be read', async () => {
    const { el, root } = await render(() => json({ error: 'AppTown unavailable' }, 404))
    await vi.waitFor(() => expect(el.textContent).toContain('The documentation list could not be read'))
    expect(el.querySelector('nav')).toBeNull()
    act(() => root.unmount())
  })

  // An empty list says there is nothing, rather than a blank page.
  it('says when there is no documentation', async () => {
    const { el, root } = await render(() => json({ groups: [], integrations: [] }))
    await vi.waitFor(() => expect(el.textContent).toContain('AppTown ships no documentation.'))
    expect(where).toBe('/docs')
    act(() => root.unmount())
  })
})
