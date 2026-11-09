import { useState } from 'react'
import { ListTable } from '../../../ListTable'
import { useSearchParams } from 'react-router-dom'
import { put } from '../../../api'
import { useAction, useGet } from '../../../data'
import { useHolds } from '../../../instancePages'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { FieldRow } from '../../../FieldRow'
import { Section } from '../../../Section'
import { jobName } from '../../../parts/modelProfiles'
import type { ProjectModels } from '../../../wire.gen'

// The project owns assignment. A profile-page link preselects a choice;
// only Use profile writes it, through the existing authorized operation.
export function ModelsTab({ slug }: { slug: string }) {
  const q = useGet<ProjectModels>(`/projects/${slug}/model-profile`)
  const canChange = useHolds('project:configure-model-profile', slug)
  const [params, setParams] = useSearchParams()
  const [choice, setChoice] = useState<string | null>(null)
  const { run, busy, err } = useAction(undefined, { affects: ['/model-slots'] })
  const data = q.data
  if (!data) return <ReadStatus q={q} what="the model profiles" />
  const current = data.profile
  const selected = choice ?? params.get('profile') ?? current
  const known = data.profiles.includes(selected)
  const save = () => {
    if (!canChange || !q.current || busy || !known || selected === current) return
    void run(async () => {
      await put(`/projects/${slug}/model-profile`, { profile: selected })
      setChoice(null)
      setParams(p => { p.delete('profile'); return p }, { replace: true })
    })
  }
  return <>
    <StaleNote q={q} />
    <Section title="Model profile">
      <p>This project uses <strong>{current}</strong>.</p>
      <div className="form-grid">
        <FieldRow label="Model profile">
          <select aria-label="Model profile" value={selected} disabled={!canChange || !q.current || busy}
            onChange={e => setChoice(e.target.value)}>
            {!known && <option value={selected}>{selected} (unavailable)</option>}
            {data.profiles.map(p => <option key={p} value={p}>{p}</option>)}
          </select>
        </FieldRow>
      </div>
      {!canChange && <p className="muted small">You need permission to change this project’s model profile.</p>}
      {!known && <p className="error">This profile is unavailable. Choose another profile.</p>}
      <div className="row mt-3 mb-3"><button disabled={!canChange || !q.current || busy || !known || selected === current} onClick={save}>{busy ? 'Saving…' : selected === current && known ? 'In use' : 'Use profile'}</button></div>
      {err && <p className="error">{err}</p>}
    </Section>
    {known && <Section title={`Models for ${selected}`}>
      <ListTable label={`Models for ${selected}`} rows={data.tasks} rowKey={row => row.slot} columns={[
        { key: 'task', label: 'Task', phone: 'name', render: row => <><strong>{jobName(row.slot)}</strong><div className="muted small">{row.gloss}</div></> },
        { key: 'model', label: 'Model', render: row => {
          const { pattern: model, slot: from } = row.models[selected]
          return model ? `${model}${from !== row.slot ? ` · from ${jobName(from)}` : ''}` : 'No model set'
        } },
      ]} />
    </Section>}
  </>
}
