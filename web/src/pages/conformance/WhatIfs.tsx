import { useHolds } from '../../instancePages'
import { Section } from '../../Section'
import { ListTable } from '../../ListTable'
import { Status } from '../../Status'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { relTime } from '../../format'
import type { FleetView, RecentWhatIfs, WhatIfStarted } from '../../wire.gen'
import { OUTCOME_ORDER, OUTCOME_WORDS } from '../WhatIfPage'
import { WhatIfPanel } from '../PackWhatIf'

// Pack upgrades is where packs meet projects: which projects are on an older
// version of a pack they adopted, trying a newer version on them first (a
// what-if), and the what-ifs run so far. A project moves to the newer version
// through an adoption change opened from a what-if's report or its own Way of
// working settings.
export function PackUpgrades() {
  return <>
    <ProjectsBehind />
    <StartWhatIf />
    <WhatIfsList />
  </>
}

// ProjectsBehind lists each project on an older version of a pack than the
// one recommended, and tries the upgrade on it in one press.
function ProjectsBehind() {
  const q = useGet<FleetView>('/wow')
  const { run, busy, err } = useAction(undefined, { affects: ['/conformance/what-ifs'] })
  // A what-if needs the authority of a fleet review; others see who is behind.
  const mayTry = useHolds('conformance:review')
  if (!q.data) return <ReadStatus q={q} what="which projects are on which pack versions" />
  const rows = (q.data.packs ?? []).flatMap(p => p.versions.filter(v => v.version !== p.recommended)
    .flatMap(v => v.projects.map(slug => ({ slug, pack: p.pack, on: v.version, recommended: p.recommended }))))
  const tryIt = (r: typeof rows[number]) => run(() => post<WhatIfStarted>('/conformance/what-if', { pack: r.pack, version: r.recommended, projects: [r.slug], rubrics: false, spendLimitMicros: 0, advanceWorkflows: false }))
  return <Section title="Projects behind" count={rows.length}>
    <StaleNote q={q} />
    {err && <p className="error small">{err}</p>}
    {rows.length === 0 ? <p className="muted small">Every project is on its packs' recommended versions.</p>
      : <ListTable label="Projects behind" rows={rows} rowKey={r => `${r.pack}/${r.slug}`} rowTo={r => `/projects/${r.slug}/settings/working`} columns={[
        { key: 'project', label: 'Project', phone: 'name', render: r => <Link to={`/projects/${r.slug}/settings/working`}>{r.slug}</Link> },
        { key: 'pack', label: 'Pack', render: r => <Link to={`/packs/${encodeURIComponent(r.pack)}`}>{r.pack}</Link> },
        { key: 'on', label: 'On', kind: 'number', render: r => `v${r.on}` },
        { key: 'recommended', label: 'Recommended', kind: 'number', render: r => `v${r.recommended}` },
        ...(!mayTry ? [] : [{ key: 'try', label: '', phone: 'action' as const, render: (r: typeof rows[number]) => <button disabled={busy} title={`Compare ${r.slug} on v${r.recommended} with how it stands now; nothing changes in the project`} onClick={() => void tryIt(r)}>Try the upgrade</button> }]),
      ]} />}
  </Section>
}

// StartWhatIf tries any pack version on chosen projects: the same panel a
// pack version's page carries, with the pack and version chosen here.
function StartWhatIf() {
  const q = useGet<FleetView>('/wow')
  const [pack, setPack] = useState('')
  const [version, setVersion] = useState(0)
  const packs = (q.data?.packs ?? []).filter(p => p.versions.length > 1)
  const chosen = packs.find(p => p.pack === pack)
  if (!q.data) return null
  return <Section title="Try a pack version">
    {packs.length === 0 ? <p className="muted small">No pack has more than one version yet, so there is nothing to try.</p> : <>
      <div className="row what-if-choose">
        <label>Pack <select value={pack} onChange={e => { setPack(e.target.value); setVersion(packs.find(p => p.pack === e.target.value)?.recommended ?? 0) }}>
          <option value="">Choose a pack</option>
          {packs.map(p => <option key={p.pack} value={p.pack}>{p.pack}</option>)}
        </select></label>
        {chosen && <label>Version <select value={version} onChange={e => setVersion(Number(e.target.value))}>
          {chosen.versions.map(v => <option key={v.version} value={v.version}>v{v.version}{v.version === chosen.recommended ? ' (recommended)' : ''}</option>)}
        </select></label>}
      </div>
      {chosen && version > 0 && <WhatIfPanel key={`${pack}@${version}`} pack={pack} version={version} />}
    </>}
  </Section>
}

// WhatIfsList is the recent what-ifs, newest first: which pack version, what
// it answered per project and how many projects it moves rules back on, how
// far it got, who asked and when. Each row opens its report, where adopting
// from it happens.
export function WhatIfsList() {
  const [limit, setLimit] = useState(10)
  const q = useGet<RecentWhatIfs>('/conformance/what-ifs', { poll: 5000 })
  if (!q.data) return <ReadStatus q={q} what="the what-ifs" />
  const list = q.data.whatIfs
  return (
    <div>
      <StaleNote q={q} />
      <Section title="What-ifs" count={list.length || undefined}>
      {list.length === 0 && <p className="muted small">None run yet.</p>}
      {list.length > 0 && (
        <ListTable label="What-ifs" rows={list.slice(0, limit)} rowKey={w => w.id ?? `${w.pack}@${w.version}@${w.createdAt}`} rowTo={w => `/conformance/what-ifs/${w.id}`} columns={[
          { key: 'pack', label: 'Pack version', phone: 'name', render: w => <><Link to={`/conformance/what-ifs/${w.id}`}>{w.pack} v{w.version}</Link>{w.advanceWorkflows && <span className="muted small"> · and other workflows to newest</span>}</> },
          { key: 'answers', label: 'Results', render: w => {
            const counts: Record<string, number> = {}
            for (const p of w.projects) counts[p.outcome] = (counts[p.outcome] ?? 0) + 1
            return <div className="row what-if-answers">
              <span className="muted">{w.projects.length} {w.projects.length === 1 ? 'project' : 'projects'}:</span>
              {OUTCOME_ORDER.filter((o) => counts[o]).map((o) => <span key={o}>{counts[o]} <Status word={w.outcomeLabels[o]} help={OUTCOME_WORDS[o]} /></span>)}
              {w.movesBackProjects > 0 && <span className="badge moves-back">{w.movesBackProjects} move rules back</span>}
            </div>
          } },
          { key: 'state', label: 'Status', phone: 'state', render: w => <Status word={w.statusLabel} help={w.detail || undefined} /> },
          { key: 'by', label: 'Asked by', render: w => <span className="muted">{w.requestedBy}</span> },
          { key: 'when', label: 'When', kind: 'time', render: w => relTime(w.createdAt) },
        ]} />
      )}
      </Section>
      {list.length > limit && <button onClick={() => setLimit(n => n + 10)}>Show more</button>}
    </div>
  )
}
