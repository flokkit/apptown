// The tour's camera (make tour; internal/apptown/tour_test.go starts it):
// signs in to the tour's AppTown and photographs each stop at 1440x900,
// device scale 2, in the light and the dark theme, with animations and the
// text caret off. Writes <out>/<id>-light.png and <id>-dark.png.
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
      const url = base + shot.path + (shot.path.includes('?') ? '&' : '?') + 'theme=' + theme;
      await page.goto(url, { waitUntil: 'networkidle' });
      if (shot.wait) await page.getByText(shot.wait, { exact: false }).first().waitFor();
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
