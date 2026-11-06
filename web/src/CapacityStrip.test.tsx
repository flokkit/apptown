import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { TargetCapacity } from './wire.gen'
import { CapacityStrip, targetCapacityReadings } from './CapacityStrip'

describe('disk capacity strip', () => {
  it('keeps absent or explicitly unknown capacity empty', () => {
    for (const props of [{}, { total: 100 }, { total: 0, used: 0 }, { total: 100, used: undefined }]) {
      const html = renderToStaticMarkup(<CapacityStrip name="Data" {...props} />)
      expect(html).toContain('unknown')
      expect(html).toContain('width="0%"')
      expect(html).not.toContain('aria-valuenow')
      expect(html).not.toContain('capacity-strip-high')
    }
  })
  it('colours only a reading strictly above eighty percent', () => {
    const render = (freeBytes: number) => renderToStaticMarkup(<CapacityStrip name="Data" total={100} used={100 - freeBytes} />)
    expect(render(20)).not.toContain('capacity-strip-high')
    expect(render(19)).toContain('capacity-strip-high')
    expect(render(19)).toContain('aria-valuenow="81"')
    expect(render(100)).toContain('0 B / 100 B')
    expect(render(100)).not.toContain('unknown')
  })
})


it('does not present container use, promises, or stale readings as whole-host use', () => {
  const known = (value: number) => ({ known: true, value })
  const c = { memory: { total: known(100), inUse: { apptown: known(60), other: known(5) } }, disk: { total: known(100), free: { ...known(20), stale: true } } } as TargetCapacity
  const readings = targetCapacityReadings(c)
  expect(readings.map(r => r.used)).toEqual([undefined, undefined, undefined])
  for (const r of readings) expect(renderToStaticMarkup(<CapacityStrip name={r.label} used={r.used} total={r.total} />)).toContain('unknown')
})
