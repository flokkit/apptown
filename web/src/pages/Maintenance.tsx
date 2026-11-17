import type { ReactNode } from 'react'
import { Link, Navigate, useLocation } from 'react-router-dom'
import { useFeature } from '../features'
import { NotAvailable, useInstancePages } from '../instancePages'
import { PageHead } from '../PageHead'
import { Tabs } from '../Tabs'
import { useGet } from '../data'
import type { JanitorView } from '../wire.gen'
import { needs, summary } from './Janitor'

export function ConformanceRedirect() {
  const location = useLocation()
  return <Navigate replace to={location.pathname.replace(/^\/conformance/, '/maintenance/standards') + location.search + location.hash} />
}

function JanitorHead() {
  const { data } = useGet<JanitorView>('/janitor', { poll: 10000 })
  return <PageHead page="maintenance" line={data && summary(data, needs(data))} />
}

export default function Maintenance({ children }: { children: ReactNode }) {
  const may = useInstancePages()
  const feature = useFeature()
  const { pathname } = useLocation()
  const janitor = may('maintenance')
  const standards = may('conformance') && feature('conformance')
  if (janitor === undefined || may('conformance') === undefined) return <p>Checking access…</p>
  if (!janitor && !standards) return <NotAvailable page="maintenance" />
  if (pathname === '/maintenance' && !janitor && standards) return <Navigate replace to={feature('conformance-estate') ? '/maintenance/standards' : '/maintenance/standards/reviews'} />
  const inStandards = pathname.startsWith('/maintenance/standards')
  const tab = (to: string, label: string, active = pathname === to) => <Link to={to} className={active ? 'tab active' : 'tab'}>{label}</Link>
  return <div>
    {janitor && !inStandards ? <JanitorHead /> : <PageHead page="maintenance" />}
    <Tabs aria-label="Maintenance">
      {janitor && tab('/maintenance', 'Needs you')}
      {standards && tab('/maintenance/standards', 'Standards', inStandards)}
      {janitor && tab('/maintenance/updates', 'Updates')}
      {janitor && tab('/maintenance/settings', 'Settings')}
    </Tabs>
    {inStandards && <Tabs aria-label="Standards">
      {feature('conformance-estate') && tab('/maintenance/standards', 'Projects')}
      {feature('conformance-estate') && tab('/maintenance/standards/checks', 'Checks')}
      {tab('/maintenance/standards/reviews', 'Reviews')}
      {tab('/maintenance/standards/remediations', 'Remediations')}
      {feature('what-ifs') && tab('/maintenance/standards/what-ifs', 'Pack upgrades')}
    </Tabs>}
    {children}
  </div>
}
