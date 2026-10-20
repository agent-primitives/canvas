import { createServer } from 'node:http';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import { readFile } from 'node:fs/promises';

const routes = {
  GET: new Set(['/api/state', '/api/document', '/api/announcement', '/api/demo', '/api/forks']),
  POST: new Set(['/api/run', '/api/review', '/api/verify', '/api/announcement/request', '/api/announcement/decision', '/api/announcement/continue', '/api/announcement/verify']),
};
const assets = { '/': ['index.html', 'text/html'], '/app.js': ['app.js', 'text/javascript'], '/style.css': ['style.css', 'text/css'] };
const security = {
  'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer', 'X-Content-Type-Options': 'nosniff',
  'Content-Security-Policy': "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'",
};

// The capability protects this local prototype's iframe transport, not human authority.
export async function startBridge({ engineURL = 'http://127.0.0.1:18878' } = {}) {
  const engine = new URL(engineURL);
  if (engine.protocol !== 'http:' || engine.hostname !== '127.0.0.1' || !engine.port || engine.username || engine.password || engine.pathname !== '/' || engine.search || engine.hash) {
    throw new Error('A literal loopback engine URL is required');
  }
  const upstream = (path, options = {}) => fetch(new URL(path, engine), { ...options, redirect: 'error', signal: AbortSignal.timeout(10000) });
  async function demoInfo() {
    const response = await upstream('/api/demo');
    if (!response.ok) throw new Error('Start the Canvas review engine first');
    const data = await response.json();
    if (!['deterministic-northwind', 'authenticated-sessions', 'trusted-loopback-sessions', 'hosted-model', 'offline-model'].includes(data.mode) || (data.mode === 'deterministic-northwind' && data.realModelCalls !== 0) || typeof data.boot !== 'string' || !data.boot || data.boot.length > 128) throw new Error('Supported Canvas review engine required');
    return data;
  }
  const initialDemo = await demoInfo();
  const boot = initialDemo.boot;
  const headlessPrefix = initialDemo.headlessPrefix || '';
  if (!['', '/engine'].includes(headlessPrefix)) throw new Error('Unsupported engine API prefix');
  const bootstrap = await upstream('/');
  const cookie = bootstrap.headers.getSetCookie().find(value => value.startsWith('canvas-local='))?.split(';')[0];
  await bootstrap.body?.cancel();
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
