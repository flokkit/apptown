import type { Feature } from './wire.gen'

// Page fixtures exercise areas that their administrator enabled.
export const enabledFeatures: Feature[] = ['projects', 'changes', 'landing', 'releases', 'deployments', 'people', 'record', 'setup', 'conformance', 'backups', 'ways', 'chats', 'usage', 'worker', 'previews', 'models', 'updates', 'complaints', 'evals', 'what-ifs', 'restore-rehearsals', 'conformance-estate', 'app-platform', 'portainer', 'bucket-site', 'mcp', 'memory'].map(area => ({ area, name: area, tier: 'standard', enabled: true, covers: area, docs: '/docs/guides/features' }))
export function withFeatures(handler: (url: string, init?: RequestInit) => Promise<Response>) {
  return (url: string, init?: RequestInit) => url === '/api/v1/features' ? Promise.resolve(new Response(JSON.stringify(enabledFeatures))) : handler(url, init)
}
