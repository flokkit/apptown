import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ChangeState, HttpError, del, post } from '../../../api'
import { changeStateLabel, chipFor } from '../../../states'
import { FieldRow } from '../../../FieldRow'
import { QuietConfirm } from '../../../QuietConfirm'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { relTime } from '../../../format'
import type { Attachable, AttachableList, AttachmentList, AttachmentView, DependencyKind, DependencyList, IntegrationWiring, ProjectServiceList, ServicesProposed } from '../../../wire.gen'

// The service selector: the databases and backing services this app runs
// against, declared in its contract as `needs:` (catalog capabilities the
// AppTown fulfills per deployment). Editing here opens a Change (they live in
// the versioned contract) that previews with the new service and lands via
// gates. The app's own parts, including any off-catalog container, are
// source and change through a change like the rest of the app.
export type NeedPick = { capability: string; params?: Record<string, string> }

export type DepKind = DependencyKind

// Which catalogue kinds an app may declare under needs: everything but the
// kinds an integration delivers when attached (error reporting), which live
// under "Connected to". A kind is runnable if AppTown can fulfil it: a local
// service, or a registration through an integration (llm through OpenRouter).
export const declarable = (k: DepKind) => !k.integration && (!!k.local?.image || !!k.register?.connector)

// Display names for the catalogue's vendor and connector keys; an unknown key
// shows as written.
const VENDOR: Record<string, string> = { digitalocean: 'DigitalOcean' }
const CONNECTOR: Record<string, string> = { openrouter: 'OpenRouter', glitchtip: 'GlitchTip' }

// fulfilledBy says what fulfils a need, read from its catalogue entry (the
// owner's declaration), never inferred: a service AppTown runs beside the app,
// each cloud's own kind, or a registration through an integration.
export function fulfilledBy(k: DepKind): string[] {
  const out: string[] = []
  if (k.local?.image) out.push('Docker host or Portainer: runs beside the app')
  for (const [vendor, c] of Object.entries(k.cloud ?? {})) {
    out.push(`${VENDOR[vendor] ?? vendor}: ${c.kind ? c.kind.replace(/-/g, ' ') : 'provided by the platform'}`)
  }
  if (k.register?.connector) out.push(`through the ${CONNECTOR[k.register.connector] ?? k.register.connector} integration (attach it under Connected to)`)
  return out
}

const sameNeeds = (a: NeedPick[], b: NeedPick[]) =>
  a.length === b.length && a.every((n) => { const m = b.find((x) => x.capability === n.capability); return !!m && m.params?.version === n.params?.version })

// onto applies the picks a person changed since `from` to `now`: added and
// removed needs, and changed versions. What someone else changed meanwhile
// and the person did not touch stays as they left it.
function onto(now: NeedPick[], from: NeedPick[], mine: NeedPick[]): NeedPick[] {
  const had = (list: NeedPick[], cap: string) => list.find((n) => n.capability === cap)
  let out = now.filter((n) => !(had(from, n.capability) && !had(mine, n.capability)))
  for (const n of mine) {
    const before = had(from, n.capability)
    const theirs = had(now, n.capability)
    if (!before || !theirs) {
      // Added by me, or removed by them while I kept it: my pick as I made it.
      if (!before || JSON.stringify(before.params ?? {}) !== JSON.stringify(n.params ?? {})) {
        out = [...out.filter((x) => x.capability !== n.capability), n]
      }
      continue
    }
    // Only the parameters I changed go on top; theirs stay as they left them
    // (a review found a version edit putting back a database name someone
    // else had changed, 28 September 2026).
    const params = { ...(theirs.params ?? {}) }
    let changed = false
    for (const [k, v] of Object.entries(n.params ?? {})) {
      if (before.params?.[k] !== v) { params[k] = v; changed = true }
    }
    if (changed) out = out.map((x) => (x.capability === n.capability ? { ...x, params } : x))
  }
  return out
}

