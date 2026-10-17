import { useEffect, useState } from 'react'
import { createStore } from './hooks'

// What the top bar says (design/subsystems/ui-coherence.md § Top bar): a page whose
// URL doesn't tell the whole story — a change, a decision — publishes its
// crumb and state here; the bar derives everything else from the route.
export type Crumb = { label: string; to?: string }
export type PageContext = { crumbs?: Crumb[]; chip?: { text: string; cls?: string } }

const pageContext = createStore<PageContext>({})
export const setPageContext = pageContext.set
export const usePageContext = pageContext.use

// The shell's one breakpoint: under it the sidebar folds into a menu button
// and the top bar keeps only the menu, the name and where you are; finding,
// the theme and who you are move into the menu. Kept in step with the
// @media (max-width: 800px) rules in styles.css.
export const NARROW_QUERY = '(max-width: 800px)'

export function useNarrow(): boolean {
  const query = () => typeof window !== 'undefined' && !!window.matchMedia && window.matchMedia(NARROW_QUERY).matches
  const [narrow, setNarrow] = useState(query)
  useEffect(() => {
    if (!window.matchMedia) return
    const mq = window.matchMedia(NARROW_QUERY)
    const on = () => setNarrow(mq.matches)
    on()
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return narrow
}
