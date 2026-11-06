import { fmtDay, fmtWhen } from '../../format'
import { useHolds } from '../../instancePages'
import { Status } from '../../Status'
import { Link } from 'react-router-dom'
import { Finding, del } from '../../api'
import { FindingsTable } from '../../FindingsTable'
import { QuietConfirm } from '../../QuietConfirm'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import './security.css'
import type { DispositionList, ProjectFindingsView } from '../../wire.gen'

// Findings posture (design/subsystems/findings.md §5 / S3): latest scan per scanner,
// normalized + disposition-annotated, with acceptance management.
export type PostureFinding = Finding


// What the findings list can say. No accepted scan report means nothing is
// known about main yet, and a report AppTown could not read (its scanner
// is no longer configured) leaves its findings unknown: neither reads as
// clean. Only scans that ran, were all read, and found nothing are
// "nothing found".
export function FindingsTab({ slug }: { slug: string }) {
  const canScan = useHolds('change:create', slug)
  const q = useGet<ProjectFindingsView>(`/projects/${slug}/findings`)
  const { data, reload } = q

  if (!data) return <ReadStatus q={q} what="the findings" />
  const unread = data.sources.filter((s) => s.unreadable)
  const nextScan = canScan ? <p><Link to={`/projects/${slug}`}>Open a change to re-run the scan</Link>.</p> : <p>Ask a project member with permission to open a change to re-run the scan.</p>

  const problems = data.sources.length > 0 && !unread.length && data.statusLabel !== 'passed'
  return (
    <div className="security-page">
      <StaleNote q={q} />
      <Section title="Latest scan" count={data.sources.length || undefined}>
        <div className="card row" style={{ display: 'flex' }}>
          <span className="row-status"><Status observation word={data.statusLabel} help={data.repo?.stale ? "Main changed since the last scan. Open a change to scan the latest code." : undefined} /></span>
          <span style={{ minWidth: 0 }} className="grow">{unread.length > 0
            ? (unread.length === 1 ? "One scan report couldn't be read; security checks are incomplete." : `${unread.length} scan reports couldn't be read; security checks are incomplete.`)
            : data.summary.actionable > 0 ? `${data.summary.actionable} problems to fix` : data.sources.length ? data.repo?.stale ? 'No problems found in the last scan; main has changed since.' : 'No problems found in the last scan.' : 'Scan results not available.'}</span>
        </div>
        {(unread.length > 0 || data.repo?.stale || !data.sources.length) && <div className="security-next">{nextScan}</div>}
        {data.sources.length === 0 && <p className="muted small">No scans yet.</p>}
        {data.sources.map(s => <div key={s.binding} className="card row small security-source" style={{ display: 'flex' }}>
          <code>{s.binding}</code>
          <span className={(s.unreadable ? 'error' : 'muted') + " grow"} style={{ minWidth: 0, overflowWrap: 'anywhere' }}>{s.unreadable || `${s.findings} problems`} · {fmtWhen(s.at)}</span>
          {s.changeId && <Link to={`/changes/${s.changeId}`}>{s.changeNumber ? `change ${s.changeNumber}` : 'its change'}</Link>}
        </div>)}
      </Section>
      {unread.length > 0 && data.sources.length > unread.length && <p className="muted small">No findings in the scans that could be read; the unread reports may have some.</p>}
      {problems && (
        <Section title="Problems" count={data.summary.actionable || undefined}>
        {Object.entries(
            data.findings.reduce<Record<string, PostureFinding[]>>((acc, f) => {
              const key = f.kind === 'vuln' && f.package ? `${f.package.name}@${f.package.version ?? '?'}` : `${f.kind}: ${f.source}`
              ;(acc[key] = acc[key] ?? []).push(f)
              return acc
            }, {})
          ).map(([group, fs]) => {
            const actionable = fs.filter((f) => !f.disposition).length
            const reached = fs.filter((f) => f.reached === true).length
            const maxFix = fs.map((f) => f.package?.fixedIn ?? '').sort((a, b) =>
              a.localeCompare(b, undefined, { numeric: true })).at(-1)
            return (
              <section key={group} className="diff-file">
                <h3>
                  <span className="badge">
                    {actionable > 0 ? `${actionable} actionable` : 'all suppressed'}
                  </span>
                  <code>{group}</code>
                  <span className="muted small">
                    {fs.length} finding{fs.length === 1 ? '' : 's'}
                    {reached > 0 ? ` · ${reached} reached` : ''}
                    {maxFix ? ` · all fixed by ${maxFix}` : ''}
                  </span>
                </h3>
                <FindingsTable findings={fs} projectSlug={slug} onChanged={reload} />
              </section>
            )
          })}
        </Section>
      )}
    </div>
  )
}

// The suppression register (SARIF's word): every recorded decision about a
// finding — time-boxed, justified, attributed, revocable, exported as
// OpenVEX. Separate from Findings: one page is "what is wrong", this one is
// "what we decided about it".
export function SuppressionsTab({ slug }: { slug: string }) {
  const q = useGet<DispositionList>(`/projects/${slug}/dispositions`)
  const { data, reload } = q
  const { run, err } = useAction(reload)
  const revoke = (id: string) => run(() => del(`/dispositions/${id}`))

  if (!data) return <ReadStatus q={q} what="the suppressions" />
  const list = data.dispositions

  return (
    <div className="security-page">
      <StaleNote q={q} />
      {err && <div className="error">{err}</div>}
      <Section title="Recorded" count={list.length || undefined}
        action={<a href={`/api/v1/projects/${slug}/dispositions/openvex`} target="_blank" rel="noreferrer">OpenVEX export ↗</a>}>
        {list.length === 0 && <p className="muted">No suppressions.</p>}
        {list.map((d) => (
          <div className="card row security-suppression" key={d.id} style={{ display: 'flex' }}>
            <span className="row-status"><Status observation word={d.statusLabel} help={`${d.status}${d.expiresAt ? ` · until ${fmtDay(d.expiresAt)}` : ''}`} /></span>
            <div style={{ minWidth: 0, overflowWrap: 'anywhere' }} className="grow">
              <span>{d.identifier || d.fingerprint}</span>{d.package && <span className="muted small"> · {d.package}</span>}
              <div className="muted small" title={d.justification}>“{d.justification.length > 70 ? `${d.justification.slice(0, 70)}…` : d.justification}” · {d.by}{d.expiresAt ? `, until ${fmtDay(d.expiresAt)}` : ''}</div>
            </div>
            <span className="row-action"><QuietConfirm label="Revoke" ask="Revoke this suppression? The finding becomes actionable again immediately." onYes={() => revoke(d.id!)} /></span>
          </div>
        ))}
      </Section>
    </div>
  )
}
