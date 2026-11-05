import { mkdir, writeFile } from 'node:fs/promises';
import { wrappedRowActions } from '../../../web/row-action-lines.mjs';
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE);
const base = process.env.DESIGN_URL, out = process.env.DESIGN_OUT;
const browser = await chromium.launch({ headless: true, args: ['--no-proxy-server'] });
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1, timezoneId: 'UTC' });
const page = await context.newPage();
// Content decisions awaiting resolution; these pages still receive every layout check.
// These exclusions concern status meaning and help wording, not layout.
// The affected pages still receive every viewport and disclosure check.
const cameraContentExclusions = {
  'deployment-infrastructure-help': 'The first target help shows stale host-observation details rather than a Target check prefix; whether connection facts must also appear there is an open content decision.',
  'target-unknown': 'The unreachable target shows a capacity-check failure without a memory summary; whether unknown memory must always be shown is an open content decision.',
  conformance: 'Overview uses unknown states; whether unavailable execution needs a separate red marker is an open content decision.',
  'conformance-project-mixed': 'Project detail groups unavailable execution under Couldn’t run with unknown states; whether it needs a separate red marker is the same open content decision.',
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
    const attachments = await read('/projects/club-newsletter/integrations');
    const reviews = await Promise.all(activity.active.map(r => read('/changes/' + r.changeId)));
    return { releases, registryAttached: attachments.attachments.some(i => i.capability === 'container-registry' && i.title === 'Container registry'), idleSeconds: activity.history.find(c => c.title === "Replace the old ride archive")?.tookS, history: activity.history.length, backups: backups.backups, stoppedCheck: changes.some(c => c.title === "Release check: a chat answers" && c.state === "abandoned"), deployments, backupBytes: backups.backups.find(b => b.bytes > 0)?.bytes, failingCheck: reviews.some(v => v.detail?.steps.some(s => s.status === 'fail')) };
  });
  await mkdir(out, { recursive: true });
  await writeFile(`${out}/facts.json`, JSON.stringify(facts, null, 2));
  if (!facts.registryAttached || facts.history < 8 || facts.history > 12 || !facts.failingCheck || !facts.backupBytes || !facts.stoppedCheck || facts.backups.length < 16 || facts.releases.length < 16 || !(facts.idleSeconds >= 458640) || !facts.releases.some(r => r.serving && r.notes?.startsWith("To satisfy it:"))) throw new Error('incomplete design seed: ' + JSON.stringify(facts));
  if (!facts.deployments.some(d => d.displayLabel === 'Production' && d.running === true) || !facts.deployments.some(d => d.kind === 'preview' && d.running === null && d.observation === 'unreachable')) throw new Error('deployment seed is not running production + unavailable preview: ' + JSON.stringify(facts.deployments));
  const infraCases = await page.evaluate(async () => {
    const read = async path => { const r = await fetch('/api/v1' + path); if (!r.ok) throw new Error(path + ': ' + r.status); return r.json(); };
    // The fixture collected capacity, including the failed preview-host probe.
    const pending = await read('/attention');
    const busy = await read('/targets/built-in-docker/capacity');
    const unknown = await read('/targets/design-preview/capacity');
    return { pending, busy, unknown };
  });
  await writeFile(`${out}/infrastructure.json`, JSON.stringify(infraCases, null, 2));
  if (!infraCases.busy.memory.total.known || infraCases.busy.memory.promised.unlimited.apptown + infraCases.busy.memory.promised.unlimited.other !== 19 || infraCases.busy.tenants.filter(t => t.kind === 'deployment').length !== 9) throw new Error('busy host seed lacks nine apps and nineteen containers without limits');
  if (!infraCases.busy.memory.overcommitted || Math.abs(infraCases.busy.memory.total.value / 2 ** 30 - 1.93) > .01 || Math.abs((infraCases.busy.memory.promised.sum.apptown.value + infraCases.busy.memory.promised.sum.other.value) / 2 ** 30 - 4.23) > .01) throw new Error('host seed lacks real memory overcommit');
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
    for (let n = 1; n <= 21; n++) {
      await request('/complaints', { text: `Could not open the household calendar (${n})`, why: 'The link did not open.', wish: 'Show a way back.' });
    }
    const complaints = await request('/complaints');
    for (const c of complaints.complaints) await request(`/complaints/${c.id}/answer`, { answer: 'The calendar link has been repaired.' });
    await request('/projects/club-newsletter/members', { grantee: 'sam', role: 'project-approver' });
    await request('/projects', { slug: 'shared-notes', name: 'Shared Notes', blurb: 'Notes for the household.' });
    if (!audit) await request('/projects/shared-notes/repo', { defaultBranch: 'main' });
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
    const evaluation = await request('/evals', { changes: [source.id], profile: form.profiles[0], budgetDollars: 5, shown: form.meaning });
    const history = await request('/conformance/projects/club-newsletter/history');
    const review = history.history[0];
    const whatif = await request('/conformance/what-if', { pack: 'conventions-only', version: 1, projects: ['club-newsletter'] });
    const remediation = await request('/conformance/remediations', { scope: 'all', concurrency: 1 });
    const backups = await request('/projects/club-newsletter/deployments/main/backups');
    const operation = await request('/projects/club-newsletter/deployments/main/backups/' + backups.backups.find(b => b.bytes > 0).id + '/restore', { confirm: 'main' });
    return { guidance: blocks[0].id, advisory: advisory?.change.id, delivered: delivered.change.id, review: review.reviewId, adr: adr.id, invite: new URL(invite.link).pathname, failed: failed.change.id, evaluation: evaluation.id, whatif: whatif.id, remediation: remediation.remediation.id, operation: operation.operationId };
  }, !!process.env.DESIGN_AUDIT);
  if (process.env.DESIGN_AUDIT) await page.goto(base + '/?cameraPrepare=1', { waitUntil: 'networkidle' });
  const routes = {
    'infrastructure-expanded': '/infrastructure', 'target-settings': '/integrations/targets/design-preview', 'target-resources': '/infrastructure/built-in-docker',
    'backup-store-details': '/deployments/backups', 'release-invalid': '/releases/6', 'usage-details': '/usage', 'usage-timing': '/usage', 'people-agents-expanded': '/people?tab=agents',
    'guidance-block': '/guidance/' + seeded.guidance, 'guidance-missing': '/guidance/lang%2Fselection%2Fdjango-fullstack', 'release-record': '/projects/club-newsletter/releases/1',
    home: '/', 'home-search': '/', 'overview-navigation': '/projects/club-newsletter', projects: '/projects', deployments: '/deployments', backups: '/deployments/backups',
    fleet: '/deployments/table', 'deployment-infrastructure': '/deployments/infrastructure', attention: '/attention', updates: '/updates',
    'target-offers': '/integrations/targets', 'release-detail': '/projects/club-newsletter/releases/' + facts.releases.find(r => r.serving).number,
    'settings-decisions-empty': '/projects/shared-notes/settings/decisions', 'target-cloud': '/infrastructure/design-cloud', infrastructure: '/infrastructure', jobs: '/infrastructure/jobs', 'infrastructure-new': '/infrastructure/new',
    conformance: '/conformance', checks: '/conformance/checks', reviews: '/conformance/reviews',
    'what-ifs': '/conformance/what-ifs', remediations: '/conformance/remediations', 'fleet-ways': '/conformance/ways',
    integrations: '/integrations', slots: '/integrations/slots', 'service-kinds': '/integrations/services', secrets: '/integrations/secrets',
    packs: '/packs', workflows: '/workflows', guidance: '/guidance', policies: '/policies',
    'people-groups': '/people?tab=groups', 'people-signin': '/people?tab=signin', 'people-agents': '/people?tab=agents',
    evals: '/evals', 'eval-new': '/evals/new', people: '/people', account: '/account', usage: '/usage',
    complaints: '/complaints', 'complaints-answered': '/complaints/answered', 'complaints-archive': '/complaints/archive',
    'model-profile': '/integrations/profiles/standard', 'target-new': '/integrations/targets/new',
    services: '/services', 'services-shared': '/services/shared', 'deployment': '/projects/club-newsletter/deployments/main',
    'camera-not-found': '/camera-missing-page',
    'new-project': '/projects/_new', docs: '/docs', invite: '/invite',
    overview: '/projects/club-newsletter', console: '/projects/club-newsletter/console',
    security: '/projects/club-newsletter/security', suppressions: '/projects/club-newsletter/security/suppressions',
    janitor: '/projects/club-newsletter/janitor', release: '/projects/club-newsletter/release',
    'change-delivery': `/changes/${seeded.delivered}`,
    change: `/changes/${process.env.DESIGN_CHANGE}`,
    'change-non-approver': `/changes/${process.env.DESIGN_CHANGE}`,
    'change-failed': `/changes/${seeded.failed}`, 'overview-empty': '/projects/shared-notes',
    'eval-scorecard': `/evals/${seeded.evaluation}`, 'what-if': `/conformance/what-ifs/${seeded.whatif}`,
    remediation: `/conformance/remediations/${seeded.remediation}`, operation: `/operations/${seeded.operation}`,
    'invite-pending': seeded.invite,
    'conformance-project': '/conformance/projects/club-newsletter',
    'conformance-project-mixed': '/conformance/projects/lunch-orders',
    'deployment-infrastructure-help': '/deployments/infrastructure',
    'janitor-all': '/janitor',
    'people-actions': '/people',
    'review': `/conformance/reviews/${seeded.review}`,
    'review-checks': `/conformance/reviews/${seeded.review}/checks`,
    'review-project': `/conformance/reviews/${seeded.review}/projects/club-newsletter`,

  };
  for (const tab of ['general', 'working', 'services', 'deployments', 'secrets', 'members', 'decisions', 'usage']) routes['settings-' + tab] = '/projects/club-newsletter/settings/' + tab;
  // Detail links come from the actual seeded records.
  for (const [screen, path, pattern] of [
    ['pack', '/packs', /^\/packs\/[^/]+$/],
    ['target', '/infrastructure', /^\/infrastructure\/(?!new|jobs)[^/]+$/],
    ['integration', '/integrations', /^\/integrations\/(?!slots|services|secrets|targets)[^/]+$/],
    ['adr', '/projects/club-newsletter/settings/decisions', /^\/adrs\/[^/]+$/],
    ['doc', '/docs', /^\/docs\/[^/]+\/[^/]+$/],
  ]) {
    await page.goto(base + path, { waitUntil: 'networkidle' });
    try { await page.waitForFunction(source => [...document.querySelectorAll('main a[href]')].some(el => new RegExp(source).test(el.getAttribute('href'))), pattern.source); }
    catch (err) {
      await mkdir(out, { recursive: true });
      await page.screenshot({ path: `${out}/discovery-error.png`, fullPage: true });
      throw new Error(`${screen}: ${await page.locator('body').innerText()} ${err}`);
    }
    const links = await page.locator('main a[href]').evaluateAll(els => els.map(el => el.getAttribute('href')));
    const detail = links.find(href => pattern.test(href));
    if (detail) routes[screen] = screen === 'target' ? '/infrastructure/built-in-docker' : detail;
    if (screen === 'target') routes['target-unknown'] = '/infrastructure/design-preview';
    if (!detail) throw new Error(`No detail link for ${screen}`);
  }
  await page.goto(`${base}/changes/${process.env.DESIGN_CHANGE}?tab=conversation`, { waitUntil: 'networkidle' });
  const workRecord = page.locator('a[href*="/work/"]').first();
  await workRecord.waitFor();
  routes['work-record'] = await workRecord.getAttribute('href');
  routes['change-code'] = `/changes/${process.env.DESIGN_CHANGE}?tab=changes`;
  for (const tab of ['overview', 'conversation', 'details']) routes['change-' + tab] = `/changes/${process.env.DESIGN_CHANGE}?tab=${tab}`;
  Object.assign(routes, { 'new-project-way': '/projects/_new', 'new-project-repository': '/projects/_new', 'pack-new': '/packs', 'policy-new': '/policies', 'workflow-new': '/workflows', 'infrastructure-connection': '/infrastructure/new' });
  if (process.env.DESIGN_AUDIT) routes['janitor-saved'] = '/projects/club-newsletter/janitor';
  if (process.env.DESIGN_AUDIT) routes['change-advisory'] = `/changes/${seeded.advisory}`;
  if (process.env.DESIGN_AUDIT) routes['reviews-running'] = '/conformance/reviews?cameraRunning=1';
  const selected = new Set((process.env.DESIGN_PAGES || '').split(',').filter(Boolean));
  for (const name of selected) if (name !== 'login' && !(name in routes)) throw new Error('unknown camera page: ' + name);
  const captures = [];
  const variants = { desktop: { width: 1440, height: 1000 }, phone: { width: 390, height: 844 }, 'desktop-large': { width: 1440, height: 1000 }, 'phone-large': { width: 390, height: 844 } };
  const finalPaths = new Set();
  const disclosures = [];
  await writeFile(`${out}/content-exclusions.json`, JSON.stringify(cameraContentExclusions, null, 2));
  let cameraUser = 'sam';
  for (const [screen, path] of Object.entries(routes)) {
    if (selected.size && !selected.has(screen)) continue;
    const username = screen === 'change-non-approver' ? 'tom' : 'sam';
    if (username !== cameraUser) {
      await page.evaluate(async ({ username, password }) => {
        const r = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) });
        if (!r.ok) throw new Error('camera login: ' + r.status);
      }, { username, password: process.env.DESIGN_PASSWORD });
      cameraUser = username;
    }
    await mkdir(`${out}/${screen}`, { recursive: true });
    for (const [size, viewport] of Object.entries(variants)) {
      await page.setViewportSize(viewport);
      for (const theme of (process.env.DESIGN_AUDIT ? [size.startsWith('phone') ? 'dark' : 'light'] : ['light', 'dark'])) {
        await page.goto(`${base}${path}${path.includes('?') ? '&' : '?'}theme=${theme}&size=${size.endsWith('-large') ? 'large' : 'medium'}`, { waitUntil: 'networkidle' });
        await page.locator('#root > *').first().waitFor({ state: 'visible' });
        await page.waitForFunction(() => ![...document.querySelectorAll('main .muted')].some(el => el.textContent === 'Loading…'));
        if (screen === 'infrastructure-expanded') {
          const summary = page.locator('.host-disk > summary').first();
          const before = await summary.locator('strong').boundingBox();
          await summary.click();
          const after = await summary.locator('strong').boundingBox();
          if (!before || !after || Math.abs(before.x - after.x) > 1 || Math.abs(before.y - after.y) > 1) throw new Error('disk title moved on expansion');
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
          const help = page.locator('main .row-status .status-help').first();
          await help.locator('summary').click();
          if (!await help.locator('div').isVisible()) throw new Error('target check help is missing');
          if (!cameraContentExclusions[screen] && !(await help.innerText()).includes('Target check:')) throw new Error('target check facts are missing');
        }
        if (screen === 'target-resources') await page.locator('#target-resources').getByRole('button', { name: 'View resource', exact: true }).first().click();
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
        if (screen === 'usage-details') await page.getByRole('button', { name: 'Details', exact: true }).click();
        if (screen === 'usage-timing') await page.getByRole('button', { name: 'Time spent', exact: true }).click();
        if (screen === 'backup-store-details') await page.getByRole('region', { name: 'Backup store', exact: true }).getByText('household-copies', { exact: true }).waitFor();
        if (screen === 'people-agents-expanded') await page.locator('main details').evaluateAll(els => els.forEach(el => { el.open = true; }));
        if (screen === 'janitor-saved') {
          const saved = page.locator('main summary').filter({ hasText: /^Saved consent/ });
          await saved.click();
          await page.locator('main summary').filter({ hasText: /^Details$/ }).first().click();
          const history = page.getByRole('button', { name: 'History', exact: true });
          if (await history.count()) { await history.click(); await page.getByText(/Revision 1:/).waitFor(); }
        }
        if (screen === 'people-actions') await page.getByRole('button', { name: 'Actions for sam', exact: true }).click();
        if (screen.startsWith('new-project-')) {
          await page.locator('#project-description').fill('A calendar for the household');
          await page.getByPlaceholder('Suggested from your description').fill('Family Calendar');
          await page.getByRole('button', { name: 'Next →', exact: true }).click();
          await page.getByRole('button', { name: 'Choose a way of working myself →', exact: true }).click();
          await page.locator('.pack-card').filter({ hasText: 'conventions-only' }).waitFor();
          if (screen === 'new-project-repository') {
            await page.getByRole('button', { name: /none \(bare\)/ }).click();
            await page.getByRole('button', { name: 'Next →', exact: true }).click();
          }
        }
        if (screen === 'home-search') {
          await page.locator('button[aria-label="Search"]').click();
          await page.getByRole('textbox', { name: 'find', exact: true }).fill('club');
          await page.locator('.topbar-find-menu').waitFor();
          const clipped = await page.locator('.topbar-find-menu button').evaluateAll(els => els.some(el => el.scrollWidth > el.clientWidth + 1));
          if (clipped) throw new Error('search result text overflows its control');
        }
        if (screen === 'overview-navigation' && size.startsWith('phone')) await page.getByRole('button', { name: 'open the menu', exact: true }).click();
        if (screen === 'pack-new') {
          await page.getByRole('button', { name: 'New pack', exact: true }).click();
          await page.getByText('adopt-integration', { exact: true }).waitFor();
          await page.locator('.badge').filter({ hasText: 'api/rest-pragmatist' }).waitFor();
        }
        if (screen === 'policy-new') await page.getByRole('button', { name: 'New policy', exact: true }).click();
        if (screen === 'workflow-new') await page.getByRole('link', { name: 'New workflow', exact: true }).click();
        if (screen === 'infrastructure-connection') await page.getByRole('button', { name: /Docker host/ }).click();
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
        if (screen === 'target' && !process.env.DESIGN_BASELINE && !words.includes('over by')) throw new Error('host memory warning missing before Details');
        if ((screen === 'change' || screen === 'change-non-approver') && !process.env.DESIGN_BASELINE) {
          const eligible = screen === 'change';
          if (await page.getByRole('button', { name: 'Decide…', exact: true }).count() !== (eligible ? 1 : 0)) throw new Error(screen + ': wrong approval eligibility');
        }
        if (screen === 'change' && !process.env.DESIGN_BASELINE) {
          await page.getByRole('button', { name: 'Decide…', exact: true }).click();
          const decision = page.getByRole('dialog', { name: 'Accept this change?', exact: true });
          await decision.waitFor();
          const covered = await decision.locator('.decision-form').evaluate(form => {
            const choices = form.querySelector('.ch-decision-choices').getBoundingClientRect();
            return [...form.querySelectorAll('.decision-meaning')].some(meaning => meaning.getBoundingClientRect().bottom > choices.top);
          });
          if (covered) throw new Error('decision controls cover the approval meaning');
          if (await decision.evaluate(el => el.scrollWidth > el.clientWidth)) throw new Error('decision screen overflows');
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-decision.png`, fullPage: true });
          await decision.getByRole('button', { name: 'Close', exact: true }).click();
          await decision.waitFor({ state: 'hidden' });
        }
        if (!process.env.DESIGN_BASELINE) {
          const clipped = await page.locator('.topbar > .account-menu > button').evaluateAll(els => els.some(el => {
            const r = el.getBoundingClientRect(); return r.left < 0 || r.right > innerWidth;
          }));
          if (clipped) throw new Error(screen + ': account control clipped by the topbar');
        }
        if (!process.env.DESIGN_BASELINE) {
          if (screen === 'home' && (words.includes('Release check: a chat answers') || words.includes('Replace the old ride archive') || words.includes('127.4h'))) throw new Error('Home still shows stopped work');
          if (screen === 'overview') {
            const rows = await page.locator('.activity-ledger tbody tr').allTextContents();
            if (rows.some(r => /backup/.test(r) && !/failed|stopped|manual/.test(r))) throw new Error('routine automated backup in Activity');
            if (await page.locator('.application-details').getByText('To satisfy it:', { exact: false }).count()) throw new Error('release narrative leaked into the overview');
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
          const capacity = page.getByRole('region', { name: 'Capacity', exact: true });
          if (!cameraContentExclusions[screen] && !/memory/i.test(await capacity.innerText())) throw new Error(screen + ': capacity reading missing');
        }
        if (!process.env.DESIGN_BASELINE && screen === 'people') {
          if (await page.locator('.people-accounts').first().getByText('retired', { exact: true }).count()) throw new Error('disabled account in first list');
          if (words.includes('Where administrators may sign in') || words.includes('Connected coding agents')) throw new Error('People still includes other settings');
        }
        if (!process.env.DESIGN_BASELINE && screen === 'account' && (!words.includes('sam-laptop') || words.includes('tom-laptop'))) throw new Error('personal token list crossed account boundary');
        if (!process.env.DESIGN_BASELINE && screen === 'home' && size.startsWith('phone') && await page.locator('.dash-projects').getByText('Details', { exact: true }).count()) throw new Error('phone project row has Details');
        if (!process.env.DESIGN_BASELINE && screen === 'home') {
          const finished = await page.getByRole('region', { name: 'Recently landed', exact: true }).locator('a.card').evaluateAll(rows => rows.map(row => ({ title: row.querySelector('strong')?.textContent, detail: row.querySelector('.small')?.textContent })));
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
          if (screen === 'people-agents' && !(await page.locator('.agent-group').first().innerText()).includes('×5')) throw new Error('duplicate agents not grouped');
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
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
        const clipped = await page.locator('main *').evaluateAll(els => els.filter(el => {
          if (!el.getClientRects().length || el.closest('pre, .xterm, .diff, .log') || el.matches('.tabs') || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName)) return false;
          const style = getComputedStyle(el);
          return ['auto', 'scroll', 'hidden'].includes(style.overflowX) && el.clientWidth > 4 && el.scrollWidth > el.clientWidth + 2;
        }).map(el => ({ tag: el.tagName, class: el.className, text: el.textContent.slice(0, 100) })));
        const headings = await page.getByRole('heading', { level: 1 }).allTextContents();
        await page.evaluate(() => window.scrollTo(0, 0));
        await page.screenshot({ path: `${out}/${screen}/${size}-${theme}.png`, fullPage: true });
        finalPaths.add(new URL(page.url()).pathname);
        captures.push({ screen, path, finalPath: new URL(page.url()).pathname, size, theme, words: words.trim().split(/\s+/).filter(Boolean).length, text: words, headings, overflow, clipped });
        await writeFile(`${out}/pages-progress.json`, JSON.stringify(captures, null, 2));
        console.log('captured', screen, size, theme, overflow ? 'OVERFLOW' : '');
        // The navigation capture deliberately covers main with its backdrop.
        // Preserve that screenshot, then restore main before its disclosure check.
        if (screen === 'overview-navigation' && size.startsWith('phone')) {
          await page.getByRole('button', { name: 'close the menu', exact: true }).click();
          await page.locator('.sidebar-backdrop').waitFor({ state: 'detached' });
        }
        // Open the page's first disclosure without invoking any action inside it.
        const trigger = page.locator('main details > summary:visible, main button[aria-expanded]:visible').first();
        if (await trigger.count()) {
          const wasOpen = await trigger.evaluate(el => el.tagName === 'SUMMARY' ? el.parentElement.open : el.getAttribute('aria-expanded') === 'true');
          if (!wasOpen) await trigger.click();
          disclosures.push({ screen, size, theme, trigger: await trigger.innerText() });
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-disclosure.png`, fullPage: true });
          if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error(screen + ': open disclosure overflows');
          if (!wasOpen) {
            if (await page.locator('.popover').count()) await page.keyboard.press('Escape');
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
          await page.getByRole('button', { name: 'set password', exact: true }).waitFor();
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-actions.png`, fullPage: true });
          await page.getByLabel('Actions for sam', { exact: true }).click();
          await page.getByText(/^Other accounts and names \(/).click();
          await page.getByLabel('Actions for retired', { exact: true }).click();
          await page.getByRole('button', { name: 'enable', exact: true }).waitFor();
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-other.png`, fullPage: true });
        }
        if (!process.env.DESIGN_BASELINE && screen === 'people-groups') {
          await page.getByText('What each role can do', { exact: true }).click();
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-permissions.png`, fullPage: true });
        }
        if (!process.env.DESIGN_BASELINE && screen === 'change') {
          await page.goto(`${base}/changes/${process.env.DESIGN_CHANGE}?tab=details&theme=${theme}&size=${size.endsWith('-large') ? 'large' : 'medium'}`, { waitUntil: 'networkidle' });
          const checks = page.getByRole('table', { name: 'Checks', exact: true });
          const tests = checks.getByRole('row').filter({ hasText: 'Tests' }).first();
          await tests.getByRole('link', { name: 'details', exact: true }).click();
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-receipt.png`, fullPage: true });
        }
        if (!process.env.DESIGN_BASELINE && screen === 'target-cloud') {
          await page.locator('a[href="#target-resources"]').click();
          if (!await page.locator('#target-resources').isVisible()) throw new Error('retained resource review is hidden');
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-resources.png`, fullPage: true });
          if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error('retained resources overflow');
        }
        if (!process.env.DESIGN_BASELINE && screen === 'backups') {
          // Phone rows mount their secondary controls only when expanded.
          for (const row of await page.locator('main tr.expandable-row').all()) {
            if (await row.getAttribute('aria-expanded') === 'false') await row.press('Enter');
          }
          await page.locator('main details:not(.status-help)').evaluateAll(els => els.forEach(el => { el.open = true; }));
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-history.png`, fullPage: true });
          await page.getByRole('button', { name: 'Restore main from this copy', exact: true }).first().click();
          await page.getByRole('button', { name: 'Restore', exact: true }).waitFor();
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-restore-question.png`, fullPage: true });
        }
        if (screen === 'target' || screen === 'target-unknown') {
          await page.locator('.capacity-details, .cap-details, .target-deployments, .target-settings').evaluateAll(els => els.forEach(el => { el.open = true; }));
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-details.png`, fullPage: true });
          if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error(screen + ': open details overflow');
        }
      }
    }
  }
  // Login is a separate session; the invite page above is public too.
  await context.clearCookies();
  for (const [size, viewport] of Object.entries(selected.size && !selected.has('login') ? {} : variants)) {
    await page.setViewportSize(viewport);
    for (const theme of (process.env.DESIGN_AUDIT ? [size.startsWith('phone') ? 'dark' : 'light'] : ['light', 'dark'])) {
      await page.goto(`${base}/?theme=${theme}&size=${size.endsWith('-large') ? 'large' : 'medium'}`, { waitUntil: 'networkidle' });
      await page.locator('form').waitFor();
      await mkdir(`${out}/login`, { recursive: true });
      const words = await page.locator('body').innerText();
      await page.screenshot({ path: `${out}/login/${size}-${theme}.png`, fullPage: true });
      captures.push({ screen: 'login', path: '/', size, theme, words: words.trim().split(/\s+/).length, headings: await page.getByRole('heading', { level: 1 }).allTextContents(), overflow: false });
    }
  }
  await writeFile(`${out}/disclosures.json`, JSON.stringify(disclosures, null, 2));
  await writeFile(`${out}/final-paths.json`, JSON.stringify([...finalPaths], null, 2));
  await writeFile(`${out}/pages.json`, JSON.stringify(captures, null, 2));
  if (errors.length) throw new Error(errors.join('\n'));
  if (!process.env.DESIGN_BASELINE) {
    const issues = captures.filter(c => c.overflow || c.headings.length !== 1 || (c.size.startsWith('phone') && c.clipped?.length));
    if (issues.length) throw new Error('layout issues: ' + JSON.stringify(issues));
  }
  await writeFile(`${out}/capture.json`, JSON.stringify({ project: 'club-newsletter', change: process.env.DESIGN_CHANGE, actor: 'sam', infraCases, viewports: { desktop: '1440×1000', phone: '390×844' }, facts, records: 'disposable local tour scene; actual API responses; local scripts; recorded agent transcripts; running production test daemon; unreachable preview transport; backup owner operation; passing approval and failing photo review; summary provider unavailable', errors }, null, 2));
} catch (err) {
  await mkdir(out, { recursive: true });
  await writeFile(`${out}/failure.txt`, `${page.url()}\n${err.stack}`);
  await page.screenshot({ path: `${out}/failure.png`, fullPage: true }).catch(() => {});
  throw err;
} finally { await browser.close(); }
