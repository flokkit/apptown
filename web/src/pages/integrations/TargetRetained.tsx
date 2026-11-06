import { useState } from 'react'
import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { MoreMenu } from '../../MoreMenu'
import { ListRow } from '../../ListRow'
import { Section } from '../../Section'
import type { RetainedList } from '../../wire.gen'

// Settlement records what the operator removed at the provider; it does not
// delete anything there. The target owns this door, including stale-read guards.
export function TargetRetained({ name }: { name: string }) {
  const q = useGet<RetainedList>('/retained', { poll: 30000 })
  const { run, busy, err } = useAction()
  const [selected, setSelected] = useState<string | null>(null)
  const [note, setNote] = useState('')
  if (!q.data) return <ReadStatus q={q} what="old resources on this target" />
  const rows = q.data.retained.filter(r => r.target === name && r.disposition === 'retained')
  if (!rows.length) return <StaleNote q={q} />
  return <div id="target-retained"><Section title="Old resources" count={rows.length}>
    <StaleNote q={q} />
    {rows.map(r => <div key={r.id}>
      <ListRow identity={<><strong>{r.need}</strong><div className="muted small">{r.project} · {String(r.handle?.name || r.need)}</div></>}
        state={<span className="muted small">Still recorded at the provider</span>}
        action={<MoreMenu label={String(r.handle?.name || r.need)} items={[{ label: 'Record removal…', disabled: !q.current || busy, onClick: () => { setSelected(r.id); setNote('') } }]} />} />
      {selected === r.id && <form className="stack stack-2" onSubmit={async event => {
        event.preventDefault()
        const saved = await run(() => post(`/projects/${r.project}/retained/${r.id}`, { disposition: 'released', note }))
        if (saved) { setSelected(''); await q.reload() }
      }}>
        <p>Confirm that you deleted this resource at the provider. This records its removal and frees any deployment name it holds; it deletes nothing at the provider.</p>
        <label>Note <input value={note} onChange={event => setNote(event.target.value)} /></label>
        <div className="row row-2"><button disabled={busy || !q.current} type="submit">Record removal</button><button type="button" disabled={busy} onClick={() => setSelected('')}>Cancel</button></div>
        {err && <p className="error">{err}</p>}
      </form>}
    </div>)}
  </Section></div>
}
