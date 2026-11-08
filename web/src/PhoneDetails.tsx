import { Children, type ReactNode } from 'react'

export function PhoneDetails({ children }: { children: ReactNode }) {
  if (Children.toArray(children).length === 0) return null
  return <div className="phone-details">{children}</div>
}
