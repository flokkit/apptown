import type { ModelSlotView } from '../wire.gen'

// The four basic choices use the glossary names; tasks keep their own names.
const BASIC: Record<string, string> = { 'main-strong': 'Main · strong', 'main-fast': 'Main · fast', 'secondary-strong': 'Second opinion · strong', 'secondary-fast': 'Second opinion · fast' }
export const jobName = (slot: string) => BASIC[slot] ?? slot.charAt(0).toUpperCase() + slot.slice(1)

// Follow the server's profile-first resolution, including its deciding profile.
export function effective(rows: ModelSlotView[], slot: string, profile: string): { model: string; from: string; profile: string } {
  const bySlot = new Map(rows.map((r) => [r.slot, r]))
  for (const p of profile === 'standard' ? [profile] : [profile, 'standard']) {
    const seen = new Set<string>()
    let name = slot
    while (name && !seen.has(name)) {
      seen.add(name)
      const r = bySlot.get(name)
      const m = r?.patterns[p]
      if (m) return { model: m, from: name, profile: p }
      name = r?.fallback || (name === 'main-strong' ? '' : 'main-strong')
    }
  }
  return { model: '', from: '', profile: '' }
}

