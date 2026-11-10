// Serve the web job's built SPA while the browser test owns disposable API state.
// Each run gets its own folder, made fresh under TMPDIR and printed first and
// last: the scene's data folder, its temporary files and the captures
// (<run>/captures) live in it, so two runs never share or remove each other's
// files. It stays under TMPDIR because the heavy runner shares only that root
// with the Docker host, which bind-mounts paths beneath it.
import { createServer } from 'node:http';
import { readFile, mkdir, mkdtemp } from 'node:fs/promises';
import { resolve, extname, join } from 'node:path';
import { spawn } from 'node:child_process';

// The scene's data folder (<run>/c-<digits>) holds SSH connection-sharing
// sockets, which macOS limits to 104 bytes including OpenSSH's suffix; a run
// folder of more than 40 characters leaves too little room.
const parent = (process.env.TMPDIR || '/tmp').replace(/\/+$/, '');
if (parent.length + '/cam-XXXXXX'.length > 40) {
  console.error(`camera: TMPDIR (${parent}) is too long for the scene's SSH sockets; run with TMPDIR=/tmp`);
  process.exit(2);
}
const run = await mkdtemp(join(parent, 'cam-'));
console.log(`camera: run folder ${run}`);

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
const out = `${run}/captures`;
await mkdir(out, { recursive: true });
const child = spawn('go', ['test', '-tags=browser', './internal/apptown/', '-run', '^TestWebUICapture$', '-v', ...process.argv.slice(2)], {
  stdio: 'inherit', env: { ...process.env, TMPDIR: run, APPTOWN_DESIGN_VITE: `http://127.0.0.1:${server.address().port}`, APPTOWN_DESIGN_OUT: out },
});
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
child.on('error', err => { console.error(err); server.close(); process.exitCode = 1; });
child.on('exit', code => { server.close(); console.log(`camera: captures in ${out}`); process.exitCode = code ?? 1; });
