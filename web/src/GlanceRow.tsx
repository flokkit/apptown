import { Children, cloneElement, isValidElement, type ReactElement, type ReactNode, type HTMLAttributes } from 'react'

// Table facts and actions stay visible in the same reading order at both widths.
export function GlanceRow({ children, labels, detail, ...props }: HTMLAttributes<HTMLTableRowElement> & {
  labels: string[]
  detail?: ReactNode
}) {
  const cells = Children.toArray(children).filter(isValidElement) as ReactElement<{ children?: ReactNode; className?: string }>[]
  // All facts stay in the reading order at both widths. Tables reflow in CSS.
  return <>
    <tr {...props}>{cells.map((cell, i) => cloneElement(cell, { key: i, 'data-label': labels[i] } as object))}</tr>
    {detail && <tr className="row-expansion"><td colSpan={cells.length}>{detail}</td></tr>}
  </>
}

export function PhoneDetails({ children }: { children: ReactNode }) {
  if (Children.toArray(children).length === 0) return null
  return <div className="phone-details">{children}</div>
}
