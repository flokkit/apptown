// Exercise the pinned Renovate implementation, without network access or a repo write.
// Usage: node scripts/renovate-test.mjs /path/to/node_modules/renovate
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { execFileSync } from 'node:child_process';
const renovate = path.resolve(process.argv[2]);
const require = createRequire(path.join(renovate, 'package.json'));
assert.equal(require('./package.json').version, '44.149.0');
const JSON5 = require('json5');
const { GlobalConfig } = await import(path.join(renovate, 'dist/config/global.js'));
const { extractPackageFile } = await import(path.join(renovate, 'dist/modules/manager/index.js'));
const { extractPackageFile: extractNpm } = await import(path.join(renovate, 'dist/modules/manager/npm/extract/index.js'));
const { doAutoReplace } = await import(path.join(renovate, 'dist/workers/repository/update/branch/auto-replace.js'));
const { applyPackageRules } = await import(path.join(renovate, 'dist/util/package-rules/index.js'));
const root = process.cwd();
const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'apptown-renovate-'));
GlobalConfig.set({ localDir: temp, cacheDir: path.join(temp, 'cache') });
const config = JSON5.parse(await fs.readFile('renovate.json5', 'utf8'));
const files = execFileSync('git', ['ls-files'], {encoding: 'utf8'}).trim().split('\n');
const records = [];
const content = new Map();
async function extract(manager, file, options = {}) {
  const text = content.get(file) ?? await fs.readFile(file, 'utf8');
  content.set(file, text);
  const result = manager === 'npm' ? await extractNpm(text, file, options) : await extractPackageFile(manager, text, file, options);
  for (const [depIndex, dep] of (result?.deps ?? []).entries()) {
    records.push(await applyPackageRules({...options, ...result, ...dep, depIndex,
      manager, packageFile: file, packageName: dep.packageName ?? dep.depName,
      packageRules: config.packageRules}, 'test'));
  }
}
try {
  for (const cm of config.customManagers) {
    for (const file of files.filter(f => cm.managerFilePatterns.some(p => new RegExp(p.slice(1, -1)).test(f)))) {
      await extract('regex', file, cm);
    }
  }
  for (const file of files.filter(f => /^stock\/toolchains\/.*\.toml$/.test(f))) await extract('mise', file);
  for (const file of files.filter(f => /Dockerfile$/.test(f) && !f.includes('testdata') && !f.startsWith('eval/'))) await extract('dockerfile', file);
  for (const file of files.filter(f => /^\.github\/workflows\/.*\.yml$/.test(f))) await extract('github-actions', file);
  for (const file of files.filter(f => /(^|\/)(go.mod|package.json|composer.json)$/.test(f) && !f.includes('testdata') && !f.startsWith('eval/'))) {
    await extract(file.endsWith('go.mod') ? 'gomod' : file.endsWith('composer.json') ? 'composer' : 'npm', file);
  }
  const live = records.filter(d => d.currentValue && (!d.skipReason || d.enabled === false));
  function deps(file, name) { return live.filter(d => d.packageFile === file && (!name || d.depName === name)); }
  function found(file, value) { assert(live.some(d => d.packageFile === file && d.currentValue === value), `${file}: missing ${value}`); }
  for (const manager of ['gomod', 'npm', 'composer', 'github-actions', 'dockerfile', 'mise', 'regex']) assert(live.some(d => d.manager === manager), manager);
  const agent = 'deploy/apptown-agent/Dockerfile';
  for (const [, name, version] of content.get(agent).matchAll(/^ARG (\w+)_VERSION=(\S+)/gm)) {
    if (name !== 'GLAB') found(agent, version);
  }
  for (const [, ref] of content.get('stock/scanners.yaml').matchAll(/^    image: (\S+)/gm)) {
    const [name, value] = ref.split(':');
    assert(deps('stock/scanners.yaml', name).some(d => d.currentValue === value), `scanner ${ref}`);
  }
  for (const [, version] of content.get('Makefile').matchAll(/^\w+_VERSION := (\S+)/gm)) found('Makefile', version);
  for (const file of files.filter(f => /^stock\/toolchains\/.*\.toml$/.test(f))) assert(deps(file).length, `${file}: no supported tool`);
  for (const d of deps(agent).filter(d => d.datasource !== 'docker')) {
    const central = deps('deploy/tool-pins.txt', d.depName);
    assert(central.length && central.every(p => p.groupName === d.groupName), `agent owner ${d.depName}`);
  }
  for (const d of deps('stock/tool-versions.yaml')) assert(/^(stock |runtime )/.test(d.groupName), `stock owner ${d.depName}`);
  assert.equal(deps(agent).filter(d => d.datasource === 'github-release-attachments').length, 10);
  assert.equal(deps('.github/workflows/ci.yml', 'gitleaks/gitleaks').length, 2);
  const catalog = 'stock/tool-versions.yaml';
  const dockerHashes = content.get(agent).match(/^ARG DOCKER_SHA256_.*$/gm);
  const runtimes = [
    ['alpine/git', 'runtime Alpine Git', 'v2.47.3'],
    ['busybox', 'runtime BusyBox', '1.37.1'],
    ['caddy', 'runtime Caddy', '2.10.3'],
    ['docker', 'runtime private Docker 27', '27.5.2'],
    ['postgres', 'stock Postgres 16', '16.16'],
    ['mariadb', 'stock MariaDB 11.4', '11.4.14'],
  ];
  for (const [name, group] of runtimes) {
    const refs = deps(catalog, name);
    assert(refs.some(d => d.currentDigest), `runtime image ${name}`);
    assert(refs.some(d => !d.currentValue.includes('-') && d.currentValue !== ''), `runtime exact version ${name}`);
    assert(refs.every(d => d.groupName === group && d.enabled !== false), `runtime owner ${name}`);
    assert(refs.every(d => d.allowedVersions), `runtime line ${name}`);
    for (const d of refs) {
        assert(new RegExp(d.allowedVersions.slice(1, -1)).test(d.currentValue), `current line ${name}`);
      assert(!new RegExp(d.allowedVersions.slice(1, -1)).test('99.0.0'), `future line ${name}`);
    }
  }
  assert(!records.some(d => d.packageFile.startsWith('private/')));
  assert.equal(config.minimumReleaseAge, '3 days');
  assert.equal(config.dependencyDashboardApproval, true);
  assert.equal(config.automerge, false);
  const privateDocker = live.filter(d => d.groupName === 'runtime private Docker 27');
  assert(privateDocker.some(d => d.packageFile === agent));
  assert.equal(privateDocker.filter(d => d.packageFile === 'deploy/tool-pins.txt').length, 2);
  const deployDocker = deps('deploy/Dockerfile', 'docker');
  assert.equal(deployDocker.length, 2);
  assert(deployDocker.every(d => d.groupName === 'deployment Docker 29' && d.enabled !== false && d.allowedVersions === '/^29(?:\\.|$)/'));
  for (const name of ['ghcr.io/google/osv-scanner', 'zricethezav/gitleaks', 'anchore/syft', 'aquasec/trivy', 'semgrep/semgrep']) {
    const group = deps('deploy/tool-pins.txt', name)[0]?.groupName;
    assert(group, `central ${name}`);
    for (const file of ['stock/scanners.yaml', 'internal/apptown/integration/connector_catalog.go']) {
      assert(deps(file, name).length, `${file}: ${name}`);
      assert(deps(file, name).every(d => d.groupName === group));
    }
  }
  async function update(dep, newValue, newDigest) {
    const text = content.get(dep.packageFile);
    const updated = await doAutoReplace({...dep, newValue, newDigest, autoReplaceGlobalMatch: true}, text, false);
    assert(updated && updated !== text, `replacement failed: ${dep.packageFile} ${dep.depName}`);
    content.set(dep.packageFile, updated);
  }
  // Both architecture checksum records and executable/central versions update.
  for (const d of live.filter(d => d.groupName === 'project uv tools')) {
    const value = d.currentValue.replace('0.13.0', '0.13.1');
    await update(d, value, d.currentDigest ? (d.currentDigest[0] === '1' ? 'c' : 'd').repeat(64) : undefined);
  }
  assert(content.get(agent).includes('ARG UV_VERSION=0.13.1'));
  assert.equal((content.get(agent).match(/checksum=astral-sh\/uv version=0.13.1/g) ?? []).length, 2);
  assert(content.get(agent).includes('ARG UV_SHA256_AMD64=' + 'c'.repeat(64)));
  assert(content.get(agent).includes('ARG UV_SHA256_ARM64=' + 'd'.repeat(64)));
  for (const d of live.filter(d => d.groupName === 'project scanner semgrep')) await update(d, '1.100.0');
  assert(!content.get('internal/apptown/integration/connector_catalog.go').includes('semgrep/semgrep:1.99.0'));
  for (const d of deps('.github/workflows/ci.yml', 'gitleaks/gitleaks')) await update(d, d.currentValue.startsWith('v') ? 'v8.30.2' : '8.30.2', d.currentDigest ? 'e'.repeat(64) : undefined);
  await update(deps('Makefile', 'golang.org/x/vuln')[0], 'v1.7.1');
  const stockGo = deps('stock/tool-versions.yaml', 'go')[0];
  await update(stockGo, '1.27.1');
  // Digest-only refreshes retain each exact version and image variant.
  for (const [name] of runtimes) {
    const d = deps(catalog, name).find(d => d.currentDigest);
    const before = content.get(catalog);
    await update(d, d.currentValue, 'sha256:' + 'a'.repeat(64));
    assert(content.get(catalog).includes('sha256:' + 'a'.repeat(64)), `digest refresh ${name}`);
    assert.equal(content.get(catalog), before.replace(d.currentDigest, 'sha256:' + 'a'.repeat(64)), `only digest changes ${name}`);
    content.set(catalog, before);
  }
  // Each runtime proposal uses real Renovate replacement, retaining variants.
  for (const [name, group, next] of runtimes) {
    const before = content.get(catalog);
    for (const d of live.filter(d => d.groupName === group && d.enabled !== false)) {
      const value = d.currentValue.replace(/^v?[0-9.]+/, next);
      await update(d, value, d.currentDigest ? 'sha256:' + 'f'.repeat(64) : undefined);
    }
    const text = content.get(catalog);
    assert.notEqual(text, before, `runtime replacement ${name}`);
    assert(text.includes('version: ' + (name === 'postgres' ? '"' + next + '"' : next)), `exact version replaced ${name}`);
    const image = deps(catalog, name).find(d => d.currentDigest);
    const expected = name === 'busybox' ? 'busybox@sha256:' : name + ':' + image.currentValue.replace(/^v?[0-9.]+/, next) + '@sha256:';
    assert(text.includes(expected + 'f'.repeat(64)), `version and digest replaced ${name}`);
  }
  assert(content.get(agent).includes('ARG DOCKER_VERSION=27.5.2'));
  assert.deepEqual(content.get(agent).match(/^ARG DOCKER_SHA256_.*$/gm), dockerHashes, 'CLI checksums require manual verification');
  for (const d of deployDocker) await update(d, d.currentValue.replace('29.9.2', '29.9.3'), 'sha256:' + 'e'.repeat(64));
  assert.equal((content.get('deploy/Dockerfile').match(/docker:29\.9\.3-(?:cli|dind)@sha256:/g) ?? []).length, 2);
  // The actual pin gate must accept a complete temporary grouped update.
  for (const file of files.filter(f => !f.startsWith('private/'))) {
    const dest = path.join(temp, file); await fs.mkdir(path.dirname(dest), {recursive: true});
    await fs.copyFile(path.join(root, file), dest);
  }
  for (const [file, text] of content) await fs.writeFile(path.join(temp, file), text);
  execFileSync('python3', ['scripts/pins-check.py', temp], {cwd: root});
  console.log(`Renovate ${require('./package.json').version}: ${live.length} extracted dependencies; grouped version, architecture checksum, CI, stock and digest replacements passed.`);
  if (process.env.RENOVATE_EVIDENCE) await fs.writeFile(process.env.RENOVATE_EVIDENCE, JSON.stringify(live.map(({packageFile, depName, currentValue, currentDigest, datasource, groupName, allowedVersions, enabled}) => ({packageFile, depName, currentValue, currentDigest, datasource, groupName, allowedVersions, enabled})), null, 2));
} finally { await fs.rm(temp, {recursive: true, force: true}); }
