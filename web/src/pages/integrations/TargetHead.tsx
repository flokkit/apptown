import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { BetaChip, targetFeatureArea } from '../../features'
import { useHostStates } from '../../hostState'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Status } from '../../Status'
import type { TargetPage } from '../../wire.gen'

// TargetHead is the head every tab of a target carries: its name, its one
// state and Check target. The tab strip says which tab is open, so the head
// stays the same as a person moves between tabs. It owns its read's notices:
// a first read that failed, or a reread that failed under a shown state.
export function TargetHead({ name }: { name: string }) {
  const q = useGet<TargetPage>(`/targets/${name}`)
  const hostState = useHostStates()
  const { run, busy, err } = useAction()
  const t = q.data?.target
  if (!t) return <><h1 className="target-page-h1">{name}</h1><ReadStatus q={q} what="the deployment target" /></>
  const state = hostState(t.name, t.statusLabel)
  return <>
    <StaleNote q={q} />
    <div className="row target-page-div">
      <h1 className="grow target-page-h1">{t.displayLabel || t.name}</h1>
      {targetFeatureArea(t.kind) !== 'deployments' && <BetaChip />}
      <Status observation word={state.word} help={state.help.startsWith('Host connection') ? undefined : state.help} />
      {t.status !== 'retired' && <button className="primary" disabled={busy} onClick={() => void run(async () => { await post(`/targets/${t.name}/check`, {}); await q.reload() })}>Check target</button>}
    </div>
    {err && <div className="error small">{err}</div>}
  </>
}
