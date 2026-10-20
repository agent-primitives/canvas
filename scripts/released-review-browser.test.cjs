const test = require('node:test');
const assert = require('node:assert/strict');
const { execFileSync, spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || path.join(execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim(), 'playwright'));
const root = path.join(__dirname, '..');

test('released exports and mismatch render honestly through actual engine, adapter and browser', async t => {
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'canvas-release-'));
  t.after(() => fs.rmSync(scratch, { recursive: true, force: true }));
  const binary = path.join(scratch, 'copilot-review');
  execFileSync('go', ['build', '-o', binary, './cmd/copilot-review'], { cwd: root, stdio: 'pipe' });
  const { startBridge } = await import(pathToFileURL(path.join(root, '.github/extensions/northwind-review/server.mjs')));
  for (const mismatch of [false, true]) await t.test(mismatch ? 'mismatched subject' : 'matching exported evidence', async t => {
    const filename = mismatch ? 'canvas-mismatched.yaml' : 'canvas.yaml';
    const child = spawn(binary, ['-definition', `examples/northwind-released/${filename}`, '-state', path.join(scratch, mismatch ? 'mismatch' : 'matching'), '-listen', '127.0.0.1:0'], { cwd: root, env: { PATH: process.env.PATH, HOME: process.env.HOME, TMPDIR: os.tmpdir() }, stdio: ['ignore', 'pipe', 'pipe'] });
    const exited = new Promise(resolve => { child.once('exit', resolve); child.once('error', resolve); });
    t.after(async () => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM'); await exited; });
    let stderr = ''; child.stderr.on('data', chunk => { stderr += chunk; });
    const engineURL = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`Released demo startup failed: ${stderr}`)), 10000);
      exited.then(() => { clearTimeout(timer); reject(new Error(`Released demo exited: ${stderr}`)); });
      child.stdout.on('data', chunk => { const match = String(chunk).match(/http:\/\/127\.0\.0\.1:\d+/); if (match) { clearTimeout(timer); resolve(match[0]); } });
    });
    const bridge = await startBridge({ engineURL }); t.after(() => bridge.close());
    const browser = await chromium.launch({ headless: true }); t.after(() => browser.close());
    const page = await browser.newPage(); page.setDefaultTimeout(5000);
    const posts = [], errors = [];
    page.on('request', request => { if (request.method() === 'POST') posts.push(new URL(request.url()).pathname); });
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(bridge.url);
    await page.locator('#draft:not(:disabled)').waitFor();
    const evidence = page.locator('#suite-evidence');
    await evidence.filter({ hasText: mismatch ? 'not verified (subject)' : 'evaluation consistent; unsigned' }).waitFor({ state: 'attached' });
    const text = await evidence.textContent();
    assert(text.includes('sha256:b9513f8f72b41ea36fde2179da726f62d1d28412402621023cfc0f23cadb3544'));
    for (const value of ['northwind-mock', 'execution not qualified', 'mocked native transport']) assert(text.includes(value), value);
    assert.doesNotMatch(text, /attested/i);
    assert.deepEqual(posts, [], 'rendering cannot dispatch work');
    await page.locator('#draft').click();
    await page.locator('#document-status').filter({ hasText: 'Document decision: pending' }).waitFor();
    await page.locator('#refresh:not(:disabled)').waitFor();
    assert.equal(await page.locator('#documents > details').count(), 2);
    assert((await evidence.textContent()).includes(mismatch ? 'not verified (subject)' : 'evaluation consistent; unsigned'));
    // Missing historical identity must not borrow passing current-agent evidence.
    let removeHistoricalRun = false;
    await page.route(bridge.origin + '/api/state', async route => {
      const response = await route.fetch(); const data = await response.json();
      data.runs = removeHistoricalRun ? [] : data.runs.map(run => ({ ...run, suite: null }));
      await route.fulfill({ json: data });
    });
    for (const missingRun of [false, true]) {
      removeHistoricalRun = missingRun;
      await page.locator('#refresh:not(:disabled)').waitFor(); await page.locator('#refresh').click();
      await page.locator('#refresh:not(:disabled)').waitFor();
      const historical = await evidence.textContent();
      assert(historical.includes('not verified (historical provenance unavailable)'), missingRun ? 'missing run' : 'missing suite');
      assert.doesNotMatch(historical, /evaluation consistent; unsigned/);
    }
    await page.goto(engineURL);
    await page.locator('#suite-status').filter({ hasText: mismatch ? 'not verified (subject)' : 'evaluation consistent; unsigned' }).waitFor();
    assert((await page.locator('#agents').textContent()).includes('execution not qualified'));
    assert.deepEqual(errors, []);
    await page.setViewportSize({ width: 320, height: 900 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'browser evidence overflows at 320px');
  });
  const denied = spawn(binary, ['-definition', 'examples/northwind-released/canvas.yaml'], { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] });
  let refusal = ''; denied.stderr.on('data', chunk => { refusal += chunk; });
  const code = await new Promise(resolve => denied.once('exit', resolve));
  assert.equal(code, 1); assert.match(refusal, /explicit separate -state/);
});
