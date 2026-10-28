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
    if (!['GLAB', 'DOCKER'].includes(name)) found(agent, version);
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
  for (const d of deps('stock/tool-versions.yaml')) assert(d.groupName?.startsWith('stock '), `stock owner ${d.depName}`);
  assert.equal(deps(agent).filter(d => d.datasource === 'github-release-attachments').length, 10);
  assert.equal(deps('.github/workflows/ci.yml', 'gitleaks/gitleaks').length, 2);
  for (const d of live.filter(d => d.depName === 'docker' && d.packageFile.startsWith('deploy/'))) assert.equal(d.enabled, false);
  assert(!deps('stock/tool-versions.yaml').some(d => ['docker', 'caddy', 'busybox', 'alpine/git'].includes(d.depName)));
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
  const dbImage = deps('stock/tool-versions.yaml', 'postgres').find(d => d.currentDigest);
  await update(dbImage, dbImage.currentValue, 'sha256:' + 'f'.repeat(64));
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
