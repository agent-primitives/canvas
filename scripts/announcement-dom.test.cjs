'use strict';
// Actual Chromium DOM against deterministic HTTP boundaries, never a provider.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || path.join(execFileSync('npm', ['root', '-g'], { encoding: 'utf8' }).trim(), 'playwright'));
const root = path.join(__dirname, '..');
const snapshot = id => ({ operation: id, definition: 'd'.repeat(64), action: 'announce', document: 'announcement', path: 'docs/announcement.md', proposal: 'accepted', revision: 'a'.repeat(40), digest: 'b'.repeat(64), content: '<img src=x onerror=alert(1)>\nExact older text\n' });
function inspected(id = 'op-one', newer = true) {
  const s = snapshot(id);
  return { id, session: 'native-session', status: 'pending-approval', concluded: false, snapshot: s, warning: newer ? 'A newer version exists; approving publishes this older version' : '', newer, currentRevision: (newer ? 'c' : 'a').repeat(40), confirmation: { confirmed: false, session: 'native-session', executionID: 'execution-one', toolCallID: 'call-one', requestSeq: 4, head: 5, revision: s.revision, digest: s.digest, currentRevision: (newer ? 'c' : 'a').repeat(40), newer } };
}
async function fixture(t, { unsupported = false, noActions = false } = {}) {
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: { width: 1000, height: 900 } });
  const state = { role: 'GM', head: 'accepted-head', agents: [], people: [{ id: 'GM', name: 'Morgan' }, { id: 'coach', name: 'Riley' }], documents: ['docs/announcement.md'], runs: [], history: [], triggers: [], prospects: [], errors: [], conversation: [], proposals: [{ id: 'accepted', status: 'accepted', acceptedCommit: 'a'.repeat(40), changes: { 'docs/announcement.md': 'Accepted text' } }, { id: 'wrong-path', status: 'accepted', acceptedCommit: 'e'.repeat(40), changes: { 'docs/other.md': 'Other' } }, { id: 'pending', status: 'pending', changes: { 'docs/announcement.md': 'Pending' } }], ...(unsupported ? {} : { definition: { id: 'northwind', title: 'Northwind', defaultDocument: 'docs/announcement.md' }, actions: [{ id: 'announce', name: 'Local announcement', document: 'announcement', path: 'docs/announcement.md' }], announcements: [{ id: 'op-one', action: 'announce', proposal: 'accepted', stage: 'pending' }, { id: 'op-two', action: 'announce', proposal: 'accepted', stage: 'pending' }] }) };
  if (noActions) { state.actions = []; state.announcements = []; }
  const f = { page, state, posts: [], views: { 'op-one': inspected(), 'op-two': inspected('op-two', false) }, hold: null };
  await page.route('http://canvas.test/**', async route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/state') return route.fulfill({ json: state });
    if (route.request().method() === 'POST') {
      const body = route.request().postDataJSON(); f.posts.push([url.pathname, body]);
      if (f.postHook && await f.postHook(route, body, url)) return;
      const view = f.views[body.id];
      if (url.pathname.endsWith('/decision')) { view.status = body.approved ? 'approved' : 'denied'; view.confirmation.head++; }
      if (url.pathname.endsWith('/continue')) { view.concluded = true; view.confirmation.head++; if (view.status === 'approved') { view.status = 'simulated'; view.receipt = { kind: 'SIMULATED_ANNOUNCEMENT', snapshot: view.snapshot }; } }
      return route.fulfill({ json: url.pathname.endsWith('/request') ? { id: 'op-two' } : {} });
    }
    if (url.pathname === '/api/announcement') {
      const id = url.searchParams.get('id');
      const value = structuredClone(f.views[id]);
      if (f.hold) { const hold = f.hold; f.hold = null; await hold; }
      return route.fulfill({ json: value });
    }
    if (url.pathname === '/api/document') return route.fulfill({ json: { accepted: 'Accepted text' } });
    const file = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
    if (['index.html', 'app.js', 'style.css'].includes(file)) return route.fulfill({ body: fs.readFileSync(path.join(root, 'web', file)), contentType: file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html' });
    return route.fulfill({ status: 404, body: '{}' });
  });
  await page.goto('http://canvas.test/');
  await page.waitForFunction(() => ui.state !== null);
  f.select = async (id = 'op-one') => { await page.selectOption('#announcement-operation', id); await page.waitForFunction(id => ui.publication?.view?.id === id && !ui.publication.loading, id); };
  f.refresh = async () => { await page.evaluate(async () => { await refreshState(); await inspectAnnouncement(); }); await page.waitForFunction(() => !ui.publication.loading); };
  return f;
}

