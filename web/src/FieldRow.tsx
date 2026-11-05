import { cloneElement, useId, type ReactElement } from 'react'

// Keep the label association even when callers do not supply an id.
export function FieldRow({ label, help, children }: { label: string; help?: string; children: ReactElement<{ id?: string }> }) {
  const generated = useId()
  const id = children.props.id ?? generated
  return <><label htmlFor={id} title={help}>{label}</label>{cloneElement(children, { id })}</>
}
