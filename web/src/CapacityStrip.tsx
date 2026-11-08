import { fmtBytes } from './format'
import './styles/capacity-strip.css'
import type { TargetCapacity } from './wire.gen'

// basis says what the use counts when it is not the whole host's.
export type CapacityReading = { label: string; used?: number; total?: number; format?: (value: number) => string; basis?: string }

// Shared capacity display. Missing or incomplete readings stay unknown; a
// known use with nothing to measure it against (unlimited) says so.
export function CapacityStrip({ name, used, total, unlimited = false, format = fmtBytes, basis }: {
  name: string; used?: number; total?: number; unlimited?: boolean; format?: (value: number) => string; basis?: string
}) {
  const known = used !== undefined && total !== undefined && Number.isFinite(used) && Number.isFinite(total) && used >= 0 && total > 0
  const percent = known ? Math.min(100, used / total * 100) : 0
  const words = known ? `${format(used!)} / ${format(total!)}${basis ? ` ${basis}` : ''}` : unlimited && used !== undefined && Number.isFinite(used) ? `${format(used)} · no limit` : 'unknown'
  return <div className={`capacity-strip${known && percent > 80 ? ' capacity-strip-high' : ''}`}>
    <div className="capacity-strip-label"><h3>{name}</h3><span>{words}</span></div>
    <div className="capacity-strip-track" role="meter" aria-label={`${name} use`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={known ? percent : undefined} aria-valuetext={known ? `${format(used!)} used of ${format(total!)}${basis ? ` ${basis}` : ''}` : words}>
      <svg className="capacity-strip-fill" width={`${percent}%`} height="100%" aria-hidden="true"><rect width="100%" height="100%" /></svg>
    </div>
  </div>
}

export function targetCapacityReadings(c?: TargetCapacity): CapacityReading[] {
  const current = (f: { known: boolean; stale?: boolean; value: number } | null | undefined) => f?.known && !f.stale && !c?.stale ? f.value : undefined
  const diskTotal = current(c?.disk?.total), diskFree = current(c?.disk?.free)
  // Use is the whole host's when the host reports it. Otherwise it is what
  // the containers use, said as such, and only when every reading is known;
  // limits are never read as use.
  const sum = (values: (number | undefined)[]) => values.length > 0 && values.every(v => v !== undefined) ? values.reduce((a, b) => a! + b!, 0) : undefined
  const tenants = (c?.tenants ?? []).filter(t => !t.stale)
  // Only the tenants say whether every container was read: the in-use split
  // can be known while a container is missing from it.
  const complete = c?.tenants !== undefined && tenants.length === c.tenants.length
  const containersCPU = complete ? sum(tenants.map(t => current(t.cpuUsed))) : undefined
  const containersMemory = complete ? sum(tenants.map(t => current(t.used))) : undefined
  const reading = (label: string, host: number | undefined, containers: number | undefined, total: number | undefined, format?: (v: number) => string): CapacityReading =>
    host !== undefined ? { label, used: host, total, format } : { label, used: containers, total, format, basis: containers !== undefined ? 'by containers' : undefined }
  return [
    reading('CPU', current(c?.cpu?.hostUsed), containersCPU, current(c?.cpu?.total), v => `${Number(v.toFixed(2))}`),
    reading('MEMORY', current(c?.memory?.hostUsed), containersMemory, current(c?.memory?.total)),
    { label: 'DISK', used: diskTotal !== undefined && diskFree !== undefined ? diskTotal - diskFree : undefined, total: diskTotal },
  ]
}
