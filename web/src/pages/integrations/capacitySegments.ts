import type { Figure, TargetCapacity, Tenant } from '../../wire.gen'

// The capacity bars' arithmetic, apart from their drawing: which segments a
// bar holds, how wide, and what the rest of it is. A reading AppTown
// could not make is never drawn as free, and a container with no limit is
// never drawn as a size.

export type Kind = 'deployment' | 'previews' | 'work' | 'apptown' | 'unrecorded' | 'other' | 'host' | 'free' | 'unread' | 'over'

export type Segment = {
  key: string
  kind: Kind
  name: string
  value: number
  // what the segment is in words, for hover and the table: its value
  // and, for a collapsed or unread one, what it holds or why.
  detail?: string
  project?: string
  deployment?: string
}

export type Bar = {
  segments: Segment[]
  // scale is what 100% of the bar is: the total, or the promise when it is
  // larger; mark is where the total falls on that scale (0..1).
  scale: number
  mark: number
  // unlimited counts the containers with no limit, shown beside the bar.
  unlimited: number
  // unknown lists the holders whose figure is unknown: they are named, not drawn.
  unknown: { name: string; reason: string }[]
}

// A segment under this share of its bar joins its kind's "N more".
export const collapseBelow = 0.02

const tenantKind = (k: string): Kind =>
  (['deployment', 'previews', 'work', 'apptown', 'unrecorded', 'other'] as const).includes(k as never) ? (k as Kind) : 'unrecorded'

// collapse folds each kind's small segments into one "N more <kind>", so
// the bar stays readable on a host with dozens of deployments.
export function collapse(segs: Segment[], scale: number): Segment[] {
  if (scale <= 0) return segs
  const out: Segment[] = []
  const small = new Map<Kind, Segment[]>()
  for (const s of segs) {
    if (s.value / scale < collapseBelow && s.kind !== 'free' && s.kind !== 'unread' && s.kind !== 'over' && s.kind !== 'host') {
      small.set(s.kind, [...(small.get(s.kind) ?? []), s])
    } else {
      out.push(s)
    }
  }
  for (const [kind, list] of small) {
    if (list.length === 1) {
      out.push(list[0])
      continue
    }
    const value = list.reduce((a, s) => a + s.value, 0)
    out.push({ key: `more-${kind}`, kind, name: `${list.length} more`, value, detail: list.map((s) => s.name).join(', ') })
  }
  // Holders first, largest first; then the host's own, then what is left.
  const rank = (k: Kind) => (k === 'host' ? 1 : k === 'unread' ? 2 : k === 'free' ? 3 : k === 'over' ? 4 : 0)
  return out.sort((a, b) => rank(a.kind) - rank(b.kind) || b.value - a.value)
}

function holders(tenants: Tenant[], fig: (t: Tenant) => Figure | number) {
  const segs: Segment[] = []
  const unknown: Bar['unknown'] = []
  for (const t of tenants) {
    const f = fig(t)
    const v = typeof f === 'number' ? { known: true, value: f, reason: '' } : f
    if (!v.known) {
      unknown.push({ name: t.name, reason: v.reason || 'not read' })
      continue
    }
    if (v.value <= 0) continue
    segs.push({ key: `${t.kind}:${t.name}`, kind: tenantKind(t.kind), name: t.name, value: v.value, project: t.project, deployment: t.deployment })
  }
  return { segs, unknown }
}

// usedMemory is the memory in use: each holder's, then the host's own (its
// use outside the containers), then free. When the host's use or any
// holder's is unknown, what is left after the known holders is "not read",
// never free.
export function usedMemory(c: TargetCapacity): Bar | null {
  const m = c.memory
  if (!m || !m.total.known) return null
  const total = m.total.value
  const { segs, unknown } = holders(c.tenants ?? [], (t) => t.used)
  const held = segs.reduce((a, s) => a + s.value, 0)
  const host = m.hostUsed
  let rest: Segment[]
  if (host?.known && unknown.length === 0 && host.value >= held && host.value <= total) {
    rest = [
      { key: 'host', kind: 'host', name: 'the host itself', value: host.value - held, detail: 'the system, caches and processes outside containers' },
      { key: 'free', kind: 'free', name: 'free', value: total - host.value },
    ]
  } else {
    const why = !host?.known
      ? host?.reason || "the host's own use is not read"
      : unknown.length > 0
        ? `${unknown.length} holder(s) not read`
        : 'the readings disagree (taken moments apart)'
    rest = [{ key: 'unread', kind: 'unread', name: 'not read', value: Math.max(0, total - held), detail: why }]
  }
  return { segments: collapse([...segs, ...rest], total), scale: Math.max(total, held), mark: 1, unlimited: 0, unknown }
}

// promised is what the running containers are promised of a resource: each
// holder's limits, against the total; a promise beyond the total runs past
// the mark. Containers with no limit are counted beside the bar. A CPU
// limit AppTown could not read as a number makes the sum partial: the
// rest of the bar is then "not read", never "not promised" (CV-C02).
export function promised(c: TargetCapacity, resource: 'memory' | 'cpu'): (Bar & { sum: number; partial: number }) | null {
  const r = resource === 'memory' ? c.memory : c.cpu
  if (!r || !r.total.known) return null
  const total = r.total.value
  const tenants = c.tenants ?? []
  const { segs, unknown } = holders(tenants, (t) => (resource === 'memory' ? t.promised : t.cpus))
  const sum = segs.reduce((a, s) => a + s.value, 0)
  const unlimited = tenants.reduce((a, t) => a + (resource === 'memory' ? t.unlimited : t.cpuUnlimited), 0)
  const partial = resource === 'cpu' ? tenants.reduce((a, t) => a + t.cpuUnread, 0) : 0
  if (partial > 0) {
    for (const t of tenants) if (t.cpuUnread > 0) unknown.push({ name: t.name, reason: `${t.cpuUnread} limit(s) not read as a number of CPUs` })
  }
  const scale = Math.max(total, sum)
  const rest: Segment[] = sum >= total ? [] : partial > 0
    ? [{ key: 'unread', kind: 'unread', name: 'not read', value: total - sum, detail: `${partial} limit(s) not read: the promise is at least what is drawn` }]
    : [{ key: 'free', kind: 'free', name: 'not promised', value: total - sum }]
  return { segments: collapse([...segs, ...rest], scale), scale, mark: total / scale, unlimited, unknown, sum, partial }
}

// disk is the Docker data disk: used and free.
export function disk(c: TargetCapacity): Bar | null {
  const d = c.disk
  if (!d || !d.total.known || !d.free.known) return null
  const total = d.total.value
  return {
    segments: [
      { key: 'used', kind: 'other', name: 'used', value: total - d.free.value },
      { key: 'free', kind: 'free', name: 'free', value: d.free.value },
    ],
    scale: total, mark: 1, unlimited: 0, unknown: [],
  }
}
