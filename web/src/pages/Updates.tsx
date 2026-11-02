import { Section, Group } from '../Section'
import { Status } from '../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../api'
import { PRODUCT } from '../brand'
import { useGet } from '../data'
import { useHolds } from '../instancePages'
import { fmtStamp, relTime } from '../format'
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
  return <div className="card small" role="status">{PRODUCT} {data.candidate.version} is available. <Link to="/updates">View release</Link></div>
}

const states: Record<string, string> = {
  unknown: 'Not checked yet.',
  checking: 'Checking release signatures.',
  'no-release': 'No releases in this channel.',
  current: 'Up to date.',
  available: 'Release available.',
  stale: 'Release information expired. Check for updates.',
  unavailable: 'Release information could not be verified.',
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
  const development = data?.build.startsWith('dev-')
  // Everything in view, in sections: what this AppTown is, then the release
  // on offer with what is new, how to install it and how to verify it.
  // A check that ran after the last successful one did not succeed.
  const failedCheck = !!data?.lastAttempt && (!data.lastSuccess || Date.parse(data.lastAttempt) - Date.parse(data.lastSuccess) > 1000)
  return <div>
    <div className="page-head-row" style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
      <h1 style={{ flex: 1 }}>Updates</h1>
      {/* A development build is updated by deploying it; a release channel
          has nothing to say about it. */}
      {!development && <button className="primary" disabled={checking || data?.state === 'checking'} onClick={() => void check()}>{checking ? 'Checking…' : 'Check for updates'}</button>}
    </div>
    {(query.err || error) && <div className="error" role="alert">{error || query.err}</div>}
    {!data ? <p role="status">Release status is unavailable.</p> : <>
      <Section title={`This ${PRODUCT}`} action={<Link to="/docs/guides/upgrading">Upgrade guide →</Link>}>
        <dl className="facts">
          <dt>Status</dt>
          <dd>{development
            ? <>This {PRODUCT} runs a development build. Updates come by deployment.</>
            : <><Status word={data.statusLabel} /> <span className="muted">{data.reason || states[data.state]}</span></>}</dd>
          <dt>Version</dt><dd>{data.build}</dd>
          <dt>Platform</dt><dd>{data.platform}</dd>
          {/* A release channel and its checks mean nothing to a development build. */}
          {!development && <>
            <dt>Channel</dt><dd>{data.channel}</dd>
            <dt>Last checked</dt><dd>{data.lastSuccess ? <>{fmtStamp(data.lastSuccess)} <span className="muted">({relTime(data.lastSuccess)})</span></> : 'Never'}</dd>
            {failedCheck && <><dt>Last attempt</dt><dd>{data.lastAttempt ? fmtStamp(data.lastAttempt) : 'Never'} <span className="muted">did not succeed</span></dd></>}
          </>}
        </dl>
      </Section>
      {data.candidate && <Section title={data.state === 'available' ? `${data.candidate.version} is available` : `Latest release: ${data.candidate.version}`}>
        <Group label="What's new">
          <div className="markdown" dangerouslySetInnerHTML={{ __html: renderMd(data.candidate.notes) }} />
        </Group>
        {data.state === 'available' && <Group label="How to update"><HowToUpdate version={data.candidate.version} /></Group>}
        <Group label="Verify the release">
          <dl className="facts">
            <dt>Source commit</dt><dd><code>{data.candidate.commit}</code></dd>
            <dt>Archive</dt><dd><code>{data.candidate.archive.name}</code></dd>
            <dt>SHA-256</dt><dd><code>{data.candidate.archive.sha256}</code></dd>
          </dl>
        </Group>
      </Section>}
    </>}
  </div>
}

// AppTown does not know how it was installed, so it shows each way. The
// upgrade guide's earlier steps (nothing executing, a backup) come first.
function HowToUpdate({ version }: { version: string }) {
  const bare = version.replace(/^v/, '')
  return <section>
    <p>First check nothing is executing and take a backup: steps 1 and 2 of the <Link to="/docs/guides/upgrading">upgrade guide</Link>.</p>
    <dl className="facts">
      <dt>Docker (one-server compose)</dt>
      <dd>Set <code>APPTOWN_VERSION={bare}</code> in <code>.env</code>, then <code>docker compose pull &amp;&amp; docker compose up -d</code></dd>
      <dt>Release archive</dt>
      <dd>Unpack the {version} archive over <code>/opt/apptown</code> and restart: step 3 of the upgrade guide</dd>
      <dt>Homebrew (CLI)</dt>
      <dd><code>brew upgrade apptown</code></dd>
    </dl>
  </section>
}