test('native persona select retains undeclared active attribution and follows polling without a POST', async t => {
  const f=await fixture(t,{noActions:true});
  for (const people of [[],[{id:'coach',name:'Declared adviser'}]]) {
    f.state.people=people;
    for (const active of ['GM','owner']) {
      f.state.role=active;
      await f.refresh();
      assert.equal(await f.page.locator('#role').inputValue(),active);
      assert.equal(await f.page.locator(`#role option[value="${active}"]`).textContent(),`Attribution ID · ${active}`);
      assert.equal(await f.page.evaluate(()=>ui.state.role),active);
      assert.deepEqual(f.state.people,people);
    }
  }
  // A pending selection may remain visible while busy, without dropping the
  // separately recorded active attribution; idle polls then select that active ID.
  await f.page.evaluate(()=>{ui.busy=true;});
  f.state.role='GM'; await f.refresh();
  assert.equal(await f.page.locator('#role').inputValue(),'owner');
  assert.equal(await f.page.locator('#role option[value="GM"]').count(),1);
  await f.page.evaluate(()=>{ui.busy=false;renderState();});
  assert.equal(await f.page.locator('#role').inputValue(),'GM');
  assert.deepEqual(f.posts,[]);
});

for (const mode of ['host-unreadable', 'inactive-gate', 'recoverable']) {
  test(`local Abandon of ${mode} requires full fresh confirmation and never dispatches`, async t => {
    const f = await fixture(t); const { page } = f;
    page.setDefaultTimeout(3000);
    const v = f.views['op-one'];
    v.status = mode === 'recoverable' ? 'recoverable' : 'unavailable';
    if (v.status === 'unavailable') v.confirmation.unavailable = true;
    if (mode === 'host-unreadable') Object.assign(v.confirmation, { head: 0, executionID: '', toolCallID: '', requestSeq: 0 });
    await f.select();
    assert.equal(await page.locator('#announcement-abandon').isVisible(), true);
    assert.equal(await page.locator('#announcement-abandon').isDisabled(), true);
    assert.equal(await page.locator('#announcement-approve').isVisible(), false);
    assert.equal(await page.locator('#announcement-continue').isVisible(), false);
    if (mode !== 'recoverable') assert.equal(await page.locator('#announcement-recover').isVisible(), false);
    assert.deepEqual(f.posts, []);
    await page.check('#announcement-confirm'); await page.click('#announcement-abandon');
    await page.waitForFunction(() => !ui.publication.busy);
    assert.equal(f.posts.length, 1); assert.equal(f.posts[0][0], '/api/announcement/abandon');
    assert.deepEqual(f.posts[0][1].confirmation, { ...v.confirmation, confirmed: true });
  });
}

