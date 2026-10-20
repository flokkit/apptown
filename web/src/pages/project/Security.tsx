import { Link } from 'react-router-dom'
import { Finding, del } from '../../api'
import { FindingsTable } from '../../FindingsTable'
import { QuietConfirm } from '../../QuietConfirm'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { Disposition as Suppression, DispositionList, ProjectFindingsView } from '../../wire.gen'

// Findings posture (design/subsystems/findings.md §5 / S3): latest scan per scanner,
// normalized + disposition-annotated, with acceptance management.
export type PostureFinding = Finding


// What the findings list can say. No accepted scan report means nothing is
// known about main yet, and a report AppTown could not read (its scanner
// is no longer configured) leaves its findings unknown: neither reads as
// clean. Only scans that ran, were all read, and found nothing are
// "nothing found".
export function scanState(data: Pick<ProjectFindingsView, 'findings' | 'sources'>): 'not-scanned' | 'unreadable' | 'clean' | 'findings' {
  if (data.findings.length > 0) return 'findings'
  if (data.sources.length === 0) return 'not-scanned'
  return data.sources.some((s) => s.unreadable) ? 'unreadable' : 'clean'
}

export function FindingsTab({ slug }: { slug: string }) {
  const q = useGet<ProjectFindingsView>(`/projects/${slug}/findings`)
  const { data, reload } = q

  if (!data) return <ReadStatus q={q} what="the findings" />
  const state = scanState(data)
  const unread = data.sources.filter((s) => s.unreadable)

  return (
    <div>
      <StaleNote q={q} />
      <div className="row" style={{ marginBottom: 12 }}>
        <span className={`chip ${data.summary.actionable > 0 ? 'fail' : state === 'clean' ? 'pass' : 'unknown'}`}>
          {data.summary.actionable} actionable
        </span>
        <span className="chip">{data.summary.dispositioned} suppressed</span>
        <span className="chip">{data.summary.total} total</span>
        {Object.entries(data.summary.bySeverity).map(([sev, n]) => (
          <span key={sev} className="muted small">{sev}: {n}</span>
        ))}
      </div>
      <div className="muted small" style={{ marginBottom: 12 }}>
        Findings on main, from the latest scans of completed runs. A change's own findings are on its page.
        {data.repo?.sha && <> Default branch @ <code>{data.repo.sha}</code>.</>}
        {data.repo?.stale && (
          <span className="chip pending" style={{ marginLeft: 8 }}
            title="The default branch has commits newer than the latest scan — the next completed run's scans will refresh this view.">
            main changed since last scan
          </span>
        )}
      </div>
      <div className="muted small" style={{ marginBottom: 12 }}>
        {data.sources.map((s) => (
          <span key={s.binding} style={{ marginRight: 12 }}>
            {s.binding}: {s.unreadable
              ? <span className="chip unknown" title={s.unreadable}>not read</span>
              : <>{s.findings} finding{s.findings === 1 ? '' : 's'}</>}{' '}
            ({s.changeId ? <Link to={`/changes/${s.changeId}`}>{s.changeNumber ? `change ${s.changeNumber}` : 'its change'}</Link> : <>{s.runNumber ? `run ${s.runNumber}, ` : ''}a run with no change</>}, {s.at.slice(0, 10)})
          </span>
        ))}
      </div>

      {unread.length > 0 && (
        <div className="small" style={{ marginBottom: 12 }}>
          <span className="chip unknown">not read</span>{' '}
          {unread.length === 1 ? 'One scan report' : `${unread.length} scan reports`} could not be read, so {unread.length === 1 ? 'its' : 'their'} findings are unknown:{' '}
          {unread.map((s) => s.unreadable).join('; ')}.
        </div>
      )}
      {state === 'not-scanned'
        ? <div className="muted"><span className="chip unknown">not scanned yet</span> No scan of main has been recorded, so its findings are unknown.</div>
        : state === 'unreadable'
        ? <div className="muted">No findings in the scans that could be read; the unread ones above may have some.</div>
        : state === 'clean'
        ? <div><span className="chip pass">scanned: nothing found</span> <span className="muted small">The latest scans listed above found nothing.</span></div>
        : Object.entries(
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
              <details key={group} className="diff-file" open={actionable > 0}>
                <summary>
                  <span className={`chip ${actionable > 0 ? 'fail' : 'pass'}`}>
                    {actionable > 0 ? `${actionable} actionable` : 'all suppressed'}
                  </span>
                  <code>{group}</code>
                  <span className="muted small">
                    {fs.length} finding{fs.length === 1 ? '' : 's'}
                    {reached > 0 ? ` · ${reached} reached` : ''}
                    {maxFix ? ` · all fixed by ${maxFix}` : ''}
                  </span>
                </summary>
                <FindingsTable findings={fs} projectSlug={slug} onChanged={reload} />
              </details>
            )
          })}
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
  const expired = (d: Suppression) => !!d.expiresAt && d.expiresAt <= new Date().toISOString()

  return (
    <div>
      <StaleNote q={q} />
      {err && <div className="error">{err}</div>}
      <div className="row" style={{ marginBottom: 12 }}>
        <span className="muted small">
          Suppressions are created from the Findings page; they match findings by fingerprint or
          identifier+package, and expire on schedule — expired ones fail the gate again.
        </span>
        <span style={{ flex: 1 }} />
        <a href={`/api/v1/projects/${slug}/dispositions/openvex`} target="_blank" rel="noreferrer" className="small">OpenVEX export ↗</a>
      </div>
      {list.length === 0 && <div className="muted">No suppressions recorded.</div>}
      {list.map((d) => (
        <div className="row" key={d.id} style={{ marginTop: 4 }}>
          <span className={`chip ${expired(d) ? 'fail' : d.status === 'accepted' ? 'pending' : ''}`}>
            {expired(d) ? 'expired' : d.status}
          </span>
          <span className="small">{d.identifier || d.fingerprint}</span>
          {d.package && <span className="muted small">{d.package}</span>}
          <span className="muted small" title={d.justification}>“{d.justification.slice(0, 70)}…” — {d.by}
            {d.expiresAt ? `, until ${d.expiresAt.slice(0, 10)}` : ''}</span>
          <span style={{ flex: 1 }} />
          <QuietConfirm label="revoke" ask="Revoke this suppression? The finding becomes actionable again immediately." onYes={() => revoke(d.id!)} />
        </div>
      ))}
    </div>
  )
}
