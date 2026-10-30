import type { DependencyKind, PackView } from '../../wire.gen'
import type { NeedPick } from '../project/settings/Services'

// The new-project wizard's state as one object, and the acts that change
// it. Pure, so the rules of the three steps are tested without a page:
// the address follows the name until someone types their own, a step is
// left only once its one decision is made, and picking a way of working
// starts from the services it proposes.

export type Origin = 'empty' | 'bundle' | 'url'

export type Draft = {
  step: 1 | 2 | 3
  name: string
  nameEdited: boolean
  // Local creation handoff; not sent until the owner supports replay.
  creationRequestId: string
  interviewRevision: number
  // The address someone typed; null while it follows the name.
  address: string | null
  // The initial software description also guides the optional interview.
  description: string
  questions: string[]
  answers: Record<string, string>
  // The packs the project takes together; null = undecided, [] = bare.
  packs: string[] | null
  packWhy: string
  icon: string // the project's face, picked by the interview
  choosing: boolean // the card wall, on demand
  // The services a new project declares: catalog needs, the same list the
  // project's Services tab edits later.
  services: NeedPick[]
  // servicesEdited: someone has ticked or unticked a service. Until then the
  // list is the chosen packs' proposal, worked out again whenever the packs
  // or the service catalog arrive (a pack picked before the catalog has
  // answered would otherwise keep no services).
  servicesEdited: boolean
  // step 3 — a GitLab copy (null until someone picks: the default follows
  // whether this AppTown has a connection), its group ('' = the first listed),
  // and where the code starts from.
  remoteChoice: boolean | null
  namespaceId: string
  repositoryName: string | null
  attachmentRequestId: string
  origin: Origin
  bundleFile: File | null
  sourceURL: string
  sourceBranch: string
  gitSecret: string
}

export const newDraft = (): Draft => ({
  step: 1, name: '', nameEdited: false, address: null, creationRequestId: crypto.randomUUID(), interviewRevision: 0,
  description: '', questions: [], answers: {}, packs: null, packWhy: '', icon: '', choosing: false, services: [], servicesEdited: false,
  remoteChoice: null, namespaceId: '', repositoryName: null, attachmentRequestId: crypto.randomUUID(), origin: 'empty', bundleFile: null, sourceURL: '', sourceBranch: '', gitSecret: '',
})

// slugify is the address a name gets when nobody types one.
export const slugify = (name: string) => name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')

// slugOf is the project's address: the one typed, else the name's.
export const slugOf = (d: Draft) => d.address ?? (slugify(d.name) || (!d.nameEdited && d.name ? 'new-project' : ''))

// The guards: what each step needs before the next.
export const nameReady = (d: Draft) => d.description.trim() !== '' && slugOf(d) !== ''
export const wayReady = (d: Draft) => d.packs !== null
export const originReady = (d: Draft) =>
  d.origin === 'empty' || (d.origin === 'bundle' ? !!d.bundleFile : d.sourceURL.trim().startsWith('https://'))

// servicesFor is what the chosen packs propose together, as needs from the
// catalog: a pack names a service by its catalog capability, and its image
// tag is the version it suggests. A project with the postgres pack starts
// with Postgres ticked; the person tunes from there. Two packs proposing
// the same service propose it once (packs that disagree on it cannot be
// taken together, and creating the project says so).
export function servicesFor(chosen: string[], packs: PackView[], catalog: DependencyKind[]): NeedPick[] {
  const proposed = packs.filter((p) => chosen.includes(p.name)).flatMap((p) => p.services ?? [])
  return proposed
    .filter((sv, i) => proposed.findIndex((o) => o.name === sv.name) === i)
    .filter((sv) => catalog.some((k) => k.capability === sv.name))
    .map((sv): NeedPick => {
      const tag = sv.image.split(':')[1]?.replace(/-alpine$/, '')
      return { capability: sv.name, params: tag ? { version: tag } : {} }
    })
}

type RepoField = 'repositoryName' | 'remoteChoice' | 'namespaceId' | 'origin' | 'bundleFile' | 'sourceURL' | 'sourceBranch' | 'gitSecret'

