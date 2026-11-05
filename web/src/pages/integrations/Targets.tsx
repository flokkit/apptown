import { useHostStates } from '../../hostState'
import { ListRow } from '../../ListRow'
import { Status } from '../../Status'
import { offerTarget, targetOffer } from '../../targetCommands'
import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { AUDIENCES, DATA_CLASSES, familyRank, placeOf } from '../../targets'
import type { TargetList } from '../../wire.gen'

// Deployment targets (design/subsystems/deployment.md §2.2, §15.7): the
// places this AppTown can put software, and who they are offered to. This is
// the setting-up list; what runs on each is under Deployments › Infrastructure.
// A target is usable by a project only once it is offered to that project's
// group, or to everyone, and the offer can cap what goes there.
export function TargetRows() {
  const q = useGet<TargetList>('/targets')
  const state = useHostStates()
  const nav = useNavigate()
  if (!q.data) return <ReadStatus q={q} what="the deployment targets" />
  const targets = [...q.data.targets].sort((a, b) => familyRank(a.kindInfo.family) - familyRank(b.kindInfo.family) || a.name.localeCompare(b.name))
  return <>
    <StaleNote q={q} />
    <div role="list">
      {targets.map((t) => {
        const to = `/integrations/targets/${t.name}`
        const st = state(t.name, t.statusLabel)
        const offered = t.offers.map((o) => o.group === '*' ? 'everyone' : o.group)
        return (
          <div key={t.id} role="listitem" className="list-row-open int-row" onClick={() => nav(to)}>
            <ListRow className="int-list-row"
              identity={<div style={{ minWidth: 0 }}>
                <Link to={to} className="int-name" onClick={(e) => e.stopPropagation()}>{t.displayLabel || t.name}</Link>
                <div className="muted small">{placeOf(t.kindInfo, t.address)} · {offered.length ? `offered to ${offered.join(', ')}` : 'not offered to anyone yet'}</div>
              </div>}
              state={<Status observation word={st.word} help={st.help} />} />
          </div>
        )
      })}
    </div>
  </>
}

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
    <div className="card" style={{ padding: 10, marginTop: 4 }}>
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
      <div className="row" style={{ marginTop: 8 }}>
        <button className="primary" disabled={busy} onClick={save}>Offer</button>
        <button className="subtle" disabled={busy} onClick={() => onDone(false)}>Cancel</button>
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}
