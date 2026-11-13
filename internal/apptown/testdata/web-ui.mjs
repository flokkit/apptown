import { mkdir, writeFile } from 'node:fs/promises';
import { rowFontDifferences } from '../../../web/row-font-sizes.mjs';
import { wrappedRowActions } from '../../../web/row-action-lines.mjs';
import { splitWords } from '../../../web/word-splits.mjs';
import { runQueue, oneFailure } from './camera-queue.mjs';
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE);
const base = process.env.DESIGN_URL, out = process.env.DESIGN_OUT;
// The scene's AppTown has a real-looking name (DESIGN_RESOLVE: that name and
// the loopback address it is served on); the browser resolves it there.
const resolve = process.env.DESIGN_RESOLVE ? [`--host-resolver-rules=MAP ${process.env.DESIGN_RESOLVE}`] : [];
const browser = await chromium.launch({ headless: true, args: ['--no-proxy-server', ...resolve] });
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1, timezoneId: 'UTC' });
const page = await context.newPage();
// A full-page shot of a scrolled page draws the fixed top bar where the
// viewport was, pasted over the middle of the page. In full-page shots the
// bar is placed at the top of the document instead, where a reader expects it.
// Every tab's page gets this.
const topBarAtTop = tabPage => {
  const screenshot = tabPage.screenshot.bind(tabPage);
  tabPage.screenshot = options => screenshot(options?.fullPage ? { ...options, style: `${options.style ?? ''} .topbar { position: absolute; }` } : options);
  return tabPage;
};
topBarAtTop(page);
// Each deployment row's height when it is first drawn, before its late reads
// (the viewer's permissions, the target's reading, the backups) arrive, so the
// deployments pages can check that rows do not grow when they do. Installed
// before every navigation, since the camera waits for the network to settle.
await context.addInitScript(() => {
  const seen = new WeakSet();
  const record = () => document.querySelectorAll('.fleet-table tbody tr').forEach(tr => {
    if (seen.has(tr)) return;
    seen.add(tr);
    tr.dataset.firstHeight = String(tr.getBoundingClientRect().height);
  });
  const start = () => new MutationObserver(() => document.fonts.ready.then(record)).observe(document.documentElement, { childList: true, subtree: true });
  if (document.documentElement) start(); else document.addEventListener('DOMContentLoaded', start);
});
// Content decisions awaiting resolution; these pages still receive every layout check.
// These exclusions concern status meaning and help wording, not layout.
// The affected pages still receive every viewport and disclosure check.
const cameraContentExclusions = {
  'target-unknown': 'The unreachable target shows a capacity-check failure without a memory summary; whether unknown memory must always be shown is an open content decision.',
  conformance: 'Overview uses unknown states; whether unavailable execution needs a separate red marker is an open content decision.',
  'conformance-project-mixed': 'Project detail groups unavailable execution under Couldn’t run with unknown states; whether it needs a separate red marker is the same open content decision.',
};
// Where the run's time goes: its phases, and each shot's parts (load, settle,
// checks, screenshot, after: the disclosure and the screen's extra shots).
// Written as it goes, so a failed run keeps what it measured.
const timings = { phases: [], shots: [] };
let phaseStart = performance.now();
const phase = name => { const now = performance.now(); timings.phases.push({ name, ms: Math.round(now - phaseStart) }); phaseStart = now; };
const saveTimings = () => writeFile(`${out}/timings.json`, JSON.stringify(timings, null, 2));
const shotTimer = (screen, size, theme) => {
  const shot = { screen, size, theme, total: 0 };
  const start = performance.now();
  let at = start;
  return {
    shot,
    lap: name => { const now = performance.now(); shot[name] = Math.round(now - at) + (shot[name] ?? 0); at = now; },
    done: () => { shot.total = Math.round(performance.now() - start); timings.shots.push(shot); },
  };
};
const timingSummary = () => {
  const sum = (list, key) => list.reduce((n, x) => n + (x[key] ?? 0), 0);
  const lines = ['camera timings:'];
  for (const p of timings.phases) lines.push(`  ${p.name}: ${(p.ms / 1000).toFixed(1)} s`);
  const shots = timings.shots, total = sum(shots, 'total');
  if (shots.length) {
    lines.push(`  ${shots.length} shots in ${(total / 1000).toFixed(1)} s of tab time, ${(shots.length / (total / 60000)).toFixed(1)} a minute per tab`);
    lines.push('  ' + ['load', 'settle', 'checks', 'screenshot', 'after'].map(k => `${k} ${Math.round(100 * sum(shots, k) / total)}%`).join(', '));
    const byScreen = {};
    for (const x of shots) byScreen[x.screen] = (byScreen[x.screen] ?? 0) + x.total;
    const how = {};
    for (const x of shots) if (x.how && x.how !== 'fresh') how[x.how] = (how[x.how] ?? 0) + 1;
    lines.push('  large text: ' + Object.entries(how).sort((a, b) => b[1] - a[1]).map(([k, v]) => `${k} ${v}`).join('; '));
    lines.push('  slowest pages: ' + Object.entries(byScreen).sort((a, b) => b[1] - a[1]).slice(0, 10).map(([k, v]) => `${k} ${(v / 1000).toFixed(1)} s`).join(', '));
  }
  return lines.join('\n');
};
const errors = [];
page.on('pageerror', e => { errors.push(e.message); void writeFile(`${out}/browser-errors.json`, JSON.stringify(errors)); });
try {
  await page.goto(base + '/');
  const status = await page.evaluate(async password => (await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: 'sam', password }) })).status, process.env.DESIGN_PASSWORD);
  if (status !== 200) throw new Error('login: ' + status);
  const facts = await page.evaluate(async () => {
    const read = async path => { const r = await fetch('/api/v1' + path); if (!r.ok) throw new Error(path + ': ' + r.status); return r.json(); };
    const activity = await read('/projects/club-newsletter/overview');
    const deployments = await read('/projects/club-newsletter/deployments');
    const backups = await read('/projects/club-newsletter/deployments/main/backups');
    const releases = await read('/projects/club-newsletter/releases');
    const changes = await read('/projects/club-newsletter/changes');
    const integrations = await read('/integrations');
    const reviews = await Promise.all(activity.active.map(r => read('/changes/' + r.changeId)));
    return { releases, registryConfigured: integrations.integrations.some(i => i.capability === 'container-registry'), idleSeconds: activity.history.find(c => c.title === "Replace the old ride archive")?.tookS, history: activity.history.filter(c => c.closedAs !== 'nothing-to-change' && c.type !== 'scheduled').length, quietChanges: activity.history.filter(c => c.closedAs === 'nothing-to-change').map(c => c.title), historyItems: activity.history.map(c => ({title: c.title, closedAs: c.closedAs})), backups: backups.backups, stoppedCheck: changes.some(c => c.title === "Fix the broken ride calendar link" && c.state === "abandoned"), deployments, backupBytes: backups.backups.find(b => b.bytes > 0)?.bytes, failingCheck: reviews.some(v => v.detail?.steps.some(s => s.status === 'fail')) };
  });
  if (process.env.DESIGN_PAGES?.split(',').includes('janitor')) {
    const janitor = await page.evaluate(async () => {
      const current = await (await fetch('/api/v1/janitor')).json();
      const history = await (await fetch('/api/v1/janitor/history')).json();
      return { current, history };
    });
    if (!janitor.current.projects.some(p => p.queues.some(q => q.state === 'stopped')) ||
        !janitor.current.deployments.some(d => d.hold) || !janitor.history.history.changes.length)
      throw new Error('incomplete janitor scene: ' + JSON.stringify(janitor));
    await writeFile(`${out}/janitor-facts.json`, JSON.stringify(janitor, null, 2));
  }
  await mkdir(out, { recursive: true });
  phase('login and facts');
  await writeFile(`${out}/facts.json`, JSON.stringify(facts, null, 2));
  if (!facts.quietChanges.includes('Check the spring notice') || !facts.registryConfigured || facts.history < 8 || facts.history > 12 || !facts.failingCheck || !facts.backupBytes || !facts.stoppedCheck || facts.backups.length < 16 || facts.releases.length < 16 || !(facts.idleSeconds >= 458640) || !facts.releases.some(r => r.serving && r.notes?.startsWith("Adds the winter ride times page"))) throw new Error('incomplete design seed: ' + JSON.stringify(facts));
  if (!facts.deployments.some(d => d.displayLabel === 'Production' && d.running === true) || !facts.deployments.some(d => d.name === 'noticeboard' && d.running === null && d.observation === 'unreachable')) throw new Error('deployment seed is not running production + an unreachable copy: ' + JSON.stringify(facts.deployments));
  const infraCases = await page.evaluate(async () => {
    const read = async path => { const r = await fetch('/api/v1' + path); if (!r.ok) throw new Error(path + ': ' + r.status); return r.json(); };
    // The fixture collected capacity, including the failed preview-host probe.
    const pending = await read('/attention');
    const busy = await read('/targets/built-in-docker/capacity');
    const unknown = await read('/targets/design-preview/capacity');
    return { pending, busy, unknown };
  });
  await writeFile(`${out}/infrastructure.json`, JSON.stringify(infraCases, null, 2));
  if (!infraCases.busy.memory.total.known || infraCases.busy.memory.promised.unlimited.apptown + infraCases.busy.memory.promised.unlimited.other !== 20 || infraCases.busy.tenants.filter(t => t.kind === 'deployment').length !== 9) throw new Error('busy host seed lacks nine running apps and twenty containers without limits');
  if (!infraCases.busy.memory.overcommitted || Math.abs(infraCases.busy.memory.total.value / 2 ** 30 - 3.8) > .01 || Math.abs((infraCases.busy.memory.promised.sum.apptown.value + infraCases.busy.memory.promised.sum.other.value) / 2 ** 30 - 5.64) > .01) throw new Error('host seed lacks real memory overcommit');
  if (infraCases.unknown.memory?.total.known) throw new Error('unknown host memory total became known');
  const seeded = await page.evaluate(async (audit) => {
    const request = async (path, body) => {
      const r = await fetch('/api/v1' + path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const data = await r.json();
      if (!r.ok) throw new Error(path + ': ' + r.status + ' ' + JSON.stringify(data));
      return data;
    };
    for (const username of ['disabled-reader', 'disabled-helper']) {
      await request('/users', { username, password: 'local-disabled-password', role: 'member' });
      await request('/users', { username, disabled: true });
    }
    for (let n = 1; n <= 32; n++) {
      const username = `colleague-${String(n).padStart(2, '0')}`;
      await request('/users', { username, password: 'disposable-account-password', role: 'member' });
      if (n <= 30) {
        // Exercise real token authentication without replacing the administrator's cookie.
        const name = `camera-${username}`;
        const issued = await request('/tokens', { name, user: username, purpose: 'cli' });
        const seen = await fetch('/api/v1/auth/me', { credentials: 'omit', headers: { Authorization: `Bearer ${issued.token}` } });
        if (!seen.ok) throw new Error('colleague activity: ' + seen.status);
        const revoked = await fetch(`/api/v1/tokens/${name}`, { method: 'DELETE' });
        if (!revoked.ok) throw new Error('camera token revoke: ' + revoked.status);
      }
    }
    await request('/groups', { name: 'reviewers', description: 'Reviews team changes', members: ['sam', 'priya', 'colleague-01', 'colleague-02', 'never-signed-in'] });
    for (const name of ['support', 'editors', 'operations', 'readers']) await request('/groups', { name, members: ['sam', 'colleague-01'] });
    // The household's complaints are filed in TestWebUICapture (cameraComplaints).
    await request('/projects/club-newsletter/members', { grantee: 'sam', role: 'project-approver' });
    await request('/projects', { slug: 'shared-notes', name: 'Shared Notes', blurb: 'Notes for the household.' });
    if (!audit) await request('/projects/shared-notes/repo', { defaultBranch: 'main' });
    const chatChange = await request('/projects/club-newsletter/changes', { title: 'Update the club contact details', type: 'feature', body: 'Put the membership secretary’s new contact details on the newsletter.', workflow: '' });
    const blocks = await request('/guidance/blocks');
    const adr = await request('/projects/club-newsletter/adrs', { title: 'Keep newsletter pages static', tags: 'delivery' });
    await request('/users', { username: 'retired', password: 'disposable-password' });
    await request('/users', { username: 'retired', disabled: true });
    await request('/tokens', { name: 'sam-laptop', user: 'sam', purpose: 'cli' });
    await request('/tokens', { name: 'tom-laptop', user: 'tom', purpose: 'cli' });
    const invite = await request('/invites', { username: 'mei', label: 'Mei', role: 'member' });
    const changes = await request('/projects/club-newsletter/changes');
    const views = await Promise.all(changes.map(c => request('/changes/' + c.id)));
    const advisory = views.find(v => v.change.title === 'Shorten the page names');
    const failed = views.find(v => v.detail?.steps.some(s => s.status === 'fail'));
    const form = await request('/evals/start');
    const delivered = views.find(v => v.evidence?.releases?.some(r => r.serving && r.observedAt));
    if (!delivered) throw new Error('no delivery receipts in design seed');
    const source = views.find(v => v.change.state === 'shipped' && v.runs?.some(r => r.role === 'working' && r.workflowName === 'implement')).change;
    // The eval compares the project's setup (standard) with another one.
    const variant = form.profiles.find(p => p !== 'standard');
    if (!variant) throw new Error('the scene offers no second model profile to compare: ' + JSON.stringify(form.profiles));
    const evaluation = await request('/evals', { changes: [source.id], profile: variant, budgetDollars: 5, shown: form.meaning });
    const history = await request('/conformance/projects/club-newsletter/history');
    const review = history.history.at(-1); // the newest review, after the app declared its parts
    const whatif = await request('/conformance/what-if', { pack: 'conventions-only', version: 1, projects: ['club-newsletter'] });
    const remediation = await request('/conformance/remediations', { scope: 'all', concurrency: 1 });
    const backups = await request('/projects/club-newsletter/deployments/main/backups');
    const operation = await request('/projects/club-newsletter/deployments/main/backups/' + backups.backups.find(b => b.bytes > 0).id + '/restore', { confirm: 'main' });
    return { chat: chatChange.change.id, guidance: encodeURIComponent(blocks[0].name), advisory: advisory?.change.id, delivered: delivered.change.id, review: review.reviewId, adr: adr.id, invite: new URL(invite.link).pathname, failed: failed.change.id, evaluation: evaluation.id, whatif: whatif.id, remediation: remediation.remediation.id, operation: operation.operationId };
  }, !!process.env.DESIGN_AUDIT);
  phase('seeding');
  // The scene runs the work just seeded (the what-if, the restore, the
  // eval's pass) through the jobs' own handlers; nothing else runs them.
  const ran = await page.evaluate(async () => (await fetch('/?cameraSeeded=1')).status);
  if (ran !== 204) throw new Error('the scene did not run the seeded work: ' + ran);
  phase('seeded work');
  // The scene must look like a real install, or the shots show test noise
  // instead of the pages people will see. Every unrealistic fact found is
  // listed in one error (the readings are kept in realism.json).
  const realism = await page.evaluate(async seeded => {
    const read = async path => { const r = await fetch('/api/v1' + path); if (!r.ok) throw new Error(path + ': ' + r.status); return r.json(); };
    // Every object in a response, however deep.
    const objects = value => typeof value !== 'object' || value === null ? [] : [value, ...Object.values(value).flatMap(objects)];
    const r = {};
    for (const [key, path] of Object.entries({
      integrations: '/integrations', slots: '/model-slots', catalog: '/model-catalog', evalStart: '/evals/start', evaluation: '/evals/' + seeded.evaluation,
      store: '/backup-store', networks: '/auth/admin-networks', releases: '/projects/club-newsletter/releases', deployments: '/projects/club-newsletter/deployments',
      projects: '/projects', conformance: '/conformance', mixed: '/conformance/projects/lunch-orders/history', review: '/conformance/reviews/' + seeded.review,
      timing: '/timing', whatif: '/conformance/what-if/' + seeded.whatif, operation: '/operations/' + seeded.operation, jobs: '/jobs',
      chats: '/projects/club-newsletter/transcript', complaints: '/complaints', usage: '/usage', updates: '/instance-updates',
    })) {
      try { r[key] = await read(path); } catch (err) { r[key] = { unread: String(err) }; }
    }
    const text = key => JSON.stringify(r[key]);
    const problems = [];
    const want = (ok, what) => { if (!ok) problems.push(what); };
    for (const key of Object.keys(r)) want(!r[key]?.unread, 'could not read ' + key + ': ' + r[key]?.unread);
    // Models (f486, f677, f050, f198, f840, f321, f815).
    want(!objects(r.integrations).some(o => o.name === 'fake'), 'an integration is named "fake"');
    want(!/"fake\//.test(text('slots')), 'a task uses a fake/ model');
    want(!/could not be asked/.test(text('catalog')), 'the model catalog has a listing error');
    want((r.evalStart.profiles ?? []).length >= 2, 'only one model profile');
    want(new Set(objects(r.evaluation).map(o => o.profile).filter(Boolean)).size >= 2, 'the eval compares a profile with itself');
    want(objects(r.integrations).some(o => o.capability === 'llm'), 'no AI models connection for apps');
    // Addresses and names (f067, f077, f658, f726).
    want(!/127\.0\.0\.1|localhost|CAMERA_/.test(text('store')), 'the backup store has a test address or key name: ' + text('store'));
    want(!/"(127\.0\.0\.1|::1)"/.test(text('networks')), 'the sign-in page sees a loopback address');
    want(!/sha256:built/.test(text('releases')), 'an image digest is the fake\'s placeholder');
    // Deployments (f692, f718, f743, f693, f707, f736).
    const deploying = objects(r.releases).filter(o => o.number && ['deploying', 'releasing'].includes(o.state));
    want(deploying.length <= 1, `${deploying.length} releases are deploying at once`);
    const noServing = objects(r.deployments).filter(o => o.name && o.kind !== 'preview' && 'serving' in o && !o.serving);
    want(noServing.length <= 4, `${noServing.length} deployments have no serving release`);
    want(objects(r.projects).filter(o => o.slug && /running|working/.test(JSON.stringify(o))).length >= 2, 'fewer than two projects read running');
    // Checks (f165, f193, f771, f777).
    want(!/the tour (has|calls)/i.test(text('conformance') + text('mixed') + text('review')), 'a check reason names the tour');
    want(!/there is no \.apptown\/app\.yaml/.test(text('review')), 'the review shown predates the app declaring its parts');
    // Work run by workers (f467, f507, f1048).
    want(!/"(waiting|queued)"/.test(JSON.stringify(r.whatif?.status ?? r.whatif?.state ?? '')), 'the what-if never ran');
    want(!/"(waiting|queued)"/.test(JSON.stringify(r.operation?.status ?? r.operation?.state ?? '')), 'the restore never ran');
    // Chats, complaints, usage, version (f593, f594, f174, f869, f1005).
    want((r.chats.sessions ?? []).length >= 1, 'no ended project chat is recorded');
    const complaints = objects(r.complaints).filter(o => o.text && o.id);
    want(new Set(complaints.map(c => c.by ?? c.actor ?? c.author)).size >= 2, 'every complaint has one author');
    want(new Set(complaints.map(c => c.text)).size === complaints.length, 'complaints repeat');
    want(!/"dev-/.test(text('updates')), 'AppTown reports a development build');
    return { problems, readings: r };
  }, seeded);
  await writeFile(`${out}/realism.json`, JSON.stringify(realism, null, 2));
  if (realism.problems.length) throw new Error('the scene is unlike a real install:\n  ' + realism.problems.join('\n  '));
  phase('realism');
  if (process.env.DESIGN_AUDIT) await page.goto(base + '/?cameraPrepare=1', { waitUntil: 'networkidle' });
  const routes = {
    'target-settings': '/targets/design-preview', 'target-resources': '/targets/built-in-docker/resources', 'target-capacity': '/targets/built-in-docker/capacity', 'target-capacity-unknown': '/targets/design-preview/capacity', 'target-history': '/targets/built-in-docker/history', 'executor': '/integrations/executor',
    'backup-store-details': '/backups', 'release-invalid': '/releases/6', 'usage-90': '/usage', 'usage-details': '/usage', 'usage-timing': '/usage', 'people-revoke': '/people/agents-and-tokens', 'people-disconnect': '/people/agents-and-tokens',

    'guidance-block': '/guidance/' + seeded.guidance, 'guidance-missing': '/guidance/lang%2Fselection%2Fno-such-guidance', 'release-record': '/projects/club-newsletter/releases/1',
    home: '/', 'home-search': '/', 'overview-navigation': '/projects/club-newsletter', projects: '/projects', releases: '/releases', deployments: '/deployments', backups: '/backups',
    'project-deployments': '/projects/club-newsletter/deployments', fleet: '/deployments', 'deployment-infrastructure': '/targets', attention: '/attention', 'attention-checks': '/attention/checks', 'attention-condition': '/attention/checks/' + encodeURIComponent(infraCases.pending.items[0].key),
    // The garden shed's host answers but its disk cannot be read.
    'attention-condition-disk': '/attention/checks/' + encodeURIComponent((infraCases.pending.items.find(i => /garden-shed/.test(JSON.stringify(i)) && /disk/i.test(JSON.stringify(i))) ?? { key: 'missing-garden-shed-disk-condition' }).key), updates: '/updates',
    'target-offers': '/targets', 'release-detail': '/projects/club-newsletter/releases/' + facts.releases.find(r => r.serving).number,
    'memory-decisions-empty': '/projects/shared-notes/memory/decisions', 'target-cloud': '/targets/design-cloud', infrastructure: '/targets', jobs: '/jobs', 'infrastructure-new': '/targets/new',
    conformance: '/conformance', checks: '/conformance/checks', reviews: '/conformance/reviews', 'text-roles': '/text-roles',
    'what-ifs': '/conformance/what-ifs', remediations: '/conformance/remediations',
    integrations: '/integrations', slots: '/integrations/slots', 'service-kinds': '/integrations/services', secrets: '/integrations/secrets', notifications: '/integrations/notifications',
    janitor: '/janitor', packs: '/packs', workflows: '/workflows', guidance: '/guidance', policies: '/policies',
    'people-groups': '/people/groups', 'people-group': '/people/groups/reviewers', 'people-account': '/people/accounts/sam', 'people-roles': '/people/roles', 'people-signin': '/people/sign-in', 'people-agents': '/people/agents-and-tokens',
    evals: '/evals', 'eval-new': '/evals/new', people: '/people/accounts', account: '/account', usage: '/usage',
    complaints: '/complaints', 'complaints-answered': '/complaints/answered', 'complaints-archive': '/complaints/archive',
    glossary: '/docs/guides/glossary', 'setups-guide': '/docs/guides/setups',
    'model-profile': '/integrations/profiles/standard', 'target-new': '/targets/new',
    services: '/services', 'services-shared': '/services/shared', 'deployment': '/projects/club-newsletter/deployments/main', 'deployment-settings': '/projects/club-newsletter/deployments/main?tab=settings',
    'camera-not-found': '/camera-missing-page',
    'new-project': '/projects/_new', docs: '/docs', invite: '/invite',
    overview: '/projects/club-newsletter', 'project-changes': '/projects/club-newsletter/changes', 'project-console': '/projects/club-newsletter/console', console: `/changes/${seeded.chat}?tab=conversation`,
    'memory-notes': '/projects/club-newsletter/memory/notes',
    'memory-decisions': '/projects/club-newsletter/memory/decisions',
    'overview-help': '/projects/club-newsletter',
    security: '/projects/club-newsletter/security', suppressions: '/projects/club-newsletter/security/suppressions',
    release: '/projects/club-newsletter/release',
    'change-delivery': `/changes/${seeded.delivered}`,
    change: `/changes/${process.env.DESIGN_CHANGE}`,
    'change-progress': `/changes/${process.env.DESIGN_PROGRESS}?tab=overview`,
    'change-non-approver': `/changes/${process.env.DESIGN_CHANGE}`,
    'change-failed': `/changes/${seeded.failed}`, 'overview-empty': '/projects/shared-notes',
    'eval-scorecard': `/evals/${seeded.evaluation}`, 'what-if': `/conformance/what-ifs/${seeded.whatif}`,
    remediation: `/conformance/remediations/${seeded.remediation}`, operation: `/operations/${seeded.operation}`,
    'invite-pending': seeded.invite,
    'conformance-project': '/conformance/projects/club-newsletter',
    'conformance-project-mixed': '/conformance/projects/lunch-orders',
    'deployment-infrastructure-help': '/targets/built-in-docker',
    'people-actions': '/people/accounts',
    'review': `/conformance/reviews/${seeded.review}`,
    'review-checks': `/conformance/reviews/${seeded.review}/checks`,
    'review-project': `/conformance/reviews/${seeded.review}/projects/club-newsletter`,

  };
  for (const tab of ['general', 'working', 'models', 'services', 'secrets', 'members', 'usage']) routes['settings-' + tab] = '/projects/club-newsletter/settings/' + tab;
  // Detail links come from the actual seeded records.
  for (const [screen, path, pattern] of [
    ['pack', '/packs', /^\/packs\/[^/]+$/],
    ['target', '/targets', /^\/targets\/(?!new)[^/]+$/],
    ['integration', '/integrations', /^\/integrations\/(?!slots|services|secrets|targets)[^/]+$/],
    ['adr', '/projects/club-newsletter/memory/decisions', /^\/adrs\/[^/]+$/],
    ['doc', '/docs', /^\/docs\/[^/]+\/[^/]+$/],
  ]) {
    await page.goto(base + path, { waitUntil: 'networkidle' });
    try { await page.waitForFunction(source => [...document.querySelectorAll('main a[href]')].some(el => new RegExp(source).test(el.getAttribute('href'))), pattern.source); }
    catch (err) {
      await mkdir(out, { recursive: true });
      await page.screenshot({ path: `${out}/discovery-error.png`, fullPage: true });
      throw new Error(`${screen}: ${await page.locator('body').innerText()} ${err}; browser errors ${JSON.stringify(errors)}`);
    }
    const links = await page.locator('main a[href]').evaluateAll(els => els.map(el => el.getAttribute('href')));
    const detail = links.find(href => pattern.test(href));
    if (detail) routes[screen] = screen === 'target' ? '/targets/built-in-docker' : detail;
    if (screen === 'target') routes['target-unknown'] = '/targets/design-preview';
    if (!detail) throw new Error(`No detail link for ${screen}`);
  }
  await page.goto(`${base}/changes/${seeded.delivered}?tab=conversation`, { waitUntil: 'networkidle' });
  await page.locator('a[href*="/work/"]').first().waitFor();
  const workRecords = await page.locator('a[href*="/work/"]').evaluateAll(els => [...new Set(els.map(el => el.getAttribute('href')))]);
  for (const href of workRecords) {
    await page.goto(base + href, {waitUntil:'networkidle'});
    const artifact = page.locator('main a[href*="/records/"]').first();
    if (await artifact.count()) {
      routes['work-record'] = href;
      routes['work-record-artifact'] = await artifact.getAttribute('href');
      break;
    }
  }
  if (!routes['work-record-artifact']) throw new Error('No seeded work session has an artifact');
  const workURL = new URL(routes['work-record'], base);
  for (const part of ['request','workflow','report']) routes['work-record-'+part] = `${workURL.pathname}/${part}${workURL.search}`;
  routes['change-code'] = `/changes/${process.env.DESIGN_CHANGE}?tab=changes`;
  routes['change-findings'] = `/changes/${process.env.DESIGN_CHANGE}/records/checks`;
  for (const tab of ['overview', 'conversation', 'details']) routes['change-' + tab] = `/changes/${process.env.DESIGN_CHANGE}?tab=${tab}`;
  Object.assign(routes, { 'new-project-way': '/projects/_new', 'new-project-repository': '/projects/_new', 'pack-new': '/packs', 'policy-new': '/policies/new', 'workflow-new': '/workflows', 'infrastructure-connection': '/targets/new' });
  if (process.env.DESIGN_AUDIT) routes['change-advisory'] = `/changes/${seeded.advisory}`;
  if (process.env.DESIGN_AUDIT) routes['reviews-running'] = '/conformance/reviews?cameraRunning=1';
  Object.assign(routes, { features: '/features', 'feature-off': '/evals' });
  // Pages in a state a person opens: the Targets page with a section's help
  // open, a connection's access, a stopped deployment, a change's preview
  // record (shotOpen below opens them).
  Object.assign(routes, {
    'infrastructure-expanded': '/targets', 'people-agents-expanded': '/people/agents-and-tokens',
    'deployment-stopped': '/projects/club-newsletter/deployments/print-layout',
    'change-record-preview': `/changes/${process.env.DESIGN_CHANGE}/records/preview`,
  });
  const resource = await page.evaluate(async () => {
    const data = await (await fetch('/api/v1/infrastructure')).json();
    const resources = data.targets.find(t => t.target === 'built-in-docker').resources;
    // A network the scene's preview made; linked as the Resources list links it.
    return resources.find(r => r.kind === 'network') ?? { missing: resources.map(r => `${r.kind} ${r.name} ${r.record} ${r.status ?? ''}`) };
  });
  if (resource.missing) throw new Error('resource detail seed missing: ' + resource.missing.join('; '));
  routes['target-resource'] = '/targets/built-in-docker/resources/' + encodeURIComponent(resource.kind + ':' + (resource.id || resource.name));
  routes['target-resources-filtered'] = '/targets/built-in-docker/resources?deployment=club-newsletter%2Fmain';
  const linked = await page.evaluate(async () => {
    const read = async path => { const r = await fetch('/api/v1' + path); if (!r.ok) throw new Error(path + ': ' + r.status); return r.json(); };
    const [policies, workflows, generated, integrations, conformance, transcript, docs] = await Promise.all([
      read('/policy-library'), read('/workflows/library'), read('/guidance/generated'), read('/integrations'), read('/conformance'), read('/projects/club-newsletter/transcript'), read('/docs'),
    ]);
    return { policy: policies.library[0].name, workflow: workflows[0].name, generated: generated.generated[0]?.name,
      integration: integrations.integrations[0].name, service: (integrations.integrations.find(b => ['llm','errors'].includes(b.capability)) || integrations.integrations[0]).name,
      check: Object.keys(conformance.checkDefs)[0], session: transcript.sessions?.[0]?.id, doc: docs.integrations[0] };
  });
  Object.assign(routes, {
    'integration-doc': `/docs/${linked.doc.capability}/${linked.doc.provider}`,
    'integration-doc-admin': `/docs/${linked.doc.capability}/${linked.doc.provider}`,
    'profile-projects': '/integrations/profiles/standard/projects',
    'guidance-projects': `/guidance/${seeded.guidance}/projects`,
    'policy': `/policies/${encodeURIComponent(linked.policy)}`,
    'policy-projects': `/policies/${encodeURIComponent(linked.policy)}/projects`,
    'workflow': `/workflows/${encodeURIComponent(linked.workflow)}`,
    'pack-version': `${routes.pack}/versions/1`, 'pack-projects': `${routes.pack}/projects`,
    'integration-projects': `/integrations/${linked.integration}/projects`, 'service-projects': `/services/${linked.service}/projects`,
    'check-projects': `/conformance/checks/${encodeURIComponent(linked.check)}/projects`,
    'review-check-projects': `/conformance/reviews/${seeded.review}/checks/${encodeURIComponent(linked.check)}/projects`,
    'check-record': `/conformance/checks/${encodeURIComponent(linked.check)}`,
    'review-check-record': `/conformance/reviews/${seeded.review}/checks/${encodeURIComponent(linked.check)}`,
    'what-if-project': `/conformance/what-ifs/${seeded.whatif}/projects/club-newsletter`,
    'past-chats': '/projects/club-newsletter/chats', 'past-chat': `/projects/club-newsletter/chats/${process.env.DESIGN_PROJECT_CHAT}`,
    'release-log': '/projects/club-newsletter/releases/1/log',
  });
  const deployScenes = {
    preparing: ['Code', 'getting the code ready'], 'backing-up': ['Backup', 'backing up the data'],
    building: ['Build', 'building the app'], starting: ['Start', 'starting the app'],
    checking: ['Check', 'checking the app answers'], live: ['Live', 'live'], failed: ['Start', 'starting the app'],
  };
  // The deployments TestWebUICapture holds at each stage (cameraDeployments there).
  // One release is mid-deploy (members-preview's), moved to each stage
  // before its pages are shot (heldStages, final stops); staging's has just
  // gone live and archive-migration's failed.
  const stageDeployments = { live: 'staging', failed: 'archive-migration' };
  const heldStages = ['preparing', 'backing-up', 'building', 'starting', 'checking'];
  Object.keys(deployScenes).forEach(stage => { routes['deployment-' + stage] = '/projects/club-newsletter/deployments/' + (stageDeployments[stage] ?? 'members-preview'); });
  routes['automatic-guidance'] = '/guidance/generated';
  if (linked.generated) routes['generated-guidance'] = `/guidance/generated/${encodeURIComponent(linked.generated)}`;
  await page.goto(`${base}/changes/${seeded.delivered}?tab=details`, {waitUntil:'networkidle'});
  routes['change-review-record'] = `/changes/${seeded.delivered}/records/review`;
  routes['change-record'] = await page.locator('a[href*="/records/"]').first().getAttribute('href');
  routes['change-artifact'] = await page.locator('a[href*="/records/record-"]').first().getAttribute('href');

  // TestWebUICapture fills DESIGN_PAGES from APPTOWN_DESIGN_PAGES; set that
  // name outside (the test refuses DESIGN_PAGES itself).
  phase('discovery');
  const selected = new Set((process.env.DESIGN_PAGES || '').split(',').filter(Boolean));
  for (const name of selected) if (name !== 'login' && !(name in routes)) throw new Error('unknown camera page: ' + name);
  console.log(`camera: shooting ${selected.size || Object.keys(routes).length + 1} of ${Object.keys(routes).length + 1} pages`);
  const captures = [];
  const variants = { desktop: { width: 1440, height: 1000 }, phone: { width: 390, height: 844 }, 'desktop-large': { width: 1440, height: 1000 }, 'phone-large': { width: 390, height: 844 } };
  const finalPaths = new Set();
  const disclosures = [];
  // Tabs write the progress files one after another, never at once.
  let writing = Promise.resolve();
  const saveProgress = () => (writing = writing.then(() => Promise.all([
    writeFile(`${out}/pages-progress.json`, JSON.stringify(captures, null, 2)),
    writeFile(`${out}/layout-errors.json`, JSON.stringify(errors, null, 2)),
    saveTimings(),
  ])));
  await writeFile(`${out}/content-exclusions.json`, JSON.stringify(cameraContentExclusions, null, 2));
  const changedFirst = name => /projects$|^work-record|^change-(record|artifact|review-record)|^policy$|^workflow$|^pack-version$|guidance/.test(name)
  // A finished deploy keeps its scene for ten minutes (DeploymentDeploys'
  // sceneAfterMs), so the live and failed deploy scenes, seeded before the
  // camera starts, go first: a full run takes longer than that. (The held
  // stages are final stops, each moved to just before it is shot.)
  const timed = name => /^deployment-(live|failed)$/.test(name)
  const rank = name => timed(name) ? 2 : Number(changedFirst(name))
  const orderedRoutes = Object.entries(routes).sort(([a], [b]) => rank(b) - rank(a))
  // Pages that reload at large text instead of switching in place: their
  // checks compare a row with its own first drawing.
  const reloadForLarge = {
    deployments: 'rows are compared with their first-drawn height',
    fleet: 'rows are compared with their first-drawn height',
    overview: 'rows are compared with their first-drawn height',
  };
  // What a page looks like just after it loaded; a page is switched to large
  // text in place only when it is back in that state.
  const pageState = page => page.evaluate(() => ({
    url: location.href,
    text: (document.querySelector('main') ?? document.body).innerText,
    expanded: document.querySelectorAll('[aria-expanded="true"]').length,
    open: document.querySelectorAll('details[open]').length,
    overlays: document.querySelectorAll('[role="dialog"], .popover').length,
    focus: document.activeElement === document.body || !document.activeElement ? '' : document.activeElement.outerHTML.slice(0, 120),
  }));
  // Large text by the product's own switch: the root's data-size, as
  // applySize sets it (without its browser-storage write). Themes are always
  // loaded fresh: the chat terminal reads its colours only when it opens.
  const sizeInPlace = async (page, screen, loaded) => {
    if (reloadForLarge[screen]) return 'reload: ' + reloadForLarge[screen];
    // A fresh load focuses nothing; the medium shot's disclosure check leaves
    // its trigger focused, which would draw a focus ring in the large shot.
    await page.evaluate(unfocused => { window.scrollTo(0, 0); if (unfocused) document.activeElement?.blur(); }, loaded.focus === '');
    const now = await pageState(page);
    const changed = Object.keys(now).filter(key => now[key] !== loaded[key]);
    if (changed.length) return 'reload: changed ' + changed.join(', ');
    await page.evaluate(() => { document.documentElement.dataset.size = 'large'; });
    await page.evaluate(() => document.fonts.ready.then(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))));
    return 'switched';
  };
  // One page's shots: per width and theme, a fresh load at medium text, then
  // large text switched in place (or reloaded, with the reason recorded).
  const shootPage = async (page, screen, path, tab) => {
    await mkdir(`${out}/${screen}`, { recursive: true });
    for (const width of ['desktop', 'phone']) {
      for (const theme of (process.env.DESIGN_AUDIT ? [width === 'phone' ? 'dark' : 'light'] : ['light', 'dark'])) {
        let loaded;
        for (const size of [width, width + '-large']) {
          const viewport = variants[size];
          const timer = shotTimer(screen, size, theme);
          timer.shot.tab = tab;
          const how = size === width ? 'fresh' : await sizeInPlace(page, screen, loaded);
          timer.shot.how = how;
          if (how !== 'switched') {
            await page.setViewportSize(viewport);
            const captureURL = new URL(path, base);
            captureURL.searchParams.set('theme', theme);
            captureURL.searchParams.set('size', size.endsWith('-large') ? 'large' : 'medium');
            await page.goto(captureURL.href, { waitUntil: 'networkidle' });
            await page.locator('#root > *').first().waitFor({ state: 'visible' });
          }
          timer.lap('load');
          await page.waitForFunction(() => ![...document.querySelectorAll('main .muted')].some(el => el.textContent === 'Loading…'));
          if (how === 'fresh') loaded = await pageState(page);
          timer.lap('settle');
          if (screen === 'change-progress') {
            const panel = page.locator('.ch-now');
            await panel.waitFor();
            if (!(await panel.innerText()).includes('Standards check')) throw new Error('progress must use the standards step label');
            const passed = await panel.locator('.ok').evaluateAll(els => {
              const probe = document.createElement('span'); probe.style.color = 'var(--ok)'; document.body.append(probe);
              const green = getComputedStyle(probe).color; probe.remove();
              return els.length > 0 && els.every(el => getComputedStyle(el).color === green);
            });
            if (!passed || !await panel.locator('[aria-label="pending"]').count()) throw new Error('progress needs shared passed colours and a pending mark');
          }
          if (screen === 'features') {
            const clipped = await page.locator('main tbody td').evaluateAll(cells => cells.filter(el => {
              const style = getComputedStyle(el);
              return style.whiteSpace === 'nowrap' || el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1;
            }).map(el => el.textContent));
            if (clipped.length) throw new Error('Features facts must wrap without clipping: ' + clipped.join(', '));
            // Each feature has one switch; the ? beside it is a help button, not a switch.
            for (const row of await page.locator('main tbody tr').all()) {
              const switches = await row.locator('button[role="switch"]').evaluateAll(buttons => buttons.map(b => b.getAttribute('aria-checked')));
              if (switches.length !== 1 || !['true', 'false'].includes(switches[0])) throw new Error('Features must use accessible switches');
            }
          }
          if (screen === 'feature-off' && await page.locator('main a[href="/features"]').count() !== 1) throw new Error('administrator refusal must link Features');
          if (['overview-help', 'memory-notes', 'memory-decisions'].includes(screen)) {
            const title = screen === 'overview-help' ? 'Overview' : screen === 'memory-notes' ? 'Notes' : 'Decisions';
            await page.getByRole('button', { name: 'About ' + title, exact: true }).click();
            if (!await page.locator('.page-help-pop a, [role="dialog"] .page-help-text a').first().isVisible()) throw new Error('missing guide link');
            // The help was opened to check its link; the page's own shot shows the page.
            await page.keyboard.press('Escape');
            if (screen === 'overview-help' && await page.locator('main').getByText('What agents know about this project', { exact: true }).count()) throw new Error('notes remain on Overview');
            if (screen === 'overview-help' && await page.locator('main').getByText('Changes →', { exact: true }).count()) throw new Error('Changes link remains on Overview');
          }
          if (screen === 'target') {
            const recorded = await page.evaluate(async () => {
              const target = await (await fetch('/api/v1/targets/built-in-docker')).json();
              const infra = await (await fetch('/api/v1/infrastructure')).json();
              const dep = target.target.deployments.find(d => d.project === 'club-newsletter' && d.name === 'main');
              const resources = infra.targets.find(t => t.target === 'built-in-docker').resources;
              return { ...dep, count: resources.filter(r => r.hosts?.some(h => h.project === dep.project && h.deployment === dep.name)).length };
            });
            if (!recorded.generation || recorded.count < 2) throw new Error('target seed must have recorded generation and resources');
            const row = page.locator('.target-deployments-table tbody tr').filter({ has: page.locator('a[href="/projects/club-newsletter/deployments/main"]') });
            if (await row.locator('[data-label="Resources"] a').textContent() !== `${recorded.count} resources`) throw new Error('recorded resource count disappeared');
            if (await row.locator('[data-label="Generation"]').textContent() !== String(recorded.generation)) throw new Error('recorded generation disappeared');
            if (await row.locator('[data-label="Address"]').textContent() !== (recorded.url?.replace(/^https?:\/\//, '') || 'unknown')) throw new Error('recorded address disappeared');
          }
          if (screen === 'infrastructure' && size.startsWith('desktop')) {
            const aligned = await page.locator('section').evaluateAll(sections => sections.every(section => {
              const rows = [...section.querySelectorAll('.record-table tr')];
              if (rows.length < 2) return true;
              const reference = [...rows[0].children].map(cell => cell.getBoundingClientRect().left);
              return rows.every(row => [...row.children].every((cell, i) => Math.abs(cell.getBoundingClientRect().left - reference[i]) <= 1));
            }));
            if (!aligned) throw new Error('connection column headers and rows are misaligned');
          }
          if (screen.startsWith('deployment-') && screen.slice('deployment-'.length) in deployScenes) {
            const stage = screen.slice('deployment-'.length);
            const [word, description] = deployScenes[stage];
            const scene = page.locator('.deployment-deploy-card .deploy-scene');
            const active = scene.locator('li[aria-current="step"]');
            if (await scene.count() !== 1 || await scene.evaluate(el => el.classList.contains('paused')) ||
                await active.count() !== 1 || !(await active.innerText()).includes(word)) throw new Error('deployment scene has no active ' + stage + ' stage');
            if (stage === 'failed') {
              if (await scene.locator('.bad').count() !== 1 || await scene.locator('svg').getAttribute('aria-label') !== 'Stopped while ' + description) throw new Error('deployment failure did not light its failed stage');
            } else if (stage !== 'live') {
              // Preparing has no picture: it would draw only the plan's outline.
              if (await active.locator('.busy').count() !== 1 || (stage === 'preparing' ? await scene.locator('svg').count() !== 0 : await scene.locator('svg').getAttribute('aria-label') !== 'Deploying: ' + description) ||
                  !(await page.locator('.ch-deploy-progress').innerText()).match(new RegExp(description + ' · \\d'))) throw new Error('deployment scene is not running at ' + stage + ' with time in stage');
            } else if (await scene.locator('svg').getAttribute('aria-label') !== 'The app is live') throw new Error('released deployment is not live');
          }
          if (screen === 'deployment') {
            const waiting = facts.releases.find(r => r.deployment === 'main' && r.state === 'waiting for approval');
            if (!waiting || !waiting.decision.canApprove || !waiting.decision.canCancel) throw new Error('deployment scene needs a waiting release the viewer can decide');
            const words = await page.locator('main').innerText();
            const action = waiting.approvalRole ? 'Approve' : 'Release';
            if (!words.includes(waiting.decision.meaning) || !waiting.decision.meaning.includes('running on Production') ||
                await page.getByRole('button', { name: action, exact: true }).count() !== 1 ||
                await page.getByRole('button', { name: 'Cancel', exact: true }).count() !== 1) throw new Error('waiting release is missing its meaning or doors');
            if (waiting.approvalRole) {
              if (!words.includes(`Waiting for approval from ${waiting.approvalRoleTitle || waiting.approvalRole}.`)) throw new Error('approval wait is missing its required role');
            } else if (!waiting.decision.waitReason || !words.includes(waiting.decision.waitReason) || words.includes('Waiting for approval from')) {
              throw new Error('personal release wait is missing its reason');
            }
            if (!process.env.DESIGN_BASELINE) {
              // Restoring is on the deployment's ⋯ menu: pick a copy, then the question.
              await page.getByRole('button', { name: /^More actions for /, exact: false }).first().click();
              await page.getByRole('button', { name: 'Restore from a backup…', exact: true }).click();
              await page.getByRole('dialog').locator('input[type="radio"]').first().check();
              await page.getByRole('button', { name: 'Restore', exact: true }).waitFor();
              await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-restore-question.png`, fullPage: true });
              await page.keyboard.press('Escape');
            }
          }
          if (!process.env.DESIGN_BASELINE && !cameraContentExclusions[screen] && (screen === 'conformance' || screen === 'conformance-project-mixed')) {
            const notes = page.locator('.conformance-broken');
            if (!(await notes.count())) throw new Error('unavailable checks must remain visible');
            const colors = await notes.evaluateAll(els => els.map(el => getComputedStyle(el).color));
            const red = await page.locator('main').evaluate(el => {
              const probe = document.createElement('span');
              probe.style.color = 'var(--bad)'; el.append(probe);
              const color = getComputedStyle(probe).color; probe.remove(); return color;
            });
            if (colors.some(color => color !== red)) throw new Error('failed execution must stay red beside amber findings');
          }
          if (screen === 'deployment-infrastructure-help') {
            const check = page.getByTestId('target-check');
            await check.waitFor({ state: 'visible' });
            const reading = await page.evaluate(async () => (await (await fetch('/api/v1/targets/built-in-docker')).json()).target.check);
            const facts = [reading.detail, reading.failure].filter(Boolean);
            const words = await check.innerText();
            if (!words.startsWith('Target check:') || !facts.length || facts.some(fact => !words.includes(fact))) throw new Error('target check facts are missing');
          }
          if (screen === 'target-resources' && !await page.locator('th').filter({ hasText: 'Owned' }).count()) throw new Error('resource table is missing');
          if (!process.env.DESIGN_BASELINE && screen === 'packs') {
            const boxed = await page.locator('.pack-actions button').evaluateAll(els => els.some(el => getComputedStyle(el).borderTopWidth !== '0px'));
            if (boxed) throw new Error('pack actions must be plain links');
          }
          if (!process.env.DESIGN_BASELINE && screen === 'projects' && size.startsWith('phone')) {
            const rows = page.locator('.project-list .project-row');
            if (await rows.locator(':scope[aria-expanded]').count()) throw new Error('project rows must not expand');
            for (const row of await rows.all()) {
              const name = await row.locator('.project-name').boundingBox();
              const actions = await row.locator('.project-action').boundingBox();
              if (!name || !actions || actions.y < name.y + name.height - 1) throw new Error('project actions run into its name');
              const facts = row.locator('.project-summary');
              await facts.waitFor({ state: 'visible' });
              if (!/\d+ shipped/.test(await facts.innerText())) throw new Error('phone project hides shipped facts');
            }
          }
          if (screen === 'integration-doc-admin') await page.getByRole('button', {name:'Admin',exact:true}).click();
          if (screen === 'usage-90') {
            await page.getByRole('button', {name:'90d',exact:true}).click();
            await page.waitForFunction(() => document.querySelectorAll('.usage-col').length === 90);
          }
          if (screen === 'usage-details') await page.getByRole('button', { name: 'Details', exact: true }).click();
          if (screen === 'usage-timing') await page.getByRole('button', { name: 'Time spent', exact: true }).click();
          if (screen === 'backup-store-details') await page.getByRole('region', { name: 'Backup store', exact: true }).getByText('household-copies', { exact: true }).waitFor();
          if (screen === 'people-revoke' || screen === 'people-disconnect') await page.getByRole('button', { name: screen === 'people-revoke' ? 'revoke' : 'disconnect', exact: true }).first().click();
          if (screen === 'people-actions') await page.getByRole('button', { name: 'Actions for sam', exact: true }).click();
          if (screen === 'infrastructure-expanded') {
            await page.getByRole('button', { name: 'About Cloud accounts', exact: true }).click();
            await page.locator('.page-help-pop, [role="dialog"]').first().waitFor();
          }
          if (screen === 'people-agents-expanded') {
            await page.getByRole('button', { name: 'Permissions for ChatGPT as sam', exact: true }).first().click();
            await page.getByRole('dialog').waitFor();
          }
          // A credential's confirmation is a sized dialog on screen; it does not cover its row.
          if (['people-revoke', 'people-disconnect'].includes(screen)) {
            const box = await page.getByRole('dialog').boundingBox();
            const view = page.viewportSize();
            if (!box || box.x < 0 || box.y < 0 || box.x + box.width > view.width + 0.5 || box.y + box.height > view.height + 0.5) throw new Error(`${screen}: the confirmation is not a dialog on screen at ${size}: ${JSON.stringify(box)}`);
          }
          // An account's actions open beside the control that opened them; on a
          // phone they are a dialog named for the account, on screen.
          if (screen === 'people-actions' && size.startsWith('phone')) {
            const box = await page.getByRole('dialog', { name: 'sam', exact: true }).boundingBox();
            const view = page.viewportSize();
            if (!box || box.x < 0 || box.y < 0 || box.x + box.width > view.width + 0.5 || box.y + box.height > view.height + 0.5) throw new Error(`${screen}: the account's actions are not a dialog on screen at ${size}: ${JSON.stringify(box)}`);
          } else if (screen === 'people-actions') {
            const trigger = page.locator('main [aria-expanded="true"]').first();
            const [panel, control] = await Promise.all([page.locator('.popover').boundingBox(), trigger.boundingBox()]);
            if (!panel || !control || Math.min(Math.abs(panel.y - (control.y + control.height)), Math.abs(control.y - (panel.y + panel.height))) > 8) throw new Error(`${screen}: the menu is not beside its control at ${size}: ${JSON.stringify({ panel, control })}`);
          }
          if (screen.startsWith('new-project-')) {
            await page.locator('#project-description').fill('A calendar for the household');
            await page.getByPlaceholder('Suggested from your description').fill('Family Calendar');
            await page.getByRole('button', { name: 'Next →', exact: true }).click();
            await page.getByRole('button', { name: 'Choose a way of working myself →', exact: true }).click();
            await page.locator('.pack-card').filter({ hasText: 'conventions-only' }).waitFor();
            if (screen === 'new-project-repository') {
              await page.locator('.pack-card').filter({ hasText: 'No packs' }).click();
              await page.getByRole('button', { name: 'Use no packs', exact: true }).click();
              await page.getByRole('button', { name: 'Next →', exact: true }).click();
              await page.getByRole('radio', { name: /Default target/ }).waitFor();
              // Both sections use the same divider, inset and heading-to-options gap.
              const sections = await page.evaluate(() => {
                const measure = text => {
                  const heading = [...document.querySelectorAll('.np-panel strong')].find(el => el.textContent === text);
                  const section = heading.parentElement.tagName === 'LEGEND'
                    ? heading.parentElement.parentElement.parentElement : heading.parentElement.parentElement;
                  const box = heading.getBoundingClientRect();
                  const option = section.querySelector('label').getBoundingClientRect();
                  const style = getComputedStyle(section);
                  return { left: box.left, inset: box.top - section.getBoundingClientRect().top,
                    gap: option.top - box.bottom, border: style.borderTopWidth, borderStyle: style.borderTopStyle };
                };
                return { start: measure('Start from'), target: measure('Where it runs') };
              });
              if (sections.start.borderStyle !== 'solid' || sections.target.borderStyle !== 'solid' ||
                  sections.start.border !== sections.target.border ||
                  ['left', 'inset', 'gap'].some(key => Math.abs(sections.start[key] - sections.target[key]) > 1)) {
                throw new Error('repository sections differ in divider, alignment or spacing: ' + JSON.stringify(sections));
              }
            }
          }
          if (screen === 'home-search') {
            await page.locator('button[aria-label="Search"]').click();
            await page.getByRole('textbox', { name: 'find', exact: true }).fill('club');
            await page.locator('.topbar-find-menu button').filter({ hasText: 'Club Newsletter' }).waitFor();
            const clipped = await page.locator('.topbar-find-menu button').evaluateAll(els => els.some(el => el.scrollWidth > el.clientWidth + 1));
            if (clipped) throw new Error('search result text overflows its control');
          }
          if (screen === 'overview-navigation' && size.startsWith('phone')) await page.getByRole('button', { name: 'open the menu', exact: true }).click();
          if (screen === 'pack-new') {
            await page.getByRole('button', { name: 'New pack', exact: true }).click();
            await page.getByRole('button', { name: 'adopt-integration', exact: true }).waitFor();
            await page.locator('.packs-chip').filter({ hasText: 'api/rest-pragmatist' }).waitFor();
          }
          if (screen === 'policy-new') await page.getByRole('heading', { name: 'New policy', exact: true }).waitFor();
          if (screen === 'workflow-new') await page.getByRole('link', { name: 'New workflow', exact: true }).click();
          if (screen === 'infrastructure-connection') { await page.getByRole('button', { name: /Docker host/ }).click(); await page.getByRole('link', { name: 'Setup guide', exact: true }).waitFor(); }
          if (screen === 'change-findings') {
            await page.getByRole('link', { name: /^Security › Findings \(/ }).first().waitFor();
            if (await page.getByRole('button', { name: /^(Fix…|Suppress…)$/ }).count()) throw new Error('finding actions remain on the change');
          }
          if (screen === 'settings-models') {
            await page.getByText('This project uses', { exact: false }).waitFor();
            const chat = page.getByRole('row').filter({ has: page.getByText('Chat', { exact: true }) });
            await chat.waitFor();
            const modelCell = await chat.locator('td').nth(1).innerText();
            if (modelCell !== 'openrouter/anthropic/claude-haiku-5.5') throw new Error(`unexpected Chat model: ${modelCell}`);
            const plan = page.getByRole('row').filter({ has: page.getByText('Plan', { exact: true }) });
            const inherited = await plan.locator('td').nth(1).innerText();
            if (inherited !== 'openrouter/anthropic/claude-haiku-5.5 · from Main · strong') throw new Error(`unexpected Plan model: ${inherited}`);
            for (const row of await page.locator('tbody tr').all()) {
              const name = await row.locator('strong').innerText();
              if ((await row.locator('.muted').innerText()).startsWith(name + ':')) throw new Error(`repeated task description: ${name}`);
            }
            if (await page.getByRole('combobox', { name: 'Model profile', exact: true }).isDisabled()) throw new Error('administrator cannot select a model profile');
          }
          await page.waitForLoadState('networkidle');
          await page.waitForFunction(() => ![...document.querySelectorAll('main .muted')].some(el => el.textContent === 'Loading…'));
          if (!process.env.DESIGN_BASELINE) await page.getByRole('heading', { level: 1 }).first().waitFor();
          await page.evaluate(() => document.fonts.ready);
          if (await page.locator('.update-note').count()) throw new Error(screen + ': false development update notice');
          if (!process.env.DESIGN_BASELINE && await page.locator('.main').count()) {
            const background = await page.locator('body').evaluate(el => getComputedStyle(el).backgroundColor);
            const expected = theme === 'light' ? 'rgb(245, 246, 246)' : 'rgb(23, 27, 29)';
            if (background !== expected) throw new Error(screen + ': wrong design tokens ' + background);
          }
          await page.addStyleTag({ content: '*,*::before,*::after { animation: none !important; transition: none !important; caret-color: transparent !important; }' });
          const words = await page.locator('body').innerText();
          await writeFile(`${out}/${screen}/${size}-${theme}.txt`, words);
          if (screen === 'janitor') {
            const approvals = page.getByRole('table', { name: 'Janitor approvals', exact: true });
            for (const name of ['Dependency updates', 'Keep packs current']) {
              const row = approvals.getByRole('row').filter({ hasText: name }).filter({ hasText: 'All projects, including new ones' });
              await row.getByRole('button', { name: 'Preview', exact: true }).waitFor({ state: 'visible' });
              if (await row.getByRole('button', { name: 'Preview', exact: true }).isDisabled()) throw new Error(name + ': owner cannot preview an off rule');
            }

          }
          if (screen === 'target' && await page.locator('.capacity-strip-track').count() !== 3) throw new Error('target capacity strip is missing');
          if (screen === 'target-capacity' && !words.includes('over by')) throw new Error('capacity limit warning missing');
          // The scene's host reports what it runs, so these pages show readings
          // with real values, not "unknown".
          if (!process.env.DESIGN_BASELINE) {
            const lines = words.split('\n').map(line => line.trim());
            const readings = { deployment: ['CPU', 'MEMORY'], target: ['CPU', 'MEMORY', 'DISK'] }[screen] ?? [];
            for (const label of readings) {
              if (!lines.includes(label) || lines[lines.indexOf(label) + 1] === 'unknown') throw new Error(`${screen}: ${label} was not read`);
            }
            if (screen === 'deployment' && (!words.includes('/ 256 MB') || words.includes('did not report what every container uses'))) throw new Error('deployment: its parts do not report their use against their limits');
            if (screen === 'check-record' && words.includes('is turned off')) throw new Error('check-record: an earlier shot left its feature turned off');
            // A deploy under way names what is served, never the release on its
            // way, and does not offer a second deploy admission would refuse.
            if (/^deployment-(preparing|building|backing-up|starting|checking)$/.test(screen)) {
              if (/Serving release \d/.test(words)) throw new Error(screen + ': the release still on its way is called served');
              const deploy = page.getByRole('button', { name: 'Deploy latest', exact: true });
              if (await deploy.count() && await deploy.first().isEnabled()) throw new Error(screen + ': Deploy latest offered mid-deploy');
            }
            // A reading taken just now is not old.
            if (screen === 'attention' && lines.some((line, i) => line.startsWith('●old ·') && /just now$/.test(lines[i - 1] ?? ''))) throw new Error('attention: a condition seen just now is marked old');
          }
          if ((screen === 'change' || screen === 'change-non-approver') && !process.env.DESIGN_BASELINE) {
            const eligible = screen === 'change';
            for (const name of [/^Accept(?: and put live)?$/, 'Send back…']) {
              if (await page.locator('#decision').getByRole('button', { name, exact: true }).count() !== (eligible ? 1 : 0)) throw new Error(screen + ': wrong approval eligibility for ' + name);
            }
          }
          if (screen === 'change' && !process.env.DESIGN_BASELINE) {
            const decision = page.locator('#decision');
            await decision.waitFor();
            const covered = await decision.locator('.decision-form').evaluate(form => {
              const choices = form.querySelector('.ch-decision-choices').getBoundingClientRect();
              return [...form.querySelectorAll('.decision-meaning')].some(meaning => meaning.getBoundingClientRect().bottom > choices.top);
            });
            if (covered) throw new Error('decision controls cover the approval meaning');
            if (await decision.evaluate(el => el.scrollWidth > el.clientWidth)) throw new Error('decision screen overflows');
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-decision.png` });
          }
          if (!process.env.DESIGN_BASELINE) {
            const clipped = await page.locator('.topbar > .account-menu > button').evaluateAll(els => els.some(el => {
              const r = el.getBoundingClientRect(); return r.left < 0 || r.right > innerWidth;
            }));
            if (clipped) throw new Error(screen + ': account control clipped by the topbar');
          }
          if (!process.env.DESIGN_BASELINE) {
            if (screen === 'home' && (words.includes('Fix the broken ride calendar link') || words.includes('Replace the old ride archive') || words.includes('127.4h'))) throw new Error('Home still shows stopped work');
            if (screen === 'overview') {
              const rows = await page.locator('.activity-ledger tbody tr').allTextContents();
              if (rows.some(r => /backup/.test(r) && !/failed|stopped|manual/.test(r))) throw new Error('routine automated backup in Activity');
              if (await page.locator('.application-details').getByText('Adds the winter ride times page', { exact: false }).count()) throw new Error('release narrative leaked into the overview');
            }
            if (screen === 'change-delivery') {
              const widths = size.startsWith('desktop') ? [320, 390, 600, 768, 800, 801, 900, 1024, 1280, 1440, 1920] : [viewport.width];
              for (const width of widths) {
                await page.setViewportSize({ width, height: viewport.height });
                const overlap = await page.locator('.ch-line').evaluateAll(rows => rows.some(row => {
                  const cells = [...row.children].filter(el => el.textContent.trim());
                  const rects = cells.map(el => {
                    const range = document.createRange();
                    range.selectNodeContents(el);
                    return [...range.getClientRects()].filter(r => r.width && r.height);
                  });
                  return rects.some((a, i) => rects.slice(i + 1).some(b => a.some(x => b.some(y =>
                    x.left < y.right && y.left < x.right && x.top < y.bottom && y.top < x.bottom))));
                }));
                if (overlap) throw new Error(`delivery columns overlap at ${width}px`);
                if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error(`delivery overflows at ${width}px`);
              }
              await page.setViewportSize(viewport);
            }
          }
          if (screen === 'target' || screen === 'target-unknown') {
            const capacity = page.locator('.capacity-strip');
            if (!cameraContentExclusions[screen] && !/memory/i.test((await capacity.allTextContents()).join(' '))) throw new Error(screen + ': capacity reading missing');
          }
          if (screen.startsWith('people')) {
            if (await page.locator('main details').count()) throw new Error('People has a fold-down');
            if (await page.locator('main h1').count() !== 1) throw new Error('People needs one page heading');
            const heading = (await page.locator('main h1').innerText()).replace(/\s+\d+$/, '').trim();
            if ((await page.locator('main h2').allTextContents()).some(text => text.replace(/\s*\d+$/, '').trim() === heading)) throw new Error('People repeats its page heading');
            for (const action of await page.locator('main .record-table .record-action').all()) {
              if (await action.evaluate(el => el.nextElementSibling !== null || getComputedStyle(el).textAlign !== 'right')) throw new Error('row action must be last and aligned right');
            }
            for (const table of await page.locator('main .record-table').all()) {
              if (size.startsWith('desktop') && !await table.locator('thead').isVisible()) throw new Error('list column headers are hidden');
              const rows = await table.locator('tbody tr').evaluateAll(els => els.map(el => ({ height: el.getBoundingClientRect().height, line: parseFloat(getComputedStyle(el.querySelector('td')).lineHeight), tracks: getComputedStyle(el).gridTemplateRows.split(' ').length })));
              if (rows.some(r => size.startsWith('phone') ? r.tracks > 2 : r.height > r.line + 54)) throw new Error(`${screen} ${size}: People table rows wrap beyond their layout`);
            }
          }
          if (!process.env.DESIGN_BASELINE && screen === 'people') {
            if (!(await page.locator('.people-accounts tbody tr').filter({ hasText: 'retired' }).innerText()).includes('disabled')) throw new Error('disabled account missing from table');
            if (await page.locator('.people-accounts tbody tr').count() < 30 || !words.includes('never signed in')) throw new Error('account states missing');
            const unused = page.locator('.people-accounts tbody tr').filter({ hasText: 'never-signed-in' });
            if ((await unused.locator('[data-label="Last seen"]').innerText()).trim() !== '—') throw new Error('unused name has a last activity time');
            const provisioned = page.locator('.people-accounts tbody tr').filter({ hasText: 'colleague-32' });
            if (!(await provisioned.innerText()).includes('never signed in') || (await provisioned.locator('[data-label="Last seen"]').innerText()).trim() !== '—') throw new Error('password provisioning counted as activity');
            if (words.includes('Where administrators may sign in') || words.includes('Connected coding agents')) throw new Error('People still includes other settings');
          }
          if (!process.env.DESIGN_BASELINE && screen === 'account' && (!words.includes('sam-laptop') || words.includes('tom-laptop'))) throw new Error('personal token list crossed account boundary');
          if (!process.env.DESIGN_BASELINE && screen === 'home' && size.startsWith('phone') && await page.locator('.dash-projects').getByText('Details', { exact: true }).count()) throw new Error('phone project row has Details');
          if (!process.env.DESIGN_BASELINE && screen === 'home') {
            const finished = await page.getByRole('region', { name: 'Recently landed', exact: true }).locator('.list-row').evaluateAll(rows => rows.map(row => ({ title: row.querySelector('strong')?.textContent, detail: row.querySelector('.small')?.textContent })));
            if (finished.length < 2 || finished.some(row => !row.title || !row.detail?.includes(' · ')) || new Set(finished.map(row => row.title)).size < 2) throw new Error('recently landed rows need distinct titles, projects and ages');
          }
          if (!process.env.DESIGN_BASELINE && screen === 'infrastructure' && size.startsWith('phone') && /\/var\/|\/mnt\//.test(words)) throw new Error('raw path on the infrastructure phone list');
          if (!process.env.DESIGN_BASELINE && (screen === 'change' || screen === 'change-delivery')) {
            const currentTab = await page.locator('.ch-tabs [aria-current="page"]').innerText();
            if (currentTab !== 'Overview' || await page.locator('.diff').count()) throw new Error('change first view must show Overview; Code stays one tap away');
          }
          if (!process.env.DESIGN_BASELINE && size.startsWith('phone') && screen.startsWith('change') && await page.locator('.ch-line-by:visible, .ch-line-when:visible').count()) throw new Error('phone receipts must keep extra state and timestamps in the full record');
          if (!process.env.DESIGN_BASELINE && screen.startsWith('change')) {
            const hidden = await page.locator('.ch-tabs [aria-current="page"]').evaluateAll(els => els.some(el => {
              const r = el.getBoundingClientRect(), nav = el.parentElement.getBoundingClientRect();
              return r.left < nav.left - 1 || r.right > nav.right + 1;
            }));
            if (hidden) throw new Error(screen + ': active tab is outside its scrollable navigation');
          }
          if (!process.env.DESIGN_BASELINE) {
            const strips = await page.locator('.tabs').evaluateAll(els => els.map(el => {
              const active = el.querySelector('.active, [aria-current="page"], [aria-selected="true"]');
              const r = el.getBoundingClientRect(), a = active?.getBoundingClientRect();
              return { rows: new Set([...el.children].map(c => Math.round(c.getBoundingClientRect().top))).size, hidden: a && (a.left < r.left - 1 || a.right > r.right + 1) };
            }));
            if (strips.some(s => s.rows > 1 || s.hidden)) throw new Error(screen + ': tabs wrap or active tab is hidden');
            if (screen === 'usage' && !await page.locator('.usage-chart').count()) throw new Error('Usage Summary is empty');
            if (screen === 'workflows' && !await page.getByRole('link', { name: 'New workflow', exact: true }).count()) throw new Error('Workflows is empty');
            if (screen === 'people-agents' && await page.locator('.agent-group', { hasText: 'ChatGPT ×2' }).count() !== 1) throw new Error('duplicate agents not grouped');
            if (screen === 'docs') {
              const exposed = await page.locator('details.docs-group:not([open]) .conn-subnav-item').evaluateAll(els => els.some(el => el.getClientRects().length && el.checkVisibility()));
              if (exposed) throw new Error('closed docs group exposes its contents');
            }
          }
          if (size.startsWith('desktop')) {
            const original = await page.evaluate(() => document.documentElement.dataset.size);
            try {
              for (const textSize of ['medium', 'large']) {
                await page.evaluate(value => { document.documentElement.dataset.size = value; }, textSize);
                const wrapped = await page.evaluate(wrappedRowActions);
                if (wrapped.length) throw new Error(`${screen} ${textSize}: wrapped action labels ${JSON.stringify(wrapped)}`);
              }
            } finally {
              await page.evaluate(value => { if (value) document.documentElement.dataset.size = value; else delete document.documentElement.dataset.size; }, original);
            }
          }
          if (['home', 'projects', 'project-deployments', 'deployments', 'deployment', 'target', 'target-cloud'].includes(screen)) {
            const misplaced = await page.locator('.status-help-stack').evaluateAll(statuses => statuses.some(status => {
              const chip = status.querySelector('.chip'), reason = status.querySelector('.muted');
              if (!chip || !reason) return true;
              const c = chip.getBoundingClientRect(), r = reason.getBoundingClientRect();
              return r.top < c.bottom || Math.abs(r.left - c.left) > 1 || reason.scrollWidth > reason.clientWidth + 1;
            }));
            if (misplaced) throw new Error(screen + ': status reasons must wrap below their chip');
          }
          if (screen === 'project-deployments' && size.startsWith('desktop')) {
            const squeezed = await page.locator('.deployment-row').evaluateAll(rows => rows.some(row => {
              const state = row.querySelector('.list-state').getBoundingClientRect();
              const identity = row.querySelector('.list-identity').getBoundingClientRect();
              const detail = row.querySelector('.list-detail').getBoundingClientRect();
              return state.width > 300 || identity.width < 150 || detail.width < 150;
            }));
            if (squeezed) throw new Error('deployment reasons must leave room for name, address and details');
          }
          if (screen === 'target' && size.startsWith('phone')) {
            const namesFit = await page.locator('.target-deployments-table .phone-name').evaluateAll(cells => cells.every(cell => cell.scrollWidth <= cell.clientWidth + 1));
            if (!namesFit) throw new Error('deployment names must remain readable on phones');
          }
          const capacityAligned = await page.locator('.capacity-strip').evaluateAll(strips => strips.every(strip => {
            const value = strip.querySelector('.capacity-strip-label > span');
            const bar = strip.querySelector('.capacity-strip-track');
            return value && bar && Math.abs(value.getBoundingClientRect().right - bar.getBoundingClientRect().right) <= 1;
          }));
          if (!capacityAligned) throw new Error(screen + ': capacity values must align with the right edge of their bars');
          const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
          if (overflow) {
            await page.screenshot({path:`${out}/${screen}/${size}-${theme}-overflow.png`,fullPage:true});
            const outside = await page.locator('body *').evaluateAll(els => els.filter(el => {const r=el.getBoundingClientRect();return r.width && (r.right>innerWidth+1 || r.left< -1)}).map(el=>({tag:el.tagName,cls:el.className,text:el.textContent.slice(0,100),width:el.getBoundingClientRect().width})).slice(-25));
            errors.push(`${screen}: horizontal overflow at ${size}: ${JSON.stringify(outside)}`);
          }
          const headerOverflow = await page.locator('.topbar').evaluateAll(bars => bars.some(bar => [...bar.children].some(el => {const r=el.getBoundingClientRect();return r.width && (r.left < -1 || r.right > innerWidth+1)})));
          if (headerOverflow) errors.push(`${screen}: fixed top-bar controls leave the viewport at ${size}`);
          const fonts = await page.evaluate(rowFontDifferences);
          if (fonts.length) errors.push(`${screen}: different font sizes between rows ${JSON.stringify(fonts)}`);
          if (await page.locator('main details').count()) errors.push(`${screen}: page fold-down remains`);
          const clipped = await page.locator('main *').evaluateAll(els => els.filter(el => {
            if (el.matches('.record-table td') && getComputedStyle(el).textOverflow === 'ellipsis' && el.querySelector('a[href], [title]')) return false;
            if (!el.getClientRects().length || el.closest('pre, .xterm, .diff, .log') || el.matches('.tabs') || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName)) return false;
            // These labelled, focusable regions intentionally keep full table columns scrollable.
            if (el.matches('.target-resource-scroll[tabindex="0"], .cap-details[tabindex="0"], .capacity-details[tabindex="0"], .md-doc table[tabindex="0"]')) return false;
            const style = getComputedStyle(el);
            return ['auto', 'scroll', 'hidden'].includes(style.overflowX) && el.clientWidth > 4 && el.scrollWidth > el.clientWidth + 2;
          }).map(el => ({ tag: el.tagName, class: el.className, text: el.textContent.slice(0, 100) })));
          // The project's own Deployments tab is not checked: its release cell
          // adds and drops lines as its own reads answer (a preview's change
          // title, a waiting release, backup and disk warnings).
          if (['deployments', 'fleet', 'overview'].includes(screen)) {
            const grown = await page.locator('.fleet-table tbody tr').evaluateAll(rows => rows.filter(tr => tr.dataset.firstHeight && Math.abs(tr.getBoundingClientRect().height - Number(tr.dataset.firstHeight)) > 1)
              .map(tr => ({ row: tr.textContent.slice(0, 60), first: Number(tr.dataset.firstHeight), now: tr.getBoundingClientRect().height })));
            if (grown.length) errors.push(`${screen}: deployment rows grew after they were first drawn at ${size}: ${JSON.stringify(grown)}`);
          }
          if (size.startsWith('phone')) {
            const split = await page.evaluate(splitWords);
            if (split.length) errors.push(`${screen}: words split across lines at ${size} ${theme}: ${JSON.stringify(split.slice(0, 10))}`);
          }
          const headings = await page.getByRole('heading', { level: 1 }).allTextContents();
          // An open menu closes when its control scrolls away, so these shots stay where the menu is.
          if (!['people-actions', 'people-revoke', 'people-disconnect', 'infrastructure-expanded', 'people-agents-expanded'].includes(screen)) await page.evaluate(() => window.scrollTo(0, 0));
          // A resting mouse is not part of the page: on a phone its hover would
          // light whatever card it last passed over.
          if (size.startsWith('phone')) await page.mouse.move(-1, -1);
          // The open phone menu covers the viewport, not the whole page.
          timer.lap('checks');
          // A dialog covers the screen; the page under it is not part of the shot.
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}.png`, fullPage: !(screen === 'overview-navigation' && size.startsWith('phone')) && !(await page.locator('[role="dialog"]:visible').count()) });
          if (screen === 'overview' || screen === 'conformance-project') {
            const section = screen === 'overview' ? 'Janitor' : 'Last scan';
            await page.getByRole('region', { name: section, exact: true }).screenshot({ path: `${out}/${screen}/${size}-${theme}-scan-section.png`, style: ".topbar { visibility: hidden !important; }" });
          }
          timer.lap('screenshot');
          finalPaths.add(new URL(page.url()).pathname);
          captures.push({ screen, path, finalPath: new URL(page.url()).pathname, size, theme, words: words.trim().split(/\s+/).filter(Boolean).length, text: words, headings, overflow, headerOverflow, clipped, fonts });
          await saveProgress();
          console.log('captured', screen, size, theme, `tab ${tab}`, overflow ? 'OVERFLOW' : '');
          if (['infrastructure', 'target-resources', 'target-resources-filtered'].includes(screen)) {
            const label = screen.startsWith('infrastructure') ? 'Targets' : 'Resources';
            const table = page.getByRole('table', { name: label, exact: true });
            // Squeezed means a narrow cell grown tall, a few letters a line. A
            // phone row stacks its values, so a tall row alone is not squeezed.
            const squeezed = await table.locator('tbody tr').first().evaluate(row => [...row.children].some(cell => {
              const r = cell.getBoundingClientRect();
              return r.width > 0 && r.width < row.getBoundingClientRect().width / 4 && r.height > 90;
            })).catch(() => false);
            if (squeezed) throw new Error(label + ' columns are squeezing words into vertical text');
          }
          // The navigation capture deliberately covers main with its backdrop.
          // Preserve that screenshot, then restore main before its disclosure check.
          if (screen === 'overview-navigation' && size.startsWith('phone')) {
            await page.getByRole('button', { name: 'close the menu', exact: true }).click();
            await page.locator('.sidebar-backdrop').waitFor({ state: 'detached' });
          }
          // A confirmation the capture opened (revoke, disconnect), or Find or an
          // account's actions on a phone, is a dialog over the page; close it
          // before looking for the page's disclosure.
          if (['people-revoke', 'people-disconnect'].includes(screen) || (['home-search', 'people-actions'].includes(screen) && size.startsWith('phone'))) {
            await page.keyboard.press('Escape');
            await page.getByRole('dialog').waitFor({ state: 'detached' });
          }
          if (['infrastructure-expanded', 'people-agents-expanded'].includes(screen)) {
            await page.keyboard.press('Escape');
            await page.locator('.page-help-pop, [role="dialog"]').first().waitFor({ state: 'detached' });
          }
          // Open the page's first disclosure without invoking any action inside it.
          // A page whose status has details opens those: the status is what
          // a person asks about first there.
          const statusTrigger = ['eval-scorecard', 'target-cloud'].includes(screen) ? page.locator('main .status-word button:visible').first() : null;
          const trigger = statusTrigger && await statusTrigger.count() ? statusTrigger : page.locator('main details > summary:visible, main button[aria-expanded]:visible').first();
          if (await trigger.count()) {
            const wasOpen = await trigger.evaluate(el => el.tagName === 'SUMMARY' ? el.parentElement.open : el.getAttribute('aria-expanded') === 'true');
            if (!wasOpen) await trigger.click();
            disclosures.push({ screen, size, theme, trigger: await trigger.innerText() });
            // A dialog covers the screen; the page under it is not part of the shot.
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-disclosure.png`, fullPage: !(await page.locator('[role="dialog"]:visible').count()) });
            // A disclosure that opened a dialog: on screen, with its close button.
            const dialog = page.locator('[role="dialog"]:visible').first();
            if (await dialog.count()) {
              const box = await dialog.boundingBox(), view = page.viewportSize();
              if (box.x < 0 || box.y < 0 || box.x + box.width > view.width + 0.5 || box.y + box.height > view.height + 0.5 || !await dialog.getByRole('button', { name: 'close' }).isVisible()) throw new Error(`${screen}: open dialog leaves the screen or has no close button at ${size}`);
            }
            if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error(screen + ': open disclosure overflows');
            if (!wasOpen) {
              if (await page.locator('.popover, [role="dialog"]').count()) await page.keyboard.press('Escape');
              else await trigger.click();
            }
          } else disclosures.push({ screen, size, theme, reason: 'No page disclosure' });
          if (screen === 'jobs') {
            // Repeated completed jobs follow unfinished work; expose their actual
            // history without filtering or replacing any of the rows.
            for (let more = 0; more < 50 && await page.getByRole('button', { name: 'Show 10 more', exact: true }).count(); more++) {
              await page.getByRole('button', { name: 'Show 10 more', exact: true }).click();
            }
            await page.evaluate(() => window.scrollTo(0, 0));
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-history.png`, fullPage: true });
          }
          if (!process.env.DESIGN_BASELINE && screen === 'people') {
            await page.getByLabel('Actions for sam', { exact: true }).click();
            await page.getByRole('button', { name: 'type a new password', exact: true }).waitFor();
            // On a phone the actions are a dialog: the shot is the screen it covers.
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-actions.png`, fullPage: !size.startsWith('phone') });
            // On a phone the actions are a dialog over the ⋯: close it as a person does.
            if (size.startsWith('phone')) await page.keyboard.press('Escape');
            else await page.getByLabel('Actions for sam', { exact: true }).click();
            await page.getByLabel('Actions for retired', { exact: true }).click();
            await page.getByRole('button', { name: 'enable', exact: true }).waitFor();
            if (size.startsWith('phone') && !await page.getByRole('dialog', { name: 'retired', exact: true }).isVisible()) throw new Error("people: retired's actions are not a dialog on a phone");
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-other.png`, fullPage: !size.startsWith('phone') });
          }

          if (!process.env.DESIGN_BASELINE && screen === 'change') {
            await page.goto(`${base}/changes/${process.env.DESIGN_CHANGE}?tab=details&theme=${theme}&size=${size.endsWith('-large') ? 'large' : 'medium'}`, { waitUntil: 'networkidle' });
            const checks = page.getByRole('table', { name: 'Checks', exact: true });
            const tests = checks.getByRole('row').filter({ hasText: 'Tests' }).first();
            await tests.getByRole('link', { name: 'details', exact: true }).click();
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-receipt.png`, fullPage: true });
          }
          if (!process.env.DESIGN_BASELINE && screen === 'target-cloud') {
            await page.locator('a[href="#target-retained"]').click();
            if (!await page.locator('#target-retained').isVisible()) throw new Error('retained resource review is hidden');
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-resources.png`, fullPage: true });
            if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error('retained resources overflow');
          }
          if (screen === 'project-deployments') {
            await page.getByRole('button', { name: 'New deployment', exact: true }).click();
            await page.getByLabel('Release feed', { exact: true }).selectOption('main');
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-new-follows-main.png`, fullPage: true });
            await page.getByLabel('Release feed', { exact: true }).selectOption('promotion');
            await page.getByLabel('Source deployment', { exact: true }).waitFor();
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-new-promotion.png`, fullPage: true });
            if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error('new deployment form overflows');
          }
          if (!process.env.DESIGN_BASELINE && screen === 'backups') {
            // Phone rows mount their secondary controls only when expanded.
            for (const row of await page.locator('main tr.expandable-row').all()) {
              if (await row.getAttribute('aria-expanded') === 'false') await row.press('Enter');
            }
            await page.locator('main details:not(.status-help)').evaluateAll(els => els.forEach(el => { el.open = true; }));
            // The history is the whole list of backups, as the jobs history is.
            for (let more = 0; more < 50 && await page.getByRole('button', { name: 'Show 10 more', exact: true }).count(); more++) {
              await page.getByRole('button', { name: 'Show 10 more', exact: true }).click();
            }
            await page.evaluate(() => { document.activeElement?.blur(); window.scrollTo(0, 0); });
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-history.png`, fullPage: true });
            // The Backups page lists; restoring is the deployment's act.
            if (await page.getByRole('button', { name: /^Restore |^Back up now$/ }).count()) throw new Error('Backups rows must not carry deployment acts');
          }
          if (screen === 'target' || screen === 'target-unknown') {
            await page.locator('.capacity-details, .cap-details, .target-deployments, .target-settings').evaluateAll(els => els.forEach(el => { el.open = true; }));
            await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-details.png`, fullPage: true });
            if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error(screen + ': open details overflow');
          }
          timer.lap('after');
          timer.done();
          await saveProgress();
        }
      }
    }
  };
  const shootLogin = async page => {
    for (const [size, viewport] of Object.entries(variants)) {
      await page.setViewportSize(viewport);
      for (const theme of (process.env.DESIGN_AUDIT ? [size.startsWith('phone') ? 'dark' : 'light'] : ['light', 'dark'])) {
        await page.goto(`${base}/?theme=${theme}&size=${size.endsWith('-large') ? 'large' : 'medium'}`, { waitUntil: 'networkidle' });
        await page.locator('form').waitFor();
        await mkdir(`${out}/login`, { recursive: true });
        const words = await page.locator('body').innerText();
        await page.screenshot({ path: `${out}/login/${size}-${theme}.png`, fullPage: true });
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
        captures.push({ screen: 'login', path: '/', size, theme, words: words.trim().split(/\s+/).length, headings: await page.getByRole('heading', { level: 1 }).allTextContents(), overflow });
      }
    }
  };
  // The Features shots turn areas off for every page, so they are final
  // stops; the areas are turned on again after them.
  let turnedOff = [];
  const switchAreas = (areas, enabled) => page.evaluate(async ({ areas, enabled }) => {
    for (const area of areas) {
      const r = await fetch('/api/v1/features/' + area, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected: !enabled, enabled }) });
      if (!r.ok) throw new Error('turn area ' + (enabled ? 'on: ' : 'off: ') + area + ' ' + r.status);
    }
  }, { areas, enabled });
  const turnOff = async screen => {
    const areas = await page.evaluate(async ({ screen, used }) => {
      const response = await fetch('/api/v1/features');
      if (!response.ok) throw new Error('read Features: ' + response.status);
      const features = await response.json();
      // The Features shot shows a household's choice: the beta areas its
      // scene uses stay on, the others are off.
      return features.filter(f => f.enabled && (screen === 'features' ? f.tier === 'beta' && !used.includes(f.area) : f.area === 'evals')).map(f => f.area);
    }, { screen, used: ['evals', 'what-ifs', 'restore-rehearsals', 'conformance-estate', 'app-platform', 'mcp', 'memory'] });
    await switchAreas(areas, false);
    turnedOff.push(...areas);
  };
  const restoreAreas = async () => { if (turnedOff.length) await switchAreas(turnedOff, true); turnedOff = []; };
  // A page in a session of its own: tom (who may not approve) and the
  // signed-out login page do not change the other tabs' session.
  const ownSession = async username => {
    const own = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1, timezoneId: 'UTC' });
    const tabPage = topBarAtTop(await own.newPage());
    tabPage.on('pageerror', e => { errors.push(e.message); });
    if (username) {
      await tabPage.goto(base + '/');
      await tabPage.evaluate(async ({ username, password }) => {
        const r = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) });
        if (!r.ok) throw new Error('camera login: ' + r.status);
      }, { username, password: process.env.DESIGN_PASSWORD });
    }
    return { page: tabPage, close: () => own.close() };
  };
  const finalNames = ['features', 'feature-off', 'reviews-running', ...heldStages.map(stage => 'deployment-' + stage)];
  const wanted = ([screen]) => !selected.size || selected.has(screen);
  const pool = orderedRoutes.filter(wanted).filter(([screen]) => !finalNames.includes(screen));
  if (!selected.size || selected.has('login')) pool.push(['login', '/']);
  // The held release's stage is changed for everyone, so its pages are
  // final stops, each after the scene moved the release to its stage.
  const moveHeld = stage => async () => {
    const status = await page.evaluate(async stage => (await fetch('/?cameraStage=' + stage)).status, stage);
    if (status !== 204) throw new Error('camera stage ' + stage + ': ' + status);
  };
  const finals = [
    ...heldStages.filter(stage => wanted(['deployment-' + stage])).flatMap(stage => [['(stage ' + stage + ')', null, moveHeld(stage)], orderedRoutes.find(([screen]) => screen === 'deployment-' + stage)]),
    ...orderedRoutes.filter(wanted).filter(([screen]) => screen === 'features' || screen === 'feature-off'),
    ['(areas on again)', null, restoreAreas],
    ...orderedRoutes.filter(wanted).filter(([screen]) => screen === 'reviews-running'),
  ];
  // Four tabs: the measured run time stops falling there on a 4-CPU runner.
  const tabPages = [page];
  for (let i = 1; i < Math.min(4, pool.length); i++) {
    const tabPage = topBarAtTop(await context.newPage());
    tabPage.on('pageerror', e => { errors.push(e.message); });
    tabPages.push(tabPage);
  }
  const failure = oneFailure();
  await runQueue({
    pool, finals, tabs: tabPages.length,
    shoot: async ([screen, path, run], tab) => {
      if (run) return run();
      let tabPage = tabPages[tab], session;
      try {
        if (screen === 'change-non-approver' || screen === 'login') {
          session = await ownSession(screen === 'login' ? null : 'tom');
          tabPage = session.page;
        }
        if (screen === 'features' || screen === 'feature-off') await turnOff(screen);
        if (screen === 'login') await shootLogin(tabPage);
        else if (screen === 'jobs') {
          // One backup is held running while the jobs page is shot (its
          // upload waits at the NAS), and let through after.
          const job = async what => {
            if (!tabPage.url().startsWith(base)) await tabPage.goto(base + '/', { waitUntil: 'domcontentloaded' });
            const status = await tabPage.evaluate(async query => (await fetch(query)).status, '/?cameraJob=' + what);
            if (status !== 204) throw new Error(`camera job ${what}: ${status}`);
          };
          await job('hold');
          try { await shootPage(tabPage, screen, path, tab); } finally { await job('release'); }
        } else await shootPage(tabPage, screen, path, tab);
      } catch (err) {
        // The run's failure is the first a tab claims; only that tab writes the
        // failure text and screenshot, of its own page, and every tab reports it.
        if (failure.claim(err)) {
          err.captured = true;
          await writeFile(`${out}/failure.txt`, `${screen} on tab ${tab}: ${tabPage.url()}\n${err.stack}`);
          await tabPage.screenshot({ path: `${out}/failure.png`, fullPage: true }).catch(() => {});
        }
        throw failure.chosen;
      } finally {
        await session?.close();
      }
    },
  });
  // Every tab's captures, in page and variant order, so the files are stable
  // between runs whichever tab finished first.
  const order = new Map([...orderedRoutes.map(([screen]) => screen), 'login'].map((screen, i) => [screen, i]));
  const variantOrder = Object.keys(variants);
  captures.sort((a, b) => order.get(a.screen) - order.get(b.screen) || variantOrder.indexOf(a.size) - variantOrder.indexOf(b.size) || a.theme.localeCompare(b.theme));
  disclosures.sort((a, b) => order.get(a.screen) - order.get(b.screen) || variantOrder.indexOf(a.size) - variantOrder.indexOf(b.size) || a.theme.localeCompare(b.theme));
  phase('shooting');
  await writeFile(`${out}/disclosures.json`, JSON.stringify(disclosures, null, 2));
  await writeFile(`${out}/final-paths.json`, JSON.stringify([...finalPaths].sort(), null, 2));
  await writeFile(`${out}/pages.json`, JSON.stringify(captures, null, 2));
  if (errors.length) throw new Error(errors.join('\n'));
  if (!process.env.DESIGN_BASELINE) {
    const issues = captures.filter(c => c.overflow || c.headings.length !== 1 || (c.size.startsWith('phone') && c.clipped?.length));
    if (issues.length) throw new Error('layout issues: ' + JSON.stringify(issues));
  }
  await writeFile(`${out}/capture.json`, JSON.stringify({ project: 'club-newsletter', change: process.env.DESIGN_CHANGE, actor: 'sam', infraCases, viewports: { desktop: '1440×1000', phone: '390×844' }, facts, records: 'disposable local tour scene; actual API responses; local scripts; recorded agent transcripts; running production test daemon; unreachable preview transport; backup owner operation; passing approval and failing photo review; summary provider unavailable', errors }, null, 2));
  phase('wrap-up');
  await saveTimings();
  console.log(timingSummary());
} catch (err) {
  await mkdir(out, { recursive: true });
  await saveTimings().catch(() => {});
  if (!err.captured) {
    await writeFile(`${out}/failure.txt`, `${page.url()}\n${err.stack}`);
    await page.screenshot({ path: `${out}/failure.png`, fullPage: true }).catch(() => {});
  }
  throw err;
} finally { await browser.close(); }
