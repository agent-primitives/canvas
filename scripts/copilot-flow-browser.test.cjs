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

async function flowPanel(t, transform) {
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'canvas-flow-panel-'));
  t.after(() => fs.rmSync(scratch, { recursive: true, force: true }));
  const binary = path.join(scratch, 'copilot-review');
  execFileSync('go', ['build', '-o', binary, './cmd/copilot-review'], { cwd: root, stdio: 'pipe' });
  let definition = 'examples/northwind-prd/flow.yaml';
  if (transform) {
    const directory = path.join(scratch, 'definition');
    fs.cpSync(path.join(root, 'examples/northwind-prd'), directory, { recursive: true });
    definition = path.join(directory, 'flow.yaml');
    fs.writeFileSync(definition, transform(fs.readFileSync(definition, 'utf8')));
  }
  let child, exited, engineURL, bridge;
  async function start(listen = '127.0.0.1:0') {
    child = spawn(binary, ['-definition', definition, '-state', path.join(scratch, 'state'), '-flow-agent-runs', '12', '-listen', listen], { cwd: root, env: { PATH: process.env.PATH, HOME: process.env.HOME, TMPDIR: os.tmpdir() }, stdio: ['ignore', 'pipe', 'pipe'] });
    exited = new Promise(resolve => { child.once('exit', resolve); child.once('error', resolve); });
    child.stderr.resume();
    engineURL = await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('Flow engine startup timeout')), 10000);
      exited.then(() => { clearTimeout(timeout); reject(new Error('Flow engine exited before readiness')); });
      child.stdout.on('data', chunk => { const match = String(chunk).match(/http:\/\/127\.0\.0\.1:\d+/); if (match) { clearTimeout(timeout); resolve(match[0]); } });
    });
  }
  async function stop() { if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM'); await exited; }
  await start(); t.after(stop);
  const { startBridge } = await import(pathToFileURL(path.join(root, '.github/extensions/northwind-review/server.mjs')));
  bridge = await startBridge({ engineURL }); t.after(() => bridge.close());
  const parent = createServer((req, res) => { res.setHeader('Content-Type', 'text/html'); res.end(`<iframe title="Canvas flow" src="${bridge.url}" style="width:100%;height:1800px;border:0"></iframe>`); });
  await new Promise(resolve => parent.listen(0, '127.0.0.1', resolve));
  t.after(() => { parent.closeAllConnections(); return new Promise(resolve => parent.close(resolve)); });
  const browser = await chromium.launch({ headless: true }); t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 900, height: 1000 } }); page.setDefaultTimeout(5000);
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  const posts = []; page.on('request', req => { if (req.method() === 'POST') posts.push({ path: new URL(req.url()).pathname, body: req.postDataJSON() }); });
  const url = `http://localhost:${parent.address().port}`;
  await page.goto(url);
  const frame = page.frameLocator('iframe');
  const ready = () => frame.locator('#refresh:not(:disabled)').waitFor();
  async function api(route) {
    const headers = { 'x-canvas-review-token': new URL(bridge.url).hash.slice('#token='.length) };
    const response = await fetch(bridge.origin + route, { headers }); assert.equal(response.status, 200); return response.json();
  }
  return { page, frame, ready, api, posts, errors, get bridge() { return bridge; },
    async reopen() { await bridge.close(); bridge = await startBridge({ engineURL }); await page.goto(url); await ready(); },
    async restart() { const listen = new URL(engineURL).host; await stop(); await start(listen); },
  };
}

async function startTopic(p, topic, budget = '12', structured = false) {
  await p.ready(); await p.frame.locator('#flow-open:not(:disabled)').click();
  await p.frame.locator('#flow-input-topic').fill(topic);
  await p.frame.locator('#flow-budget').fill(budget);
  if (structured) {
    await p.frame.locator('#flow-input-audience').selectOption('"engineering"');
    await p.frame.locator('#flow-input-includeRisks').selectOption('false');
  }
  await p.frame.locator('#flow-start').click();
  await p.frame.locator('#flow-status').filter({ hasText: 'Waiting for reviewer' }).waitFor(); await p.ready();
}
async function acceptGate(p, step) {
  await p.frame.locator('#flow-location').filter({ hasText: step }).waitFor(); await p.ready();
  await p.frame.locator('#document-confirm:not(:disabled)').check();
  await p.frame.locator('#accept:not(:disabled)').click();
}

