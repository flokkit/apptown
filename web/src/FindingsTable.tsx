import { useNavigate } from 'react-router-dom'
import { Finding, findingFixPayload, post } from './api'
import { QuietPrompt } from './QuietConfirm'
import { useAction } from './data'
import type { CreatedChange } from './wire.gen'

// Structured findings (design/subsystems/findings.md): the normalized view over scan
// reports, with per-finding acceptance (VEX-shaped dispositions) — the
// right-sized alternative to blanket step overrides. One table for the
// change page and the project's Security tab (debt review, finding 25:
// the two copies had drifted; this one shows the suppression's words).

const sevChip = (s: string) =>
  s === 'critical' || s === 'high' ? 'fail' : s === 'medium' || s === 'unknown' ? 'pending' : ''

export function FindingsTable({ findings, projectSlug, onChanged }: {
  findings: Finding[]; projectSlug: string; onChanged: () => void
}) {
  const nav = useNavigate()
  const { run, err } = useAction(onChanged)

  const accept = (f: Finding, status: 'accepted' | 'false_positive', justification: string, expiresAt: string) => {
    const cve = f.identifiers?.find((i) => i.startsWith('CVE-')) ?? f.identifiers?.[0] ?? ''
    return run(() => post(`/projects/${projectSlug}/dispositions`, {
      fingerprint: f.fingerprint, identifier: cve, package: f.package?.name ?? '',
      status, justification, expiresAt,
    }))
  }
  const fix = async (f: Finding, workflow: string) => {
    const r = await run(() => post<CreatedChange>(`/projects/${projectSlug}/changes`,
      { type: 'bug', workflow, ...findingFixPayload(f) }))
    if (r) nav(`/changes/${r.change.id}`)
  }

  if (findings.length === 0) return <div className="muted small">no findings</div>
  return (
    <div style={{ marginTop: 6 }}>
      <table>
        <thead><tr><th>Sev</th><th>Finding</th><th>Package / location</th><th>Fix</th><th></th></tr></thead>
        <tbody>
          {findings.map((f) => (
            <tr key={f.fingerprint} style={f.disposition ? { opacity: 0.55 } : undefined}>
              <td>
                <span className={`chip ${sevChip(f.severity)}`}
                  title={f.severity === 'unknown' ? 'The source database gives no severity; it is treated as high.' : undefined}>
                  {f.severity === 'unknown' ? 'unscored' : f.severity}
                </span>
                {f.reached === false && <div><span className="chip" title="osv-scanner call analysis: the vulnerable code is never called by this program.">not reached</span></div>}
                {f.reached === true && <div><span className="chip pending" title="osv-scanner call analysis: the program CALLS the vulnerable code.">reached</span></div>}
              </td>
              <td>
                <span style={f.disposition ? { textDecoration: 'line-through' } : undefined} title={f.detail ?? ''}>
                  {f.url ? <a href={f.url} target="_blank" rel="noreferrer">{f.identifiers?.[0] ?? f.title}</a>
                    : (f.identifiers?.[0] ?? f.title)}
                </span>
                <span className="muted small"> {f.title.slice(0, 70)}</span>
                {f.regression === 'undecided' && (
                  <div className="small"><span className="chip pending">undecided</span> <span className="muted">{f.detail}</span></div>
                )}
                {(f.regression === 'preexisting' || f.preExisting) && (
                  <div className="small"><span className="chip">already on main</span></div>
                )}
                {f.disposition && (
                  <div className="muted small" title={f.disposition.justification}>
                    suppressed ({f.disposition.status}) by {f.disposition.by}
                    {f.disposition.expiresAt ? ` until ${f.disposition.expiresAt.slice(0, 10)}` : ''} — “{f.disposition.justification.slice(0, 80)}”
                  </div>
                )}
              </td>
              <td className="muted small">
                {f.package ? `${f.package.name}@${f.package.version ?? '?'}` : ''}
                {f.location ? `${f.location.file}:${f.location.line ?? ''}` : ''}
              </td>
              <td className="muted small">{f.package?.fixedIn ?? ''}</td>
              <td>
                {!f.disposition && (
                  <span className="row" style={{ gap: 4, flexWrap: 'nowrap' }}>
                    <QuietPrompt label="Fix…" ask="Fix with workflow:"
                      fields={[{ key: 'workflow', placeholder: 'workflow', initial: 'quick-change', required: true, width: 140 }]}
                      yes="Start" onYes={(v) => fix(f, v.workflow.trim())} />
                    <QuietPrompt label="Suppress…"
                      ask={`Suppressing ${f.identifiers?.[0] ?? f.fingerprint} . The reason is recorded.`}
                      fields={[
                        { key: 'justification', placeholder: 'why (required)', required: true },
                        { key: 'expiresAt', placeholder: 'expires, e.g. 2026-12-01T00:00:00Z (empty = until revoked)', width: 300 },
                      ]}
                      yes="Suppress" onYes={(v) => accept(f, 'accepted', v.justification.trim(), v.expiresAt.trim())} />
                    <QuietPrompt label="FP…" ask="Marking as a false positive. The reason is recorded."
                      fields={[{ key: 'justification', placeholder: 'why (required)', required: true }]}
                      yes="Mark" onYes={(v) => accept(f, 'false_positive', v.justification.trim(), '')} />
                  </span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {err && <div className="error">{err}</div>}
    </div>
  )
}
