// The camera's page queue: ordinary pages on several tabs at once, taken in
// order, then the final stops one after another, alone, once every ordinary
// page has finished. A final stop changes what other pages show (Features
// turns areas off for everyone; an audit's running review is begun by
// navigating to it), so none may start while another page is still being
// photographed. The first failure stops the queue: tabs finish the page they
// are on and take no more, and the failure is thrown.
export async function runQueue({ pool, finals, tabs, shoot }) {
  let next = 0, failure = null;
  const worker = async tab => {
    while (!failure && next < pool.length) {
      const task = pool[next++];
      try { await shoot(task, tab); } catch (err) { failure ??= err; }
    }
  };
  await Promise.all(Array.from({ length: Math.min(tabs, pool.length) }, (_, tab) => worker(tab)));
  if (failure) throw failure;
  for (const task of finals) await shoot(task, 0);
}

// The run's one failure: the first a tab claims, chosen synchronously so that
// only that page writes the failure text and screenshot, and every tab throws
// that same error, whichever finishes its own failure handling first.
export function oneFailure() {
  let chosen = null;
  return {
    // claim records err when it is the first; it returns whether it was.
    claim: err => { chosen ??= err; return chosen === err; },
    get chosen() { return chosen; },
  };
}
