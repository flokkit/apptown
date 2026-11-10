import { cloneElement, useCallback, useId, useLayoutEffect, useRef, useState, type ReactElement, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { useNarrow } from './chrome'
import { Modal } from './Modal'

const focusable = 'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]'

// A disclosure, not an ARIA menu: its ordinary links and controls keep Tab's
// native order. Portalling avoids clipping by scrolling lists and the sidebar.
// takeFocus: a panel opened by a click or key moves focus into itself, for
// keyboard use; one opened by a resting mouse leaves focus where it is. On
// close, focus returns to the trigger only from inside the panel, so a click
// into a field elsewhere keeps the field's focus. Scrolling the trigger out of
// sight closes the panel.
export function Popover({ anchor, onClose, children, id, placement = 'below', className = '', onPointerEnter, onPointerLeave, takeFocus = true }: {
  anchor: RefObject<HTMLElement>; onClose: () => void; children: ReactNode; id: string
  placement?: 'above' | 'below'; className?: string; takeFocus?: boolean
  onPointerEnter?: (event: React.PointerEvent) => void; onPointerLeave?: (event: React.PointerEvent) => void
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
      // A panel belongs beside its control: once the control has scrolled
      // out of sight, close rather than pin the panel over unrelated rows.
      if (rect.bottom < top || rect.top > top + height) { onClose(); return }
      const below = rect.bottom + 6
      const y = placement === 'above' || below + box.height > top + height - 8 ? rect.top - box.height - 6 : below
      element.style.left = `${Math.max(left + 8, Math.min(rect.right - box.width, left + width - box.width - 8))}px`
      element.style.top = `${Math.max(top + 8, Math.min(y, top + height - box.height - 8))}px`
    }
    position()
    if (takeFocus) (element.querySelector<HTMLElement>(focusable) ?? element).focus()
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
      if (element.contains(document.activeElement)) trigger.focus({ preventScroll: true })
    }
    // Focus is taken once, as the panel opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [anchor, onClose, placement])
  return createPortal(<div ref={panel} id={id} tabIndex={-1} className={`popover ${className}`} onPointerEnter={onPointerEnter} onPointerLeave={onPointerLeave}>{children}</div>, document.body)
}

// hover: a mouse resting on the trigger opens the same panel a click does,
// and leaving both closes it again; a click keeps it open. Touch and
// keyboard open it by tapping or pressing, as without hover.
export const hoverDelay = 1000

// title: a disclosure that holds a form (fields, prompts that open in place)
// rather than a few commands. On a phone a panel beside its control would
// lie over the rows below it while it is filled in, so there it opens as a
// dialog sized to its content, titled by what it acts on, as DetailsPopover
// does; elsewhere it is the same panel beside its control.
export function Disclosure({ trigger, children, className, placement, hidden, closeOnAction = true, hover = false, title }: {
  trigger: ReactElement; children: ReactNode | ((close: () => void) => ReactNode); className?: string
  placement?: 'above' | 'below'; hidden?: boolean; closeOnAction?: boolean; hover?: boolean; title?: ReactNode
}) {
  const narrow = useNarrow()
  const [open, setOpen] = useState<false | 'hover' | 'click'>(false)
  const anchor = useRef<HTMLButtonElement>(null)
  const leaving = useRef<ReturnType<typeof setTimeout>>()
  const id = useId()
  const close = useCallback(() => setOpen(false), [])
  // A mouse must rest a second before the panel opens, so one passing over
  // a list does not flash panels; once open, a short grace lets it reach the
  // panel.
  const enter = (event: React.PointerEvent) => {
    if (!hover || event.pointerType !== 'mouse') return
    clearTimeout(leaving.current)
    leaving.current = setTimeout(() => setOpen(value => value || 'hover'), open ? 0 : hoverDelay)
  }
  const leave = (event: React.PointerEvent) => {
    if (!hover || event.pointerType !== 'mouse') return
    clearTimeout(leaving.current)
    leaving.current = setTimeout(() => setOpen(value => value === 'hover' ? false : value), 150)
  }
  const content = <div className="popover-content" onClick={event => {
    if ((event.target as Element).closest('a[href]') || (closeOnAction && (event.target as Element).closest('button'))) close()
  }}>{typeof children === 'function' ? children(close) : children}</div>
  if (title !== undefined && narrow) {
    return <div className={className} hidden={hidden} onClick={event => event.stopPropagation()}>
      {cloneElement(trigger, { ref: anchor, 'aria-expanded': !!open, 'aria-controls': open ? id : undefined, 'aria-haspopup': 'dialog', onClick: () => setOpen('click') })}
      {open && !hidden && <Modal fit title={title} onClose={close}><div id={id}>{content}</div></Modal>}
    </div>
  }
  return <div className={className} hidden={hidden} onClick={event => event.stopPropagation()} onPointerEnter={enter} onPointerLeave={leave}>
    {cloneElement(trigger, { ref: anchor, 'aria-expanded': !!open, 'aria-controls': open ? id : undefined, onClick: () => setOpen(value => value === 'click' ? false : 'click') })}
    {open && !hidden && <Popover anchor={anchor} onClose={close} id={id} placement={placement} onPointerEnter={enter} onPointerLeave={leave} takeFocus={open === 'click'}>
      {content}
    </Popover>}
  </div>
}

// DetailsPopover is a status's details: the same panel opens when a mouse
// rests on the status or when it is clicked or tapped. Statuses and approval
// states use it; their details are never written on the page. On a phone a
// panel beside the status would lie over the buttons next to it, so there a
// tap opens the details as a dialog sized to its text, titled by the status.
export function DetailsPopover({ trigger, children, className, closeOnAction, title }: {
  trigger: ReactElement; children: ReactNode; className?: string; closeOnAction?: boolean; title: ReactNode
}) {
  const narrow = useNarrow()
  const [open, setOpen] = useState(false)
  const id = useId()
  const close = useCallback(() => setOpen(false), [])
  if (!narrow) return <Disclosure hover trigger={trigger} className={className} closeOnAction={closeOnAction}>{children}</Disclosure>
  return <div className={className} onClick={event => event.stopPropagation()}>
    {cloneElement(trigger, { 'aria-expanded': open, 'aria-controls': open ? id : undefined, 'aria-haspopup': 'dialog', onClick: () => setOpen(true) })}
    {open && <Modal fit title={title} onClose={close}><div id={id}>{children}</div></Modal>}
  </div>
}
