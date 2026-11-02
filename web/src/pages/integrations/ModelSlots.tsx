import { GlanceRow } from '../../GlanceRow'
import { useState } from 'react'
import { put } from '../../api'
import { fmtTokens as fmtTokensShared } from '../../format'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ModelCatalogList, ModelSlotMatrix } from '../../wire.gen'

// Model slots: workflows name slots; slots pick models.
// Selectors are fed by /model-catalog — live-discovered custom-endpoint
// models, curated openrouter models, and each binding's default pattern.


const fmtTokens = (n?: number) => fmtTokensShared(n ?? 0)

export function ModelSlotsPanel() {
  const slotsQ = useGet<ModelSlotMatrix>('/model-slots')
  const modelsQ = useGet<ModelCatalogList>('/model-catalog', { kind: 'config' })
  const data = slotsQ.data
  const models = modelsQ.data?.models ?? []
  const [custom, setCustom] = useState<string | null>(null) // "slot|profile" being given a custom pattern
  const [customVal, setCustomVal] = useState('')
  const [newProfile, setNewProfile] = useState<string | null>(null)
  // A project's profile is shown in the slot matrix, so its write refreshes
  // the matrix too.
  const { run, err: actErr } = useAction(undefined, { affects: ['/model-slots'] })

  if (!data) return <ReadStatus q={slotsQ} what="the model slots" />

  // A freshly added profile is a column with no cells yet — keep it visible
  // locally until its first cell is saved.
  const profiles = [...data.profiles]
  if (newProfile && !profiles.includes(newProfile)) profiles.push(newProfile)

  const set = (slot: string, profile: string, pattern: string) =>
    run(async () => { await put('/model-slots', { slot, profile, pattern }); setCustom(null) })
  const setProjectProfile = (slug: string, profile: string) =>
    run(() => put(`/projects/${slug}/model-profile`, { profile }))

  const cell = (slot: string, profile: string, mapped: string, fallbackLabel: string) => {
    const key = slot + '|' + profile
    if (custom === key) {
      return (
        <span className="row" style={{ gap: 6 }}>
          <input autoFocus value={customVal} placeholder="provider/model-pattern"
            onChange={(e) => setCustomVal(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && customVal.includes('/')) set(slot, profile, customVal); if (e.key === 'Escape') setCustom(null) }}
            style={{ width: 180, fontSize: 'var(--fs-xs)' }} />
          <button className="subtle" disabled={!customVal.includes('/')} onClick={() => set(slot, profile, customVal)}>set</button>
          <button className="subtle" onClick={() => setCustom(null)}>cancel</button>
        </span>
      )
    }
    return (
      <select value={models.some((m) => m.pattern === mapped) ? mapped : mapped ? '__custom_current__' : ''}
        onChange={(e) => {
          const v = e.target.value
          if (v === '__custom__') { setCustom(key); setCustomVal(mapped) }
          else set(slot, profile, v)
        }}
        style={{ maxWidth: 230, fontSize: 'var(--fs-xs)' }}>
        <option value="">{fallbackLabel}</option>
        {models.map((m) => (
          <option key={m.pattern} value={m.pattern}>{m.pattern}</option>
        ))}
        {mapped && !models.some((m) => m.pattern === mapped) &&
          <option value="__custom_current__">{mapped} (custom)</option>}
        <option value="__custom__">Custom pattern…</option>
      </select>
    )
  }

  return (
    <div className="conn-card" style={{ maxWidth: 980 }}>
      <details className="small"><summary>Model fallback</summary>Empty cells use <code>standard</code>, then the slot's fallback chain.</details>
      <StaleNote q={slotsQ} />
      {!modelsQ.data && <ReadStatus q={modelsQ} what="the model catalogue" className="small" />}
      <StaleNote q={modelsQ} />
      {/* A binding whose models could not be listed is said, never shown as offering none. */}
      {(modelsQ.data?.unlisted ?? []).map((u) => (
        <div key={u.binding} className="error small">Not every model of {u.binding} is listed: {u.error}</div>
      ))}
      {actErr && <div className="error">{actErr}</div>}
      <table className="kv stack slots-table" style={{ width: '100%' }}>
        <thead>
          <tr>
            <th style={{ textAlign: 'left' }} className="small muted">slot</th>
            {profiles.map((p) => <th key={p} style={{ textAlign: 'left' }} className="small muted">{p}</th>)}
            <th style={{ textAlign: 'left' }} className="small muted">Spent (7 days)</th>
          </tr>
        </thead>
        <tbody>
          {data.slots.map((row) => (
            <GlanceRow name={0} summary={row.patterns.standard || 'Not set'} labels={["Purpose", ...profiles, "Spent in 7 days"]} key={row.slot}>
              <td className="kv-key stack-lead" style={{ width: 110, verticalAlign: 'top' }}>
                {row.slot}
                {(row.gloss || row.consumers?.length) && <details className="inline-detail small"><summary>Use</summary><p>{row.gloss}</p><p>{(row.consumers ?? []).join(' · ')}</p></details>}
              </td>
              {profiles.map((p) => (
                <td key={p} data-label={p} style={{ verticalAlign: 'top', paddingRight: 8 }}>
                  {cell(row.slot, p, row.patterns[p] ?? '',
                    row.slot === 'default' ? '(unset)'
                      : p !== 'standard' && row.patterns['standard'] ? '(same as standard)'
                        : `(fall back → ${row.fallback ?? 'default'})`)}
                </td>
              ))}
              <td className="small muted" style={{ verticalAlign: 'top', maxWidth: 260 }}>
                {row.spend7d && <div>{fmtTokens(row.spend7d.input)} in / {fmtTokens(row.spend7d.output)} out</div>}
              </td>
            </GlanceRow>
          ))}
        </tbody>
      </table>
      <div className="conn-foot row" style={{ gap: 12 }}>
        {newProfile === null ? (
          <button className="subtle" onClick={() => setNewProfile('')}>+ profile</button>
        ) : (
          <span className="row" style={{ gap: 6 }}>
            <input autoFocus value={newProfile} placeholder="profile name (e.g. high)"
              onChange={(e) => setNewProfile(e.target.value.toLowerCase())}
              onKeyDown={(e) => { if (e.key === 'Escape') setNewProfile(null) }}
              style={{ width: 160, fontSize: 'var(--fs-xs)' }} />
            <span className="muted small">set any cell in the new column to keep it</span>
          </span>
        )}
        <span className="muted small">{data.spendNote}</span>
      </div>

      <div className="small conn-status" style={{ marginTop: 14 }}>
<b>Project profiles</b>
      </div>
      <table className="kv">
        <tbody>
          {data.projects.map((p) => (
            <tr key={p.slug}>
              <td className="kv-key" style={{ width: 200 }}>{p.name}</td>
              <td className="kv-val">
                <select value={p.profile || 'standard'} onChange={(e) => setProjectProfile(p.slug, e.target.value)} style={{ fontSize: 'var(--fs-xs)' }}>
                  {profiles.map((pr) => <option key={pr} value={pr}>{pr}</option>)}
                  {!profiles.includes(p.profile) && p.profile && <option value={p.profile}>{p.profile}</option>}
                </select>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
