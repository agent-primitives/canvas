const assert = require('node:assert/strict');
const test = require('node:test');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const script = path.join(__dirname, 'browser-smoke.cjs');
const fixtureSource = `
  const http = require('node:http');
  if (process.env.LIFECYCLE_MODE === 'ignore-term') process.on('SIGTERM', () => {});
  if (process.env.LIFECYCLE_MODE === 'startup-exit') {
    console.error('private-fixture-sentinel'); process.exit(7);
  }
  const server = http.createServer((req, res) => res.end('fixture'));
  server.listen(0, '127.0.0.1', () => {
    const url = 'http://127.0.0.1:' + server.address().port;
    require('node:fs').writeFileSync(process.env.LIFECYCLE_READY, url);
    if (!['timeout', 'signal-startup'].includes(process.env.LIFECYCLE_MODE))
      console.log('Test-only deterministic provider fixture: ' + url);
    if (process.env.LIFECYCLE_MODE === 'unexpected-exit') setTimeout(() => process.exit(7), 100);
  });
`;

// Only the Go build and Playwright boundaries are replaced. All lifecycle code,
// signals, child events, PID termination and temporary directories are real.
function wrapper(mode, scratch, url) {
  return `
    const fs = require('node:fs');
    const path = require('node:path');
    const native = require('node:child_process');
    const Module = require('node:module');
    const original = Module._load;
    const mode = ${JSON.stringify(mode)};
    const scratch = ${JSON.stringify(scratch)};
    const script = ${JSON.stringify(script)};
    let rejectPage;
    const browser = {
      async newPage() {
        fs.writeFileSync(path.join(scratch, 'browser-active'), 'active');
        if (['signal-browser', 'unexpected-exit'].includes(mode))
          return new Promise((resolve, reject) => { rejectPage = reject; });
        throw new Error('private-browser-sentinel');
      },
      async close() {
        fs.writeFileSync(path.join(scratch, 'browser-closed'), 'closed');
        rejectPage?.(new Error('closed'));
      },
    };
    Module._load = function(name, parent, ...rest) {
      if (parent?.filename === script) {
        if (name === 'node:child_process') return {
          execFileSync(command, args) {
            if (command !== 'go') throw new Error('no global dependency');
            const binary = args[args.indexOf('-o') + 1];
            fs.writeFileSync(path.join(scratch, 'metadata'), JSON.stringify({ dir: path.dirname(binary) }));
            if (mode === 'build-fail') throw new Error('private-build-sentinel');
          },
          spawn(binary, args, options) {
            const child = mode === 'spawn-error'
              ? native.spawn(path.join(scratch, 'private-fixture-sentinel'), [], options)
              : native.spawn(process.execPath, ['-e', ${JSON.stringify(fixtureSource)}], {
                ...options, env: { ...options.env, LIFECYCLE_MODE: mode,
                  LIFECYCLE_READY: path.join(scratch, 'ready') },
              });
            const metadata = JSON.parse(fs.readFileSync(path.join(scratch, 'metadata')));
            fs.writeFileSync(path.join(scratch, 'metadata'), JSON.stringify({ ...metadata, pid: child.pid }));
            child.on('exit', (code, signal) => fs.writeFileSync(path.join(scratch, 'exit'), JSON.stringify({ code, signal })));
            return child;
          },
        };
        if (name === 'node:fs') return { ...fs, mkdirSync(dir, options) {
          if (dir !== path.resolve(path.dirname(script), '../.state')) fs.mkdirSync(dir, options);
        } };
        if (name === 'playwright' || name === 'missing-playwright-for-lifecycle') {
          if (['dependency-fail', 'explicit-dependency-fail'].includes(mode)) throw new Error('private-dependency-sentinel');
          return { chromium: { async launch() {
            if (['launch-fail', 'ignore-term', 'external'].includes(mode)) throw new Error('private-browser-sentinel');
            if (mode === 'signal-launch') {
              fs.writeFileSync(path.join(scratch, 'launch-active'), 'active');
              await new Promise(resolve => setTimeout(resolve, 150));
            }
            return browser;
          } } };
        }
      }
      return original.call(this, name, parent, ...rest);
    };
    process.argv[2] = ${JSON.stringify(url)};
    if (mode === 'dependency-fail') delete process.env.PLAYWRIGHT_MODULE;
    else process.env.PLAYWRIGHT_MODULE = 'missing-playwright-for-lifecycle';
    if (['import', 'idempotent', 'timeout'].includes(mode)) {
      const api = require(script);
      if (mode !== 'import' && api.main) {
        const run = mode === 'idempotent'
          ? api.startFixture().then(async fixture => { await fixture.stop(); await fixture.stop(); })
          : api.main({ fixtureOptions: { startupTimeout: 150 } });
        run.catch(error => { console.error('Browser smoke failed:', error.name); process.exitCode = error.exitCode || 1; });
      }
    } else {
      // Execute the real CLI guard and final error reporting as the entry module.
      const entry = new Module(script);
      entry.filename = script;
      entry.paths = Module._nodeModulePaths(path.dirname(script));
      process.mainModule = entry;
      entry._compile(fs.readFileSync(script, 'utf8'), script);
    }
  `;
}

const alive = pid => {
  if (!pid) return false;
  try { process.kill(pid, 0); return true; } catch (error) {
    if (error.code === 'ESRCH') return false;
    throw error;
  }
};

