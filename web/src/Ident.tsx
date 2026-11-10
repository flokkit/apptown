import { Fragment } from 'react'

// A short name (a target, a pack, a policy) is one unit in running text: it
// moves to the next line whole rather than breaking at its hyphen
// ("built-in-" then "docker"). A name with "/" is a unit per segment, so it
// may break after the slash. A name wider than its line still breaks inside
// (overflow-wrap) rather than widening the page. The text stays plain, so
// copying it and finding it on the page still work. Wrap plain text only:
// an underline does not reach into an inline-block, so put Ident outside a
// link, never inside one.
// after is punctuation that belongs to the name in a list (", "'s comma):
// it sits inside the name's unit, so a line never starts with it.
export function Ident({ children, after = '' }: { children: string; after?: string }) {
  const parts = children.split('/')
  return <>{parts.map((part, i) => <Fragment key={i}><span className="ident">{part}{i < parts.length - 1 ? '/' : after}</span>{i < parts.length - 1 && <wbr />}</Fragment>)}</>
}
