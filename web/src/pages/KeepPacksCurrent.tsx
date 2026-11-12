import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { useHolds } from '../instancePages'
import { ListRow } from '../ListRow'
import { Modal } from '../Modal'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { StandingDefinition, StandingQuestion, StandingStatus } from '../wire.gen'

const definition: StandingDefinition = {
  scope: 'instance', schedules: [{ workflow: 'janitor-adopt', crons: {} }],
  categories: { dependencies: '', securityFixes: false, baseImages: false, waysOfWorking: true },
  projects: [], administrative: true, delivery: 'full',
}

export function KeepPacksCurrent() {
  const mayManage = useHolds('instance:manage-standing-approval')
  return mayManage ? <PackConsent /> : null
}

function PackConsent() {
  const q = useGet<StandingStatus[]>('/standing-approvals', { kind: 'config' })
  const [preview, setPreview] = useState<StandingQuestion | null>(null)
  const action = useAction(q.reload)
  const rule = Array.isArray(q.data) ? q.data.find(s => s.rule.enabled && s.rule.question.definition.scope === 'instance' && s.rule.question.definition.categories.waysOfWorking)?.rule : undefined
  return <>
    <ReadStatus q={q} what="pack update approval" className="small" /><StaleNote q={q} />
    <ListRow identity={<><strong>Keep packs current</strong><span>Moves projects to newly published pack versions and releases them like any janitor change.</span>
      {rule && <span className="small">Enabled by {rule.issuer.name}</span>}</>}
      action={rule ? <button disabled={action.busy || !q.current} onClick={() => action.run(() => post(`/standing-approvals/${rule.ruleId}/disable`, { expectedRevision: rule.revision }))}>Revoke</button>
        : <button disabled={action.busy || !q.current} onClick={() => action.run(async () => setPreview(await post<StandingQuestion>('/standing-approvals/preview', definition)))}>Preview</button>} />
    {action.err && <p className="error">{action.err}</p>}
    {preview && <Modal title={<h2>Keep packs current</h2>} onClose={() => setPreview(null)} actions={<><button onClick={() => setPreview(null)}>Cancel</button><button disabled={action.busy} onClick={() => action.run(async () => {
      await post('/standing-approvals', { definition: preview.definition, shownDigest: preview.digest, expectedRevision: 0 }); setPreview(null)
    })}>Enable</button></>}>
      <p>{preview.shown}</p>
      {(preview.portfolio ?? []).filter(p => !p.covered).length > 0 && <><h3>Needs a pack upgrade</h3><p>The janitor will move these projects when Keep packs current is on.</p>
        {(preview.portfolio ?? []).filter(p => !p.covered).map(p => <p key={p.projectId}><Link to={`/projects/${p.project}`}>{p.project}</Link> · {p.needsPackUpgrade.join(', ')}</p>)}</>}
      {(preview.portfolio ?? []).some(p => p.deployments.length > 0) && <><h3>Automatic releases</h3>
        {(preview.portfolio ?? []).flatMap(p => p.deployments.map(d => <p key={d.deploymentId}><Link to={`/projects/${p.project}/deployments/${d.deployment}`}>{p.project} / {d.deployment}</Link> · {d.setting}</p>))}</>}
      {(preview.portfolio ?? []).filter(p => p.onlyChecksStarts).map(p => <p key={p.projectId}>{p.project}: only checks that it starts.</p>)}
    </Modal>}
  </>
}