test('actual flow iframe binds topic and every human gate, separates Continue and proves aggregate native replay', async t => {
  const p = await flowPanel(t), { page, frame } = p;
  await p.ready();
  assert.equal(await frame.locator('#draft').isVisible(), false, 'manual Run must not compete with flow mode');
  const unsupported = async route => {
    const response = await route.fetch(); const data = await response.json();
    data.flows[0].schema.properties.topic = { type: 'object', properties: { nested: { type: 'string' } } };
    await route.fulfill({ json: data });
  };
  await page.route(p.bridge.origin + '/api/flows', unsupported);
  await frame.locator('#refresh').click(); await p.ready();
  await frame.locator('#flow-open').click();
  assert.equal(await frame.locator('#flow-start').isDisabled(), true, 'unsupported required control must not be silently omitted');
  assert.equal((await p.api('/api/demo')).providerCalls, 0);
  await page.unroute(p.bridge.origin + '/api/flows', unsupported);
  await frame.locator('#refresh').click(); await p.ready();
  await frame.locator('#flow-open').focus(); await page.keyboard.press('Enter');
  assert.equal(await frame.locator('#flow-input-audience').inputValue(), '', 'schema default is a visible hint, not inserted');
  assert.equal(await frame.locator('#flow-input-includeRisks').inputValue(), '', 'optional false/absent remain distinct');
  await frame.locator('#flow-input-topic').fill('Safer fictional setup');
  // Real publication may precede the attempt's public proposal association.
  const splitPublication = async route => { const response = await route.fetch(); const data = await response.json(); await route.fulfill({ json: { ...data, runs: data.runs.map(run => ({ ...run, proposal: '' })) } }); };
  await page.route(p.bridge.origin + '/api/state', splitPublication);
  // The server commits the real Start, but the response is lost. Never mint a replacement key.
  await page.route(p.bridge.origin + '/api/flow/start', async route => { await route.fetch(); await route.abort('failed'); });
  await frame.locator('#flow-start').click();
  await frame.locator('#flow-status').filter({ hasText: 'Waiting for reviewer' }).waitFor(); await p.ready();
  await page.unroute(p.bridge.origin + '/api/flow/start');
  assert.equal(p.posts.filter(x => x.path === '/api/flow/start').length, 1);
  assert.deepEqual(p.posts.find(x => x.path === '/api/flow/start').body.inputs, { topic: 'Safer fictional setup' });
  assert.equal(p.posts.find(x => x.path === '/api/flow/start').body.role, 'GM', 'requester is not the logical gate role');
  assert.equal(await frame.locator('#flow-steps > li').count(), 4);
  await frame.locator('#documents > details').nth(1).waitFor();
  await page.unroute(p.bridge.origin + '/api/state', splitPublication);
  assert.equal(await frame.locator('#documents > details').count(), 2);
  assert.match(await frame.locator('#documents').textContent(), /Safer fictional setup/);
  assert.equal(await frame.locator('#request').isVisible(), false, 'flow actions are prepared only by the engine');
  assert.equal((await p.api('/api/demo')).providerCalls, 1);
  const flowID = (await p.api('/api/flows')).runs.at(-1).id;
  // Closing/reopening the iframe reads the same gate and never cancels or dispatches.
  await p.reopen();
  assert.equal((await p.api('/api/flows')).runs.at(-1).id, flowID);
  assert.equal((await p.api('/api/demo')).providerCalls, 1);
  assert.equal(p.posts.filter(x => /cancel|advance|reconcile/.test(x.path)).length, 0);
  // Consent is revoked before changed-flow evidence is available, not after it.
  await frame.locator('#document-confirm').check();
  let release, observed;
  const held = new Promise(resolve => { observed = resolve; });
  const released = new Promise(resolve => { release = resolve; });
  const changed = async route => { const response = await route.fetch(); const data = await response.json(); await route.fulfill({ json: { ...data, runs: data.runs.map(run => ({ ...run, reason: 'Changed flow inspection' })) } }); };
  const heldDocument = async route => { observed(); await released; await route.continue(); };
  await page.route(p.bridge.origin + '/api/flows', changed);
  await page.route(p.bridge.origin + '/api/document?*', heldDocument);
  try {
    await frame.locator('#refresh').click(); await held;
    assert.equal(await frame.locator('#document-confirm').isChecked(), false);
    assert.equal(await frame.locator('#accept').isDisabled(), true);
  } finally { await page.unroute(p.bridge.origin + '/api/flows', changed); release(); await p.ready(); await page.unroute(p.bridge.origin + '/api/document?*', heldDocument); }
  await p.ready(); await frame.locator('#refresh').click(); await p.ready();
  await acceptGate(p, 'draft'); await acceptGate(p, 'critique'); await acceptGate(p, 'revise');
  await frame.locator('#action-status').filter({ hasText: 'pending-approval' }).waitFor(); await p.ready();
  assert.equal((await p.api('/api/demo')).providerCalls, 3);
  await frame.locator('#action-confirm').check(); await frame.locator('#approve').click();
  await frame.locator('#flow-status').filter({ hasText: 'Approved; not executed' }).waitFor(); await p.ready();
  assert.equal(await frame.locator('#action-confirm').isChecked(), false);
  assert.equal(await frame.locator('#continue').isDisabled(), true);
  const op = (await p.api('/api/flows')).runs.at(-1).action;
  assert.equal((await p.api('/api/announcement?id=' + op)).receipt, undefined, 'approval must not produce receipt');
  await frame.locator('#action-confirm').check(); await frame.locator('#continue').click();
  await frame.locator('#flow-status').filter({ hasText: 'Complete' }).waitFor(); await p.ready();
  assert((await p.api('/api/announcement?id=' + op)).receipt);
  await frame.locator('#flow-panel > details').last().locator('summary').click();
  await frame.locator('#flow-verify').click();
  await frame.locator('#flow-proof').filter({ hasText: 'model 0 / executor 0' }).waitFor();
  assert.equal((await p.api('/api/demo')).providerCalls, 3);
  assert.equal(p.posts.filter(x => x.path === '/api/announcement/continue').length, 1);
  for (const width of [320, 480, 900]) {
    await page.setViewportSize({ width, height: 1000 });
    const actual = page.frames().find(f => f.url().startsWith(p.bridge.origin));
    assert(await actual.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `overflow at ${width}`);
  }
  assert.deepEqual(p.errors, []);
});

