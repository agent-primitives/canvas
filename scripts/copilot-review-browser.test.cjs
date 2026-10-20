const test = require('node:test');
const assert = require('node:assert/strict');
const { execFileSync, spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { createServer } = require('node:http');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || path.join(execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim(), 'playwright'));
const root = path.join(__dirname, '..');

test('actual iframe drives the deterministic engine with separate document/approval/Continue and replay 0/0', async t => {
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'canvas-copilot-'));
  t.after(() => fs.rmSync(scratch, { recursive: true, force: true }));
  const binary = path.join(scratch, 'copilot-review');
  execFileSync('go', ['build', '-o', binary, './cmd/copilot-review'], { cwd: root, stdio: 'pipe' });
  const child = spawn(binary, ['-state', path.join(scratch, 'state'), '-listen', '127.0.0.1:0'], { cwd: root, env: { PATH: process.env.PATH, HOME: process.env.HOME, TMPDIR: os.tmpdir() }, stdio: ['ignore', 'pipe', 'pipe'] });
  const exited = new Promise(resolve => { child.once('exit', resolve); child.once('error', resolve); });
  t.after(async () => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM'); await exited; });
  child.stderr.resume();
  const engineURL = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('Demo startup did not complete')), 10000);
    exited.then(() => { clearTimeout(timeout); reject(new Error('Demo exited before readiness')); });
    child.stdout.on('data', chunk => { const match = String(chunk).match(/http:\/\/127\.0\.0\.1:\d+/); if (match) { clearTimeout(timeout); resolve(match[0]); } });
  });
  const { startBridge } = await import(pathToFileURL(path.join(root, '.github/extensions/northwind-review/server.mjs')));
  const bridge = await startBridge({ engineURL }); t.after(() => bridge.close());
  const parent = createServer((req, res) => { res.setHeader('Content-Type', 'text/html'); res.end(`<iframe title="Northwind" src="${bridge.url}" style="width:100%;height:1400px;border:0"></iframe>`); });
  await new Promise(resolve => parent.listen(0, '127.0.0.1', resolve));
  t.after(() => { parent.closeAllConnections(); return new Promise(resolve => parent.close(resolve)); });
  const browser = await chromium.launch({ headless: true }); t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 900, height: 1000 } });
  page.setDefaultTimeout(5000);
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  const posts = [];
  page.on('request', request => { if (request.method() === 'POST') posts.push(new URL(request.url()).pathname); });
  await page.goto(`http://localhost:${parent.address().port}`);
  const frame = page.frameLocator('iframe');
  // Ordinary actions need completed inspection; deliberate held-read checks below do not.
  const waitForInspection = () => frame.locator('#refresh:not(:disabled)').waitFor();
  await waitForInspection();
  await frame.locator('#draft:not(:disabled)').waitFor();
  assert.equal(await frame.locator('#accept').isDisabled(), true);
  // Force proposal publication before the run records its proposal association.
  let splitPublication = false;
  const proposalBeforeRun = async route => {
    const response = await route.fetch(); const data = await response.json();
    if (!data.proposals.length) return route.fulfill({ response });
    splitPublication = true;
    await route.fulfill({ json: { ...data, runs: data.runs.map(run => ({ ...run, proposal: '', status: 'running' })) } });
  };
  await page.route(bridge.origin + '/api/state', proposalBeforeRun);
  // Hold the first document read: status can update before inspected bytes.
  let releaseInitialDocuments, observedInitialDocuments;
  const initialDocumentsHeld = new Promise(resolve => { observedInitialDocuments = resolve; });
  const initialDocumentsRelease = new Promise(resolve => { releaseInitialDocuments = resolve; });
  const holdInitialDocuments = async route => { observedInitialDocuments(); await initialDocumentsRelease; await route.continue(); };
  await page.route(bridge.origin + '/api/document?*', holdInitialDocuments);
  let initialInspection, initialInspectionFinished = false;
  try {
    await frame.locator('#draft').click();
    await initialDocumentsHeld;
    assert.equal(splitPublication, true, 'held inspection must cover proposal-before-run publication');
    await frame.locator('#document-status').filter({ hasText: 'Document decision: pending' }).waitFor();
    initialInspection = waitForInspection().then(() => { initialInspectionFinished = true; });
    assert.equal(await frame.locator('#refresh').isDisabled(), true);
    assert.equal(await frame.locator('#documents > details').count(), 0);
    assert.equal(initialInspectionFinished, false, 'early status is not completed inspection');
  } finally {
    await page.unroute(bridge.origin + '/api/state', proposalBeforeRun);
    releaseInitialDocuments(); if (initialInspection) await initialInspection;
  }
  await page.unroute(bridge.origin + '/api/document?*', holdInitialDocuments);
  assert.equal(await frame.locator('#documents > details').count(), 2);
  // Only a later state poll can associate the run and re-enable Draft.
  await frame.locator('#draft:not(:disabled)').waitFor();
  await frame.locator('#document-confirm').check();
  // Changed state must revoke consent before its replacement document bytes arrive.
  let releaseDocuments, observedDocuments, changeDocumentState = true, holdDocuments = true;
  const documentsHeld = new Promise(resolve => { observedDocuments = resolve; });
  const documentsRelease = new Promise(resolve => { releaseDocuments = resolve; });
  await page.route(bridge.origin + '/api/state', async route => {
    if (!changeDocumentState) return route.continue();
    changeDocumentState = false;
    const response = await route.fetch(); const data = await response.json();
    await route.fulfill({ json: { ...data, head: 'changed-document-head' } });
  });
  await page.route(bridge.origin + '/api/document?*', async route => {
    if (holdDocuments) { observedDocuments(); await documentsRelease; }
    await route.continue();
  });
  try {
    await documentsHeld;
    assert.equal(await frame.locator('#document-confirm').isChecked(), false);
    assert.equal(await frame.locator('#documents > details').count(), 0);
    assert.equal(await frame.locator('#accept').isDisabled(), true, 'changed evidence must disable acceptance while document bytes load');
    assert.equal(await frame.locator('#reject').isDisabled(), true);
    // Even a new click before replacement bytes arrive must not borrow their binding.
    await frame.locator('#document-confirm').check();
    await frame.locator('#accept').evaluate(button => { button.onclick(); });
    assert(!posts.includes('/api/review'));
  } finally { holdDocuments = false; releaseDocuments(); }
  await frame.locator('#status').filter({ hasText: 'Evidence changed' }).waitFor();
  assert(!posts.includes('/api/review'));
  await frame.locator('#refresh:not(:disabled)').waitFor(); await frame.locator('#refresh').click();
  await frame.locator('#refresh:not(:disabled)').waitFor();
  assert.equal(await frame.locator('#documents > details').count(), 2);
  assert.equal(await frame.locator('#document-confirm').isChecked(), false);
  await t.test('consent checked during document loading cannot authorize Accept afterward', async () => {
    let release, observed;
    const held = new Promise(resolve => { observed = resolve; });
    const released = new Promise(resolve => { release = resolve; });
    const changedState = async route => {
      const response = await route.fetch(); const data = await response.json();
      await route.fulfill({ json: { ...data, head: 'loading-consent-head' } });
    };
    const heldDocuments = async route => { observed(); await released; await route.continue(); };
    await page.route(bridge.origin + '/api/state', changedState);
    await page.route(bridge.origin + '/api/document?*', heldDocuments);
    try {
      await frame.locator('#refresh').click(); await held;
      assert.equal(await frame.locator('#documents > details').count(), 0);
      await frame.locator('#document-confirm').check();
      release();
      await frame.locator('#refresh:not(:disabled)').waitFor();
      assert.equal(await frame.locator('#documents > details').count(), 2);
      // Attempt acceptance only after the replacement evidence has been installed.
      await frame.locator('#accept').evaluate(button => button.click());
      await frame.locator('#refresh:not(:disabled)').waitFor();
      assert.equal(await frame.locator('#document-confirm').isChecked(), false);
      assert.equal(await frame.locator('#accept').isDisabled(), true);
      assert(!posts.includes('/api/review'), 'loading-time consent must not dispatch acceptance');
    } finally {
      release();
      await page.unroute(bridge.origin + '/api/document?*', heldDocuments);
      await page.unroute(bridge.origin + '/api/state', changedState);
    }
  });
  await frame.locator('#refresh:not(:disabled)').waitFor(); await frame.locator('#refresh').click();
  await frame.locator('#refresh:not(:disabled)').waitFor();
  await frame.locator('#document-confirm').check(); await frame.locator('#reject').click();
  await frame.locator('#document-status').filter({ hasText: 'Document decision: rejected' }).waitFor();
  await waitForInspection();
  assert.equal(await frame.locator('#request').isDisabled(), true);
  await frame.locator('#draft:not(:disabled)').waitFor(); await frame.locator('#draft').click();
  await frame.locator('#document-status').filter({ hasText: 'Document decision: pending' }).waitFor();
  await waitForInspection();
  await t.test('switching drafts during held inspection cannot authorize unseen bytes and polling retains focus', async () => {
    const current = await frame.locator('#proposal').inputValue();
    const previous = await frame.locator('#proposal option').first().getAttribute('value');
    let release, observed;
    const held = new Promise(resolve => { observed = resolve; });
    const released = new Promise(resolve => { release = resolve; });
    const heldDocuments = async route => { observed(); await released; await route.continue(); };
    await page.route(bridge.origin + '/api/document?*', heldDocuments);
    try {
      await frame.locator('#proposal').selectOption(previous); await held;
      await frame.locator('#proposal').focus();
      assert.equal(await frame.locator('#proposal').isDisabled(), false);
      await frame.locator('#proposal').selectOption(current);
      await frame.locator('#document-confirm').evaluate(el => { el.checked = true; el.dispatchEvent(new Event('change')); });
      assert.equal(await frame.locator('#accept').isDisabled(), true);
      const count = posts.filter(p => p === '/api/review').length;
      await frame.locator('#accept').evaluate(button => { button.onclick(); });
      assert.equal(posts.filter(p => p === '/api/review').length, count);
      release(); await waitForInspection();
      assert.equal(await frame.locator('#documents > details').count(), 2);
      assert.equal(await frame.locator('#document-confirm').isChecked(), false);
      assert.equal(await frame.locator('#accept').isDisabled(), true);
    } finally {
      release(); await page.unroute(bridge.origin + '/api/document?*', heldDocuments);
    }
    await frame.locator('#refresh').click(); await waitForInspection();
    await frame.locator('#proposal').focus();
    const announcements = await frame.locator('#status').evaluate(async el => {
      let changes = 0;
      const observer = new MutationObserver(records => { changes += records.length; });
      for (const region of [el, document.getElementById('connection'), document.getElementById('draft-proof')]) observer.observe(region, { childList: true });
      await new Promise(resolve => setTimeout(resolve, 1800)); observer.disconnect(); return changes;
    });
    assert.equal(announcements, 0, 'unchanged polling must not repeat live announcements');
    assert.equal(await frame.locator('#proposal').evaluate(el => document.activeElement === el), true);
  });
  await t.test('lost response after acceptance never duplicates the write', async () => {
    const before = posts.filter(p => p === '/api/review').length;
    const loseResponse = async route => { await route.fetch(); await route.abort('failed'); };
    await page.route(bridge.origin + '/api/review', loseResponse);
    try {
      await frame.locator('#document-confirm').check(); await frame.locator('#accept').click();
      await frame.locator('#document-status').filter({ hasText: 'Document decision: accepted' }).waitFor();
      await waitForInspection();
      assert.match(await frame.locator('#status').textContent(), /may have been recorded.*not retried/i);
      await page.waitForTimeout(1800);
      assert.equal(posts.filter(p => p === '/api/review').length, before + 1);
      assert.equal(await frame.locator('#request').isDisabled(), false);
      assert.equal(await frame.locator('#action-confirm').isChecked(), false);
    } finally { await page.unroute(bridge.origin + '/api/review', loseResponse); }
  });
  assert.equal(await frame.locator('#completed-documents').getAttribute('open'), null);
  await frame.locator('#completed-documents > summary').click();
  assert.equal(await frame.locator('#documents > details').count(), 2);
  await frame.locator('#completed-documents > summary').click();
  await frame.locator('#document-status').filter({ hasText: 'Document decision: accepted' }).waitFor();
  await waitForInspection();
  assert.match(await frame.locator('#action-status').textContent(), /Documents accepted.*Request action approval/);
  await frame.locator('#run-evidence > summary').click();
  await frame.locator('#verify:not(:disabled)').waitFor(); await frame.locator('#verify').click();
  await frame.locator('#draft-proof').filter({ hasText: 'model 0 / executor 0' }).waitFor();
  await frame.locator('#request:not(:disabled)').waitFor(); await frame.locator('#request').click();
  await frame.locator('#action-status').filter({ hasText: 'pending-approval' }).waitFor();
  await waitForInspection();
  await frame.locator('#action-confirm:not(:disabled)').waitFor();
  await t.test('abandoned audit records neither block requests nor hide replacement gates', async () => {
    let replacement = false;
    const selectedProposal = await frame.locator('#proposal').inputValue();
    const currentView = JSON.parse(await frame.locator('#evidence').textContent());
    const abandonedState = async route => {
      const response = await route.fetch(); const data = await response.json();
      const current = data.announcements.find(item => item.proposal === selectedProposal);
      const abandoned = { ...current, id: 'abandoned-audit', stage: 'abandoned' };
      await route.fulfill({ json: { ...data, announcements: [abandoned, ...(replacement ? data.announcements : [])] } });
    };
    const abandonedView = async route => {
      // Reuse the engine's full inspection shape for the retained audit fixture.
      await route.fulfill({ json: { ...currentView, id: 'abandoned-audit', status: 'abandoned', concluded: true } });
    };
    await page.route(bridge.origin + '/api/state', abandonedState);
    await page.route(bridge.origin + '/api/announcement?id=abandoned-audit', abandonedView);
    try {
      await frame.locator('#refresh').click(); await frame.locator('#refresh:not(:disabled)').waitFor();
      assert.equal(await frame.locator('#request').isDisabled(), false, 'abandoned-only history must allow requesting a new gate');
      assert.equal(await frame.locator('#action-confirm').isDisabled(), true);
      replacement = true;
      await frame.locator('#refresh').click(); await frame.locator('#refresh:not(:disabled)').waitFor();
      assert.match(await frame.locator('#action-status').textContent(), /pending-approval/);
      assert.equal(await frame.locator('#request').isDisabled(), true);
      assert.equal(await frame.locator('#action-confirm').isDisabled(), false);
      assert.equal(await frame.locator('#approve').isDisabled(), true, 'replacement still requires explicit action consent');
    } finally {
      await page.unroute(bridge.origin + '/api/state', abandonedState);
      await page.unroute(bridge.origin + '/api/announcement?id=abandoned-audit', abandonedView);
    }
  });
  await frame.locator('#action-confirm').check(); await frame.locator('#deny').click();
  await frame.locator('#action-status').filter({ hasText: 'Denial recorded' }).waitFor();
  await waitForInspection();
  assert.equal(await frame.locator('#action-confirm').isChecked(), false);
  assert.equal(await frame.locator('#continue').isDisabled(), true);
  await frame.locator('#action-confirm:not(:disabled)').waitFor(); await frame.locator('#action-confirm').check();
  let releasePoll, observedPoll;
  const pollHeld = new Promise(resolve => { observedPoll = resolve; });
  const pollRelease = new Promise(resolve => { releasePoll = resolve; });
  let holdNext = true;
  await page.route(bridge.origin + '/api/state', async route => {
    if (holdNext) { holdNext = false; observedPoll(); await pollRelease; }
    await route.continue();
  });
  try {
    await pollHeld;
    assert.equal(await frame.locator('#continue').isDisabled(), false, 'unchanged polling must not swallow an explicit Continue click');
    await frame.locator('#continue').click();
    assert(!posts.includes('/api/announcement/continue'), 'held inspection must finish before dispatch');
  } finally { releasePoll(); }
  try {
    await frame.locator('#evidence').filter({ hasText: '"concluded": true' }).waitFor({ state: 'attached' });
  } catch {
    throw new Error(JSON.stringify({ posts, status: await frame.locator('#status').textContent(), action: await frame.locator('#action-status').textContent(), evidence: (await frame.locator('#evidence').textContent()).slice(0, 2500) }));
  }
  assert.equal(await frame.locator('#verify-action').isDisabled(), true);
  // A new accepted revision requires its own native gate, even with equal fixture text.
  await frame.locator('#draft:not(:disabled)').waitFor(); await frame.locator('#draft').click();
  await frame.locator('#document-status').filter({ hasText: 'Document decision: pending' }).waitFor();
  await waitForInspection();
  await frame.locator('#document-confirm:not(:disabled)').waitFor();await frame.locator('#document-confirm').check();await frame.locator('#accept').click();
  await frame.locator('#document-status').filter({ hasText: 'Document decision: accepted' }).waitFor();
  await waitForInspection();
  await frame.locator('#request:not(:disabled)').waitFor();await frame.locator('#request').click();
  await frame.locator('#action-status').filter({ hasText: 'pending-approval' }).waitFor();
  await waitForInspection();
  await frame.locator('#action-confirm:not(:disabled)').waitFor();await frame.locator('#action-confirm').check();await frame.locator('#approve').click();
  await frame.locator('#action-status').filter({ hasText: 'Recorded; not executed' }).waitFor();
  await waitForInspection();
  assert.equal(await frame.locator('#continue').isDisabled(), true);
  assert.match(await frame.locator('#status').textContent(), /Approval recorded; not executed/);
  assert.match(await frame.locator('#action-help').textContent(), /Fresh confirmation/);
  assert.equal(await frame.locator('#action-proof').textContent(), '');
  await t.test('a click awaiting a failed poll leaves recovery manual', async () => {
    let release, observed, reads = 0;
    const held = new Promise(resolve => { observed = resolve; });
    const released = new Promise(resolve => { release = resolve; });
    const failPoll = async route => { reads++; observed(); await released; await route.fulfill({ status: 503, json: { error: 'unavailable', recovery: 'refresh' } }); };
    await page.route(bridge.origin + '/api/state', failPoll);
    try {
      await frame.locator('#action-confirm').check(); await held;
      const before = posts.filter(p => p === '/api/announcement/continue').length;
      await frame.locator('#continue').click(); release(); await waitForInspection();
      assert.equal(await frame.locator('#offline').isVisible(), true);
      await page.waitForTimeout(1800);
      assert.equal(reads, 1, 'awaited failed inspection must not trigger a second read');
      assert.equal(posts.filter(p => p === '/api/announcement/continue').length, before);
    } finally { release(); await page.unroute(bridge.origin + '/api/state', failPoll); }
    await frame.locator('#refresh').click(); await waitForInspection();
    assert.equal(await frame.locator('#offline').isVisible(), false);
  });
  // A held read returning changed evidence cancels the click instead of dispatching it.
  let releaseChanged, observedChanged, changeNext = true;
  const changedHeld = new Promise(resolve => { observedChanged = resolve; });
  const changedRelease = new Promise(resolve => { releaseChanged = resolve; });
  await page.route(bridge.origin + '/api/state', async route => {
    if (!changeNext) return route.continue();
    changeNext = false;
    const response = await route.fetch(); const data = await response.json();
    observedChanged(); await changedRelease;
    await route.fulfill({ json: { ...data, head: 'different-inspected-head' } });
  });
  await frame.locator('#action-confirm:not(:disabled)').waitFor();await frame.locator('#action-confirm').check();
  const continuesBefore = posts.filter(path => path === '/api/announcement/continue').length;
  try { await changedHeld; await frame.locator('#continue').click(); } finally { releaseChanged(); }
  await frame.locator('#status').filter({ hasText: 'Evidence changed' }).waitFor();
  assert.equal(posts.filter(path => path === '/api/announcement/continue').length, continuesBefore);
  assert.equal(await frame.locator('#action-confirm').isChecked(), false);
  await waitForInspection();
  await frame.locator('#action-confirm:not(:disabled)').waitFor();await frame.locator('#action-confirm').check();await frame.locator('#continue').click();
  await frame.locator('#action-status').filter({ hasText: 'SIMULATED_ANNOUNCEMENT' }).waitFor();
  await waitForInspection();
  await frame.locator('#action-evidence > summary').click();
  await frame.locator('#verify-action:not(:disabled)').waitFor();await frame.locator('#verify-action').click();
  await frame.locator('#action-proof').filter({ hasText: 'model 0 / executor 0' }).waitFor();
  await t.test('offline records are last-known and only explicit Refresh recovers the same view', async () => {
    let reads = 0;
    const unavailable = async route => { reads++; await route.fulfill({ status: 503, json: { error: 'unavailable', recovery: 'refresh' } }); };
    await page.route(bridge.origin + '/api/state', unavailable);
    try {
      await frame.locator('#refresh').click(); await waitForInspection();
      assert.equal(await frame.locator('#offline').isVisible(), true);
      assert.match(await frame.locator('#connection').textContent(), /Last known.*Refresh connection/);
      assert.doesNotMatch(await frame.locator('#status').textContent(), /Confirm again|then Continue/);
      assert.match(await frame.locator('#action-status').textContent(), /Last known/);
      assert.match(await frame.locator('#action-help').textContent(), /Refresh connection/);
      assert.equal(await frame.locator('#draft').isDisabled(), true);
      assert.equal(await frame.locator('#proposal').isDisabled(), true, 'last-known content must retain its displayed draft identity');
      const stoppedAt = reads; await page.waitForTimeout(1800);
      assert.equal(reads, stoppedAt, 'offline inspection must not auto-retry');
    } finally { await page.unroute(bridge.origin + '/api/state', unavailable); }
    await frame.locator('#refresh').click(); await waitForInspection();
    assert.equal(await frame.locator('#offline').isVisible(), false);
    assert.match(await frame.locator('#connection').textContent(), /Connected/);
    let observedSelectionPoll, releaseSelectionPoll;
    const selectionPollHeld = new Promise(resolve => { observedSelectionPoll = resolve; });
    const selectionPollRelease = new Promise(resolve => { releaseSelectionPoll = resolve; });
    // A background poll may arrive first; deliver its outage only after selection.
    const unavailableOnSelection = async route => { observedSelectionPoll(); await selectionPollRelease; await unavailable(route); };
    await page.route(bridge.origin + '/api/state', unavailableOnSelection);
    try {
      await selectionPollHeld;
      const previous = await frame.locator('#proposal option').nth(1).getAttribute('value');
      const oldEvidence = await frame.locator('#evidence').textContent();
      await frame.locator('#proposal').selectOption(previous);
      releaseSelectionPoll(); await waitForInspection();
      assert.equal(await frame.locator('#proposal').inputValue(), previous);
      assert.equal(await frame.locator('#documents > details').count(), 0, 'failed switch cannot retain previous document evidence');
      assert.equal(await frame.locator('#snapshot').isHidden(), true);
      assert.notEqual(await frame.locator('#evidence').textContent(), oldEvidence);
      assert.match(await frame.locator('#action-status').textContent(), /unknown/i);
      assert.equal(await frame.locator('#scope').textContent(), '');
    } finally {
      releaseSelectionPoll();
      await page.unroute(bridge.origin + '/api/state', unavailableOnSelection);
    }
    await frame.locator('#refresh').click(); await waitForInspection();
    const before = posts.length;
    const restarted = route => route.fulfill({ status: 409, json: { error: 'restarted', recovery: 'reopen' } });
    await page.route(bridge.origin + '/api/state', restarted);
    await frame.locator('#refresh').click(); await waitForInspection();
    assert.match(await frame.locator('#connection').textContent(), /Reopen.*Refresh cannot recover/);
    await page.unroute(bridge.origin + '/api/state', restarted);
    await frame.locator('#refresh').click();
    assert.equal(await frame.locator('#draft').isDisabled(), true, 'a stale capability cannot be renewed by Refresh');
    assert.equal(posts.length, before);
  });
  assert.deepEqual(errors, []);
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const actualFrame = page.frames().find(f => f.url().startsWith(bridge.origin));
    assert(await actualFrame.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `overflow at ${width}px`);
  }
  const actualFrame = page.frames().find(f => f.url().startsWith(bridge.origin));
  await actualFrame.evaluate(() => { document.documentElement.style.fontSize = '200%'; });
  assert(await actualFrame.evaluate(() => document.documentElement.scrollWidth <= innerWidth), '200% text has no page overflow');
  await frame.locator('#evidence').focus();
  await frame.locator('#evidence').press('Control+End');
  await actualFrame.waitForFunction(() => document.getElementById('evidence').scrollTop > 0);
  assert(await frame.locator('#evidence').evaluate(el => document.activeElement === el), 'native evidence retains keyboard focus while scrolling');
  await actualFrame.evaluate(() => { document.documentElement.style.fontSize = ''; });
  const height = await actualFrame.evaluate(() => document.documentElement.scrollHeight);
  await page.locator('iframe').evaluate((element, height) => { element.style.height = `${height}px`; }, height);
  await actualFrame.evaluate(() => window.scrollTo(0, 0));
  await page.evaluate(() => window.scrollTo(0, 0));
  fs.mkdirSync(path.join(root, '.state'), { recursive: true });
  await page.screenshot({ path: path.join(root, '.state/copilot-review.png'), fullPage: true });
});
