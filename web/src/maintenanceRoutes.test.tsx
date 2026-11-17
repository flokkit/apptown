// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { createMemoryRouter, RouterProvider, useParams } from 'react-router-dom'
import { expect, it } from 'vitest'
import { ConformanceRedirect } from './pages/Maintenance'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const encoded = 'repo%2Fcontract-matches-code%401%C2%B7contract-matches-code'
function Check() { return <p>{useParams().checkKey}</p> }
it.each(['/conformance', '/conformance/reviews/r1/checks/k?x=1#y', ...['', '/projects'].flatMap(end => [`/conformance/checks/${encoded}${end}`, `/conformance/reviews/r1/checks/${encoded}${end}`])])('redirects the raw address %s', async old => {
  const router = createMemoryRouter([
    { path: '/conformance/*', element: <ConformanceRedirect /> },
    { path: '/maintenance/standards/checks/:checkKey', element: <Check /> },
    { path: '/maintenance/standards/checks/:checkKey/projects', element: <Check /> },
    { path: '/maintenance/standards/reviews/:reviewId/checks/:checkKey', element: <Check /> },
    { path: '/maintenance/standards/reviews/:reviewId/checks/:checkKey/projects', element: <Check /> },
    { path: '/maintenance/standards', element: <p>Standards</p> },
  ], { initialEntries: [old] })
  const el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<RouterProvider router={router} />))
    const l = router.state.location
    expect(l.pathname + l.search + l.hash).toBe(old.replace('/conformance', '/maintenance/standards'))
    if (old.includes(encoded)) expect(el.textContent).toBe(decodeURIComponent(encoded))
  } finally { act(() => root.unmount()); router.dispose() }
})
