import { PRODUCT } from '../../brand'
import { Link } from 'react-router-dom'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { Section } from '../../Section'
import { CapacityStrip, targetCapacityReadings } from '../../CapacityStrip'
import { Status } from '../../Status'
import { fmtStamp } from '../../format'
import type { DiskReadings, ExecutingStepsView, ExecutorView, TargetCapacity, TierMap } from '../../wire.gen'

export function ExecutorObservation() {
  const q = useGet<ExecutorView>('/executor', { poll: 30000 })
  const disks = useGet<DiskReadings>('/infrastructure/disks', { poll: 60000 })
  const work = useGet<ExecutingStepsView>('/runs/executing', { poll: 8000 })
  const previews = useGet<TierMap>('/tiers', { poll: 10000 })
  // The host whose Docker runs the work, when AppTown reads it (no worker):
  // its CPU and memory are that target's capacity reading.
  const host = q.data?.hostTarget
  const capacity = useGet<TargetCapacity>(host ? `/targets/${encodeURIComponent(host)}/capacity` : null, { poll: 30000 })
  if (!q.data) return <ReadStatus q={q} what="the executor" />
  const e = q.data
  const preview = previews.data?.tiers.find(t => t.name === 'preview')
  const readings = disks.data?.disks.filter(d => e.configured ? !d.what.startsWith('this ') : d.what.startsWith('this ')) ?? []
  return <>
    <Section title="Where work runs">
      <StaleNote q={q} />
      {e.configured ? <dl className="facts"><dt>Worker address</dt><dd><code>{e.address}</code></dd>
        <dt>Connection</dt><dd><Status observation word={!q.refreshFailed && e.state === 'reachable' ? 'passed' : 'unknown'} reason={!q.refreshFailed && e.state === 'reachable' ? 'reachable' : undefined} /></dd>
        <dt>Last checked</dt><dd>{e.checkedAt ? fmtStamp(e.checkedAt) : 'unknown'}</dd>
        <dt>Version</dt><dd>{e.version || 'unknown'}{e.versionMismatch && <span className="small"> · differs from {PRODUCT} ({e.appTownVersion})</span>}</dd>
      </dl> : <p>Work runs on {PRODUCT}’s own server.</p>}
      {e.failure && <p>{e.failure}</p>}
      <p>Change and release previews, checks, test and agent steps, and release builds run here.</p>
      {host ? !capacity.data ? <ReadStatus q={capacity} what={`${host}'s CPU and memory`} /> : <>
        <div className="stack stack-3">{targetCapacityReadings(capacity.refreshFailed ? undefined : capacity.data).filter(r => r.label !== 'DISK').map(r => <CapacityStrip key={r.label} name={r.label} used={r.used} total={r.total} format={r.format} basis={r.basis} />)}</div>
        <p className="muted small">CPU and memory of the Docker host <Link to={`/targets/${encodeURIComponent(host)}`}>{host}</Link>.</p>
        <StaleNote q={capacity} />
      </> : e.configured && <p className="muted small">The worker's CPU and memory are not read.</p>}
      {!disks.data ? <ReadStatus q={disks} what="the executor's disks" /> : <>
        <div className="stack stack-3">{readings.map(d => ({ label: `DISK · ${d.what}`, used: !d.unknown && !d.stale && !d.error && !disks.refreshFailed && d.totalBytes !== undefined && d.freeBytes !== undefined ? d.totalBytes - d.freeBytes : undefined, total: d.totalBytes })).map(r => <CapacityStrip key={r.label} name={r.label} used={r.used} total={r.total} />)}</div>
        {!readings.length && <CapacityStrip name="DISK" />}
        <StaleNote q={disks} />
      </>}
    </Section>
    <Section title="Running now">
      {!work.data ? <ReadStatus q={work} what="running work" /> : <>
        <StaleNote q={work} />
        {!work.data.executing.length && <p className="muted small">No steps running at the last reading.</p>}
        {work.data.executing.map(s => <p key={s.stepId}><Link to={`/changes/${s.changeId}?tab=details&run=${s.runId}`}>{s.project}: {s.title || s.step}</Link> · {s.step}</p>)}
      </>}
      {!previews.data ? <ReadStatus q={previews} what="running previews" /> : <>
        <StaleNote q={previews} />
        {preview?.observed ? <p>Previews are unknown: {preview.reason}</p> : preview?.apps.length ? preview.apps.map(p => <p key={p.changeId}><Link to={`/changes/${p.changeId}`}>{p.name || p.slug} · preview for change #{p.changeNumber}</Link></p>) : <p className="muted small">No change previews running at the last reading.</p>}
      </>}
      {e.previewsUnknown ? <p>Release and workflow previews could not be read.</p> : (e.previews ?? []).map((p, i) => <p key={i}><Link to={p.release ? `/projects/${p.project}/releases/${p.release}` : `/changes/${p.changeId}?tab=details&run=${p.runId}`}>{p.project} · {p.release ? `preview for release #${p.release}` : 'workflow preview'}</Link></p>)}
    </Section>
  </>
}
