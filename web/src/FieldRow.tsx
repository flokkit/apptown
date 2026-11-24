import { cloneElement, useId, type ReactElement, type ReactNode } from 'react'

// Keep the label association even when callers do not supply an id.
export function FieldRow({ label, help, children }: { label: ReactNode; help?: string; children: ReactElement<{ id?: string }> }) {
  const generated = useId()
  const id = children.props.id ?? generated
  return <>{typeof label === 'string' ? <label htmlFor={id} title={help}>{label}</label> : <div className="row row-2" title={help}>{label}</div>}{cloneElement(children, { id })}</>
}
