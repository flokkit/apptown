import type { DeploymentView } from './wire.gen'

// Preview keys contain Change IDs. Keep the key for addresses and label edits;
// an unnamed preview has a literal name in inventories and page headings.
export function deploymentName(e: Pick<DeploymentView, 'name' | 'displayLabel' | 'kind'>) {
  return e.kind === 'preview' && (!e.displayLabel || e.displayLabel === e.name) ? 'Preview' : e.displayLabel || e.name
}
