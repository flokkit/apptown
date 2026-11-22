import { useEffect, useState } from 'react'
import { createStore } from './hooks'

// Pages provide display names for dynamic route segments. The shell owns the trail.
export type PageContext = { names?: Record<string, string> }

const pageContext = createStore<PageContext>({})
export const setPageContext = pageContext.set
export const usePageContext = pageContext.use

export function usePageName(path: string, name: string | undefined): void {
  useEffect(() => {
    setPageContext({ names: name ? { [path]: name } : {} })
    return () => setPageContext({})
  }, [path, name])
}

// The shared widths (web/DESIGN.md). Under narrow (800) pages take their
// phone layout: help and Find open as dialogs and rows stack. Under fold
// (1100) the left nav folds behind the menu button at the start of the
// page's bar: beside a 210px nav a portrait tablet or a half-screen window
// would leave the page less than its reading width. Kept in step with the
// @media rules in the stylesheets and the Stylelint allow-list.
export const BREAKPOINTS = { compact: 600, narrow: 800, fold: 1100 } as const
export const NARROW_QUERY = `(max-width: ${BREAKPOINTS.narrow}px)`
export const FOLD_QUERY = `(max-width: ${BREAKPOINTS.fold}px)`

function useMedia(q: string): boolean {
  const query = () => typeof window !== 'undefined' && !!window.matchMedia && window.matchMedia(q).matches
  const [matches, setMatches] = useState(query)
  useEffect(() => {
    if (!window.matchMedia) return
    const mq = window.matchMedia(q)
    const on = () => setMatches(mq.matches)
    on()
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [q])
  return matches
}

// useNarrow: the phone layout.
export function useNarrow(): boolean { return useMedia(NARROW_QUERY) }

// useFolded: the left nav is behind the menu button.
export function useFolded(): boolean { return useMedia(FOLD_QUERY) }

// Safari's keyboard resizes the visual viewport, not the CSS viewport.
// Publish its position too, so focus panning cannot move the chat's head away.
export function useChatViewport(narrow: boolean): void {
  useEffect(() => {
    const viewport = window.visualViewport
    if (!narrow || !viewport) return
    const style = document.documentElement.style
    const update = () => {
      style.setProperty('--viewport-h', `${viewport.height}px`)
      style.setProperty('--viewport-top', `${viewport.offsetTop}px`)
    }
    update()
    viewport.addEventListener('resize', update)
    viewport.addEventListener('scroll', update)
    return () => {
      viewport.removeEventListener('resize', update)
      viewport.removeEventListener('scroll', update)
      style.removeProperty('--viewport-h')
      style.removeProperty('--viewport-top')
    }
  }, [narrow])
}
