import './Release.css'
import { Status } from '../../Status'
import { Fragment, useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { openAfter, post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'

// The release subpage: pressing Release opens a page, not a trigger —
// you see what would ship before anything ships, and the act lands on a
// PERMALINK. A release is an institutional event; the page is its record.
// The server's shapes, generated from its Go types (wire.gen.ts).
export type { ReleasePlan } from '../../wire.gen'
import type { ReleaseBundleView, ReleaseDetail, ReleasePlan, ReleasePreviewStarted } from '../../wire.gen'
import { fmtStamp } from '../../format'
import { PRODUCT } from '../../brand'
import { ReleaseDefaultPicker } from './settings/ReleaseDefault'
import type { DeploymentView } from '../../wire.gen'

export function NewReleasePage({ slug }: { slug: string }) {
  return <ReleasePage key={slug} slug={slug} />
}

function ReleasePage({ slug }: { slug: string }) {
  const nav = useNavigate()
  const planQ = useGet<ReleasePlan>(`/projects/${slug}/release/plan`)
  const plan = planQ.data
  const [choiceOpen, setChoiceOpen] = useState(false)
  const [choiceSaved, setChoiceSaved] = useState(false)
  const { run, busy, err } = useAction()

  const fire = async () => {
    const r = await run(() => post<{ number: number }>(`/projects/${slug}/release`, { defaultRevision: plan?.defaultRevision }))
    if (r) nav(`/projects/${slug}/releases/${r.number}`)
  }

  // Without a plan there is nothing to offer: never a release button.
  if (!plan) return <ReadStatus q={planQ} what="the release plan" />
  const blocker = !plan.head ? 'Nothing has landed yet.' : ''

  return (
    <div className="release-page">
      <StaleNote q={planQ} />
      {choiceSaved && <p role="status">Release preference saved. No release has started; review the plan before releasing.</p>}
      {choiceOpen && <ReleaseChoice slug={slug} onConfigured={() => { setChoiceSaved(true); setChoiceOpen(false) }} />}
      <div className="card release-summary">
        <h2 className="mt-0">{plan.configured ? `Release to ${plan.deploymentLabel || plan.deployment}` : 'Choose where to release'}</h2>
        <p className="muted mt-1" >
          Release checks run before deployment.
        </p>
        {plan?.url && (
          <div className="release-block">
            <div className="muted small mb-1" >It will live at</div>
            <a href={plan.url} target="_blank" rel="noreferrer"
               className="release-address">
              {plan.url}
            </a>
          </div>
        )}
        {plan?.head && (
          <details className="muted small release-disclosure"><summary>Build details</summary>
            Shipping main as of: “{plan.head.subject}” <code className="release-reference">{plan.head.sha.slice(0, 10)}</code>
          </details>
        )}
        {plan && plan.changesSinceLast.length > 0 && (
          <div className="release-section">
            <div className="muted small mb-1" >
              New since the last release:
            </div>
            {plan.changesSinceLast.slice(0, 3).map((c) => (
              <div key={c.id}>• <Link to={`/changes/${c.id}`}>{c.title}</Link></div>
            ))}
            {plan.changesSinceLast.length > 3 && <details><summary>More changes ({plan.changesSinceLast.length - 3})</summary>{plan.changesSinceLast.slice(3).map(c => <p key={c.id}><Link to={`/changes/${c.id}`}>{c.title}</Link></p>)}</details>}
          </div>
        )}
        {plan && plan.lastRelease && plan.changesSinceLast.length === 0 && (
          <div className="muted small release-facts">
            Nothing new has shipped since release #{plan.lastRelease.number}; releasing again ships the same commit.
          </div>
        )}
        {!plan.configured && !choiceOpen && <button onClick={() => setChoiceOpen(true)}>Choose release preference</button>}
        {!plan.configured ? <details><summary>Release setup</summary>{plan.configError && <p>{plan.configError}</p>}<Link to={`/projects/${slug}/settings/deployments`}>Deployment settings</Link></details> : blocker
          ? <div className="error mt-3" >{blocker}</div>
          : (
            <button className="primary btn-door mt-3" disabled={busy} onClick={fire} >
              {busy ? 'Releasing…' : '🏢 Release it'}
            </button>
          )}
        {err && <div className="error release-error">{err}</div>}
        <div className="release-next">
          <Link className="muted small" to={`/projects/${slug}`}>← back to the project</Link>
        </div>
      </div>
    </div>
  )
}

function ReleaseChoice({ slug, onConfigured }: { slug: string; onConfigured: () => void }) {
  const deployments = useGet<DeploymentView[]>(`/projects/${slug}/deployments`)
  if (!deployments.data) return <ReadStatus q={deployments} what="the project's deployments" />
  if (!deployments.data.some(d => d.kind === 'standing' && d.name !== 'main' && d.feed !== 'landing')) return <><StaleNote q={deployments} /><p>No environment is available for a manual release. <Link to={`/projects/${slug}/settings/deployments`}>Add a release environment in deployment settings</Link>.</p></>
  return <><StaleNote q={deployments} /><ReleaseDefaultPicker slug={slug} deployments={deployments.data} onConfigured={onConfigured} /></>
}

export function ReleasePermalinkPage({ slug, number }: { slug: string; number?: string }) {
  // The log can gain health-controller notes after release. Keep reading
  // the current version, and reset the cadence when the permalink changes.
  const path = number ? `/projects/${slug}/releases/${number}` : null
  const [cadence, setCadence] = useState<{ path: string | null; poll: number }>({ path: null, poll: 2000 })
  const q = useGet<ReleaseDetail>(path, { poll: cadence.path === path ? cadence.poll : 2000 })
  const { data } = q
  const state = data?.release?.state
  const live = data?.isLive
  useEffect(() => {
    if (state) setCadence({ path, poll: state === 'releasing' ? 2000 : live ? 8000 : 0 })
  }, [path, state, live])

  if (!data || !data.release) return <ReadStatus q={q} what="the release" />
  const r = data.release
  const when = r.releasedAt || r.createdAt
  return (
    <div className="release-page">
      <StaleNote q={q} />
      <div className="card release-summary">
        <div className="row">
          <h2 className="release-title">Release #{r.number}</h2>
          {r.deployment && <span className="badge">{r.deployment}</span>}
          <Status word={data.statusLabel} />
          {data.isLive && <span className="badge">current release</span>}
          {r.origin === 'observed-after-restore' && <span className="badge" title={`found running after ${PRODUCT} was restored; ${PRODUCT} did not ship it`}>found after restore</span>}
        </div>
        <div className="release-facts">
          {r.state === 'releasing'
            ? <>Deploying{r.approvedBy ? <> by <strong>{r.approvedBy}</strong></> : null}.</>
            : r.state === 'failed'
              ? <>This release attempt{r.approvedBy ? <> by <strong>{r.approvedBy}</strong></> : null} failed. See the log for the failed step.</>
              : <>Released {fmtStamp(when)}{r.approvedBy ? <> by <strong>{r.approvedBy}</strong></> : null}.</>}
          {r.deployment && r.state !== 'releasing' && (r.state === 'failed' || data.isLive) && (
            <> <Link to={`/projects/${slug}/settings/deployments#diagnostics-${r.deployment}`}>Check this deployment</Link></>
          )}
          {r.origin === 'observed-after-restore' && r.notes && <> {r.notes}. Deploying the release {PRODUCT} last shipped here puts that back.</>}
          {r.state === 'released' && r.notes && r.notes.startsWith('http') && (
            <> Open it at <a href={r.notes} target="_blank" rel="noreferrer"><strong>{r.notes}</strong></a></>
          )}
        </div>
        {!data.backup && data.backupReason && <p>{data.backupReason}</p>}
        {data.backup && <p><Link to={`/deployments/backups#backup-${data.backup.id}`}>{data.backup.status === 'ok' ? 'Backed up before this release →' : data.backup.status === 'pruned' ? 'Backup before this release was removed →' : 'Backup before this release was incomplete →'}</Link></p>}
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
          {data.bundle && <BundleFacts bundle={data.bundle} />}
          {r.commitSha && <><dt>Commit</dt><dd><code>{r.commitSha.slice(0, 12)}</code></dd></>}
          {!data.bundle && r.externalRef && <><dt>Image</dt><dd><ImageReference value={r.externalRef} /></dd></>}
          {data.sbomArtifact && <><dt>Software list</dt><dd><a href={`/api/v1/artifacts/${data.sbomArtifact}`} target="_blank" rel="noreferrer">Components in this release (SBOM)</a></dd></>}
        </dl>
        {data.events.length > 0 && (
          <details className="release-block"><summary>Release log</summary><div>
            <div className="release-log">
              {data.events.map((e, i) => (
                <div key={i} style={{ color: e.text.startsWith('FAILED') ? 'var(--bad)' : undefined }} className="release-event">
                  <span className="muted">{fmtStamp(e.at)}</span>  {e.text}
                </div>
              ))}
              {r.state === 'releasing' && <div className="muted release-event">…</div>}
            </div>
          </div></details>
        )}
        {r.state === 'released' && r.externalRef && r.externalRef.includes('/') && (
          <div className="release-next">
            <OpenThisVersion slug={slug} number={r.number} isLive={data.isLive} />
          </div>
        )}
        <div className="release-next">
          <Link className="muted small" to={`/projects/${slug}`}>← back to the project</Link>
        </div>
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
        {busy ? 'starting…' : isLive ? '▶ open this version' : '▶ open this old version'}</button>
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
      <dt>Runs on</dt>
      <dd>{bundle.byDigest
        ? bundle.platform === APP_PLATFORM
          ? <>Any <code>{bundle.platform}</code> host, App Platform included</>
          : <>Any <code>{bundle.platform}</code> host; not App Platform, which runs {APP_PLATFORM}, so it cannot take it</>
        : <>Only the server where it was built: its images are not in a registry. Attach a container registry to the project, then land a change, to release it elsewhere.</>}</dd>
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
