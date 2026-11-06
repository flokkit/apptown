import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { CapacityStrip } from './CapacityStrip'

describe('disk capacity strip', () => {
  it('keeps absent or explicitly unknown capacity empty', () => {
    for (const props of [{}, { totalBytes: 100 }, { totalBytes: 0, freeBytes: 0 }, { totalBytes: 100, freeBytes: 1, unknown: true }]) {
      const html = renderToStaticMarkup(<CapacityStrip name="Data" {...props} />)
      expect(html).toContain('unknown')
      expect(html).toContain('width="0%"')
      expect(html).not.toContain('aria-valuenow')
      expect(html).not.toContain('capacity-strip-high')
    }
  })
  it('colours only a reading strictly above eighty percent', () => {
    const render = (freeBytes: number) => renderToStaticMarkup(<CapacityStrip name="Data" totalBytes={100} freeBytes={freeBytes} />)
    expect(render(20)).not.toContain('capacity-strip-high')
    expect(render(19)).toContain('capacity-strip-high')
    expect(render(19)).toContain('aria-valuenow="81"')
    expect(render(100)).toContain('0 B / 100 B')
    expect(render(100)).not.toContain('unknown')
  })
})