test('incomplete native input permits only explicit local abandon with the full fresh tuple', async t => {
  const f = await fixture(t); const { page } = f;
  page.setDefaultTimeout(3000);
  f.views['op-one'].status = 'incomplete'; f.views['op-one'].confirmation.requestSeq = 0;
  await f.select();
  assert.equal(await page.locator('#announcement-abandon').isVisible(), true);
  assert.equal(await page.locator('#announcement-abandon').isDisabled(), true);
  assert.equal(await page.locator('#announcement-recover').isVisible(), false);
  assert.equal(await page.locator('#announcement-approve').isVisible(), false);
  await page.check('#announcement-confirm'); await page.click('#announcement-abandon');
  await page.waitForFunction(() => !ui.publication.busy);
  assert.equal(f.posts.length,1); assert.equal(f.posts[0][0],'/api/announcement/abandon');
  assert.equal(f.posts[0][1].confirmation.head,5); assert.equal(f.posts[0][1].confirmation.executionID,'execution-one');
  assert.deepEqual(f.posts[0][1].confirmation,{...f.views['op-one'].confirmation,confirmed:true});
});

test('older and current snapshots use the same unchecked gate; exact content is inert; no render dispatch', async t => {
  const f = await fixture(t); const { page } = f;
  assert.equal(await page.locator('#announcement-inspection').isVisible(), false);
  await f.select();
  assert.equal(await page.locator('#announcement-warning').textContent(), 'A newer version exists; approving publishes this older version');
  assert.equal(await page.locator('#announcement-content').textContent(), snapshot('op-one').content);
  assert.equal(await page.locator('#announcement-content img').count(), 0);
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.equal(await page.locator('#announcement-approve').isDisabled(), true);
  await page.check('#announcement-confirm');
  await f.select('op-two');
  assert.equal(await page.locator('#announcement-warning').isVisible(), false);
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.equal(await page.locator('#announcement-approve').isDisabled(), true);
  assert.deepEqual(f.posts, []);
});

test('decision sends explicit bool only; Continue requires a new checked confirmation and receipt is simulated', async t => {
  const f = await fixture(t); const { page } = f; await f.select();
  await page.check('#announcement-confirm'); await page.click('#announcement-approve');
  await page.waitForFunction(() => ui.publication?.view?.status === 'approved' && !ui.publication.busy);
  assert.equal(f.posts.length, 1); assert.equal(f.posts[0][0], '/api/announcement/decision');
  assert.equal(f.posts[0][1].approved, true); assert.equal(f.posts[0][1].confirmation.confirmed, true);
  assert.equal(f.posts[0][1].confirmation.revision, 'a'.repeat(40));
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.equal(await page.locator('#announcement-continue').isDisabled(), true);
  await page.check('#announcement-confirm'); await page.click('#announcement-continue');
  await page.waitForFunction(() => ui.publication?.view?.status === 'simulated');
  assert.equal(f.posts[1][0], '/api/announcement/continue');
  assert.equal(await page.locator('#announcement-receipt').textContent(), 'SIMULATED_ANNOUNCEMENT: recorded locally; nothing sent or published');
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
});

test('denial is not publication, even after its separate Continue concludes', async t => {
  const f = await fixture(t); const { page } = f; await f.select();
  await page.check('#announcement-confirm'); await page.click('#announcement-deny');
  await page.waitForFunction(() => ui.publication?.view?.status === 'denied' && !ui.publication.busy);
  assert.equal(f.posts[0][1].approved, false);
  assert.equal(await page.locator('#announcement-receipt').isVisible(), false);
  await page.check('#announcement-confirm'); await page.click('#announcement-continue');
  await page.waitForFunction(() => ui.publication?.view?.concluded);
  assert.equal(await page.locator('#announcement-receipt').isVisible(), false);
  assert.equal(await page.locator('#announcement-continue').isVisible(), false);
});

test('head, current revision, newer, snapshot, persona and operation changes revoke checked consent', async t => {
  const f = await fixture(t); const { page } = f; await f.select();
  for (const change of [v => { v.confirmation.head++; }, v => { v.currentRevision = v.confirmation.currentRevision = 'e'.repeat(40); }, v => { v.newer = v.confirmation.newer = false; }, v => { v.snapshot.content += 'changed'; }]) {
    await page.check('#announcement-confirm'); change(f.views['op-one']); await f.refresh();
    assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  }
  await page.check('#announcement-confirm'); f.state.head = 'new-native-head'; await f.refresh();
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  await page.check('#announcement-confirm'); f.state.role = 'coach'; await f.refresh();
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.deepEqual(f.posts, []);
});

