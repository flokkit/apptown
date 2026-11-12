import { AUDIENCE_WORDS, DATA_WORDS } from '../../../targets'
import { useState } from 'react'
import './deployments.css'
import { put } from '../../../api'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { Section } from '../../../Section'
import { FieldRow } from '../../../FieldRow'
import type { ClassificationView } from '../../../wire.gen'

// Who a deployment serves and what data it holds (deployment.md, Audience
// and data). The owner says which values the target and its offer refuse,
// what a change adds or removes, and the sentence a person accepts to lower
// protection; this form shows them before saving.

// PROTECTED_WORDS explains protection wherever a person picks audience or data.
export const PROTECTED_WORDS = 'Protected means it reaches people outside the organisation or holds production data: only people with release permission can deploy there. Lowering its audience or data later asks you to confirm. A deployment that has run stays on its target.'
export const isProtected = (audience: string, data: string) => audience === 'partners' || audience === 'public' || data === 'production'

type Draft = { audience: string; data: string; was: { audience: string; data: string } }

export function DeploymentClassification({ slug, name, onChanged }: { slug: string; name: string; onChanged: () => void }) {
  const path = `/projects/${slug}/deployments/${name}/classification`
  const current = useGet<ClassificationView>(path)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const { run, busy, err, setErr } = useAction()
  const c = current.data
  const changed = !!c && !!draft && (draft.audience !== c.audience || draft.data !== c.data)
  const proposal = useGet<ClassificationView>(changed ? `${path}?audience=${encodeURIComponent(draft!.audience)}&data=${encodeURIComponent(draft!.data)}` : null)
  if (!c) return <ReadStatus q={current} what="audience and data" />
  const value = draft ?? { audience: c.audience, data: c.data, was: { audience: c.audience, data: c.data } }
  const moved = !!draft && (draft.was.audience !== c.audience || draft.was.data !== c.data)
  const p = changed ? proposal.data?.proposed : undefined
  const edit = (change: Partial<Draft>) => { setConfirmed(false); setErr(''); setDraft({ ...value, ...change }) }
  const save = async () => {
    const saved = await run(() => put(path, { audience: value.audience, data: value.data, was: value.was, meaning: p?.lowers ? p.meaning : '' }))
    // A refusal may mean someone else saved first: read the current values
    // so the conflict line can show them and offer to keep these edits.
    await current.reload()
    if (saved) { setDraft(null); setConfirmed(false); onChanged() }
  }
  return <Section title="Audience and data">
    <StaleNote q={current} />
    <div className="stack stack-3">
      <FieldRow label="Audience" help="who it is for"><select aria-label="Audience" disabled={busy || !current.current} value={value.audience} onChange={e => edit({ audience: e.target.value })}>
          {c.audiences.map(a => <option key={a.value} value={a.value} disabled={!!a.refusal} title={a.refusal}>{AUDIENCE_WORDS[a.value] ?? a.value}{a.refusal ? ' (not here)' : ''}</option>)}
        </select></FieldRow>
        <FieldRow label="Data" help="what kind of data it holds"><select aria-label="Data" disabled={busy || !current.current} value={value.data} onChange={e => edit({ data: e.target.value })}>
          {c.dataClasses.map(d => <option key={d.value} value={d.value} disabled={!!d.refusal} title={d.refusal}>{DATA_WORDS[d.value] ?? d.value}{d.refusal ? ' (not here)' : ''}</option>)}
        </select></FieldRow>
      <RefusedChoices view={c} />
      <div className="muted small">{isProtected(c.audience, c.data) ? 'This deployment is protected.' : 'This deployment is not protected.'} {PROTECTED_WORDS}</div>
      {changed && !p && <ReadStatus q={proposal} what="what this change does" />}
      {p && <div className="stack stack-1" aria-label="What this change does">
        {p.refusal && <div className="error" role="alert">Not allowed: {p.refusal}</div>}
        {p.adds.length > 0 && <div><strong>Adds</strong><ul className="classification-list">{p.adds.map(a => <li key={a}>{a}</li>)}</ul></div>}
        {p.removes.length > 0 && <div><strong>Removes</strong><ul className="classification-list">{p.removes.map(r => <li key={r}>{r}</li>)}</ul></div>}
        <div className="muted small">{c.effect}</div>
        {p.lowers && !p.refusal && <label className="row row-1"><input type="checkbox" checked={confirmed} onChange={e => setConfirmed(e.target.checked)} /> <span>{p.meaning}</span></label>}
      </div>}
      {moved && <div role="alert">The audience or data changed while you were editing. Now: {AUDIENCE_WORDS[c.audience] ?? c.audience}, {DATA_WORDS[c.data] ?? c.data}.
        <button type="button" onClick={() => edit({ was: { audience: c.audience, data: c.data } })}>Keep my edits on the current values</button>
      </div>}
      <div className="row row-2">
        <button className="primary" type="button" onClick={save}
          disabled={busy || !current.current || !changed || moved || !p || !!p.refusal || (p.lowers && !confirmed)}>Save audience and data</button>
        {draft && <button type="button" disabled={busy} onClick={() => { setDraft(null); setConfirmed(false); setErr('') }}>Discard changes</button>}
      </div>
      {err && <div className="error" role="alert">{err}</div>}
    </div>
  </Section>
}

// RefusedChoices says, at the fields, why a value cannot be chosen here.
function RefusedChoices({ view }: { view: ClassificationView }) {
  const why = [...new Set([...view.audiences, ...view.dataClasses].map(c => c.refusal).filter(Boolean))]
  if (why.length === 0) return null
  return <ul className="classification-list muted small" aria-label="Not available here">{why.map(w => <li key={w}>Not here: {w}</li>)}</ul>
}
