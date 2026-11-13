import { AUDIENCES, DATA_CLASSES, AUDIENCE_WORDS, DATA_WORDS } from '../../targets'
import type { Dispatch } from 'react'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { useHolds } from '../../instancePages'
import { ReadStatus } from '../../ReadGate'
import type { CreationTarget } from '../../wire.gen'
import type { Draft, DraftAct } from './draft'

export function PlacementStep({ d, act, busy, onCreate }: { d: Draft; act: Dispatch<DraftAct>; busy: boolean; onCreate: () => void }) {
  const set = (fields: Extract<DraftAct, { type: 'repo' }>['set']) => act({ type: 'repo', set: fields })
  const q = useGet<CreationTarget[]>(`/project-creation-targets?packs=${encodeURIComponent((d.packs ?? []).join(','))}&audience=${d.audience}&data=${d.data}`, { kind: 'config' })
  const targets = [...(q.data ?? [])].sort((a, b) => Number(b.default) - Number(a.default))
  const selected = d.target === '' ? targets.find(t => t.default) : targets.find(t => t.name === d.target)
  const ready = d.target === 'none' || (q.current && (d.target === '' ? !selected?.reason : !!selected && !selected.reason))
  const manage = useHolds('platform:manage-deploy-targets')
  return <div className="np-panel">
    <fieldset className="repository-step-targets"><legend><strong>Who is it for?</strong></legend>
      {AUDIENCES.map(value => <label key={value} className="row repository-step-label"><input type="radio" name="audience" checked={d.audience === value} onChange={() => set({ audience: value })} />{AUDIENCE_WORDS[value]}</label>)}
    </fieldset>
    <fieldset className="repository-step-targets mt-3"><legend><strong>What data will it hold?</strong></legend>
      {DATA_CLASSES.map(value => <label key={value} className="row repository-step-label"><input type="radio" name="data" checked={d.data === value} onChange={() => set({ data: value })} />{DATA_WORDS[value]}</label>)}
    </fieldset>
    <fieldset className="repository-step-targets mt-3"><legend><strong>Where it runs</strong></legend>
      <ReadStatus q={q} what="deployment targets" />
      {q.current && targets.map(t => {
        const audience = t.audiences?.length && !t.audiences.includes(d.audience) ? t.audiences[0] : d.audience
        const data = t.data?.length && !t.data.includes(d.data) ? t.data[0] : d.data
        const fixes = [audience !== d.audience ? AUDIENCE_WORDS[audience] : '', data !== d.data ? DATA_WORDS[data] : ''].filter(Boolean)
        return <div key={t.name}>
          <label className="row repository-step-label"><input type="radio" name="target" checked={d.target === t.name || (d.target === '' && t.default)} disabled={!!t.reason} onChange={() => set({ target: t.default ? '' : t.name })} />
            <span><strong>{t.default ? 'Default target · ' : ''}{t.label || t.name}</strong><br /><span className="muted small">{t.description}</span>{t.reason && <span className="small"> · {t.reason}</span>}<code className="tech-line muted">{t.name}</code></span>
          </label>
          {t.attributeFix && fixes.length > 0 && <button className="subtle" onClick={() => set({ audience, data, target: t.name })}>Use {fixes.join(' and ')}</button>}
        </div>
      })}
      <label className="row repository-step-label"><input type="radio" name="target" checked={d.target === 'none'} onChange={() => set({ target: 'none' })} /><span><strong>Nowhere yet</strong><br /><span className="muted small">Land changes without deploying them.</span></span></label>
      {d.target !== '' && d.target !== 'none' && q.current && !selected && <p className="error">This target is no longer offered. Choose where the project runs.</p>}
    </fieldset>
    <p className="small muted">Missing a target? {manage ? <Link to="/targets">Targets</Link> : 'Ask a workspace admin'}</p>
    <div className="row mt-3"><button className="subtle" onClick={() => act({ type: 'step', step: 3 })}>← back</button><span className="grow" /><button className="primary" disabled={busy || !ready} onClick={onCreate}>{busy ? 'Creating…' : 'Create project'}</button></div>
  </div>
}
