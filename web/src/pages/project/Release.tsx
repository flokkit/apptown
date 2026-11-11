import { usePageName } from '../../chrome'

import './Release.css'
import { ReleaseAssent } from './ReleaseApproval'
import { Status } from '../../Status'
import { Fragment, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { openAfter, post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'

// The permalink records a release. Deployment actions live on its deployment.
// The server's shapes, generated from its Go types (wire.gen.ts).
export type { ReleasePlan } from '../../wire.gen'
import type { ReleaseBundleView, ReleaseDetail, ReleaseListed, ReleasePreviewStarted } from '../../wire.gen'
import { fmtStamp } from '../../format'
import { PRODUCT } from '../../brand'
import { useTechnicalDetail } from '../change/TechnicalDetail'

// Deployment's settlement lines begin "deployment attempt <id>, " (see
// settlement_release.go). The id is for administrators, so it moves to its
// own technical line under the words, shown only with the technical switch.
const ATTEMPT_LINE = /^deployment attempt (\S+), ([\s\S]*)$/
function LogText({ text }: { text: string }) {
  const [technical] = useTechnicalDetail()
  const m = ATTEMPT_LINE.exec(text)
  if (!m) return <>{text}</>
  return <>{m[2]}{technical && <span className="tech-line">deployment attempt {m[1]}</span>}</>
}

export function ReleasePermalinkPage({ slug, number, log = false }: { slug: string; number?: string; log?: boolean }) {
  // The log can gain health-controller notes after release. Keep reading
  // the current version, and reset the cadence when the permalink changes.
  const path = number ? `/projects/${slug}/releases/${number}` : null
  const [cadence, setCadence] = useState<{ path: string | null; poll: number }>({ path: null, poll: 2000 })
  usePageName(path || '', number ? `Release #${number}` : undefined)
  const q = useGet<ReleaseDetail>(path, { poll: cadence.path === path ? cadence.poll : 2000 })
  const { data } = q
  const state = data?.release?.state
  const live = data?.isLive
  useEffect(() => {
    if (state) setCadence({ path, poll: state === 'releasing' || state === 'waiting for approval' ? 2000 : live ? 8000 : 0 })
  }, [path, state, live])

  if (!data || !data.release) return <ReadStatus q={q} what="the release" />
  const r = data.release
  const when = r.releasedAt || r.createdAt
  if (log) return <><h1>Release log</h1><StaleNote q={q} /><div className="release-log">{data.events.map((e, i) => <p key={i}><span className="muted">{fmtStamp(e.at)}</span> <LogText text={e.text} /></p>)}</div></>
  return (
    <div className="release-page">
      <StaleNote q={q} />
      <div className="card release-summary">
        {/* The page heading names the release; this row is its deployment and state. */}
        <div className="row">
          {r.deployment && <span className="badge">{data.deploymentLabel || r.deployment}</span>}
          {data.replacedBy ? <Status word="replaced" help={`Release ${data.replacedBy} replaced it on this deployment.`} /> : <Status word={data.statusLabel} />}
          {data.isLive && <span className="badge">current release</span>}
          {r.origin === 'observed-after-restore' && <span className="badge" title={`found running after ${PRODUCT} was restored; ${PRODUCT} did not ship it`}>found after restore</span>}
        </div>
        <div className="release-facts">
          {r.state === 'waiting for approval' ? <>Waiting for approval from {r.approvalRoleTitle || r.approvalRole}. <Link to={`/projects/${slug}/deployments/${r.deployment}`}>Review on the deployment page →</Link></> : r.state === 'cancelled' || r.state === 'superseded' ? <>Release {r.state}.</> : r.state === 'releasing'
            ? <>Deploying{r.approvedBy ? <> by <strong>{r.approvedBy}</strong></> : null}.</>
            : r.state === 'failed'
              ? <>This release attempt{r.approvedBy ? <> by <strong>{r.approvedBy}</strong></> : null} failed. See the log for the failed step.</>
              : <>Released {fmtStamp(when)}{r.approvedBy ? <> by <strong>{r.approvedBy}</strong></> : null}.</>}
          {r.deployment && r.state !== 'releasing' && (r.state === 'failed' || data.isLive) && (
            <> <Link to={`/projects/${slug}/deployments/${r.deployment}`}>Check this deployment</Link></>
          )}
          {r.origin === 'observed-after-restore' && r.notes && <> {r.notes}. Deploying the release {PRODUCT} last shipped here puts that back.</>}
          {r.state === 'released' && r.notes && r.notes.startsWith('http') && (
            <> Open it at <a href={r.notes} target="_blank" rel="noreferrer"><strong>{r.notes}</strong></a></>
          )}
        </div>
        {r.rebuiltFrom ? <p>Rebuilt from commit <code>{r.rebuiltCommit}</code>, not the original image of release {r.rebuiltFrom}.</p> : null}
        {r.failure && <p role="status">{r.failure}</p>}
        {/* rollbackNumber is the earlier release this one deploys again (its build), not the release it replaces. */}
        {data.rollbackNumber && <p>Deploys <Link to={`/projects/${slug}/releases/${data.rollbackNumber}`}>release {data.rollbackNumber}</Link> again{r.restoreBackupId && ', with data restored from the selected backup'}.</p>}
        <ReleaseAssent approval={r.approval} />
        {!data.backup && data.backupReason && <p>{data.backupReason}</p>}
        {data.backup && <p><Link to={`/backups#backup-${data.backup.id}`}>{data.backup.status === 'ok' ? 'Backed up before this release →' : data.backup.status === 'pruned' ? 'Backup before this release was removed →' : 'Backup before this release was incomplete →'}</Link></p>}
        {r.bundleId && <ReleaseDeployments slug={slug} bundleId={r.bundleId} number={r.number} />}
        {data.changes.length > 0 && (
          <div className="release-record">
            <h3 className="release-heading">Changes</h3>
            {data.changes.map((c) => (
              <div key={c.id} className="release-volume"><Link to={`/changes/${c.id}`}>{c.title}</Link></div>
            ))}
          </div>
        )}
        {/* One list of facts: where it runs, what it is built from, what it
            contains. Empty facts are left out rather than folded away. */}
        <dl className="facts release-record">
          {data.request?.feed === 'main' && <><dt>Automatic release</dt><dd>Follows main, set by {data.request.setBy || 'unconfirmed'} · settings revision {data.request.settingsRevision}</dd></>}
          {data.request?.approvers && <><dt>Landing approved by</dt><dd>{data.request.approvers}</dd></>}
          {data.bundle && <BundleFacts bundle={data.bundle} />}
          {r.commitSha && <><dt>Commit</dt><dd><code>{r.commitSha.slice(0, 12)}</code></dd></>}
          {!data.bundle && r.externalRef && <><dt>Image</dt><dd><ImageReference value={r.externalRef} /></dd></>}
          {data.sbomArtifact && <><dt>Software list</dt><dd><a href={`/api/v1/artifacts/${data.sbomArtifact}`} target="_blank" rel="noreferrer">Components in this release (SBOM)</a></dd></>}
        </dl>
        {data.events.length > 0 && <p><Link to={`/projects/${slug}/releases/${r.number}/log`}>Release log</Link></p>}
        {r.state === 'released' && r.externalRef && r.externalRef.includes('/') && (
          <div className="release-next">
            <OpenThisVersion slug={slug} number={r.number} isLive={data.isLive} />
          </div>
        )}

      </div>
    </div>
  )
}

// Time travel: boot this release's exact image on the preview tier. A look
// at an old version — it runs bare (no data, no services), never a restore.
export function OpenThisVersion({ slug, number, isLive }: { slug: string; number: number; isLive: boolean }) {
  const { run, busy, err } = useAction()
  return (
    <span className="row release-preview-action">
      <button disabled={busy} onClick={() => run(() =>
        openAfter(async () => (await post<ReleasePreviewStarted>(`/projects/${slug}/releases/${number}/preview`, {})).url))}>
        {busy ? 'Starting preview…' : '▶ Preview this version'}</button>
      {!isLive && <span className="muted small">the released image with empty data; today's data is not included</span>}
      {err && <span className="error small">{err}</span>}
    </span>
  )
}

// The bundle a release ships, by its form: by digest, each image is in the
// project's registry and any target running its platform can take it (App
// Platform runs linux/amd64); by image ID, the images exist only on the
// Docker daemon that built them. Shown so a person can tell before
// promoting whether a release can go to App Platform or Portainer.
const APP_PLATFORM = 'linux/amd64'
// BundleFacts: rows of the release's facts list (inside a <dl className="facts">).
export function BundleFacts({ bundle }: { bundle: ReleaseBundleView }) {
  const images = [
    ...Object.entries(bundle.parts).sort(([a], [b]) => a.localeCompare(b)).map(([n, ref]) => ({ n, ref, init: false })),
    ...Object.entries(bundle.init).sort(([a], [b]) => a.localeCompare(b)).map(([n, ref]) => ({ n, ref, init: true })),
  ]
  return (
    <>
      {bundle.rebuiltFrom ? <><dt>Rebuilt image</dt><dd>Rebuilt from commit <code>{bundle.commit}</code>, not the original image of release {bundle.rebuiltFrom}.</dd></> : null}
      <dt>Runs on</dt>
      <dd>{bundle.byDigest
        ? bundle.platform === APP_PLATFORM
          ? <>Any <code>{bundle.platform}</code> host, App Platform included</>
          : <>Any <code>{bundle.platform}</code> host; not App Platform, which runs {APP_PLATFORM}, so it cannot take it</>
        : <>Only the server where it was built: its images are not in a registry. Set up the hub registry under Integrations, then land a change, to release it elsewhere.</>}</dd>
      {bundle.byDigest && bundle.registry && <><dt>Registry</dt><dd><code>{bundle.registry}</code></dd></>}
      {images.map((i) => (
        <Fragment key={(i.init ? 'init:' : '') + i.n}>
          <dt>{i.init ? `Image · init ${i.n}` : `Image · ${i.n}`}</dt>
          <dd><ImageReference value={i.ref} /></dd>
        </Fragment>
      ))}
    </>
  )
}

function ImageReference({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')
  return <span className="row release-image">
    <code className="release-image-name">{value}</code>
    <button className="subtle" onClick={async () => { try { await navigator.clipboard.writeText(value); setCopied(true); setError('') } catch { setError('Could not copy; select the reference instead.') } }}>{copied ? 'Copied' : 'Copy'}</button>
    {error && <span className="error small">{error}</span>}
  </span>
}

// These are links into the deployments that own deploy actions. Serving is a
// fresh runtime fact; past successful runs remain history during a failed read.
// One build can be released more than once (a rollback, or the same build to
// another deployment), so a deployment serving the build under another
// release number says which release it runs rather than "Running here".
function ReleaseDeployments({ slug, bundleId, number }: { slug: string; bundleId: string; number: number }) {
  const q = useGet<ReleaseListed[]>(`/projects/${slug}/releases`, { poll: 5000 })
  const ran = q.data?.filter(r => r.bundleId === bundleId && r.state === 'released') ?? []
  const names = [...new Set(ran.map(r => r.deployment))]
  return <div className="release-record">
    <h3 className="release-heading">Deployments</h3>
    <ReadStatus q={q} what="where this release has run" /><StaleNote q={q} />
    {names.map(name => {
      const serving = q.current ? ran.find(r => r.deployment === name && r.serving) : undefined
      return <p key={name}><Link to={`/projects/${slug}/deployments/${name}`}>{ran.find(r => r.deployment === name)?.deploymentLabel || name}</Link> · {!serving ? 'Has run here' : serving.number === number ? 'Running here' : <>Runs the same build as <Link to={`/projects/${slug}/releases/${serving.number}`}>release {serving.number}</Link></>}</p>
    })}
    {q.current && names.length === 0 && <p>No successful deployments are recorded for this bundle.</p>}
  </div>
}

export function ReleaseLogPage() {
  const { slug = '', number } = useParams()
  return <ReleasePermalinkPage slug={slug} number={number} log />
}
