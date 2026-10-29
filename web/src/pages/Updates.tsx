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
        <dt>Channel</dt><dd>{data.channel}</dd>
        <dt>Last attempted check</dt><dd>{data.lastAttempt ?? 'Never'}</dd>
        <dt>Last successful check</dt><dd>{data.lastSuccess ?? 'Never'}</dd>
      </dl>
      {data.reason && <p>{data.reason}</p>}
      <p><Link to="/docs/guides/upgrading">Read the upgrade guide</Link></p>
      {data.candidate && <section>
        <h2>{data.candidate.version}</h2>
        <p>Verified source commit: <code>{data.candidate.commit}</code></p>
        <p>Archive: <code>{data.candidate.archive.name}</code></p>
        <p>SHA-256: <code>{data.candidate.archive.sha256}</code></p>
        {data.state === 'available' && <HowToUpdate version={data.candidate.version} />}
        <div className="markdown" dangerouslySetInnerHTML={{ __html: renderMd(data.candidate.notes) }} />
      </section>}
    </>}
  </div>
}

// AppTown does not know how it was installed, so it shows each way. The
// upgrade guide's earlier steps (nothing executing, a backup) come first.
function HowToUpdate({ version }: { version: string }) {
  const bare = version.replace(/^v/, '')
  return <section>
    <h3>How to update</h3>
    <p>First check nothing is executing and take a backup: steps 1 and 2 of the <Link to="/docs/guides/upgrading">upgrade guide</Link>.</p>
    <dl>
      <dt>Docker (one-server compose)</dt>
      <dd>Set <code>APPTOWN_VERSION={bare}</code> in <code>.env</code>, then <code>docker compose pull &amp;&amp; docker compose up -d</code></dd>
      <dt>Release archive</dt>
      <dd>Unpack the {version} archive over <code>/opt/apptown</code> and restart: step 3 of the upgrade guide</dd>
      <dt>Homebrew (CLI)</dt>
      <dd><code>brew upgrade apptown</code></dd>
    </dl>
  </section>
}