test('malformed inspection fields cannot send native actions even with a synthetic checked event', async t => {
  const f = await fixture(t); const { page } = f;
  for (const corrupt of [v => { v.confirmation.head = '5'; }, v => { delete v.confirmation.newer; }, v => { v.snapshot.digest = ''; }, v => { v.confirmation.revision = 'e'.repeat(40); }, v => { v.receipt = { kind: 'published', snapshot: v.snapshot }; }]) {
    f.views['op-one'] = inspected(); corrupt(f.views['op-one']);
    await page.selectOption('#announcement-operation', ''); await page.selectOption('#announcement-operation', 'op-one');
    await page.waitForFunction(() => !ui.publication.loading);
    await page.evaluate(() => { const check = document.getElementById('announcement-confirm'); check.checked = true; check.dispatchEvent(new Event('change')); document.getElementById('announcement-approve').dispatchEvent(new Event('click')); });
    assert.equal(await page.locator('#announcement-approve').isDisabled(), true);
  }
  assert.deepEqual(f.posts, []);
});

test('late inspection cannot replace the newly selected operation; eligible proposal selector filters by exact path', async t => {
  const f = await fixture(t); const { page } = f;
  assert.deepEqual(await page.locator('#announcement-proposal option').evaluateAll(nodes => nodes.map(n => n.value)), ['accepted']);
  let release; f.hold = new Promise(resolve => { release = resolve; });
  await page.selectOption('#announcement-operation', 'op-one');
  await page.waitForFunction(() => ui.publication.loading);
  assert.equal(await page.locator('#announcement-confirm').evaluate(n => n.disabled), true, 'explicit inspection keeps native locking');
  await page.locator('#announcement-operation').focus();
  await f.select('op-two'); release(); await page.waitForTimeout(150);
  assert.equal(await page.locator('#announcement-operation').evaluate(n => n === document.activeElement), true, 'late explicit inspection cannot steal selection focus');
  assert.equal(await page.locator('#announcement-content').textContent(), snapshot('op-two').content);
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.deepEqual(f.posts, []);
});

test('request is explicit, pins a matching accepted proposal, and never carries consent across reload', async t => {
  const f = await fixture(t); const { page } = f;
  await f.select(); await page.check('#announcement-confirm'); await f.refresh();
  assert.equal(await page.locator('#announcement-confirm').isChecked(), true, 'unchanged read-only inspection retains consent');
  assert.deepEqual(f.posts, []);
  await page.click('#announcement-request');
  await page.waitForFunction(() => ui.publication?.view?.id === 'op-two' && !ui.publication.loading && !ui.publication.busy);
  assert.deepEqual(f.posts, [['/api/announcement/request', { role: 'GM', action: 'announce', proposal: 'accepted' }]]);
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  await page.check('#announcement-confirm'); await page.reload();
  await page.waitForFunction(() => ui.state !== null);
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.equal(await page.locator('#announcement-inspection').isVisible(), false);
  f.state.proposals[0].acceptedCommit = ''; await f.refresh();
  await page.evaluate(() => document.getElementById('announcement-request').dispatchEvent(new Event('click')));
  assert.equal(f.posts.length, 1, 'malformed acceptance must not send');
});

