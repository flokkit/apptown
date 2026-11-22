// The chat draft writer (internal/apptown/agentharness/pi/draft.js) against the
// exact pinned harness, without paid inference. Run by hand at each pi upgrade:
// APPTOWN_PI_MODULE=/path/to/pi-coding-agent node internal/console/testdata/pisession/draft.mjs
//
// It checks what AppTown's draft rule relies on: the draft grows while pi
// writes a message, names the chat's record and shares the recorded message's
// timestamp; and a second pi with the same home and its own session (as a
// delegate from pi-subagents is started) never writes the chat's draft, while
// the chat's own next message does. And that pi's settled mark, which ends
// the turn for AppTown, comes only after the turn's last record line: after
// an answer, after a retried stream cut, and after the person stops it.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import fs from 'node:fs/promises';
import {readFileSync, readdirSync, statSync} from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';

const module = process.env.APPTOWN_PI_MODULE;
assert.equal(JSON.parse(await fs.readFile(path.join(module, 'package.json'))).version, '1.5.0');
const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'apptown-pi-draft-'));
const sessions = path.join(dir, 'sessions');
const words = {chat: ['Let ', 'me ', 'look ', 'at ', 'it.'], delegate: ['DELEGATE ', 'WORDS ', 'HERE.'], again: ['Now ', 'it ', 'is ', 'blue.'],
  long: Array(60).fill('word ')};
let speaking = 'chat';
let cut = 0; // requests whose stream the server cuts mid-reply
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const server = http.createServer(async (req, res) => {
  for await (const _ of req) { /* consume the request */ }
  res.writeHead(200, {'Content-Type': 'text/event-stream'});
  const send = (delta, finish_reason = null) => res.write(`data: ${JSON.stringify({
    id: 'fixture', object: 'chat.completion.chunk', created: 1, model: 'm', choices: [{index: 0, delta, finish_reason}],
    ...(finish_reason ? {usage: {prompt_tokens: 1, completion_tokens: 1, total_tokens: 2}} : {}),
  })}\n\n`);
  if (cut > 0) { cut--; send({content: 'Partial '}); setTimeout(() => res.socket.destroy(), 200); return; }
  for (const w of words[speaking]) { if (res.destroyed) return; send({content: w}); await sleep(120); }
  send({}, 'stop');
  res.end('data: [DONE]\n\n');
});

// What the draft file held, each time it changed while pi ran.
// Each reading carries the chat record's size when it was first seen.
function watchDraft() {
  const seen = [];
  let last = '';
  const timer = setInterval(() => {
    try {
      const now = readFileSync(path.join(sessions, 'draft.json'), 'utf8');
      if (now !== last) {
        const record = readdirSync(sessions).filter(n => n.endsWith('.jsonl'))[0];
        seen.push({...JSON.parse(now), recordSize: record ? statSync(path.join(sessions, record)).size : 0});
        last = now;
      }
    } catch { /* not written yet */ }
  }, 5);
  return {seen, stop: () => clearInterval(timer)};
}

// The mark settles the turn after its last record line, and covers it.
async function assertSettledAfterTheRecord(seen, what) {
  const [record] = (await fs.readdir(sessions)).filter(n => n.endsWith('.jsonl'));
  const mark = seen.at(-1);
  assert.equal(mark.type, 'settled', `${what}: the turn ends with pi's settled mark: ${JSON.stringify(seen.at(-1))}`);
  assert.equal(mark.record, record, `${what}: the mark names the chat record`);
  const size = (await fs.stat(path.join(sessions, record))).size;
  assert.equal(mark.recordSize, size, `${what}: the mark came after the turn's last record line`);
  const newest = Math.max(...(await fs.readFile(path.join(sessions, record), 'utf8')).trim().split('\n').map(l => JSON.parse(l))
    .filter(e => e.type === 'message').map(e => e.message.timestamp));
  assert.ok(mark.at >= newest, `${what}: the mark covers the newest message (${mark.at} < ${newest})`);
}

async function run(sessionDir, prompt) {
  const {seen, stop} = watchDraft();
  const child = spawn(process.execPath, [path.join(module, 'dist/cli.js'), '--print', '--mode', 'json',
    '--no-context-files', '--no-skills', '--offline', '--session-dir', sessionDir, '--session-id', 'chat',
    '--model', 'fixture/m', prompt], {
    cwd: dir, env: {...process.env, HOME: dir, PI_SKIP_VERSION_CHECK: '1'}, stdio: ['ignore', 'ignore', 'pipe'],
  });
  let stderr = '';
  child.stderr.on('data', b => { stderr += b; });
  assert.equal(await new Promise(resolve => child.on('close', resolve)), 0, stderr);
  stop();
  return seen;
}

