import type { ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ListRow } from './ListRow'

// One row shape for every ways-of-working library (packs, workflows,
// guidance, policies): name and what is unusual about it, one wrapping line
// of purpose, its state, a ⋯ menu on the right. The whole row opens its
// linked page. The four lists keep the same reading order.
export function LibraryRow({ name, badges, purpose, state, menu, to, detail, children }: {
  name: ReactNode; badges?: ReactNode; purpose?: ReactNode; state?: ReactNode; menu?: ReactNode
  to?: string; detail?: ReactNode; children?: ReactNode
}) {
  const nav = useNavigate()
  const act = () => { if (to) nav(to) }
  return (
    <div className={to ? 'list-row-open' : undefined} role="listitem" onClick={act}>
      <ListRow className="pack-row" identity={<>
        <span className="pack-title">
          {to ? <Link className="pack-name" to={to} onClick={(e) => e.stopPropagation()}><strong>{name}</strong></Link>
            : <strong className="pack-name">{name}</strong>}
          {badges}
        </span>
        {purpose && <span className="muted pack-purpose">{purpose}</span>}
        {children}
      </>} state={state} action={menu && <span onClick={(e) => e.stopPropagation()}>{menu}</span>}
        detail={detail ? <div className="library-detail" onClick={(e) => e.stopPropagation()}>{detail}</div> : undefined} />
    </div>
  )
}

export function LibraryRows({ children }: { children: ReactNode }) {
  return <div className="project-rows" role="list">{children}</div>
}

// A description's first sentence is its purpose: stock descriptions run a
// purpose line and a longer explanation together.
export function firstSentence(text = ''): string {
  const m = text.trim().match(/^.+?[.!?](?=\s|$)/)
  return (m ? m[0] : text).trim()
}
