import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { FleetView } from '../../wire.gen'

// Ways of working across the fleet (design/subsystems/ways-of-working-versions.md
// §11): each pack as a row of versions with the projects on each. A
// release cuts a new version and stops there; a project moves only when
// its owner prepares and adopts, under its own Settings › Guidance.


export function FleetVersionsTab() {
  const q = useGet<FleetView>('/wow')
  const { data } = q
  if (!data) return <ReadStatus q={q} what="the ways of working in use" />
  const packs = data.packs.filter((p) => p.versions.some((v) => v.projects.length > 0) || p.notOn.length > 0 || p.platform)
  const behind = packs.reduce((n, p) => n + p.versions.filter((v) => v.version !== p.recommended).reduce((m, v) => m + v.projects.length, 0), 0)
  return (
    <div>
      <StaleNote q={q} />
      <p className="muted small" style={{ maxWidth: 720, marginTop: 0 }}>
        A pack is a way of working someone publishes in versions; a project is on one or more.
        A release cuts a new version and stops there — a project moves only when its owner prepares and adopts the update under its Settings.
        {behind > 0 ? ` ${behind} project${behind === 1 ? ' is' : 's are'} on a version other than the recommended one.` : ' Every project is on its recommended version.'}
      </p>
      <div style={{ overflowX: 'auto' }}>
        <table>
          <thead><tr><th>Pack</th><th>Versions and who is on them</th><th>Names it, not on it</th></tr></thead>
          <tbody>
            {packs.map((p) => (
              <tr key={p.pack}>
                <td><b>{p.pack}</b>{p.platform ? <span className="muted small"> · platform</span> : null}<div className="muted small">recommended v{p.recommended}</div></td>
                <td>
                  {p.versions.map((v) => (
                    <div key={v.version} className="small" style={{ marginBottom: 2 }}>
                      <span className={`chip ${v.version === p.recommended ? 'pass' : v.projects.length > 0 ? 'pending' : ''}`}>v{v.version}</span>{' '}
                      <b>{v.projects.length}</b> project{v.projects.length === 1 ? '' : 's'}
                      {v.projects.length > 0 && <span className="muted"> — {v.projects.map((s, i) => <span key={s}>{i > 0 ? ', ' : ''}<Link to={`/projects/${s}/settings/working`}>{s}</Link></span>)}</span>}
                      {v.note && v.version === p.recommended ? <span className="muted"> · {v.note}</span> : null}
                    </div>
                  ))}
                </td>
                <td className="small muted">
                  {p.notOn.length === 0 ? '—' : p.notOn.map((s, i) => <span key={s}>{i > 0 ? ', ' : ''}<Link to={`/projects/${s}/settings/working`}>{s}</Link></span>)}
                  {p.notOn.length > 0 && <div className="muted small">created from an earlier shape of this pack; their blocks are carried as their own</div>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