export function ServicesTab({ slug }: { slug: string }) {
  const svcQ = useGet<ProjectServiceList>(`/projects/${slug}/services`)
  const catQ = useGet<DependencyList>(`/dependencies`)
  const catalog = (catQ.data?.dependencies ?? []).filter(declarable)
  // The form starts only from an answer this page read itself: a revisit is
  // handed the cached answer first, and saving that would propose removing
  // whatever changed since (review finding, 28 September). It remembers the
  // declaration it started from; until someone edits, it follows each newer
  // reading.
  const [base, setBase] = useState<{ slug: string; needs: NeedPick[]; declaration: string; contract: string } | null>(null)
  const [draft, setDraft] = useState<NeedPick[] | null>(null)
  const [conflict, setConflict] = useState(false)
  useEffect(() => { setDraft(null); setBase(null); setConflict(false) }, [slug])
  const svcData = svcQ.data, svcCurrent = svcQ.current
  useEffect(() => {
    if (!svcData || !svcCurrent || draft) return
    setBase({ slug, needs: svcData.needs || [], declaration: svcData.declaration, contract: svcData.contract })
  }, [svcData, svcCurrent, slug, draft])
  const { run, busy, err, failure, setErr } = useAction()
  const [createdChange, setCreatedChange] = useState<{ id: string; number: number } | null>(null)
  const needs = draft ?? base?.needs ?? []
  const changed = !!base && !!draft && !sameNeeds(draft, base.needs)

  const edit = (next: NeedPick[]) => { setCreatedChange(null); setDraft(next) }
  const pickedIdx = (cap: string) => needs.findIndex((n) => n.capability === cap)
  const toggleNeed = (k: DepKind) => {
    const i = pickedIdx(k.capability)
    if (i >= 0) edit(needs.filter((_, j) => j !== i))
    else edit([...needs, { capability: k.capability, params: k.defaults?.version ? { version: k.defaults.version } : {} }])
  }
  const setVersion = (cap: string, v: string) => {
    edit(needs.map((n) => (n.capability === cap ? { ...n, params: { ...n.params, version: v } } : n)))
  }

  const save = async () => {
    if (!base || !draft) return
    setConflict(false)
    const r = await run(() => post<ServicesProposed>(`/projects/${slug}/services`,
      { needs: draft, title: 'Update services', expectedDeclaration: base.declaration }))
    if (r) { setCreatedChange(r.changeId ? { id: r.changeId, number: r.changeNumber } : null); setDraft(null) }
  }
  // A refused save means main's declaration moved: read it again so the
  // person sees what changed before choosing.
  const reload = useRef(svcQ.reload)
  reload.current = svcQ.reload
  useEffect(() => {
    if (failure instanceof HttpError && failure.code === 'settings-moved') { setConflict(true); reload.current() }
  }, [failure])
  const now = svcCurrent && svcData ? svcData : null
  const newer = !!base && !!now && now.declaration !== base.declaration
  const keepMine = () => {
    if (!base || !draft || !now) return
    setDraft(onto(now.needs || [], base.needs, draft))
    setBase({ slug, needs: now.needs || [], declaration: now.declaration, contract: now.contract })
    setConflict(false); setErr('')
  }
  const discard = () => { setDraft(null); if (now) setBase({ slug, needs: now.needs || [], declaration: now.declaration, contract: now.contract }); setConflict(false); setErr('') }
  const said = (list: NeedPick[]) => list.length ? list.map((n) => n.capability + (n.params?.version ? ` ${n.params.version}` : '')).join(', ') : 'none'

  // Without the project's current services there is nothing safe to edit: an
  // empty form saved would remove them.
  // A refresh that fails once the form has started leaves it (and any draft)
  // in place under a stale note.
  if (!base || base.slug !== slug) return svcQ.err
    ? <div className="error">The project's services could not be read: {svcQ.reason}</div>
    : <div className="muted small">Reading the project's services…</div>
  const contract = base.contract

  return (
    <div>
      <StaleNote q={svcQ} />
      <ReadStatus q={catQ} what="the service catalogue" className="small" />
      <StaleNote q={catQ} />
      <p className="muted small" style={{ maxWidth: 580 }}>
        The services this app uses: each is a need declared in the app's contract, fulfilled by the
        deployment's target or through an integration. Saving opens a change you preview and land —
        the app boots with the new service before it ships. A container that is not in this list is
        part of the app: ask for it in a change.
        {contract === 'none' && <> <strong>No runtime contract yet</strong> — build the app first.</>}
      </p>

      <div className="pick-grid" style={{ marginTop: 8 }}>
        {catalog.map((k) => {
          const idx = pickedIdx(k.capability)
          const on = idx >= 0
          const version = on ? needs[idx].params?.version : k.defaults?.version
          return (
            <div key={k.capability} role="button" tabIndex={0} aria-pressed={on}
              className={'pick-tile' + (on ? ' on' : '')}
              onClick={() => toggleNeed(k)}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleNeed(k) } }}>
              <span className="pick-mark" aria-hidden>{on ? '✓' : '+'}</span>
              <div className="pick-icon">{k.icon}</div>
              <div className="pick-title">{k.title}</div>
              <div className="muted small">{k.description}</div>
              <ul className="muted small" aria-label="fulfilled by" style={{ margin: '6px 0 0', padding: 0, listStyle: 'none', fontSize: 'var(--fs-xs)' }}>
                {fulfilledBy(k).map((line) => <li key={line}>{line}</li>)}
              </ul>
              {on && version !== undefined ? (
                <div style={{ marginTop: 6 }} onClick={(e) => e.stopPropagation()}>
                  <span className="muted small">v </span>
                  <input value={version} style={{ width: 56, fontSize: 'var(--fs-xs)', textAlign: 'center' }}
                    onChange={(e) => setVersion(k.capability, e.target.value)} />
                </div>
              ) : (
                <div className="muted small" style={{ marginTop: 4, opacity: 0.7, fontSize: 'var(--fs-xs)' }}>
                  {k.defaults?.version ? `v${k.defaults.version}` : ' '}
                </div>
              )}
            </div>
          )
        })}
      </div>
      {needs.length === 0 &&
        <div className="muted small" style={{ marginTop: 10 }}>None declared — fine for a client-only site or a stateless service.</div>}

      {conflict && draft && (
        <div className="card tone-warn" style={{ marginTop: 10 }}>
          <div>The app's services changed on main after you started. Your picks are kept here, unsaved.</div>
          {newer && now && <div className="muted small" style={{ marginTop: 4 }}>
            Declared now: {said(now.needs || [])} (it was {said(base.needs)} when you started). Keeping your edits gives {said(onto(now.needs || [], base.needs, draft))}.
          </div>}
          <div className="row" style={{ marginTop: 8 }}>
            <button onClick={keepMine} disabled={!newer}>Keep my edits on top of theirs</button>
            {!newer && <span className="muted small">reading the current services…</span>}
            <button className="subtle" onClick={discard}>Discard my edits</button>
          </div>
        </div>
      )}

      <div className="row" style={{ marginTop: 16, gap: 10, alignItems: 'center' }}>
        <button className="primary" disabled={busy || contract === 'none' || !changed || conflict} onClick={save}>
          {busy ? 'opening a change…' : 'Save as a change'}
        </button>
        {createdChange && (
          <span className="small">✓ Change {createdChange.number} opened — <Link to={`/changes/${createdChange.id}`}>preview &amp; land it</Link></span>
        )}
        {err && !conflict && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}

