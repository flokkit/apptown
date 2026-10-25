import { useState } from 'react'
import { post, put } from '../../../api'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import type { DeploymentView, ReleaseDefault, OfferedTarget } from '../../../wire.gen'

type SetupDraft = { revision: number; targetId: string; targetRevision: number | null; name: string; promotion: boolean; legacy: boolean }

type ReleaseDefaultProps = { slug: string; deployments: DeploymentView[]; offered?: OfferedTarget[]; onConfigured?: () => void }

export function ReleaseDefaultPicker(props: ReleaseDefaultProps) {
  return <ReleaseDefaultForm key={props.slug} {...props} />
}

function ReleaseDefaultForm({ slug, deployments, offered = [], onConfigured }: ReleaseDefaultProps) {
  const path = `/projects/${slug}/release/default`
  const current = useGet<ReleaseDefault>(path)
  const [setupDraft, setSetupDraft] = useState<SetupDraft | null>(null)
  const [setupSaved, setSetupSaved] = useState(false)
  const [draft, setDraft] = useState<{ revision: number; selected: string } | null>(null)
  const { run, busy, err } = useAction()
  if (!current.data) return <ReadStatus q={current} what="the release preference" />
  const settings = current.data
  const selected = draft?.selected ?? settings.deploymentId ?? ''
  const eligible = deployments.filter((entry) => entry.kind === 'standing' && entry.name !== 'main' && entry.feed !== 'landing')
  return <section className="card" style={{ padding: 16, marginBottom: 16 }}>
    <h3 style={{ marginTop: 0 }}>Release preference</h3>
    <p className="muted small">Choose the standing environment you prefer for releases.</p>
    <StaleNote q={current} />
    {setupSaved && <p role="status">Default saved; release not started.</p>}
    {!settings.deploymentId && <p className="muted small">{settings.legacySetup ? 'Your previous release destination needs explicit setup. Its saved choice is shown below; it has not been verified or released.' : settings.revision > 0 ? 'The release preference was explicitly cleared.' : 'No release preference has been configured.'}</p>}
    {(!settings.deploymentId || setupDraft) && <ReleaseSetupForm slug={slug} settings={settings} offered={offered} draft={setupDraft} setDraft={setSetupDraft} onSaved={() => { setSetupDraft(null); setSetupSaved(true); onConfigured?.() }} />}

    {settings.eligibilityIssue && <p className="error">{settings.eligibilityIssue}</p>}
    <form className="row" onSubmit={async (event) => {
      event.preventDefault()
      const saved = await run(() => put<ReleaseDefault>(path, { deploymentId: selected || null, expectedRevision: draft?.revision ?? settings.revision }))
      if (saved) { setDraft(null); if (saved.deploymentId) onConfigured?.() }
    }}>
      <label>Preferred environment <select disabled={busy} value={selected} onChange={(event) => setDraft({ revision: draft?.revision ?? settings.revision, selected: event.target.value })}>
        <option value="">No default selected</option>
        {eligible.map((entry) => <option key={entry.id} value={entry.id ?? ''} disabled={!entry.id}>{entry.displayLabel || entry.name} ({entry.name})</option>)}
        {settings.deploymentId && !eligible.some((entry) => entry.id === settings.deploymentId) && <option value={settings.deploymentId}>Current choice is unavailable</option>}
      </select></label>
      <button disabled={busy || !draft} type="submit">Save preference</button>
      {draft && <button type="button" disabled={busy} onClick={() => setDraft(null)}>Discard changes</button>}
    </form>
    {err && <div className="error" role="alert">{err}</div>}
  </section>
}

function ReleaseSetupForm({ slug, settings, offered, draft, setDraft, onSaved }: { slug: string; settings: ReleaseDefault; offered: OfferedTarget[]; draft: SetupDraft | null; setDraft: (draft: SetupDraft | null) => void; onSaved: () => void }) {
  const { run, busy, err } = useAction()
  const initialId = settings.legacySetup?.targetId ?? ''
  const value = draft ?? { revision: settings.revision, targetId: initialId, targetRevision: offered.find(t => t.id === initialId)?.settingsRevision ?? null, name: settings.legacySetup?.deploymentName ?? '', promotion: !!settings.legacySetup, legacy: !!settings.legacySetup }
  const target = offered.find((t) => t.id === value.targetId)
  return <form onSubmit={async (event) => {
    event.preventDefault()
    if (!target?.id || value.targetRevision == null) return
    setDraft(value)
    const result = await run(() => post<ReleaseDefault>(`/projects/${slug}/release/default/setup`, {
      targetId: value.targetId, targetRevision: value.targetRevision, deploymentName: value.name,
      expectedRevision: value.revision, requirePromotion: value.promotion,
    }))
    if (result) onSaved()
  }}>
    <h4>Set up a release preference</h4>
    <label>Environment <select disabled={busy} value={value.targetId} onChange={(event) => setDraft({ ...value, targetId: event.target.value, targetRevision: offered.find(t => t.id === event.target.value)?.settingsRevision ?? null })}>
      <option value="">Choose an environment</option>
      {offered.filter((t) => t.id).map((t) => <option key={t.id} value={t.id ?? ''}>{t.displayLabel || t.name}</option>)}
      {value.targetId && !target && <option value={value.targetId}>Saved environment is unavailable — choose one explicitly</option>}
    </select></label>
    <label>Deployment name <input disabled={busy} value={value.name} onChange={(event) => setDraft({ ...value, name: event.target.value })} /></label>
    <label><input type="checkbox" disabled={busy || value.legacy} checked={value.promotion} onChange={(event) => setDraft({ ...value, promotion: event.target.checked })} /> Require release promotion permission</label>
    <p className="muted small">Setup needs permission to bind deployments. A promotion requirement also needs release permission. This saves the preference without starting a release.</p>
    <button type="submit" disabled={busy || !target?.id || value.targetRevision == null || !value.name.trim()}>Save release setup</button>
    {draft && <button type="button" disabled={busy} onClick={() => setDraft(null)}>Discard setup changes</button>}
    {err && <p className="error" role="alert">{err}</p>}
  </form>
}

export function PromotionPolicyEdit({ slug, deployment }: { slug: string; deployment: DeploymentView }) {
  const { run, busy, err } = useAction()
  return <section>
    <p className="small">Release promotion permission: {deployment.protected ? 'required by data or audience classification' : deployment.requirePromotion ? 'required by explicit policy' : 'not additionally required'}. Deployment permission is always required.</p>
    <button disabled={busy || !deployment.policyRevision} onClick={() => run(() => put(`/projects/${slug}/deployments/${deployment.id}/promotion-policy`, { expectedRevision: deployment.policyRevision, requirePromotion: !deployment.requirePromotion }))}>
      {deployment.requirePromotion ? 'Remove explicit promotion requirement' : 'Require promotion permission'}
    </button>
    <p className="muted small">Policy edits need binding and promotion permission. They do not change data classification.</p>
    {err && <p className="error" role="alert">{err}</p>}
  </section>
}
