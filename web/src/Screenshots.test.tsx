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
  const out = { text: el.textContent ?? '', imgs: el.querySelectorAll('img').length }
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
