import type { ReactNode } from 'react'
import { useGet } from './data'
import type { AuthzHoldings } from './wire.gen'

// The pages about the whole forge rather than one project, and the instance
// read each one's API asks for (design/subsystems/authorization-capabilities.md).
// The forge refuses them to anyone without it whatever the menu shows; this
// only keeps the menu to pages that will open.
export const INSTANCE_PAGES = {
  people: { cap: 'institution:read-structure', title: 'People' },
  infrastructure: { cap: 'deployment:read', title: 'Infrastructure' },
  integrations: { cap: 'project:read', title: 'Integrations' },
  complaints: { cap: 'project:read', title: 'Complaints' },
  conformance: { cap: 'conformance:read', title: 'Conformance' },
  evals: { cap: 'standards:manage-packs', title: 'Evals' },
} as const
export type InstancePage = keyof typeof INSTANCE_PAGES

// useInstancePages answers "can this person open that page?": true, false,
// or undefined while the answer is on its way. Only the forge's own "no" is
// false: when the answer could not be read every page is offered, because not
// knowing is not a refusal.
export function useInstancePages(): (page: InstancePage) => boolean | undefined {
  const { data, err } = useGet<AuthzHoldings>('/authz/me', { kind: 'config' })
  return (page) => data ? data.instance?.[INSTANCE_PAGES[page].cap] !== false : err ? true : undefined
}

// useHolds answers "may this person do that?" from the forge's own answer
// (/authz/me): a capability on the whole forge, or in one project when one
// is named. It is false until the forge has said yes, so controls never show
// to someone the forge may refuse; the forge still decides at the act. Pages
// ask this rather than reading a role's name.
export function useHolds(cap: string, project?: string): boolean {
  const { data } = useGet<AuthzHoldings>(project ? `/authz/me?project=${encodeURIComponent(project)}` : '/authz/me', { kind: 'config' })
  if (!data) return false
  return (project ? data.project?.[cap] : data.instance?.[cap]) === true
}

// What someone sees on a page about the whole forge that they cannot read,
// reached by its address: a plain statement, not an error.
// heading=false is for a tab inside a page that already has its title.
export function NotAvailable({ page, heading = true }: { page: InstancePage; heading?: boolean }) {
  return (
    <div style={{ display: 'grid', gap: 10, maxWidth: 640, marginTop: heading ? 0 : 12 }}>
      {heading && <h1 style={{ margin: 0 }}>{INSTANCE_PAGES[page].title}</h1>}
      <div className="card" role="status">
        <b>Not available to you.</b>
        <div className="muted small" style={{ marginTop: 6 }}>
          This page is about the whole forge, not one project, and your account
          does not hold the role that reads it. Your own projects are under
          Projects. Ask an administrator if you need it.
        </div>
      </div>
    </div>
  )
}

// InstanceGate shows the page, or NotAvailable when the forge has said this
// person cannot read it. Until it has answered the page waits, so its reads
// are not sent only to be refused.
export function InstanceGate({ page, children }: { page: InstancePage; children: ReactNode }) {
  const may = useInstancePages()(page)
  if (may === undefined) return <div className="muted small">Reading what you may open…</div>
  return may ? <>{children}</> : <NotAvailable page={page} />
}
