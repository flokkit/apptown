import { fmtBytes } from './format'
import './styles/capacity-strip.css'

// Shared disk capacity display. Missing capacity stays unknown, never empty.
export function CapacityStrip({ name, totalBytes, freeBytes, unknown = false }: {
  name: string; totalBytes?: number; freeBytes?: number; unknown?: boolean
}) {
  const known = !unknown && !!totalBytes && freeBytes !== undefined
  const used = known ? Math.max(0, totalBytes! - freeBytes!) : 0
  const percent = known ? Math.min(100, used / totalBytes! * 100) : 0
  return <div className={`capacity-strip${known && percent > 80 ? ' capacity-strip-high' : ''}`}>
    <div className="capacity-strip-label"><h3>{name}</h3><span>{known ? `${fmtBytes(used)} / ${fmtBytes(totalBytes!)}` : 'unknown'}</span></div>
    <div className="capacity-strip-track" role="meter" aria-label={`${name} disk use`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={known ? percent : undefined} aria-valuetext={known ? `${fmtBytes(used)} used of ${fmtBytes(totalBytes!)}` : 'unknown'}>
      <svg className="capacity-strip-fill" width={`${percent}%`} height="100%" aria-hidden="true"><rect width="100%" height="100%" /></svg>
    </div>
  </div>
}
