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
// purpose line and a longer explanation together. A full stop inside inline
// code (`build: .`) does not end the sentence.
export function firstSentence(text = ''): string {
  const t = text.trim()
  let code = false
  for (let i = 1; i < t.length; i++) {
    if (t[i] === '`') code = !code
    else if (!code && '.!?'.includes(t[i]) && /\S/.test(t[i - 1]) && (i + 1 === t.length || /\s/.test(t[i + 1]))) return t.slice(0, i + 1)
  }
  return t
}

// inlineCode sets a summary's backtick spans as code and leaves the rest as
// text. It is not markdown: nothing else is read, and an unpaired backtick
// stays as it is.
export function inlineCode(text: string): ReactNode {
  const parts = text.split('`')
  if (parts.length < 3) return text
  return parts.map((part, i) => i % 2 === 1 && i < parts.length - 1 ? <code key={i}>{part}</code> : i % 2 === 1 ? '`' + part : part)
}
