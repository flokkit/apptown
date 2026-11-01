import { fmtWhen } from './format'
import type { DetailReading } from './wire.gen'

// Provider status is measured by jobs. A successful page fetch cannot renew it.
export function BackgroundReading({ reading }: { reading?: DetailReading }) {
  if (!reading) return null
  return <span className={reading.failure || reading.unknown ? 'muted' : 'muted small'}>
    {reading.failure && <>{reading.failure} </>}
    {reading.observedAt ? <>Last checked {fmtWhen(reading.observedAt)}{reading.stale ? ' (old)' : ''}.</> : 'No completed background check yet.'}
  </span>
}
