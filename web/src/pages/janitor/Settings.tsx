import { useState } from 'react'
import { put } from '../../api'
import { useHolds } from '../../instancePages'
import { FieldRow } from '../../FieldRow'
import { Link } from 'react-router-dom'
import { useGet, useAction } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import { PageHead } from '../../PageHead'
import { fmtWhen } from '../../format'
import type { JanitorView, JanitorProject, JanitorChoices, StandingRevision } from '../../wire.gen'

const projectTo = (p: JanitorProject) => `/projects/${p.slug}#janitor`
const levelName = (level: string) => level === 'aggressive' ? 'Aggressive · including major' : level === 'routine' ? 'Routine · patch and minor' : level === 'off' ? 'Turned off for this project' : 'Not recorded'
const date = (value?: string) => value && !value.startsWith('0001-') ? fmtWhen(value) : 'Not known'
const allOff: JanitorChoices = { dependencies: 'off', securityFixes: false, baseImages: false, toolchainPatches: false, packs: false }

function Choices({ data, current, reload }: { data: JanitorView; current: boolean; reload: () => Promise<void> }) {
  const canSave = useHolds('instance:manage-standing-approval')
  const saved = data.choices
  const [edit, setEdit] = useState({ choices: saved?.choices ?? allOff, revision: saved?.revision ?? 0 })
  const action = useAction(() => { void reload() })
  const choices = edit.choices
  const changed = JSON.stringify(choices) !== JSON.stringify(saved?.choices ?? allOff)
  const conflict = edit.revision !== (saved?.revision ?? 0)
  const set = (next: JanitorChoices) => setEdit({ ...edit, choices: next })
  const exceptions = data.deployments.filter(d => !d.automatic || data.projects.find(p => p.projectId === d.projectId)?.choice?.level === 'off')
  return <>
    <div className="form-grid">
      <FieldRow label="Dependencies"><select value={choices.dependencies} disabled={!canSave || !current || action.busy} onChange={e => set({ ...choices, dependencies: e.target.value, ...(e.target.value === 'off' ? { securityFixes: false, toolchainPatches: false } : {}) })}>
        <option value="off">Off</option><option value="routine">Patch and minor</option><option value="aggressive">Including major</option>
      </select></FieldRow>
      {([['securityFixes', 'Security fixes'], ['baseImages', 'Base images'], ['toolchainPatches', 'Toolchain patches for security fixes'], ['packs', 'Packs']] as const).map(([key, label]) => <FieldRow key={key} label={label}><input type="checkbox" checked={choices[key]} disabled={!canSave || !current || action.busy || key === 'securityFixes' && choices.dependencies === 'off' || key === 'toolchainPatches' && !choices.securityFixes} onChange={e => set({ ...choices, [key]: e.target.checked, ...(key === 'securityFixes' && !e.target.checked ? { toolchainPatches: false } : {}) })} /></FieldRow>)}
      <span>Projects</span><div>All projects, including new ones{data.projects.filter(p => p.choice).map(p => <div key={p.projectId}><Link to={projectTo(p)}>{p.name}</Link> · {levelName(p.level)} · {p.choice!.person.name}, {date(p.choice!.at)} · {p.choice!.reason}</div>)}</div>
      <span>Automatic releases</span><div>{!data.deployments.length ? 'None' : exceptions.length === data.deployments.length ? 'None' : exceptions.length ? `All deployments except ${exceptions.map(d => `${data.projects.find(p => p.projectId === d.projectId)?.name} / ${d.displayLabel}`).join(', ')}` : 'All deployments'}</div>
    </div>
    <div className="row row-3"><span>{saved ? `Set by ${saved.issuer.name}, ${date(saved.at)}` : 'Nobody has set these choices'}</span>{canSave && <button disabled={!current || action.busy || !changed || conflict} onClick={() => action.run(async () => { try { const result = await put<StandingRevision>('/janitor/choices', { choices, expectedRevision: edit.revision }); await reload(); setEdit({ choices: result.choices, revision: result.revision }) } catch (error) { await reload(); throw error } })}>Save</button>}</div>
    {conflict && <div role="alert">The choices changed. <button className="link" onClick={() => setEdit({ choices: saved?.choices ?? allOff, revision: saved?.revision ?? 0 })}>Read current choices</button></div>}
    {action.err && <div role="alert" className="error">{action.err}</div>}
  </>
}

export default function JanitorSettings() {
  const q = useGet<JanitorView>('/janitor', { poll: 10000 })
  return <div><PageHead page="janitorSettings" />
    <ReadStatus q={q} what="janitor settings" /><StaleNote q={q} />
    {q.data && <Section title="What to keep updated"><Choices data={q.data} current={q.current} reload={q.reload} /></Section>}
  </div>
}
