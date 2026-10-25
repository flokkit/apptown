import type { Draft } from './draft'

// This is the wizard's submitted input and navigation checkpoint, not an
// execution receipt. Owner reads still decide what actually happened.
export type SetupStep = 'project' | 'rejected' | 'canonical' | 'repository' | 'upstream' | 'pack' | 'services' | 'services-read' | 'manifest' | 'first-version'
export type RetainedCreation = {
  version: 1
  actorId: string
  draft: Omit<Draft, 'bundleFile'>
  destination: { remote: boolean; namespaceId: string }
  bundleDigest: string | null
  project: { id: string; slug: string } | null
  pending: SetupStep | null
}

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const validID = (value: unknown): value is string => typeof value === 'string' && uuid.test(value) && value !== '00000000-0000-0000-0000-000000000000'
const record = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value)
const stringMap = (value: unknown) => record(value) && Object.values(value).every((v) => typeof v === 'string')
const steps: SetupStep[] = ['project', 'rejected', 'canonical', 'repository', 'upstream', 'pack', 'services', 'services-read', 'manifest', 'first-version']
function key(actorId: string): string {
  if (!validID(actorId)) throw new Error('Read your current account before continuing project setup.')
  return `apptown.project-creation.v1.${actorId}`
}

function validDraft(value: unknown): value is RetainedCreation['draft'] {
  if (!record(value)) return false
  const strings = ['name', 'description', 'packWhy', 'icon', 'namespaceId', 'sourceURL', 'sourceBranch', 'gitSecret']
  return strings.every((field) => typeof value[field] === 'string')
    && ['nameEdited', 'choosing', 'servicesEdited'].every((field) => typeof value[field] === 'boolean')
    && [1, 2, 3].includes(Number(value.step)) && typeof value.step === 'number'
    && typeof value.interviewRevision === 'number' && Number.isSafeInteger(value.interviewRevision) && value.interviewRevision >= 0
    && validID(value.creationRequestId) && validID(value.attachmentRequestId) && value.creationRequestId !== value.attachmentRequestId
    && ['address', 'pack', 'repositoryName'].every((field) => value[field] === null || typeof value[field] === 'string')
    && (value.remoteChoice === null || typeof value.remoteChoice === 'boolean')
    && ['empty', 'url', 'bundle'].includes(String(value.origin))
    && Array.isArray(value.questions) && value.questions.every((v) => typeof v === 'string') && stringMap(value.answers)
    && Array.isArray(value.services) && value.services.every((v) => record(v) && typeof v.capability === 'string' && (v.params === undefined || stringMap(v.params)))
}
function valid(value: unknown): value is RetainedCreation {
  if (!record(value) || value.version !== 1 || !validID(value.actorId) || !validDraft(value.draft) || !record(value.destination)) return false
  const bundle = value.draft.origin === 'bundle'
  return typeof value.destination.remote === 'boolean' && typeof value.destination.namespaceId === 'string'
    && (bundle ? typeof value.bundleDigest === 'string' && /^[0-9a-f]{64}$/.test(value.bundleDigest) : value.bundleDigest === null)
    && (value.project === null || (record(value.project) && validID(value.project.id) && typeof value.project.slug === 'string' && value.project.slug !== ''))
    && (value.pending === null || steps.includes(value.pending as SetupStep))
}

export function readCreation(storage: Storage, actorId: string): RetainedCreation | null {
  const raw = storage.getItem(key(actorId))
  if (raw === null) return null
  let value: unknown
  try { value = JSON.parse(raw) } catch { throw new Error('The saved project setup could not be read. It has been kept for recovery.') }
  if (!valid(value) || value.actorId !== actorId) throw new Error('The saved project setup does not match this account or its expected format. It has been kept for recovery.')
  return value
}

function input(value: RetainedCreation): string {
  return JSON.stringify([value.actorId, value.draft, value.destination, value.bundleDigest])
}

// Storage must succeed before the caller sends a consequential request. A
// checkpoint may record progress but cannot substitute another submitted intent.
export function retainCreation(storage: Storage, value: RetainedCreation): void {
  if (!valid(value)) throw new Error('The project setup is incomplete and cannot be retained.')
  const previous = readCreation(storage, value.actorId)
  if (previous && input(previous) !== input(value)) throw new Error('Resolve the saved project setup before starting a different request.')
  if (previous?.project && (!value.project || previous.project.id !== value.project.id)) throw new Error('The saved project identity cannot be replaced.')
  storage.setItem(key(value.actorId), JSON.stringify(value))
}

export async function bundleDigest(file: File): Promise<string> {
  const bytes = await file.arrayBuffer()
  const digest = await crypto.subtle.digest('SHA-256', bytes)
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
}

export async function captureCreation(actorId: string, draft: Draft, destination: RetainedCreation['destination']): Promise<RetainedCreation> {
  key(actorId)
  if (draft.origin === 'bundle' && !draft.bundleFile) throw new Error('Choose the Git bundle before creating the project.')
  if (draft.origin === 'url') {
    let source: URL
    try { source = new URL(draft.sourceURL) } catch { throw new Error('Enter a complete repository URL before saving project setup.') }
    if (source.username || source.password) throw new Error('Remove credentials from the repository URL; select a named credential instead.')
  }
  const { bundleFile, ...fields } = draft
  // Only the selected origin is submitted; abandoned source fields can
  // contain pasted credentials and must not enter retained setup state.
  if (draft.origin !== 'url') { fields.sourceURL = ''; fields.gitSecret = '' }
  if (draft.origin === 'empty') fields.sourceBranch = ''
  // Detach nested service/answer objects so later form changes cannot change
  // the submitted choices while hashing or sending is underway.
  const saved = JSON.parse(JSON.stringify(fields)) as RetainedCreation['draft']
  const where = { ...destination }
  const digest = draft.origin === 'bundle' ? await bundleDigest(bundleFile!) : null
  return { version: 1, actorId, draft: saved, destination: where, bundleDigest: digest, project: null, pending: null }
}

export async function matchingBundle(value: RetainedCreation, file: File): Promise<void> {
  if (value.draft.origin !== 'bundle' || await bundleDigest(file) !== value.bundleDigest) throw new Error('This bundle does not match the file selected for the saved project setup.')
}


// Starting a different intent is explicit, and never resolves an unknown
// local creation by forgetting its request ID.
export function leaveCreation(storage: Storage, actorId: string): void {
  const saved = readCreation(storage, actorId)
  if (saved && !saved.project && saved.pending !== 'rejected' && saved.pending !== null) throw new Error('Check the original creation request before starting a different project.')
  storage.removeItem(key(actorId))
}
