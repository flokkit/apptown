import { janitorLevelName } from '../../janitorLevels'
import { Switch } from '../../Switch'
import { put } from '../../api'
import { useHolds } from '../../instancePages'
import { FieldRow } from '../../FieldRow'
import { Link } from 'react-router-dom'
import { useGet, useAction } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import { PageHead } from '../../PageHead'
import { fmtWhen } from '../../format'
import type { JanitorView, JanitorProject, JanitorChoices } from '../../wire.gen'

const projectTo = (p: JanitorProject) => `/projects/${p.slug}/settings/general#janitor`
const date = (value?: string) => value && !value.startsWith('0001-') ? fmtWhen(value) : 'Not known'

function Choices({ data, current, reload }: { data: JanitorView; current: boolean; reload: () => Promise<void> }) {
  const canSave = useHolds('instance:manage-standing-approval')
  const saved = data.choices
  const action = useAction()
  if (!saved) return null
  const choices = saved.choices
  const set = (choices: JanitorChoices) => void action.run(async () => {
    try { await put('/janitor/choices', { choices, expectedRevision: saved.revision }) }
    finally { await reload() }
  })
  const exceptions = data.deployments.filter(d => !d.automatic || data.projects.find(p => p.projectId === d.projectId)?.choice?.level === 'off')
  return <>
    <div className="form-grid">
      <FieldRow label="Dependencies"><select value={choices.dependencies} disabled={!canSave || !current || action.busy} onChange={e => set({ ...choices, dependencies: e.target.value, ...(e.target.value === 'off' ? { securityFixes: false, toolchainPatches: false } : {}) })}>
        {['off', 'routine', 'aggressive'].map(level => <option key={level} value={level}>{janitorLevelName(level)}</option>)}
      </select></FieldRow>
      {([['securityFixes', 'Security fixes'], ['baseImages', 'Base images'], ['toolchainPatches', 'Toolchain patches for security fixes'], ['packs', 'Packs']] as const).map(([key, label]) => <FieldRow key={key} label={label}><Switch label={label} checked={choices[key]} disabled={!canSave || !current || action.busy || key === 'securityFixes' && choices.dependencies === 'off' || key === 'toolchainPatches' && !choices.securityFixes} onChange={checked => set({ ...choices, [key]: checked, ...(key === 'securityFixes' && !checked ? { toolchainPatches: false } : {}) })} /></FieldRow>)}
      <span>Projects</span><div>All projects, including new ones · <Link to="/projects">change per project in its settings</Link>{data.projects.filter(p => p.choice).map(p => <div key={p.projectId}><Link to={projectTo(p)}>{p.name}</Link> · {janitorLevelName(p.choice!.level)} · {p.choice!.person.name}, {date(p.choice!.at)}</div>)}</div>
      <span>Automatic releases</span><div>{!data.deployments.length ? 'None' : exceptions.length === data.deployments.length ? 'None' : exceptions.length ? `All deployments except ${exceptions.map(d => `${data.projects.find(p => p.projectId === d.projectId)?.name} / ${d.displayLabel}`).join(', ')}` : 'All deployments'} · <Link to="/deployments">change per deployment in its settings</Link></div>
    </div>
    <p>{`Set by ${saved.issuer.name}, ${date(saved.at)}`}</p>
    {action.err && <div role="alert" className="error">{action.err}</div>}
  </>
}

export default function JanitorSettings() {
  const q = useGet<JanitorView>('/janitor', { poll: 10000 })
  return <div>
    <PageHead page="maintenance" sub="Settings" />
    <ReadStatus q={q} what="janitor settings" /><StaleNote q={q} />
    {q.data && <Section title="What to keep updated"><Choices data={q.data} current={q.current} reload={q.reload} /></Section>}
  </div>
}
