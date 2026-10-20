// Browser smoke test for the UI. Run it with `make browser-smoke`.
//
// Needs Go, Node and Playwright with Chromium. Install them with
// `npm install -g playwright && npx playwright install chromium`, or point
// PLAYWRIGHT_MODULE at an existing install (PLAYWRIGHT_BROWSERS_PATH is honored).
// With no URL argument it builds and starts the test-only fixture
// (TestBrowserFixture in internal/canvas, gated by CANVAS_BROWSER_FIXTURE=1) on
// an OS-assigned loopback port, independent of make dev. The fixture is a real Git/gRPC
// backend with a deterministic provider; this never exercises live inference.
// Pass a URL to test an already running fixture instead. Screenshots go to .state/.
const assert = require('node:assert/strict');
const { execFileSync, spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const root = path.resolve(__dirname, '..');
const screenshot = (name) => path.join(root, '.state', name);

function loadPlaywright() {
  if (process.env.PLAYWRIGHT_MODULE) {
    try { return require(process.env.PLAYWRIGHT_MODULE); } catch {}
  } else {
    try { return require('playwright'); } catch {}
    try { return require(path.join(execFileSync('npm', ['root', '-g'], { encoding: 'utf8', timeout: 10000, stdio: ['ignore', 'pipe', 'ignore'] }).trim(), 'playwright')); } catch {}
  }
  const error = new Error('Browser smoke needs Playwright: npm install -g playwright && npx playwright install chromium (or set PLAYWRIGHT_MODULE).');
  error.exitCode = 2;
  throw error;
}

// Run the built binary directly: its Go backend is in-process, so only this PID is owned.
async function startFixture({ signal, startupTimeout = 10000 } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'canvas-browser-'));
  let child, exited, stopPromise;
  let terminated = false;
  let stopping = false;
  const waitForExit = async () => {
    let timer;
    try {
      return await Promise.race([exited.then(() => true), new Promise(resolve => { timer = setTimeout(() => resolve(false), 750); })]);
    } finally { clearTimeout(timer); }
  };
  const stop = () => {
    if (!stopPromise) stopPromise = (async () => {
      stopping = true;
      try {
        if (child?.pid && !terminated) {
          child.kill('SIGTERM');
          if (!await waitForExit()) {
            child.kill('SIGKILL');
            if (!await waitForExit()) throw new Error('Owned browser fixture did not exit after SIGKILL');
          }
        }
      } finally { fs.rmSync(dir, { recursive: true, force: true }); }
    })();
    return stopPromise;
  };
  try {
    signal?.throwIfAborted();
    const binary = path.join(dir, 'canvas.test');
    execFileSync('go', ['test', '-c', '-o', binary, './internal/canvas'], { cwd: root, stdio: 'inherit', timeout: 60000 });
    signal?.throwIfAborted();
    child = spawn(binary, ['-test.run', '^TestBrowserFixture$', '-test.v'], {
      cwd: path.join(root, 'internal/canvas'),
      env: { ...process.env, CANVAS_BROWSER_FIXTURE: '1' },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    // Capture only readiness, never echo fixture output or spawn error details.
    child.stderr.resume();
    exited = new Promise(resolve => {
      child.once('exit', (code, exitSignal) => { terminated = true; resolve({ code, signal: exitSignal }); });
      child.once('error', () => { terminated = true; resolve({ spawnError: true }); });
    });
    const failure = exited.then(result => {
      if (!stopping) throw new Error(result.spawnError ? 'Browser fixture could not spawn' : `Browser fixture exited (${result.signal || result.code})`);
      return new Promise(() => {});
    });
    // Startup may reject before the caller receives the ready fixture.
    failure.catch(() => {});
    const url = await new Promise((resolve, reject) => {
      let output = '';
      let settled = false;
      const finish = (error, url) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        child.stdout.off('data', onData);
        child.stdout.resume();
        signal?.removeEventListener('abort', onAbort);
        if (error) reject(error); else resolve(url);
      };
      const onData = chunk => {
        output = (output + chunk.toString()).slice(-512);
        const match = output.match(/http:\/\/127\.0\.0\.1:\d+/);
        if (match) finish(null, match[0]);
      };
      const onAbort = () => finish(signal.reason);
      const timer = setTimeout(() => finish(new Error('Browser fixture startup timed out')), startupTimeout);
      child.stdout.on('data', onData);
      signal?.addEventListener('abort', onAbort, { once: true });
      if (signal?.aborted) onAbort();
      failure.catch(error => finish(error));
    });
    return { url, stop, failure };
  } catch (error) {
    await stop();
    throw error;
  }
}

// Hold actual document GETs while authoritative state advances; fixture data never changes.
async function reviewRefreshGuards(browser, base, recordedState, question) {
  for (const transition of ['stale', 'accepted', 'rejected', 'question']) {
    for (const fail of [false, true]) {
      const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
      const observed = structuredClone(recordedState);
      const run = observed.runs.find(item => item.id === question.run);
      const proposal = observed.proposals.find(item => item.id === run.proposal);
      proposal.status = 'pending';
      run.proof = { chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0, head: 'obsolete-native-proof' };
      let hold = false, signalHeld;
      const held = [];
      try {
        await page.route('**/api/state', route => route.fulfill({ json: observed }));
        await page.route('**/api/document?**', async route => {
          const response = await route.fetch();
          const json = await response.json();
          if (hold) await new Promise(resolve => { held.push({ route, json, resolve }); signalHeld(); });
          else await route.fulfill({ json });
        });
        await page.goto(base);
        await page.waitForFunction(() => !document.querySelector('#generic-run-submit').disabled);
        await page.locator('#conversation-content').getByRole('button', { name: 'View proposal', exact: true }).click();
        await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
        await page.locator('#evidence-toggle').click();
        await page.locator('#document-evidence details > summary').first().click();
        await page.locator('#document-evidence').getByRole('button', { name: 'Replay verified · open saved proof', exact: true }).click();
        assert.match(await page.locator('#document-evidence').textContent(), /obsolete-native-proof/);
        const reachedHold = new Promise(resolve => { signalHeld = resolve; });
        hold = true;
        if (transition === 'question') observed.conversation.push({ ...question, id: `held-question-${fail}`, status: 'queued', answer: undefined });
        else proposal.status = transition;
        await page.evaluate(() => refreshState());
        await reachedHold;
        const panel = page.locator('#document-panel');
        assert.match(await panel.textContent(), /Loading/, `${transition}: authority changes cannot retain obsolete review while GET is held`);
        assert.equal(await panel.locator('button.accept').count(), 0);
        assert.equal(await page.locator('#document-evidence').getByRole('button', { name: 'Replay verified · open saved proof', exact: true }).count(), 0);
        assert.doesNotMatch(await page.locator('#document-evidence').textContent(), /obsolete-native-proof|Verified/);
        hold = false;
        for (const pending of held) {
          await pending.route.fulfill(fail ? { status: 503, json: { error: 'Held document outage' } } : { json: { ...pending.json, status: 'pending', proof: run.proof } });
          pending.resolve();
        }
        await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
        if (transition === 'question' || fail) {
          assert.equal(await page.locator('#document-evidence').getByRole('button', { name: 'Replay verified · open saved proof', exact: true }).count(), 0);
          assert.doesNotMatch(await page.locator('#document-evidence').textContent(), /obsolete-native-proof|Verified/);
        }
        if (fail) {
          assert.equal(await panel.locator('button.accept').count(), 0);
          assert.doesNotMatch(await panel.textContent(), /Awaiting your approval/);
        }
        if (!fail && transition === 'stale') {
          assert.equal(await panel.locator('button.accept').isDisabled(), true);
          assert.equal(await panel.getByRole('button', { name: 'Reject', exact: true }).isDisabled(), false);
          assert.doesNotMatch(await panel.textContent(), /Awaiting your approval/);
        } else if (!fail && ['accepted', 'rejected'].includes(transition)) {
          assert.equal(await panel.locator('button.accept').count(), 0);
          assert.equal(await panel.getByRole('button', { name: 'Reject', exact: true }).count(), 0);
          assert.doesNotMatch(await panel.textContent(), /Awaiting your approval/);
        }
      } finally { await page.close(); }
    }
  }
  console.log('PR3 held-response safety passed: stale/accepted/rejected authority and same-session proof invalidation, each with delayed success and error; stale Reject returns enabled.');
}

