import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../api'
import { PRODUCT } from '../brand'
import { useGet } from '../data'
import { useHolds } from '../instancePages'
import { renderMd } from '../md'
import type { InstanceUpdateStatus } from '../wire.gen'

// Affirmative holdings gate both the page and its background status read.
// These reads never trigger remote discovery or mutate configuration.
export function UpdatesPage() {
  const may = useHolds('instance:updates')
  if (!may) return <div role="status">Update information requires instance administration permission.</div>
  return <UpdateDetails />
}

export function ReleaseAvailabilityNote() {
  const may = useHolds('instance:updates')
  return may ? <AvailabilityRead /> : null
}
function AvailabilityRead() {
  const { data, err, refreshFailed, paused } = useGet<InstanceUpdateStatus>('/instance-updates', { kind: 'config', poll: 30_000 })
  if (err || refreshFailed || paused || data?.state !== 'available' || !data.candidate) return null
  return <div className="card small" role="status">{PRODUCT} {data.candidate.version} is available. <Link to="/updates">Review the authenticated release</Link></div>
}

const supervisorStates: Record<string, string> = {
  'not-enrolled': 'Not enrolled', unavailable: 'Supervisor unavailable', enrolled: 'Registered supervisor',
}

const states: Record<string, string> = {
  unknown: 'Release availability has not been assessed.',
  checking: 'Checking authenticated release information…',
  'no-release': 'No releases were found in this channel during the last successful check.',
  current: 'No newer release was found during the last successful check.',
  available: 'An authenticated release is available.',
  stale: 'Release information is stale. Check again before relying on it.',
  unavailable: 'Release availability could not be authenticated.',
}
function UpdateDetails() {
  const query = useGet<InstanceUpdateStatus>('/instance-updates', { kind: 'config', poll: 5_000 })
  const [checking, setChecking] = useState(false)
  const [error, setError] = useState('')
  async function check() {
    setChecking(true); setError('')
    try { await post<InstanceUpdateStatus>('/instance-updates/check', {}) }
    catch (e) { setError(e instanceof Error ? e.message : 'The release check failed.') }
    finally { await query.reload(); setChecking(false) }
  }
  const data = query.data
  return <div>
    <h1>Updates</h1>
    <p>Review signed {PRODUCT} release information before following the upgrade guide.</p>
    {(query.err || error) && <div className="error" role="alert">{error || query.err}</div>}
    <button disabled={checking || data?.state === 'checking'} onClick={() => void check()}>{checking ? 'Checking…' : 'Check for updates'}</button>
    {!data ? <p role="status">Release status is unavailable.</p> : <>
      <p role="status">{states[data.state] ?? states.unknown}</p>
      <dl>
        <dt>Installed build</dt><dd>{data.build} ({data.platform})</dd>
        <dt>Managed supervisor</dt><dd>{supervisorStates[data.supervisorState] ?? 'Supervisor unavailable'}</dd>
        <dt>Channel</dt><dd>{data.channel}</dd>
        <dt>Last attempted check</dt><dd>{data.lastAttempt ?? 'Never'}</dd>
        <dt>Last successful check</dt><dd>{data.lastSuccess ?? 'Never'}</dd>
      </dl>
      {data.reason && <p>{data.reason}</p>}
      <p>{data.supervisorReason}</p>
      {data.supervisorPhase && <p>Update phase: {data.supervisorPhase}</p>}
      <p>{data.replacementReason}</p>
      <p><Link to="/docs/guides/upgrading">Read the upgrade guide</Link></p>
      {data.candidate && <section>
        <h2>{data.candidate.version}</h2>
        <p>Verified source commit: <code>{data.candidate.commit}</code></p>
        <p>Archive: <code>{data.candidate.archive.name}</code></p>
        <p>SHA-256: <code>{data.candidate.archive.sha256}</code></p>
        <div className="markdown" dangerouslySetInnerHTML={{ __html: renderMd(data.candidate.notes) }} />
      </section>}
    </>}
  </div>
}
