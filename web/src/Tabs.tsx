import { useEffect, useRef, useState, type HTMLAttributes } from 'react'

const activeTab = (strip: HTMLElement) => strip.querySelector<HTMLElement>('.active, [aria-selected="true"], [aria-current="page"]')
export function revealActiveTab(strip: HTMLElement) {
  const active = activeTab(strip)
  if (!active) return
  const box = strip.getBoundingClientRect(), item = active.getBoundingClientRect()
  if (item.left < box.left) strip.scrollLeft -= box.left - item.left
  else if (item.right > box.right) strip.scrollLeft += item.right - box.right
}

// Native links and buttons retain their semantics and keyboard order.
export function Tabs({ children, className = 'tabs', ...props }: HTMLAttributes<HTMLDivElement>) {
  const ref = useRef<HTMLDivElement>(null)
  const activeRef = useRef<HTMLElement | null>(null)
  const [more, setMore] = useState(false)
  const [previous, setPrevious] = useState(false)
  const hint = () => {
    const strip = ref.current
    setPrevious(!!strip && strip.scrollLeft > 2)
    setMore(!!strip && strip.scrollWidth - strip.clientWidth - strip.scrollLeft > 2)
  }
  useEffect(() => {
    const strip = ref.current
    if (!strip) return
    const observer = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(() => { revealActiveTab(strip); hint() }) : null
    observer?.observe(strip)
    return () => observer?.disconnect()
  }, [])
  useEffect(() => {
    const strip = ref.current
    if (!strip) return
    const active = activeTab(strip)
    if (active !== activeRef.current) {
      activeRef.current = active
      revealActiveTab(strip)
    }
    hint()
  })
  return <div className={'tab-strip' + (more ? ' more-tabs' : '') + (previous ? ' previous-tabs' : '')}><div {...props} onScroll={hint} ref={ref} className={className}>{children}</div></div>
}
