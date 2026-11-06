import { PRODUCT } from '../../../brand'
import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { put } from '../../../api'
import { getKey, useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { Section } from '../../../Section'
import { FieldRow } from '../../../FieldRow'
import { HelpButton } from '../../../PageHead'
import type { DeploymentView } from '../../../wire.gen'

type Draft = { revision: number; feed: string; from: string }

export function feedWords(e: Pick<DeploymentView, 'feed' | 'promotionSource'>) {
  return e.feed === 'main' ? 'Follows main' : e.feed === 'promotion' ? `Promoted from ${e.promotionSource || 'a source not yet chosen'}` : e.feed === 'manual' ? 'By hand' : 'Feed unknown'
}

export function DeploymentFeed({ slug, name }: { slug: string; name: string }) {
  const path = `/projects/${slug}/deployments`
  const current = useGet<DeploymentView[]>(path, { poll: 5000 })
  const cache = useQueryClient()
  const [draft, setDraft] = useState<Draft | null>(null)
  const { run, busy, err } = useAction()
  const e = current.data?.find(d => d.name === name)
  if (!e) return <ReadStatus q={current} what="deployment settings" />
  if (e.kind !== 'standing') return null
  const from = current.data?.find(d => d.id === e.promotedFromId)?.name || ''
  const base = { revision: e.feedRevision, feed: e.feed || '', from }
  const value = draft || base
  const moved = !!draft && e.feedRevision !== draft.revision
  const edit = (change: Partial<Draft>) => setDraft({ ...value, ...change })
  return <Section title="Settings" action={<HelpButton about="Deployment feed" guide="container-lifecycle">
    <p>Follows main deploys each change after it lands. Saving it records your permission to deploy here, which {PRODUCT} checks again for each release.</p>
    <p>Promotion deploys the release the source last deployed successfully. It refuses a source taken down, never deployed, or currently observed stopped. Unknown or stale health does not block promotion. By hand deploys the latest bundle when you choose.</p>
  </HelpButton>}>
    <StaleNote q={current} />
    <form className="stack stack-3" onSubmit={async event => {
      event.preventDefault()
      const saved = await run(() => put<{ feed: string; feedRevision: number; feedConfirmed: boolean; promotedFromId?: string }>(`${path}/${name}/feed`, {
        revision: value.revision, feed: value.feed, from: value.feed === 'promotion' ? value.from : '',
      }))
      if (saved) {
        cache.setQueryData<DeploymentView[]>(getKey(path), old => old?.map(d => d.id === e.id && d.feedRevision <= saved.feedRevision ? { ...d, feed: saved.feed, feedRevision: saved.feedRevision, feedConfirmed: saved.feedConfirmed, promotedFromId: saved.promotedFromId,
          promotionSource: saved.feed === 'promotion' ? (current.data?.find(source => source.name === value.from)?.displayLabel || value.from) : '',
          promotionRelease: 0,
          promotionBlocked: saved.feed === 'promotion' ? 'Checking the source deployment…' : '',
        } : d))
        setDraft(null)
      }
    }}>
      <FieldRow label="Release feed"><select aria-label="Release feed" disabled={busy || !current.current} value={value.feed} onChange={event => edit({ feed: event.target.value, from: '' })}>
        <option value="main">Follows main</option><option value="promotion">Promoted from a deployment</option><option value="manual">By hand</option>
      </select></FieldRow>
      {value.feed === 'promotion' && <FieldRow label="Source deployment"><select aria-label="Source deployment" disabled={busy || !current.current} value={value.from} onChange={event => edit({ from: event.target.value })}>
        <option value="">Choose a source</option>
        {current.data?.filter(d => d.kind === 'standing' && d.id !== e.id).map(d => <option key={d.id} value={d.name}>{d.displayLabel || d.name}</option>)}
      </select></FieldRow>}
      {moved && <div role="alert">The feed changed while you were editing. Current setting: {feedWords(e)}.
        <button type="button" onClick={() => setDraft({ ...value, revision: e.feedRevision })}>Keep my edits on the current settings</button>
      </div>}
      <div className="row row-2">
        <button className="primary" disabled={busy || !current.current || !draft || moved || (value.feed === 'promotion' && !value.from)} type="submit">Save feed</button>
        {draft && <button type="button" disabled={busy} onClick={() => setDraft(null)}>Discard changes</button>}
      </div>
      {err && <div className="error" role="alert">{err}</div>}
    </form>
  </Section>
}
