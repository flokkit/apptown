// The tour's camera (make tour; internal/forge/tour_test.go starts it):
// signs in to the tour's AppTown and photographs each stop at 1440x900,
// device scale 2, in the light and the dark theme, with animations and the
// text caret off. Writes <out>/<id>-light.png and <id>-dark.png.
const { chromium } = await import(process.env.FORGE_PLAYWRIGHT_MODULE || 'playwright');
const base = process.env.TOUR_URL;
const shots = JSON.parse(process.env.TOUR_SHOTS);
const out = process.env.TOUR_OUT;

const browser = await chromium.launch({ headless: true, args: ['--no-proxy-server'] });
const context = await browser.newContext({
  ignoreHTTPSErrors: true,
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 2,
});
context.setDefaultTimeout(20000);
const page = await context.newPage();
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
