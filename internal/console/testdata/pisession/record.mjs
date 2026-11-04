// Re-record with the exact installed harness, without paid inference:
// APPTOWN_PI_MODULE=/path/to/pi-coding-agent node internal/console/testdata/pisession/record.mjs
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import fs from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';

const module = process.env.APPTOWN_PI_MODULE;
assert.equal(JSON.parse(await fs.readFile(path.join(module, 'package.json'))).version, '1.5.0');
const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'apptown-pi-record-'));
let request = 0;
const server = http.createServer(async (req, res) => {
  for await (const _ of req) { /* consume the actual SDK request */ }
  assert.equal(req.url, '/v1/chat/completions');
  request++;
  res.writeHead(200, {'Content-Type': 'text/event-stream'});
  const send = (delta, finish_reason = null, usage) => res.write(`data: ${JSON.stringify({
    id: `fixture-${request}`, object: 'chat.completion.chunk', created: 1,
    model: 'pi-upgrade', choices: [{index: 0, delta, finish_reason}], ...(usage ? {usage} : {}),
  })}\n\n`);
  if (request === 1) {
    send({role: 'assistant', reasoning_content: 'Inspect the fixture.'});
    send({tool_calls: [{index: 0, id: 'fixture-call', type: 'function', function: {
      name: 'bash', arguments: JSON.stringify({command: "printf 'fixture tool result\\n'"}),
    }}, {index: 1, id: 'fixture-edit', type: 'function', function: {
      name: 'edit', arguments: JSON.stringify({path: 'fixture.txt', edits: [{oldText: 'before', newText: 'after'}]}),
    }}]});
    send({}, 'tool_calls', {prompt_tokens: 100, completion_tokens: 10, total_tokens: 110});
  } else if (request === 2) {
    send({role: 'assistant', reasoning_content: 'The tool completed.'});
    send({}, 'stop', {prompt_tokens: 120, completion_tokens: 5, total_tokens: 125});
  } else {
    assert.equal(request, 3);
    send({role: 'assistant', content: 'The fixture completed.'});
    send({}, 'stop', {prompt_tokens: 140, completion_tokens: 8, total_tokens: 148});
  }
  res.end('data: [DONE]\n\n');
});
try {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const agent = path.join(dir, '.pi', 'agent');
  await fs.mkdir(agent, {recursive: true});
  await fs.writeFile(path.join(agent, 'models.json'), JSON.stringify({providers: {fixture: {
    api: 'openai-completions', baseUrl: `http://127.0.0.1:${server.address().port}/v1`, apiKey: 'fixture-key',
    models: [{id: 'pi-upgrade', reasoning: true, contextWindow: 128000, maxTokens: 4096,
      cost: {input: 1, output: 2, cacheRead: 0.1, cacheWrite: 1}}],
  }}}));
  await fs.writeFile(path.join(dir, 'fixture.txt'), 'before\n');
  const session = path.join(dir, 'console.jsonl');
  const events = [];
  for (const prompt of ['Run the fixture tool.', 'Summarize the result.']) {
    const child = spawn(process.execPath, [path.join(module, 'dist/cli.js'), '--print', '--mode', 'json',
      '--no-context-files', '--no-skills', '--no-extensions', '--offline', '--session', session,
      '--system-prompt', 'Exercise the fixture.', '--model', 'fixture/pi-upgrade', prompt], {
      cwd: dir, env: {...process.env, HOME: dir, PI_CODING_AGENT_DIR: agent}, stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stdout = '', stderr = '';
    child.stdout.on('data', b => { stdout += b; });
    child.stderr.on('data', b => { stderr += b; });
    assert.equal(await new Promise(resolve => child.on('close', resolve)), 0, stderr);
    events.push(stdout);
  }
  assert.equal(await fs.readFile(path.join(dir, 'fixture.txt'), 'utf8'), 'after\n', 'the real edit tool must execute edits[]');
  assert.equal(request, 3, 'the second invocation must resume the actual session');
  // Only redact the temporary cwd; all events, entry IDs, timestamps and usage
  // are the bytes emitted by Pi 1.5.0 against the local protocol server.
  const redact = text => text.replaceAll(dir, '/work');
  await fs.writeFile('internal/console/testdata/pisession/console.jsonl', redact(await fs.readFile(session, 'utf8')));
  await fs.mkdir('internal/apptown/agentharness/pi/testdata', {recursive: true});
  await fs.writeFile('internal/apptown/agentharness/pi/testdata/events.jsonl', redact(events.join('')));
} finally {
  server.close();
  await fs.rm(dir, {recursive: true, force: true});
}
