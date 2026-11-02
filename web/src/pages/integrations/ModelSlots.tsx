import { useState } from 'react'
import { put } from '../../api'
import { fmtTokens as fmtTokensShared } from '../../format'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import { ListRow } from '../../ListRow'
import { PageHead } from '../../PageHead'
import { Link, useNavigate, useParams } from 'react-router-dom'
import type { ReactNode } from 'react'
import type { ModelCatalogList, ModelSlotMatrix, ModelSlotView } from '../../wire.gen'

// Model profiles choose models for tasks. Each task follows its fallback chain
// within the project's profile before trying the chain in standard.

const fmtTokens = (n?: number) => fmtTokensShared(n ?? 0)
// The four basic choices are named as the glossary names them; tasks by their own name.
const BASIC: Record<string, string> = { 'main-strong': 'Main · strong', 'main-fast': 'Main · fast', 'secondary-strong': 'Second opinion · strong', 'secondary-fast': 'Second opinion · fast' }
const jobName = (slot: string) => BASIC[slot] ?? slot.charAt(0).toUpperCase() + slot.slice(1)

// Follow the server's profile-first resolution, including its deciding profile.
function effective(rows: ModelSlotView[], slot: string, profile: string): { model: string; from: string; profile: string } {
  const bySlot = new Map(rows.map((r) => [r.slot, r]))
  for (const p of profile === 'standard' ? [profile] : [profile, 'standard']) {
    const seen = new Set<string>()
    let name = slot
    while (name && !seen.has(name)) {
      seen.add(name)
      const r = bySlot.get(name)
      const m = r?.patterns[p]
      if (m) return { model: m, from: name, profile: p }
      name = r?.fallback || (name === 'main-strong' ? '' : 'main-strong')
    }
  }
  return { model: '', from: '', profile: '' }
}

// The page's help, passed by Integrations so every menu page's help lives in one place.
type Help = { body: ReactNode; guide: string }

function useProfiles() {
  const slotsQ = useGet<ModelSlotMatrix>('/model-slots')
  return slotsQ
}

// What a profile chooses differently from standard, in a few words.
function differs(data: ModelSlotMatrix, p: string): string {
  if (p === 'standard') return 'The default profile'
  const own = data.slots.filter((r) => r.patterns[p]).map((r) => jobName(r.slot))
  return own.length === 0 ? 'Same as standard' : `Own models for ${own.join(', ')}`
}

// ModelProfiles: the list of profiles, standard first, each saying how it
// differs from standard and which projects use it. A row opens its page.
export function ModelProfiles({ help, title }: { help?: Help; title: string }) {
  const slotsQ = useProfiles()
  const nav = useNavigate()
  const [adding, setAdding] = useState<string | null>(null)
  const data = slotsQ.data
  const add = () => adding && nav(`/integrations/profiles/${adding}`)
  return (
    <div>
      <PageHead page="integrations" sub={title} help={help} line="Which AI model does each task, for each kind of project"
        actions={adding === null ? <button className="primary" onClick={() => setAdding('')}>Add a profile</button>
          : <span className="row" style={{ gap: 6 }}>
            <input autoFocus value={adding} placeholder="name, e.g. high" aria-label="profile name"
              onChange={(e) => setAdding(e.target.value.toLowerCase())}
              onKeyDown={(e) => { if (e.key === 'Enter') add(); if (e.key === 'Escape') setAdding(null) }} style={{ width: 150 }} />
            <button className="primary" disabled={!adding} onClick={add}>Add</button>
            <button className="subtle" onClick={() => setAdding(null)}>Cancel</button>
          </span>} />
      {!data ? <ReadStatus q={slotsQ} what="the model profiles" /> : <>
        <StaleNote q={slotsQ} />
        <Section title="" label="Profiles"><div role="list">
          {data.profiles.map((p) => {
            const on = data.projects.filter((x) => (x.profile || 'standard') === p)
            const to = `/integrations/profiles/${p}`
            return (
              <div key={p} role="listitem" className="list-row-open int-row" onClick={() => nav(to)}>
                <ListRow className="int-list-row"
                  identity={<div style={{ minWidth: 0 }}>
                    <Link to={to} className="int-name" onClick={(e) => e.stopPropagation()}>{p}</Link>
                    <div className="muted small">{differs(data, p)}</div>
                  </div>}
                  state={<span className="muted small">{on.length === 0 ? 'No projects' : on.map((x) => x.name || x.slug).join(', ')}</span>} />
              </div>
            )
          })}
        </div></Section>
      </>}
    </div>
  )
}