async function waitForFile(file) {
  const deadline = Date.now() + 3000;
  while (!fs.existsSync(file)) {
    if (Date.now() > deadline) throw new Error('test child did not reach expected phase');
    await new Promise(resolve => setTimeout(resolve, 10));
  }
}

async function run(t, mode, { url, signal, phase } = {}) {
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'canvas-lifecycle-test-'));
  const child = spawn(process.execPath, ['-e', wrapper(mode, scratch, url)], { stdio: ['ignore', 'pipe', 'pipe'] });
  const exited = once(child, 'exit');
  let output = '';
  child.stdout.on('data', chunk => { output += chunk; });
  child.stderr.on('data', chunk => { output += chunk; });
  const started = Date.now();
  const metadata = () => fs.existsSync(path.join(scratch, 'metadata'))
    ? JSON.parse(fs.readFileSync(path.join(scratch, 'metadata'))) : {};
  t.after(async () => {
    if (alive(child.pid)) { child.kill('SIGKILL'); await exited; }
    const owned = metadata();
    if (alive(owned.pid)) process.kill(owned.pid, 'SIGKILL');
    if (owned.dir) fs.rmSync(owned.dir, { recursive: true, force: true });
    fs.rmSync(scratch, { recursive: true, force: true });
  });
  const watchdog = setTimeout(() => child.kill('SIGKILL'), 4000);
  try {
    if (signal) {
      await waitForFile(path.join(scratch, phase));
      child.kill(signal);
    }
    const [code, exitSignal] = await exited;
    const owned = metadata();
    assert.equal(exitSignal, null, `CLI must finish itself, not be killed: ${output}`);
    assert.equal(alive(owned.pid), false, 'owned fixture PID must be gone');
    if (owned.dir) assert.equal(fs.existsSync(owned.dir), false, 'fixture build directory must be removed');
    assert.doesNotMatch(output, /private-\w+-sentinel/, 'fixture and dependency details must not leak');
    return { code, elapsed: Date.now() - started, scratch, owned, output };
  } finally {
    clearTimeout(watchdog);
  }
}

for (const mode of ['dependency-fail', 'explicit-dependency-fail']) {
  test(`${mode} never starts an owned fixture`, async t => {
    const result = await run(t, mode);
    assert.equal(result.code, 2);
    assert.equal(result.owned.dir, undefined);
  });
}

test('importing the script does not build or launch anything', async t => {
  const result = await run(t, 'import');
  assert.equal(result.code, 0);
  assert.equal(result.owned.dir, undefined);
  assert.equal(fs.existsSync(path.join(result.scratch, 'browser-active')), false);
});

for (const mode of ['build-fail', 'launch-fail', 'page-fail', 'startup-exit', 'spawn-error', 'timeout', 'unexpected-exit']) {
  test(`${mode} unwinds the owned fixture and build directory`, async t => {
    const result = await run(t, mode);
    assert.equal(result.code, 1);
    if (mode === 'startup-exit' || mode === 'spawn-error' || mode === 'unexpected-exit') {
      assert.match(result.output, /Browser fixture (?:could not spawn|exited \()/);
    }
    if (mode === 'page-fail' || mode === 'unexpected-exit')
      assert.equal(fs.existsSync(path.join(result.scratch, 'browser-closed')), true);
  });
}

test('SIGTERM-ignoring fixture is killed within a bounded shutdown', async t => {
  const result = await run(t, 'ignore-term');
  assert.equal(result.code, 1);
  const exit = JSON.parse(fs.readFileSync(path.join(result.scratch, 'exit')));
  assert.equal(exit.signal, 'SIGKILL');
  assert.ok(result.elapsed < 2500, `shutdown took ${result.elapsed}ms`);
});

test('normal fixture shutdown is idempotent', async t => {
  const result = await run(t, 'idempotent');
  assert.equal(result.code, 0);
});

for (const [signal, code] of [['SIGINT', 130], ['SIGTERM', 143]]) {
  for (const [mode, phase] of [['signal-startup', 'ready'], ['signal-launch', 'launch-active'], ['signal-browser', 'browser-active']]) {
    test(`${signal} during ${mode} cleans up and preserves its exit code`, async t => {
      const result = await run(t, mode, { signal, phase });
      assert.equal(result.code, code);
      if (mode !== 'signal-startup') assert.equal(fs.existsSync(path.join(result.scratch, 'browser-closed')), true);
    });
  }
}

test('external URL failure leaves the foreign fixture running', async t => {
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'canvas-external-test-'));
  const ready = path.join(scratch, 'ready');
  const foreign = spawn(process.execPath, ['-e', fixtureSource], {
    env: { ...process.env, LIFECYCLE_READY: ready }, stdio: 'ignore',
  });
  const exited = once(foreign, 'exit');
  t.after(async () => {
    if (alive(foreign.pid)) { foreign.kill('SIGKILL'); await exited; }
    fs.rmSync(scratch, { recursive: true, force: true });
  });
  await waitForFile(ready);
  const result = await run(t, 'external', { url: fs.readFileSync(ready, 'utf8') });
  assert.equal(result.code, 1);
  assert.equal(result.owned.dir, undefined);
  assert.equal(alive(foreign.pid), true);
});
