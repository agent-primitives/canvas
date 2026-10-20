import { createServer } from 'node:http';
import { Agent, request } from 'node:https';
import { isAbsolute } from 'node:path';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import { open, readFile } from 'node:fs/promises';

const routes = {
  GET: new Set(['/api/state', '/api/document', '/api/announcement', '/api/demo', '/api/forks']),
  POST: new Set(['/api/run', '/api/review', '/api/verify', '/api/announcement/request', '/api/announcement/decision', '/api/announcement/continue', '/api/announcement/verify']),
};
const assets = { '/': ['index.html', 'text/html'], '/app.js': ['app.js', 'text/javascript'], '/style.css': ['style.css', 'text/css'] };
const security = {
  'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer', 'X-Content-Type-Options': 'nosniff',
  'Content-Security-Policy': "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'",
};

async function connectionOptions(options) {
  const explicit = Object.hasOwn(options, 'engineURL') || Object.hasOwn(options, 'caFile');
  const configFile = process.env.CANVAS_ENGINE_CONFIG;
  if (configFile && explicit) throw new Error('Engine config conflicts with explicit connection options');
  if (configFile) {
    const file = await open(configFile, 'r');
    let bytes;
    try {
      if (!(await file.stat()).isFile()) throw new Error('Engine config must be a regular JSON file');
      const buffer = Buffer.alloc(65537);
      let size = 0, count;
      while (size < buffer.length && (count = (await file.read(buffer, size, buffer.length - size, null)).bytesRead)) size += count;
      if (size > 65536) throw new Error('Engine config exceeds 64 KiB');
      bytes = buffer.subarray(0, size).toString('utf8');
    } finally { await file.close(); }
    const data = JSON.parse(bytes);
    // JSON.parse accepts duplicate members; reject those as ambiguous operator input.
    const tokens = bytes.match(/"(?:\\.|[^"\\])*"|[{}[\]:,]|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null/g) || [];
    const keys = tokens.filter((token, i) => tokens[i + 1] === ':').map(token => JSON.parse(token));
    if (!data || Array.isArray(data) || typeof data !== 'object' || keys.length !== 3 || new Set(keys).size !== 3 || keys.some(key => !['version', 'engineURL', 'caFile'].includes(key)) || data.version !== 1) throw new Error('Engine config requires only version 1, engineURL and caFile');
    options = data;
  }
  const engineURL = Object.hasOwn(options, 'engineURL') ? options.engineURL : 'http://127.0.0.1:18878';
  const match = typeof engineURL === 'string' && /^(https?):\/\/127\.0\.0\.1:([1-9]\d{0,4})\/?$/.exec(engineURL);
  if (!match || Number(match[2]) > 65535) throw new Error('A literal loopback engine URL is required');
  const secure = match[1] === 'https';
  if (secure ? typeof options.caFile !== 'string' || !isAbsolute(options.caFile) : Object.hasOwn(options, 'caFile')) throw new Error('A literal loopback engine URL over HTTPS requires an absolute CA file; HTTP forbids CA configuration');
  return { engine: new URL(engineURL), ca: secure ? await readFile(options.caFile) : undefined };
}

// Deliberately separate from globalAgent/fetch: only the selected CA and direct TLS
// apply, even when Node was started with extra roots, TLS-disable or env proxies.
function tlsUpstream(url, ca, options) {
  const agent = new Agent({ ca, rejectUnauthorized: true, minVersion: 'TLSv1.2', proxyEnv: {}, keepAlive: false });
  return new Promise((resolve, reject) => {
    const req = request(url, { ...options, agent, ca, rejectUnauthorized: true, signal: AbortSignal.timeout(10000) }, res => {
      res.on('error', reject);
      res.once('close', () => agent.destroy());
      if (res.statusCode >= 300 && res.statusCode < 400) { res.destroy(new Error('Engine redirects are not supported')); return; }
      if (url.pathname !== '/' && !/^application\/json(?:\s*;|$)/i.test(res.headers['content-type'] || '')) { res.destroy(new Error('Engine JSON response required')); return; }
      const parts = []; let size = 0;
      res.on('data', chunk => {
        size += chunk.length;
        if (size > 4 * 1024 * 1024) res.destroy(new Error('Engine view exceeds local bound'));
        else parts.push(chunk);
      });
      res.on('end', () => {
        const bytes = Buffer.concat(parts);
        resolve({ ok: res.statusCode >= 200 && res.statusCode < 300, status: res.statusCode,
          headers: { getSetCookie: () => res.headers['set-cookie'] || [], get: name => res.headers[name.toLowerCase()] },
          body: [bytes], json: async () => JSON.parse(bytes.toString('utf8')) });
      });
    });
    req.once('error', error => { agent.destroy(); reject(error); });
    req.end(options.body);
  });
}

