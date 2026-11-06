import { CapacityStrip } from '../CapacityStrip'
import { HelpButton } from '../PageHead'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { Section } from '../Section'
import { fmtBytes, readingAge } from '../format'
import type { DiskReadings } from '../wire.gen'

export function UsageDisk() {
  const q = useGet<DiskReadings>('/infrastructure/disks', { poll: 60000 })
  return <Section title="Disk" action={<HelpButton about="Disk" guide="monitoring#disk">
    <p>Disk capacity and measured sizes, with the time of each check.</p>
  </HelpButton>}>
    {!q.data ? <ReadStatus q={q} what="disk use" /> : <>
      <StaleNote q={q} />
      {q.data.disks.length === 0 && <p className="muted">Waiting for the first disk check.</p>}
      <div className="usage-disks">
        {q.data.disks.map(d => {
          const age = readingAge(d.readAt, d.stale)
          return <article className="usage-disk" key={d.what + (d.path ?? '')}>
            <CapacityStrip name={d.what} total={d.totalBytes} used={!d.unknown && !d.stale && !d.error && !q.refreshFailed && d.totalBytes !== undefined && d.freeBytes !== undefined ? Math.max(0, d.totalBytes - d.freeBytes) : undefined} />
            {age !== 'checked now' && <p className="muted small">{age}</p>}
            {d.error && <p className="error small">{d.error} These are the last measured values.</p>}
            {d.categories?.length ? <dl className="usage-disk-categories">{d.categories.map(c => <div key={c.name}><dt>{c.name}</dt><dd>{c.unknown || d.unknown ? 'unknown' : fmtBytes(c.bytes)}{c.readAt !== d.readAt && <span className="muted small"> · {readingAge(c.readAt, d.stale)}</span>}</dd></div>)}</dl> : <p className="muted small">Category sizes are unknown.</p>}
          </article>
        })}
      </div>
    </>}
  </Section>
}
