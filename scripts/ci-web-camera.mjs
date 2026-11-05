// Serve the web job's built SPA while the browser test owns disposable API state.
import { createServer } from 'node:http';
import { readFile, mkdir } from 'node:fs/promises';
import { resolve, extname } from 'node:path';
import { spawn } from 'node:child_process';

const root = resolve('web/dist');
const index = await readFile(`${root}/index.html`);
const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.woff2': 'font/woff2', '.png': 'image/png', '.ico': 'image/x-icon', '.json': 'application/json' };
const server = createServer(async (req, res) => {
  const path = resolve(root, '.' + new URL(req.url, 'http://localhost').pathname);
  if (!path.startsWith(root + '/') && path !== root) { res.writeHead(403).end(); return; }
  try {
    const data = await readFile(path);
    res.writeHead(200, { 'Content-Type': types[extname(path)] || 'application/octet-stream' }).end(data);
  } catch (err) {
    if (!['ENOENT', 'EISDIR'].includes(err.code)) { res.writeHead(500).end(); return; }
    res.writeHead(200, { 'Content-Type': 'text/html' }).end(index);
  }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const out = `${process.env.TMPDIR}/captures`;
await mkdir(out, { recursive: true });
const child = spawn('go', ['test', '-tags=browser', './internal/apptown/', '-run', '^TestWebUICapture$', '-v', ...process.argv.slice(2)], {
  stdio: 'inherit', env: { ...process.env, APPTOWN_DESIGN_VITE: `http://127.0.0.1:${server.address().port}`, APPTOWN_DESIGN_OUT: out },
});
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
child.on('error', err => { console.error(err); server.close(); process.exitCode = 1; });
child.on('exit', code => { server.close(); process.exitCode = code ?? 1; });