try {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const agent = path.join(dir, '.pi', 'agent');
  await fs.mkdir(path.join(agent, 'extensions'), {recursive: true});
  await fs.mkdir(sessions);
  await fs.writeFile(path.join(agent, 'settings.json'), JSON.stringify({quietStartup: true, defaultProjectTrust: 'never'}));
  await fs.writeFile(path.join(agent, 'models.json'), JSON.stringify({providers: {fixture: {
    api: 'openai-completions', baseUrl: `http://127.0.0.1:${server.address().port}/v1`, apiKey: 'fixture-key',
    models: [{id: 'm', reasoning: false, contextWindow: 128000, maxTokens: 4096, cost: {input: 1, output: 2, cacheRead: 0, cacheWrite: 0}}],
  }}}));
  // The extension as AppTown stages it, with this run's sessions folder.
  const writer = await fs.readFile(new URL('../../../apptown/agentharness/pi/draft.js', import.meta.url), 'utf8');
  await fs.writeFile(path.join(agent, 'extensions', 'apptown-draft.js'), writer.replace('__SESSIONS__', JSON.stringify(sessions)));

  // The chat's own message grows in the draft and shares the record's time.
  const chat = await run(sessions, 'look');
  await assertSettledAfterTheRecord(chat, 'an answer');
  const drafts = chat.filter(d => d.type === 'draft');
  const texts = drafts.map(d => d.message.content.find(p => p.type === 'text')?.text ?? '');
  assert.ok(new Set(texts).size >= 3, `the draft grew while pi wrote: ${JSON.stringify(texts)}`);
  for (let i = 1; i < texts.length; i++) assert.ok(texts[i].startsWith(texts[i - 1]), `the draft only grows: ${JSON.stringify(texts)}`);
  const [record] = (await fs.readdir(sessions)).filter(n => n.endsWith('.jsonl'));
  assert.ok(drafts.every(d => d.record === record), 'the draft names the chat record');
  const recorded = (await fs.readFile(path.join(sessions, record), 'utf8')).trim().split('\n').map(l => JSON.parse(l))
    .filter(e => e.type === 'message' && e.message.role === 'assistant').at(-1);
  assert.equal(drafts.at(-1).message.timestamp, recorded.message.timestamp, 'the draft shares the recorded message time');

  // A delegate: the same home, its own session elsewhere. It must not write.
  speaking = 'delegate';
  const delegate = await run(path.join(dir, 'delegate-sessions'), 'scout');
  assert.ok(delegate.every(d => !JSON.stringify(d).includes('DELEGATE')), 'a delegate wrote the chat draft');

  // The chat's own next message still does.
  speaking = 'again';
  const again = await run(sessions, 'go on');
  assert.ok(again.some(d => JSON.stringify(d).includes('Now it')), 'the chat stopped writing its draft');

  // A stream cut mid-reply: pi retries, and settles only after the retry.
  speaking = 'chat';
  cut = 1;
  const retried = await run(sessions, 'once more');
  await assertSettledAfterTheRecord(retried, 'a retried stream cut');

  // The person stops a long answer (Escape in pi's terminal).
  speaking = 'long';
  const {seen: stopped, stop} = watchDraft();
  const child = spawn('python3', ['-c', 'import pty,sys; pty.spawn(sys.argv[1:])', process.execPath, path.join(module, 'dist/cli.js'),
    '--no-context-files', '--no-skills', '--offline', '--session-dir', sessions, '--session-id', 'chat', '--model', 'fixture/m'], {
    cwd: dir, env: {...process.env, HOME: dir, PI_SKIP_VERSION_CHECK: '1', TERM: 'xterm-256color'}, stdio: ['pipe', 'pipe', 'inherit'],
  });
  child.stdout.on('data', () => {});
  await sleep(3000);
  child.stdin.write('Say a lot.\r');
  for (let i = 0; i < 100 && !stopped.some(d => d.type === 'draft' && JSON.stringify(d).includes('word word word')); i++) await sleep(50);
  child.stdin.write('\x1b');
  for (let i = 0; i < 100 && stopped.at(-1)?.type !== 'settled'; i++) await sleep(50);
  await sleep(500);
  child.kill('SIGKILL');
  stop();
  await assertSettledAfterTheRecord(stopped, 'a stop');
  console.log(`ok: ${chat.length} chat readings, ${delegate.length} while the delegate ran, ${again.length} on the next message, ${retried.length} with a retry, ${stopped.length} with a stop`);
} finally {
  server.close();
  await fs.rm(dir, {recursive: true, force: true});
}
