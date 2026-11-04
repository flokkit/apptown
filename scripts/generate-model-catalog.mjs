// Export the worker's exact built-in chat catalog; no runtime Node dependency.
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';
const root = path.resolve(import.meta.dirname, '..');
const dockerfile = await fs.readFile(path.join(root, 'deploy/apptown-agent/Dockerfile'), 'utf8');
const version = dockerfile.match(/^ARG PI_VERSION=(\S+)/m)[1];
const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'apptown-models-'));
try {
  execFileSync('npm', ['install', '--prefix', temp, '--cache', path.join(temp, 'cache'), '--ignore-scripts', '--no-audit', '--no-fund', `@earendil-works/pi-coding-agent@${version}`], { stdio: 'inherit' });
  const { MODELS } = await import(pathToFileURL(path.join(temp, 'node_modules/@earendil-works/pi-ai/dist/models.generated.js')));
  const providers = {};
  for (const provider of Object.keys(MODELS).sort()) {
    providers[provider] = {};
    for (const model of Object.values(MODELS[provider]).sort((a,b) => a.id.localeCompare(b.id, 'en'))) {
      providers[provider][model.id] = { cost: model.cost, contextWindow: model.contextWindow };
    }
  }
  await fs.writeFile(path.join(root, 'internal/apptown/inference/model-catalog.json'), JSON.stringify({ piVersion: version, providers }, null, 2) + '\n');
} finally {
  await fs.rm(temp, { recursive: true, force: true });
}
