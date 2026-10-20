const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync, spawn } = require('node:child_process');
const http = require('node:http');
const https = require('node:https');
const { pathToFileURL } = require('node:url');

const moduleURL = pathToFileURL(path.join(__dirname, '../.github/extensions/northwind-review/server.mjs')).href;
let directory, trusted, unrelated, wrongName;
function certificate(name, san) {
  const key = path.join(directory, `${name}.key`), cert = path.join(directory, `${name}.crt`);
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=adapter-test', '-addext', `subjectAltName=${san}`, '-addext', 'basicConstraints=critical,CA:TRUE'], { stdio: 'pipe' });
  return { key: fs.readFileSync(key), cert: fs.readFileSync(cert), caFile: cert };
}
before(() => {
  process.umask(0o022);
  try { execFileSync('openssl', ['version'], { stdio: 'pipe' }); } catch { throw new Error('OpenSSL CLI is required to generate temporary TLS test keys'); }
  directory = fs.mkdtempSync(path.join(os.tmpdir(), 'canvas-adapter-tls-'));
  trusted = certificate('trusted', 'IP:127.0.0.1');
  unrelated = certificate('unrelated', 'IP:127.0.0.1');
  wrongName = certificate('wrong-name', 'DNS:localhost');
});
after(() => { if (directory) fs.rmSync(directory, { recursive: true, force: true }); });
async function listen(t, server) {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)); });
  return `${server instanceof https.Server ? 'https' : 'http'}://127.0.0.1:${server.address().port}`;
}
async function fakeEngine(t, credentials = trusted) {
  let boot = 'boot-a', response = { ok: 'actual hosted response' }, status = 200, contentType = 'application/json', stalled = false;
  const seen = [];
  const server = https.createServer(credentials, (req, res) => {
    seen.push([req.method, req.url]);
    assert.equal(req.headers.authorization, undefined);
    assert.equal(req.headers['x-canvas-review-token'], undefined);
    if (req.url === '/') { res.setHeader('Set-Cookie', 'canvas-local=tls-secret; HttpOnly; Secure; SameSite=Strict'); res.end('bootstrap'); return; }
    res.setHeader('Content-Type', 'application/json');
    if (req.url === '/api/demo') { res.end(JSON.stringify({ mode: 'hosted-sessions', boot, headlessPrefix: '/engine', identity: 'not exercised', modelCalls: null, executorCalls: null })); return; }
    assert.match(req.url, /^\/engine\/api\//);
    assert.equal(req.headers.cookie, 'canvas-local=tls-secret');
    assert.equal(req.headers.origin, engineURL);
    if (stalled) { res.writeHead(200, { 'Content-Type': 'application/json' }); res.flushHeaders(); return; }
    res.setHeader('Content-Type', contentType);
    res.setHeader('Set-Cookie', 'must-not-leak=secret');
    res.writeHead(status, status === 302 ? { Location: engineURL + '/redirect-target' } : {});
    res.end(typeof response === 'string' ? response : JSON.stringify(response));
  });
  const engineURL = await listen(t, server);
  return { engineURL, seen, restart: () => { boot = 'boot-b'; }, stall: () => { stalled = true; }, respond: (value, code = 200, type = 'application/json') => { response = value; status = code; contentType = type; } };
}
function headers(bridge) { return { Origin: bridge.origin, 'Content-Type': 'application/json', 'x-canvas-review-token': new URL(bridge.url).hash.slice(7), Authorization: 'Bearer never-forward' }; }

test('actual TLS hosted bridge preserves capability, Host, CSRF, bounds, boot and prefix isolation', async t => {
  const { startBridge } = await import(moduleURL);
  const engine = await fakeEngine(t);
  const bridge = await startBridge({ engineURL: engine.engineURL, caFile: trusted.caFile });
  t.after(() => bridge.close());
  const h = headers(bridge), call = (route, options = {}) => fetch(bridge.origin + route, { headers: h, ...options });
  let res = await call('/api/state');
  assert.equal(res.status, 200); assert.deepEqual(await res.json(), { ok: 'actual hosted response' }); assert.equal(res.headers.get('set-cookie'), null);
  res = await call('/api/demo');
  assert.deepEqual(await res.json(), { mode: 'hosted-sessions', boot: 'boot-a', headlessPrefix: '/engine', identity: 'not exercised', modelCalls: null, executorCalls: null });
  res = await call('/api/review', { method: 'POST', body: '{"action":"accept"}' }); assert.equal(res.status, 200); assert.deepEqual(await res.json(), { ok: 'actual hosted response' });
  for (const route of ['/api/verify', '/api/announcement/request', '/api/announcement/decision', '/api/announcement/continue', '/api/announcement/verify']) {
    res = await call(route, { method: 'POST', body: '{}' }); assert.equal(res.status, 409); assert.match((await res.json()).error, /unavailable|not supported/i);
  }
  for (const route of ['/resume', '/ask', '/input', '/api/resume', '/api/ask', '/api/input']) { res = await call(route, { method: 'POST', body: '{}' }); assert.equal(res.status, 404); }
  for (const badHeaders of [{}, { ...h, Origin: 'http://evil.invalid' }, { ...h, 'Sec-Fetch-Site': 'cross-site' }]) {
    res = await call('/api/state', { headers: badHeaders }); assert.equal(res.status, 403);
  }
  const wrongHost = await new Promise((resolve, reject) => {
    const req = http.request(bridge.origin + '/api/state', { headers: { ...h, Host: 'evil.invalid' } }, response => { response.resume(); resolve(response.statusCode); });
    req.once('error', reject); req.end();
  });
  assert.equal(wrongHost, 403);
  res = await call('/api/review', { method: 'POST', headers: { ...h, Origin: '' }, body: '{}' }); assert.equal(res.status, 403);
  res = await call('/api/review', { method: 'POST', headers: { ...h, 'Content-Type': 'text/plain' }, body: '{}' }); assert.equal(res.status, 415);
  res = await call('/api/review', { method: 'POST', body: 'x'.repeat(16385) }); assert.equal(res.status, 413);
  engine.respond({ large: 'x'.repeat(4 * 1024 * 1024 - 12) }); res = await call('/api/state'); assert.equal(res.status, 200); assert.equal((await res.json()).large.length, 4 * 1024 * 1024 - 12);
  engine.respond({ large: 'x'.repeat(4 * 1024 * 1024) }); res = await call('/api/state'); assert.equal(res.status, 503);
  engine.respond({ wrong: 'MIME' }, 200, 'text/plain'); res = await call('/api/state'); assert.equal(res.status, 503);
  engine.respond('not JSON'); res = await call('/api/state'); assert.equal(res.status, 503);
  engine.respond({ redirect: true }, 302); const before = engine.seen.length;
  res = await call('/api/state'); assert.equal(res.status, 503); assert.equal(engine.seen.length, before + 2, 'one demo and one action; no redirect/retry');
  engine.restart(); const count = engine.seen.length;
  for (const [route, options] of [['/api/state', {}], ['/api/review', { method: 'POST', body: '{}' }]]) { res = await call(route, options); assert.equal(res.status, 409); assert.equal((await res.json()).recovery, 'reopen'); }
  assert.deepEqual(engine.seen.slice(count), [['GET', '/api/demo'], ['GET', '/api/demo']]);
  assert(engine.seen.every(([, route]) => route === '/' || route === '/api/demo' || route.startsWith('/engine/api/')));
  assert(!engine.seen.some(([, route]) => route.includes('announcement/') || route.includes('verify')));
  const page = await (await call('/')).text(); assert(!page.includes('tls-secret')); assert(!page.includes(trusted.caFile));
});

test('actual TLS response timeout is bounded and never retried', async t => {
  const { startBridge } = await import(moduleURL);
  const engine = await fakeEngine(t);
  const bridge = await startBridge({ engineURL: engine.engineURL, caFile: trusted.caFile }); t.after(() => bridge.close());
  engine.stall();
  const count = engine.seen.length, started = Date.now();
  const response = await fetch(bridge.origin + '/api/state', { headers: headers(bridge) });
  assert.equal(response.status, 503); assert.equal((await response.json()).recovery, 'refresh');
  assert(Date.now() - started < 14000, 'upstream deadline must expire before the bridge deadline');
  assert.deepEqual(engine.seen.slice(count), [['GET', '/api/demo'], ['GET', '/engine/api/state']]);
});

test('TLS rejects unrelated roots, wrong hostname, and plaintext mismatch', async t => {
  const { startBridge } = await import(moduleURL);
  const engine = await fakeEngine(t);
  await assert.rejects(startBridge({ engineURL: engine.engineURL, caFile: unrelated.caFile }));
  const wrong = await fakeEngine(t, wrongName);
  await assert.rejects(startBridge({ engineURL: wrong.engineURL, caFile: wrongName.caFile }), /IP|hostname|altname/i);
  const plain = await listen(t, http.createServer((req, res) => res.end('{}')));
  await assert.rejects(startBridge({ engineURL: plain.replace('http:', 'https:'), caFile: trusted.caFile }));
});

test('closed config and explicit options reject unsafe/mixed selections without fallback; defaults remain HTTP', async t => {
  const { startBridge } = await import(moduleURL);
  const engine = await fakeEngine(t);
  // Keep a usable default engine available: invalid config must refuse, not
  // accidentally pass a negative test because a fallback socket is absent.
  let defaultRequests = 0;
  const server = http.createServer((req, res) => {
    defaultRequests++;
    if (req.url === '/') { res.setHeader('Set-Cookie', 'canvas-local=default'); res.end(''); return; }
    res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({ mode: 'deterministic-northwind', boot: 'default', realModelCalls: 0 }));
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(18878, '127.0.0.1', resolve); });
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)); });
  for (const options of [
    { engineURL: engine.engineURL }, { caFile: trusted.caFile }, { engineURL: 'http://127.0.0.1:18878', caFile: trusted.caFile },
    { engineURL: engine.engineURL, caFile: 'relative.crt' }, { engineURL: engine.engineURL, caFile: null },
    ...['https://localhost:8443', 'https://127.1:8443', 'https://2130706433:8443', 'https://127.0.0.1:0', 'https://127.0.0.1:65536', 'https://127.0.0.1:8443/path', 'https://x@127.0.0.1:8443', 'https://127.0.0.1:8443?x', 'https://127.0.0.1:8443#x'].map(engineURL => ({ engineURL, caFile: trusted.caFile })),
  ]) await assert.rejects(startBridge(options));
  const config = path.join(directory, 'engine.json'), saved = process.env.CANVAS_ENGINE_CONFIG;
  t.after(() => { if (saved === undefined) delete process.env.CANVAS_ENGINE_CONFIG; else process.env.CANVAS_ENGINE_CONFIG = saved; });
  process.env.CANVAS_ENGINE_CONFIG = config;
  const valid = { version: 1, engineURL: engine.engineURL, caFile: trusted.caFile };
  for (const value of ['null', '[]', '{}', '{bad', JSON.stringify({ ...valid, version: 2 }), JSON.stringify({ ...valid, unknown: true }), JSON.stringify({ ...valid, caFile: null }), JSON.stringify({ ...valid, engineURL: 'http://127.0.0.1:18878' }), JSON.stringify({ ...valid, version: '1' }), JSON.stringify(valid).replace('{', '{"version":1,'), ' '.repeat(65537)]) {
    fs.writeFileSync(config, value); await assert.rejects(startBridge());
  }
  fs.writeFileSync(config, JSON.stringify(valid));
  await assert.rejects(startBridge({ engineURL: engine.engineURL, caFile: trusted.caFile }), /conflict|explicit|mixed/i);
  const bridge = await startBridge(); t.after(() => bridge.close());
  assert.deepEqual(await (await fetch(bridge.origin + '/api/state', { headers: headers(bridge) })).json(), { ok: 'actual hosted response' });
  fs.unlinkSync(config); await assert.rejects(startBridge());
  process.env.CANVAS_ENGINE_CONFIG = directory; await assert.rejects(startBridge());
  delete process.env.CANVAS_ENGINE_CONFIG;
  assert.equal(defaultRequests, 0, 'invalid/mixed config must never fall back to the available default engine');
  // Inspect the default without any connection options.
  const defaultBridge = await startBridge(); t.after(() => defaultBridge.close());
  assert.equal((await (await fetch(defaultBridge.origin + '/api/demo', { headers: headers(defaultBridge) })).json()).mode, 'deterministic-northwind');
});