// ModelProfilePage: one profile — its model for each job, and the projects
// on it. A new profile exists once its first job has a model.
export function ModelProfilePage() {
  const { profile = 'standard' } = useParams()
  const slotsQ = useProfiles()
  const modelsQ = useGet<ModelCatalogList>('/model-catalog', { kind: 'config' })
  const data = slotsQ.data
  const models = modelsQ.data?.models ?? []
  const [custom, setCustom] = useState<string | null>(null) // the job being given a model by hand
  const [customVal, setCustomVal] = useState('')
  const [moving, setMoving] = useState('')
  // A project's profile is shown in the matrix, so its write refreshes it too.
  const { run, err: actErr } = useAction(undefined, { affects: ['/model-slots'] })

  const crumb = <div className="muted small int-crumb"><Link to="/integrations">Integrations</Link> › <Link to="/integrations/profiles">Model profiles</Link></div>
  if (!data) return <div>{crumb}<h1>{profile}</h1><ReadStatus q={slotsQ} what="the model profiles" /></div>

  const isNew = !data.profiles.includes(profile)
  const spent = data.slots.some((r) => r.spend7d)
  const on = data.projects.filter((x) => (x.profile || 'standard') === profile)
  const others = data.projects.filter((x) => (x.profile || 'standard') !== profile)

  const set = (slot: string, pattern: string) =>
    run(async () => { await put('/model-slots', { slot, profile, pattern }); setCustom(null) })
  const moveProject = () =>
    run(async () => { await put(`/projects/${moving}/model-profile`, { profile }); setMoving('') })

  const cell = (row: ModelSlotView) => {
    const mapped = row.patterns[profile] ?? ''
    if (custom === row.slot) {
      return (
        <span className="row" style={{ gap: 6 }}>
          <input autoFocus value={customVal} placeholder="provider/model-pattern" aria-label={`${jobName(row.slot)} model`}
            onChange={(e) => setCustomVal(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && customVal.includes('/')) set(row.slot, customVal); if (e.key === 'Escape') setCustom(null) }}
            style={{ width: 200 }} />
          <button className="subtle" disabled={!customVal.includes('/')} onClick={() => set(row.slot, customVal)}>Set</button>
          <button className="subtle" onClick={() => setCustom(null)}>Cancel</button>
        </span>
      )
    }
    // An unset job says what it gets instead, and from where.
    const eff = effective(data.slots, row.slot, profile)
    const unset = row.slot === 'main-strong' && profile === 'standard' ? 'Not set'
      : eff.profile === 'standard' && profile !== 'standard' && eff.from === row.slot ? `Same as standard (${eff.model})`
        : eff.model ? `Same as ${eff.profile === 'standard' && profile !== 'standard' ? "standard's " : ''}${jobName(eff.from)} (${eff.model})` : 'Not set'
    return (
      <select aria-label={`${jobName(row.slot)} model`}
        value={models.some((m) => m.pattern === mapped) ? mapped : mapped ? '__custom_current__' : ''}
        onChange={(e) => {
          const v = e.target.value
          if (v === '__custom__') { setCustom(row.slot); setCustomVal(mapped) }
          else set(row.slot, v)
        }}
        style={{ maxWidth: 280 }}>
        <option value="">{unset}</option>
        {models.map((m) => <option key={m.pattern} value={m.pattern}>{m.pattern}</option>)}
        {mapped && !models.some((m) => m.pattern === mapped) && <option value="__custom_current__">{mapped}</option>}
        <option value="__custom__">Another model…</option>
      </select>
    )
  }

  return (
    <div>
      {crumb}
      <h1>{profile}</h1>
      <div className="page-head-line" style={{ marginBottom: 14 }}>
        {profile === 'standard' ? 'The default profile: every project uses it unless moved to another.'
          : isNew ? "A new profile. Choose a model for any task to keep it; tasks left unset follow this profile's basic choices, then standard's models."
            : "Tasks left unset follow this profile's basic choices, then standard's models."}
      </div>
      <StaleNote q={slotsQ} />
      {!modelsQ.data && <ReadStatus q={modelsQ} what="the model catalogue" className="small" />}
      <StaleNote q={modelsQ} />
      {/* A connection whose models could not be listed is said once, never shown as offering none. */}
      {(modelsQ.data?.unlisted ?? []).map((u) => (
        <div key={u.binding} className="error small">Couldn't list the models of {u.binding}: {u.error}</div>
      ))}
      {actErr && <div className="error">{actErr}</div>}

      <Section title="Tasks" count={data.slots.length}>
        <table className="stack slots-table" style={{ width: '100%' }}>
          <thead>
            <tr>
              <th className="small muted" style={{ textAlign: 'left' }}>Task</th>
              <th className="small muted" style={{ textAlign: 'left' }}>Model</th>
              {spent && <th className="small muted" style={{ textAlign: 'left' }} title={data.spendNote}>Spent (7 days)</th>}
            </tr>
          </thead>
          <tbody>
            {data.slots.map((row) => (
              <tr key={row.slot}>
                <td className="stack-lead" style={{ verticalAlign: 'top', paddingRight: 16 }}>
                  <strong>{jobName(row.slot)}</strong>
                  <div className="muted small">{row.slot === 'main-strong' ? 'Used by tasks left unset' : jobName(row.gloss)}</div>
                </td>
                <td style={{ verticalAlign: 'top', paddingRight: 8 }}>{cell(row)}</td>
                {spent && <td className="small muted" style={{ verticalAlign: 'top', whiteSpace: 'nowrap' }}>
                  {row.spend7d ? `${fmtTokens(row.spend7d.input)} in · ${fmtTokens(row.spend7d.output)} out` : ''}
                </td>}
              </tr>
            ))}
          </tbody>
        </table>
      </Section>

      {!isNew && <Section title="Projects" count={on.length}>
        {on.length === 0 ? <p className="muted small">No projects use it.</p>
          : <p>{on.map((x, i) => <span key={x.slug}>{i > 0 && ', '}<Link to={`/projects/${x.slug}`}>{x.name || x.slug}</Link></span>)}</p>}
        {others.length > 0 && (
          <p className="row" style={{ gap: 8 }}>
            <span className="small">Move</span>
            <select aria-label="project" value={moving} onChange={(e) => setMoving(e.target.value)}>
              <option value="">a project…</option>
              {others.map((x) => <option key={x.slug} value={x.slug}>{x.name || x.slug} (now {x.profile || 'standard'})</option>)}
            </select>
            <span className="small">here</span>
            <button disabled={!moving} onClick={() => void moveProject()}>Move</button>
          </p>
        )}
      </Section>}
    </div>
  )
}
