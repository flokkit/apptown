import { fmtBytes } from './format'
import './styles/capacity-strip.css'
import type { TargetCapacity } from './wire.gen'

export type CapacityReading = { label: string; used?: number; total?: number; format?: (value: number) => string }

// Shared capacity display. Missing or incomplete readings stay unknown; a
// known use with nothing to measure it against (unlimited) says so.
export function CapacityStrip({ name, used, total, unlimited = false, format = fmtBytes }: {
  name: string; used?: number; total?: number; unlimited?: boolean; format?: (value: number) => string
}) {
  const known = used !== undefined && total !== undefined && Number.isFinite(used) && Number.isFinite(total) && used >= 0 && total > 0
  const percent = known ? Math.min(100, used / total * 100) : 0
  const words = known ? `${format(used!)} / ${format(total!)}` : unlimited && used !== undefined && Number.isFinite(used) ? `${format(used)} · no limit` : 'unknown'
  return <div className={`capacity-strip${known && percent > 80 ? ' capacity-strip-high' : ''}`}>
    <div className="capacity-strip-label"><h3>{name}</h3><span>{words}</span></div>
    <div className="capacity-strip-track" role="meter" aria-label={`${name} use`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={known ? percent : undefined} aria-valuetext={known ? `${format(used!)} used of ${format(total!)}` : words}>
      <svg className="capacity-strip-fill" width={`${percent}%`} height="100%" aria-hidden="true"><rect width="100%" height="100%" /></svg>
    </div>
  </div>
}

export function targetCapacityReadings(c?: TargetCapacity): CapacityReading[] {
  const current = (f: { known: boolean; stale?: boolean; value: number } | null | undefined) => f?.known && !f.stale && !c?.stale ? f.value : undefined
  const diskTotal = current(c?.disk?.total), diskFree = current(c?.disk?.free)
  // CPU usage and whole-host memory are not inferred from limits or container sums.
  return [
    { label: 'CPU', used: current(c?.cpu?.hostUsed), total: current(c?.cpu?.total), format: v => `${Number(v.toFixed(2))}` },
    { label: 'MEMORY', used: current(c?.memory?.hostUsed), total: current(c?.memory?.total) },
    { label: 'DISK', used: diskTotal !== undefined && diskFree !== undefined ? diskTotal - diskFree : undefined, total: diskTotal },
  ]
}