test('Recover requires a known recoverable session; unknown-session uncertainty supports local Abandon', async t => {
  const f = await fixture(t); const { page } = f;
  for (const [status, session, recover, abandon] of [['recoverable', 'known', true, true], ['recoverable', '', false, false], ['uncertain', 'known', false, false], ['uncertain', '', false, true], ['approved', 'native-session', false, false], ['failed', 'native-session', false, false]]) {
    const v = inspected(); v.status = status; v.session = v.confirmation.session = session; f.views['op-one'] = v;
    await page.selectOption('#announcement-operation', ''); await f.select();
    assert.equal(await page.locator('#announcement-recover').isVisible(), recover);
    assert.equal(await page.locator('#announcement-abandon').isVisible(), abandon);
    if (recover || abandon) {
      await page.check('#announcement-confirm');
      await page.click(recover ? '#announcement-recover' : '#announcement-abandon');
      await page.waitForFunction(() => !ui.publication.busy && !ui.publication.loading);
      assert.equal(f.posts.at(-1)[0], '/api/announcement/' + (recover ? 'recover' : 'abandon'));
      assert.equal(f.posts.at(-1)[1].confirmation.confirmed, true);
      assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
    }
  }
  assert.equal(f.posts.length, 2);
});

test('refused action remains visible across read-only polls, consumes consent, and never auto-retries', async t => {
  const f = await fixture(t); const { page } = f; await f.select();
  f.postHook = async route => { await route.fulfill({ status: 409, json: { error: 'Evidence changed. Inspect again.' } }); return true; };
  await page.check('#announcement-confirm'); await page.click('#announcement-approve');
  await page.waitForFunction(() => !ui.publication.busy && !ui.publication.loading);
  await f.refresh();
  assert.match(await page.locator('#announcement-status').textContent(), /Evidence changed/);
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.equal(f.posts.length, 1);
});

test('held action response cannot switch the new selection back or show stale errors', async t => {
  const f = await fixture(t); const { page } = f; await f.select();
  let release; const held = new Promise(resolve => { release = resolve; });
  f.postHook = async route => { await held; await route.fulfill({ status: 409, json: { error: 'Old operation refused' } }); return true; };
  await page.check('#announcement-confirm'); await page.click('#announcement-approve');
  await page.locator('#announcement-operation').focus();
  await page.selectOption('#announcement-operation', 'op-two'); release();
  await page.waitForFunction(() => ui.publication?.view?.id === 'op-two' && !ui.publication.loading && !ui.publication.busy);
  assert.equal(await page.locator('#announcement-operation').evaluate(n => n === document.activeElement), true, 'late mutation cannot steal newer selection focus');
  assert.doesNotMatch(await page.locator('#announcement-status').textContent(), /Old operation/);
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.equal(f.posts.length, 1);
});

for (const control of ['confirm', 'continue', 'refresh']) {
  test(`unchanged background poll preserves focused ${control} throughout held inspection`, async t => {
    const f = await fixture(t); const { page } = f;
    if (control === 'continue') f.views['op-one'].status = 'approved';
    await f.select(); await page.check('#announcement-confirm');
    const target = page.locator('#announcement-' + control);
    await target.focus();
    let release; f.hold = new Promise(resolve => { release = resolve; });
    const polling = page.waitForRequest('**/api/announcement?id=op-one');
    try {
      // Let the real one-second state poll start the background inspection.
      await polling; await page.waitForFunction(() => ui.publication.loading);
      assert.equal(await target.evaluate(n => n === document.activeElement), true, 'held poll must not blur the focused control');
      assert.equal(await target.getAttribute('aria-disabled'), 'true');
    } finally { release(); }
    await page.waitForFunction(() => !ui.publication.loading);
    assert.equal(await target.evaluate(n => n === document.activeElement), true, 'unchanged completion must preserve focus');
    assert.equal(await target.getAttribute('aria-disabled'), 'false');
    assert.equal(await page.locator('#announcement-confirm').isChecked(), true);
    assert.deepEqual(f.posts, []);
  });
}

