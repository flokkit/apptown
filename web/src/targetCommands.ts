import { patch, post } from './api'
import type { OfferCaps, TargetEdited } from './wire.gen'

// Optional fields are actual changes: false and empty strings must survive.
export type TargetSettings = {
  domain: string
  persistentVolumes: boolean
  previews: boolean
  needsMode: string
  evals: boolean
  rehearsals: boolean
  copyRunner: string
  environment: string
}
export type TargetSettingsChanges = Partial<TargetSettings>
export type TargetOffer = OfferCaps & { group: string }

export const NEEDS_MODES = [
  { value: 'emulators', title: 'emulators on the host' },
  { value: 'managed', title: 'managed by the provider' },
  { value: 'register', title: 'registered by hand' },
] as const

// Compare to the editor's captured base, never to a refreshed query result.
export function changedTargetSettings<T extends TargetSettingsChanges>(base: T, draft: T): Partial<T> {
  const changes: Partial<T> = {}
  for (const key of Object.keys(draft) as (keyof T)[]) {
    if (draft[key] !== base[key]) changes[key] = draft[key]
  }
  return changes
}

export const editTarget = (target: string, expectedRevision: number, changes: TargetSettingsChanges) =>
  patch<TargetEdited>(`/targets/${target}`, { ...changes, expectedRevision })

// Blank or zero means unlimited; a supplied limit must be an exact whole count.
export function targetOffer(group: string, maxStanding: string, audiences: string[], data: string[]): TargetOffer {
  const caps: TargetOffer = { group }
  if (maxStanding.trim()) {
    const limit = Number(maxStanding)
    if (!Number.isSafeInteger(limit) || limit < 0) throw new Error('Enter a whole number of standing deployments, zero, or leave the limit blank.')
    caps.maxStanding = limit
  }
  if (audiences.length) caps.audiences = audiences
  if (data.length) caps.data = data
  return caps
}

export const offerTarget = (target: string, offer: TargetOffer) => post(`/targets/${target}/offers`, offer)
