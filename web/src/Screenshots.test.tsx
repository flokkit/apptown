// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { Screenshots, sameCommitWords } from './Screenshots'
import type { PreviewScreenshots } from './api'

async function render(shots: PreviewScreenshots, head?: string) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ binary: true, bodyBase64: 'iVBORw0KGgo=' }))))
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<Screenshots shots={shots} head={head} />) })
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  const out = { html: el.innerHTML, text: el.textContent ?? '', imgs: el.querySelectorAll('img').length, captions: Array.from(el.querySelectorAll('figcaption'), (c) => c.textContent) }
  act(() => root.unmount())
  vi.unstubAllGlobals()
  return out
}

describe('the screenshots at approval', () => {
  const shots: PreviewScreenshots = { commit: 'aaaaaaa1111', shots: [{ artifact: 'a1', path: '/', view: 'desktop' }, { artifact: 'a2', path: '/', view: 'phone' }] }
  it('shows the pictures and says they are the change as it is', async () => {
    const { text, imgs } = await render(shots, 'aaaaaaa1111')
    expect(imgs).toBe(2)
    expect(text).toContain('from commit aaaaaaa')
    expect(text).toContain("this is the change's current commit")
  })
  it('names the view alone at root and keeps other paths', async () => {
    expect((await render(shots)).captions).toEqual(['Desktop', 'Phone'])
    expect((await render({ shots: [{ artifact: 'a3', path: '/results', view: 'phone' }] })).captions).toEqual(['/results · Phone'])
  })
  it('says plainly when they show a different commit', async () => {
    const { text } = await render(shots, 'bbbbbbb2222')
    expect(text).toContain('the change is now at bbbbbbb; these show a different commit, aaaaaaa')
  })
  it('never says current when it could not compare', async () => {
    const { text } = await render(shots)
    expect(text).toContain("could not compare with the change's current commit")
    expect(text).not.toContain("this is the change's current commit")
    expect(sameCommitWords('a', undefined).kind).toBe('unknown')
  })
  it('says why there are none, never an empty box', async () => {
    const { text, imgs } = await render({ commit: 'a', unavailable: 'the browser did not finish: timed_out' })
    expect(imgs).toBe(0)
    expect(text).toContain('Screenshots could not be taken: the browser did not finish')
  })
})

it('shows failed resources next to pictures with the list inline', async () => {
  const { html, text } = await render({ commit: 'abc', failedResources: [{ path: '/', view: 'desktop', type: 'Stylesheet', url: 'dead.example/style.css', status: 404 }] })
  expect(html).toContain('<section class="warn small"><p>1 resource failed to load</p>')
  expect(text).toContain('Stylesheet: dead.example/style.css — 404')
})

it('keeps the total and says how many failures were omitted', async () => {
 const { text } = await render({ failedResources: Array.from({length: 50}, () => ({path:'/', view:'desktop', type:'Image', url:'example.test/a', status:404})), failedResourcesMore: 3 })
 expect(text).toContain('53 resources failed to load')
 expect(text).toContain('and 3 more')
})
