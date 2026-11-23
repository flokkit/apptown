import { post } from '../../api'
import { useAction } from '../../data'
import { QuietConfirm } from '../../QuietConfirm'
import { deploymentName } from '../../deploymentName'
import type { DeploymentView } from '../../wire.gen'

type Action = 'start' | 'stop' | 'restart'
const labels: Record<Action, string> = { start: '▶ Start', stop: '■ Stop', restart: '↻ Restart' }

export function DeploymentRuntimeActions({ slug, env, current, reload, onPending }: { slug: string; env: DeploymentView; current: boolean; reload: () => Promise<void>; onPending: (state: string | undefined) => void }) {
  const { run, busy, err } = useAction(undefined, { affects: [`/projects/${slug}/deployments`] })
  // Stopped: a person stopped it, or a current reading says nothing runs
  // (it exited by itself). The server admits Start on the same evidence.
  const stopped = env.runtimeState === 'stopped' || env.running === false
  const retry = env.runtimeError ? ({ starting: 'start', stopping: 'stop', restarting: 'restart' } as Record<string, Action>)[env.runtimeState] : undefined
  const changing = ['starting', 'stopping', 'restarting'].includes(env.runtimeState) && !retry
  const controls = env.runtimeControls
  // Only the acts the target can ever take are buttons (App Platform:
  // restart; a bucket website: none); refusals of those say why now. A
  // stopped deployment offers the first of Start, Restart its target takes.
  const can = (action: Action) => env.runtimeActions.includes(action)
  const actions: Action[] = retry ? [retry].filter(can) : stopped ? (['start', 'restart'] as Action[]).filter(can).slice(0, 1) : (['stop', 'restart'] as Action[]).filter(can)
  const act = async (action: Action) => {
    onPending({ start: 'starting', stop: 'stopping', restart: 'restarting' }[action])
    try {
      await run(async () => {
        try { await post(`/projects/${slug}/deployments/${env.name}/${action}`, {}) }
        finally { await reload() }
      })
    } finally { onPending(undefined) }
  }
  const reasons = [...new Set(actions.map(action => controls?.[action]).filter(Boolean))]
  if (actions.length === 0 && !err && !env.runtimeError) return null
  return <div className="deployment-runtime-actions stack stack-2">
    <div className="row row-2" aria-label="Deployment controls">{actions.map(action => {
      const reason = controls?.[action]
      const disabled = !current || busy || changing || !controls || !!reason || !env.generation
      return action === 'stop'
        ? <QuietConfirm key={action} className="primary" label={labels.stop} disabled={disabled}
          ask={`Stop ${deploymentName(env)}? People can't use it until you start it again.`} yes="Stop" onYes={() => act('stop')} />
        : <button key={action} className="primary" disabled={disabled} title={reason} onClick={() => act(action)}>{labels[action]}</button>
    })}</div>
    {reasons.map(reason => <span className="small muted" key={reason}>{reason}</span>)}
    {err && <span className="error" role="alert">{err}</span>}
    {env.runtimeError && <span className="error" role="alert">{env.runtimeError} Press Refresh status to check what happened.</span>}
  </div>
}
