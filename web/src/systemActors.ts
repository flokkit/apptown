import { PRODUCT } from './brand'

// AppTown's own acts are recorded under its system names (the engine,
// triggers, bindings and commit author, under the names from before and
// after the AppTown rename). History shows them as the product; a person's
// name and a run's identity read as recorded. The server's audit API does the
// same (identity.ShowActorsIn); this is for pages that print a raw actor.
const OWN_NAMES = new Set(['system', 'apptown-system', 'apptown', 'forge-system', 'forge'])

export function shownActor(name: string): string {
  return OWN_NAMES.has(name) ? PRODUCT : name
}