// The capability protects this local prototype's iframe transport, not human authority.
export async function startBridge(options = {}) {
  const { engine, ca } = await connectionOptions(options);
  const upstream = (path, options = {}) => ca !== undefined
    ? tlsUpstream(new URL(path, engine), ca, options)
    : fetch(new URL(path, engine), { ...options, redirect: 'error', signal: AbortSignal.timeout(10000) });
  async function demoInfo() {
    const response = await upstream('/api/demo');
    if (!response.ok) throw new Error('Start the configured Canvas review engine first');
    const data = await response.json();
    if (!['deterministic-northwind', 'authenticated-sessions', 'trusted-loopback-sessions', 'hosted-sessions', 'hosted-model', 'offline-model'].includes(data.mode) || (data.mode === 'deterministic-northwind' && data.realModelCalls !== 0) || typeof data.boot !== 'string' || !data.boot || data.boot.length > 128) throw new Error('Supported Canvas review engine required');
    return data;
  }
  const initialDemo = await demoInfo();
  const boot = initialDemo.boot;
  const headlessPrefix = initialDemo.headlessPrefix || '';
  if (!['', '/engine'].includes(headlessPrefix) || (initialDemo.mode === 'hosted-sessions' && headlessPrefix !== '/engine')) throw new Error('Unsupported engine API prefix');
  const bootstrap = await upstream('/');
  const cookie = bootstrap.headers.getSetCookie().find(value => value.startsWith('canvas-local='))?.split(';')[0];
  await bootstrap.body?.cancel?.();
  if (!bootstrap.ok || !cookie) throw new Error('Engine browser boundary unavailable');
  const token = randomBytes(32).toString('hex');
  let origin;
  const reply = (res, code, value) => { res.writeHead(code, { ...security, 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); };
  async function handle(req, res) {
    if (req.headers.host !== new URL(origin).host) return reply(res, 403, { error: 'Local host required' });
    const url = new URL(req.url, origin);
    if (req.method === 'GET' && Object.hasOwn(assets, url.pathname)) {
      const [file, type] = assets[url.pathname];
      const body = await readFile(new URL(file, import.meta.url));
      res.writeHead(200, { ...security, 'Content-Type': `${type}; charset=utf-8` }); res.end(body); return;
    }
    const supplied = req.headers['x-canvas-review-token'];
    if (typeof supplied !== 'string' || !/^[a-f0-9]{64}$/.test(supplied) || !timingSafeEqual(Buffer.from(token), Buffer.from(supplied)) || (req.headers.origin && req.headers.origin !== origin) || (req.method === 'POST' && req.headers.origin !== origin) || req.headers['sec-fetch-site'] === 'cross-site') {
      return reply(res, 403, { error: 'Fresh same-origin canvas capability required. Reopen the canvas.', recovery: 'reopen' });
    }
    if (!routes[req.method]?.has(url.pathname)) return reply(res, 404, { error: 'Unsupported review route' });
    let body;
    if (req.method === 'POST') {
      if (req.headers['content-type'] !== 'application/json') return reply(res, 415, { error: 'JSON required' });
      const parts = []; let size = 0;
      for await (const chunk of req) { size += chunk.length; if (size > 16384) return reply(res, 413, { error: 'Action too large' }); parts.push(chunk); }
      body = Buffer.concat(parts);
    }
    if ((await demoInfo()).boot !== boot) return reply(res, 409, { error: 'Engine restarted. Reopen the canvas; nothing was retried.', recovery: 'reopen' });
    if (initialDemo.mode === 'hosted-sessions' && req.method === 'POST' && (url.pathname === '/api/verify' || url.pathname.startsWith('/api/announcement/'))) return reply(res, 409, { error: 'Hosted announcements and replay are unavailable. Identity not exercised; nothing was executed.' });
    // Engine credentials never enter the iframe; engine CSRF checks remain unchanged.
    const upstreamPath = (url.pathname === '/api/demo' ? '' : headlessPrefix) + url.pathname + url.search;
    const response = await upstream(upstreamPath, { method: req.method, body, headers: { Cookie: cookie, Origin: engine.origin, 'Content-Type': 'application/json' } });
    const parts = []; let size = 0;
    for await (const chunk of response.body) { size += chunk.length; if (size > 4 * 1024 * 1024) throw new Error('Engine view exceeds local bound'); parts.push(chunk); }
    // Only JSON/status cross the boundary. No upstream cookies, redirects or headers.
    const result = JSON.parse(Buffer.concat(parts).toString('utf8'));
    reply(res, response.status, result);
  }
  const server = createServer((req, res) => { handle(req, res).catch(() => { if (!res.headersSent) reply(res, 503, { error: 'Engine unavailable or view invalid. Refresh before another action; nothing was retried.', recovery: 'refresh' }); else res.destroy(); }); });
  server.requestTimeout = 15000; server.headersTimeout = 10000;
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  origin = `http://127.0.0.1:${server.address().port}`;
  return { origin, url: `${origin}/#token=${token}`, close: () => new Promise((resolve, reject) => { server.closeAllConnections(); server.close(error => error ? reject(error) : resolve()); }) };
}
