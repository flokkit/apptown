import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { useFeature } from '../features'
import { NotAvailable, useInstancePages } from '../instancePages'

export function ConformanceRedirect() {
  const location = useLocation()
  return <Navigate replace to={location.pathname.replace(/^\/conformance/, '/maintenance/standards') + location.search + location.hash} />
}

// Maintenance's pages (Needs you, Standards, Updates, Statistics, Settings) are in the
// menu's Maintenance drawer and each renders its own head. This only decides
// whether the reader may open the area at all, and sends a reader who can
// open Standards but not the Janitor's pages there.
export default function Maintenance({ children }: { children: ReactNode }) {
  const may = useInstancePages()
  const feature = useFeature()
  const { pathname } = useLocation()
  const janitor = may('maintenance')
  const standards = may('conformance') && feature('conformance')
  if (janitor === undefined || may('conformance') === undefined) return <p>Checking access…</p>
  if (!janitor && !standards) return <NotAvailable page="maintenance" />
  if (pathname === '/maintenance' && !janitor && standards) return <Navigate replace to={feature('conformance-estate') ? '/maintenance/standards' : '/maintenance/standards/reviews'} />
  return <>{children}</>
}
