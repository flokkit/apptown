import { forwardRef, type ReactNode } from 'react'

// The page's hierarchy, the same on every page: Page → Section → Group →
// Row. A section is a heading, its count as a quiet number, an optional
// one-line note, and at most one action on the heading's line; its content
// starts under a rule. A group is a light label inside a section, never
// competing with a section heading.
export function Section({ title, count, note, action, children, label }: {
  title: ReactNode; count?: number; note?: ReactNode; action?: ReactNode; children?: ReactNode; label?: string
}) {
  return (
    <section className="page-section" aria-label={label ?? (typeof title === 'string' ? title : undefined)}>
      {/* A section the page title already names has no heading of its own. */}
      {(title || action) && <div className="section-head">
        <h2>{title}{count !== undefined && <span className="section-count">{count}</span>}</h2>
        {action && <div className="section-action">{action}</div>}
      </div>}
      {note && <div className="section-note">{note}</div>}
      {/* A section with nothing to show yet has no empty panel under it. */}
      {children !== null && children !== undefined && children !== false && <div className="section-body">{children}</div>}
    </section>
  )
}

export function Group({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="section-group">
      <div className="group-label">{label}</div>
      {children}
    </div>
  )
}

// SectionFilter is a list's filter, on its section's heading line: it narrows
// the rows as the person types. Every filtered list uses this one box.
export const SectionFilter = forwardRef<HTMLInputElement, { value: string; onChange: (value: string) => void; placeholder: string; className?: string }>(
  function SectionFilter({ value, onChange, placeholder, className = '' }, ref) {
    return <input ref={ref} className={`section-filter ${className}`} aria-label={placeholder} placeholder={placeholder} value={value} onChange={e => onChange(e.target.value)} />
  })
