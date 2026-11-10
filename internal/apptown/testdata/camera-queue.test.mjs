import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runQueue, oneFailure } from './camera-queue.mjs';

const held = () => { let release; const promise = new Promise(r => { release = r; }); return { promise, release }; };

test('a final stop starts only after every ordinary page has finished', async () => {
  const slow = held(), events = [];
  let entered;
  const enteredSlow = new Promise(r => { entered = r; });
  const run = runQueue({
    pool: ['slow', 'quick-1', 'quick-2'], finals: ['features', 'reviews-running'], tabs: 4,
    shoot: async task => {
      events.push('start ' + task);
      if (task === 'slow') { entered(); await slow.promise; }
      events.push('end ' + task);
    },
  });
  await enteredSlow;
  // Let every other task run to completion while the slow page is held.
  for (let i = 0; i < 20; i++) await Promise.resolve();
  assert.ok(!events.some(e => e.includes('features') || e.includes('reviews-running')), `a final stop began while a page was pending: ${events}`);
  slow.release();
  await run;
  assert.deepEqual(events.slice(-4), ['start features', 'end features', 'start reviews-running', 'end reviews-running']);
  assert.ok(events.indexOf('end slow') < events.indexOf('start features'));
});

test('pages run on several tabs at once', async () => {
  const gate = held();
  let running = 0, most = 0;
  const run = runQueue({ pool: [1, 2, 3, 4, 5], finals: [], tabs: 4, shoot: async () => { most = Math.max(most, ++running); await gate.promise; running--; } });
  for (let i = 0; i < 20; i++) await Promise.resolve();
  gate.release();
  await run;
  assert.equal(most, 4);
});

test('the first failure stops the queue and is thrown; no final stop runs', async () => {
  const shot = [];
  await assert.rejects(runQueue({
    pool: ['broken', 'a', 'b', 'c', 'd', 'e'], finals: ['features'], tabs: 1,
    shoot: async task => { shot.push(task); if (task === 'broken') throw new Error('broken page'); },
  }), /broken page/);
  assert.deepEqual(shot, ['broken']);
});

test('two tabs failing: the run reports the first failure, and only its page writes the failure files', async () => {
  const failure = oneFailure(), files = {};
  let firstWriting, releaseFirstWrite;
  const firstWrites = new Promise(r => { firstWriting = r; });
  const writeDone = new Promise(r => { releaseFirstWrite = r; });
  const shoot = async ([name], tab) => {
    try {
      if (tab === 1) await firstWrites; // the second page fails while the first is still writing
      throw new Error(name);
    } catch (err) {
      if (failure.claim(err)) {
        files.text = name;
        firstWriting();
        await writeDone; // a slow screenshot of the chosen page
        files.image = name;
      }
      throw failure.chosen;
    }
  };
  const run = runQueue({ pool: [['first'], ['second']], finals: [], tabs: 2, shoot });
  await firstWrites;
  for (let i = 0; i < 20; i++) await Promise.resolve();
  releaseFirstWrite();
  await assert.rejects(run, /^Error: first$/);
  assert.deepEqual(files, { text: 'first', image: 'first' });
});
