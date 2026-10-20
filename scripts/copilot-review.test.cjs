const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createServer, request } = require('node:http');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const moduleURL = pathToFileURL(path.join(__dirname, '../.github/extensions/northwind-review/server.mjs'));

test('thin adapter keeps engine cookie private and forwards only reviewed routes', async t => {
  const { startBridge } = await import(moduleURL);
  const observed = [];
  let boot = 'boot1';
  const upstream = createServer((req, res) => {
    if (req.url === '/') { res.setHeader('Set-Cookie', 'canvas-local=engine-secret; HttpOnly; SameSite=Strict'); res.end('fallback'); return; }
    if (req.url === '/api/demo') { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({ mode: 'deterministic-northwind', boot, providerCalls: 0, realModelCalls: 0 })); return; }
    assert.equal(req.headers.cookie, 'canvas-local=engine-secret');
    if (req.method === 'POST') assert.equal(req.headers.origin, `http://127.0.0.1:${upstream.address().port}`);
    observed.push([req.method, req.url]);
    res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({ ok: true }));
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  t.after(() => { upstream.closeAllConnections(); return new Promise(resolve => upstream.close(resolve)); });
  const bridge = await startBridge({ engineURL: `http://127.0.0.1:${upstream.address().port}` });
  t.after(() => bridge.close());
  const token = new URL(bridge.url).hash.slice('#token='.length);
  const headers = { 'x-canvas-review-token': token, 'Content-Type': 'application/json', Origin: bridge.origin };
  let response = await fetch(bridge.origin + '/api/state'); assert.equal(response.status, 403); assert.equal((await response.json()).recovery, 'reopen');
  response = await fetch(bridge.origin + '/api/state', { headers: { ...headers, Origin: 'http://evil.invalid' } }); assert.equal(response.status, 403);
  response = await fetch(bridge.origin + '/api/state', { headers }); assert.equal(response.status, 200); assert.deepEqual(await response.json(), { ok: true }); assert.equal(response.headers.get('set-cookie'), null);
  response = await fetch(bridge.origin + '/api/role', { method: 'POST', headers, body: '{"role":"owner"}' }); assert.equal(response.status, 404);
  response = await fetch(bridge.origin + '/api/announcement/decision', { method: 'POST', headers, body: '{"approved":false}' }); assert.equal(response.status, 200);
  assert.deepEqual(observed, [['GET', '/api/state'], ['POST', '/api/announcement/decision']]);
  for (const route of ['/api/flows', '/api/flow?run=known']) {
    response = await fetch(bridge.origin + route, { headers }); assert.equal(response.status, 200);
  }
  for (const name of ['start', 'changes', 'retry', 'cancel', 'reconcile', 'advance', 'verify']) {
    response = await fetch(bridge.origin + '/api/flow/' + name, { method: 'POST', headers, body: '{}' }); assert.equal(response.status, 200);
  }
  response = await fetch(bridge.origin + '/api/flow/unknown', { method: 'POST', headers, body: '{}' }); assert.equal(response.status, 404);
  const largeStart = JSON.stringify({ flow: 'release', requestKey: 'bounded', role: 'GM', inputs: { topic: 'x'.repeat(20000) }, agentRuns: 12 });
  response = await fetch(bridge.origin + '/api/flow/start', { method: 'POST', headers, body: largeStart }); assert.equal(response.status, 200, 'Start needs more than the old decision envelope');
  response = await fetch(bridge.origin + '/api/flow/start', { method: 'POST', headers, body: 'x'.repeat(72 * 1024 + 1) }); assert.equal(response.status, 413);
  response = await fetch(bridge.origin + '/api/flow/changes', { method: 'POST', headers, body: 'x'.repeat(16385) }); assert.equal(response.status, 413, 'other flow commands keep the old bridge bound');
  response = await fetch(bridge.origin + '/api/review', { method: 'POST', headers: { 'x-canvas-review-token': token, 'Content-Type': 'application/json' }, body: '{}' }); assert.equal(response.status, 403);
  response = await fetch(bridge.origin + '/api/state', { headers: { ...headers, 'Sec-Fetch-Site': 'cross-site' } }); assert.equal(response.status, 403);
  const wrongHost = await new Promise((resolve, reject) => {
    const req = request(bridge.origin + '/api/state', { headers: { ...headers, Host: 'evil.invalid' } }, res => { res.resume(); resolve(res.statusCode); });
    req.once('error', reject); req.end();
  });
  assert.equal(wrongHost, 403);
  response = await fetch(bridge.origin + '/api/review', { method: 'POST', headers, body: 'x'.repeat(16385) }); assert.equal(response.status, 413);
  response = await fetch(bridge.origin + '/api/review', { method: 'POST', headers: { ...headers, 'Content-Type': 'text/plain' }, body: '{}' }); assert.equal(response.status, 415);
  response = await fetch(bridge.origin + '/'); assert.equal(response.status, 200);
  assert(!response.headers.get('content-security-policy').includes("frame-ancestors 'none'"));
  assert(!(await response.text()).includes('engine-secret'));
  boot = 'boot2';
  const observedBeforeRestart = observed.length;
  for (const method of ['GET', 'POST']) {
    response = await fetch(bridge.origin + (method === 'GET' ? '/api/state' : '/api/review'), { method, headers, ...(method === 'POST' ? { body: '{}' } : {}) });
    assert.equal(response.status, 409);
    const refusal = await response.json();
    assert.match(refusal.error, /Reopen the canvas/); assert.equal(refusal.recovery, 'reopen');
  }
  assert.equal(observed.length, observedBeforeRestart, 'a stale bridge must not forward reads or actions');
  const reopened = await startBridge({ engineURL: `http://127.0.0.1:${upstream.address().port}` });
  t.after(() => reopened.close());
  response = await fetch(reopened.origin + '/api/state', { headers: { 'x-canvas-review-token': new URL(reopened.url).hash.slice('#token='.length) } });
  assert.equal(response.status, 200);
});

test('adapter refuses remote or malformed engine URLs before network access', async () => {
  const { startBridge } = await import(moduleURL);
  for (const engineURL of ['http://localhost:18878', 'https://127.0.0.1:18878', 'http://127.0.0.1:18878/path', 'http://user:pass@127.0.0.1:18878', 'http://127.0.0.1:18878/?token=x']) {
    await assert.rejects(startBridge({ engineURL }), /literal loopback engine URL/);
  }
});
