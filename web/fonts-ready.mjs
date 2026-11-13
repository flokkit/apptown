// The interface's faces (src/fonts.ts), loaded and decoded before a test
// measures text, so widths are IBM Plex's on every machine rather than a
// system fallback's. Every face the page's CSS declares is loaded, each
// weight and style, not only the regular ones: a browser loads a face only
// when text first needs it, so a heading or a bold label measured right after
// it is drawn could still be in the fallback face, sooner the busier the
// machine. document.fonts.check answers true when no face is registered at
// all, so it cannot tell; document.fonts.load returns the faces it loaded,
// and none when the CSS declares none.
export async function fontsReady(page) {
  const loaded = await page.evaluate(async () => {
    const fonts = [...new Set([...document.fonts].map(face => `${face.style} ${face.weight} 16px "${face.family}"`))]
    const results = await Promise.all(fonts.map(async font => [font, (await document.fonts.load(font, 'Ag')).filter(face => face.status === 'loaded').length > 0]))
    // A page's other text (another script's subset) may have started loads of its own.
    await document.fonts.ready
    return results
  })
  const families = loaded.map(([font]) => font)
  const missing = ['IBM Plex Sans', 'IBM Plex Mono'].filter(family => !families.some(font => font.endsWith(`"${family}"`)))
  const failed = loaded.filter(([, ok]) => !ok).map(([font]) => font)
  if (missing.length || failed.length) throw new Error(`the interface fonts did not load: ${JSON.stringify({ missing, failed })}`)
}
