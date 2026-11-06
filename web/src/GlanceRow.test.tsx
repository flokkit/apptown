import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { GlanceRow } from './GlanceRow'

const row = () => renderToStaticMarkup(<table><tbody><GlanceRow labels={['Host', 'Path', 'State', 'Disk', 'Action']}>
  <td>Kitchen</td><td>/var/lib/docker</td><td>working</td><td>46% free</td><td><button>Refresh</button></td>
</GlanceRow></tbody></table>)
afterEach(() => vi.unstubAllGlobals())
describe('a glance row', () => {
  it('keeps paths and secondary actions inline on phones', () => {
    vi.stubGlobal('window', { matchMedia: () => ({ matches: true }) })
    const html = row()
    const firstView = html.split('<details')[0]
    expect(firstView).toContain('Kitchen')
    expect(firstView).toContain('working')
    expect(firstView).toContain('46% free')
    expect(firstView).toContain('/var/lib/docker')
    expect(firstView).toContain('Refresh')
    expect(html).not.toContain('aria-expanded')
    expect(html).not.toContain('<summary>Details</summary>')
    expect(html).toContain('<button')
  })
  it('keeps the full columns on desktop', () => {
    vi.stubGlobal('window', { matchMedia: () => ({ matches: false }) })
    const html = row()
    expect(html).toContain('/var/lib/docker')
    expect(html).not.toContain('<details')
  })
})
