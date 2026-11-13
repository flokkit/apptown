import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../../api'
import { useAction } from '../../data'
import { useHolds } from '../../instancePages'
import { Modal } from '../../Modal'
import type { StandingDefinition, StandingQuestion, StandingRevision } from '../../wire.gen'

export const dependencyDefinition = (level = 'routine'): StandingDefinition => ({
  scope: 'instance',
  categories: { dependencies: level, securityFixes: true, baseImages: false, waysOfWorking: false },
  projects: [], administrative: true, delivery: 'full',
})
export const packDefinition: StandingDefinition = {
  scope: 'instance',
  categories: { dependencies: '', securityFixes: false, baseImages: false, waysOfWorking: true },
  projects: [], administrative: true, delivery: 'full',
}

export function RuleConsent({ rule, packs, current, reload, project }: { rule?: StandingRevision; packs: boolean; current: boolean; reload: () => Promise<void>; project?: string }) {
  const instance = useHolds('instance:manage-standing-approval')
  const local = useHolds('project:manage-standing-approval', project)
  const [level, setLevel] = useState(rule?.question.definition.categories.dependencies || 'routine')
  const [preview, setPreview] = useState<{ question: StandingQuestion; ruleId: StandingRevision['ruleId']; revision: number } | null>(null)
  const action = useAction(reload)
  if (!instance && !(project && local)) return null
  const saved = rule?.question.definition
  const definition = saved?.scope !== 'instance' && saved ? packs ? saved : { ...saved, categories: { ...saved.categories, dependencies: level } } : packs ? packDefinition : dependencyDefinition(level)
  return <>
    {rule?.enabled ? <button disabled={action.busy || !current} onClick={() => action.run(() => post(`/standing-approvals/${rule.ruleId}/disable`, { expectedRevision: rule.revision }))}>Revoke</button> : <div className="stack stack-2">
      {!packs && <select aria-label="Dependency update level" value={level} onChange={e => setLevel(e.target.value)} disabled={!current || action.busy}>
        <option value="routine">Routine · patch and minor</option><option value="aggressive">Aggressive · including major</option>
      </select>}
      <button disabled={action.busy || !current} onClick={() => action.run(async () => setPreview({ question: await post<StandingQuestion>('/standing-approvals/preview', definition), ruleId: rule?.ruleId ?? null, revision: rule?.revision ?? 0 }))}>Preview</button>
    </div>}
    {action.err && <p className="error" role="alert">{action.err}</p>}
    {preview && <Modal title={<h2>{packs ? 'Keep packs current' : 'Dependency updates'}</h2>} onClose={() => setPreview(null)} actions={<><button onClick={() => setPreview(null)}>Cancel</button><button disabled={action.busy || !current} onClick={() => action.run(async () => {
      setPreview(null)
      try { await post('/standing-approvals', { ruleId: preview.ruleId ?? undefined, definition: preview.question.definition, shownDigest: preview.question.digest, expectedRevision: preview.revision }) }
      catch (error) { await reload(); throw error }
    })}>Enable</button></>}>
      <p>{preview.question.shown}</p>
      {(preview.question.portfolio ?? []).some(p => p.covered) && <><h3>Covered projects</h3>{(preview.question.portfolio ?? []).filter(p => p.covered).map(p => <p key={p.projectId}><Link to={`/projects/${p.project}`}>{p.project}</Link>{p.overridesInstance && <> · {p.level === 'off' ? 'turned off for this project' : p.level === 'aggressive' ? 'aggressive updates including major versions' : 'routine patch and minor updates'}</>}</p>)}</>}
      {preview.question.definition.categories.dependencies === 'aggressive' && (preview.question.portfolio ?? []).filter(p => p.noMajorCommand?.length).map(p => <p key={p.projectId}>{p.project}: major versions are not offered for {p.noMajorCommand.join(', ')}.</p>)}
      {(preview.question.portfolio ?? []).filter(p => !p.covered).length > 0 && <><h3>Needs a pack upgrade</h3>{packs && <p>The janitor will move these projects when Keep packs current is on.</p>}
        {(preview.question.portfolio ?? []).filter(p => !p.covered).map(p => <p key={p.projectId}><Link to={`/projects/${p.project}`}>{p.project}</Link> · {p.needsPackUpgrade.join(', ')}</p>)}</>}
      {(preview.question.portfolio ?? []).some(p => p.deployments.length > 0) && <><h3>Automatic releases</h3>
        {(preview.question.portfolio ?? []).flatMap(p => p.deployments.map(d => <p key={d.deploymentId}><Link to={`/projects/${p.project}/deployments/${d.deployment}`}>{p.project} / {d.deployment}</Link> · {d.setting}</p>))}</>}
      {(preview.question.portfolio ?? []).filter(p => p.onlyChecksStarts).map(p => <p key={p.projectId}>{p.project}: only checks that it starts.</p>)}
      {!!preview.question.baseImageFindings?.projects && <p>{preview.question.baseImageFindings.projects} apps’ images need a base-image update; the janitor does not do this yet.</p>}
    </Modal>}
  </>
}
