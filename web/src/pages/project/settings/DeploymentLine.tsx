import type { ReactNode } from 'react'

// The change Overview's label | value | trailer grid, shared by card facts.
export function DeploymentLine({ label, children, trailer }: { label: string; children: ReactNode; trailer?: ReactNode }) {
  return <div className="deploy-line"><div className="muted">{label}</div><div className="deploy-value">{children}</div><div className="deploy-trailer">{trailer}</div></div>
}
