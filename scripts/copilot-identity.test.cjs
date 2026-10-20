const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const fs = require('node:fs');
const vm = require('node:vm');

test('extension open status defers mode and identity claims to the inspected panel', async () => {
  const source = fs.readFileSync(path.join(__dirname, '../.github/extensions/northwind-review/extension.mjs'), 'utf8').replace(/^import .*;$/gm, '');
  let canvas;
  await vm.runInNewContext(`(async () => { ${source} })()`, {
    createCanvas: options => options,
    joinSession: async options => { canvas = options.canvases[0]; },
    CanvasError: class extends Error {},
    startBridge: async () => ({ url: 'http://127.0.0.1:1234/#fixture', close: async () => {} }),
  });
  const opened = await canvas.open({ instanceId: 'fixture' });
  assert.match(opened.status, /mode.*identity.*panel/);
  assert.doesNotMatch(opened.status, /Deterministic fixture|unauthenticated|Real model calls: 0/);
});

for (const mode of ['authenticated-sessions', 'trusted-loopback-sessions', 'hosted-model', 'offline-model']) test(`${mode} outcomes pass through HTTP adapter without sessions credentials`, async t => {
  const { startBridge } = await import(pathToFileURL(path.join(__dirname, '../.github/extensions/northwind-review/server.mjs')));
  let status = 403;
  const upstream = createServer((req, res) => {
    assert.equal(req.headers.authorization, undefined, 'adapter must not invent or forward sessions bearer tokens');
    if (req.url === '/') { res.setHeader('Set-Cookie', 'canvas-local=private-cookie; HttpOnly'); res.end(''); return; }
    if (req.url === '/api/demo') { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({ mode, boot: 'identity-boot', headlessPrefix: '/engine' })); return; }
    assert.equal(req.url, '/engine/api/announcement/decision');
    assert.equal(req.headers.cookie, 'canvas-local=private-cookie');
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ code: status === 403 ? 'PERMISSION_DENIED' : 'UNAUTHENTICATED', error: status === 403 ? "You're not an approver for this session." : 'Sign in again: sessions credentials are missing, invalid or expired.' }));
  });
  await new Promise(resolve => upstream.listen(0, '127.0.0.1', resolve));
  t.after(() => { upstream.closeAllConnections(); return new Promise(resolve => upstream.close(resolve)); });
  const bridge = await startBridge({ engineURL: `http://127.0.0.1:${upstream.address().port}` });
  t.after(() => bridge.close());
  const headers = { Origin: bridge.origin, 'Content-Type': 'application/json', 'x-canvas-review-token': new URL(bridge.url).hash.slice('#token='.length), Authorization: 'Bearer must-not-forward' };
  for (status of [403, 401]) {
    const response = await fetch(bridge.origin + '/api/announcement/decision', { method: 'POST', headers, body: JSON.stringify({ role: 'GM', approved: true }) });
    assert.equal(response.status, status);
    const result = await response.json();
    assert.equal(result.code, status === 403 ? 'PERMISSION_DENIED' : 'UNAUTHENTICATED');
    assert.match(result.error, status === 403 ? /not an approver/ : /Sign in again/);
  }
});
