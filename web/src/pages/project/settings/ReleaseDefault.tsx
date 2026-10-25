import { useState } from 'react'
import { put } from '../../../api'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import type { DeploymentView, ReleaseDefault } from '../../../wire.gen'

type ReleaseDefaultProps = { slug: string; deployments: DeploymentView[] }

export function ReleaseDefaultPicker(props: ReleaseDefaultProps) {
  return <ReleaseDefaultForm key={props.slug} {...props} />
}

function ReleaseDefaultForm({ slug, deployments }: ReleaseDefaultProps) {
  const path = `/projects/${slug}/release/default`
  const current = useGet<ReleaseDefault>(path)
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
    {settings.eligibilityIssue && <p className="error">{settings.eligibilityIssue}</p>}
    <form className="row" onSubmit={async (event) => {
      event.preventDefault()
      const saved = await run(() => put<ReleaseDefault>(path, { deploymentId: selected || null, expectedRevision: draft?.revision ?? settings.revision }))
      if (saved) setDraft(null)
    }}>
      <label>Preferred environment <select value={selected} onChange={(event) => setDraft({ revision: draft?.revision ?? settings.revision, selected: event.target.value })}>
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
