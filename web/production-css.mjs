// Bundle the real entry so fixtures exercise its dependency traversal and CSS order.
import { build } from 'esbuild'
export async function productionCSS() {
  const bundle = await build({
    absWorkingDir: new URL('.', import.meta.url).pathname,
    entryPoints: ['src/main.tsx'], bundle: true, write: false,
    outdir: 'fixture-output', packages: 'external', logLevel: 'silent',
  })
  return bundle.outputFiles.find(file => file.path.endsWith('.css')).text
}
