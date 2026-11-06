import { expect, it } from 'vitest'
import appRoutes from './App.tsx?raw'

it('removes the Janitor routes and component', () => {
 expect(appRoutes).not.toContain('/janitor')
 expect(import.meta.glob('./pages/Janitor.tsx')).toEqual({})
})