function subprocess(env, options, succeeds) {
  const source = `const {startBridge}=await import(${JSON.stringify(moduleURL)}); try { const b=await startBridge(${JSON.stringify(options)}); await b.close(); if (!${succeeds}) process.exit(21); } catch(e) { if (${succeeds}) { console.error(e); process.exit(22); } }`;
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ['--input-type=module', '-e', source], { env: { ...process.env, ...env, CANVAS_ENGINE_CONFIG: '' }, stdio: ['ignore', 'pipe', 'pipe'] });
    let output = ''; child.stderr.on('data', b => { output += b; }); child.on('error', reject);
    child.on('exit', code => code === 0 ? resolve() : reject(new Error(`TLS subprocess exit ${code}: ${output}`)));
  });
}
test('pre-import TLS disable, extra roots and env proxy cannot override explicit trust/direct connection', async t => {
  const engine = await fakeEngine(t);
  let proxyCalls = 0;
  const proxy = await listen(t, http.createServer((req, res) => { proxyCalls++; res.writeHead(502); res.end(); }).on('connect', (req, socket) => { proxyCalls++; socket.destroy(); }));
  for (const env of [{ NODE_TLS_REJECT_UNAUTHORIZED: '0' }, { NODE_EXTRA_CA_CERTS: trusted.caFile }, { NODE_USE_ENV_PROXY: '1', HTTPS_PROXY: proxy, HTTP_PROXY: proxy, ALL_PROXY: proxy, NO_PROXY: '' }, { NODE_TLS_REJECT_UNAUTHORIZED: '0', NODE_EXTRA_CA_CERTS: trusted.caFile, NODE_USE_ENV_PROXY: '1', HTTPS_PROXY: proxy, NO_PROXY: '' }]) {
    await subprocess(env, { engineURL: engine.engineURL, caFile: unrelated.caFile }, false);
    await subprocess(env, { engineURL: engine.engineURL, caFile: trusted.caFile }, true);
  }
  assert.equal(proxyCalls, 0);
});
