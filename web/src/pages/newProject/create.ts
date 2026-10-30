import { get, post, upload, HttpError } from '../../api'
import type { AuthzHoldings, CreatedChange, Project as WireProject, ProjectDetail, ProjectDraft, ProjectManifestView, ProjectSecrets, ProjectServiceList, RemoteAttached } from '../../wire.gen'
import { slugOf, type Draft } from './draft'
import { captureCreation, matchingBundle, readCreation, retainCreation, leaveCreation, type RetainedCreation, type SetupStep } from './retained'

// What applying packs answers (not yet generated from its Go type).
type PackApplied = { packs: string[]; guidanceImports: number; workflowsInstalled: string[]; missing: string[]; seeded?: string[] }
export type ProgressLine = { text: string; warn?: boolean }
export type CreationContext = { actorId: string; storage: Storage; file?: File | null; assertActive: () => void }

export function creationRequest(saved: RetainedCreation): ProjectDraft {
  const d = { ...saved.draft, bundleFile: null }
  const remote = saved.destination.remote
  const namespace = remote ? Number(saved.destination.namespaceId) : 0
  if (remote && (!Number.isSafeInteger(namespace) || namespace <= 0)) throw new Error('Choose a GitLab group before creating the project.')
  return {
    requestId: d.creationRequestId, automaticAddress: d.address === null,
    slug: slugOf(d), name: d.name || slugOf(d), namespace: '', group: '', blurb: d.description.trim(), icon: d.icon, packs: d.packs ?? [],
    setup: {
      origin: d.origin, sourceURL: d.sourceURL.trim(), sourceBranch: d.sourceBranch.trim(), credentialSecret: d.gitSecret,
      bundleSHA256: saved.bundleDigest ?? '', services: d.services.map((s) => ({ capability: s.capability, params: { ...s.params } })),
      upstream: { enabled: remote, namespaceId: namespace, repositoryName: remote ? d.repositoryName ?? slugOf(d) : '', attachmentRequestId: d.attachmentRequestId },
    },
  }
}

async function currentActor(ctx: CreationContext): Promise<void> {
  ctx.assertActive()
  const who = await get<AuthzHoldings>('/authz/me')
  ctx.assertActive()
  if (who.actorId !== ctx.actorId) throw new Error('Your signed-in account changed. Open project setup again under the original account to continue.')
}

export async function createProject(d: Draft, destination: RetainedCreation['destination'], say: (line: ProgressLine) => void, ctx: CreationContext): Promise<ProjectManifestView> {
  await currentActor(ctx)
  const saved = await captureCreation(ctx.actorId, d, destination)
  ctx.assertActive()
  creationRequest(saved) // Validate resolved choices before retaining or sending them.
  retainCreation(ctx.storage, saved)
  return continueCreation({ ...ctx, file: d.bundleFile }, say)
}