test('flow panel counts feedback budget and requires explicit recovery after engine restart', async t => {
  const p = await flowPanel(t), { frame } = p;
  await startTopic(p, 'Fictional feedback', '1', true);
  assert.deepEqual(p.posts.find(x => x.path === '/api/flow/start').body.inputs, { topic: 'Fictional feedback', audience: 'engineering', includeRisks: false });
  await frame.locator('#flow-feedback').fill('Clarify the fictional scope.');
  await frame.locator('#document-confirm').check(); await frame.locator('#flow-changes:not(:disabled)').click();
  await frame.locator('#flow-status').filter({ hasText: 'Budget exhausted' }).waitFor(); await p.ready();
  await frame.locator('#flow-run-record').filter({ hasText: 'Clarify the fictional scope.' }).waitFor({ state: 'attached' });
  assert.match(await frame.locator('#flow-run-record').textContent(), /Fictional feedback/);
  assert.equal((await p.api('/api/demo')).providerCalls, 1);
  await startTopic(p, 'Fictional recovered run', '3');
  await p.restart();
  await frame.locator('#refresh').click();
  await frame.locator('#connection').filter({ hasText: /[Rr]eopen/ }).waitFor();
  assert.equal(await frame.locator('#accept').isDisabled(), true);
  await p.reopen();
  await frame.locator('#flow-status').filter({ hasText: 'Uncertain' }).waitFor();
  assert.equal((await p.api('/api/demo')).providerCalls, 0, 'reopening must not dispatch');
  await frame.locator('#flow-reconcile:not(:disabled)').click();
  await frame.locator('#flow-status').filter({ hasText: 'Waiting for reviewer' }).waitFor();
  assert.equal((await p.api('/api/demo')).providerCalls, 0, 'reconciliation must not dispatch');
  await acceptGate(p, 'draft'); await acceptGate(p, 'critique'); await acceptGate(p, 'revise');
  await frame.locator('#action-status').filter({ hasText: 'pending-approval' }).waitFor(); await p.ready();
  await frame.locator('#action-confirm').check(); await frame.locator('#deny').click();
  await frame.locator('#action-status').filter({ hasText: 'Denial recorded' }).waitFor(); await p.ready();
  assert.equal(await frame.locator('#action-confirm').isChecked(), false);
  await frame.locator('#action-confirm').check(); await frame.locator('#continue').click();
  await frame.locator('#action-status').filter({ hasText: 'Denial concluded' }).waitFor();
  assert.equal((await p.api('/api/demo')).providerCalls, 2);
  assert.deepEqual(p.errors, []);
});

