const test = require('node:test');
const assert = require('node:assert/strict');
const { execFileSync, spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || path.join(execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim(), 'playwright'));
const root = path.join(__dirname, '..');

test('a generated canvas runs locally and reviews a visible fixture proposal', async t => {
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'canvas-authoring-browser-'));
  t.after(() => fs.rmSync(scratch, { recursive: true, force: true }));
  const binary = path.join(scratch, 'canvas');
  execFileSync('go', ['build', '-o', binary, './cmd/canvas'], { cwd: root, stdio: 'pipe' });
  const packageDir = path.join(scratch, 'my-review');
  execFileSync(binary, ['init', packageDir, '--fixture', '--root', root], { stdio: 'pipe' });
  const seed = JSON.stringify({ text: 'A newcomer wants a review of a fictional project note.' });
  fs.writeFileSync(path.join(packageDir, 'seeds/draft.json'), seed);
  const definition = path.join(packageDir, 'canvas.yaml');
  execFileSync(binary, ['validate', definition], { stdio: 'pipe' });
  const child = spawn(binary, ['--fixture', '--definition', definition, '--state', path.join(scratch, 'state'), '--listen', '127.0.0.1:0'], {
    cwd: root, env: { PATH: process.env.PATH, HOME: process.env.HOME, TMPDIR: os.tmpdir() }, stdio: ['ignore', 'pipe', 'pipe'],
  });
  const exited = new Promise(resolve => { child.once('exit', resolve); child.once('error', resolve); });
  t.after(async () => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM'); await exited; });
  let stderr = ''; child.stderr.on('data', chunk => { stderr += chunk; });
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`Fixture startup failed: ${stderr}`)), 10000);
    exited.then(() => { clearTimeout(timer); reject(new Error(`Fixture exited: ${stderr}`)); });
    child.stdout.on('data', chunk => { const match = String(chunk).match(/Canvas: (http:\/\/127\.0\.0\.1:\d+)/); if (match) { clearTimeout(timer); resolve(match[1]); } });
  });
  const browser = await chromium.launch({ headless: true }); t.after(() => browser.close());
  const page = await browser.newPage(); page.setDefaultTimeout(5000);
  const posts = [], errors = [];
  page.on('request', request => { if (request.method() === 'POST') posts.push(new URL(request.url()).pathname); });
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(url);
  await page.locator('#provider-guidance').filter({ hasText: 'Deterministic fixture provider' }).waitFor();
  assert.match(await page.locator('#provider-guidance').textContent(), /No real inference/);
  const state = await (await page.request.get(url + '/api/state')).json();
  assert.equal(state.runs.length, 0, 'startup must not request work');
  assert.equal(state.agents[0].model, 'northwind-mock', 'authored identity must not be rewritten');
  const run = async () => {
    if (!await page.locator('#new-menu').evaluate(node => node.open)) await page.locator('#new-menu > summary').click();
    await page.locator('#generic-run-toggle').click();
    await page.locator('#generic-agent').selectOption('writer');
    await page.locator('#generic-request').fill('Show a deterministic local proposal for review.');
    await page.locator('#generic-run-submit').click();
    await page.getByRole('button', { name: 'Accept proposal', exact: true }).first().waitFor();
  };
  await run();
  assert.match(await page.locator('#review').textContent(), /A newcomer wants a review/);
  assert.match(await page.locator('#review').textContent(), /Deterministic fixture proposal/);
  await page.getByRole('button', { name: 'Accept proposal', exact: true }).first().click();
  await page.locator('#notice').filter({ hasText: 'Proposal accepted' }).waitFor();
  const accepted = await (await page.request.get(url + '/api/document?path=draft.json')).json();
  assert.notEqual(accepted.accepted, seed);
  await run();
  await page.getByRole('button', { name: 'Reject', exact: true }).first().click();
  await page.locator('#notice').filter({ hasText: 'Proposal rejected' }).waitFor();
  const rejected = await (await page.request.get(url + '/api/document?path=draft.json')).json();
  assert.equal(rejected.accepted, accepted.accepted, 'rejection must preserve accepted content');
  assert.equal((await (await page.request.get(url + '/api/state')).json()).runs.length, 2);
  assert.deepEqual(posts.filter(p => !['/api/run', '/api/review'].includes(p)), []);
  assert.deepEqual(errors, []);
});