test('held background inspection blocks pointer and keyboard actions; changed proof revokes consent', async t => {
  const f = await fixture(t); const { page } = f;
  f.views['op-one'].status = 'approved'; await f.select(); await page.check('#announcement-confirm');
  f.views['op-one'].confirmation.head++;
  let release; f.hold = new Promise(resolve => { release = resolve; });
  const polling = page.waitForRequest('**/api/announcement?id=op-one');
  try {
    await polling; await page.waitForFunction(() => ui.publication.loading);
    // Raw browser input avoids Playwright waiting for aria-disabled to clear.
    const check = page.locator('#announcement-confirm');
    await check.scrollIntoViewIfNeeded(); const checkBox = await check.boundingBox();
    await page.mouse.click(checkBox.x + checkBox.width / 2, checkBox.y + checkBox.height / 2);
    assert.equal(await check.isChecked(), true, 'pointer cannot change consent during inspection');
    await check.focus(); await page.keyboard.press('Space');
    assert.equal(await check.isChecked(), true, 'keyboard cannot change consent during inspection');
    const next = page.locator('#announcement-continue');
    await next.scrollIntoViewIfNeeded(); const button = await next.boundingBox();
    await page.mouse.click(button.x + button.width / 2, button.y + button.height / 2);
    await next.focus(); await page.keyboard.press('Enter'); await page.keyboard.press('Space');
    assert.deepEqual(f.posts, [], 'neither pointer nor keyboard may dispatch while evidence is in flight');
  } finally { release(); }
  await page.waitForFunction(() => !ui.publication.loading);
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.equal(await page.locator('#announcement-continue').isDisabled(), true);
  await page.evaluate(() => document.getElementById('announcement-continue').dispatchEvent(new Event('click')));
  assert.deepEqual(f.posts, [], 'changed proof cannot use the previous consent');
  f.views['op-one'].status = 'failed'; await f.refresh();
  await page.evaluate(() => {
    const check = document.getElementById('announcement-confirm'); check.checked = true; check.dispatchEvent(new Event('change'));
    document.getElementById('announcement-continue').dispatchEvent(new Event('click'));
  });
  assert.equal(await page.locator('#announcement-continue').isVisible(), false);
  assert.deepEqual(f.posts, [], 'invalid native status cannot continue even with synthetic consent');
});

test('unsupported state and generic definitions without actions have no local-publication panel; phone focus has no overflow', async t => {
  const unsupported = await fixture(t, { unsupported: true });
  assert.equal(await unsupported.page.locator('#announcement-panel').isVisible(), false);
  assert.match(await unsupported.page.locator('#notice').textContent(), /Unsupported state/);
  assert.equal(await unsupported.page.locator('#generic-run-submit').isDisabled(), true);
  const noActions = await fixture(t, { noActions: true });
  assert.equal(await noActions.page.locator('#announcement-panel').isVisible(), false);
  assert.equal(await noActions.page.locator('#notice').isVisible(), false);
  assert.equal(await noActions.page.locator('#document-panel').getByText('Accepted text', { exact: true }).count(), 2);
  const f = await fixture(t); await f.page.setViewportSize({ width: 320, height: 800 }); await f.select();
  assert.equal(await f.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await f.page.locator('#announcement-confirm').focus();
  assert.equal(await f.page.locator('#announcement-confirm').evaluate(n => n === document.activeElement), true);
});

test('unsupported response revokes native consent and held inspection ownership before controls can dispatch', async t => {
  const f = await fixture(t); const { page } = f; await f.select();
  await page.check('#announcement-confirm');
  let release; f.hold = new Promise(resolve => { release = resolve; });
  await page.evaluate(() => { void inspectAnnouncement(true); });
  await page.waitForFunction(() => ui.publication.loading);
  delete f.state.definition;
  await page.evaluate(() => refreshState());
  assert.equal(await page.locator('#announcement-confirm').isChecked(), false);
  assert.equal(await page.locator('#announcement-panel').isVisible(), false);
  assert.match(await page.locator('#notice').textContent(), /Unsupported state/);
  release(); await page.waitForTimeout(150);
  assert.equal(await page.evaluate(() => ui.publication.view), null);
  await page.evaluate(() => { document.getElementById('announcement-confirm').checked = true; document.getElementById('announcement-approve').dispatchEvent(new Event('click')); });
  assert.deepEqual(f.posts, []);
});
