import { RoutineUpdates } from './RoutineUpdates'
import { Switch } from '../../../Switch'
import { QuietConfirm } from '../../../QuietConfirm'
import { PRODUCT } from '../../../brand'
import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { put } from '../../../api'
import { getKey, useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { Section } from '../../../Section'
import { FieldRow } from '../../../FieldRow'
import { HelpButton } from '../../../PageHead'
import type { DeploymentView, RoleCatalog, RoutineUpdates as RoutineSettings } from '../../../wire.gen'

type Draft = { revision: number; feed: string; from: string; approvalRole: string }

export function feedWords(e: Pick<DeploymentView, 'feed' | 'promotionSource'>) {
  return e.feed === 'main' ? 'Follows main' : e.feed === 'promotion' ? `Promoted from ${e.promotionSource || 'a source not yet chosen'}` : e.feed === 'manual' ? 'By hand' : 'Feed unknown'
}

export function DeploymentFeed({ slug, name }: { slug: string; name: string }) {
  const path = `/projects/${slug}/deployments`
  const current = useGet<DeploymentView[]>(path, { poll: 5000 })
  const roles = useGet<RoleCatalog>('/authz/roles')
  const cache = useQueryClient()
  const [draft, setDraft] = useState<Draft | null>(null)
  const [routineDraft, setRoutineDraft] = useState<{ revision: number; routineUpdates: RoutineSettings } | null>(null)
  const routine = useAction()
  const protection = useAction(current.reload)
  const { run, busy, err } = useAction()
  const e = current.data?.find(d => d.name === name)
  if (!e) return <ReadStatus q={current} what="deployment settings" />
  if (e.kind !== 'standing') return null
  const from = current.data?.find(d => d.id === e.promotedFromId)?.name || ''
  const base = { revision: e.feedRevision, feed: e.feed || '', from, approvalRole: e.approvalRole || '' }
  const value = draft || base
  const moved = !!draft && e.feedRevision !== draft.revision
  const routineValue = routineDraft || { revision: e.routineUpdatesRevision, routineUpdates: e.routineUpdates }
  const routineMoved = !!routineDraft && e.routineUpdatesRevision !== routineDraft.revision
  const routineChanged = !e.protected && !!routineDraft
  const saving = busy || routine.busy
  const feedChanged = !!draft || (e.feed === 'main' && !e.feedConfirmed)
  const edit = (change: Partial<Draft>) => setDraft({ ...value, ...change })
  const saveProtection = (protectedValue: boolean) => protection.run(() => put(`${path}/${name}/protected`, {
    protected: protectedValue, was: e.protected, meaning: protectedValue ? '' : e.unprotectMeaning,
  }))
  return <Section title="Releases" action={<HelpButton about="Deployment feed" guide="container-lifecycle">
    <p>Follows main deploys each change after it lands. Saving this setting, including its unchanged value, records your permission to deploy here, which {PRODUCT} checks again for each release.</p>
    <p>Promotion deploys the release the source last deployed successfully. It refuses a source taken down, never deployed, or currently observed stopped. Unknown or stale health does not block promotion. By hand deploys the latest bundle when you choose.</p>
  </HelpButton>}>
    <StaleNote q={current} />
    <form className="stack stack-3" onSubmit={async event => {
      event.preventDefault()
      if (saving || !current.current || moved || (routineChanged && routineMoved)) return
      if (routineChanged) {
        const saved = await routine.run(() => put<DeploymentView>(`${path}/${name}/routine-updates`, routineValue))
        if (saved) {
          cache.setQueryData<DeploymentView[]>(getKey(path), old => old?.map(d => d.id === e.id && d.routineUpdatesRevision <= saved.routineUpdatesRevision ? { ...d, routineUpdates: saved.routineUpdates, routineUpdatesRevision: saved.routineUpdatesRevision } : d))
          setRoutineDraft(null)
        }
      }
      if (!feedChanged) return
      const saved = await run(() => put<{ feed: string; feedRevision: number; feedConfirmed: boolean; promotedFromId?: string; approvalRole: string }>(`${path}/${name}/feed`, {
        revision: value.revision, feed: value.feed, approvalRole: value.approvalRole, from: value.feed === 'promotion' ? value.from : '',
      }))
      if (saved) {
        cache.setQueryData<DeploymentView[]>(getKey(path), old => old?.map(d => d.id === e.id && d.feedRevision <= saved.feedRevision ? { ...d, approvalRole: saved.approvalRole, feed: saved.feed, feedRevision: saved.feedRevision, feedConfirmed: saved.feedConfirmed, promotedFromId: saved.promotedFromId,
          promotionSource: saved.feed === 'promotion' ? (current.data?.find(source => source.name === value.from)?.displayLabel || value.from) : '',
          promotionRelease: 0,
          runtimeControls: saved.feed === 'promotion' ? { ...d.runtimeControls, deploy: 'Checking the source deployment…' } : d.runtimeControls,
        } : d))
        setDraft(null)
      }
    }}>
      <div className="form-grid">
        <FieldRow label={<>Protected <HelpButton about="Protected" guide="container-lifecycle">
          <p>On: a person okays every release before it goes out, Janitor updates included.</p>
          <p>Off: releases go out by themselves, as set below.</p>
          <p>Turn it on for apps people rely on. Leave it off for toys and sketches, or when your backups are good and you'd rather stay up to date.</p>
        </HelpButton></>}><div>
          {e.protected ? <QuietConfirm key={e.protected + e.unprotectMeaning} label="Protected" ask={e.unprotectMeaning}
            disabled={protection.busy || saving || !current.current} onYes={() => saveProtection(false)}
            trigger={(open, disabled) => <div className="row row-2"><Switch label="Protected" checked disabled={disabled} onChange={open} /><span>on{e.protectedReason && <> · {e.protectedReason}</>}</span></div>} />
            : <div className="row row-2"><Switch label="Protected" checked={false} disabled={protection.busy || saving || !current.current} onChange={() => { void saveProtection(true) }} /><span>off</span></div>}
        </div></FieldRow>
        <FieldRow label="Release feed"><select aria-label="Release feed" disabled={saving || !current.current} value={value.feed} onChange={event => edit({ feed: event.target.value, from: '' })}>
          <option value="main">Follows main</option><option value="promotion">Promoted from a deployment</option><option value="manual">By hand</option>
        </select></FieldRow>
        {value.feed === 'promotion' && <FieldRow label="Source deployment"><select aria-label="Source deployment" disabled={saving || !current.current} value={value.from} onChange={event => edit({ from: event.target.value })}>
          <option value="">Choose a source</option>
          {current.data?.filter(d => d.kind === 'standing' && d.id !== e.id).map(d => <option key={d.id} value={d.name}>{d.displayLabel || d.name}</option>)}
        </select></FieldRow>}
        {!e.protected && <>
        <RoutineUpdates value={routineValue.routineUpdates} disabled={saving || !current.current} moved={routineMoved}
          onChange={routineUpdates => setRoutineDraft({ ...routineValue, routineUpdates })} />
        <FieldRow label="Needs approval"><select aria-label="Needs approval" disabled={saving || !current.current} value={value.approvalRole} onChange={event => edit({ approvalRole: event.target.value })}>
          <option value="">None</option>
          {roles.data?.roles.filter(role => role.scope === 'project').map(role => <option key={role.name} value={role.name}>{role.title}</option>)}
        </select></FieldRow>
        </>}
      </div>
      {protection.err && <div className="error" role="alert">{protection.err}</div>}
      {e.protected ? <div>Every release waits for a person with release rights.</div> : <>
        <ReadStatus q={roles} what="approval roles" />
        {routineMoved && <p role="alert">The routine update settings changed while you were editing. Discard your changes and reload before saving.</p>}
        {routine.err && <p className="error" role="alert">{routine.err}</p>}
      </>}
      {moved && <div role="alert">The settings changed while you were editing. Current setting: {feedWords(e)}.
        <button type="button" onClick={() => setDraft({ ...value, revision: e.feedRevision })}>Keep my edits on the current settings</button>
      </div>}
      <div className="row row-2">
        <button className="primary" disabled={saving || !current.current || (!feedChanged && !routineChanged) || moved || (routineChanged && routineMoved) || (value.feed === 'promotion' && !value.from)} type="submit">Save settings</button>
        {(draft || routineDraft) && <button type="button" disabled={saving} onClick={() => { setDraft(null); setRoutineDraft(null) }}>Discard changes</button>}
      </div>
      {err && <div className="error" role="alert">{err}</div>}
    </form>
  </Section>
}
