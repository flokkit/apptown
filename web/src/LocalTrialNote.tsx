import { PRODUCT } from './brand'
// The one-line warning for a forge opened on this computer's own address:
// the desktop trial (and a source-built one) runs everything on this
// machine's Docker. It is read from the browser's own address, so a forge
// reached by a server's name never shows it.
export function onThisComputer(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '[::1]' || hostname === '::1'
}

// The published guide, not /docs: this note shows on the first-account page,
// before anyone can sign in, and the built-in docs need a signed-in session.
export const RUNNING_GUIDE = 'https://github.com/flokkit/apptown/blob/main/stock/docs/guides/running-apptown.md'

export function LocalTrialNote({ hostname = window.location.hostname }: { hostname?: string }) {
  if (!onThisComputer(hostname)) return null
  return (
    <div className="small muted">
      For trying {PRODUCT} on your own computer: it has full control of your Docker, and the
      agents and apps it runs are containers on this machine. For a server or anything other
      people use, follow <a href={RUNNING_GUIDE}>Running {PRODUCT}</a>.
    </div>
  )
}

// The setup token handed over in the address (the desktop trial prints
// http://localhost:7777/?setup=<token>), taken out of the address so it is
// not kept in the page's URL.
export function takeSetupToken(): string {
  const url = new URL(window.location.href)
  const token = url.searchParams.get('setup') ?? ''
  if (token) {
    url.searchParams.delete('setup')
    window.history.replaceState(window.history.state, '', url.pathname + url.search + url.hash)
  }
  return token
}
