import { PRODUCT } from '../../brand'
import { statusProps } from '../../runtime'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { post } from '../../api'
import { SplitButton } from '../../SplitButton'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { ChangeButton } from './ChangeButton'
import type { DeploymentView, ReleasePlan, CreatedChange, ProjectDetail as ProjectT } from '../../wire.gen'
import './overview.css'

// The project overview's top: the two big doors and the catalog of prepared
// changes behind one of them.

// Change begins governed work; Deployments opens the places that run it.
export function BigChangeBox({ slug, inline = false }: { slug: string; inline?: boolean }) {
  const nav = useNavigate()
  const [busy, setBusy] = useState(false)
  const envsQ = useGet<DeploymentView[]>(`/projects/${slug}/deployments`)
  const projectQ = useGet<ProjectT>(`/projects/${slug}`)
  const envs = envsQ.data ?? []
  const p = projectQ.data
  const request = p?.originalRequest ?? ''
  const planQ = useGet<ReleasePlan>(`/projects/${slug}/release/plan`)
  const plan = planQ.current ? planQ.data : undefined
  // The project's own read is shown by the page around this box.
  const [err, setErr] = useState('')

  // Prefer the selected destination by identity when its address can be opened.
  // Otherwise show an addressable alternative by its own customer label.
  const addressed = envs.filter((e) => !!e.url && !e.hostOnly)
  const best = addressed.find((e) => !!plan?.deploymentId && e.id === plan.deploymentId) ?? addressed.find((e) => e.running) ?? addressed[0]

  // The Build-the-first-version door:
  // while a project has no running app, building v1 is overwhelmingly the
  // likely intent — a standing primary action, not a hint. Not a workflow:
  // a Change + workshop with the project's original request seeded.
  // Only while the project has no app at all: a project whose repository
  // already declares one (a pack's seed may carry a working starter, read as
  // the runtime form) is changed, not built, even before its first deploy.
  const buildFirst = !best && p?.runtimeForm === 'no app yet'
  const buildV0 = async () => {
    if (busy) return
    setBusy(true); setErr('')
    try {
      // No request, no brief: say so in the body rather than sending nothing
      // (complaint #38) — the helper then asks before it builds.
      const body = request.trim() || 'No description was written for this project yet. Ask what the first version should do before building anything.'
      const r = await post<CreatedChange>(`/projects/${slug}/changes`, {
        title: 'First version', type: 'feature', body, workflow: '',
      })
      nav(`/changes/${r.change.id}?tab=conversation&kickoff=1`)
    } catch (e) { setErr((e as Error).message); setBusy(false) }
  }

  return (
    <div className="card overview-actions" style={inline ? undefined : { padding: 18, margin: '14px 0 18px', maxWidth: 980 }}>
      <div className="row" style={{ gap: 14, alignItems: 'stretch', flexWrap: 'wrap' }}>
        {best && !inline && (
          <SplitButton icon="▶" label="Open" green
            sub={(best.displayLabel || best.name) + ' · ' + best.url.replace(/^https?:\/\//, '')}
            onMain={() => window.open(best.url, '_blank')}
            items={[
              ...envs.map((e) => ({
                label: (e.displayLabel || e.name) + (e.url ? ' · ' + e.url.replace(/^https?:\/\//, '') : ''),
                hint: !e.url ? 'no address yet · nothing has been released here' : statusProps(e.status).help,
                onClick: () => { if (e.url) window.open(e.url, '_blank'); else nav('/deployments') },
              })),
              { label: 'All deployments', onClick: () => nav('/deployments') },
            ]} />
        )}
        {buildFirst && (
          <SplitButton icon="🔨" label="Build the first version" green
            sub={request ? `"${request.slice(0, 60)}${request.length > 60 ? '…' : ''}"` : 'no app here yet · start it in a building chat'}
            busy={busy} onMain={buildV0} items={[]} />
        )}
        <ChangeButton slug={slug} chatAvailable primary={!buildFirst} />
        {/* Inline on the Changes page, the box is the section's action: the
            project's Deployments page is in its sidebar. */}
        {!inline && <Link className="btn" to={`/projects/${slug}/deployments`}>Deployments →</Link>}
      </div>
      <ReadStatus q={envsQ} what="the deployments" className="small" />
      <ReadStatus q={planQ} what={`the ${PRODUCT} release destination`} className="small" />
      <StaleNote q={[envsQ, planQ]} />
      {err && <div className="error" style={{ marginTop: 10 }}>{err}</div>}
    </div>
  )
}
