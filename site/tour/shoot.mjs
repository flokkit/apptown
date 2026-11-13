// The tour's camera (make tour; internal/apptown/browser_security_browser_test.go starts it):
// signs in to the tour's AppTown and photographs each stop at 1440x900,
// device scale 2, in the light and the dark theme, with animations and the
// text caret off. Writes <out>/<id>-light.png and <id>-dark.png. A stop's
// click is a button's text: the camera clicks it and waits for the dialog
// it opens before the shot. A happy stop (the story's stops, Home and
// Projects) fails the run if it shows a failed or unknown status chip in
// the shot, or the word failed anywhere on the page. TestTour
// runs it once for each phase of the story (tour.yaml's after).
const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE || 'playwright');
const base = process.env.TOUR_URL;
const shots = JSON.parse(process.env.TOUR_SHOTS);
const out = process.env.TOUR_OUT;

const browser = await chromium.launch({ headless: true, args: ['--no-proxy-server'] });
const context = await browser.newContext({
  ignoreHTTPSErrors: true,
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 2,
  // The scene's times are UTC (TestTour), so every machine shoots the
  // same clock times.
  timezoneId: 'UTC',
});
context.setDefaultTimeout(20000);
const page = await context.newPage();
// The disk the tour shows is a fixed, plausible one: the real reading would
// publish the camera machine's own disk. Only the shot is staged; the page
// renders this answer exactly as it renders a real one.
const GiB = 1024 ** 3;
await page.route('**/api/v1/infrastructure/disks', (route) => route.fulfill({
  contentType: 'application/json',
  body: JSON.stringify({ disks: [{
    what: "this AppTown's data folder", path: '/var/lib/apptown',
    totalBytes: 500 * GiB, freeBytes: 312 * GiB, floorBytes: 20 * GiB,
    refuses: false, low: false, readAt: new Date().toISOString(),
  }] }),
}));
try {
  await page.goto(base + '/');
  const status = await page.evaluate(async ([username, password]) => {
    const r = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }) });
    return r.status;
  }, [process.env.TOUR_USER, process.env.TOUR_PASSWORD]);
  if (status !== 200) throw new Error('sign-in answered ' + status);
  for (const shot of shots) {
    for (const theme of ['light', 'dark']) {
      // The theme is a query parameter; a path's #anchor stays last.
      const url = new URL(/^https?:\/\//.test(shot.path) ? shot.path : base + shot.path);
      url.searchParams.set('theme', theme);
      await page.goto(url.href, { waitUntil: 'networkidle' });
      if (shot.wait) await page.getByText(shot.wait, { exact: false }).first().waitFor();
      // A stop that shows an error instead of its page fails the tour: a
      // removed page (the app's "Page not found"), a page that could not
      // render (ErrorBoundary), or a read the page could not make
      // (ReadStatus) would otherwise be photographed and published as if
      // it were the page.
      const wrong = await page.evaluate(() => {
        if ([...document.querySelectorAll('h1')].some((h) => h.textContent.trim() === 'Page not found')) return 'Page not found';
        if (document.body.innerText.includes('This page could not be displayed.')) return 'This page could not be displayed.';
        const failed = document.querySelector('div.error');
        if (failed) return 'a read failed: ' + (failed.innerText.trim() || failed.querySelector('[title],[aria-label]')?.getAttribute('title') || failed.querySelector('[aria-label]')?.getAttribute('aria-label') || '');
        const denied = [...document.querySelectorAll('.muted')].find((e) => e.textContent.startsWith('Not available to you'));
        if (denied) return denied.textContent.trim();
        return '';
      });
      if (wrong) throw new Error(`stop ${shot.id} (${shot.path}) shows ${wrong} instead of its page`);
      // The happy path shows nothing failed and nothing unknown: a failed
      // or unknown status chip, or the word failed, on a happy stop means
      // the scene is not the story the tour tells.
      if (shot.happy) {
        // The scene settled before the camera came (tourWorld.settle): once
        // the page has finished its reads, it is judged once.
        await page.waitForLoadState('networkidle');
        const trouble = await page.evaluate(() => {
          const main = document.querySelector('main') || document.body;
          // A chip is judged where the shot sees it: a standing note
          // below the fold (a deployment's data protection names what it
          // never assesses as unknown) is not in the photo.
          const chip = [...main.querySelectorAll('.chip.fail, .chip.unknown')].find((c) => {
            const r = c.getBoundingClientRect();
            return r.width > 0 && r.bottom > 0 && r.top < window.innerHeight;
          });
          if (chip) {
            const where = chip.closest('li, tr, section, a') || chip.parentElement;
            return `a ${chip.classList.contains('fail') ? 'failed' : 'unknown'} status (${(chip.getAttribute('aria-label') || chip.getAttribute('data-help') || chip.textContent).trim()}) in "${where.innerText.replace(/\s+/g, ' ').slice(0, 160)}"`;
          }
          const text = main.innerText;
          const at = text.search(/\bfailed\b/i);
          if (at >= 0) return `"${text.slice(Math.max(0, at - 60), at + 20).replace(/\s+/g, ' ')}"`;
          return '';
        });
        if (trouble) throw new Error(`happy stop ${shot.id} (${shot.path}) shows ${trouble}`);
      }
      if (shot.id === 'change' || shot.id === 'decide') {
        for (const name of [/^Accept(?: and put live)?$/, 'Send back…']) {
          await page.locator('#decision').getByRole('button', { name, exact: true }).waitFor();
        }
      }
      if (shot.click) {
        await page.getByRole('button', { name: shot.click, exact: true }).first().click();
        await page.getByRole('dialog').first().waitFor();
      }
      await page.addStyleTag({ content: '*, *::before, *::after { animation: none !important; transition: none !important; caret-color: transparent !important; }' });
      await page.mouse.move(0, 0);
      await page.waitForTimeout(400);
      await page.screenshot({ path: `${out}/${shot.id}-${theme}.png` });
      console.log('shot', shot.id, theme);
    }
  }
} finally {
  await browser.close();
}
