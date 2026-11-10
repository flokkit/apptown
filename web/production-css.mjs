// Bundle the real entry so fixtures exercise its dependency traversal and CSS order.
// Packages stay external, except the interface fonts (src/fonts.ts), which are
// bundled with their files inlined so every fixture measures text in IBM Plex
// on every machine, with no font requests to route. Only the woff2 files are
// inlined: every browser the tests run uses them, so the woff fallbacks are
// left empty.
import { build } from 'esbuild'
export async function productionCSS() {
  const bundle = await build({
    absWorkingDir: new URL('.', import.meta.url).pathname,
    entryPoints: ['src/main.tsx'], bundle: true, write: false,
    outdir: 'fixture-output', logLevel: 'silent',
    loader: { '.woff2': 'dataurl', '.woff': 'empty' },
    plugins: [{
      name: 'external-packages-but-fonts',
      setup(b) {
        b.onResolve({ filter: /^[^./]/ }, args => args.path.startsWith('@fontsource/') ? undefined : { path: args.path, external: true })
      },
    }],
  })
  return bundle.outputFiles.find(file => file.path.endsWith('.css')).text
}
