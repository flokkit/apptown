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


// Without the host's own figure, a strip shows what the containers use and
// says so, to the eye and to a screen reader; it never presents that, or a
// promise, as the whole host's use. The tenants say whether every container
// was read: a missing or stale one leaves the total unknown, even when the
// in-use split looks known.
it('shows container use as container use, never as whole-host use, and keeps partial or stale readings unknown', () => {
  const known = (value: number) => ({ known: true, value })
  const tenant = (used: { known: boolean; value: number }, cpuUsed = known(0.5)) => ({ used, cpuUsed })
  const base = { memory: { total: known(100), inUse: { apptown: known(60), other: known(5) }, promised: { sum: { apptown: known(90), other: known(0) } } }, cpu: { total: known(4) },
    disk: { total: known(100), free: { ...known(20), stale: true } }, tenants: [tenant(known(60)), tenant(known(5))] }
  const [cpu, memory, disk] = targetCapacityReadings(base as unknown as TargetCapacity)
  expect(memory).toMatchObject({ used: 65, basis: 'by containers' })
  expect(cpu).toMatchObject({ used: 1, basis: 'by containers' })
  const markup = renderToStaticMarkup(<CapacityStrip name={memory.label} used={memory.used} total={memory.total} basis={memory.basis} />)
  expect(markup).toContain('65 B / 100 B by containers')
  expect(markup).toContain('aria-valuetext="65 B used of 100 B by containers"')
  expect(disk.used).toBeUndefined()
  // A container missing from the readings: the split is known, the total is not.
  const missing = targetCapacityReadings({ ...base, tenants: [tenant(known(60)), tenant({ known: false, value: 0 })] } as unknown as TargetCapacity)
  expect(missing[1].used).toBeUndefined()
  // No tenants read, or a stale one: unknown too.
  expect(targetCapacityReadings({ ...base, tenants: undefined } as unknown as TargetCapacity)[1].used).toBeUndefined()
  expect(targetCapacityReadings({ ...base, tenants: [tenant(known(60)), { ...tenant(known(5)), stale: true }] } as unknown as TargetCapacity)[0].used).toBeUndefined()
})

it("prefers the host's own use and then names no basis", () => {
  const known = (value: number) => ({ known: true, value })
  const [, memory] = targetCapacityReadings({ memory: { total: known(100), hostUsed: known(80), inUse: { apptown: known(60), other: known(5) } } } as unknown as TargetCapacity)
  expect(memory).toMatchObject({ used: 80 })
  expect(memory.basis).toBeUndefined()
})


it('shows whole-host memory and CPU figures on the target strips', () => {
  const known = (value: number) => ({ known: true, value })
  const c = { cpu: { total: known(1), hostUsed: known(0.4) }, memory: { total: known(2072321720), hostUsed: known(1600000000), inUse: { apptown: known(100), other: known(200) } } } as TargetCapacity
  const [cpu, memory] = targetCapacityReadings(c)
  expect(cpu.used).toBe(0.4)
  expect(cpu.total).toBe(1)
  expect(memory.used).toBe(1600000000)
  expect(memory.total).toBe(2072321720)
  for (const r of [cpu, memory]) expect(renderToStaticMarkup(<CapacityStrip name={r.label} used={r.used} total={r.total} format={r.format} />)).not.toContain('unknown')
})
