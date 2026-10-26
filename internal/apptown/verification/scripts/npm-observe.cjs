// Owner-controlled frozen installation. Never evaluate project scripts or config.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const cp = require('node:child_process');
const root = '/work';
const hash = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const env = {PATH: '/usr/local/bin:/usr/bin:/bin', HOME: '/tmp/assessment-home', LANG: 'C.UTF-8'};
fs.mkdirSync(env.HOME, {recursive: true});
fs.writeFileSync(env.HOME + "/user.npmrc", "");
fs.writeFileSync(env.HOME + "/global.npmrc", "");
function run(args, cwd) {
  const answer = cp.spawnSync('npm', args, {cwd, env, encoding: 'utf8', timeout: 180000, maxBuffer: 1048576});
  if (answer.error || answer.signal || answer.status !== 0) throw new Error(`npm did not complete normally: ${answer.error || answer.signal || answer.stderr}`);
  return answer.stdout.trim();
}
function regular(file) {
  const st = fs.lstatSync(file);
  if (!st.isFile() || st.size > 1048576) throw new Error('package observation is not a bounded regular file');
  return fs.readFileSync(file);
}
function observe(side) {
  const cwd = path.join(root, side);
  const input = ['package.json', 'package-lock.json'].map(name => hash(regular(path.join(cwd, name))));
  run(['ci', '--ignore-scripts', '--audit=false', '--fund=false', '--include=dev', '--include=optional', '--include=peer', '--userconfig=/tmp/assessment-home/user.npmrc', '--globalconfig=/tmp/assessment-home/global.npmrc', '--registry=https://registry.npmjs.org', `--cache=/tmp/npm-${side}`], cwd);
  const installed = {};
  function packageAt(folder) {
    if (!fs.lstatSync(folder).isDirectory()) throw new Error('installed package is not an owned directory');
    const key = path.relative(cwd, folder).split(path.sep).join('/');
    if (Object.keys(installed).length >= 10000) throw new Error('package inventory exceeds observation bound');
    installed[key] = regular(path.join(folder, 'package.json')).toString('utf8');
    modules(path.join(folder, 'node_modules'));
  }
  function modules(folder) {
    let entries;
    try { entries = fs.readdirSync(folder, {withFileTypes: true}); } catch (error) { if (error.code === 'ENOENT') return; throw error; }
    if (!fs.lstatSync(folder).isDirectory()) throw new Error('node_modules is not an owned directory');
    for (const entry of entries) {
      if (entry.name === '.bin' || entry.name === '.package-lock.json') continue;
      if (entry.name.startsWith('.')) throw new Error('unexpected hidden installation entry');
      const full = path.join(folder, entry.name);
      if (entry.name.startsWith('@')) {
        if (!entry.isDirectory()) throw new Error('package scope is not an owned directory');
        for (const scoped of fs.readdirSync(full)) packageAt(path.join(full, scoped));
      } else packageAt(full);
    }
  }
  modules(path.join(cwd, 'node_modules'));
  const output = ['package.json', 'package-lock.json'].map(name => hash(regular(path.join(cwd, name))));
  if (input.some((digest, i) => digest !== output[i])) throw new Error('npm changed the committed input');
  return {input, output, installed};
}
const result = {node: process.version, npm: run(['--version'], root), platform: `${process.platform}/${process.arch}`, before: observe('before'), after: observe('after')};
fs.writeFileSync(path.join(root, 'observation.json'), JSON.stringify(result));
