const assert = require('node:assert/strict');
const test = require('node:test');
const net = require('node:net');
const { startFixture } = require('./browser-smoke.cjs');

// Use the running demo if present; otherwise reserve its Sessions port ourselves.
async function occupyDemoPort() {
  const server = net.createServer(socket => socket.end());
  return new Promise((resolve, reject) => {
    server.once('error', error => error.code === 'EADDRINUSE' ? resolve(null) : reject(error));
    server.listen(8766, '127.0.0.1', () => resolve(server));
  });
}

async function connect(port) {
  return new Promise((resolve, reject) => {
    const socket = net.connect({ host: '127.0.0.1', port });
    socket.once('error', reject);
    socket.once('connect', () => { socket.destroy(); resolve(); });
  });
}

test('real browser fixture starts and stops without taking the running demo port', { timeout: 70000 }, async t => {
  const foreign = await occupyDemoPort();
  if (foreign) t.after(() => new Promise(resolve => foreign.close(resolve)));
  await connect(8766);
  let fixture;
  t.after(() => fixture?.stop());
  fixture = await startFixture({ signal: t.signal });
  const port = Number(new URL(fixture.url).port);
  assert.notEqual(port, 8766);
  await connect(port);
  // Catch a legacy/empty fixture before any browser selector can time out.
  const response = await fetch(`${fixture.url}/api/state`);
  assert.equal(response.status, 200);
  const state = await response.json();
  assert.equal(state.definition?.id, 'northwind-prd', 'fixture must load the native generic definition');
  assert.deepEqual(state.agents.map(agent => agent.id).sort(), ['reviewer', 'writer']);
  assert.deepEqual([...state.documents].sort(), ['announcement.md', 'brief.json', 'feedback/initial.json', 'prd.json', 'review.json']);
  assert.equal(state.runs.length, 0, 'startup must not invoke the deterministic provider');
  assert.equal(state.proposals.length, 0);
  assert.equal(response.headers.get('X-Canvas-Provider'), 'deterministic-fixture');
  // Exercise the real Git/gRPC fixture without depending on a browser or the UI.
  const post = async (route, body) => {
    const result = await fetch(`${fixture.url}${route}`, {
      method: 'POST', headers: { 'Content-Type': 'application/json', Origin: fixture.url, Cookie: response.headers.get('set-cookie').split(';')[0] },
      body: JSON.stringify(body), signal: t.signal,
    });
    assert.equal(result.ok, true, await result.clone().text());
    return result.json();
  };
  const readState = async () => (await fetch(`${fixture.url}/api/state`, { signal: t.signal })).json();
  const waitForState = async predicate => {
    const deadline = Date.now() + 10000;
    while (Date.now() < deadline) {
      const current = await readState();
      if (predicate(current)) return current;
    }
    assert.fail('native fixture did not publish the expected recorded state');
  };
  let previous;
  for (let attempt = 1; attempt <= 2; attempt++) {
    const { run: id } = await post('/api/run', { agent: 'writer', request: 'Draft a fictional PRD and announcement.', role: 'GM' });
    const current = await waitForState(current => current.runs.some(run => run.id === id && !['queued', 'running'].includes(run.status)));
    const run = current.runs.find(run => run.id === id);
    assert.equal(run.status, 'proposed', JSON.stringify(run));
    const proposal = current.proposals.find(proposal => proposal.id === run.proposal);
    assert.deepEqual(Object.keys(proposal.changes).sort(), ['announcement.md', 'prd.json']);
    assert.equal(JSON.parse(proposal.changes['prd.json']).title, `Proposed fictional pilot PRD ${attempt}`);
    assert.match(proposal.changes['announcement.md'], new RegExp(`^# Fictional announcement draft ${attempt}\\n`));
    if (previous) assert.notDeepEqual(proposal.changes, previous);
    previous = proposal.changes;
    for (const [path, content] of Object.entries(proposal.changes)) {
      const document = await (await fetch(`${fixture.url}/api/document?path=${encodeURIComponent(path)}&proposal=${encodeURIComponent(proposal.id)}`)).json();
      assert.equal(document.proposed, content, 'whole-scope replacement is readable through the real API');
    }
    await post('/api/ask', { run: id, request: 'Explain the recorded proposal.', role: 'owner' });
    const answered = await waitForState(current => current.conversation.some(entry => entry.kind === 'question' && entry.run === id && !['queued', 'running'].includes(entry.status)));
    const question = answered.conversation.find(entry => entry.kind === 'question' && entry.run === id);
    assert.equal(question.status, 'answered');
    assert.equal(question.session, run.session);
    assert.match(question.answer, /fictional documents/);
    assert.equal(answered.head, current.head, 'question fallback stays read-only');
    assert.deepEqual(answered.proposals, current.proposals);
  }
  await post('/api/input', { path: 'feedback/browser.json', content: '{"team":"Northwind","feedback":"Fictional browser observation."}', role: 'GM' });
  const watched = await waitForState(current => current.runs.some(run => run.trigger === 'new-feedback' && !['queued', 'running'].includes(run.status)));
  assert.equal(watched.runs.find(run => run.trigger === 'new-feedback').status, 'proposed', 'explicit Watch handles declared source input');
  await fixture.stop();
  await assert.rejects(connect(port), { code: 'ECONNREFUSED' });
  await connect(8766);
});
