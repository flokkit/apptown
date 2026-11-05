import { cloneElement, useCallback, useId, useLayoutEffect, useRef, useState, type ReactElement, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'

const focusable = 'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]'

// A disclosure, not an ARIA menu: its ordinary links and controls keep Tab's
// native order. Portalling avoids clipping by scrolling lists and the sidebar.
export function Popover({ anchor, onClose, children, id, placement = 'below', className = '' }: {
  anchor: RefObject<HTMLElement>; onClose: () => void; children: ReactNode; id: string
  placement?: 'above' | 'below'; className?: string
}) {
  const panel = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const trigger = anchor.current!
    const element = panel.current!
    const viewport = window.visualViewport
    const position = () => {
      const left = viewport?.offsetLeft ?? 0, top = viewport?.offsetTop ?? 0
      const width = viewport?.width ?? window.innerWidth, height = viewport?.height ?? window.innerHeight
      element.style.maxWidth = `${width - 16}px`
      element.style.maxHeight = `${height - 16}px`
      const rect = trigger.getBoundingClientRect(), box = element.getBoundingClientRect()
      const below = rect.bottom + 6
      const y = placement === 'above' || below + box.height > top + height - 8 ? rect.top - box.height - 6 : below
      element.style.left = `${Math.max(left + 8, Math.min(rect.right - box.width, left + width - box.width - 8))}px`
      element.style.top = `${Math.max(top + 8, Math.min(y, top + height - box.height - 8))}px`
    }
    position()
    ;(element.querySelector<HTMLElement>(focusable) ?? element).focus()
    const down = (event: PointerEvent) => {
      if (!element.contains(event.target as Node) && !trigger.contains(event.target as Node)) onClose()
    }
    const key = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); onClose() }
    }
    const observer = new ResizeObserver(position)
    observer.observe(element)
    window.addEventListener('resize', position)
    window.addEventListener('scroll', position, true)
    viewport?.addEventListener('resize', position)
    viewport?.addEventListener('scroll', position)
    document.addEventListener('pointerdown', down)
    document.addEventListener('keydown', key)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', position)
      window.removeEventListener('scroll', position, true)
      viewport?.removeEventListener('resize', position)
      viewport?.removeEventListener('scroll', position)
      document.removeEventListener('pointerdown', down)
      document.removeEventListener('keydown', key)
      trigger.focus()
    }
  }, [anchor, onClose, placement])
  return createPortal(<div ref={panel} id={id} tabIndex={-1} className={`popover ${className}`}>{children}</div>, document.body)
}

export function Disclosure({ trigger, children, className, placement, hidden, closeOnAction = true }: {
  trigger: ReactElement; children: ReactNode | ((close: () => void) => ReactNode); className?: string
  placement?: 'above' | 'below'; hidden?: boolean; closeOnAction?: boolean
}) {
  const [open, setOpen] = useState(false)
  const anchor = useRef<HTMLButtonElement>(null)
  const id = useId()
  const close = useCallback(() => setOpen(false), [])
  return <div className={className} hidden={hidden} onClick={event => event.stopPropagation()}>
    {cloneElement(trigger, { ref: anchor, 'aria-expanded': open, 'aria-controls': open ? id : undefined, onClick: () => setOpen(value => !value) })}
    {open && !hidden && <Popover anchor={anchor} onClose={close} id={id} placement={placement}>
      <div className="popover-content" onClick={event => {
        if ((event.target as Element).closest('a[href]') || (closeOnAction && (event.target as Element).closest('button'))) close()
      }}>{typeof children === 'function' ? children(close) : children}</div>
    </Popover>}
  </div>
}
