const test = require('node:test');
const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const { createServer } = require('node:http');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || path.join(execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim(), 'playwright'));

test('review panel shows independent verified identity and role without inventing remote usage or replay', async t => {
  const root = path.join(__dirname, '../.github/extensions/northwind-review');
  const reviewer = { issuer: 'https://issuer.example', subject: '<img src=x onerror=alert(1)>', role: 'GM' };
  const proposal = { id: 'proposal-a', status: 'accepted', reviewer, changes: { 'document-a.md': 'one', 'document-b.md': 'two' } };
  const server = createServer((req, res) => {
    const url = new URL(req.url, 'http://localhost');
    if (['/', '/app.js', '/style.css'].includes(url.pathname)) { res.setHeader('Content-Type', url.pathname.endsWith('.js') ? 'text/javascript' : url.pathname.endsWith('.css') ? 'text/css' : 'text/html'); res.end(fs.readFileSync(path.join(root, url.pathname === '/' ? 'index.html' : url.pathname.slice(1)))); return; }
    let data;
    if (url.pathname === '/api/demo') data = { mode: 'authenticated-sessions', boot: 'boot', identity: 'engine-verified per action; sessions authorizes' };
    else if (url.pathname === '/api/state') data = { head: 'head', proposals: [proposal], runs: [], agents: [], announcements: [{ id: 'op-a', action: 'announce', proposal: proposal.id, stage: 'idle' }] };
    else if (url.pathname === '/api/document') data = { path: url.searchParams.get('path'), diff: '+proposed', proposed: 'proposed' };
    else if (url.pathname === '/api/announcement') data = { id: 'op-a', status: 'approved', reviewer: { ...reviewer, subject: 'different-approver' }, identitySource: 'engine-verified', snapshot: { content: 'reviewed text' }, confirmation: {} };
    else { res.writeHead(404); res.end('{}'); return; }
    res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(data));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)); });
  const browser = await chromium.launch({ headless: true }); t.after(() => browser.close());
  const page = await browser.newPage(); page.setDefaultTimeout(5000);
  await page.goto(`http://127.0.0.1:${server.address().port}/#token=${'a'.repeat(64)}`);
  await page.locator('#document-reviewer').filter({ hasText: reviewer.subject }).waitFor();
  assert.match(await page.locator('#document-reviewer').textContent(), /role: GM/);
  assert.match(await page.locator('#document-reviewer').textContent(), /engine-verified/);
  assert.match(await page.locator('#action-reviewer').textContent(), /different-approver/);
  assert.match(await page.locator('#action-reviewer').textContent(), /not sessions-attested/);
  assert.match(await page.locator('#action-reviewer').textContent(), /selected attribution/);
  assert.equal(await page.locator('#document-reviewer img').count(), 0);
  assert.equal(await page.locator('#documents > details').count(), 2);
  assert.doesNotMatch(await page.locator('#meter').textContent(), /Real model calls: 0|undefined/);
  assert.equal(await page.locator('#verify-action').isDisabled(), true);
});

for (const mode of ['trusted-loopback-sessions', 'hosted-sessions', 'hosted-model', 'offline-model']) test(`real provider modes show truthful usage and replay availability: ${mode}`, async t => {
  const root = path.join(__dirname, '../.github/extensions/northwind-review');
  const proposal = { id: 'proposal-a', status: 'accepted', changes: { 'prd.json': 'proposed', 'announcement.md': 'proposed announcement' } };
  let actionStatus = 'simulated', includeAction = true, posts = 0;
  const server = createServer((req, res) => {
    const url = new URL(req.url, 'http://localhost');
    if (['/', '/app.js', '/style.css'].includes(url.pathname)) {
      res.setHeader('Content-Type', url.pathname.endsWith('.js') ? 'text/javascript' : url.pathname.endsWith('.css') ? 'text/css' : 'text/html');
      res.end(fs.readFileSync(path.join(root, url.pathname === '/' ? 'index.html' : url.pathname.slice(1)))); return;
    }
    if (req.method === 'POST') posts++;
    let data;
    if (url.pathname === '/api/demo') data = { mode, boot: 'boot', providerInvocations: 2, completionTokenCeiling: 4096, modelCalls: null, executorCalls: null };
    else if (url.pathname === '/api/state') data = { head: 'head', proposals: [proposal], runs: [{ id: 'run', proposal: proposal.id, proof: { chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0, executorCalls: 0, executionIDs: ['old-execution'] } }], agents: [], announcements: includeAction ? [{ id: 'op', action: 'announce', proposal: proposal.id, stage: 'idle' }] : [] };
    else if (url.pathname === '/api/document') data = { path: url.searchParams.get('path'), diff: '+proposed', proposed: 'proposed' };
    else if (url.pathname === '/api/announcement') data = { id: 'op', status: actionStatus, concluded: actionStatus === 'simulated', snapshot: { content: 'simulated text' }, confirmation: {} };
    else { res.writeHead(404); res.end('{}'); return; }
    res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(data));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)); });
  const browser = await chromium.launch({ headless: true }); t.after(() => browser.close());
  const page = await browser.newPage(); page.setDefaultTimeout(5000);
  await page.goto(`http://127.0.0.1:${server.address().port}/#token=${'b'.repeat(64)}`);
  await page.locator('#connection').filter({ hasText: 'Connected.' }).waitFor();
  assert.match(await page.locator('#identity').textContent(), /not exercised/);
  const meter = await page.locator('#meter').textContent();
  assert.doesNotMatch(meter, /Real model calls: 0|Fixture provider|undefined/);
  const remote = ['trusted-loopback-sessions', 'hosted-sessions'].includes(mode);
  if (remote) {
    assert.match(meter, /unknown|not reported/);
    assert.equal(await page.locator('#draft-proof').textContent(), '', 'remote mode must not present a saved local proof as remote qualification');
  }
  else assert.match(meter, /Provider invocations.*2.*4096/);
  assert.equal(await page.locator('#verify').isDisabled(), remote);
  assert.equal(await page.locator('#verify-action').isDisabled(), remote);
  if (mode === 'hosted-sessions') {
    assert.match(await page.locator('#identity').textContent(), /hosted|trusted.network/i);
    assert.match(await page.locator('#action-help').textContent(), /unavailable|not supported/i);
    for (const status of ['pending-approval', 'approved', 'denied']) {
      actionStatus = status;
      await page.locator('#refresh').click();
      await page.waitForFunction(status => document.querySelector('#evidence').textContent.includes(`"status": "${status}"`), status);
      assert.equal(await page.locator('#action-confirm').isDisabled(), true);
      // Force a checked box to prove that a retained confirmation cannot re-enable hosted actions.
      await page.locator('#action-confirm').evaluate(element => { element.checked = true; element.dispatchEvent(new Event('change')); });
      for (const id of ['request', 'approve', 'deny', 'continue', 'verify', 'verify-action']) assert.equal(await page.locator(`#${id}`).isDisabled(), true, `${id} disabled for hosted ${status}`);
    }
    includeAction = false;
    await page.locator('#refresh').click();
    await page.waitForFunction(() => document.querySelector('#evidence').textContent === 'No action requested.');
    assert.equal(await page.locator('#request').isDisabled(), true, 'accepted document does not enable hosted announcement request');
    assert.equal(posts, 0);
  }
});
