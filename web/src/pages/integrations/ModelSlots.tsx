import { usePageName } from '../../chrome'
import { ListTable } from '../../ListTable'
import { ProjectCount } from '../DependentProjects'
import './ModelSlots.css'
import { useState } from 'react'
import { put } from '../../api'
import { fmtTokens as fmtTokensShared } from '../../format'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import { MoreMenu } from '../../MoreMenu'
import { effective, jobName } from '../../parts/modelProfiles'
import { ListRow } from '../../ListRow'
import { PageHead } from '../../PageHead'
import { Link, useNavigate, useParams } from 'react-router-dom'
import type { ReactNode } from 'react'
import type { ModelCatalogList, ModelSlotMatrix, ModelSlotView } from '../../wire.gen'

// Model profiles choose models for tasks. Each task follows its fallback chain
// within the project's profile before trying the chain in standard.

const fmtTokens = (n?: number) => fmtTokensShared(n ?? 0)
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
          : <span className="row model-slots-model-profiles-span">
            <input autoFocus value={adding} placeholder="name, e.g. high" aria-label="profile name"
              onChange={(e) => setAdding(e.target.value.toLowerCase())}
              onKeyDown={(e) => { if (e.key === 'Enter') add(); if (e.key === 'Escape') setAdding(null) }} className="model-slots-model-profiles-input" />
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
                  identity={<div className="model-slots-div">
                    <Link to={to} className="int-name" onClick={(e) => e.stopPropagation()}>{p}</Link>
                    <div className="muted small">{differs(data, p)}</div>
                  </div>}
                  state={<ProjectCount to={`${to}/projects`} count={on.length} />} />
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
export function ModelProfilePage({ projects = false }: { projects?: boolean }) {
  const { profile = 'standard' } = useParams()
  const slotsQ = useProfiles()
  const modelsQ = useGet<ModelCatalogList>('/model-catalog', { kind: 'config' })
  const data = slotsQ.data
  usePageName(`/integrations/profiles/${profile}`, profile)
  const models = modelsQ.data?.models ?? []
  const [custom, setCustom] = useState<string | null>(null) // the job being given a model by hand
  const [customVal, setCustomVal] = useState('')
  const nav = useNavigate()
  const { run, err: actErr } = useAction()

  // A profile's name is the word an administrator chose; the title reads it
  // as a name ("Standard profile"), not as a bare lowercase key.
  const title = projects ? 'Projects' : `${profile.charAt(0).toUpperCase()}${profile.slice(1)} profile`
  if (!data) return <div><h1>{title}</h1><ReadStatus q={slotsQ} what="the model profiles" /></div>

  const isNew = !data.profiles.includes(profile)
  const spent = data.slots.some((r) => r.spend7d)
  const on = data.projects.filter((x) => (x.profile || 'standard') === profile)
  const others = data.projects.filter((x) => (x.profile || 'standard') !== profile)

  const set = (slot: string, pattern: string) =>
    run(async () => { await put('/model-slots', { slot, profile, pattern }); setCustom(null) })

  const cell = (row: ModelSlotView) => {
    const mapped = row.patterns[profile] ?? ''
    if (custom === row.slot) {
      return (
        <span className="row model-slots-model-profiles-span">
          <input autoFocus value={customVal} placeholder="provider/model-pattern" aria-label={`${jobName(row.slot)} model`}
            onChange={(e) => setCustomVal(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && customVal.includes('/')) set(row.slot, customVal); if (e.key === 'Escape') setCustom(null) }}
             className="model-slots-cell-input" />
          <button className="subtle" disabled={!customVal.includes('/')} onClick={() => set(row.slot, customVal)}>Set</button>
          <button className="subtle" onClick={() => setCustom(null)}>Cancel</button>
        </span>
      )
    }
    // An unset job says what it gets instead, and from where.
    const eff = effective(data.slots, row.slot, profile)
    const first = row.fallback || 'main-strong'
    const source = first === eff.from ? jobName(eff.from) : `${jobName(first)} → ${jobName(eff.from)}`
    const unset = row.slot === 'main-strong' && profile === 'standard' ? 'Not set'
      : eff.profile === 'standard' && profile !== 'standard' && eff.from === row.slot ? `Same as standard (${eff.model})`
        : eff.model ? `Same as ${eff.profile === 'standard' && profile !== 'standard' ? "standard's " : ''}${source} (${eff.model})` : 'Not set'
    const binding = eff.model.split('/')[0]
    const notEnabled = modelsQ.data && !modelsQ.refreshFailed && eff.model
      && !models.some((m) => m.pattern === eff.model)
      && !modelsQ.data.unlisted?.some((u) => u.binding === binding)
    return (
      <div>
        <select aria-label={`${jobName(row.slot)} model`}
          value={models.some((m) => m.pattern === mapped) ? mapped : mapped ? '__custom_current__' : ''}
          onChange={(e) => {
            const v = e.target.value
            if (v === '__custom__') { setCustom(row.slot); setCustomVal(mapped) }
            else set(row.slot, v)
          }}
           className="model-slots-cell-select">
          <option value="">{unset}</option>
          {models.map((m) => <option key={m.pattern} value={m.pattern}>{m.pattern}</option>)}
          {mapped && !models.some((m) => m.pattern === mapped) && <option value="__custom_current__">{mapped}</option>}
          <option value="__custom__">Another model…</option>
        </select>
        <div className="muted small">{mapped || unset}</div>
        {notEnabled && <div className="error small">not enabled on {binding}</div>}
      </div>
    )
  }

  return (
    <div>

      <h1>{title}</h1>
      <div className="page-head-line model-slots-model-profile-page-page-head-line">
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

      {!projects && <Section title="Tasks" count={data.slots.length}>
        <ListTable label="Tasks" className="slots-table model-slots-model-profile-page-slots-table" rows={data.slots} rowKey={row => row.slot} columns={[
          { key: 'task', label: 'Task', phone: 'name', className: 'model-slots-stack-lead', render: row => <>
            <strong>{jobName(row.slot)}</strong>
            <div className="muted small">{jobName(row.gloss)}</div>
            <div className="muted small">Used by: {row.consumers?.length ? row.consumers.join(', ') : 'no jobs request it directly'}</div>
          </> },
          { key: 'model', label: 'Model', phone: 'detail', className: 'model-slots-td', render: row => cell(row) },
          ...(spent ? [{ key: 'spent', label: 'Spent (7 days)', phone: 'extra' as const, className: 'small muted model-slots-td-2', render: (row: typeof data.slots[number]) => row.spend7d ? `${fmtTokens(row.spend7d.input)} in · ${fmtTokens(row.spend7d.output)} out` : '' }] : []),
        ]} />
      </Section>}

      {!isNew && !projects && <p><ProjectCount to={`/integrations/profiles/${profile}/projects`} count={on.length} /></p>}
      {projects && <Section title="Projects" count={on.length}>
        {on.length === 0 ? <p className="muted small">No projects use it.</p>
          : <p>{on.map((x, i) => <span key={x.slug}>{i > 0 && ', '}<Link to={`/projects/${x.slug}`}>{x.name || x.slug}</Link></span>)}</p>}
        {others.length > 0 && <><h3>Other projects</h3><div role="list">
          {others.map(x => <div key={x.slug} role="listitem">
            <ListRow to={`/projects/${x.slug}`} identity={<Link to={`/projects/${x.slug}`}>{x.name || x.slug}</Link>}
              state={<span className="muted small">Uses {x.profile || 'standard'}</span>}
              action={<MoreMenu label={x.name || x.slug} items={[{
                label: 'Move project here', hint: `Choose ${profile} in this project's Models settings`,
                onClick: () => nav(`/projects/${x.slug}/settings/models?profile=${encodeURIComponent(profile)}`),
              }]} />} />
          </div>)}
        </div></>}
      </Section>}
    </div>
  )
}
