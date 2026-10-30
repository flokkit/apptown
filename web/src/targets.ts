// Targets and deployments as the UI reads them (design/subsystems/deployment.md §2.2,
// §2.3, §15.7). The kind catalog comes from AppTown (`/targets` → kinds);
// nothing here names a provider, so AWS and Google Cloud are new rows in
// that catalog and nothing else.

// The server's shapes, generated from its Go types (wire.gen.ts).
export type { KindInfo as TargetKind, Offer, OfferCaps, TargetCapabilities as TargetCaps, TargetView, OfferedTarget, RetainedResource as Retained } from './wire.gen'
import type { KindInfo, OfferCaps } from './wire.gen'

// The deployment target families, in the one order every list of targets
// uses, and their
// titles.
export const FAMILY_ORDER = ['built-in', 'attached', 'intranet', 'cloud']
export const FAMILY_TITLE: Record<string, string> = { 'built-in': 'Built-in', attached: 'Attached', intranet: 'Intranet', cloud: 'Cloud' }
// familyRank is a family's place in that order; a family not listed goes last.
export const familyRank = (f: string) => { const i = FAMILY_ORDER.indexOf(f); return i < 0 ? FAMILY_ORDER.length : i }

export const AUDIENCES = ['team', 'organisation', 'partners', 'public'] as const
export const DATA_CLASSES = ['synthetic', 'non-production', 'production'] as const

// Where a target is, in one phrase: "App Platform · DigitalOcean · syd".
export const placeWords = (title: string, vendor: string, region: string) =>
  [title, vendor, region].filter(Boolean).join(' · ')
export const placeOf = (kindInfo: KindInfo, address: Record<string, unknown> | undefined) =>
  placeWords(kindInfo.title, kindInfo.vendor, kindInfo.region && address ? String(address[kindInfo.region] ?? '') : '')

// An offer's caps as a sentence fragment: "non-production data, up to 2 per project".
export const capsWords = (c: OfferCaps | undefined) => {
  if (!c) return ''
  const parts: string[] = []
  if (c.audiences?.length) parts.push(`for ${c.audiences.join('/')}`)
  if (c.data?.length) parts.push(`${c.data.join('/')} data`)
  if (c.maxStanding) parts.push(`up to ${c.maxStanding} per project`)
  return parts.join(', ')
}
