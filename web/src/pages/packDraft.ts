import type { PackSpec } from '../wire.gen'

// Draft is a pack's declaration as the editor sends it (httpapi PackSave):
// every field the author wrote, so a field the form does not edit
// (toolchain, services, extensions) is saved as it was, never dropped.
export type Draft = PackSpec & {
  name: string; description: string; versionName: string
}

export function draftFrom(name: string, description: string, declaration: PackSpec): Draft {
  const d = structuredClone(declaration)
  return { ...d, name, description, versionName: '',
    guidance: d.guidance ?? [], workflows: d.workflows ?? [], policies: d.policies ?? [] }
}

// keptAsWritten names the declaration's fields the editor carries but does
// not edit, so the page can say they are kept.
export function keptAsWritten(d: Draft): string[] {
  const out: string[] = []
  if (d.toolchain) out.push(`toolchain ${d.toolchain}`)
  if ((d.services ?? []).length > 0) out.push(`services ${(d.services ?? []).map((s) => s.name).join(', ')}`)
  if ((d.extensions ?? []).length > 0) out.push(`extensions ${(d.extensions ?? []).join(', ')}`)
  return out
}
