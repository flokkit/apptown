import { PRODUCT } from '../../brand'
import { useEffect, useMemo, useRef, useState, type Dispatch } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { PackInspect } from '../Packs'
import type { DependencyList, PackInterviewPick, PackInterviewQuestions, PackView } from '../../wire.gen'
import { servicesFor, type Draft, type DraftAct } from './draft'

// Step 2 — Way of working: "pick for me" leads (a description, then perhaps
// a question or two); the card wall only appears if someone asks to choose
// themselves. Then the services the chosen way proposes, to tune.
export function WayOfWorkingStep({ d, act }: { d: Draft; act: Dispatch<DraftAct> }) {
  const [inspecting, setInspecting] = useState<PackView | null>(null)
  const { run, busy: pickBusy, err } = useAction()
  const currentInterview = useRef<number | null>(d.interviewRevision)
  useEffect(() => {
    currentInterview.current = d.interviewRevision
    return () => { currentInterview.current = null }
  }, [d.interviewRevision])

  // Only packs the instance has turned on are offered here (Ways of working → Packs).
  const packsQ = useGet<PackView[]>('/packs', { kind: 'config' })
  const packs = useMemo(() => (packsQ.data ?? []).filter((p) => p.enabled !== false), [packsQ.data])
  const catQ = useGet<DependencyList>('/dependencies', { kind: 'config' })
  const catalog = useMemo(() => (catQ.data?.dependencies ?? []).filter((k) => k.local?.image), [catQ.data])

  const { name, pack, packWhy, choosing, questions, answers } = d
  // The chosen pack's proposal follows the packs and catalog as they arrive,
  // until someone edits the services (the reducer ignores it after that).
  const proposed = useMemo(() => (pack ? servicesFor(pack, packs, catalog) : []), [pack, packs, catalog])
  useEffect(() => { act({ type: 'proposed', services: proposed }) }, [proposed, act])
  // A pack's proposal needs both the pack list and the service catalog, so
  // leaving the step waits until each has answered or failed (its error is
  // shown here); a read still on its way never counts as an empty proposal
  // (the interview can pick a pack before the pack list has answered).
  const readsKnown = (!!packsQ.data || !!packsQ.err) && (!!catQ.data || !!catQ.err)
  const pick = (p: string, why = '', icon?: string, revision?: number) => act({ type: 'pack', pack: p, why, icon, revision, services: servicesFor(p, packs, catalog) })
  // An interview that cannot decide answers why and decides nothing: the
  // reason is shown and the person chooses from the ways of working.
  const interview = (withAnswers: boolean) => run(async () => {
    const revision = d.interviewRevision
    const body: Record<string, unknown> = { description: (name ? name + ' — ' : '') + d.description }
    if (withAnswers) body.answers = questions.map((q) => ({ question: q, answer: answers[q] ?? 'not sure' }))
    let r: PackInterviewPick | PackInterviewQuestions
    try {
      r = await post<PackInterviewPick | PackInterviewQuestions>('/packs/interview', body)
    } catch (e) {
      if (currentInterview.current !== revision) return
      act({ type: 'choosing', on: true, revision })
      throw e
    }
    if ('pack' in r) pick(r.pack ?? '', r.why ?? '', r.icon ?? '', revision)
    else act({ type: 'asked', questions: r.questions ?? [], revision })
  })
  const selected = packs.find((p) => p.name === pack)
  const unbound = selected?.requires.filter((r) => !r.bound) ?? []

  return (
    <div className="card" style={{ padding: 'var(--space-5)' }}>
      <div className="mb-2"><strong>{choosing ? 'Choose a way of working' : 'Way of working'}</strong></div>
      {pack === null && !choosing && questions.length === 0 && (
        <>
          <div className="muted small mb-3">
            {PRODUCT} can use your description to suggest a way of working, or you can choose one yourself.
          </div>
          <textarea aria-label="Software description" className="prose" rows={3} value={d.description} onChange={(e) => act({ type: 'describe', description: e.target.value })} autoFocus
            placeholder={`e.g. "a game where you catch falling eggs and try to beat your high score"`}
            style={{ width: '100%', boxSizing: 'border-box', fontSize: 'var(--fs-base)' }} />
          <div className="row mt-3">
            <button className="primary" disabled={pickBusy || !d.description.trim()}
              style={{ fontSize: 'var(--fs-base)', padding: '10px 26px' }}
              onClick={() => interview(false)}>{pickBusy ? 'Thinking…' : 'Next →'}</button>
            <button className="subtle" onClick={() => act({ type: 'choosing', on: true })}>Choose a way of working myself →</button>
          </div>
        </>
      )}
      {pack === null && !choosing && questions.length > 0 && (
        <>
          <div className="muted small mb-3">
            Your answers decide which way of working fits {name || 'it'}.
          </div>
          {questions.map((q) => (
            <div key={q} className="mb-3">
              <div className="small mb-1">{q}</div>
              <div className="row" style={{ gap: 'var(--space-2)' }}>
                {['yes', 'no', 'not sure'].map((opt) => (
                  <button key={opt}
                    className={answers[q] === opt ? 'primary' : undefined}
                    style={{ padding: '6px 18px' }}
                    onClick={() => act({ type: 'answer', question: q, answer: opt })}>{opt}</button>
                ))}
              </div>
            </div>
          ))}
          <div className="row mt-3">
            <button className="primary" disabled={pickBusy || questions.some((q) => !answers[q])}
              style={{ fontSize: 'var(--fs-base)', padding: '10px 26px' }}
              onClick={() => interview(true)}>{pickBusy ? 'Thinking…' : 'Done →'}</button>
            <button className="subtle" onClick={() => act({ type: 'unask' })}>← change my description</button>
          </div>
        </>
      )}
      {pack !== null && !choosing && (
        <div className="mt-1">
          <div className="row">
            <span className="chip pass">{pack === '' ? 'bare project' : pack}</span>
            {selected && <span className="muted small">{selected.description}</span>}
          </div>
          {packWhy && <div className="small mt-2">{packWhy}</div>}
          <div className="muted small mt-2">
            <button className="subtle" onClick={() => act({ type: 'repick' })}>pick differently</button>
          </div>

          {selected?.staticStarter ? (
            <div className="muted small" style={{ borderTop: '1px solid var(--border)', margin: '18px 0 0', paddingTop: 'var(--space-4)' }}>
              This way of working starts from static files with no server, so there are no
              databases or other services to choose.
            </div>
          ) : (
          <div style={{ borderTop: '1px solid var(--border)', margin: '18px 0 0', paddingTop: 'var(--space-4)' }}>
            <div style={{ fontSize: 'var(--fs-base)', fontWeight: 650 }}>Services</div>
            <div className="muted small" style={{ marginTop: 2, marginBottom: 'var(--space-3)', maxWidth: 560 }}>
              The databases and backing services this app runs against. Picking one makes this a
              server; a client-only site or game needs none. You can change these any time later.
            </div>
            <div className="pick-grid">
              {catalog.map((k) => {
                const on = d.services.some((n) => n.capability === k.capability)
                return (
                  <div key={k.capability} role="button" tabIndex={0} aria-pressed={on}
                    className={'pick-tile' + (on ? ' on' : '')}
                    onClick={() => act({ type: 'service', kind: k })}
                    onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); act({ type: 'service', kind: k }) } }}>
                    <span className="pick-mark" aria-hidden>{on ? '✓' : '+'}</span>
                    <div className="pick-icon">{k.icon}</div>
                    <div className="pick-title">{k.title}</div>
                    <div className="muted small">{k.description}</div>
                    <div className="muted small" style={{ marginTop: 'var(--space-1)', opacity: 0.7, fontSize: 'var(--fs-xs)' }}>{k.defaults?.version ? `v${k.defaults.version}` : ' '}</div>
                  </div>
                )
              })}
            </div>
            <ReadStatus q={catQ} what="the service list" className="small" />
            {/* The pack's proposed services come from the pack list too. */}
            <ReadStatus q={packsQ} what="what this way of working proposes" className="small" />
            <StaleNote q={catQ} />
            {d.services.length === 0 && <div className="muted small mt-3">None selected — fine for a client-only site or a stateless service.</div>}
          </div>
          )}
        </div>
      )}
      {choosing && (
        <div className="mt-2">
          {inspecting && <PackInspect pack={inspecting} onClose={() => setInspecting(null)} />}
          <div className="muted small mb-3">
            A way of working is the set of rules, checks and steps a project follows: which
            language and tools it uses, how changes are checked, and how it is deployed.
            Choose the one closest to what {name || 'the project'} is; it can be changed later.
          </div>
          {/* The packs never read are not "no packs": say why instead. */}
          <ReadStatus q={packsQ} what="the ways of working" />
          <StaleNote q={packsQ} />
          <div className="pack-grid">
            {packs.map((p) => {
              // Only what differs between packs is shown: a capability
              // this AppTown has not set up, as a warning. Everything
              // that is set up is the normal case and says nothing.
              const missing = p.requires.filter((r) => !r.bound)
              return (
                <div key={p.name} className={'card pack-card' + (pack === p.name ? ' on' : '')} role="button" tabIndex={0}
                  onClick={() => pick(p.name)}
                  onKeyDown={(e) => { if (e.key === 'Enter') pick(p.name) }}>
                  <div className="row">
                    <strong>{p.name}</strong>
                    <span className="grow" />
                    <button className="subtle" onClick={(e) => { e.stopPropagation(); setInspecting(p) }}>inspect</button>
                  </div>
                  <div className="small" style={{ margin: '6px 0' }}>{p.description}</div>
                  {missing.length > 0 && (
                    <div className="row" style={{ gap: 'var(--space-1)' }}>
                      {missing.map((r) => (
                        <span key={r.capability} className="chip pending" title="no integration provides this yet">
                          needs {r.capability} — not set up
                        </span>
                      ))}
                    </div>
                  )}
                </div>
              )
            })}
            <div className={'card pack-card' + (pack === '' ? ' on' : '')} role="button" tabIndex={0}
              onClick={() => pick('')}
              onKeyDown={(e) => { if (e.key === 'Enter') pick('') }}>
              <strong>none (bare)</strong>
              <div className="muted small mt-2">No rules, checks or workflows: an empty project to set up yourself later.</div>
            </div>
          </div>
          <button className="subtle mt-2" onClick={() => act({ type: 'choosing', on: false })}>← describe it instead</button>
        </div>
      )}
      {unbound.length > 0 && (
        <div className="error">
          This way of working needs {unbound.map((r) => r.capability).join(', ')} but nothing provides it yet —
          attach it on the <Link to="/integrations">Integrations</Link> page (its workflows will fail until then).
        </div>
      )}
      {err && <div className="error">{err}</div>}
      <div className="row mt-4">
        <button className="subtle" onClick={() => act({ type: 'step', step: 1 })}>← back</button>
        <span className="grow" />
        {/* Only one "Next →" may exist at a time: the describe/questions
            phases have their own submit button, so the step-level Next
            appears only once a way of working is actually chosen. */}
        {pack !== null && (
          <button className="primary" disabled={!readsKnown} title={readsKnown ? undefined : 'Reading the ways of working and the service list first'} onClick={() => act({ type: 'step', step: 3 })}
            style={{ fontSize: 'var(--fs-base)', padding: '10px 26px' }}>Next →</button>
        )}
      </div>
    </div>
  )
}