test('flow action inspection binds the earlier accepted document rather than the last reviewed proposal', async t => {
  const p = await flowPanel(t, text => text.replace('id: revise, agent: writer, gate: {accept: [prd, announcement]', 'id: revise, agent: reviewer, gate: {accept: [review]'));
  await startTopic(p, 'Earlier action document');
  const first = (await p.api('/api/flows')).runs.at(-1).proposal;
  await acceptGate(p, 'draft'); await acceptGate(p, 'critique'); await acceptGate(p, 'revise');
  await p.frame.locator('#action-status').filter({ hasText: 'pending-approval' }).waitFor(); await p.ready();
  const f = (await p.api('/api/flows')).runs.at(-1);
  const action = await p.api('/api/announcement?id=' + f.action);
  assert.equal(action.snapshot.proposal, first);
  assert.notEqual(await p.frame.locator('#proposal').inputValue(), first, 'selected document history must not retarget the action');
  assert.match(await p.frame.locator('#snapshot').textContent(), /Earlier action document/);
  assert.equal(await p.frame.locator('#request').isVisible(), false);
  assert.deepEqual(p.errors, []);
});

test('a document-only flow completes without claiming a simulated receipt', async t => {
  const p = await flowPanel(t, text => text.replace('        - {id: announce, action: announce}\n', ''));
  await startTopic(p, 'Document-only topic');
  const first = (await p.api('/api/flows')).runs.at(-1).proposal;
  // The flow read can already name the next proposal while the document read
  // still contains only the preceding accepted proposal. Retain the new target.
  const publicationGap = async route => {
    const response = await route.fetch(); const data = await response.json();
    if (data.proposals.some(proposal => proposal.id === first && proposal.status === 'accepted')) data.proposals = data.proposals.filter(proposal => proposal.status !== 'pending');
    await route.fulfill({ json: data });
  };
  await p.page.route(p.bridge.origin + '/api/state', publicationGap);
  try {
    await acceptGate(p, 'draft');
    await p.frame.locator('#flow-location').filter({ hasText: 'critique' }).waitFor();
    await p.frame.locator('#flow-status').filter({ hasText: 'Waiting for reviewer' }).waitFor(); await p.ready();
  } finally { await p.page.unroute(p.bridge.origin + '/api/state', publicationGap); }
  await p.frame.locator('#refresh').click(); await p.ready();
  const next = (await p.api('/api/flows')).runs.at(-1).proposal;
  assert.equal(await p.frame.locator('#proposal').inputValue(), next, 'a late proposal must not leave selection on the prior accepted gate');
  await acceptGate(p, 'critique'); await acceptGate(p, 'revise');
  await p.frame.locator('#flow-status').filter({ hasText: 'Complete' }).waitFor(); await p.ready();
  assert.match(await p.frame.locator('#flow-status').textContent(), /documents accepted/i);
  assert.equal(await p.frame.locator('#action-stage').isVisible(), false);
  assert.equal((await p.api('/api/state')).announcements?.length || 0, 0);
  assert.deepEqual(p.errors, []);
});

