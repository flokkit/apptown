import { mkdir, writeFile } from 'node:fs/promises';
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE);
const base = process.env.DESIGN_URL, out = process.env.DESIGN_OUT;
const browser = await chromium.launch({ headless: true, args: ['--no-proxy-server'] });
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1, timezoneId: 'UTC' });
const page = await context.newPage();
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
    const reviews = await Promise.all(activity.active.map(r => read('/changes/' + r.changeId)));
    return { releases, idleSeconds: activity.history.find(c => c.title === "Replace the old ride archive")?.tookS, history: activity.history.length, backups: backups.backups, stoppedCheck: activity.history.some(c => c.title === "Release check: a chat answers" && c.statusLabel === "stopped"), deployments, backupBytes: backups.backups[0]?.bytes, failingCheck: reviews.some(v => v.detail?.steps.some(s => s.status === 'fail')) };
  });
  if (facts.history < 8 || facts.history > 12 || !facts.failingCheck || !facts.backupBytes || !facts.stoppedCheck || facts.backups.length < 16 || facts.releases.length !== 5 || !(facts.idleSeconds >= 458640) || !facts.releases.some(r => r.serving && r.notes?.startsWith("To satisfy it:"))) throw new Error('incomplete design seed: ' + JSON.stringify(facts));
  if (!facts.deployments.some(d => d.displayLabel === 'Production' && d.running === true) || !facts.deployments.some(d => d.kind === 'preview' && d.running === null && d.observation === 'unreachable')) throw new Error('deployment seed is not running production + unavailable preview: ' + JSON.stringify(facts.deployments));
  const infraCases = await page.evaluate(async () => {
    const read = async path => { const r = await fetch('/api/v1' + path); if (!r.ok) throw new Error(path + ': ' + r.status); return r.json(); };
    // Attention queues the first disk probe; this scene does not run that job.
    const pending = await read('/attention');
    const busy = await read('/targets/design-production/capacity');
    const unknown = await read('/targets/design-preview/capacity');
    if (!busy.memory.total.known || busy.memory.promised.unlimited.apptown + busy.memory.promised.unlimited.other !== 19 || busy.tenants.filter(t => t.kind === 'deployment').length !== 9) throw new Error('busy host seed lacks nine apps and nineteen containers without limits');
    if (!busy.memory.overcommitted || Math.abs(busy.memory.total.value / 2 ** 30 - 1.93) > .01 || Math.abs((busy.memory.promised.sum.apptown.value + busy.memory.promised.sum.other.value) / 2 ** 30 - 4.23) > .01) throw new Error('host seed lacks real memory overcommit');
    if (unknown.memory.total.known) throw new Error('unknown host memory total became known');
    return { pending, busy, unknown };
  });
  if (!process.env.DESIGN_BASELINE && infraCases.pending.items.some(i => i.key.endsWith('/disk-unassessed'))) throw new Error('unchecked disk probe raised Attention');
  const seeded = await page.evaluate(async () => {
    const request = async (path, body) => {
      const r = await fetch('/api/v1' + path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const data = await r.json();
      if (!r.ok) throw new Error(path + ': ' + r.status + ' ' + JSON.stringify(data));
      return data;
    };
    await request('/projects', { slug: 'shared-notes', name: 'Shared Notes', blurb: 'Notes for the household.' });
    await request('/projects/shared-notes/repo', { defaultBranch: 'main' });
    const adr = await request('/projects/club-newsletter/adrs', { title: 'Keep newsletter pages static', tags: 'delivery' });
    const invite = await request('/invites', { username: 'mei', label: 'Mei', role: 'member' });
    const changes = await request('/projects/club-newsletter/changes');
    const views = await Promise.all(changes.map(c => request('/changes/' + c.id)));
    const failed = views.find(v => v.detail?.steps.some(s => s.status === 'fail'));
    const form = await request('/evals/start');
    const delivered = views.find(v => v.evidence?.releases?.some(r => r.serving && r.observedAt));
    if (!delivered) throw new Error('no delivery receipts in design seed');
    const source = views.find(v => v.change.state === 'shipped' && v.runs?.some(r => r.role === 'working')).change;
    const evaluation = await request('/evals', { changes: [source.id], profile: form.profiles[0], budgetDollars: 5, shown: form.meaning });
    const history = await request('/conformance/projects/club-newsletter/history');
    const review = history.history[0];
    const whatif = await request('/conformance/what-if', { pack: 'conventions-only', version: 1, projects: ['club-newsletter'] });
    const remediation = await request('/conformance/remediations', { scope: 'all', concurrency: 1 });
    const backups = await request('/projects/club-newsletter/deployments/main/backups');
    const operation = await request('/projects/club-newsletter/deployments/main/backups/' + backups.backups[0].id + '/restore', { confirm: 'main' });
    return { delivered: delivered.change.id, review: review.reviewId, adr: adr.id, invite: new URL(invite.link).pathname, failed: failed.change.id, evaluation: evaluation.id, whatif: whatif.id, remediation: remediation.remediation.id, operation: operation.operationId };
  });
  const routes = {
    home: '/', 'home-search': '/', 'overview-navigation': '/projects/club-newsletter', projects: '/projects', deployments: '/deployments', backups: '/deployments/backups',
    fleet: '/deployments/table', 'deployment-infrastructure': '/deployments/infrastructure', attention: '/attention', updates: '/updates',
    'target-offers': '/integrations/targets', 'release-detail': '/projects/club-newsletter/releases/' + facts.releases.find(r => r.serving).number,
    infrastructure: '/infrastructure', jobs: '/infrastructure/jobs', 'infrastructure-new': '/infrastructure/new',
    conformance: '/conformance', checks: '/conformance/checks', reviews: '/conformance/reviews',
    'what-ifs': '/conformance/what-ifs', remediations: '/conformance/remediations', 'fleet-ways': '/conformance/ways',
    integrations: '/integrations', slots: '/integrations/slots', 'service-kinds': '/integrations/services', secrets: '/integrations/secrets',
    packs: '/packs', workflows: '/workflow-library', guidance: '/guidance', policies: '/policies',
    evals: '/evals', 'eval-new': '/evals/new', people: '/people', account: '/account', usage: '/usage',
    complaints: '/complaints', 'complaints-answered': '/complaints/answered', 'complaints-archive': '/complaints/archive',
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
    'janitor-all': '/janitor',
    'review': `/conformance/reviews/${seeded.review}`,
    'review-checks': `/conformance/reviews/${seeded.review}/checks`,
    'review-project': `/conformance/reviews/${seeded.review}/projects/club-newsletter`,

  };
  for (const tab of ['general', 'working', 'services', 'deployments', 'secrets', 'members', 'decisions', 'usage']) routes['settings-' + tab] = '/projects/club-newsletter/settings/' + tab;
  // Detail links come from the actual seeded records.
  for (const [screen, path, pattern] of [
    ['pack', '/packs', /^\/packs\/[^/]+$/], ['guidance-block', '/guidance', /^\/guidance\/[^/]+$/],
    ['target', '/infrastructure', /^\/infrastructure\/(?!new|jobs)[^/]+$/],
    ['integration', '/integrations', /^\/integrations\/(?!slots|services|secrets|targets)[^/]+$/],
    ['adr', '/projects/club-newsletter/settings/decisions', /^\/adrs\/[^/]+$/],
    ['doc', '/docs', /^\/docs\/[^/]+\/[^/]+$/],
  ]) {
    await page.goto(base + path, { waitUntil: 'networkidle' });
    const links = await page.locator('main a[href]').evaluateAll(els => els.map(el => el.getAttribute('href')));
    const detail = links.find(href => pattern.test(href));
    if (detail) routes[screen] = screen === 'target' ? '/infrastructure/design-production' : detail;
    if (screen === 'target') routes['target-unknown'] = '/infrastructure/design-preview';
  }
  for (const tab of ['work', 'conversation', 'releases', 'history', 'about']) routes['change-' + tab] = `/changes/${process.env.DESIGN_CHANGE}?tab=${tab}`;
  Object.assign(routes, { 'new-project-way': '/projects/_new', 'new-project-repository': '/projects/_new', 'pack-new': '/packs', 'policy-new': '/policies', 'workflow-new': '/workflow-library', 'infrastructure-connection': '/infrastructure/new' });
  const selected = new Set((process.env.DESIGN_PAGES || '').split(',').filter(Boolean));
  for (const name of selected) if (name !== 'login' && !(name in routes)) throw new Error('unknown camera page: ' + name);
  const captures = [];
  let signedIn = 'sam';
  for (const [screen, path] of Object.entries(routes)) {
    if (selected.size && !selected.has(screen)) continue;
    const username = screen === 'change-non-approver' ? 'tom' : 'sam';
    if (username !== signedIn) await page.evaluate(async ({ username, password }) => {
      const r = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) });
      if (!r.ok) throw new Error('camera login: ' + r.status);
    }, { username, password: process.env.DESIGN_PASSWORD });
    signedIn = username;
    await mkdir(`${out}/${screen}`, { recursive: true });
    for (const [size, viewport] of Object.entries({ desktop: { width: 1440, height: 1000 }, phone: { width: 390, height: 844 } })) {
      await page.setViewportSize(viewport);
      for (const theme of ['light', 'dark']) {
        await page.goto(`${base}${path}${path.includes('?') ? '&' : '?'}theme=${theme}`, { waitUntil: 'networkidle' });
        await page.locator('#root > *').first().waitFor({ state: 'visible' });
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
          await page.locator('summary[aria-label="Search"]').click();
          await page.getByRole('textbox', { name: 'find', exact: true }).fill('club');
          await page.locator('.topbar-find-menu').waitFor();
          const clipped = await page.locator('.topbar-find-menu button').evaluateAll(els => els.some(el => el.scrollWidth > el.clientWidth + 1));
          if (clipped) throw new Error('search result text overflows its control');
        }
        if (screen === 'overview-navigation' && size === 'phone') await page.getByRole('button', { name: 'open the menu', exact: true }).click();
        if (screen === 'pack-new') {
          await page.getByRole('button', { name: 'New pack', exact: true }).click();
          await page.getByText('adopt-integration', { exact: true }).waitFor();
          await page.locator('.chip').filter({ hasText: 'api/rest-pragmatist' }).waitFor();
        }
        if (screen === 'policy-new') await page.getByRole('button', { name: 'New policy', exact: true }).click();
        if (screen === 'workflow-new') await page.getByRole('button', { name: 'New workflow', exact: true }).click();
        if (screen === 'infrastructure-connection') await page.getByRole('button', { name: /Docker host/ }).click();
        await page.waitForLoadState('networkidle');
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
        if (screen === 'target' && !process.env.DESIGN_BASELINE && !words.includes('Apps may need more memory than this host has.')) throw new Error('host memory warning missing before Details');
        if ((screen === 'change' || screen === 'change-non-approver') && !process.env.DESIGN_BASELINE) {
          const eligible = screen === 'change';
          const panel = page.locator('.ch-state');
          const headline = eligible ? 'Approve to land' : 'Waiting for approval to land';
          if (!(await panel.innerText()).includes(headline)) throw new Error(screen + ': wrong approval headline');
          if (!eligible && !(await panel.innerText()).includes('Waiting on:')) throw new Error('non-approver missing awaited person');
        }
        if (screen === 'change' && !process.env.DESIGN_BASELINE) {
          const covered = await page.locator('.decision-form').evaluate(form => {
            const bar = form.querySelector('.decision-bar');
            return [...form.children].filter(child => child !== bar).some(child => child.getBoundingClientRect().bottom > bar.getBoundingClientRect().top);
          });
          if (covered) throw new Error('decision controls cover the approval meaning');
        }
        if (!process.env.DESIGN_BASELINE) {
          const clipped = await page.locator('.topbar > .account-menu > summary').evaluateAll(els => els.some(el => {
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
            const widths = size === 'desktop' ? [320, 390, 600, 768, 800, 801, 900, 1024, 1280, 1440, 1920] : [viewport.width];
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
        if (!process.env.DESIGN_BASELINE && (screen === 'target' || screen === 'target-unknown')) {
          for (const word of ['key from secret', 'needs are', 'promised', 'no limit', '/var/lib/docker']) if (words.includes(word)) throw new Error(screen + ': technical detail on the first view: ' + word);
          if (!(await page.locator('.capacity-summary').innerText()).includes('Memory')) throw new Error(screen + ': capacity summary missing');
        }
        if (!process.env.DESIGN_BASELINE && screen === 'infrastructure' && size === 'phone' && /\/var\/|\/mnt\//.test(words)) throw new Error('raw path on the infrastructure phone list');
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
        const clipped = await page.locator('main *').evaluateAll(els => els.filter(el => {
          if (!el.getClientRects().length || el.closest('pre, .xterm, .diff, .log') || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName)) return false;
          const style = getComputedStyle(el);
          return ['auto', 'scroll', 'hidden'].includes(style.overflowX) && el.clientWidth > 4 && el.scrollWidth > el.clientWidth + 2;
        }).map(el => ({ tag: el.tagName, class: el.className, text: el.textContent.slice(0, 100) })));
        const headings = await page.getByRole('heading', { level: 1 }).allTextContents();
        await page.screenshot({ path: `${out}/${screen}/${size}-${theme}.png`, fullPage: true });
        captures.push({ screen, path, size, theme, words: words.trim().split(/\s+/).filter(Boolean).length, text: words, headings, overflow, clipped });
        await writeFile(`${out}/pages-progress.json`, JSON.stringify(captures, null, 2));
        console.log('captured', screen, size, theme, overflow ? 'OVERFLOW' : '');
        if (!process.env.DESIGN_BASELINE && (screen === 'target' || screen === 'target-unknown')) {
          await page.locator('.capacity-details, .cap-details, .target-deployments, .target-settings').evaluateAll(els => els.forEach(el => { el.open = true; }));
          await page.screenshot({ path: `${out}/${screen}/${size}-${theme}-details.png`, fullPage: true });
          if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error(screen + ': open details overflow');
        }
      }
    }
  }
  // Login is a separate session; the invite page above is public too.
  await context.clearCookies();
  for (const [size, viewport] of Object.entries(selected.size && !selected.has('login') ? {} : { desktop: { width: 1440, height: 1000 }, phone: { width: 390, height: 844 } })) {
    await page.setViewportSize(viewport);
    for (const theme of ['light', 'dark']) {
      await page.goto(`${base}/?theme=${theme}`, { waitUntil: 'networkidle' });
      await page.locator('form').waitFor();
      await mkdir(`${out}/login`, { recursive: true });
      const words = await page.locator('body').innerText();
        if (screen === 'target' && !process.env.DESIGN_BASELINE && !words.includes('Apps may need more memory than this host has.')) throw new Error('host memory warning missing before Details');
      await page.screenshot({ path: `${out}/login/${size}-${theme}.png`, fullPage: true });
      captures.push({ screen: 'login', path: '/', size, theme, words: words.trim().split(/\s+/).length, headings: await page.getByRole('heading', { level: 1 }).allTextContents(), overflow: false });
    }
  }
  await writeFile(`${out}/pages.json`, JSON.stringify(captures, null, 2));
  if (errors.length) throw new Error(errors.join('\n'));
  if (!process.env.DESIGN_BASELINE) {
    const issues = captures.filter(c => c.overflow || c.headings.length !== 1 || (c.size === 'phone' && c.clipped?.length));
    if (issues.length) throw new Error('layout issues: ' + JSON.stringify(issues));
  }
  await writeFile(`${out}/capture.json`, JSON.stringify({ project: 'club-newsletter', change: process.env.DESIGN_CHANGE, actor: 'sam', infraCases, viewports: { desktop: '1440×1000', phone: '390×844' }, facts, records: 'disposable local tour scene; actual API responses; local scripts; recorded agent transcripts; running production test daemon; unreachable preview transport; backup owner operation; passing approval and failing photo review; summary provider unavailable', errors }, null, 2));
} finally { await browser.close(); }
