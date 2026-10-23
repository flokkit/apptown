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

export function NewReleasePage({ slug }: { slug: string }) {
  const nav = useNavigate()
  const planQ = useGet<ReleasePlan>(`/projects/${slug}/release/plan`)
  const plan = planQ.data
  const { run, busy, err } = useAction()

  const fire = async () => {
    const r = await run(() => post<{ number: number }>(`/projects/${slug}/release`, {}))
    if (r) nav(`/projects/${slug}/releases/${r.number}`)
  }

  // Without a plan there is nothing to offer: never a release button.
  if (!plan) return <ReadStatus q={planQ} what="the release plan" />
  const blocker =
    !plan.configured ? plan.configError ?? 'the intranet is not set up yet' :
    !plan.published ? "The intranet pulls every image from the project's container registry, and the latest landing was not published there. Attach a container registry to the project, then land a change." :
    !plan.head ? 'nothing has landed yet' : ''

  return (
    <div style={{ maxWidth: 760 }}>
      <StaleNote q={planQ} />
      <div className="card" style={{ padding: 20, marginTop: 14 }}>
        <h2 style={{ marginTop: 0 }}>🏢 Release to the intranet</h2>
        <p className="muted" style={{ marginTop: 4 }}>
          This takes what has already landed and shipped — nothing new is built or judged here —
          and puts it where everyone on the intranet can reach it.
        </p>
        {plan?.url && (
          <div style={{ margin: '14px 0' }}>
            <div className="muted small" style={{ marginBottom: 4 }}>It will live at</div>
            <a href={plan.url} target="_blank" rel="noreferrer"
              style={{ display: 'inline-block', fontSize: 'var(--fs-lg)', fontWeight: 700,
                       color: 'var(--accent)', textDecoration: 'underline',
                       textUnderlineOffset: 4, wordBreak: 'break-all' }}>
              {plan.url}
            </a>
          </div>
        )}
        {plan?.head && (
          <div className="muted small" style={{ margin: '6px 0' }}>
            Shipping main as of: “{plan.head.subject}” <code style={{ fontSize: 'var(--fs-xs)' }}>{plan.head.sha.slice(0, 10)}</code>
          </div>
        )}
        {plan && plan.changesSinceLast.length > 0 && (
          <div style={{ margin: '12px 0' }}>
            <div className="muted small" style={{ marginBottom: 4 }}>
              New since release #{plan.lastRelease?.number ?? '—'}:
            </div>
            {plan.changesSinceLast.map((c) => (
              <div key={c.id}>• <Link to={`/changes/${c.id}`}>{c.title}</Link></div>
            ))}
          </div>
        )}
        {plan && plan.lastRelease && plan.changesSinceLast.length === 0 && (
          <div className="muted small" style={{ margin: '10px 0' }}>
            Nothing new has shipped since release #{plan.lastRelease.number}; releasing again ships the same commit.
          </div>
        )}
        {blocker
          ? <div className="error" style={{ marginTop: 12 }}>{blocker}</div>
          : (
            <button className="primary btn-door" disabled={busy} onClick={fire} style={{ marginTop: 12 }}>
              {busy ? 'Releasing…' : '🏢 Release it'}
            </button>
          )}
        {err && <div className="error" style={{ marginTop: 10 }}>{err}</div>}
        <div style={{ marginTop: 14 }}>
          <Link className="muted small" to={`/projects/${slug}`}>← back to the project</Link>
        </div>
      </div>
    </div>
  )
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
    <div style={{ maxWidth: 760 }}>
      <StaleNote q={q} />
      <div className="card" style={{ padding: 20, marginTop: 14 }}>
        <div className="row">
          <h2 style={{ margin: 0 }}>Release #{r.number}</h2>
          {r.deployment === 'lan' && <span className="chip pass">🏢 intranet</span>}
          {r.state === 'releasing' && <span className="chip running">releasing</span>}
          {r.state === 'failed' && <span className="chip fail">failed</span>}
          {data.isLive && <span className="chip pass">current release</span>}
        </div>
        <div style={{ margin: '10px 0' }}>
          {r.state === 'releasing'
            ? <>Being released right now{r.approvedBy ? <> by <strong>{r.approvedBy}</strong></> : null}. The log is below.</>
            : r.state === 'failed'
              ? <>This release attempt{r.approvedBy ? <> by <strong>{r.approvedBy}</strong></> : null} failed. The log below shows where it stopped; the running app was not changed.</>
              : <>Released {new Date(when).toLocaleString()}{r.approvedBy ? <> by <strong>{r.approvedBy}</strong></> : null}.</>}
          {r.deployment && r.state !== 'releasing' && (r.state === 'failed' || data.isLive) && (
            <> <Link to={`/projects/${slug}/settings/deployments#diagnostics-${r.deployment}`}>Diagnostics of {r.deployment}</Link></>
          )}
          {r.state === 'released' && r.notes && r.notes.startsWith('http') && (
            <> Open it at <a href={r.notes} target="_blank" rel="noreferrer"><strong>{r.notes}</strong></a></>
          )}
        </div>
        {data.sbomArtifact && (
          <div className="muted small" style={{ margin: '6px 0' }}>
            <a href={`/api/v1/artifacts/${data.sbomArtifact}`} target="_blank" rel="noreferrer">Bill of materials</a>, recorded at landing.
          </div>
        )}
        {data.changes.length > 0 && (
          <div style={{ margin: '12px 0' }}>
            <div className="muted small" style={{ marginBottom: 4 }}>Changes in this release:</div>
            {data.changes.map((c) => (
              <div key={c.id}>• <Link to={`/changes/${c.id}`}>{c.title}</Link></div>
            ))}
          </div>
        )}
        {data.events.length > 0 && (
          <div style={{ margin: '14px 0' }}>
            <div className="muted small" style={{ marginBottom: 4 }}>Log:</div>
            <div style={{ background: 'var(--code-bg)', borderRadius: 8, padding: '10px 12px', fontFamily: 'var(--font-mono)', fontSize: 'var(--fs-xs)' }}>
              {data.events.map((e, i) => (
                <div key={i} style={{ margin: '2px 0', color: e.text.startsWith('FAILED') ? 'var(--bad)' : undefined }}>
                  <span className="muted">{new Date(e.at).toLocaleTimeString([], { hour12: false })}</span>  {e.text}
                </div>
              ))}
              {r.state === 'releasing' && <div className="muted" style={{ margin: '2px 0' }}>…</div>}
            </div>
          </div>
        )}
        {r.state === 'released' && r.externalRef && r.externalRef.includes('/') && (
          <div style={{ marginTop: 14 }}>
            <OpenThisVersion slug={slug} number={r.number} isLive={data.isLive} />
          </div>
        )}
        {data.bundle && <BundleFacts bundle={data.bundle} />}
        <div className="muted small" style={{ marginTop: 14 }} title="The exact commit and image this release ships.">
          {r.commitSha && <div>commit <code style={{ fontSize: 'var(--fs-xs)' }}>{r.commitSha.slice(0, 12)}</code></div>}
          {r.externalRef && <div>image <code style={{ fontSize: 'var(--fs-xs)', wordBreak: 'break-all' }}>{r.externalRef}</code></div>}
        </div>
        <div style={{ marginTop: 14 }}>
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
    <span className="row" style={{ gap: 8 }}>
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
export function BundleFacts({ bundle }: { bundle: ReleaseBundleView }) {
  const images = [
    ...Object.entries(bundle.parts).sort(([a], [b]) => a.localeCompare(b)).map(([n, ref]) => ({ n, ref, init: false })),
    ...Object.entries(bundle.init).sort(([a], [b]) => a.localeCompare(b)).map(([n, ref]) => ({ n, ref, init: true })),
  ]
  return (
    <div style={{ margin: '14px 0' }}>
      <div className="muted small" style={{ marginBottom: 4 }}>What it ships:</div>
      {bundle.byDigest
        ? <div className="small">
            <span className="chip pass">by digest</span>{' '}
            Images in <code style={{ wordBreak: 'break-all' }}>{bundle.registry}</code>, built for <code>{bundle.platform}</code>.{' '}
            {bundle.platform === APP_PLATFORM
              ? 'Any target that runs this platform can take this release, App Platform included.'
              : `Any target that runs this platform can take it; App Platform runs ${APP_PLATFORM}, so it cannot.`}
          </div>
        : <div className="small">
            <span className="chip">by image ID</span>{' '}
            The images exist only on the Docker daemon that built them, not in a registry. App Platform and Portainer take a
            release by digest. Attach a container registry to the project, then land a change to make one.
          </div>}
      <div style={{ display: 'grid', gridTemplateColumns: 'auto minmax(0, 1fr)', columnGap: 14, rowGap: 4, marginTop: 8 }}>
        {images.map((i) => (
          <Fragment key={(i.init ? 'init:' : '') + i.n}>
            <span className="muted small">{i.init ? `init ${i.n}` : i.n}</span>
            <code style={{ fontSize: 'var(--fs-xs)', overflowWrap: 'anywhere' }}>{i.ref}</code>
          </Fragment>
        ))}
      </div>
    </div>
  )
}
