import type { Message } from '../../wire.gen'
import { PRODUCT } from '../../brand'

// ---- a conversation as a thread -----------------------------------------
// The record is a list of messages; a person reads it as turns: an ask (your
// words, a button you pressed, or AppTown's own check), then the helper's
// working-out, its runs of steps, and the words where it handed back. Every
// chat (live, ended, on the change page) is drawn from this one projection.

// The mark on AppTown's own check messages in a chat, either spelling: a
// chat from before the AppTown rename keeps its "[forge check]" as written.
export const CHECK_MARK = /^\[(?:apptown|forge) check\]/
export const BUTTON_MARK = /^\[apptown button: ([^\]]+)\]/

export type Step = { name: string; detail?: string; done?: boolean; at?: string }
export type AskKind = 'you' | 'check' | 'button'
export type Ask = { key: string; kind: AskKind; message: Message }
// A note is words the helper went on from; a final is where it handed back.
export type Part =
  | { kind: 'note' | 'final'; key: string; text: string; draft: boolean }
  | { kind: 'run'; key: string; steps: Step[]; live: boolean }
  | { kind: 'error' | 'stopped'; key: string; text: string }
export type RunPart = Extract<Part, { kind: 'run' }>
export type Turn = { key: string; ask: Ask | null; parts: Part[] }

export function askKind(m: Message): AskKind {
  const text = m.text ?? ''
  return CHECK_MARK.test(text) ? 'check' : BUTTON_MARK.test(text) ? 'button' : 'you'
}

// toThread folds the record (and the message the helper is writing, its
// draft) into turns. handedBack is the server's reading of whose turn it is.
//
// The answer of a turn (drawn as final) is its last words, when nothing
// follows them in the turn and the turn is over: the helper handed back, or
// the person asked again. AppTown's own check is not the person asking: a
// failed boot check after "it's ready" cancels that claim, as it always has.
// Steps are live only in the last turn while the helper has not handed back.
export function toThread(messages: Message[], draft: Message | null, handedBack: boolean): Turn[] {
  const turns: Turn[] = []
  let cur: Turn | null = null
  const all = draft ? [...messages, draft] : messages
  const turn = (): Turn => {
    if (!cur) { cur = { key: 'lead', ask: null, parts: [] }; turns.push(cur) }
    return cur
  }
  for (const m of all) {
    const isDraft = m === draft
    if (m.role === 'you') {
      cur = { key: `a${m.i}`, ask: { key: `a${m.i}`, kind: askKind(m), message: m }, parts: [] }
      turns.push(cur)
      continue
    }
    const t = turn()
    if (m.error) { t.parts.push({ kind: 'error', key: `e${m.i}`, text: m.text ?? '' }); continue }
    if (m.stopped) { t.parts.push({ kind: 'stopped', key: `s${m.i}`, text: m.text ?? '' }); continue }
    if (m.text?.trim()) t.parts.push({ kind: 'note', key: `t${m.i}`, text: m.text, draft: isDraft })
    const tools = m.tools ?? []
    if (tools.length) {
      const steps = tools.map((x) => ({ name: x.name, detail: x.detail, done: isDraft ? false : x.done, at: m.at }))
      const last = t.parts.at(-1)
      if (last?.kind === 'run') last.steps.push(...steps)
      else t.parts.push({ kind: 'run', key: `r${m.i}`, steps, live: false })
    }
  }
  turns.forEach((t, n) => {
    const next = turns[n + 1]
    const over = next ? next.ask?.kind !== 'check' : handedBack
    const last = t.parts.at(-1)
    if (last?.kind === 'note' && !last.draft && over) t.parts[t.parts.length - 1] = { ...last, kind: 'final' }
    // The stream marks a step done once its result is recorded; until then it runs.
    if (!next && !handedBack) for (const p of t.parts) if (p.kind === 'run') p.live = p.steps.some((s) => !s.done)
  })
  return turns
}

// What a run's newest step is doing, in words, and what the run amounts to.
export const TOOL_WORDS: Record<string, string> = {
  bash: 'ran', read: 'read', edit: 'edited', write: 'wrote',
  grep: 'searched', find: 'looked for', ls: 'listed',
}
export const stepVerb = (s: Step) => TOOL_WORDS[s.name] ?? s.name
export const stepCount = (n: number) => `${n} step${n === 1 ? '' : 's'}`

// The first line of an ask, for the steps column's headings.
export function askLine(ask: Ask | null): string {
  if (!ask) return 'Before your first message'
  const text = ask.message.text ?? ''
  if (ask.kind === 'check') return `${PRODUCT} check: the app did not start`
  const button = text.match(BUTTON_MARK)
  if (button) return button[1]
  const line = text.split('\n').find((l) => l.trim() && !/attached .* \(\d+ bytes\)/.test(l)) ?? ''
  return line.trim() || '(a file)'
}

// Time of a step from its ask, as +m:ss, when both are known.
export function sinceAsk(ask: Ask | null, step: Step): string {
  const from = ask?.message.at ? Date.parse(ask.message.at) : NaN
  const at = step.at ? Date.parse(step.at) : NaN
  if (!Number.isFinite(from) || !Number.isFinite(at) || at < from) return ''
  const s = Math.round((at - from) / 1000)
  return `+${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

// morphHtml brings target's children to the given rendering, keeping every
// node that is already right: equal nodes stay, a text node is extended (or
// its data set), an element with the same tag and attributes is walked, and
// anything else is replaced. The result always equals a fresh rendering, so
// a revised link or a closed code fence simply replaces what changed; while
// words stream, only the growing text node changes.
export function morphHtml(target: Element, html: string): void {
  const t = document.createElement('template')
  t.innerHTML = html
  morphChildren(target, t.content)
}

function sameAttributes(a: Element, b: Element): boolean {
  if (a.attributes.length !== b.attributes.length) return false
  for (const at of Array.from(a.attributes)) if (b.getAttribute(at.name) !== at.value) return false
  return true
}

function morphChildren(old: Node, next: Node): void {
  const want = Array.from(next.childNodes)
  for (let i = 0; i < want.length; i++) {
    const o = old.childNodes[i], n = want[i]
    if (!o) { old.appendChild(n); continue }
    if (o.nodeType === Node.TEXT_NODE && n.nodeType === Node.TEXT_NODE) {
      const od = (o as Text).data, nd = (n as Text).data
      if (od !== nd) { if (nd.startsWith(od)) (o as Text).appendData(nd.slice(od.length)); else (o as Text).data = nd }
      continue
    }
    if (o.nodeType === Node.ELEMENT_NODE && n.nodeType === Node.ELEMENT_NODE && o.nodeName === n.nodeName && sameAttributes(o as Element, n as Element)) {
      if (!o.isEqualNode(n)) morphChildren(o, n)
      continue
    }
    old.replaceChild(n, o)
  }
  while (old.childNodes.length > want.length) old.removeChild(old.lastChild!)
}
