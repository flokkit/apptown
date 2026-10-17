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
  // step 1 — the ghost in the box is already a random roll: the
  // suggestion sits lightly grey; the 🎲 adopts exactly what you see, and
  // pressing it again re-rolls in place.
  suggestion: string
  name: string
  // The address someone typed; null while it follows the name.
  address: string | null
  // step 2 — the short interview.
  description: string
  questions: string[]
  answers: Record<string, string>
  pack: string | null // null = undecided, '' = bare
  packWhy: string
  icon: string // the project's face, picked by the interview
  choosing: boolean // the card wall, on demand
  // The services a new project declares: catalog needs, the same list the
  // project's Services tab edits later.
  services: NeedPick[]
  // servicesEdited: someone has ticked or unticked a service. Until then the
  // list is the chosen pack's proposal, worked out again whenever the packs
  // or the service catalog arrive (a pack picked before the catalog has
  // answered would otherwise keep no services).
  servicesEdited: boolean
  // step 3 — a GitLab copy (null until someone picks: the default follows
  // whether this forge has a connection), its group ('' = the first listed),
  // and where the code starts from.
  remoteChoice: boolean | null
  namespaceId: string
  origin: Origin
  bundleFile: File | null
  sourceURL: string
  sourceBranch: string
  gitSecret: string
}

export const newDraft = (suggestion: string): Draft => ({
  step: 1, suggestion, name: '', address: null,
  description: '', questions: [], answers: {}, pack: null, packWhy: '', icon: '', choosing: false, services: [], servicesEdited: false,
  remoteChoice: null, namespaceId: '', origin: 'empty', bundleFile: null, sourceURL: '', sourceBranch: '', gitSecret: '',
})

// slugify is the address a name gets when nobody types one.
export const slugify = (name: string) => name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')

// slugOf is the project's address: the one typed, else the name's.
export const slugOf = (d: Draft) => d.address ?? slugify(d.name)

// The guards: what each step needs before the next.
export const nameReady = (d: Draft) => slugOf(d) !== ''
export const wayReady = (d: Draft) => d.pack !== null
export const originReady = (d: Draft) =>
  d.origin === 'empty' || (d.origin === 'bundle' ? !!d.bundleFile : d.sourceURL.trim().startsWith('https://'))

// servicesFor is what a pack proposes, as needs from the catalog: a pack
// names a service by its catalog capability, and its image tag is the
// version it suggests. A server pack that says "Postgres" starts with
// Postgres ticked; the person tunes from there.
export function servicesFor(pack: string, packs: PackView[], catalog: DependencyKind[]): NeedPick[] {
  return (packs.find((p) => p.name === pack)?.services ?? [])
    .filter((sv) => catalog.some((k) => k.capability === sv.name))
    .map((sv): NeedPick => {
      const tag = sv.image.split(':')[1]?.replace(/-alpine$/, '')
      return { capability: sv.name, params: tag ? { version: tag } : {} }
    })
}

type RepoField = 'remoteChoice' | 'namespaceId' | 'origin' | 'bundleFile' | 'sourceURL' | 'sourceBranch' | 'gitSecret'

export type DraftAct =
  | { type: 'step'; step: Draft['step'] }
  | { type: 'name'; name: string }
  // The 🎲: an empty box takes the suggestion shown; a filled one takes `next`.
  | { type: 'roll'; next: string }
  | { type: 'address'; address: string }
  | { type: 'describe'; description: string }
  | { type: 'asked'; questions: string[] }
  | { type: 'answer'; question: string; answer: string }
  | { type: 'unask' }
  | { type: 'pack'; pack: string; why: string; icon?: string; services: NeedPick[] }
  // The chosen pack's proposal, worked out again from newer reads; ignored
  // once someone has edited the services.
  | { type: 'proposed'; services: NeedPick[] }
  | { type: 'repick' }
  | { type: 'choosing'; on: boolean }
  | { type: 'service'; kind: DependencyKind }
  | { type: 'repo'; set: Partial<Pick<Draft, RepoField>> }

export function draftReducer(d: Draft, a: DraftAct): Draft {
  switch (a.type) {
    case 'step':
      if (a.step === 2 && !nameReady(d)) return d
      if (a.step === 3 && (!nameReady(d) || !wayReady(d))) return d
      return { ...d, step: a.step }
    case 'name':
      return { ...d, name: a.name }
    case 'roll':
      return d.name ? { ...d, name: a.next, suggestion: a.next } : { ...d, name: d.suggestion }
    case 'address':
      return { ...d, address: a.address }
    case 'describe':
      return { ...d, description: a.description }
    case 'asked':
      return { ...d, questions: a.questions, answers: {} }
    case 'answer':
      return { ...d, answers: { ...d.answers, [a.question]: a.answer } }
    case 'unask':
      return { ...d, questions: [], answers: {} }
    case 'pack':
      return { ...d, pack: a.pack, packWhy: a.why, icon: a.icon ?? d.icon, questions: [], answers: {}, choosing: false, services: a.services, servicesEdited: false }
    case 'proposed':
      return d.servicesEdited ? d : { ...d, services: a.services }
    case 'repick':
      return { ...d, pack: null, packWhy: '', choosing: false }
    case 'choosing':
      return { ...d, choosing: a.on }
    case 'service': {
      const k = a.kind
      const on = d.services.some((n) => n.capability === k.capability)
      return {
        ...d, servicesEdited: true, services: on
          ? d.services.filter((n) => n.capability !== k.capability)
          : [...d.services, { capability: k.capability, params: k.defaults?.version ? { version: k.defaults.version } : {} }],
      }
    }
    case 'repo':
      return { ...d, ...a.set }
  }
}

// The 🎲: random-but-sensical names for the blank-page moment. Two curated
// lists of short, everyday words; about 1,000 combinations, each a short,
// friendly project name ("Turbo Pickle", "Midnight Library").
const NAME_FIRST = [
  'Turbo', 'Mega', 'Tiny', 'Sneaky', 'Bouncy', 'Cosmic', 'Pixel', 'Rocket',
  'Golden', 'Frozen', 'Wobbly', 'Secret', 'Thunder', 'Lucky', 'Midnight',
  'Rainbow', 'Robo', 'Super', 'Jelly', 'Brave',
  // more descriptive words and moods
  'Sparkly', 'Glitter', 'Fluffy',
  'Spooky', 'Giant', 'Speedy', 'Sleepy', 'Grumpy', 'Dizzy', 'Invisible',
  'Fancy', 'Angry', 'Electric', 'Magic',
]
const NAME_SECOND = [
  'Wombat', 'Volcano', 'Penguin', 'Garden', 'Racer', 'Castle', 'Comet',
  'Noodle', 'Dragon', 'Library', 'Kitchen', 'Galaxy', 'Goblin', 'Picnic',
  'Tornado', 'Wizard', 'Mango', 'Submarine', 'Meadow', 'Biscuit',
  // more playful nouns
  'Chicken', 'Egg', 'Pickle', 'Potato', 'Banana', 'Sock',
  'Monkey', 'Ninja', 'Dinosaur', 'Unicorn',
]
export function rollName(): string {
  const pick = (xs: string[]) => xs[Math.floor(Math.random() * xs.length)]
  return `${pick(NAME_FIRST)} ${pick(NAME_SECOND)}`
}
