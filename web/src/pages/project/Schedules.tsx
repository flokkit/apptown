import { Fragment } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { fmtStamp, utcDate } from '../../format'
import type { ScheduleList, ScheduleView } from '../../wire.gen'

// A project's schedules on its overview: what fires, when next, and what
// the last firing did. The cron is AppTown's (UTC); the words and times
// are the reader's own zone, read off the next firing the server computed
// with the scheduler's own parser, so the page and the scheduler agree.

const ordinal = (n: number): string => {
  const tens = n % 100
  if (tens >= 11 && tens <= 13) return `${n}th`
  return `${n}${['th', 'st', 'nd', 'rd'][n % 10] ?? 'th'}`
}

const hhmm = (d: Date): string => `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`

// cadenceWords is a cron in plain words in the reader's zone: "monthly, 1st
// at 17:00". next is one of its firings (the server's), which places the
// day and time in that zone. A cron of another shape, or one with no
// known next firing, is given as itself.
export function cadenceWords(cron: string, next?: string): string {
  const f = cron.trim().split(/\s+/)
  const raw = `cron ${cron.trim()} (UTC)`
  if (f.length !== 5) return raw
  const [mi, h, dom, mon, dow] = f
  if (f.every((x) => x === '*')) return 'every minute'
  const at = next ? utcDate(next) : null
  if (!at || isNaN(at.getTime())) return raw
  const num = (x: string) => /^\d+$/.test(x)
  if (mon !== '*' || !num(mi)) return raw
  if (h === '*' && dom === '*' && dow === '*') return `hourly at :${String(at.getMinutes()).padStart(2, '0')}`
  if (!num(h)) return raw
  if (dom === '*' && dow === '*') return `daily at ${hhmm(at)}`
  if (dom === '*' && num(dow)) return `weekly, ${at.toLocaleDateString('en-AU', { weekday: 'long' })} at ${hhmm(at)}`
  if (num(dom) && dow === '*') {
    // A local day is the cadence only when the zone keeps the firing in
    // the cron's month; one that crosses a month's end (the 1st at 06:00
    // UTC is the 31st, 30th or 28th west of UTC) is said in UTC instead.
    if (at.getMonth() === at.getUTCMonth()) return `monthly, ${ordinal(at.getDate())} at ${hhmm(at)}`
    return `monthly, ${ordinal(Number(dom))} at ${h.padStart(2, '0')}:${mi.padStart(2, '0')} UTC`
  }
  return raw
}

function LastFiring({ s }: { s: ScheduleView }) {
  const last = s.last
  if (s.problem) return <span><span className="muted">last: unknown · </span><span className="chip unknown" title={s.problem}>could not be read</span></span>
  if (!last) return <span className="muted">last: never</span>
  const when = fmtStamp(last.at)
  switch (last.outcome) {
    case 'opened':
      return <span>last: <Link to={`/changes/${last.changeId}`}>{when} · {last.changeNumber ? `change ${last.changeNumber}` : 'its change'}</Link></span>
    case 'skipped':
      return <span>last: {when} · <span className="chip">skipped: previous run still active</span></span>
    case 'failed':
      return <span>last: {when} · <span className="chip fail">could not start</span></span>
    default:
      return <span>last: {when} · <span className="muted">outcome unknown</span></span>
  }
}

function NextFiring({ s }: { s: ScheduleView }) {
  if (s.problem || !s.next) return <span className="muted">next: unknown</span>
  if (s.due) return <span className="chip pending">due now</span>
  return <span>next: {fmtStamp(s.next)}</span>
}

// ScheduleRows: one line per schedule, nothing when there are none, one
// muted line on an eval copy. A failed read says so; it never reads as none.
export function ScheduleRows({ slug }: { slug: string }) {
  const q = useGet<ScheduleList>(`/projects/${slug}/schedules`)
  const data = q.data
  if (!data) return <ReadStatus q={q} what="the schedules" className="small" />
  // An answer whose refresh failed keeps its mark, so an old "none" or
  // "eval copy" never reads as current.
  if (data.evalCopy) return <div className="muted small">Eval copy: runs no schedules.<StaleNote q={q} inline /></div>
  if (data.schedules.length === 0) return q.refreshFailed ? <div className="muted small">Schedules: none.<StaleNote q={q} inline /></div> : null
  return (
    <div className="card" data-testid="schedules">
      <StaleNote q={q} />
      <strong>Schedules</strong>
      <div className="schedule-rows small">
        {data.schedules.map((s) => (
          <Fragment key={s.source.workflow}>
            <span className="schedule-name" title={`workflow ${s.source.workflow}, version ${s.source.version}`}>{s.source.workflow}</span>
            <span title={`cron ${s.cron} (${s.timeZone})`}>{cadenceWords(s.cron, s.next)}</span>
            <NextFiring s={s} />
            <LastFiring s={s} />
          </Fragment>
        ))}
      </div>
    </div>
  )
}
