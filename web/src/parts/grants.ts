import type { GrantView } from '../wire.gen'

// A grant's subject, in the words people use: a name, or group:name.
export function groupLabel(name: string) { return name === 'all-authenticated-users' ? 'Everyone signed in' : `Team of ${name}` }
export function subjectLabel(g: GrantView) { return g.subjectType === 'group' ? groupLabel(g.subject) : g.subject }
// What a grant gives, in the role's own title.
export function grantTitle(g: GrantView) { return g.roleTitle || g.role || g.capability || '' }
