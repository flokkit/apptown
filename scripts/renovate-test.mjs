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
const { extractPackageFile, get: getManager } = await import(path.join(renovate, 'dist/modules/manager/index.js'));
const { extractPackageFile: extractNpm } = await import(path.join(renovate, 'dist/modules/manager/npm/extract/index.js'));
const { doAutoReplace } = await import(path.join(renovate, 'dist/workers/repository/update/branch/auto-replace.js'));
const { applyPackageRules } = await import(path.join(renovate, 'dist/util/package-rules/index.js'));
const root = process.cwd();
const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'apptown-renovate-'));
GlobalConfig.set({ localDir: temp, cacheDir: path.join(temp, 'cache') });
const config = JSON5.parse(await fs.readFile('renovate.json5', 'utf8'));
const minimatch = require('minimatch').minimatch;
const allFiles = execFileSync('git', ['ls-files'], {encoding: 'utf8'}).trim().split('\n');
const matches = (file, patterns) => patterns.some(p => p.startsWith('/') && p.endsWith('/')
  ? new RegExp(p.slice(1, -1)).test(file) : minimatch(file, p, {dot: true}));
const files = allFiles.filter(f => !matches(f, config.ignorePaths));
const records = [];
const content = new Map();
async function extract(manager, file, options = {}) {
  const text = content.get(file) ?? await fs.readFile(file, 'utf8');
  content.set(file, text);
  const result = manager === 'npm' ? await extractNpm(text, file, options) : await extractPackageFile(manager, text, file, options);
  for (const [depIndex, dep] of (result?.deps ?? []).entries()) {
    records.push(await applyPackageRules({...config, ...options, ...result, ...dep, depIndex,
      manager, packageFile: file, packageName: dep.packageName ?? dep.depName,
      packageRules: config.packageRules}, 'test'));
  }
}
try {
  async function extractConfigured(file) {
    if (config.enabledManagers.includes('custom.regex')) {
      for (const cm of config.customManagers) {
        if (matches(file, cm.managerFilePatterns)) await extract('regex', file, cm);
      }
    }
    for (const manager of config.enabledManagers.filter(m => !m.startsWith('custom.'))) {
      const defaults = getManager(manager, 'defaultConfig');
      const options = {...defaults, ...config[manager], managerFilePatterns: [
        ...(defaults?.managerFilePatterns ?? []), ...(config[manager]?.managerFilePatterns ?? []),
      ]};
      if (matches(file, options.managerFilePatterns ?? [])) await extract(manager, file, options);
    }
  }
  for (const file of files) await extractConfigured(file);
  const live = records.filter(d => d.currentValue && (!d.skipReason || d.enabled === false));
  function deps(file, name) { return live.filter(d => d.packageFile === file && (!name || d.depName === name)); }
  function found(file, value) { assert(live.some(d => d.packageFile === file && d.currentValue === value), `${file}: missing ${value}`); }
  // Inventory pins independently of Renovate's match patterns. Each custom pin
  // must be inside an extracted replacement span, so another ARG with the same
  // value cannot accidentally satisfy coverage.
  const guide = await fs.readFile('design/operations/github.md', 'utf8');
  const exceptionSection = guide.split('<!-- renovate-exceptions:start -->')[1].split('<!-- renovate-exceptions:end -->')[0];
  const exceptions = [...exceptionSection.matchAll(/^\| `([^`]+)` \| `([^`]+)` \| ([^\n]+) \|$/gm)]
    .map(([, file, selector, reason]) => ({file, selector, reason, used: false}));
  assert(exceptions.length, 'documented exception inventory');
  function pins(file, text) {
    const result = [];
    function add(key, token, start, kind, name, value = token) {
      result.push({file, key, token, start, end: start + token.length, kind, name, value});
    }
    // Native manifests are checked dependency by dependency, including engines.
    if (/(^|\/)package.json$/.test(file)) {
      const manifest = JSON.parse(text);
      for (const type of ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies', 'engines', 'packageManager']) {
        const entries = type === 'packageManager' ? [] : Object.entries(manifest[type] ?? {});
        for (const [name, value] of entries) add(name, value, text.indexOf(JSON.stringify(value)), 'npm', name);
      }
      if (manifest.packageManager) {
        const i = manifest.packageManager.lastIndexOf('@');
        add('packageManager', manifest.packageManager, text.indexOf(manifest.packageManager), 'npm', manifest.packageManager.slice(0, i), manifest.packageManager.slice(i + 1));
      }
      return result;
    }
    if (/(^|\/)composer.json$/.test(file)) {
      const manifest = JSON.parse(text);
      for (const type of ['require', 'require-dev']) for (const [name, value] of Object.entries(manifest[type] ?? {}))
        add(name, value, text.indexOf(JSON.stringify(value)), 'composer', name);
      for (const [name, value] of Object.entries(manifest.config?.platform ?? {}))
        add(`config.platform.${name}`, value, text.indexOf(value, text.indexOf('"platform"')), 'custom');
      return result;
    }
    if (/(^|\/)go.mod$/.test(file)) {
      for (const m of text.matchAll(/^\s*(\S+)\s+(v?\d[^\s]*)/gm))
        add(m[1], m[2], m.index + m[0].lastIndexOf(m[2]), 'gomod', m[1]);
      return result;
    }
    if (file === 'deploy/ci-heavy/test-images.txt') {
      for (const m of text.matchAll(/^(?!#|$)(\S+)$/gm)) add('test image preload', m[1], m.index, 'fixture');
      return result;
    }
    // Lockfiles are generated by the native manifest managers. Fixtures,
    // documentation, API schemas and qualification evidence are not new releases.
    if (!/(?:Dockerfile[^/]*|\.(?:go|sh|ya?ml|toml|py|[cm]?js|txt)|Makefile)$/.test(file) ||
        /(?:^|\/)(?:eval|testdata)(?:\/|$)|[-_]test\.|\.test\.|fake\/|workertest\//.test(file)) return result;
    let offset = 0, catalogueKey, tomlSection;
    for (const line of text.split('\n')) {
      const trimmed = line.trim();
      if (file.endsWith('.toml') && /^\[.*\]$/.test(trimmed)) tomlSection = trimmed;
      if (file === 'stock/tool-versions.yaml' && /^  [\w-]+:$/.test(line)) catalogueKey = trimmed.slice(0, -1);
      if (!/^(?:#|\/\/|\*)/.test(trimmed)) {
        const addMatch = (m, key, token, kind = 'custom', name) =>
          add(key, token, offset + m.index + m[0].lastIndexOf(token), kind, name);
        for (const m of line.matchAll(/(?:ARG |ENV |^\s*)(\w+_(?:VERSION|SHA256(?:_\w+)?))\s*(?:=|:=|:)\s*["']?([v0-9a-f][^\s"']*)/g))
          addMatch(m, trimmed, m[2], m[1].includes('SHA256') ? 'checksum' : 'custom');
        for (const m of line.matchAll(/['"]([a-f0-9]{64})  /g)) addMatch(m, trimmed, m[1], 'checksum');
        for (const m of line.matchAll(/\b(\w+_VERSION)=\$\{[^}]*:-(v?\d[^}]+)\}/g)) addMatch(m, trimmed, m[2]);
        // Plain image-list files also need a manager or an explicit exception.
        if (file.endsWith('.txt') && /^[a-z][a-z0-9./_-]*(?::[\w.-]+)?(?:@sha256:[a-f0-9]{64})?$/.test(trimmed) && /[:@]/.test(trimmed))
          add(trimmed, trimmed, offset + line.indexOf(trimmed), 'image');
        for (const m of line.matchAll(/(?:FROM (?:--\S+ )*|(?:image|client): ["']?)([a-z][a-z0-9./_-]*:[a-zA-Z0-9_.-]+(?:@sha256:[a-f0-9]{64})?)/g))
          addMatch(m, catalogueKey ?? trimmed, m[1], 'image');
        // Numeric image tags, named distro tags with digests, and untagged digests.
        for (const m of line.matchAll(/(?:FROM (?:--\S+ )*|(?:image|client): |"image": "|Placeholder: "|(?:ARG |ENV )\w+=|\b\w+_IMAGE\s*(?:=|:=)\s*|["'])([a-z][a-z0-9./_-]*(?::[a-zA-Z0-9_.-]+)?@sha256:[a-f0-9]{64}|[a-z][a-z0-9./_-]*:v?\d[a-zA-Z0-9_.-]*)/g)) {
          const token = m[1];
          if (/^(?:https?|localhost):/.test(token)) continue;
          addMatch(m, catalogueKey ?? trimmed, token, 'image');
        }
        if (file === 'deploy/tool-pins.txt') {
          const m = /^(\S+)\s+(\S+)/.exec(line);
          if (m) addMatch(m, m[1], m[2]);
        }
        if (file === 'stock/tool-versions.yaml') {
          const m = /\{version: "?([^",}\s]+)/.exec(line);
          if (m) addMatch(m, catalogueKey, m[1]);
        }
        if (file.endsWith('.toml') && tomlSection === '[tools]') {
          const m = /^(\w+) = "([^"]+)"/.exec(line);
          if (m) addMatch(m, m[1], m[2], 'mise', m[1]);
        }
        if (/^stock\/dependencies\//.test(file)) {
          const m = /^  version: "?([^"\s]+)/.exec(line);
          if (m) addMatch(m, 'version', m[1]);
        }
        if (/^\.github\/workflows\//.test(file)) {
          const m = /uses: ([^@\s]+)@([^\s]+)/.exec(line);
          if (m) addMatch(m, m[1], m[2], 'action', m[1].split('/').slice(0, 2).join('/'));
          const tool = /(?:node|go|python|ruby|java)-version: ["']?([0-9][^\s"']*)/.exec(line);
          if (tool) addMatch(tool, trimmed, tool[1], 'github-actions', trimmed.split('-version:')[0]);
          const runner = /runs-on: ([a-z]+)-([0-9.]+)/.exec(line);
          if (runner) addMatch(runner, trimmed, runner[2], 'github-actions', runner[1]);
        }
        // A literal downloaded/installed tool version in a new script needs an
        // annotation/manager too, even when it uses no *_VERSION variable.
        for (const url of line.matchAll(/https?:\/\/[^/\s"'`]+(\/[^\s"'`]+)/g)) {
          if (!/\b(?:curl|wget)\b/.test(line) && !/\.(?:phar|tgz|zip|tar\.(?:gz|xz))$/.test(url[1])) continue;
          for (const m of url[1].matchAll(/(?:\/|[-_])v?(\d+\.\d+\.\d+)/g))
            add(trimmed, m[1], offset + url.index + url[0].indexOf(url[1]) + m.index + m[0].indexOf(m[1]), 'custom');
        }
        if (/\b(?:curl|wget|go install|npm install|uv tool install)\b/.test(line)) {
          for (const m of line.matchAll(/(?:@|==|download\/v?)(v?\d+\.\d+(?:\.\d+)?)/g)) addMatch(m, trimmed, m[1]);
        }
      }
      offset += line.length + 1;
    }
    return result;
  }
  function covers(pin, dep, text) {
    if (dep.enabled === false || dep.skipReason || !dep.currentValue) return false;
    if (dep.manager === 'regex') {
      if (!dep.replaceString || (pin.kind === 'checksum' && dep.currentDigest !== pin.token)) return false;
      let start = -1;
      while ((start = text.indexOf(dep.replaceString, start + 1)) !== -1)
        if (pin.start >= start && pin.end <= start + dep.replaceString.length) return true;
      return false;
    }
    if (pin.kind === 'image' && ['dockerfile', 'docker-compose'].includes(dep.manager))
      return pin.token === `${dep.depName}${dep.currentValue ? ':' + dep.currentValue : ''}${dep.currentDigest ? '@' + dep.currentDigest : ''}`;
    if (pin.kind === 'action') return dep.manager === 'github-actions' && dep.depName === pin.name &&
      [dep.currentDigest, dep.currentValue].includes(pin.value);
    return pin.kind === dep.manager && dep.depName === pin.name &&
      (dep.currentValue === pin.value || (dep.manager === 'mise' && pin.value.endsWith(dep.currentValue)));
  }
  function uncovered(file, text, inventory = records, useExceptions = true) {
    return pins(file, text).filter(pin => {
      const exception = useExceptions && exceptions.find(e => e.file === file &&
        (pin.key === e.selector || ((e.selector.startsWith('ARG ') || e.selector.startsWith('FROM ')) && pin.key.startsWith(e.selector))));
      if (exception) { exception.used = true; return false; }
      return !inventory.some(d => d.packageFile === file && covers(pin, d, text));
    });
  }
  for (const file of files.filter(f => /(?:package-lock.json|composer.lock|go.sum)$/.test(f))) {
    const manifest = file.replace(/(?:package-lock.json|composer.lock|go.sum)$/, name => ({'package-lock.json': 'package.json', 'composer.lock': 'composer.json', 'go.sum': 'go.mod'}[name]));
    assert(live.some(d => d.packageFile === manifest && d.enabled !== false), `${file}: no managed manifest`);
  }
  const missing = [];
  for (const file of allFiles.filter(f => !/(?:^|\/)(?:node_modules|testdata|eval)\//.test(f))) {
    const text = content.get(file) ?? await fs.readFile(file, 'utf8');
    missing.push(...uncovered(file, text));
  }
  assert.deepEqual(missing.map(p => `${p.file}: ${p.key} (${p.token})`), [], 'pins without an enabled manager or documented exception');
  for (const manager of ['gomod', 'npm', 'composer', 'github-actions', 'dockerfile', 'docker-compose', 'mise', 'regex']) assert(live.some(d => d.manager === manager), manager);
  assert.equal(config.lockFileMaintenance?.enabled, true, 'refresh transitive lockfile pins');
  for (const e of exceptions) assert(e.used, `stale exception: ${e.file} ${e.selector}`);
  // Regression: same version as an existing pin, but a different owner/site.
  for (const [file, text] of [
    ['Makefile', 'NEW_VERSION := v0.8.1\n'],
    ['scripts/new-tool.sh', 'TOOL_VERSION=1.2.3\n'],
    ['scripts/new-tool.py', 'TOOL_VERSION = "1.2.3"\n'],
    ['scripts/new-tool.sh', 'TOOL_VERSION=${TOOL_VERSION:-1.2.3}\n'],
    ['deploy/ci-heavy/Dockerfile', 'ARG NEW_SHA256=' + 'a'.repeat(64) + '\n'],
    ['stock/scanners.yaml', '    image: new/scanner:1.2.3\n'],
    ['stock/toolchains/go.toml', '[tools]\nunknown = "1.27.0"\n'],
    ['stock/tool-versions.yaml', '  unknown:\n    "1": {version: 1.2.3}\n'],
    ['stock/dependencies/new.yaml', '  client: new/runtime:1.2.3\n'],
    ['.github/workflows/new.yml', '  TOOL_VERSION: 1.2.3\n'],
    ['deploy/new/Dockerfile', 'FROM new/runtime:1.2.3\n'],
    ['deploy/new/Dockerfile', 'FROM new/runtime:stable\n'],
    ['deploy/new/Dockerfile', 'ARG BASE=new/runtime:1.2.3\nFROM ${BASE}\n'],
    ['deploy/new/Dockerfile', 'ENV TOOL_VERSION=1.2.3\n'],
    ['deploy/new/compose.yaml', '    image: new/runtime:1.2.3\n'],
    ['scripts/install.sh', 'npm install tool@1.2.3\n'],
    ['scripts/install.sh', 'curl https://example.com/tool/v1.2.3/tool.tar.gz\n'],
    ['internal/new.go', 'const image = "new/runtime:1.2.3"\n'],
  ]) assert(uncovered(file, text, records, false).length, `new pin escaped: ${file}`);
  // Every active runtime proposal keeps human qualification, even a digest bump.
  for (const dep of live.filter(d => d.enabled !== false && (d.datasource === 'docker' || /^stock\/(toolchains\/|tool-versions.yaml|dependencies\/)/.test(d.packageFile)))) {
    for (const updateType of ['major', 'minor', 'patch', 'digest', 'pin', 'pinDigest']) {
      const proposal = await applyPackageRules({...dep, updateType}, 'test');
      assert.equal(proposal.automerge, false, `automerge: ${dep.packageFile} ${dep.depName} ${updateType}`);
      assert(proposal.addLabels?.includes('qualification-required'), `qualification: ${dep.packageFile} ${dep.depName} ${updateType}`);
    }
    if (dep.allowedVersions?.startsWith('/'))
      assert(new RegExp(dep.allowedVersions.slice(1, -1)).test(dep.currentValue), `current line: ${dep.packageFile} ${dep.depName}`);
  }
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
  for (const d of deps('stock/scanners.yaml')) {
    const name = d.depName === 'ghcr.io/astral-sh/uv' ? 'astral-sh/uv' : d.depName;
    const central = deps('deploy/tool-pins.txt', name);
    assert(central.length && central.every(p => p.groupName === d.groupName), `scanner owner ${d.depName}`);
  }
  for (const d of deps('stock/tool-versions.yaml')) assert(/^(stock |runtime )/.test(d.groupName) || d.groupName === 'project scanner godot-ci', `stock owner ${d.depName}`);
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
  const archiveGroups = new Set(deps(agent).filter(d => d.datasource === 'github-release-attachments' && d.groupName !== 'project uv tools').map(d => d.groupName));
  for (const group of archiveGroups) for (const d of live.filter(d => d.groupName === group)) {
    const value = d.currentValue.replace(/(\d+)$/, patch => String(Number(patch) + 1));
    await update(d, value, d.currentDigest ? 'a'.repeat(64) : undefined);
  }
  for (const d of live.filter(d => d.groupName === 'project scanner semgrep')) await update(d, '1.100.0');
  assert(!content.get('internal/apptown/integration/connector_catalog.go').includes('semgrep/semgrep:1.99.0'));
  for (const d of deps('.github/workflows/ci.yml', 'gitleaks/gitleaks')) await update(d, d.currentValue.startsWith('v') ? 'v8.30.2' : '8.30.2', d.currentDigest ? 'e'.repeat(64) : undefined);
  await update(deps('Makefile', 'golang.org/x/vuln')[0], 'v1.7.1');
  const stockGo = deps('stock/tool-versions.yaml', 'go')[0];
  await update(stockGo, '1.27.1');
  // Every coupled scanner occurrence, not just one representative, moves.
  for (const group of ['project Go tools', ...['osv-scanner', 'gitleaks', 'syft', 'trivy', 'hadolint', 'godot-ci', 'ruby'].map(n => `project scanner ${n}`)]) {
    for (const d of live.filter(d => d.groupName === group)) {
      const value = d.currentValue.replace(/^(v?\d+\.\d+\.)(\d+)/, (_, prefix, patch) => prefix + (Number(patch) + 1));
      await update(d, value, d.currentDigest ? 'sha256:' + 'a'.repeat(64) : undefined);
    }
  }
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
  const wpComposer = 'stock/seeds/wordpress/composer.json';
  for (const d of live.filter(d => d.groupName === 'WordPress PHP runtime')) {
    await update(d, d.currentValue.replace('8.3.35', '8.3.36'), d.currentDigest ? 'sha256:' + 'b'.repeat(64) : undefined);
  }
  assert.equal(JSON.parse(content.get(wpComposer)).config.platform.php, '8.3.36');
  const wp = 'stock/seeds/wordpress/Dockerfile';
  await update(deps(wp, 'wp-cli/wp-cli')[0], 'v2.12.1', 'a'.repeat(64));
  assert(content.get(wp).includes('/download/v2.12.1/wp-cli-2.12.1.phar'));
  assert(content.get(wp).includes(`echo '${'a'.repeat(64)}  /usr/local/bin/wp-cli.phar'`));
  const heavy = 'deploy/ci-heavy/Dockerfile';
  for (const [name, version, arg] of [
    ['docker/buildx', 'v0.37.2', 'DOCKER_BUILDX'],
    ['docker/compose', 'v5.5.2', 'DOCKER_COMPOSE'],
    ['shssoichiro/oxipng', 'v10.2.2', 'OXIPNG'],
  ]) {
    const dep = deps(heavy, name)[0];
    assert.equal(dep.datasource, 'github-release-attachments');
    await update(dep, version, 'b'.repeat(64));
    assert(content.get(heavy).includes(`ARG ${arg}_VERSION=${arg === 'OXIPNG' ? version.slice(1) : version}\nARG ${arg}_SHA256=${'b'.repeat(64)}`));
  }
  // The actual pin gate must accept a complete temporary grouped update.
  for (const file of files.filter(f => !f.startsWith('private/'))) {
    const dest = path.join(temp, file); await fs.mkdir(path.dirname(dest), {recursive: true});
    await fs.copyFile(path.join(root, file), dest);
  }
  for (const [file, text] of content) await fs.writeFile(path.join(temp, file), text);
  execFileSync('python3', ['scripts/pins-check.py', temp], {cwd: root});
  console.log(`Renovate ${require('./package.json').version}: ${live.length} extracted dependencies; pin inventory, qualification rules, grouped versions, checksums and digest replacements passed.`);
  if (process.env.RENOVATE_EVIDENCE) await fs.writeFile(process.env.RENOVATE_EVIDENCE, JSON.stringify(live.map(({packageFile, depName, currentValue, currentDigest, datasource, groupName, allowedVersions, enabled}) => ({packageFile, depName, currentValue, currentDigest, datasource, groupName, allowedVersions, enabled})), null, 2));
} finally { await fs.rm(temp, {recursive: true, force: true}); }
