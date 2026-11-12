import { useState } from 'react'
import { put } from '../../../api'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { Section } from '../../../Section'
import { FieldRow } from '../../../FieldRow'
import type { DeploymentView, RoutineUpdates as Settings } from '../../../wire.gen'

export function RoutineUpdates({ slug, name }: { slug: string; name: string }) {
  const current = useGet<DeploymentView[]>(`/projects/${slug}/deployments`, { poll: 5000 })
  const [draft, setDraft] = useState<{ revision: number; routineUpdates: Settings } | null>(null)
  const action = useAction(current.reload)
  const deployment = current.data?.find(d => d.name === name)
  if (!deployment) return <ReadStatus q={current} what="routine update settings" />
  if (deployment.kind !== 'standing') return null
  const value = draft || { revision: deployment.routineUpdatesRevision, routineUpdates: deployment.routineUpdates }
  const moved = !!draft && draft.revision !== deployment.routineUpdatesRevision
  const edit = (settings: Settings) => setDraft({ ...value, routineUpdates: settings })
  return <Section title="Janitor releases">
    <p>Choose when the janitor releases accepted updates here. Protected deployments still need a person's approval. A failed release or a rollback holds updates for a person.</p>
    <StaleNote q={current} />
    <form className="stack stack-3" onSubmit={async event => {
      event.preventDefault()
      if (await action.run(() => put(`/projects/${slug}/deployments/${name}/routine-updates`, value))) setDraft(null)
    }}>
      <FieldRow label="Routine updates"><select aria-label="Routine updates" disabled={action.busy || !current.current} value={value.routineUpdates?.mode || 'automatic'} onChange={event => edit(event.target.value === 'window' ? { mode: 'window', days: 'sun', from: '02:00', to: '04:00', zone: Intl.DateTimeFormat().resolvedOptions().timeZone } : { mode: event.target.value })}>
        <option value="automatic">Automatically</option><option value="window">In a release window</option><option value="person">When a person says</option>
      </select></FieldRow>
      {value.routineUpdates?.mode === 'window' && <>
        <FieldRow label="Days"><input aria-label="Window days" value={value.routineUpdates.days || ''} placeholder="sun,mon" disabled={action.busy || !current.current} onChange={e => edit({ ...value.routineUpdates, days: e.target.value })} /></FieldRow>
        <FieldRow label="From"><input type="time" aria-label="Window from" value={value.routineUpdates.from || ''} disabled={action.busy || !current.current} onChange={e => edit({ ...value.routineUpdates, from: e.target.value })} /></FieldRow>
        <FieldRow label="To"><input type="time" aria-label="Window to" value={value.routineUpdates.to || ''} disabled={action.busy || !current.current} onChange={e => edit({ ...value.routineUpdates, to: e.target.value })} /></FieldRow>
        <FieldRow label="Time zone"><input aria-label="Window time zone" value={value.routineUpdates.zone || ''} disabled={action.busy || !current.current} onChange={e => edit({ ...value.routineUpdates, zone: e.target.value })} /></FieldRow>
      </>}
      {moved && <p role="alert">The settings changed while you were editing. Discard your changes and reload before saving.</p>}
      <div className="row row-2"><button type="submit" className="primary" disabled={!draft || moved || !current.current || action.busy}>Save routine updates</button>{draft && <button type="button" disabled={action.busy} onClick={() => setDraft(null)}>Discard changes</button>}</div>
      {action.err && <p className="error" role="alert">{action.err}</p>}
    </form>
  </Section>
}
