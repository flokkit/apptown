import { describe, expect, it } from 'vitest'
import type { Figure, TargetCapacity, Tenant } from '../../wire.gen'
import { collapse, disk, promised, usedMemory } from './capacitySegments'

const fig = (value: number): Figure => ({ known: true, value })
const none = (reason: string): Figure => ({ known: false, value: 0, reason })
const GiB = 2 ** 30
const MiB = 2 ** 20
const tenant = (name: string, kind: string, used: Figure, prom: number, extra: Partial<Tenant> = {}): Tenant =>
  ({ kind, name, containers: 1, used, promised: fig(prom), unlimited: 0, cpus: 0, cpuUnlimited: 0, cpuUnread: 0, ...extra })
const cap = (tenants: Tenant[], hostUsed: Figure | null, total = 8 * GiB): TargetCapacity => ({
  target: 'ct', kind: 'docker-host', elastic: false, words: '', readAt: '2026-10-13T00:00:00Z', tenants,
  memory: { total: fig(total), promised: { sum: { forge: fig(0), other: fig(0) }, limited: { forge: 0, other: 0 }, unlimited: { forge: 0, other: 0 }, unread: { forge: 0, other: 0 } }, hostUsed, overcommitted: false },
  cpu: { total: fig(8), promised: { sum: { forge: fig(0), other: fig(0) }, limited: { forge: 0, other: 0 }, unlimited: { forge: 0, other: 0 }, unread: { forge: 0, other: 0 } }, overcommitted: false },
  disk: { total: fig(400 * GiB), free: fig(300 * GiB), low: false, refuses: false },
})

describe('the capacity bars', () => {
  // Used memory: the holders largest first, then the host's own use, then
  // free; the segments add up to what the host has.
  // Kills: free drawn when the host's use is unknown; the host's own use left out.
  it('draws used memory as holders, the host itself, then free', () => {
    const b = usedMemory(cap([tenant('shop/main', 'deployment', fig(700 * MiB), 0), tenant('the forge', 'forge', fig(GiB), 0)], fig(3 * GiB)))!
    expect(b.segments.map((s) => s.kind)).toEqual(['forge', 'deployment', 'host', 'free'])
    expect(b.segments.find((s) => s.kind === 'free')!.value).toBe(5 * GiB)
    expect(b.segments.find((s) => s.kind === 'host')!.value).toBe(3 * GiB - GiB - 700 * MiB)
    expect(b.segments.reduce((a, s) => a + s.value, 0)).toBe(8 * GiB)
  })
  it('never draws free when the host use or a holder is not read', () => {
    for (const c of [
      cap([tenant('shop/main', 'deployment', fig(GiB), 0)], none('not one machine')),
      cap([tenant('shop/main', 'deployment', none('stats failed'), 0), tenant('other containers', 'other', fig(GiB), 0)], fig(3 * GiB)),
      cap([tenant('shop/main', 'deployment', fig(4 * GiB), 0)], fig(3 * GiB)), // readings disagree
    ]) {
      const b = usedMemory(c)!
      expect(b.segments.some((s) => s.kind === 'free')).toBe(false)
      const unread = b.segments.find((s) => s.kind === 'unread')!
      expect(unread.detail).toBeTruthy()
    }
    const b = usedMemory(cap([tenant('shop/main', 'deployment', none('stats failed'), 0)], fig(GiB)))!
    expect(b.unknown).toEqual([{ name: 'shop/main', reason: 'stats failed' }])
  })

  // Promised: a promise past the total moves the mark inside the bar;
  // containers with no limit are counted beside it, never drawn as a size.
  // Kills: overcommit drawn inside the total; unlimited summed as a width.
  it('runs a promise past the total beyond the mark, and counts no-limit containers apart', () => {
    const c = cap([tenant('a/main', 'deployment', fig(0), 6 * GiB, { unlimited: 3 }), tenant('b/main', 'deployment', fig(0), 4 * GiB)], fig(GiB))
    const b = promised(c, 'memory')!
    expect(b.scale).toBe(10 * GiB)
    expect(b.mark).toBeCloseTo(0.8)
    expect(b.unlimited).toBe(3)
    expect(b.segments.some((s) => s.kind === 'free')).toBe(false)
    const room = promised(cap([tenant('a/main', 'deployment', fig(0), 2 * GiB)], fig(GiB)), 'memory')!
    expect(room.mark).toBe(1)
    expect(room.segments.find((s) => s.kind === 'free')!.value).toBe(6 * GiB)
  })

  // A host with dozens of deployments: each kind's small segments fold
  // into one "N more", keeping its kind's colour and naming what it holds.
  // Kills: small segments dropped (the bar no longer adds up); folded across kinds.
  it('folds small segments into one "N more" per kind', () => {
    const segs = collapse([
      { key: 'a', kind: 'deployment', name: 'big/main', value: 50 },
      { key: 'b', kind: 'deployment', name: 'tiny/main', value: 1 },
      { key: 'c', kind: 'deployment', name: 'wee/main', value: 1 },
      { key: 'd', kind: 'previews', name: 'previews', value: 1 },
      { key: 'f', kind: 'free', name: 'free', value: 47 },
    ], 100)
    expect(segs.map((s) => s.name)).toEqual(['big/main', '2 more', 'previews', 'free'])
    const more = segs.find((s) => s.name === '2 more')!
    expect(more.kind).toBe('deployment')
    expect(more.value).toBe(2)
    expect(more.detail).toBe('tiny/main, wee/main')
    expect(segs.reduce((a, s) => a + s.value, 0)).toBe(100)
  })

  it('draws the disk as used and free, and nothing when either is unknown', () => {
    const c = cap([], null)
    expect(disk(c)!.segments.map((s) => [s.name, s.value])).toEqual([['used', 100 * GiB], ['free', 300 * GiB]])
    expect(disk({ ...c, disk: { total: fig(1), free: none('df failed'), low: false, refuses: false } })).toBeNull()
  })

  // A CPU limit not read as a number makes the promise partial: the rest is
  // "not read", never "not promised", and the sum says "at least" (CV-C02).
  // Kills: an unread limit drawn as unpromised capacity.
  it('never calls the rest unpromised when a CPU limit was not read', () => {
    const allUnread = promised(cap([tenant('a/main', 'deployment', fig(0), 0, { cpuUnread: 1 })], fig(GiB)), 'cpu')!
    expect(allUnread.segments.map((s) => s.kind)).toEqual(['unread'])
    expect(allUnread.partial).toBe(1)
    const mixed = promised(cap([tenant('a/main', 'deployment', fig(0), 0, { cpus: 2 }), tenant('b/main', 'deployment', fig(0), 0, { cpuUnread: 2 })], fig(GiB)), 'cpu')!
    expect(mixed.sum).toBe(2)
    expect(mixed.segments.some((s) => s.kind === 'free')).toBe(false)
    expect(mixed.segments.find((s) => s.kind === 'unread')!.value).toBe(6)
    expect(mixed.unknown.map((u) => u.name)).toEqual(['b/main'])
  })
})

