// The interface's faces (src/fonts.ts), loaded and decoded before a test
// measures text, so widths are IBM Plex's on every machine rather than a
// system fallback's. document.fonts.check answers true when no face is
// registered at all, so it cannot tell; document.fonts.load returns the
// faces it loaded, and none when the CSS declares none.
export async function fontsReady(page) {
  const loaded = await page.evaluate(async () => (await Promise.all(['400 16px "IBM Plex Sans"', '400 16px "IBM Plex Mono"']
    .map(font => document.fonts.load(font, 'Ag')))).map(faces => faces.length > 0 && faces.every(face => face.status === 'loaded')))
  if (!loaded.every(Boolean)) throw new Error(`the interface fonts did not load (IBM Plex Sans, Mono): ${JSON.stringify(loaded)}`)
}
