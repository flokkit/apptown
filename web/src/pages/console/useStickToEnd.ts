import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from 'react'

// useStickToEnd keeps a conversation at its newest words while the reader
// is there, whatever made it grow: a new message, words streaming in, a
// picture loading, the input growing or the keyboard opening. It watches
// sizes, not data, so no kind of growth is missed. A finger on the
// conversation or the wheel turned up always wins at once; reaching the end
// again resumes following. While the reader is up, growth raises "New
// messages below"; far up with nothing new, the cue offers "Newest".
export type Cue = '' | 'new' | 'newest'
const NEAR = 60
const FAR = 500

export function useStickToEnd(scroller: RefObject<HTMLElement | null>, content: RefObject<HTMLElement | null>, shown: boolean): { cue: Cue; toEnd: () => void } {
  const stuck = useRef(true)
  const touching = useRef(false)
  const height = useRef(0)
  const [cue, setCue] = useState<Cue>('')
  const toEnd = useCallback(() => {
    const el = scroller.current
    stuck.current = true
    setCue('')
    if (el) el.scrollTop = el.scrollHeight
  }, [scroller])

  useLayoutEffect(() => {
    const el = scroller.current, inner = content.current
    if (!el || !inner) return
    const distance = () => el.scrollHeight - el.scrollTop - el.clientHeight
    const grew = () => {
      const h = inner.offsetHeight
      const more = h > height.current
      height.current = h
      if (stuck.current && !touching.current) el.scrollTop = el.scrollHeight
      else if (more && !stuck.current) setCue('new')
    }
    const onScroll = () => {
      stuck.current = distance() < NEAR
      if (stuck.current) setCue('')
      else setCue((c) => c === 'new' ? c : distance() > FAR ? 'newest' : '')
    }
    const onWheel = (e: WheelEvent) => { if (e.deltaY < 0) stuck.current = false }
    const onTouchStart = () => { touching.current = true }
    const onTouchEnd = () => { touching.current = false; stuck.current = distance() < NEAR }
    const onTouchMove = () => { stuck.current = distance() < NEAR && stuck.current }
    el.addEventListener('scroll', onScroll, { passive: true })
    el.addEventListener('wheel', onWheel, { passive: true })
    el.addEventListener('touchstart', onTouchStart, { passive: true })
    el.addEventListener('touchmove', onTouchMove, { passive: true })
    el.addEventListener('touchend', onTouchEnd, { passive: true })
    el.addEventListener('touchcancel', onTouchEnd, { passive: true })
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(grew)
    observer?.observe(inner)
    observer?.observe(el)
    return () => {
      observer?.disconnect()
      el.removeEventListener('scroll', onScroll)
      el.removeEventListener('wheel', onWheel)
      el.removeEventListener('touchstart', onTouchStart)
      el.removeEventListener('touchmove', onTouchMove)
      el.removeEventListener('touchend', onTouchEnd)
      el.removeEventListener('touchcancel', onTouchEnd)
    }
  }, [scroller, content])

  // Coming back to a conversation (the change page's tab shown again) opens
  // at its newest words, like a fresh view.
  useEffect(() => { if (shown) toEnd() }, [shown, toEnd])
  return { cue, toEnd }
}
