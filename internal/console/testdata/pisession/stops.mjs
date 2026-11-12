// Re-record how pi ends a turn the person stops and a turn whose stream is
// cut, with the exact installed harness and no paid inference:
// APPTOWN_PI_MODULE=/path/to/pi-coding-agent node internal/console/testdata/pisession/stops.mjs
// The stop presses Escape in pi's own terminal (python3's pty), as tmux
// delivers the chat's stop and a person's keystroke.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import fs from 'node:fs/promises';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';

const module = process.env.APPTOWN_PI_MODULE;
assert.equal(JSON.parse(await fs.readFile(path.join(module, 'package.json'))).version, '1.5.0');
const out = 'internal/console/testdata/pisession';

// record runs one conversation against a local protocol server whose replies
// `reply(request, send, res)` writes, and returns pi's record.
async function record(name, reply, drive) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'apptown-pi-stops-'));
  let request = 0;
  const server = http.createServer(async (req, res) => {
    for await (const _ of req) { /* consume the actual SDK request */ }
    request++;
    res.writeHead(200, {'Content-Type': 'text/event-stream'});
    const send = (delta, finish_reason = null) => res.write(`data: ${JSON.stringify({
      id: `fixture-${request}`, object: 'chat.completion.chunk', created: 1,
      model: 'pi-upgrade', choices: [{index: 0, delta, finish_reason}],
    })}\n\n`);
    reply(request, send, res);
  });
  try {
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const agent = path.join(dir, '.pi', 'agent');
    await fs.mkdir(agent, {recursive: true});
    await fs.writeFile(path.join(agent, 'models.json'), JSON.stringify({providers: {fixture: {
      api: 'openai-completions', baseUrl: `http://127.0.0.1:${server.address().port}/v1`, apiKey: 'fixture-key',
      models: [{id: 'pi-upgrade', contextWindow: 128000, maxTokens: 4096, cost: {input: 1, output: 2, cacheRead: 0.1, cacheWrite: 1}}],
    }}}));
    const sessions = path.join(dir, 'sessions');
    const args = [path.join(module, 'dist/cli.js'), '--no-context-files', '--no-skills', '--no-extensions', '--offline',
      '--session-dir', sessions, '--system-prompt', 'Exercise the fixture.', '--model', 'fixture/pi-upgrade'];
    await drive(args, {cwd: dir, env: {...process.env, HOME: dir, PI_CODING_AGENT_DIR: agent, TERM: 'xterm-256color', PI_SKIP_VERSION_CHECK: '1'}}, () => request);
    const [file] = await fs.readdir(sessions);
    // Only the temporary directory is redacted; everything else is pi's bytes.
    await fs.writeFile(path.join(out, name), (await fs.readFile(path.join(sessions, file), 'utf8')).replaceAll(dir, '/work'));
  } finally {
    server.close();
    await fs.rm(dir, {recursive: true, force: true});
  }
}

const until = async (cond, what) => {
  for (let i = 0; i < 200 && !cond(); i++) await new Promise(r => setTimeout(r, 50));
  assert.ok(cond(), what);
};
const sleep = ms => new Promise(r => setTimeout(r, ms));

// The person presses Escape while a tool runs: pi aborts the tool, then
// records the turn's next model call as stopReason "error", "This operation
// was aborted".
await record('stop-during-tool.jsonl', (request, send, res) => {
  assert.equal(request, 1, 'the stop must end the turn: no further model call');
  send({tool_calls: [{index: 0, id: 'fixture-call', type: 'function', function: {name: 'bash', arguments: JSON.stringify({command: 'sleep 30'})}}]});
  send({}, 'tool_calls');
  res.end('data: [DONE]\n\n');
}, async (args, opts, requests) => {
  const child = spawn('python3', ['-c', 'import pty,sys; pty.spawn(sys.argv[1:])', process.execPath, ...args], {...opts, stdio: ['pipe', 'pipe', 'inherit']});
  child.stdout.on('data', () => {});
  await sleep(3000);
  child.stdin.write('Run the long command.\r');
  await until(() => requests() === 1, 'pi asked for the tool call');
  await sleep(2000); // the tool is running
  child.stdin.write('\x1b');
  await sleep(3000);
  child.kill('SIGKILL');
});

// The server cuts the stream mid-reply; pi retries on its own and the retry
// answers.
await record('stream-cut-retried.jsonl', (request, send, res) => {
  send({role: 'assistant', content: 'Partial '});
  if (request === 1) { setTimeout(() => res.socket.destroy(), 300); return; }
  send({content: 'answer.'});
  send({}, 'stop');
  res.end('data: [DONE]\n\n');
}, async (args, opts, requests) => {
  const child = spawn(process.execPath, [...args, '--print', 'Answer briefly.'], {...opts, stdio: ['ignore', 'pipe', 'inherit']});
  child.stdout.on('data', () => {});
  assert.equal(await new Promise(resolve => child.on('close', resolve)), 0);
  assert.equal(requests(), 2, 'pi retried the cut stream once');
});