// Only local creation has an equal-input replay contract. A dispatched
// external step stays unresolved until its owner can establish its outcome.
export async function continueCreation(ctx: CreationContext, say: (line: ProgressLine) => void): Promise<ProjectManifestView> {
  await currentActor(ctx)
  let saved = readCreation(ctx.storage, ctx.actorId)
  if (!saved) throw new Error('No submitted project setup is saved for this account.')
  const checkpoint = (pending: SetupStep | null) => {
    ctx.assertActive()
    saved = { ...saved!, pending }
    retainCreation(ctx.storage, saved)
  }
  if (saved.pending === 'rejected') throw new Error('The original creation was refused. Edit the saved choices to start a new request.')
  const request = creationRequest(saved)
  const setup = request.setup!
  const log = (text: string) => say({ text })
  const warn = (text: string) => say({ text, warn: true })
  if (!saved.project) {
    if (saved.pending !== null && saved.pending !== 'project') throw new Error('The saved project setup has no confirmed project identity. Keep it for recovery.')
    const firstAttempt = saved.pending === null
    checkpoint('project')
    let project: WireProject
    try { project = await post<WireProject>('/projects', request) } catch (e) {
      ctx.assertActive()
      // A refusal of an initial request confirms no creation. A refusal
      // during replay does not resolve an earlier unknown outcome.
      if (firstAttempt && e instanceof HttpError && [400, 409, 422].includes(e.status)) checkpoint('rejected')
      throw e
    }
    ctx.assertActive()
    if (!project.id || !project.slug) throw new Error('Creation returned no project identity. Resume the same saved request to check its outcome.')
    saved = { ...saved, project: { id: project.id, slug: project.slug }, pending: 'canonical' }
    retainCreation(ctx.storage, saved)
    log(`Project ${project.slug} created`)
  }
  const current = await get<ProjectDetail>(`/projects/${encodeURIComponent(saved.project!.slug)}`)
  ctx.assertActive()
  if (current.id !== saved.project!.id || !current.slug || current.lifecycle?.state !== 'active') throw new Error('The saved project could not be resolved as the same active project. Setup has stopped.')
  saved = { ...saved, project: { id: current.id, slug: current.slug } }
  retainCreation(ctx.storage, saved)
  const slug = encodeURIComponent(current.slug)
  if (saved.pending === 'services-read' || saved.pending === 'services') {
    const matches = await alreadyDeclared(current.slug, setup.services)
    ctx.assertActive()
    if (!matches) throw new Error('The current services differ from the saved setup. Review them in project settings; recovery will not overwrite them.')
    ctx.assertActive()
    checkpoint('manifest')
  }
  if (saved.pending !== 'canonical' && saved.pending !== 'manifest' && saved.pending !== null) {
    throw new Error(`Project ${current.slug} exists; ${saved.pending} setup needs checking. Open the project to resolve this step. It will not be repeated automatically.`)
  }
  // Finished setup and a lost manifest response need only a fresh owner read.
  if (saved.pending === null || saved.pending === 'manifest') {
    const manifest = await get<ProjectManifestView>(`/projects/${slug}/manifest`)
    ctx.assertActive()
    checkpoint(null)
    return manifest
  }
  if (setup.origin === 'bundle') {
    if (!ctx.file) throw new Error('Reselect the original Git bundle to continue the saved project setup.')
    await matchingBundle(saved, ctx.file)
  }
  await currentActor(ctx)
  checkpoint('repository')
  if (setup.origin === 'empty') {
    await post(`/projects/${slug}/repo`, { defaultBranch: 'main' })
    ctx.assertActive()
    log('Git repository initialized')
  } else {
    const p = setup.origin === 'bundle'
      ? await upload<WireProject>(`/projects/${slug}/repo/from-bundle?branch=${encodeURIComponent(setup.sourceBranch)}`, ctx.file!)
      : await post<WireProject>(`/projects/${slug}/repo/from-url`, { url: setup.sourceURL, branch: setup.sourceBranch, credentialSecret: setup.credentialSecret })
    ctx.assertActive()
    log(`Repository started on ${p.defaultBranch}`)
    try {
      const sec = await get<ProjectSecrets>(`/projects/${slug}/secrets`)
      ctx.assertActive()
      const names = (sec.secrets ?? []).filter((x) => !x.generate).map((x) => x.name)
      if (names.length) warn(`The code declares secrets to set in Settings › Secrets: ${names.join(', ')}`)
    } catch (e) { ctx.assertActive(); warn(`The declared secrets could not be read: ${(e as Error).message}`) }
  }
  if (setup.upstream.enabled) {
    await currentActor(ctx)
    checkpoint('upstream')
    const r = await post<RemoteAttached>(`/projects/${slug}/repo/remote`, { requestId: setup.upstream.attachmentRequestId, repositoryName: setup.upstream.repositoryName, create: true, namespaceId: setup.upstream.namespaceId })
    ctx.assertActive()
    log(`GitLab copy: ${r.remote}`)
  }
  if (request.packs?.length) {
    await currentActor(ctx)
    checkpoint('pack')
    const r = await post<PackApplied>(`/projects/${slug}/pack`, { names: request.packs })
    ctx.assertActive()
    log(`Way of working ${request.packs.join(' + ')}: ${r.guidanceImports} guidance blocks, workflows: ${r.workflowsInstalled.join(', ')}`)
    if (r.seeded?.length) log(`Starter files: ${r.seeded.join(', ')}`)
    if (r.missing?.length) warn(`Missing from store: ${r.missing.join(', ')}`)
  }
  if (setup.services.length) {
    await currentActor(ctx)
    // A failed read never supplies permission for a write. This is a fresh
    // active flow; uncertain dispatched writes are refused above on resume.
    checkpoint('services-read')
    const matches = await alreadyDeclared(current.slug, setup.services)
    ctx.assertActive()
    if (matches) {
      log(`Services: ${setup.services.map((n) => n.capability).join(', ')} (declared by the starter)`)
    } else {
      await currentActor(ctx)
      checkpoint('services')
      await post(`/projects/${slug}/services`, { needs: setup.services, direct: true })
      ctx.assertActive()
      log(`Services: ${setup.services.map((n) => n.capability).join(', ')}`)
    }
  }
  checkpoint('manifest')
  const manifest = await get<ProjectManifestView>(`/projects/${slug}/manifest`)
  ctx.assertActive()
  checkpoint(null)
  return manifest
}

// alreadyDeclared: the project's app.yaml declares exactly the picked
// services, with no parameter the pick sets differently. A declaration that
// cannot be read supplies no basis for changing it.
export async function alreadyDeclared(slug: string, picks: Draft['services']): Promise<boolean> {
  const declared = (await get<ProjectServiceList>(`/projects/${slug}/services`)).needs
  if (declared.length !== picks.length) return false
  return picks.every((p) => {
    const d = declared.find((n) => n.capability === p.capability)
    return !!d && Object.entries(p.params ?? {}).every(([k, v]) => String(d.params?.[k]) === String(v))
  })
}


export async function leaveSubmittedCreation(ctx: CreationContext): Promise<void> {
  await currentActor(ctx)
  leaveCreation(ctx.storage, ctx.actorId)
}

export async function buildFirstVersion(ctx: CreationContext): Promise<{ slug: string; change: CreatedChange }> {
  await currentActor(ctx)
  const saved = readCreation(ctx.storage, ctx.actorId)
  if (!saved?.project || saved.pending !== null) throw new Error('Check the saved setup or the project’s changes before requesting another first version.')
  const project = await get<ProjectDetail>(`/projects/${encodeURIComponent(saved.project.slug)}`)
  ctx.assertActive()
  if (project.id !== saved.project.id || project.lifecycle?.state !== 'active') throw new Error('The same active project could not be resolved.')
  await currentActor(ctx)
  retainCreation(ctx.storage, { ...saved, project: { id: project.id, slug: project.slug }, pending: 'first-version' })
  const change = await post<CreatedChange>(`/projects/${encodeURIComponent(project.slug)}/changes`, {
    title: 'First version', type: 'feature', body: saved.draft.description.trim(), workflow: '',
  })
  ctx.assertActive()
  return { slug: project.slug, change }
}
