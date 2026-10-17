// precompress writes name.br and name.gz beside each text file in the build
// output. forged embeds web/dist and serves the smallest variant the browser
// accepts (web/static.go), so compression holds behind any proxy and costs
// nothing per request. Already-compressed formats (fonts, images) are left
// alone, and a variant is written only when it is meaningfully smaller.
//
// Plain JavaScript because the web project has no Node type declarations;
// precompress.d.ts gives vite.config.ts its type.
import { promisify } from 'node:util'
import { brotliCompress, gzip, constants } from 'node:zlib'
import { readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'

const text = /\.(js|mjs|css|html|svg|json|txt|map)$/
const br = promisify(brotliCompress)
const gz = promisify(gzip)

export default function precompress() {
  let outDir = 'dist'
  return {
    name: 'forge-precompress',
    apply: 'build',
    configResolved(config) { outDir = config.build.outDir },
    async writeBundle(_options, bundle) {
      await Promise.all(Object.keys(bundle).filter((f) => text.test(f)).map(async (f) => {
        const path = join(outDir, f)
        const raw = await readFile(path)
        if (raw.length < 1024) return
        const variants = [
          ['.br', await br(raw, { params: { [constants.BROTLI_PARAM_QUALITY]: 11, [constants.BROTLI_PARAM_SIZE_HINT]: raw.length } })],
          ['.gz', await gz(raw, { level: 9 })],
        ]
        await Promise.all(variants.filter(([, b]) => b.length < raw.length * 0.9).map(([ext, b]) => writeFile(path + ext, b)))
      }))
    },
  }
}
