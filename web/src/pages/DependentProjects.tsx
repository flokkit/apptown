import { useEffect } from 'react'
import { Status } from '../Status'
import { ListTable } from '../ListTable'
import { setPageContext } from '../chrome'
import { Link, useLocation, useParams, useSearchParams } from 'react-router-dom'
import { useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { usePageName } from '../chrome'
import { Section } from '../Section'
import type { ConformanceView, FleetView, GuidanceBlockDetail, IntegrationList, PolicyUsageList } from '../wire.gen'

export function ProjectCount({ to, count }: { to: string; count: number }) {
  return <Link to={to} onClick={e => e.stopPropagation()}>{count} project{count === 1 ? '' : 's'}</Link>
}

export function GuidanceProjects() {
  const { id = '' } = useParams()
  const q = useGet<GuidanceBlockDetail>(`/guidance/blocks/${encodeURIComponent(id)}`)
  usePageName(`/guidance/${id}`, q.data?.block.name)
  return <><h1>Projects</h1><ReadStatus q={q} what="the guidance's projects" /><StaleNote q={q} />
    {q.data && <Section title="Projects" count={q.data.usedBy.length}>
      {q.data.usedBy.map(u => <p key={u.projectSlug}><Link to={`/projects/${u.projectSlug}`}>{u.projectSlug}</Link> · v{u.version}{u.disabled ? ' · turned off' : ''}</p>)}
      {q.data.usedBy.length === 0 && <p>No project uses it directly.</p>}
    </Section>}</>
}

export function PolicyProjects() {
  const { name = '' } = useParams()
  const q = useGet<PolicyUsageList>(`/policy-library/${encodeURIComponent(name)}/usage`)
  return <><h1>Projects adopting {name}</h1><ReadStatus q={q} what="the policy's projects" /><StaleNote q={q} />
    {q.data && <Section title="Projects" count={q.data.usedBy.length}>
      {q.data.usedBy.map(u => <div key={u.slug} className="card"><Link to={`/projects/${u.slug}`}>{u.project}</Link> · {u.conformant ? 'following it' : 'not following it'}
        {u.violations.map((v, i) => <p key={i}>Workflow “{v.workflow}”: {v.detail}</p>)}
      </div>)}
      {q.data.usedBy.length === 0 && <p>No project adopted it.</p>}
    </Section>}</>
}

export function PackProjects() {
  const { name = '' } = useParams()
  const [params] = useSearchParams()
  const version = params.get('version')
  const q = useGet<FleetView>('/wow')
  usePageName(`/packs/${name}`, name)
  const versions = q.data?.packs.find(p => p.pack === name)?.versions.filter(v => !version || String(v.version) === version) ?? []
  const recommended = q.data?.packs.find(p => p.pack === name)?.recommended
  const rows = versions.flatMap(v => v.projects.map(slug => ({ slug, version: v.version })))
  return <><h1>Projects{version ? ` on v${version}` : ''}</h1><ReadStatus q={q} what="the pack's projects" /><StaleNote q={q} />
    {q.data && <Section title="Projects" count={new Set(rows.map(r => r.slug)).size}>
      {rows.length === 0 ? <p className="muted">No project uses it.</p>
        // A project moves to the recommended version from its own Way of working settings; the row opens them.
        : <ListTable label="Projects using this pack" rows={rows} rowKey={r => `${r.version}/${r.slug}`} rowTo={r => `/projects/${r.slug}/settings/working`} columns={[
          { key: 'project', label: 'Project', phone: 'name', render: r => <Link to={`/projects/${r.slug}/settings/working`}>{r.slug}</Link> },
          { key: 'version', label: 'Version', phone: 'detail', render: r => `v${r.version}` },
          { key: 'status', label: 'Status', phone: 'state', render: r => r.version === recommended ? <Status word="current" /> : <Status word="available" reason="update available" help={`v${recommended} is recommended; update it from the project's Way of working settings.`} /> },
        ]} />}
    </Section>}</>
}

export function IntegrationProjects() {
  const { name = '' } = useParams()
  const area = useLocation().pathname.split('/')[1]
  const q = useGet<IntegrationList>('/integrations')
  const binding = q.data?.integrations.find(b => b.name === name)
  usePageName(`/${area}/${name}`, binding?.displayLabel || name)
  const use = binding?.usedBy
  const label = binding?.displayLabel || name
  return <><h1>Projects</h1><ReadStatus q={q} what="the service's projects" /><StaleNote q={q} />
    {q.data && (!binding ? <p>No integration is called “{name}”.</p> : <Section title={`Using ${label}`}>
      {use?.projects.map(p => <p key={p.slug}><Link to={`/projects/${p.slug}`}>{p.name}</Link></p>)}
      {use?.deployments.map(d => <p key={`${d.project}/${d.name}`}><Link to={`/projects/${d.project}/deployments/${d.name}`}>{d.project}/{d.name}</Link> · deployment on <Link to={`/targets/${d.target}`}>{d.target}</Link></p>)}
      {!use?.projects.length && !use?.deployments.length && <p>No project or deployment names {label} directly.</p>}
    </Section>)}</>
}

export function CheckProjects() {
  const { reviewId, checkKey = '' } = useParams()
  const base = reviewId ? `/conformance/reviews/${reviewId}` : '/conformance'
  const q = useGet<ConformanceView>(reviewId ? `/conformance?review=${reviewId}` : '/conformance')
  const def = q.data?.checkDefs[checkKey]
  const checkName = def?.title || def?.check || checkKey
  const reviewNumber = q.data?.lens.review?.number
  useEffect(() => {
    setPageContext({ names: {
      [`${base}/checks/${encodeURIComponent(checkKey)}`]: checkName,
      ...(reviewId && reviewNumber ? { [`/conformance/reviews/${reviewId}`]: `Review #${reviewNumber}` } : {}),
    } })
    return () => setPageContext({})
  }, [base, checkKey, checkName, reviewId, reviewNumber])
  return <><h1>Projects</h1><ReadStatus q={q} what="the check's projects" /><StaleNote q={q} />
    {q.data && (!def ? <p>This check is no longer recorded.</p> : <Section title="Projects" count={def.adopters.length}>
      {def.adopters.map(slug => <p key={slug}><Link to={`${base}/projects/${slug}`}>{slug}</Link></p>)}
      {def.adopters.length === 0 && <p>No project adopted this check.</p>}
    </Section>)}
  </>
}