export type DraftAct =
  | { type: 'restore'; draft: Draft }
  | { type: 'step'; step: Draft['step'] }
  | { type: 'name'; name: string }
  | { type: 'address'; address: string }
  | { type: 'describe'; description: string }
  | { type: 'asked'; questions: string[]; revision: number }
  | { type: 'answer'; question: string; answer: string }
  | { type: 'unask' }
  | { type: 'packs'; packs: string[]; why: string; icon?: string; services: NeedPick[]; revision?: number }
  // The chosen packs' proposal, worked out again from newer reads; ignored
  // once someone has edited the services.
  | { type: 'proposed'; services: NeedPick[] }
  | { type: 'repick' }
  | { type: 'choosing'; on: boolean; revision?: number }
  | { type: 'service'; kind: DependencyKind }
  | { type: 'repo'; set: Partial<Pick<Draft, RepoField>> }

export function draftReducer(d: Draft, a: DraftAct): Draft {
  if ('revision' in a && a.revision !== undefined && a.revision !== d.interviewRevision) return d
  switch (a.type) {
    case 'restore': return a.draft
    case 'step':
      if (a.step === 2 && !nameReady(d)) return d
      if (a.step === 3 && (!nameReady(d) || !wayReady(d))) return d
      return { ...d, step: a.step, interviewRevision: d.interviewRevision + 1 }
    case 'name':
      return { ...d, name: a.name, nameEdited: true, interviewRevision: d.interviewRevision + 1 }
    case 'address':
      return { ...d, address: a.address }
    case 'describe':
      return { ...d, description: a.description, name: d.nameEdited ? d.name : suggestedName(a.description, d), questions: [], answers: {}, interviewRevision: d.interviewRevision + 1 }
    case 'asked':
      return { ...d, questions: a.questions, answers: {} }
    case 'answer':
      return { ...d, answers: { ...d.answers, [a.question]: a.answer }, interviewRevision: d.interviewRevision + 1 }
    case 'unask':
      return { ...d, questions: [], answers: {}, interviewRevision: d.interviewRevision + 1 }
    case 'packs':
      return { ...d, interviewRevision: d.interviewRevision + 1, packs: a.packs, packWhy: a.why, icon: a.icon ?? d.icon, questions: [], answers: {}, choosing: false, services: a.services, servicesEdited: false }
    case 'proposed':
      return d.servicesEdited ? d : { ...d, services: a.services }
    case 'repick':
      return { ...d, packs: null, packWhy: '', choosing: false, interviewRevision: d.interviewRevision + 1 }
    case 'choosing':
      return { ...d, choosing: a.on, interviewRevision: d.interviewRevision + 1 }
    case 'service': {
      const k = a.kind
      const on = d.services.some((n) => n.capability === k.capability)
      return {
        ...d, servicesEdited: true, services: on
          ? d.services.filter((n) => n.capability !== k.capability)
          : [...d.services, { capability: k.capability, params: k.defaults?.version ? { version: k.defaults.version } : {} }],
      }
    }
    case 'repo': {
      const next = { ...d, ...a.set }
      return next.nameEdited ? next : { ...next, name: suggestedName(next.description, next) }
    }
  }
}

// A working title, not a uniqueness claim. The owner reserves the address.
function suggestedName(description: string, d: Pick<Draft, 'origin' | 'bundleFile' | 'sourceURL'>): string {
  let repository = ''
  if (d.origin === 'bundle') repository = d.bundleFile?.name.replace(/\.bundle$/i, '') ?? ''
  if (d.origin === 'url') {
    try { repository = decodeURIComponent(new URL(d.sourceURL).pathname.split('/').filter(Boolean).at(-1) ?? '').replace(/\.git$/i, '') } catch { /* An unfinished URL supplies no title. */ }
  }
  const words = (repository || description).trim().split(/\s+/).filter(Boolean)
  return words.length ? words.slice(0, 6).join(' ').slice(0, 60).trim() : ''
}
