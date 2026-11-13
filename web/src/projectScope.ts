import { useEffect } from 'react'
import { useLocation } from 'react-router-dom'
import { createStore } from './hooks'

// A scope belongs to one address. A previous page's answer cannot name the
// project for a new address, even before its effects or reads have run.
const scope = createStore<{ address: string; slug: string } | null>(null)
export const clearProjectScope = () => scope.set(null)
export function useSetProjectScope(slug: string | null | undefined) {
  const { pathname } = useLocation()
  useEffect(() => {
    scope.set(slug ? { address: pathname, slug } : null)
    return clearProjectScope
  }, [pathname, slug])
}
export function useProjectScope(): string | null {
  const { pathname } = useLocation()
  const value = scope.use()
  return value?.address === pathname ? value.slug : null
}