// Native rendering checks use only recorded deterministic-fixture data and routed state copies.
async function reviewAccessibility(browser, base, recordedState, question) {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
  const observed = structuredClone(recordedState);
  const selected = observed.runs.find(run => run.id === question.run);
  const pending = observed.conversation.find(entry => entry.id === question.id);
  selected.status = 'running'; pending.status = 'running'; delete pending.answer;
  const sibling = { ...selected, id: 'accessibility-sibling', session: 'accessibility-sibling-session', proposal: undefined, seconds: 1 };
  observed.runs.push(sibling);
  let documentRequests = 0;
  const posts = [];
  page.on('request', request => {
    if (new URL(request.url()).pathname === '/api/document') documentRequests++;
    if (request.method() === 'POST') posts.push(request.url());
  });
  try {
    await page.route('**/api/state', route => route.fulfill({ json: observed }));
    await page.goto(base);
    await page.waitForFunction(() => !document.querySelector('#generic-run-submit').disabled);
    await page.locator(`#request-strip button[data-focus-key="request-proposal-${selected.proposal}"]`).click();
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    const status = page.locator('#poll-status');
    assert.equal(await status.getAttribute('role'), 'status');
    assert.equal(await status.getAttribute('aria-live'), 'polite');
    assert.equal(await status.getAttribute('aria-atomic'), 'true');
    assert.equal(await status.textContent(), '', 'initial history is silent');
    await page.evaluate(() => {
      window.pr3Announcements = [];
      new MutationObserver(() => window.pr3Announcements.push(document.querySelector('#poll-status').textContent))
        .observe(document.querySelector('#poll-status'), { childList: true, subtree: true, characterData: true });
    });
    await page.locator('#room-request').fill('Preserve accessibility draft');
    sibling.seconds += 1; pending.seconds = (pending.seconds || 0) + 1;
    await page.evaluate(() => refreshState());
    assert.deepEqual(await page.evaluate(() => window.pr3Announcements), [], 'duration-only polls are silent');
    Object.assign(selected, recordedState.runs.find(run => run.id === selected.id));
    Object.assign(pending, question);
    await page.evaluate(() => { notify('Important review error', true); return refreshState(); });
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    assert.match(await status.textContent(), /finished.*awaiting approval.*question answered/is);
    assert.doesNotMatch(await status.textContent(), new RegExp(question.text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
    await page.evaluate(() => refreshState());
    assert.equal((await page.evaluate(() => window.pr3Announcements)).length, 1, 'completion announces once');
    for (const [index, terminal] of ['failed', 'uncertain'].entries()) {
      sibling.status = terminal; pending.status = terminal;
      await page.evaluate(() => refreshState());
      assert.match(await status.textContent(), new RegExp(terminal === 'uncertain' ? 'interrupted' : 'failed', 'i'));
      assert.match(await status.textContent(), /Read-only question/);
      await page.evaluate(() => refreshState());
      assert.equal((await page.evaluate(() => window.pr3Announcements)).length, index + 2, `${terminal} announces only its transition`);
    }
    pending.status = 'unknown-recorded-status'; sibling.status = 'unknown-recorded-status';
    await page.evaluate(() => refreshState());
    assert.equal((await page.evaluate(() => window.pr3Announcements)).length, 3, 'unknown states invent no lifecycle notice');
    assert.equal(await page.locator('#notice').innerText(), 'Important review error');
    assert.equal(await page.locator('#room-request').inputValue(), 'Preserve accessibility draft');

    await page.locator('#document-panel .scope-detail > summary').click();
    await page.locator('#document-panel .proposal-scope').getByRole('button', { name: /^Review / }).filter({ hasText: 'Simulated announcement' }).click();
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    await page.locator('#document-panel').evaluate(node => { node.scrollTop = node.scrollHeight; window.pr3Review = document.querySelector('#document-panel').firstElementChild; window.pr3Scroll = node.scrollTop; });
    const requestsBefore = documentRequests;
    sibling.seconds += 1;
    await page.evaluate(() => refreshState());
    assert.equal(await page.locator('#document-panel').evaluate(node => document.querySelector('#document-panel').firstElementChild === window.pr3Review && node.scrollTop === window.pr3Scroll), true, 'unrelated sibling seconds retain native review DOM and reading');
    assert.equal(documentRequests, requestsBefore, 'unrelated sibling duration does not refetch documents');
    await page.locator('#document-panel .proposal-scope button').first().focus();
    const centralFocus = await page.locator('#document-panel .proposal-scope button').first().getAttribute('data-focus-key');
    const reading = await page.locator('#document-panel').evaluate(node => node.scrollTop);
    selected.seconds += 1;
    await page.evaluate(() => refreshState());
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    assert.equal(await page.locator('#document-panel .proposal-scope button').first().evaluate(node => node === document.activeElement), true, 'meaningful same-owner refresh restores attached review focus');
    assert.equal(await page.locator('#document-panel .proposal-scope button').first().getAttribute('data-focus-key'), centralFocus);
    assert.equal(await page.locator('#document-panel').evaluate(node => node.scrollTop), reading);
    assert.notEqual(await page.locator('#document-panel .scope-detail').getAttribute('open'), null);
    await page.locator('#document-panel button.evidence-link').first().click();
    await page.locator('#evidence-panel').evaluate(node => { node.scrollTop = 180; });
    const auditReading = await page.locator('#evidence-panel').evaluate(node => node.scrollTop);
    selected.seconds += 1;
    await page.evaluate(() => refreshState());
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    assert.equal(await page.locator('#document-evidence details > summary').first().evaluate(node => node === document.activeElement), true, 'same-owner refresh retains attached audit focus');
    assert.equal(await page.locator('#evidence-panel').evaluate(node => node.scrollTop), auditReading);
    assert.notEqual(await page.locator('#document-evidence details').first().getAttribute('open'), null);
    await page.locator('#document-panel .document-diff').focus();
    selected.seconds += 1;
    await page.evaluate(() => refreshState());
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    assert.equal(await page.locator('#document-panel .document-diff').evaluate(node => node === document.activeElement), true, 'same-owner refresh restores native diff focus');

    // A newer proposal-scope focus choice must own a held document delivery.
    // Hold the real fixture document response: focus chosen during the request owns delivery.
    let releaseDocument;
    let documentHeld;
    const heldDocument = new Promise(resolve => { documentHeld = resolve; });
    await page.route('**/api/document?**', async route => {
      const response = await route.fetch();
      await new Promise(resolve => {
        releaseDocument = async () => { await route.fulfill({ json: await response.json() }); resolve(); };
        documentHeld();
      });
    });
    await page.getByRole('button', { name: 'Reject', exact: true }).focus();
    selected.seconds += 1;
    await page.evaluate(() => refreshState());
    await heldDocument;
    await page.locator('#document-panel .proposal-scope button').first().focus();
    await releaseDocument();
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    assert.equal(await page.locator('#document-panel .proposal-scope button').first().evaluate(node => node === document.activeElement), true, 'delivery restores the reader\'s newer proposal-scope focus, not request-start Reject');
    await page.unroute('**/api/document?**');

    // A failed same-owner refresh transfers attached focus to Retry, then recovers
    // the actual document without posting or losing scope disclosure state.
    await page.route('**/api/document?**', route => route.fulfill({ status: 503, json: { error: 'Simulated document outage' } }));
    await page.locator('#document-panel .proposal-scope button').first().focus();
    selected.seconds += 1;
    await page.evaluate(() => refreshState());
    const retry = page.locator('#document-panel').getByRole('button', { name: 'Retry document', exact: true });
    await retry.waitFor();
    assert.equal(await retry.evaluate(node => node === document.activeElement), true, 'failed delivery focuses attached Retry');
    await page.unroute('**/api/document?**');
    await page.keyboard.press('Enter');
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    assert.equal(await page.locator('#document-panel .scope-detail').getAttribute('open') !== null, true, 'Retry preserves proposal-scope disclosure');

    // Resolve native OKLCH colors through a canvas into sRGB before checking contrast.
    const palette = () => page.evaluate(() => {
      const selectors = ['html', 'body', '.topbar', '#review', '#conversation', '#evidence-panel', '#room-request', '#role', '#room-post', '#room-ask', '#document-panel .proposal-banner', '.badge.finished'];
      const context = document.createElement('canvas').getContext('2d');
      const rgb = color => { context.clearRect(0, 0, 1, 1); context.fillStyle = color; context.fillRect(0, 0, 1, 1); return Array.from(context.getImageData(0, 0, 1, 1).data).slice(0, 3); };
      return selectors.map(selector => { const style = getComputedStyle(document.querySelector(selector)); return { selector, scheme: style.colorScheme, background: rgb(style.backgroundColor), color: rgb(style.color), border: rgb(style.borderTopColor) }; });
    });
    await page.emulateMedia({ colorScheme: 'light' }); const light = await palette();
    await page.emulateMedia({ colorScheme: 'dark' }); assert.deepEqual(await palette(), light, 'OS dark must compute identical light surfaces and native control colors');
    const luminance = rgb => rgb.map(channel => channel / 255).map(channel => channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4).reduce((sum, value, index) => sum + value * [.2126, .7152, .0722][index], 0);
    const contrast = (a, b) => { const x = luminance(a), y = luminance(b); return (Math.max(x, y) + .05) / (Math.min(x, y) + .05); };
    for (const selector of ['#room-request', '#role', '#room-ask']) {
      const control = light.find(item => item.selector === selector);
      assert.ok(contrast(control.border, control.background) >= 3, `${selector} boundary needs 3:1, observed ${contrast(control.border, control.background).toFixed(2)}`);
      assert.ok(contrast(control.border, light.find(item => item.selector === '#conversation').background) >= 3, `${selector} boundary also contrasts with the adjacent canvas`);
    }
    await page.locator('#room-request').focus();
    for (const interaction of ['hover', 'active']) {
      await page.locator('#room-ask').hover();
      await page.locator('#room-ask').evaluate(node => Promise.all(node.getAnimations().map(animation => animation.finished)));
      if (interaction === 'active') await page.mouse.down();
      const control = (await palette()).find(item => item.selector === '#room-ask');
      assert.ok(contrast(control.border, control.background) >= 3, `${interaction} control boundary needs 3:1, observed ${contrast(control.border, control.background).toFixed(2)}: ${JSON.stringify(control)}`);
      if (interaction === 'active') { await page.mouse.move(0, 0); await page.mouse.up(); }
    }
    await page.locator('#role').focus();
    const focus = await page.locator('#role').evaluate(node => {
      const style = getComputedStyle(node), context = document.createElement('canvas').getContext('2d');
      context.fillStyle = style.outlineColor; context.fillRect(0, 0, 1, 1);
      return { color: Array.from(context.getImageData(0, 0, 1, 1).data).slice(0, 3), width: style.outlineWidth, offset: style.outlineOffset, visible: node.matches(':focus-visible') };
    });
    assert.equal(focus.visible, true); assert.equal(focus.width, '3px'); assert.equal(focus.offset, '3px');
    assert.ok(contrast(focus.color, light.find(item => item.selector === '.topbar').background) >= 3, 'header focus ring needs 3:1 against dark header');
    await page.locator('#room-request').focus();
    assert.equal(await page.locator('#room-request').evaluate(node => getComputedStyle(node).outlineWidth), '3px');
    const finished = light.find(item => item.selector === '.badge.finished');
    assert.notDeepEqual(finished.background, light.find(item => item.selector === '#room-post').background, 'Finished is not a primary action');
    const approvalSurface = await page.evaluate(() => {
      const context = document.createElement('canvas').getContext('2d');
      context.fillStyle = getComputedStyle(document.documentElement).getPropertyValue('--green-soft').trim();
      context.fillRect(0, 0, 1, 1);
      return Array.from(context.getImageData(0, 0, 1, 1).data).slice(0, 3);
    });
    assert.notDeepEqual(finished.background, approvalSurface, 'Finished must not signal approval');
    await page.emulateMedia({ reducedMotion: 'reduce' });
    assert.equal(await page.locator('#room-post').evaluate(node => getComputedStyle(node).transitionDuration), '0s');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.keyboard.press('Control+Home');
    await page.locator('a.skip-link').first().focus(); await page.keyboard.press('Enter');
    assert.equal(await page.locator('#review').evaluate(node => node === document.activeElement), true, 'skip-to-decision reaches the review landmark');

    await page.locator('#document-panel .proposal-scope').getByRole('button', { name: /^Review / }).first().focus();
    selected.seconds += 1;
    await page.evaluate(() => refreshState());
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    assert.equal(await page.locator('#document-panel .proposal-scope button').first().evaluate(node => node === document.activeElement), true, 'same-owner refresh restores attached proposal-scope focus');
    await page.locator('#navigation-toggle').click();
    for (const width of [320, 390, 960, 1024, 1280, 1600]) {
      await page.setViewportSize({ width, height: 844 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `normal ${width}px has no page overflow`);
      assert.equal(await page.locator('#document-tabs button').evaluateAll(tabs => tabs.some(tab => tab.scrollWidth > tab.clientWidth + 1)), false, `${width}px document tab names stay readable in their horizontally scrolling strip`);
    }
    await page.locator('#new-menu > summary').click();
    await page.locator('#generic-run-toggle').click();
    await page.locator('#room-request').focus();
    const normalRootFontSize = await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).fontSize));
    for (const width of [320, 390]) {
      await page.setViewportSize({ width, height: 844 });
      for (const doubled of [false, true]) {
        await page.evaluate(({ doubled, normalRootFontSize }) => { document.documentElement.style.fontSize = doubled ? `${normalRootFontSize * 2}px` : ''; }, { doubled, normalRootFontSize });
        assert.equal(await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).fontSize)), normalRootFontSize * (doubled ? 2 : 1), 'text scaling must actually exercise 100% or 200% of the measured baseline');
        const reflow = await page.evaluate(() => ({ width: innerWidth, scrollWidth: document.documentElement.scrollWidth, outside: Array.from(document.querySelectorAll('body *')).flatMap(node => {
          const rect = node.getBoundingClientRect();
          return rect.width && (rect.left < -1 || rect.right > innerWidth + 1) ? [{ tag: node.tagName, id: node.id, className: node.className, left: Math.round(rect.left), right: Math.round(rect.right), width: Math.round(rect.width) }] : [];
        }).slice(0, 24) }));
        assert.equal(reflow.scrollWidth > reflow.width, false, `${width}px ${doubled ? 'doubled' : 'normal'} text has no page overflow: ${JSON.stringify(reflow)}`);
        for (const selector of ['#role', '#generic-run-submit', '#room-post', '#room-ask', '#navigation-toggle', '#generic-run-toggle', '#evidence-toggle', '#document-panel .affected-documents button', '#generic-input-toggle', '#document-panel .full-text-control']) {
          if (selector === '#generic-run-toggle' || selector === '#generic-input-toggle') await page.locator('#new-menu').evaluate(node => { node.open = true; });
          const target = page.locator(selector).first();
          const box = await target.boundingBox();
          assert.ok(box.width >= 44 && box.height >= 44, `phone ${selector} practical touch area: ${JSON.stringify(box)}`);
        }
        await page.locator('#new-menu').evaluate(node => { node.open = false; });
        await page.locator('#generic-run-submit').scrollIntoViewIfNeeded();
        assert.equal(await page.locator('#generic-run-submit').evaluate(node => { const rect = node.getBoundingClientRect(); return rect.left >= 0 && rect.right <= innerWidth && node.contains(document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2)); }), true, 'reflow keeps Request run reachable');
        assert.equal(await page.locator('#document-panel .proposal-banner').evaluate(node => getComputedStyle(node).position), 'static', 'generic banner must not obscure replacement bytes when text is doubled');
      }
    }
    await page.evaluate(() => { document.documentElement.style.fontSize = ''; });
    await page.setViewportSize({ width: 800, height: 500 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, '200%-equivalent CSS viewport reflows without page overflow');
    const announcementsBeforeError = (await page.evaluate(() => window.pr3Announcements)).length;
    selected.error = 'Private bookkeeping detail /local/path retained only in recorded evidence';
    await page.evaluate(() => refreshState());
    assert.match(await status.textContent(), /proposal recorded.*needs attention/is);
    assert.doesNotMatch(await status.textContent(), /awaiting approval|Private|bookkeeping|local\/path|failed|Finished|Applied/i, 'a later execution error announces attention without repeating approval or private details');
    selected.error = 'Another private detail'; selected.seconds += 1;
    await page.evaluate(() => refreshState());
    assert.equal((await page.evaluate(() => window.pr3Announcements)).length, announcementsBeforeError + 1, 'unchanged published status announces new error presence only once');
    assert.equal(selected.status, 'proposed');
    assert.deepEqual(posts, [], 'accessibility checks never mutate recorded fixture data or invoke a model');
    console.log('PR3 native checks passed: light colors identical under OS dark; control and header-focus contrast >=3:1; neutral Finished; reduced motion; one-time concise completion/failure/interruption/needs-attention notices; unrelated run clocks retain review DOM without refetch; meaningful refresh retains review/audit/diff/proposal-scope focus and reading; 320/390 doubled text, 44px functional targets, 960/1024/1280/1600 and 800x500 reflow.');
  } finally { await page.close(); }
}

