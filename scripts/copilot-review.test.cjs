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
  for (const route of ['/api/edits', '/api/edit?id=known', '/api/edit/context?document=prd', '/api/flows', '/api/flow?run=known']) {
    response = await fetch(bridge.origin + route, { headers }); assert.equal(response.status, 200);
  }
  for (const name of ['start', 'changes', 'retry', 'cancel', 'reconcile', 'advance', 'verify']) {
    response = await fetch(bridge.origin + '/api/flow/' + name, { method: 'POST', headers, body: '{}' }); assert.equal(response.status, 200);
  }
  for (const name of ['request', 'reconcile', 'abandon']) {
    response = await fetch(bridge.origin + '/api/edit/' + name, { method: 'POST', headers, body: '{}' }); assert.equal(response.status, 200);
  }
  for (const name of ['resume', 'rerun']) {
    const count = observed.length;
    response = await fetch(bridge.origin + '/api/edit/' + name, { method: 'POST', headers, body: '{}' }); assert.equal(response.status, 404);
    assert.equal(observed.length, count, 'unsupported edit commands never reach the engine');
  }
  response = await fetch(bridge.origin + '/api/edit/context?document=prd&flowRun=known', { headers }); assert.equal(response.status, 400);
  response = await fetch(bridge.origin + '/api/edit/context?document=prd&path=secret', { headers }); assert.equal(response.status, 400);
  response = await fetch(bridge.origin + '/api/edit?id=a&id=b', { headers }); assert.equal(response.status, 400);
  response = await fetch(bridge.origin + '/api/edit/request', { method: 'POST', headers, body: 'x'.repeat(8193) }); assert.equal(response.status, 413);
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

test('authoring bridge binds the inspected instance and only explicit successful activation renews its boot', async t => {
  const { startBridge } = await import(moduleURL);
  let boot = 'inert', activeInstance = '', loseReply = false;
  const observed = [];
  const upstream = createServer(async (req, res) => {
    if (req.url === '/') { res.setHeader('Set-Cookie', 'canvas-local=secret; HttpOnly'); res.end('shell'); return; }
    if (req.url === '/api/demo') { res.end(JSON.stringify({ mode: activeInstance ? 'deterministic-northwind' : 'inert-authoring', realModelCalls: 0, authoring: true, headlessPrefix: '/engine', boot, activeInstance })); return; }
    observed.push({ url: req.url, instance: req.headers['x-canvas-instance'] });
    if (req.url === '/api/authoring/activate') {
      const chunks = []; for await (const chunk of req) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks));
      assert.equal(body.candidate, 'frozen'); assert.equal(body.expectedCurrent, activeInstance);
      activeInstance = activeInstance ? 'second' : 'first'; boot = activeInstance;
      if (loseReply) { req.socket.destroy(); return; }
      res.end(JSON.stringify({ instance: { id: activeInstance, package: body.candidate, previous: body.expectedCurrent } })); return;
    }
    res.end(JSON.stringify({ ok: true }));
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  t.after(() => { upstream.closeAllConnections(); return new Promise(resolve => upstream.close(resolve)); });
  const engineURL = `http://127.0.0.1:${upstream.address().port}`;
  const bridge = await startBridge({ engineURL }); t.after(() => bridge.close());
  const stale = await startBridge({ engineURL }); t.after(() => stale.close());
  const headers = b => ({ 'x-canvas-review-token': new URL(b.url).hash.slice(7), 'Content-Type': 'application/json', Origin: b.origin, 'X-Canvas-Instance': 'caller-forged' });
  const call = (route, body, b = bridge) => fetch(b.origin + route, { headers: headers(b), ...(body ? { method: 'POST', body: JSON.stringify(body) } : {}) });
  for (const route of ['/api/authoring', '/api/authoring/candidate?id=frozen', '/api/authoring/file?candidate=frozen&path=flow.yaml', '/api/authoring/instance?id=old']) assert.equal((await call(route)).status, 200);
  assert.equal((await call('/api/authoring/prepare', { definition: 'flow.yaml' })).status, 200);
  assert.equal((await call('/api/authoring/arbitrary')).status, 404);
  assert.equal((await call('/api/authoring/activate/extra', {})).status, 404);
  assert.equal((await fetch(bridge.origin + '/activation.js')).status, 200);
  const payload = { candidate: 'frozen', expectedCurrent: '', requestKey: 'same-key', confirmed: true, actor: 'local person (unverified)' };
  assert.equal((await call('/api/authoring/activate', payload)).status, 200);
  assert.equal((await call('/api/state')).status, 200);
  assert.deepEqual(observed.at(-1), { url: '/engine/api/state', instance: 'first' });
  assert.equal((await call('/api/review', {}, stale)).status, 409);
  assert.equal((await call('/api/authoring', null, stale)).status, 200, 'stale panels may inspect history, not rebind runtime');
  loseReply = true;
  assert.equal((await call('/api/authoring/activate', { ...payload, expectedCurrent: 'first', requestKey: 'next' })).status, 503);
  assert.equal((await call('/api/authoring')).status, 200);
  assert.equal((await call('/api/state')).status, 409, 'a lost committed reply must require reopen, never automatic rebind');
});

test('pending activation restart binds its durable exact request without claiming a live predecessor', async t => {
  const { startBridge } = await import(moduleURL);
  const request = { candidate: 'frozen', expectedCurrent: 'old', requestKey: 'durable-key', confirmed: true, actor: 'local person (unverified)' };
  let activeInstance = '', boot = 'pending', pendingActivation = { ...request, instance: 'new' };
  const upstream = createServer(async (req, res) => {
    if (req.url === '/') { res.setHeader('Set-Cookie', 'canvas-local=secret; HttpOnly'); res.end('shell'); return; }
    if (req.url === '/api/demo') { res.end(JSON.stringify({ mode: activeInstance ? 'deterministic-northwind' : 'inert-authoring', realModelCalls: 0, authoring: true, headlessPrefix: '/engine', boot, activeInstance, pendingActivation })); return; }
    if (req.url === '/api/authoring/activate') {
      const parts = []; for await (const chunk of req) parts.push(chunk);
      assert.deepEqual(JSON.parse(Buffer.concat(parts)), request);
      activeInstance = 'new'; boot = 'active'; pendingActivation = undefined;
      res.end(JSON.stringify({ instance: { id: 'new', package: 'frozen', previous: 'old' } })); return;
    }
    assert.equal(req.headers['x-canvas-instance'], 'new'); res.end('{}');
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  t.after(() => { upstream.closeAllConnections(); return new Promise(resolve => upstream.close(resolve)); });
  const bridge = await startBridge({ engineURL: `http://127.0.0.1:${upstream.address().port}` }); t.after(() => bridge.close());
  const headers = { 'x-canvas-review-token': new URL(bridge.url).hash.slice(7), 'Content-Type': 'application/json', Origin: bridge.origin };
  const result = await fetch(bridge.origin + '/api/authoring/activate', { method: 'POST', headers, body: JSON.stringify(request) });
  assert.equal(result.status, 200, await result.text());
  assert.equal((await fetch(bridge.origin + '/api/state', { headers })).status, 200);
});

test('adapter refuses remote or malformed engine URLs before network access', async () => {
  const { startBridge } = await import(moduleURL);
  for (const engineURL of ['http://localhost:18878', 'https://127.0.0.1:18878', 'http://127.0.0.1:18878/path', 'http://user:pass@127.0.0.1:18878', 'http://127.0.0.1:18878/?token=x']) {
    await assert.rejects(startBridge({ engineURL }), /literal loopback engine URL/);
  }
});
