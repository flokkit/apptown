import { Fragment, type ReactNode } from 'react'
import { TECH_BREAK } from './md'

// A technical value (an address, a path, a key, a scope) breaks at its
// separators: a <wbr> after each "/ . : - _ = ," lets a narrow line end
// there instead of inside a word ("club-newsletter.apps." then
// "localhost:36257", not "…apps.lo" then "calhost…"). overflow-wrap: anywhere
// stays the last resort for a segment wider than the line.
export function techBreaks(value: string): ReactNode {
  const parts = value.split(TECH_BREAK)
  return parts.length < 2 ? value : parts.map((part, i) => <Fragment key={i}>{i > 0 && <wbr />}{part}</Fragment>)
}

export function Tech({ children }: { children: string }) {
  return <>{techBreaks(children)}</>
}

// An address inside a sentence (a check's result: "front door
// https://club.example/ → 200") breaks as a technical value does, at its
// separators; the words around it wrap as words.
export function proseBreaks(text: string): ReactNode {
  const parts = text.split(/(https?:\/\/\S+)/)
  return parts.length < 2 ? text : parts.map((part, i) => <Fragment key={i}>{i % 2 ? techBreaks(part) : part}</Fragment>)
}