async function smoke(base, { chromium } = loadPlaywright(), signal) {
  fs.mkdirSync(path.join(root, '.state'), { recursive: true });
  const browser = await chromium.launch({ headless: true, timeout: 10000 });
  let closing;
  const close = () => closing ||= browser.close();
  const onAbort = () => { close().catch(() => {}); };
  signal?.addEventListener('abort', onAbort, { once: true });
  try {
    signal?.throwIfAborted();
    const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
    const pageErrors = [];
    page.on('pageerror', () => pageErrors.push('browser exception'));
    const state = () => page.evaluate(async () => (await fetch('/api/state')).json());
    const newestVisible = () => page.waitForFunction(() => {
      const pane = document.querySelector('#conversation-content');
      return pane.scrollHeight - pane.clientHeight - pane.scrollTop < 2;
    }, undefined, { timeout: 3000 });
    await page.goto(base);
    const initialResponse = await page.request.get(`${base}/api/state`);
    assert.equal(initialResponse.ok(), true);
    const initial = await initialResponse.json();
    assert.equal(initial.definition?.id, 'northwind-prd', 'native generic fixture required before browser steps');
    assert.deepEqual(initial.agents.map(agent => agent.id).sort(), ['reviewer', 'writer']);
    assert.deepEqual([...initial.documents].sort(), ['announcement.md', 'brief.json', 'feedback/initial.json', 'prd.json', 'review.json']);
    assert.equal(initial.runs.length, 0, 'fixture startup must not run a model');
    assert.equal(initial.proposals.length, 0);
    assert.equal(initialResponse.headers()['x-canvas-provider'], 'deterministic-fixture');
    await page.waitForFunction(() => !document.querySelector('#generic-run-submit').disabled);
    assert.match(await page.locator('#provider-guidance').innerText(), /Deterministic fixture provider.*No real inference/s);
    const navigationToggle = page.getByRole('button', { name: 'Navigation', exact: true });
    const evidenceToggle = page.getByRole('button', { name: 'Evidence panel', exact: true });
    assert.equal(await page.locator('#navigation').isVisible(), false);
    assert.equal(await page.locator('#evidence-panel').isVisible(), false);
    assert.equal(await navigationToggle.getAttribute('aria-controls'), 'navigation');
    assert.equal(await evidenceToggle.getAttribute('aria-expanded'), 'false');
    assert.equal(await page.locator('#request-strip').isVisible(), false, 'empty fixture has no pending work');
    assert.equal(await page.locator('#generic-request').isVisible(), false);
    await page.locator('#room-request').fill('Unsubmitted room draft');
    await navigationToggle.focus(); await page.keyboard.press('Enter');
    assert.equal(await page.locator('#navigation').isVisible(), true);
    await page.locator('#agents button').first().focus();
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#navigation').isVisible(), false);
    assert.equal(await navigationToggle.evaluate(node => node === document.activeElement), true);
    await evidenceToggle.focus(); await page.keyboard.press('Space');
    assert.equal(await page.locator('#evidence-panel').isVisible(), true);
    await page.locator('#evidence-close').click();
    assert.equal(await evidenceToggle.evaluate(node => node === document.activeElement), true);
    assert.equal(await page.locator('#room-request').inputValue(), 'Unsubmitted room draft');
    await navigationToggle.click();
    await page.locator('#new-menu > summary').click();
    await page.locator('#generic-run-toggle').click();
    await page.locator('#generic-request').fill('Unsubmitted agent draft');
    await page.locator('#role').selectOption('owner');
    await page.waitForFunction(() => document.querySelector('#role').value === 'owner' && !document.querySelector('#generic-run-submit').disabled);
    assert.equal(await page.locator('#navigation').isVisible(), false);
    await evidenceToggle.click();
    await page.locator('#role').selectOption('GM');
    await page.waitForFunction(() => document.querySelector('#role').value === 'GM' && !document.querySelector('#generic-run-submit').disabled);
    assert.equal(await page.locator('#navigation').isVisible(), true);
    assert.equal(await page.locator('#evidence-panel').isVisible(), false);
    await page.waitForTimeout(1100);
    assert.equal(await page.locator('#generic-request').inputValue(), 'Unsubmitted agent draft');
    assert.equal(await page.locator('#room-request').inputValue(), 'Unsubmitted room draft');
    await page.reload();
    await page.waitForFunction(() => !document.querySelector('#generic-run-submit').disabled);
    assert.equal(await page.locator('#navigation').isVisible(), true, 'GM layout survives reload');
    await page.locator('#role').selectOption('owner');
    await page.waitForFunction(() => document.querySelector('#role').value === 'owner' && !document.querySelector('#generic-run-submit').disabled);
    assert.equal(await page.locator('#evidence-panel').isVisible(), true, 'owner preference remains independent');
    await page.locator('#role').selectOption('GM');
    await page.waitForFunction(() => document.querySelector('#role').value === 'GM' && !document.querySelector('#generic-run-submit').disabled);
    for (const mode of ['corrupt', 'blocked']) {
      const fallback = await browser.newPage();
      try {
        await fallback.addInitScript(mode => {
          if (mode === 'corrupt') localStorage.setItem('canvas.layout.v1', '{broken');
          else Object.defineProperty(window, 'localStorage', { get() { throw new Error('blocked'); } });
        }, mode);
        await fallback.goto(base); await fallback.waitForFunction(() => !document.querySelector('#generic-run-submit').disabled);
        assert.equal(await fallback.locator('#navigation').isVisible(), false, mode);
        assert.equal(await fallback.locator('#evidence-panel').isVisible(), false, mode);
        await fallback.getByRole('button', { name: 'Navigation', exact: true }).click();
        assert.equal(await fallback.locator('#navigation').isVisible(), true, mode);
        assert.equal(await fallback.locator('#connection').isVisible(), false, 'storage must not affect API');
      } finally { await fallback.close(); }
    }
    await page.locator('#new-menu > summary').click();
    await page.locator('#generic-run-toggle').click();
    const initialPRD = await page.evaluate(async () => (await (await fetch('/api/document?path=prd.json')).json()).accepted);
    assert.match(initialPRD, /Fictional Northwind PRD/);
    await page.getByRole('tab', { name: 'Proposed PRD', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
    const acceptedSection = page.locator('#document-panel .document-section').filter({ has: page.getByRole('heading', { name: 'Currently accepted content', exact: true }) });
    const fullText = acceptedSection.getByRole('checkbox', { name: 'Show full document text', exact: true });
    assert.equal(await fullText.isChecked(), false);
    await fullText.check();
    assert.equal(await acceptedSection.locator('pre.document-text').innerText(), initialPRD);
    await page.evaluate(() => refreshState());
    assert.equal(await fullText.isChecked(), true, 'polling preserves full-text disclosure');
    await fullText.uncheck();
    assert.match(await page.locator('#persona-help').textContent(), /Attribution only, not sign-in or authorization/);
    assert.equal(await page.locator('#role').getAttribute('aria-describedby'), 'persona-help');
    assert.match(await page.locator('#agents .speaker-writer').innerText(), /draft-prd/);
    assert.match(await page.locator('#agents .speaker-reviewer').innerText(), /review-accepted-prd/);
    const speakerNames = await page.evaluate(() => ['#people .speaker-gm .nav-name', '#people .speaker-coach .nav-name', '#people .speaker-owner .nav-name', '#agents .speaker-writer .nav-name', '#agents .speaker-reviewer .nav-name'].map(selector => document.querySelector(selector).textContent));
    assert.deepEqual(speakerNames, ['Morgan', 'Casey', 'Avery', 'PRD writer', 'Independent reviewer']);
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok((await page.locator('.topbar').boundingBox()).height <= 100, 'default 390px header must stay within 100px');
    await page.setViewportSize({ width: 1600, height: 1000 });
    await page.locator('#role').selectOption('GM');
    await page.waitForFunction(() => document.querySelector('#role').value === 'GM' && !document.querySelector('#generic-run-submit').disabled);
    await page.locator('#generic-agent').selectOption('writer');
    await page.locator('#generic-request').fill('Draft a fictional pilot PRD and announcement; identify missing evidence.');
    await page.locator('#generic-run-submit').click();
    await page.waitForFunction(async () => (await (await fetch('/api/state')).json()).runs.some(r => r.status === 'proposed'));
    await page.locator('#document-panel .proposal-banner.pending .badge.pending').waitFor();
    assert.equal(await page.locator('.message.speaker-gm h3').innerText(), 'Morgan');
    assert.equal(await page.locator('#conversation-content .message:not([data-conversation-id])').filter({ has: page.getByRole('heading', { name: 'PRD writer', exact: true }) }).locator('h3').innerText(), 'PRD writer');
    assert.equal(await page.locator('.message.speaker-gm .message-avatar').innerText(), 'GM');
    assert.equal(await page.locator('#document-panel .proposal-banner.pending .badge.pending').innerText(), 'Awaiting your approval');
    await page.locator('#document-panel .scope-detail > summary').click();
    assert.match(await page.locator('.proposal-scope').innerText(), /Accept saves all affected documents together.*Reject leaves accepted documents unchanged/is);
    const documentEvidence = page.locator('#document-evidence details.evidence').filter({ has: page.getByRole('heading', { name: 'Proposal provenance and input revisions', exact: true, includeHidden: true }) });
    assert.equal(await documentEvidence.getAttribute('open'), null);
    assert.equal(await page.locator('#document-panel').getByRole('heading', { name: 'Diff against proposal base', exact: true }).isVisible(), true, 'generic full diff stays with document bytes');
    await page.locator('#document-panel').getByRole('button', { name: 'Evidence', exact: true }).first().click();
    await page.locator('#document-evidence').getByRole('heading', { name: 'Proposal provenance and input revisions', exact: true }).waitFor();
    assert.equal(await page.locator('#document-evidence .badge.finished').innerText(), 'Finished');
    assert.ok(await page.locator('.diff-added').count());
    const writerState = await state();
    const first = writerState.runs.find(r => r.agent === 'writer');
    const writerProposal = writerState.proposals.find(p => p.id === first.proposal);
    for (const path of Object.keys(writerProposal.changes)) assert.equal(await page.locator(`#document-panel button[data-focus-key="affected-${writerProposal.id}-${path}"]`).count(), 1, `scope includes ${path}`);
    for (const id of [writerProposal.id, writerProposal.commit, writerProposal.base]) assert.ok((await page.locator('#document-evidence .proposal-identity').innerText()).includes(id.slice(0, 12)), 'visible immutable identity');
    for (const width of [320, 390]) {
      await page.setViewportSize({ width, height: 844 });
      const more = page.locator('#document-panel .proposal-menu > summary');
      await more.click();
      const menuBounds = await page.locator('#document-panel .proposal-menu .menu').boundingBox();
      assert.ok(menuBounds.x >= 0 && menuBounds.x + menuBounds.width <= width, `proposal menu stays within ${width}px`);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      await more.click();
    }
    // Review every full replacement through its scoped document route, not publication actions.
    for (const [width, height, bothPanels] of [[1600, 1000, false], [390, 844, false], [320, 568, false], [1280, 600, true], [1024, 600, true]]) {
      await page.setViewportSize({ width, height });
      if (bothPanels && await evidenceToggle.getAttribute('aria-expanded') !== 'true') await evidenceToggle.click();
      for (const [path, content] of Object.entries(writerProposal.changes)) {
        await page.locator('#conversation-content').getByRole('button', { name: 'View proposal', exact: true }).click();
        const scope = page.locator('#document-panel .scope-detail');
        if (await scope.getAttribute('open') === null) await scope.locator('summary').click();
        await page.locator(`#document-panel button[data-focus-key="affected-${writerProposal.id}-${path}"]`).click();
        await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
        assert.equal(await page.locator('#document-panel .pending-content pre.document-text').innerText(), content);
        assert.equal(await page.evaluate(() => selectedRun()?.id), first.id, 'full replacement route keeps original recorded attempt');
      }
    }
    await page.setViewportSize({ width: 1600, height: 1000 });
    await page.locator('#document-panel .proposal-scope').getByRole('button', { name: /^Review / }).filter({ hasText: 'Proposed PRD' }).click();
    await page.getByRole('tab', { name: 'Fictional project brief', exact: true }).click();
    await navigationToggle.click();
    await page.locator('#evidence-close').click();
    assert.equal(await page.locator('#navigation').isVisible(), false);
    assert.equal(await page.locator('#evidence-panel').isVisible(), false);
    const pendingReview = page.locator(`#request-strip button[data-focus-key="request-proposal-${writerProposal.id}"]`);
    assert.equal(await pendingReview.isVisible(), true);
    await pendingReview.click();
    assert.equal(await page.locator('#review').evaluate(node => node === document.activeElement), true);
    await page.locator('button.accept').waitFor();
    assert.equal(await page.locator('button.accept').isVisible(), true);
    // Return links on the same selection must reset the inner document scroller,
    // while ordinary state refreshes retain the full-text reading position.
    for (const route of [page.locator('#document-panel .proposal-scope').getByRole('button', { name: /^Review / }).filter({ hasText: 'Proposed PRD' }), page.locator('#conversation-content').getByRole('button', { name: 'View proposal', exact: true })]) {
      await page.locator('#document-panel .proposal-scope').getByRole('button', { name: /^Review / }).filter({ hasText: 'Simulated announcement' }).click();
      await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
      assert.equal(await page.locator('#document-panel .pending-content pre.document-text').innerText(), writerProposal.changes['announcement.md']);
      await page.locator('#document-panel').evaluate(node => { node.scrollTop = node.scrollHeight; });
      const readingScroll = await page.locator('#document-panel').evaluate(node => node.scrollTop);
      assert.ok(readingScroll > 0, 'fixture must be reading below the decision header');
      await page.evaluate(() => refreshState());
      assert.equal(await page.locator('#document-panel').evaluate(node => node.scrollTop), readingScroll, 'polling must preserve document reading position');
      await route.click();
      await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
      assert.equal(await page.locator('#document-panel').evaluate(node => node.scrollTop), 0, 'explicit same-context proposal route restores the approval header');
      assert.equal(await page.locator('#document-panel button.accept').evaluate(node => {
        const rect = node.getBoundingClientRect();
        return node.contains(document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2));
      }), true, 'returned approval action is hit-testable');
      assert.equal(await page.locator('#review').evaluate(node => node === document.activeElement), true);
    }
    await navigationToggle.click();
    await page.locator('#document-panel').getByRole('button', { name: 'Evidence', exact: true }).first().click();
    assert.equal(first.model, 'qwen3:8b', 'observed metadata must come from the deterministic provider event');
    assert.ok(first.activity.some(step => step.kind === 'model-requested' && step.at));
    assert.ok(first.activity.some(step => step.kind === 'finished' && step.at));
    assert.match(await page.locator('#document-evidence .decision-meta').innerText(), /qwen3:8b/);
    assert.equal(first.evaluation, 'not verified', 'an observed authored model name does not qualify the suite');
    assert.match(await documentEvidence.locator('.decision-activity').last().innerText(), /Inputs checkpointed.*Execution started.*Model request recorded.*Output received.*Proposal published.*Finished/s);
    assert.deepEqual(Object.keys(writerProposal.changes).sort(), ['announcement.md', 'prd.json']);
    assert.notEqual(writerProposal.changes['prd.json'], initialPRD);
    const documentReview = await page.evaluate(async proposal => (await (await fetch(`/api/document?path=prd.json&proposal=${encodeURIComponent(proposal)}`)).json()), writerProposal.id);
    assert.equal(documentReview.proposed, writerProposal.changes['prd.json']);
    const conversationEvidence = page.locator('#conversation-evidence details.evidence').filter({ has: page.getByRole('heading', { name: 'Recorded run', exact: true, includeHidden: true }) });
    assert.equal(await conversationEvidence.getAttribute('open'), null);
    assert.equal(await page.locator('#conversation-meta').innerText().then(text => text.includes(first.session)), false);
    await page.locator('#conversation-content .message:not([data-conversation-id])').getByRole('button', { name: 'Evidence', exact: true }).click();
    await conversationEvidence.getByRole('button', { name: 'Verify replay & reconstruction', exact: true }).click();
    await conversationEvidence.locator(`#proof-run-${first.id}`).getByText('Model calls during reconstruction', { exact: true }).waitFor();
    assert.equal((await state()).runs.find(r => r.id === first.id).proof.modelCalls, 0);
    assert.notEqual(await conversationEvidence.getAttribute('open'), null);
    await documentEvidence.getByRole('button', { name: 'Replay verified · open saved proof', exact: true }).waitFor({ state: 'attached' });
    await page.locator('#evidence-close').click();
    const proofPosts = [];
    const recordProofPost = request => { if (request.method() === 'POST') proofPosts.push(request.url()); };
    page.on('request', recordProofPost);
    await evidenceToggle.click();
    if (await documentEvidence.getAttribute('open') === null) await documentEvidence.locator('summary').click();
    await documentEvidence.getByRole('button', { name: 'Replay verified · open saved proof', exact: true }).click();
    assert.equal(await page.locator('#evidence-panel').isVisible(), true);
    assert.notEqual(await documentEvidence.getAttribute('open'), null);
    const proofRegion = documentEvidence.getByRole('region', { name: 'Recorded replay integrity result', exact: true });
    assert.equal(await proofRegion.evaluate(node => node === document.activeElement), true, 'one-click proof route focuses the actual integrity result');
    const proofBounds = await documentEvidence.locator('.proof-list').boundingBox();
    const evidenceBounds = await page.locator('#evidence-panel').boundingBox();
    assert.ok(proofBounds.y >= evidenceBounds.y && proofBounds.y + proofBounds.height <= evidenceBounds.y + evidenceBounds.height, 'saved integrity checks must be within the evidence viewport after one click');
    assert.match(await documentEvidence.locator('.proof-list').innerText(), /Journal chain.*Verified.*Model calls during reconstruction\s+0/is);
    assert.match(await documentEvidence.locator('.decision-activity').last().innerText(), /Full recorded chronology.*Finished/is);
    assert.match(await documentEvidence.innerText(), new RegExp(first.session));
    assert.deepEqual(proofPosts, [], 'opening saved proof must not POST or call verification');
    page.off('request', recordProofPost);
    await documentEvidence.locator('summary').click();
    // Real room POSTs persist attribution, but cannot alter proposals or accepted documents.
    await page.locator('#role').selectOption('coach');
    await page.waitForFunction(() => document.querySelector('#role').value === 'coach' && !document.querySelector('#room-post').disabled);
    await page.locator('#room-request').fill('Coach room note: keep the recorded evidence visible.');
    await page.getByRole('button', { name: 'Post to room', exact: true }).click();
    await page.waitForFunction(async () => (await (await fetch('/api/state')).json()).conversation.some(entry => entry.kind === 'comment' && entry.actor === 'coach'));
    await page.waitForFunction(() => !document.querySelector('#room-post').disabled);
    const coachComment = (await state()).conversation.find(entry => entry.kind === 'comment' && entry.actor === 'coach');
    assert.equal(coachComment.text, 'Coach room note: keep the recorded evidence visible.');
    await page.locator(`.message[data-conversation-id="${coachComment.id}"]`).waitFor();
    assert.equal(await page.locator(`.message[data-conversation-id="${coachComment.id}"] h3`).innerText(), 'Casey');
    assert.equal(await page.locator(`.message[data-conversation-id="${coachComment.id}"] time`).getAttribute('datetime'), coachComment.at);
    await newestVisible();
    await page.locator('#conversation-content').evaluate(node => { node.scrollTop = 0; });
    await page.waitForTimeout(1100);
    assert.equal(await page.locator('#conversation-content').evaluate(node => node.scrollTop), 0, 'unchanged polling must preserve historical reading position');
    await page.locator('#role').selectOption('owner');
    await page.waitForFunction(() => document.querySelector('#role').value === 'owner' && !document.querySelector('#room-post').disabled);
    await page.locator('#room-request').fill('Owner room note: <script>inert text</script>');
    await page.getByRole('button', { name: 'Post to room', exact: true }).click();
    await page.waitForFunction(async () => (await (await fetch('/api/state')).json()).conversation.some(entry => entry.kind === 'comment' && entry.actor === 'owner'));
    const ownerComment = (await state()).conversation.find(entry => entry.kind === 'comment' && entry.actor === 'owner');
    await page.reload();
    await page.locator(`.message[data-conversation-id="${ownerComment.id}"]`).waitFor();
    assert.equal(await page.locator(`.message[data-conversation-id="${ownerComment.id}"] h3`).innerText(), 'Avery');
    assert.equal(await page.locator(`.message[data-conversation-id="${coachComment.id}"] h3`).innerText(), 'Casey');
    assert.equal(await page.locator(`.message[data-conversation-id="${ownerComment.id}"] script`).count(), 0);
    assert.match(await page.locator(`.message[data-conversation-id="${ownerComment.id}"]`).innerText(), /<script>inert text<\/script>/);
    const commentedState = await state();
    assert.equal(commentedState.head, writerState.head);
    assert.deepEqual(commentedState.proposals, writerState.proposals);
    assert.equal(commentedState.runs.length, writerState.runs.length);
    assert.ok(commentedState.conversation.some(entry => entry.id === coachComment.id));
    assert.ok(commentedState.conversation.some(entry => entry.id === ownerComment.id));
    await navigationToggle.click();
    await page.locator(`#sessions button[data-focus-key="run-${first.id}"]`).click();
    assert.equal(first.questionsSupported, true, 'browser fixture must record a profile-3 session binding');
    const beforeQuestion = await state();
    await page.locator('#room-request').fill('Why retain the recorded PRD proposal?');
    await page.waitForFunction(() => !document.querySelector('#room-ask').disabled);
    await page.getByRole('button', { name: 'Ask agent why', exact: true }).click();
    await page.locator('.message[data-conversation-id] .badge.answered').waitFor();
    const afterQuestion = await state();
    const question = afterQuestion.conversation.find(entry => entry.kind === 'question');
    assert.equal(question.actor, 'owner');
    assert.equal(question.run, first.id);
    assert.equal(question.session, first.session, 'answer must use the same recorded session UID');
    assert.equal(question.agent, first.agent);
    assert.ok(question.answer?.trim());
    assert.equal(afterQuestion.head, beforeQuestion.head);
    assert.deepEqual(afterQuestion.proposals, beforeQuestion.proposals);
    assert.equal(afterQuestion.history.length, beforeQuestion.history.length);
    assert.equal(afterQuestion.runs.length, beforeQuestion.runs.length);
    assert.equal(afterQuestion.runs.find(run => run.id === first.id).model, first.model);
    assert.ok(afterQuestion.runs.find(run => run.id === first.id).proof == null, 'question must invalidate raw-source proof');
    await page.locator(`.message[data-conversation-id="${question.id}"] .badge.answered`).waitFor();
    await newestVisible();
    await page.waitForFunction(() => document.querySelector('#notice').hidden, undefined, { timeout: 3000 });
    assert.equal(await page.locator('#document-evidence').getByText('Replay verified · open saved proof', { exact: true }).count(), 0, 'cached pre-question proof must not appear current');
    assert.match(await page.locator(`.message[data-focus-key="question-answer-${question.id}"]`).innerText(), /answering a question · read-only/);
    assert.equal(await page.locator('#room-request').inputValue(), '');
    await reviewAccessibility(browser, base, afterQuestion, question);
    await reviewRefreshGuards(browser, base, afterQuestion, question);
    // Delay state delivery in a second view: updated results need not be the final DOM row.
    const historyPage = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
    try {
      const historyState = structuredClone(afterQuestion);
      const historicalRun = historyState.runs.find(run => run.id === first.id);
      historicalRun.status = 'running';
      const historicalQuestion = historyState.conversation.find(entry => entry.id === question.id);
      historicalQuestion.status = 'running';
      delete historicalQuestion.answer;
      for (let index = 0; index < 12; index++) historyState.conversation.push({
        id: `later-comment-${index}`, kind: 'comment', actor: 'coach',
        text: `Later fictional room comment ${index}: continue reviewing the recorded draft evidence.`,
        at: new Date(Date.parse(question.at) + index + 1).toISOString(),
      });
      await historyPage.route('**/api/state', route => route.fulfill({ json: historyState }));
      await historyPage.goto(base);
      await historyPage.waitForFunction(() => !document.querySelector('#generic-run-submit').disabled);
      assert.equal(await historyPage.locator('#navigation').isVisible(), false);
      assert.equal(await historyPage.locator('#evidence-panel').isVisible(), false);
      assert.equal(await historyPage.locator('#request-strip li').count(), 3, 'actual proposal, active agent run and pending question, never later comments');
      assert.equal(await historyPage.locator('#request-strip button').count(), 2, 'selected proposal needs no redundant review link');
      await historyPage.locator(`#request-strip button[data-focus-key="request-question-${question.id}"]`).click();
      assert.equal(await historyPage.locator(`.message[data-conversation-id="${question.id}"]`).last().evaluate(node => node === document.activeElement), true);
      await historyPage.getByRole('button', { name: 'Navigation', exact: true }).click();
      await historyPage.locator(`#sessions button[data-focus-key="run-${first.id}"]`).click();
      // Native identity checks: a .focus() call on the DOM double cannot catch detached-node focus.
      for (const selector of ['#conversation-content .message:not([data-conversation-id]) button.evidence-link', `.message[data-conversation-id="${question.id}"] button.evidence-link`]) {
        const launcher = historyPage.locator(selector).last();
        await launcher.focus();
        historicalQuestion.seconds = (historicalQuestion.seconds || 0) + 1;
        await historyPage.evaluate(() => refreshState());
        assert.equal(await launcher.evaluate(node => node === document.activeElement), true, 'refresh must focus the attached replacement Evidence/Source launcher');
      }
      await historyPage.locator(`#request-strip button[data-focus-key="request-question-${question.id}"]`).click();
      const questionDestination = historyPage.locator(`.message[data-conversation-id="${question.id}"]`).last();
      for (const status of ['queued', 'running', 'answered']) {
        Object.assign(historicalQuestion, { status, seconds: (historicalQuestion.seconds || 0) + 1, answer: status === 'answered' ? question.answer : undefined });
        await historyPage.evaluate(() => refreshState());
        assert.equal(await questionDestination.evaluate(node => node === document.activeElement), true, `question shortcut destination retains native focus through ${status}`);
      }
      historicalQuestion.status = 'running'; delete historicalQuestion.answer;
      await historyPage.evaluate(() => refreshState());
      const visibleResult = async locator => {
        const position = await locator.evaluate(node => {
          const pane = document.querySelector('#conversation-content').getBoundingClientRect();
          // Generic result cards include status/actions and can exceed the pane.
          // The actual changed summary/answer, not the entire card, must be exposed.
          const result = node.querySelector('.message-body > p').getBoundingClientRect();
          const row = node.getBoundingClientRect();
          const scroller = document.querySelector('#conversation-content');
          return { top: pane.top, bottom: pane.bottom, rowTop: row.top, resultTop: result.top, resultBottom: result.bottom, scrollTop: scroller.scrollTop, scrollHeight: scroller.scrollHeight, clientHeight: scroller.clientHeight, text: node.querySelector('.message-context')?.textContent };
        });
        assert.ok(position.resultTop >= position.top - 1 && position.resultBottom <= position.bottom + 1, `updated summary/answer must be fully revealed inside the conversation pane: ${JSON.stringify(position)}`);
      };
      historicalRun.status = 'proposed';
      historicalRun.summary = 'Fresh fictional writer result after later room comments.';
      await historyPage.locator('#conversation-content').getByText(historicalRun.summary, { exact: true }).waitFor();
      await visibleResult(historyPage.locator('#conversation-content > .message:not([data-conversation-id])').last());
      Object.assign(historicalQuestion, question);
      await historyPage.locator(`.message[data-conversation-id="${question.id}"] .badge.answered`).waitFor();
      await visibleResult(historyPage.locator(`.message[data-conversation-id="${question.id}"]`).last());
      await historyPage.locator('#document-panel button.evidence-link').first().click();
      const priorAudit = await historyPage.locator('#document-evidence details').evaluateAll(nodes => nodes.map(node => node.id));
      await historyPage.route('**/api/document*', route => new URL(route.request().url()).searchParams.get('path') === 'brief.json'
        ? route.fulfill({ status: 503, json: { error: 'Simulated document outage' } }) : route.continue());
      await historyPage.getByRole('tab', { name: 'Fictional project brief', exact: true }).click();
      await historyPage.getByRole('button', { name: 'Retry document', exact: true }).waitFor();
      assert.match(await historyPage.locator('#document-evidence').innerText(), /Evidence unavailable.*Fictional project brief/);
      for (const id of priorAudit) assert.equal(await historyPage.locator('#document-evidence').evaluate((node, id) => Array.from(node.querySelectorAll('details')).some(detail => detail.id === id), id), false, 'failed switch cannot retain the prior document audit');
      assert.equal(await historyPage.locator('#conversation-evidence details').filter({ has: historyPage.getByRole('heading', { name: 'Recorded run', exact: true, includeHidden: true }) }).count(), 1);
    } finally { await historyPage.close(); }
    await conversationEvidence.getByText('Evidence', { exact: true }).click();
    await conversationEvidence.getByRole('button', { name: 'Verify replay & reconstruction', exact: true }).click();
    await page.locator('#document-evidence').getByText('Replay verified · open saved proof', { exact: true }).waitFor({ state: 'attached' });
    const questionProof = (await state()).runs.find(run => run.id === first.id).proof;
    assert.equal(questionProof.modelCalls, 0);
    assert.ok(questionProof.records > beforeQuestion.runs.find(run => run.id === first.id).proof.records, 'verification must cover the augmented raw journal');
    // A routed view supplies multiple real-shaped proposal associations without invoking a model.
    const selectionPage = await browser.newPage({ viewport: { width: 390, height: 844 } });
    try {
      const selectionState = structuredClone(await state());
      const alternate = { ...selectionState.runs[0], id: 'alternate-attempt', session: 'alternate-session', proposal: 'alternate-proposal' };
      const alternateProposal = { ...selectionState.proposals[0], id: 'alternate-proposal', attribution: { ...selectionState.proposals[0].attribution, run: alternate.id } };
      const missingProposal = { ...alternateProposal, id: 'missing-association', status: 'stale', attribution: { ...alternateProposal.attribution, run: 'missing-attempt' } };
      selectionState.runs.push(alternate);
      selectionState.proposals.push(alternateProposal, missingProposal);
      const selectionPosts = [];
      selectionPage.on('request', request => { if (request.method() === 'POST') selectionPosts.push(request.url()); });
      await selectionPage.route('**/api/state', route => route.fulfill({ json: selectionState }));
      await selectionPage.route('**/api/document*', route => route.fulfill({ json: documentReview }));
      await selectionPage.goto(base);
      await selectionPage.locator('#navigation-toggle').click();
      await selectionPage.locator('#proposal-select').waitFor();
      await selectionPage.locator('#room-request').fill('Selection draft stays untouched');
      await selectionPage.locator('#proposal-select').selectOption(first.proposal);
      await selectionPage.waitForFunction(id => selectedRun()?.id === id, first.id);
      assert.match(await selectionPage.locator('#room-context').innerText(), /Ask PRD writer about.*on this recorded session, read-only/is);
      assert.ok((await selectionPage.locator('#conversation-meta').innerText()).includes('Draft a fictional pilot PRD and announcement; identify missing evidence.'), 'selected conversation identifies its original request, not another attempt');
      assert.equal(await selectionPage.locator('#room-request').inputValue(), 'Selection draft stays untouched');
      assert.equal(await selectionPage.locator('#document-panel .proposal-banner.pending .badge.pending').innerText(), 'Awaiting your approval');
      await selectionPage.locator('#document-panel button.evidence-link').first().click();
      const selectedProvenance = JSON.parse(await selectionPage.locator('#document-evidence .evidence-record').filter({ has: selectionPage.getByRole('heading', { name: 'Proposal provenance and input revisions', exact: true }) }).locator('pre').innerText());
      assert.equal(selectedProvenance.executionStatus, 'proposed');
      assert.equal(selectedProvenance.attribution.run, first.id, 'Evidence retains the recorded originating attempt, not an assumed source');
      await selectionPage.locator('#document-evidence').getByRole('button', { name: 'Replay verified · open saved proof', exact: true }).click();
      const phoneProof = selectionPage.locator('#document-evidence').getByRole('region', { name: 'Recorded replay integrity result', exact: true });
      assert.equal(await phoneProof.evaluate(node => node === document.activeElement), true);
      const checks = await phoneProof.locator('.proof-list').boundingBox();
      assert.ok(checks.y >= 0 && checks.y + checks.height <= 844, 'one click reveals saved integrity checks in the phone viewport');
      assert.match(await selectionPage.locator('#document-evidence').innerText(), new RegExp(first.session));
      assert.equal(await selectionPage.locator('#proposal-select').inputValue(), first.proposal);
      // A proof-only run refresh preserves focus on the attached result, not a detached record.
      selectionState.runs[0].seconds += 1;
      await selectionPage.evaluate(() => refreshState());
      await selectionPage.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
      assert.equal(await phoneProof.evaluate(node => node === document.activeElement), true);
      await selectionPage.locator('#evidence-close').click();
      await selectionPage.locator('#proposal-select').selectOption('missing-association');
      await selectionPage.waitForFunction(() => document.querySelector('#room-context').textContent.includes('Originating attempt not recorded'));
      assert.equal(await selectionPage.locator('#room-ask').isDisabled(), true);
      await selectionPage.locator('#document-panel .review-actions').scrollIntoViewIfNeeded();
      assert.equal(await selectionPage.locator('#document-panel .review-actions').getByRole('button', { name: 'Reject', exact: true }).evaluate(node => {
        const rect = node.getBoundingClientRect();
        return node.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2));
      }), true, 'stale generic review actions remain reachable on phone');
      assert.equal(await selectionPage.locator('#document-panel button.accept').isDisabled(), true);
      assert.equal(await selectionPage.locator('#document-panel .review-actions').getByRole('button', { name: 'Reject', exact: true }).isEnabled(), true);
      await selectionPage.locator('#proposal-select').selectOption('');
      await selectionPage.waitForFunction(() => document.querySelector('#room-context').textContent.includes('Accepted content only'));
      assert.equal(await selectionPage.locator('#room-ask').isDisabled(), true);
      assert.equal(await selectionPage.locator('#room-request').inputValue(), 'Selection draft stays untouched');
      assert.deepEqual(selectionPosts, [], 'selecting history and saved proof has no POST or model call');
    } finally { await selectionPage.close(); }
    for (const role of ['coach', 'owner']) {
      await page.locator('#role').selectOption(role);
      await page.waitForFunction(role => document.querySelector('#role').value === role && !document.querySelector('#room-post').disabled, role);
      const sourceLink = page.locator(`.message[data-conversation-id="${question.id}"]`).last().getByRole('button', { name: 'Source', exact: true });
      await sourceLink.click();
      const target = (await sourceLink.getAttribute('aria-controls')).split(' ')[1];
      assert.equal(await page.locator(`#${target}`).count(), 1, 'each refreshed source link targets one attached audit record');
      assert.match(await page.locator(`#${target}`).innerText(), new RegExp(first.session));
    }
    await page.locator('#role').selectOption('GM');
    await page.waitForFunction(() => document.querySelector('#role').value === 'GM' && !document.querySelector('.accept').disabled);
    await page.locator('#document-panel button.accept').click();
    await page.waitForFunction(async () => (await (await fetch('/api/state')).json()).proposals.some(p => p.status === 'accepted'));
    await page.locator(`#sessions button[data-focus-key="run-${first.id}"] .nav-detail`).getByText(/accepted/).waitFor();
    assert.match(await page.locator('#agents .speaker-writer .nav-detail').first().innerText(), /accepted/);
    assert.match(await page.locator('#conversation-meta').innerText(), /accepted/);
    assert.equal(await page.locator('.message:not([data-conversation-id]) .badge.accepted').count(), 1);
    assert.equal((await state()).runs.find(run => run.id === first.id).status, 'proposed', 'review never rewrites recorded execution');
    for (const [path, content] of Object.entries(writerProposal.changes)) {
      const accepted = await page.evaluate(async path => (await (await fetch(`/api/document?path=${encodeURIComponent(path)}`)).json()).accepted, path);
      assert.equal(accepted, content, 'Accept saves every replacement byte');
    }
    // Two real writer attempts share one base. Accepting either must stale the other.
    const requestWriter = async request => {
      await page.locator('#new-menu > summary').click();
      await page.locator('#generic-run-toggle').click();
      await page.locator('#generic-agent').selectOption('writer');
      await page.locator('#generic-request').fill(request);
      const before = (await state()).runs.map(run => run.id);
      const [response] = await Promise.all([
        page.waitForResponse(response => new URL(response.url()).pathname === '/api/run' && response.request().method() === 'POST'),
        page.locator('#generic-run-submit').click(),
      ]);
      assert.equal(response.status(), 202, 'manual generic run request is recorded');
      const { run: id } = await response.json();
      assert.ok(id && !before.includes(id), 'request returns a new recorded attempt');
      await page.locator(`#sessions button[data-focus-key="run-${id}"] .nav-detail`).getByText(/^proposed/).waitFor({ state: 'attached' });
      const after = await state();
      const created = after.runs.find(run => run.id === id);
      assert.ok(created, `manual request must retain its newly recorded run: ${JSON.stringify({ request, before, after: after.runs.map(run => ({ id: run.id, status: run.status, request: run.requestSummary })) })}`);
      assert.equal(created.status, 'proposed');
      assert.ok(created.proposal, 'completed writer records its proposal identity');
      return created;
    };
    const competing = await requestWriter('Draft a competing fictional proposal.');
    await requestWriter('Draft another fictional proposal on the same accepted base.');
    await page.locator('#document-panel button.accept').click();
    await page.waitForFunction(async id => (await (await fetch('/api/state')).json()).proposals.some(p => p.id === id && p.status === 'stale'), competing.proposal);
    await page.locator(`#sessions button[data-focus-key="run-${competing.id}"]`).click();
    await page.locator('#document-panel .proposal-banner.stale').waitFor();
    assert.equal(await page.locator('#document-panel button.accept').isEnabled(), false);
    assert.equal(await page.locator('#document-panel').getByRole('button', { name: 'Reject', exact: true }).isEnabled(), true);
    const staleState = await state();
    const originalStale = staleState.proposals.find(p => p.id === competing.proposal);
    const [regenerationResponse] = await Promise.all([
      page.waitForResponse(response => new URL(response.url()).pathname === '/api/regenerate' && response.request().method() === 'POST'),
      page.locator('#document-panel').getByRole('button', { name: 'Try again', exact: true }).click(),
    ]);
    assert.equal(regenerationResponse.ok(), true);
    const { run: regeneratedID } = await regenerationResponse.json();
    await page.locator(`#sessions button[data-focus-key="run-${regeneratedID}"] .nav-detail`).getByText(/^proposed/).waitFor({ state: 'attached' });
    const regeneratedState = await state();
    const regenerated = regeneratedState.runs.find(r => r.previousAttempt === competing.id);
    assert.notEqual(regenerated.proposal, competing.proposal);
    assert.deepEqual(regeneratedState.runs.find(r => r.id === competing.id), staleState.runs.find(r => r.id === competing.id));
    assert.deepEqual(regeneratedState.proposals.find(p => p.id === competing.proposal), originalStale);
    await page.locator('#document-panel').getByRole('button', { name: 'Reject', exact: true }).click();
    await page.locator(`#sessions button[data-focus-key="run-${regenerated.id}"] .nav-detail`).getByText(/^rejected/).waitFor({ state: 'attached' });
    assert.match(await page.locator('#conversation-meta').innerText(), /rejected/);
    assert.equal((await state()).runs.find(r => r.id === regenerated.id).status, 'proposed');
    // Saving a declared source exercises the native file-added watcher, not a legacy domain endpoint.
    await page.locator('#new-menu > summary').click();
    await page.locator('#generic-input-toggle').click();
    await page.locator('#generic-input-path').fill('feedback/browser.json');
    await page.locator('#generic-input-content').fill('{"team":"Northwind","feedback":"Fictional browser observation for a fresh PRD."}');
    await page.locator('#generic-input-submit').click();
    await page.locator('#sessions button .dot.proposed').waitFor({ state: 'attached' });
    const inputState = await state();
    const inputRun = inputState.runs.find(r => r.trigger === 'new-feedback');
    await page.locator(`#sessions button[data-focus-key="run-${inputRun.id}"]`).click();
    await page.locator('#document-panel .proposal-banner.pending').waitFor();
    const inputProposal = (await state()).proposals.find(p => p.id === inputRun.proposal);
    assert.deepEqual(Object.keys(inputProposal.changes).sort(), ['announcement.md', 'prd.json']);
    assert.notEqual(inputProposal.changes['prd.json'], writerProposal.changes['prd.json'], 'repeat attempts have visibly changed replacements');
    assert.equal(inputRun.model, 'qwen3:8b');
    assert.equal(await page.locator('#announcement-panel').isVisible(), true, 'native publication panel participates in viewport checks');
    // Shared geometry: generic actions precede complete document bytes and remain
    // naturally scroll-reachable without overlays, including the publication panel.
    for (const [width, height] of [[1600, 1000], [1280, 720], [1280, 600], [1024, 600], [960, 844], [390, 844], [320, 568]]) {
      await page.setViewportSize({ width, height });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${width}x${height} generic workspace does not overflow`);
      assert.equal(await page.locator('.focus-room').evaluate(node => node.scrollHeight <= node.clientHeight || getComputedStyle(node).overflowY === 'auto'), true, 'short workspace scrolls rather than clipping');
      await page.locator('#room-request').fill('Short-height draft');
      await page.locator('#room-post').scrollIntoViewIfNeeded();
      assert.equal(await page.locator('#room-post').evaluate(node => {
        const rect = node.getBoundingClientRect();
        return node.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2));
      }), true, 'room action remains hit-testable with publication panel present');
      await page.locator('#conversation-content').getByRole('button', { name: 'View proposal', exact: true }).click();
      await page.waitForFunction(() => document.querySelector('#document-panel').getAttribute('aria-busy') === 'false');
      const accept = page.locator('#document-panel button.accept');
      await accept.scrollIntoViewIfNeeded();
      assert.equal(await accept.isEnabled(), true);
      assert.equal(await accept.evaluate(node => {
        const rect = node.getBoundingClientRect();
        const replacement = document.querySelector('#document-panel .pending-content');
        return node.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2))
          && Boolean(node.compareDocumentPosition(replacement) & Node.DOCUMENT_POSITION_FOLLOWING);
      }), true, 'approval precedes full replacement bytes and is not obscured');
      assert.ok((await page.locator('#review').boundingBox()).y < (await page.locator('#conversation').boundingBox()).y, 'review precedes conversation');
      const box = await accept.boundingBox();
      assert.ok(box.width >= 44 && box.height >= 44, 'generic approval has practical touch target');
    }
    await page.setViewportSize({ width: 1600, height: 1000 });
    await page.locator('#document-panel button.accept').click();
    await page.locator(`#sessions button[data-focus-key="run-${inputRun.id}"] .nav-detail`).getByText(/^accepted/).waitFor({ state: 'attached' });
    const final = await state();
    assert.ok(final.documents.includes('feedback/browser.json'));
    assert.ok(final.documents.includes('announcement.md'));
    assert.equal(final.runs.find(r => r.id === inputRun.id).status, 'proposed');
    for (const [path, content] of Object.entries(inputProposal.changes)) {
      assert.equal(await page.evaluate(async path => (await (await fetch(`/api/document?path=${encodeURIComponent(path)}`)).json()).accepted, path), content);
    }
    assert.match(await page.locator(`#sessions button[data-focus-key="run-${regenerated.id}"] .nav-detail`).innerText(), /^rejected/, 'rejected history keeps its terminal decision label');
    const originalReadback = await page.evaluate(async proposal => (await (await fetch(`/api/document?path=prd.json&proposal=${encodeURIComponent(proposal)}`)).json()).proposed, writerProposal.id);
    assert.equal(originalReadback, writerProposal.changes['prd.json'], 'later acceptance never rewrites the original review target');
    await page.screenshot({ path: screenshot('ui-smoke.png'), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: screenshot('ui-mobile.png'), fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    assert.equal(pageErrors.length, 0);
    console.log('Browser smoke passed: native generic definition and visible deterministic provider; whole-scope PRD/announcement review and immutable history; real manual writer, source watcher, stale refusal/regeneration, accept/reject; persona layout/storage/drafts, room attribution/inert text, same-session read-only questions and zero-call replay; held-response authority/proof and keyboard focus/reading; phone/doubled-text/touch/contrast and short-desktop geometry. Deterministic fixture only.');
  } finally {
    signal?.removeEventListener('abort', onAbort);
    await close();
  }
}

async function main({ url = process.argv[2], fixtureOptions = {} } = {}) {
  const controller = new AbortController();
  const { signal } = controller;
  const interrupt = (name, code) => () => {
    const error = new Error(`Browser smoke interrupted by ${name}`);
    error.exitCode = code;
    process.exitCode = code;
    controller.abort(error);
  };
  const onINT = interrupt('SIGINT', 130);
  const onTERM = interrupt('SIGTERM', 143);
  process.on('SIGINT', onINT);
  process.on('SIGTERM', onTERM);
  const interrupted = new Promise((resolve, reject) => signal.addEventListener('abort', () => reject(signal.reason), { once: true }));
  interrupted.catch(() => {});
  let fixture, smokeRun;
  try {
    // Dependency failure must not create a fixture (or its build directory).
    const playwright = loadPlaywright();
    signal.throwIfAborted();
    if (!url) fixture = await startFixture({ ...fixtureOptions, signal });
    smokeRun = smoke(url || fixture.url, playwright, signal);
    await Promise.race([smokeRun, interrupted, ...(fixture ? [fixture.failure] : [])]);
  } catch (error) {
    if (!signal.aborted) controller.abort(error);
    throw signal.reason;
  } finally {
    try {
      await fixture?.stop();
      await smokeRun?.catch(() => {});
    } finally {
      process.off('SIGINT', onINT);
      process.off('SIGTERM', onTERM);
    }
  }
}

// Importing this test-only script must not build, spawn or launch a browser.
module.exports = { startFixture, smoke, main };
if (require.main === module) main().catch(error => {
  if (error.exitCode === 2) console.error(error.message);
  else {
    const boundedReason = /^(?:Browser fixture (?:could not spawn|startup timed out|exited \((?:\d+|null|SIG[A-Z]{1,12})\))|Owned browser fixture did not exit after SIGKILL|Browser smoke interrupted by SIG(?:INT|TERM))$/.test(error.message) ? error.message : 'Browser execution failed';
    console.error('Browser smoke failed:', boundedReason, String(error.stack).split('\n').filter(line => /^\s+at .*browser-smoke\.cjs:\d+:\d+\)?$/.test(line)).slice(0, 5).join('\n'));
  }
  process.exitCode = process.exitCode || error.exitCode || 1;
});
