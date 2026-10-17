import { createStore } from './hooks'

// Which project a page is INSIDE, for pages whose URL doesn't say (a change,
// a decision). The sidebar reads it so leaving a project's overview for one
// of its changes doesn't bounce the menu back to the instance.
const scope = createStore<string | null>(null)
export const setProjectScope = scope.set
export const useProjectScope = scope.use
