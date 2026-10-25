import { get, post, upload } from '../../api'
import type { Project as WireProject, ProjectManifestView, ProjectSecrets, ProjectServiceList, RemoteAttached } from '../../wire.gen'
import { slugOf, type Draft } from './draft'

// What applying a pack answers (not yet generated from its Go type).
type PackApplied = { guidanceImports: number; workflowsInstalled: string[]; missing: string[]; seeded?: string[] }

// What creating a project did, line by line: what was wired (ok) and what
// was skipped (warn).
export type ProgressLine = { text: string; warn?: boolean }

// createProject runs the whole assembly from a finished draft, reporting
// each step as it goes, and answers with the manifest: the receipt of what
// was wired. A step the project can live without (a GitLab copy, the
// services) is a warning; one it cannot (the project, its repository, its
// way of working) stops here with an error.
export async function createProject(d: Draft, opts: { remote: boolean; namespaceId: string }, say: (line: ProgressLine) => void): Promise<ProjectManifestView> {
  const slug = slugOf(d)
  const log = (text: string) => say({ text })
  const warn = (text: string) => say({ text, warn: true })
  await post('/projects', { slug, name: d.name || slug, blurb: d.description.trim(), icon: d.icon })
  log(`✓ project ${slug} created`)
  if (d.origin === 'empty') {
    await post(`/projects/${slug}/repo`, { defaultBranch: 'main' })
    log('✓ git repository initialized')
  } else {
    try {
      const branch = d.sourceBranch.trim()
      const p = d.origin === 'bundle'
        ? await upload<WireProject>(`/projects/${slug}/repo/from-bundle?branch=${encodeURIComponent(branch)}`, d.bundleFile!)
        : await post<WireProject>(`/projects/${slug}/repo/from-url`, { url: d.sourceURL.trim(), branch, credentialSecret: d.gitSecret })
      log(`✓ repository started from ${d.origin === 'bundle' ? d.bundleFile!.name : d.sourceURL.trim()} on ${p.defaultBranch}`)
    } catch (e) {
      throw new Error(`${slug} was created, but its code could not be brought in: ${(e as Error).message}. Delete it in its settings and try again.`)
    }
    try {
      const sec = await get<ProjectSecrets>(`/projects/${slug}/secrets`)
      const toSet = (sec.secrets ?? []).filter((x) => !x.generate).map((x) => x.name)
      if (toSet.length) warn(`⚠ the code declares secrets to set in Settings › Secrets: ${toSet.join(', ')}`)
    } catch (e) { warn(`⚠ the declared secrets could not be read: ${(e as Error).message}`) }
  }
  if (opts.remote) {
    try {
      const r = await post<RemoteAttached>(`/projects/${slug}/repo/remote`, { requestId: d.attachmentRequestId, repositoryName: d.repositoryName ?? slug, create: true, namespaceId: Number(opts.namespaceId) || 0 })
      log(`✓ GitLab copy: ${r.remote}`)
    } catch (e) { warn(`⚠ GitLab copy failed (the project was created): ${(e as Error).message}`) }
  }
  if (d.pack) {
    const r = await post<PackApplied>(`/projects/${slug}/pack`, { name: d.pack })
    log(`✓ way of working "${d.pack}": ${r.guidanceImports} guidance blocks, workflows: ${r.workflowsInstalled.join(', ')}`)
    if (r.seeded?.length) log(`✓ starter files: ${r.seeded.join(', ')}`)
    if (r.missing?.length) warn(`⚠ missing from store: ${r.missing.join(', ')}`)
  }
  if (d.services.length) {
    const names = d.services.map((n) => n.capability).join(', ')
    try {
      // The way of working's starter may already declare exactly these; the
      // server then refuses an edit with nothing to change, which is not a
      // skip: the services are there.
      if (await alreadyDeclared(slug, d.services)) {
        log(`✓ services: ${names} (declared by the starter)`)
      } else {
        await post(`/projects/${slug}/services`, { needs: d.services, direct: true })
        log(`✓ services: ${names}`)
      }
    } catch (e) { warn(`⚠ services not added (add them later in the project's settings): ${(e as Error).message}`) }
  }
  return get<ProjectManifestView>(`/projects/${slug}/manifest`)
}

// alreadyDeclared: the project's app.yaml declares exactly the picked
// services, with no parameter the pick sets differently. A declaration that
// cannot be read is not known to hold them: the services are then added.
export async function alreadyDeclared(slug: string, picks: Draft['services']): Promise<boolean> {
  let declared: ProjectServiceList['needs']
  try { declared = (await get<ProjectServiceList>(`/projects/${slug}/services`)).needs } catch { return false }
  if (declared.length !== picks.length) return false
  return picks.every((p) => {
    const d = declared.find((n) => n.capability === p.capability)
    return !!d && Object.entries(p.params ?? {}).every(([k, v]) => String(d.params?.[k]) === String(v))
  })
}