test('an unreceived Run retains its exact key until an explicit same-request retry', async t => {
  const p = await flowPanel(t), { page, frame } = p;
  await p.ready(); await frame.locator('#flow-open').click();
  await frame.locator('#flow-input-topic').fill('Unreceived fictional request');
  const abort = route => route.abort('failed');
  await page.route(p.bridge.origin + '/api/flow/start', abort);
  await frame.locator('#flow-start').click();
  await frame.locator('#flow-resubmit:not([hidden])').waitFor(); await p.ready();
  assert.equal((await p.api('/api/demo')).providerCalls, 0);
  assert.equal(p.posts.filter(x => x.path === '/api/flow/start').length, 1);
  // Enter/programmatic submission must not turn unknown outcome into a fresh key.
  await frame.locator('#flow-run-form').evaluate(form => form.requestSubmit());
  await p.ready();
  assert.equal(p.posts.filter(x => x.path === '/api/flow/start').length, 1);
  await page.unroute(p.bridge.origin + '/api/flow/start', abort);
  await frame.locator('#flow-resubmit').click();
  await frame.locator('#flow-status').filter({ hasText: 'Waiting for reviewer' }).waitFor(); await p.ready();
  const starts = p.posts.filter(x => x.path === '/api/flow/start');
  assert.equal(starts.length, 2); assert.deepEqual(starts[0].body, starts[1].body);
  assert.equal((await p.api('/api/demo')).providerCalls, 1);
  await frame.locator('#document-confirm:not(:disabled)').check(); await frame.locator('#reject').click();
  await frame.locator('#flow-status').filter({ hasText: 'Rejected' }).waitFor();
  assert.equal((await p.api('/api/demo')).providerCalls, 1);
  await startTopic(p, 'Explicit cancellation');
  await frame.locator('#flow-cancel:not(:disabled)').click();
  await frame.locator('#flow-status').filter({ hasText: /Cancelled|Cancellation requested/ }).waitFor();
  assert.equal((await p.api('/api/demo')).providerCalls, 2);
  assert.deepEqual(p.errors, []);
});

test('new-run flow selection previews independently of completed run progress', async t => {
  const p = await flowPanel(t, text => text.replace('  flows:\n', '  flows:\n    short:\n      inputs: {schema: schemas/run.json}\n      limits: {agentRuns: 2}\n      steps:\n        - {id: short-draft, agent: writer, gate: {accept: [prd, announcement], role: reviewer}}\n'));
  await p.ready(); await p.frame.locator('#flow-open').click();
  assert.equal(await p.frame.locator('#flow-steps > li').count(), 4);
  await p.frame.locator('#flow-definition-select').selectOption('short'); await p.ready();
  assert.equal(await p.frame.locator('#flow-steps > li').count(), 1);
  assert.equal(await p.frame.locator('#flow-budget').inputValue(), '2');
  assert.equal((await p.api('/api/demo')).providerCalls, 0);
  await p.frame.locator('#flow-definition-select').selectOption('release'); await p.ready();
  await startTopic(p, 'Completed flow before another selection');
  await acceptGate(p, 'draft'); await acceptGate(p, 'critique'); await acceptGate(p, 'revise');
  await p.frame.locator('#action-status').filter({ hasText: 'pending-approval' }).waitFor(); await p.ready();
  await p.frame.locator('#action-confirm').check(); await p.frame.locator('#approve').click();
  await p.frame.locator('#flow-status').filter({ hasText: 'Approved; not executed' }).waitFor(); await p.ready();
  await p.frame.locator('#action-confirm').check(); await p.frame.locator('#continue').click();
  await p.frame.locator('#flow-status').filter({ hasText: 'Complete' }).waitFor(); await p.ready();
  const history = await p.frame.locator('#flow-history').inputValue();
  await p.frame.locator('#flow-open').click();
  await p.frame.locator('#flow-definition-select').selectOption('short'); await p.ready();
  const preview = p.frame.getByRole('list', { name: 'New run preview: short' });
  assert.equal(await preview.locator('li').count(), 1, 'new-run preview must follow the form, not the completed historical flow');
  assert.match(await preview.textContent(), /short-draft/);
  assert.doesNotMatch(await preview.textContent(), /Completed/);
  assert.equal(await p.frame.locator('#flow-steps > li').count(), 4, 'selected run retains its own steps and progress');
  assert.equal(await p.frame.locator('#flow-steps > li span').count(), 4);
  assert.equal(await p.frame.locator('#flow-history').inputValue(), history);
  assert.equal(await p.frame.locator('#flow-budget').inputValue(), '2');
  assert.equal((await p.api('/api/demo')).providerCalls, 3);
  await startTopic(p, 'New short flow', '2');
  assert.equal(p.posts.filter(post => post.path === '/api/flow/start').at(-1).body.flow, 'short');
  assert.equal((await p.api('/api/flows')).runs.at(-1).flow, 'short');
  assert.equal((await p.api('/api/demo')).providerCalls, 4);
  assert.deepEqual(p.errors, []);
});
