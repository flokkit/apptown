import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { GlanceRow } from './GlanceRow'

const row = () => renderToStaticMarkup(<table><tbody><GlanceRow name={0} state={2} fact={3} labels={['Host', 'Path', 'State', 'Disk', 'Action']}>
  <td>Kitchen</td><td>/var/lib/docker</td><td>working</td><td>46% free</td><td><button>Refresh</button></td>
</GlanceRow></tbody></table>)
afterEach(() => vi.unstubAllGlobals())
describe('a glance row', () => {
  it('puts paths and secondary actions behind a disclosure on phones', () => {
    vi.stubGlobal('window', { matchMedia: () => ({ matches: true }) })
    const html = row()
    const firstView = html.split('<details')[0]
    expect(firstView).toContain('Kitchen')
    expect(firstView).toContain('working')
    expect(firstView).toContain('46% free')
    expect(firstView).not.toContain('/var/lib/docker')
    expect(firstView).not.toContain('Refresh')
    expect(html).toContain('aria-expanded="false"')
    expect(html).not.toContain('<summary>Details</summary>')
    expect(html).not.toContain('<button')
  })
  it('keeps the full columns on desktop', () => {
    vi.stubGlobal('window', { matchMedia: () => ({ matches: false }) })
    const html = row()
    expect(html).toContain('/var/lib/docker')
    expect(html).not.toContain('<details')
  })
})