// Project integrations: what this product
// plugs into. Linked = resource provisioned via a connector; direct =
// resource fields entered (secret NAMES, never values). Contributions (env
// into app containers, agent steering) follow the integration either way.
// The server sends the wiring change's state as its word; it is a ChangeState.
export type ProjIntegration = Omit<AttachmentView, 'wire'> & {
  wire?: { changeId: string | null; changeNumber: number; state: ChangeState; title: string } | null
}

export const INTEG_ICON: Record<string, string> = {
  errors: '🚨', feedback: '💬', telemetry: '📡', sso: '🔐', 'container-registry': '📦',
}


export type AttachableT = Attachable

export function ProjectIntegrationsTab({ slug }: { slug: string }) {
  const itemsQ = useGet<AttachmentList>(`/projects/${slug}/integrations`)
  const attachablesQ = useGet<AttachableList>('/attachables', { kind: 'config' })
  // The server sends each wiring change's state as its word (ProjIntegration).
  const items = (itemsQ.data?.attachments ?? []) as ProjIntegration[]
  const attachables = attachablesQ.data?.attachables ?? []
  const [adding, setAdding] = useState<AttachableT | null>(null)
  const [connector, setConnector] = useState('')
  const [fields, setFields] = useState<Record<string, string>>({})
  const [direct, setDirect] = useState(false)
  // What is attached changes what can still be attached.
  const { run, busy, err, setErr } = useAction(undefined, { affects: ['/attachables'] })
  // Attaching is half the act; the hand-off to wiring is the other half,
  // so attaching visibly starts the next step rather than stopping silently.
  const [justAttached, setJustAttached] = useState<string | null>(null)
  const [wiring, setWiring] = useState('')
  const nav = useNavigate()

  const openAdd = (a: AttachableT) => {
    setAdding(a); setConnector(a.connectors[0] ?? ''); setFields({}); setDirect(!a.provisionable); setErr('')
  }
  const attach = async () => {
    if (!adding) return
    const body: Record<string, unknown> = { capability: adding.capability, connector }
    if (direct || !adding.provisionable) body.resource = fields
    const done = await run(async () => { await post(`/projects/${slug}/integrations`, body); return true })
    if (done) { setAdding(null); setJustAttached(adding.capability) }
  }
  // Wire it in: the batch road (the adopt-integration workflow, unattended
  // to its approval) or the interactive one (a chat seeded with the brief).
  const wire = async (capability: string, mode: 'workflow' | 'chat') => {
    setWiring(capability)
    const r = await run(() => post<IntegrationWiring>(`/projects/${slug}/integrations/${capability}/wire`, { mode }))
    if (!r) { setWiring(''); return }
    if (mode === 'chat') nav(`/projects/${slug}/console?change=${r.change.id}&kickoff=go`)
    else nav(`/changes/${r.change.id}`)
  }
  const detach = (capability: string) => run(() => del(`/projects/${slug}/integrations/${capability}`))

  const unattached = attachables.filter((a) => !items.some((i) => i.capability === a.capability))

  // Never read is not "nothing attached".
  if (!itemsQ.data) return <ReadStatus q={itemsQ} what="the project's integrations" />
  return (
    <div style={{ maxWidth: 760 }}>
      <StaleNote q={[itemsQ, attachablesQ]} />
      <ReadStatus q={attachablesQ} what="the integrations that can be attached" className="small" />
      <div className="muted small" style={{ marginBottom: 12 }}>
        Integrations attached to this project directly (an integration that fulfils a need is
        shown with the need above). Each passes its settings to the app's containers and its
        guidance to agents; <em>linked</em> ones were provisioned through a connector,
        <em> direct</em> ones were entered by hand.
      </div>
      {/* Integrations are big-ticket: something to crow about, not a
          row of chips. One card each — the face, the state from the app's
          side, the proof, the guide, the doors. */}
      <div className="integ-grid">
      {items.map((i) => {
        const desc = attachables.find((a) => a.capability === i.capability)?.description
        const last = i.lastEvent ? relTime(i.lastEvent) : ''
        return (
        <div key={i.capability} className={'integ-card' + (justAttached === i.capability && i.wirable ? ' tone-accent' : '')}>
          <div className="row" style={{ alignItems: 'flex-start', gap: 12 }}>
            <span className="integ-icon" aria-hidden>{INTEG_ICON[i.capability] ?? '🔌'}</span>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div className="row" style={{ gap: 8 }}>
                <strong style={{ fontSize: 'var(--fs-md)' }}>{i.title}</strong>
                <span className={`chip ${i.provenance === 'provisioned' ? 'pass' : ''}`}
                  title={i.provenance === 'provisioned' ? `provisioned via connector ${i.connector}`
                    : i.connector ? `configured from connector ${i.connector}; nothing provisioned` : 'resource entered directly'}>
                  {i.provenance === 'provisioned' ? `linked · ${i.connector}` : i.connector ? `via ${i.connector}` : 'direct'}
                </span>
              </div>
              {desc && <div className="muted small" style={{ marginTop: 2 }}>{desc}</div>}
            </div>
          </div>

          {justAttached === i.capability && i.wirable ? (
            <div style={{ margin: '12px 0 4px' }}>
              <strong>Attached. Now the app has to use it.</strong>
              <div className="muted small" style={{ margin: '2px 0 8px' }}>
                The app gets the connection details in every container from now on, but nothing in the code
                reads them yet. Wiring it in is an ordinary change.
              </div>
              <div className="row">
                <button className="primary" disabled={wiring !== ''} onClick={() => wire(i.capability, 'workflow')}>
                  {wiring === i.capability ? '…' : 'Wire it in →'}
                </button>
                <button disabled={wiring !== ''} onClick={() => wire(i.capability, 'chat')}>Do it in a chat ▸</button>
                <button className="subtle" onClick={() => setJustAttached(null)}>later</button>
              </div>
            </div>
          ) : i.wirable && (
            <div className="row" style={{ marginTop: 12, gap: 8 }}>
              {i.wire ? (
                <Link to={`/changes/${i.wire.changeId}`} className={`chip ${chipFor(i.wire.state)}`} title={i.wire.title}>
                  {i.wire.state === 'shipped' ? '✓ wired in' : i.wire.state === 'abandoned' ? 'wiring abandoned' : `wiring · change ${i.wire.changeNumber} · ${changeStateLabel(i.wire.state)}`}
                </Link>
              ) : (
                <span className="chip pending">not wired in yet</span>
              )}
              {i.capability === 'errors' && (
                last
                  ? <span className="small" title={i.lastEvent}>last error seen {last}</span>
                  : <span className="muted small">no events seen yet{i.wire?.state === 'shipped' ? ' — try the test hook (in the guide)' : ''}</span>
              )}
              {(!i.wire || i.wire.state === 'abandoned') && (
                <>
                  <span style={{ flex: 1 }} />
                  <button className="primary" disabled={wiring !== ''} onClick={() => wire(i.capability, 'workflow')}>
                    {wiring === i.capability ? '…' : 'Wire it in →'}
                  </button>
                  <button className="subtle" disabled={wiring !== ''} onClick={() => wire(i.capability, 'chat')}>or in a chat ▸</button>
                </>
              )}
            </div>
          )}

          {Array.isArray(i.resource.unresolved) && i.resource.unresolved.length > 0 && (
            <div className="small" style={{ marginTop: 8 }}>
              {(i.resource.unresolved as { title: string; count: string; lastSeen: string }[]).slice(0, 3).map((u, n) => (
                <div key={n} className="muted small">⚠ {u.title} ×{u.count}</div>
              ))}
            </div>
          )}
          <div className="muted small" style={{ marginTop: 8 }}>
            {Object.entries(i.resource)
              .filter(([k]) => !['unresolved', 'issuesError'].includes(k))
              .map(([k, v]) => `${k}: ${String(v)}`).join(' · ')}
          </div>
          <div className="row integ-foot">
            {i.docs && <Link to={i.docs} className="small">Guide: how it is wired in and how to test it</Link>}
            <span style={{ flex: 1 }} />
            <QuietConfirm label="Detach" ask={`Detach ${i.capability}? The app and agents stop receiving its settings; nothing is deleted at the provider.`} onYes={() => detach(i.capability)} />
          </div>
        </div>
        )
      })}
      </div>
      {items.length === 0 && <div className="muted small" style={{ marginBottom: 8 }}>Nothing attached yet.</div>}

      {adding === null ? (
        <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
          {unattached.map((a) => (
            <button key={a.capability} onClick={() => openAdd(a)} title={a.description}>+ {a.title}</button>
          ))}
        </div>
      ) : (
        <div className="card">
          <strong>Attach {adding.title}</strong>
          <div className="muted small" style={{ margin: '4px 0 8px' }}>{adding.description}</div>
          {adding.provisionable && adding.resourceFields.length > 0 && (
            <label className="row" style={{ gap: 6, marginBottom: 6 }}>
              <input type="checkbox" checked={direct} onChange={(e) => setDirect(e.target.checked)} />
              <span className="small">direct — enter the resource myself instead of provisioning via a connector</span>
            </label>
          )}
          {(!direct || !adding.provisionable) && adding.connectors.length > 0 && (
            <label className="row" style={{ gap: 6 }}>
              <span className="muted small">via connector</span>
              <select value={connector} onChange={(e) => setConnector(e.target.value)}>
                {adding.connectors.map((c) => <option key={c} value={c}>{c}</option>)}
              </select>
            </label>
          )}
          {(direct || !adding.provisionable) && (
            <div className="form-grid" style={{ marginTop: 8 }}>
              {adding.resourceFields.map((f) => (
                <FieldRow key={f.key} label={f.label + (f.optional ? ' (optional)' : '')} help={f.help}>
                  <input value={fields[f.key] ?? ''} placeholder={f.placeholder ?? (f.type === 'secretRef' ? 'SECRET_NAME (never a value)' : '')}
                    onChange={(e) => setFields({ ...fields, [f.key]: e.target.value })} />
                </FieldRow>
              ))}
            </div>
          )}
          {err && <div className="error">{err}</div>}
          <div className="row" style={{ marginTop: 10 }}>
            <span style={{ flex: 1 }} />
            <button onClick={() => setAdding(null)}>Cancel</button>
            <button className="primary" onClick={attach} disabled={busy}>{busy ? 'Attaching…' : 'Attach'}</button>
          </div>
        </div>
      )}
      {adding === null && err && <div className="error">{err}</div>}
    </div>
  )
}
