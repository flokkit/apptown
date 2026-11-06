import { offerTarget, targetOffer } from '../../targetCommands'
import { useState } from 'react'
import { useAction } from '../../data'
import { AUDIENCES, DATA_CLASSES } from '../../targets'

export function OfferForm({ target, groups, onDone }: { target: string; groups: string[]; onDone: (saved: boolean) => void }) {
  const [group, setGroup] = useState('*')
  const [maxStanding, setMaxStanding] = useState('')
  const [audiences, setAudiences] = useState<string[]>([])
  const [data, setData] = useState<string[]>([])
  const { run, busy, err } = useAction()
  const toggle = (list: string[], set: (v: string[]) => void, x: string) => set(list.includes(x) ? list.filter((y) => y !== x) : [...list, x])
  const save = async () => {
    const r = await run(() => offerTarget(target, targetOffer(group, maxStanding, audiences, data)))
    if (r) onDone(true)
  }
  return (
    <div className="card mt-1" style={{ padding: 10 }}>
      <div className="form-grid" style={{ maxWidth: 'none' }}>
        <label>To</label>
        <select aria-label="Offer to" value={group} onChange={(e) => setGroup(e.target.value)}>
          <option value="*">everyone</option>
          {groups.map((g) => <option key={g} value={g}>{g}</option>)}
        </select>
        <label title="leave empty for no limit">At most</label>
        <span className="row"><input aria-label="At most standing deployments per project" value={maxStanding} onChange={(e) => setMaxStanding(e.target.value)} placeholder="no limit" style={{ width: 90 }} inputMode="numeric" /> <span className="muted small">standing deployments per project</span></span>
        <label title="leave all unticked to allow any">Audiences</label>
        <span className="row">{AUDIENCES.map((x) => (
          <label key={x} className="small"><input type="checkbox" checked={audiences.includes(x)} onChange={() => toggle(audiences, setAudiences, x)} /> {x}</label>
        ))}</span>
        <label title="leave all unticked to allow any">Data</label>
        <span className="row">{DATA_CLASSES.map((x) => (
          <label key={x} className="small"><input type="checkbox" checked={data.includes(x)} onChange={() => toggle(data, setData, x)} /> {x}</label>
        ))}</span>
      </div>
      <div className="row mt-2" >
        <button className="primary" disabled={busy} onClick={save}>Offer</button>
        <button className="subtle" disabled={busy} onClick={() => onDone(false)}>Cancel</button>
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}
