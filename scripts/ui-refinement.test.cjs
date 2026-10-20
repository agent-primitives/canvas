const assert = require('node:assert/strict');
const test = require('node:test');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

// Run the real view functions without booting polling or a model/service.
// This small DOM double checks rendered text/state; browser smoke owns native semantics.
class Node {
  constructor(tag = 'div') { this.tagName = tag; this.children = []; this.dataset = {}; this.attributes = {}; this.style = {}; this.listeners = {}; this.open = false; this._text = ''; this.className = ''; this.disabled = false; this.scrollTop = 0; this.scrollHeight = 1000; this.clientTop = 0; this.classList = { add: (...names) => { this.className += ` ${names.join(' ')}`; } }; }
  set textContent(value) { this._text = String(value); this.children = []; }
  get textContent() { return this._text + this.children.map(node => node.textContent).join(''); }
  append(...nodes) { for (const node of nodes) { if (node.parentNode) node.parentNode.children = node.parentNode.children.filter(child => child !== node); node.parentNode = this; this.children.push(node); } }
  replaceChildren(...nodes) { if (this.ownerDocument && this.children.some(node => node.contains(this.ownerDocument.activeElement))) this.ownerDocument.activeElement = this.ownerDocument.body; this.children.forEach(node => { node.parentNode = null; }); this._text = ''; this.children = []; this.append(...nodes); }
  replaceWith(node) { const parent = this.parentNode; parent.children[parent.children.indexOf(this)] = node; node.parentNode = parent; this.parentNode = null; }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  setCustomValidity(message) { this.validationMessage = message; }
  reportValidity() { this.validityReported = true; }
  getAttribute(name) { return this.attributes[name]; }
  addEventListener(name, callback) { this.listeners[name] = callback; }
  contains(node) { return this === node || this.children.some(child => child.contains(node)); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  querySelectorAll(selector) { return this.children.flatMap(child => [...((selector === '.decision-heading' && child.className === 'decision-heading') || (selector === '[data-focus-key]' && child.dataset.focusKey) || (selector === '[data-prospect]' && child.dataset.prospect) || (selector === 'details.evidence' && child.tagName === 'details' && child.className === 'evidence') ? [child] : []), ...child.querySelectorAll(selector)]); }
  focus() { this.focused = true; if (this.ownerDocument) { this.ownerDocument.activeElement = this; this.ownerDocument.listeners.focusin?.(); } }
  scrollIntoView() { this.revealed = true; }
  getBoundingClientRect() { return { top: 0 }; }
  get childElementCount() { return this.children.length; }
  get lastElementChild() { return this.children[this.children.length - 1] || null; }
}
function view(runs = [], { handlers = false, segmenter = true, storage, allowUnsupported = false } = {}) {
  const nodes = new Map();
  const document = { listeners: {}, addEventListener(name, callback) { this.listeners[name] = callback; }, createElement: tag => new Node(tag), createTextNode: text => { const node = new Node('text'); node.textContent = text; return node; }, getElementById: id => { if (!nodes.has(id)) nodes.set(id, new Node()); return nodes.get(id); }, activeElement: null, body: new Node(), querySelectorAll: () => [] };
  const createElement = document.createElement;
  document.createElement = tag => { const node = createElement(tag); node.ownerDocument = document; return node; };
  const getElement = document.getElementById;
  document.getElementById = id => { const node = getElement(id); node.ownerDocument = document; return node; };
  const context = vm.createContext({ document, URLSearchParams, TextEncoder, window: { matchMedia: () => ({ matches: false }), getComputedStyle: () => ({ position: 'static' }), localStorage: storage }, ...(segmenter ? {} : { Intl: { Segmenter: undefined } }) });
  const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
  const marker = handlers ? '// Polling never resets' : "$('generic-run-form').addEventListener";
  assert.ok(source.includes(marker), `Missing VM boundary: ${marker}`);
  vm.runInContext(source.slice(0, source.indexOf(marker)), context);
  context.state = { runs, definition: { id: 'native', title: 'Native documents', defaultDocument: 'docs/spec.md', documentTitles: { 'docs/spec.md': 'Product requirements', 'strategy.md': 'Strategy', 'docs/source.md': 'Source notes' } }, agents: [{ id: 'writer', name: 'Native writer', model: 'fixture' }, { id: 'reviewer', name: 'Native reviewer', model: 'fixture' }], people: [], triggers: [{ name: 'Draft', type: 'manual', agent: 'writer' }], proposals: [], prospects: [], role: 'coach', documents: ['docs/spec.md'], history: [], conversation: [] };
  vm.runInContext("ui.state = state; $('evidence-panel').append($('document-evidence'), $('conversation-evidence'), $('history-evidence'))", context);
  return { context, nodes, call: code => {
    const result = vm.runInContext(code, context);
    const requireSupported = () => {
      if (!allowUnsupported) {
        assert.ok(vm.runInContext('Boolean(ui.state?.definition)', context), 'ordinary view must remain supported');
        assert.doesNotMatch(nodes.get('notice')?.textContent || '', /^Unsupported state\./i, 'ordinary view must not silently render an unsupported empty DOM');
      }
    };
    requireSupported();
    return result?.then ? result.then(value => { requireSupported(); return value; }) : result;
  } };
}
const scenario = (id, name, extra = {}) => ({ id, agent: 'writer', mode: 'scenario', scenario: name, status: 'proposed', base: 'base-a', session: `session-${id}`, ...extra });

function genericView(runs = [], options = {}) {
  const v = view(runs, options);
  const suite = { digest: 'sha256:artifact-pin', graphDigest: 'sha256:graph-pin', contentDigest: 'sha256:content-pin', member: 'writer', memberDigest: 'sha256:member-pin', compositions: [{ digest: 'sha256:composition-pin', platform: { os: 'linux', architecture: 'amd64' } }] };
  Object.assign(v.context.state, {
    definition: { id: 'northwind-prd', title: 'Northwind PRD', digest: 'definition-pin', defaultDocument: 'docs/spec.md', documentTitles: { 'docs/spec.md': 'Product requirements', 'docs/review.json': 'Review' } },
    agents: [{ id: 'writer', name: 'PRD writer', model: 'fictional-model', suite, evaluation: 'not verified' }, { id: 'reviewer', name: 'PRD reviewer', model: 'fictional-model', suite: { ...suite, member: 'reviewer' }, evaluation: 'not verified' }],
    people: [{ id: 'GM', name: 'Morgan', title: 'Product owner' }, { id: 'coach', name: 'Riley', title: 'Engineering reviewer' }],
    prospects: [], documents: ['docs/spec.md', 'docs/review.json'], head: 'accepted-head',
    triggers: [{ name: 'Draft PRD', type: 'manual', agent: 'writer' }, { name: 'Review PRD', type: 'manual', agent: 'reviewer' }, { name: 'Feedback', type: 'file-added', agent: 'writer', path: 'feedback/*.json' }],
  });
  v.context.fetch = async () => reply({ accepted: 'Accepted fictional document' });
  return v;
}

test('generic definition shows declared agents, suite identity and unverified evaluation without football controls', async () => {
  const v = genericView();
  v.call('renderState()'); await tick();
  assert.equal(v.nodes.get('room-brand').textContent, '/ Northwind PRD');
  assert.match(v.nodes.get('suite-status').textContent, /evaluation: not verified/);
  assert.match(v.nodes.get('agents').textContent, /PRD writer.*PRD reviewer/s);
  assert.match(v.nodes.get('agents').textContent, /sha256:artifact-pin/);
  assert.doesNotMatch(v.nodes.get('conversation-content').textContent, /prospect|draft board|pick #14|injury/i);
  const html = fs.readFileSync(path.join(__dirname, '../web/index.html'), 'utf8');
  for (const id of ['assessment-toggle', 'assessment-panel', 'new-injury', 'injury-panel', 'injury-form', 'prospect', 'injury-prospect', 'scout-form', 'send', 'comparison-toggle', 'scenario-section', 'scenarios']) assert.doesNotMatch(html, new RegExp(`id="${id}"`), `${id} must be absent, not hidden`);
  assert.equal(v.nodes.get('generic-run-toggle').hidden, false);
  assert.equal(v.call('ui.path'), 'docs/spec.md');
});

test('initial persona choices are neutral and declared people supply labels without changing opaque IDs', () => {
  const html=fs.readFileSync(path.join(__dirname,'../web/index.html'),'utf8');
  const roles=html.match(/<select id="role"[^>]*>(.*?)<\/select>/s)[1];
  assert.doesNotMatch(roles,/>GM<|>Coach<|>Owner</);
  assert.match(roles,/value="GM">Loading personas…/);
  const v=genericView(); v.call('renderState()');
  assert.deepEqual(v.nodes.get('role').children.map(option=>[option.value,option.textContent]),[
    ['GM','Morgan · Product owner'],['coach','Riley · Engineering reviewer'],
  ]);
  v.context.state.people[1].name='Renamed reviewer'; v.call('renderState()');
  assert.equal(v.nodes.get('role').children[1].textContent,'Renamed reviewer · Engineering reviewer');
});

test('undeclared active attribution stays selectable across polls without inventing people', async () => {
  for (const people of [[], [{id:'coach',name:'Declared adviser',title:'Adviser'}]]) {
    const v=genericView();
    v.context.state.people=people;
    for (const active of ['GM','owner']) {
      v.context.state.role=active;
      v.call('renderState()'); await tick();
      const role=v.nodes.get('role');
      assert.equal(role.value,active);
      const fallback=role.children.find(option=>option.value===active);
      assert.ok(fallback,'active attribution needs a real option');
      assert.equal(fallback.textContent,`Attribution ID · ${active}`);
      assert.deepEqual(v.context.state.people,people);
      assert.equal(v.context.state.role,active);
    }
  }
});

test('released evidence shows unsigned consistency separately from Canvas runtime and mismatch reason', () => {
  const v = genericView();
  const release = { schemaVersion: '1.0.0-draft', platform: 'linux/amd64', buildProfile: 'tested-build', compositionDigest: 'sha256:composition', runtimeImage: 'runtime@sha256:image', harnessImage: 'harness@sha256:image', canvasRuntime: 'canvas-local/bundle-v4; execution not qualified by release evaluation', evaluation: { status: 'evaluation consistent; unsigned', unsigned: true, cases: 2, runs: 2, completedAt: '2026-10-17T16:02:00Z', evidenceDigest: 'sha256:evidence', testedExecution: { runtime: { id: 'orka' }, model: { id: 'tested-model' } } } };
  const passed = release.evaluation;
  v.context.state.agents[0].suite.release = release;
  v.call('renderNavigation()');
  const text = v.nodes.get('agents').textContent;
  for (const value of ['evaluation consistent; unsigned', 'sha256:evidence', 'tested-build', 'linux/amd64', 'tested-model', 'execution not qualified', '2026-10-17T16:02:00Z']) assert(text.includes(value), value);
  assert.doesNotMatch(text, /attested/i);
  release.evaluation = { status: 'not verified', reason: 'subject', unsigned: false };
  v.call('renderNavigation()');
  assert.match(v.nodes.get('agents').textContent, /not verified \(subject\)/);
  assert.doesNotMatch(v.nodes.get('agents').textContent, /evaluation consistent; unsigned/);
  release.evaluation = passed;
  v.call("ui.run='missing-recorded-run'; renderDefinition()");
  assert.match(v.nodes.get('suite-status').textContent, /not verified \(historical provenance unavailable\)/);
  v.context.state.runs = [{ id: 'missing-recorded-run', agent: 'writer', suite: null }];
  v.call('renderDefinition()');
  assert.match(v.nodes.get('suite-status').textContent, /not verified \(historical provenance unavailable\)/);
});

test('generic trimmed fields clear native custom validity when corrected', () => {
  const v = genericView([], { handlers: true });
  for (const id of ['generic-request', 'generic-input-path']) {
    const field = v.call(`$('${id}')`); field.value = '  ';
    v.context.field = field;
    assert.equal(v.call("trimmedValue(field, 1, 'Required.')"), null);
    assert.equal(field.validationMessage, 'Required.');
    field.value = 'Corrected value';
    field.listeners.input?.();
    assert.equal(field.validationMessage, '', id);
  }
});

test('generic trigger counts use recorded trigger identity instead of injury-agent assumptions', () => {
  const v = genericView([
    { id: 'manual-run', agent: 'writer', mode: 'documents', trigger: 'Draft PRD', status: 'proposed' },
    { id: 'file-run', agent: 'writer', mode: 'documents', trigger: 'Feedback', status: 'proposed' },
  ]);
  v.call('renderNavigation()');
  const labels = v.nodes.get('agents').textContent;
  assert.match(labels, /Draft PRD · 1 recorded run for agent/);
  assert.match(labels, /Feedback · 1 recorded run for agent/);
});

test('generic run retains its recorded suite identity and replay never verifies evaluation', async () => {
  const run = { id: 'generic-run', agent: 'writer', mode: 'documents', status: 'proposed', session: 'synthetic-session', base: 'old-base', requestSummary: 'Draft fictional requirements', suite: { digest: 'sha256:recorded-artifact', graphDigest: 'sha256:recorded-graph', contentDigest: 'sha256:recorded-content', member: 'writer', memberDigest: 'sha256:recorded-member' }, evaluation: 'not verified', proof: { chainValid: true, reconstructed: true, outputsMatch: true, modelCalls: 0 } };
  const v = genericView([run]);
  v.call("ui.run='generic-run'; ui.agent='writer'; renderState()"); await tick();
  const audit = v.nodes.get('conversation-evidence').textContent;
  assert.match(audit, /sha256:recorded-artifact/);
  assert.match(audit, /sha256:recorded-graph/);
  assert.match(audit, /sha256:recorded-content/);
  assert.match(audit, /sha256:recorded-member/);
  assert.match(v.nodes.get('conversation-content').textContent, /evaluation: not verified/);
  assert.doesNotMatch(v.nodes.get('conversation-content').textContent, /evaluation: verified/);
});

test('generic whole-document review preserves every replacement and original acceptance target', async () => {
  const run = { id: 'generic-run', agent: 'writer', mode: 'documents', status: 'proposed', proposal: 'generic-proposal', session: 'synthetic-session', base: 'old-base' };
  const v = genericView([run]);
  const proposal = { id: 'generic-proposal', status: 'pending', base: 'old-base', changes: { 'docs/spec.md': '# Fictional PRD', 'docs/review.json': '{"fictional":true}' }, attribution: { run: run.id, agent: 'writer' } };
  v.context.state.proposals = [proposal];
  const posts = [];
  v.context.fetch = async (url, options) => {
    if (options.method === 'POST') { posts.push([url, JSON.parse(options.body)]); return reply({}); }
    if (url === '/api/state') return reply(v.context.state);
    return reply({ accepted: 'Accepted', proposed: proposal.changes['docs/spec.md'], baseCommit: 'old-base' });
  };
  v.call("ui.run='generic-run'; ui.path='docs/spec.md'; renderState()"); await tick();
  const panel = v.nodes.get('document-panel');
  assert.match(panel.textContent, /Product requirements.*Review/s);
  assert.match(panel.textContent, /Fictional PRD/);
  assert.doesNotMatch(panel.textContent, /draft board|scouting|rank|tier/i);
  assert.equal(descendants(panel, 'button').filter(node => node.textContent.startsWith('Review ')).length, 2);
  await descendants(panel, 'button').find(node => node.textContent === 'Accept proposal').listeners.click();
  assert.deepEqual(posts, [['/api/review', { proposal: 'generic-proposal', action: 'accept', role: 'coach' }]]);
});

test('generic document paths do not reinterpret board.md as a football export', () => {
  const v = genericView();
  v.context.state.documents = ['board.md', 'notes.md'];
  assert.deepEqual(Array.from(v.call('documentPaths()')), ['board.md', 'notes.md']);
  assert.equal(v.call("documentLabel('board.md')"), 'board.md');
});

test('generic JSON document named board.json is not rendered as a football table', async () => {
  const v = genericView();
  v.context.state.documents = ['board.json'];
  v.context.state.definition.defaultDocument = 'board.json';
  v.context.fetch = async () => reply({ accepted: canonical([boardEntry]) });
  v.call("ui.path='board.json'; renderState()"); await tick();
  const content = v.nodes.get('document-panel');
  assert.equal(v.call('ui.path'), 'board.json');
  assert.equal(descendants(content, 'table').length, 0);
  assert.match(content.textContent, /"version":1/);
});

test('generic input submission preserves exact source bytes and never dispatches during render', async () => {
  const v = genericView([], { handlers: true });
  const posts = [];
  v.context.fetch = async (url, options) => {
    if (options.method === 'POST') { posts.push([url, JSON.parse(options.body)]); return reply({}); }
    return reply(url === '/api/state' ? v.context.state : { accepted: 'Accepted' });
  };
  v.call('renderState(); renderState()'); await tick();
  assert.equal(posts.length, 0);
  v.call("$('generic-input-path').value='feedback/new.json'; $('generic-input-content').value=' {\"fictional\":true}\\n'");
  await v.nodes.get('generic-input-form').listeners.submit({ preventDefault() {} });
  assert.deepEqual(posts, [['/api/input', { path: 'feedback/new.json', content: ' {"fictional":true}\n', role: 'coach' }]]);
  assert.equal(v.nodes.get('generic-input-content').value, '');
});

test('generic input uses the server 64 KiB UTF-8 byte bound, not 6000 characters', async () => {
  const html = fs.readFileSync(path.join(__dirname, '../web/index.html'), 'utf8');
  for (const [text, allowed] of [['x'.repeat(65536), true], ['x'.repeat(65537), false], ['é'.repeat(32768), true], ['é'.repeat(32769), false]]) {
    const v = genericView([], { handlers: true });
    const posts = [];
    v.context.fetch = async (url, options) => { if (options.method === 'POST') { posts.push(JSON.parse(options.body)); return reply({}); } return reply(url === '/api/state' ? v.context.state : { accepted: '' }); };
    v.call("$('generic-input-path').value='feedback/new.json'");
    const field = v.call("$('generic-input-content')"); field.value = text;
    await v.nodes.get('generic-input-form').listeners.submit({preventDefault(){}});
    assert.equal(posts.length, allowed ? 1 : 0, `byte bound ${new TextEncoder().encode(text).length}`);
    if (allowed) assert.equal(posts[0].content, text);
    else { assert.match(field.validationMessage, /64 KiB/); field.value='corrected'; field.listeners.input(); assert.equal(field.validationMessage, ''); }
  }
  assert.match(html, /id="generic-input-content"[^>]*maxlength="65536"/);
});

test('generic manual run is explicit, keeps submitted data exact and preserves replacement draft', async () => {
  const v = genericView([], { handlers: true });
  const posts = [];
  let deliver;
  v.context.fetch = async (url, options) => {
    if (options.method === 'POST') { posts.push([url, JSON.parse(options.body)]); return new Promise(resolve => { deliver = () => resolve(reply({ run: 'new-run' })); }); }
    return reply(url === '/api/state' ? v.context.state : { accepted: 'Accepted' });
  };
  v.call('renderState()'); await tick();
  assert.equal(posts.length, 0);
  v.nodes.get('generic-agent').value = 'writer';
  v.call("$('generic-request').value = 'Draft the fictional PRD.'");
  const pending = v.nodes.get('generic-run-form').listeners.submit({ preventDefault() {} });
  await tick();
  assert.deepEqual(posts, [['/api/run', { agent: 'writer', request: 'Draft the fictional PRD.', role: 'coach' }]]);
  v.nodes.get('generic-request').value = 'Replacement draft';
  deliver(); await pending; await tick();
  assert.equal(v.nodes.get('generic-request').value, 'Replacement draft');
});

test('literal DOM references resolve to actual shipped HTML rather than FakeDOM-created nodes', () => {
  const html = fs.readFileSync(path.join(__dirname, '../web/index.html'), 'utf8');
  const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
  const ids = new Set([...html.matchAll(/\bid="([^"]+)"/g)].map(match => match[1]));
  for (const match of source.matchAll(/\$\('([^']+)'\)/g)) assert.ok(ids.has(match[1]), match[1]);
});

test('run readiness requires a supported loaded definition, eligible manual agent and idle UI', () => {
  const html = fs.readFileSync(path.join(__dirname, '../web/index.html'), 'utf8');
  assert.match(html, /id="generic-run-submit"[^>]*\bdisabled\b/);
  const v = view([], { allowUnsupported: true });
  v.call('ui.state=null; applyBusy()');
  assert.equal(v.nodes.get('generic-run-submit').disabled, true);
  v.call('ui.state=state; renderDefinition(); applyBusy()');
  assert.equal(v.nodes.get('generic-run-submit').disabled, false);
  v.call('ui.busy=true; applyBusy()');
  assert.equal(v.nodes.get('generic-run-submit').disabled, true);
  v.call('ui.busy=false; ui.state.triggers=[]; renderDefinition(); applyBusy()');
  assert.equal(v.nodes.get('generic-run-submit').disabled, true);
  v.call('delete ui.state.definition; renderState()');
  assert.equal(v.nodes.get('generic-run-submit').disabled, true);
});

test('unsupported state revokes old document and Resume ownership, clears actions and deduplicates notice', async () => {
  const v = decisionFixture();
  // Explicit opt-in is limited to the negative-state view, not ordinary fixtures.
  const negative = view(v.context.state.runs, { handlers: true, allowUnsupported: true });
  Object.assign(negative.context.state, v.context.state);
  negative.call("ui.run='decision-run'; ui.path='docs/spec.md'");
  let finish;
  negative.context.fetch = () => new Promise(resolve => { finish = resolve; });
  const held = negative.call('renderDocuments()');
  const notice = negative.call("$('notice')");
  let writes = 0, text = '';
  Object.defineProperty(notice, 'textContent', { get: () => text, set: value => { writes++; text = String(value); } });
  negative.call("ui.resumePanels.set('old', {status:'ready'}); delete ui.state.definition; renderState(); renderState()");
  assert.match(text, /unsupported/i);
  assert.equal(writes, 1);
  assert.equal(negative.call('ui.resumePanels.size'), 0);
  assert.equal(negative.nodes.get('generic-run-submit').disabled, true);
  finish(reply({ accepted: 'Old accepted', proposed: 'Old actionable proposal' })); await held;
  assert.doesNotMatch(negative.nodes.get('document-panel').textContent, /Old actionable/);
  assert.equal(descendants(negative.nodes.get('document-panel'), 'button').length, 0);
  const posts = [];
  negative.context.fetch = async (url, options) => { if (options.method === 'POST') posts.push(url); return reply({}); };
  await negative.call("mutate('/api/review', {proposal:'decision-proposal',action:'accept'})");
  await negative.nodes.get('generic-run-form').listeners.submit({preventDefault(){}});
  assert.deepEqual(posts, []);
});

test('all recorded fork attempts stay selectable and preserve lineage evidence without creation', async () => {
  const runs = [
    { id: 'old', agent: 'writer', scenario: 'Recorded fork', parentSession: 'parent-session', forkSeq: 7, comparison: 'comparison-one', session: 'fork-session', base: 'old-base', status: 'proposed', proposal: 'fork-proposal' },
    { id: 'new', agent: 'writer', scenario: 'Recorded fork', previousAttempt: 'old', comparison: 'comparison-one', session: 'new-session', base: 'new-base', status: 'queued' },
  ];
  const v = view(runs);
  v.context.state.proposals = [{ id: 'fork-proposal', status: 'accepted', attribution: {run:'old',agent:'writer'}, changes: {'docs/spec.md':'Exact historical bytes'} }];
  v.context.fetch = async () => reply({ accepted: 'Current bytes', proposed: 'Exact historical bytes' });
  v.call('renderNavigation()');
  assert.deepEqual(descendants(v.nodes.get('sessions'), 'button').map(node => node.dataset.focusKey), ['run-new', 'run-old']);
  descendants(v.nodes.get('sessions'), 'button')[1].listeners.click(); await tick();
  assert.equal(v.call('ui.run'), 'old');
  assert.match(v.nodes.get('document-panel').textContent, /Exact historical bytes/);
  const evidence = v.nodes.get('conversation-evidence').textContent;
  for (const value of ['Recorded fork', 'comparison-one', 'parent-session', 'fork-session', 'old-base', '"forkCheckpoint": 7']) assert.ok(evidence.includes(value), value);
  descendants(v.nodes.get('sessions'), 'button')[0].listeners.click(); await tick();
  assert.match(v.nodes.get('conversation-evidence').textContent, /previousAttempt.*old/s);
  assert.doesNotMatch(v.nodes.get('conversation-content').textContent, /Compare trade|Create fork/i);
});

test('arbitrary declared agents and paths use the same run and document endpoints without defaults', async () => {
  const v=view([], {handlers:true});
  Object.assign(v.context.state, {
    definition:{id:'research',title:'Research',defaultDocument:'references/source.txt'},
    agents:[{id:'synthesizer',name:'Evidence synthesizer',model:'authored-only'}],
    triggers:[{type:'manual',agent:'synthesizer',name:'Synthesize'}],
    documents:['references/source.txt','board.md','board.json'],
  });
  const requests=[];
  v.context.fetch=async(url,options)=>{
    requests.push([url,options?.body&&JSON.parse(options.body)]);
    return reply(url==='/api/state'?v.context.state:url==='/api/run'?{run:'new-synthesis'}:{accepted:'Exact arbitrary bytes'});
  };
  v.call('renderState()'); await tick();
  assert.equal(v.call('ui.agent'),'synthesizer');
  assert.equal(v.call('ui.path'),'references/source.txt');
  assert.deepEqual(descendants(v.nodes.get('document-tabs'),'button').map(node=>node.textContent),['references/source.txt','board.md','board.json']);
  assert.equal(v.nodes.get('generic-run-submit').disabled,false);
  v.call("$('generic-request').value='Synthesize this evidence.'");
  await v.nodes.get('generic-run-form').listeners.submit({preventDefault(){}});
  assert.deepEqual(requests.find(([url])=>url==='/api/run'),['/api/run',{agent:'synthesizer',request:'Synthesize this evidence.',role:'coach'}]);
  assert.ok(requests.some(([url])=>url==='/api/document?path=references%2Fsource.txt'));
});

test('ordinary view refuses unsupported state synchronously and after an asynchronous state response', async () => {
  const v=view();
  assert.throws(()=>v.call('delete ui.state.definition; renderState()'),/ordinary view must remain supported/);
  const asyncView=view();
  const state={...asyncView.context.state}; delete state.definition;
  asyncView.context.fetch=async()=>reply(state);
  await assert.rejects(asyncView.call('refreshState()'),/ordinary view must remain supported/);
});

test('returning from unsupported state rebuilds cached generic navigation, documents and pending work', async () => {
  const v=view([],{handlers:true,allowUnsupported:true});
  const definition=v.context.state.definition;
  v.context.fetch=async()=>reply({accepted:'Restored exact bytes'});
  v.call('renderState()'); await tick();
  for (const kind of ['run','input']) {
    v.call(`setGenericForm('${kind}', true)`);
    assert.equal(v.nodes.get(`generic-${kind}-toggle`).getAttribute('aria-expanded'),'true');
  }
  delete v.context.state.definition;
  v.call('renderState()');
  assert.equal(v.nodes.get('document-tabs').children.length,0);
  v.context.state.definition=definition;
  v.call('renderState()'); await tick();
  assert.equal(v.nodes.get('document-tabs').children.length,1);
  assert.equal(descendants(v.nodes.get('agents'),'button').length,2);
  assert.match(v.nodes.get('document-panel').textContent,/Restored exact bytes/);
  assert.equal(v.nodes.get('notice').hidden,true);
  assert.equal(v.nodes.get('generic-run-submit').disabled,false);
  for (const kind of ['run','input']) {
    assert.equal(v.nodes.get(`generic-${kind}-panel`).hidden,true);
    assert.equal(v.nodes.get(`generic-${kind}-toggle`).getAttribute('aria-expanded'),'false');
  }
});

test('unsupported state clears old checkpoint and proposal counts, then rebuilds them on return', async () => {
  const v=view([],{allowUnsupported:true});
  const definition=v.context.state.definition;
  v.context.fetch=async()=>reply({accepted:'Exact accepted bytes'});
  v.call('renderState()'); await tick();
  const prior=v.nodes.get('review-counts').textContent;
  assert.notEqual(prior,'');
  delete v.context.state.definition;
  v.call('renderState()');
  assert.equal(v.nodes.get('review-counts').textContent,'');
  v.context.state.definition=definition;
  v.call('renderState()'); await tick();
  assert.equal(v.nodes.get('review-counts').textContent,prior);
});

// Removing validation or treating panels as a shared/global preference breaks these contracts.
function layoutStorage(initial = null) {
  const values = new Map(initial == null ? [] : [['canvas.layout.v1', initial]]);
  return { getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value) };
}

test('New menu opens declared run and source forms without clearing either draft', () => {
  const v = view([], { handlers: true });
  v.call("$('generic-request').value='Run draft'; $('generic-input-content').value='Source draft'; $('generic-run-panel').hidden=true; $('generic-input-panel').hidden=true");
  v.nodes.get('generic-run-toggle').listeners.click();
  assert.equal(v.nodes.get('generic-run-panel').hidden, false);
  assert.equal(v.nodes.get('generic-agent').focused, true);
  v.nodes.get('generic-input-toggle').listeners.click();
  assert.equal(v.nodes.get('generic-input-panel').hidden, false);
  assert.equal(v.nodes.get('generic-input-path').focused, true);
  assert.equal(v.nodes.get('generic-request').value, 'Run draft');
  assert.equal(v.nodes.get('generic-input-content').value, 'Source draft');
});
test('focused panels default closed and do not rebuild or consume drafts when toggled', () => {
  const v = view([], { storage: layoutStorage() });
  v.call("$('generic-request').value='Scout draft'; $('room-request').value='Room draft'; renderLayout()");
  assert.equal(v.nodes.get('navigation').hidden, true);
  assert.equal(v.nodes.get('evidence-panel').hidden, true);
  assert.equal(v.nodes.get('navigation-toggle').getAttribute('aria-expanded'), 'false');
  assert.equal(v.nodes.get('evidence-toggle').getAttribute('aria-controls'), 'evidence-panel');
  v.call("setPanel('navigation', true); setPanel('evidence', true); setPanel('navigation', false)");
  assert.equal(v.nodes.get('evidence-panel').hidden, false);
  assert.equal(v.nodes.get('navigation').hidden, true);
  assert.equal(v.nodes.get('generic-request').value, 'Scout draft');
  assert.equal(v.nodes.get('room-request').value, 'Room draft');
});

test('layout remembers each supported persona independently across reload', () => {
  const storage = layoutStorage();
  const v = view([], { storage });
  v.call("renderLayout(); setPanel('navigation', true)");
  v.context.state.role = 'owner'; v.call('renderLayout()');
  assert.equal(v.nodes.get('navigation').hidden, true);
  v.call("setPanel('evidence', true)");
  v.context.state.role = 'coach'; v.call('renderLayout()');
  assert.equal(v.nodes.get('navigation').hidden, false);
  assert.equal(v.nodes.get('evidence-panel').hidden, true);
  const reloaded = view([], { storage }); reloaded.call('renderLayout()');
  assert.equal(reloaded.nodes.get('navigation').hidden, false);
  reloaded.context.state.role = 'owner'; reloaded.call('renderLayout()');
  assert.equal(reloaded.nodes.get('evidence-panel').hidden, false);
});

test('corrupt, unsupported and unavailable layout storage safely defaults focused', () => {
  for (const value of ['{broken', 'null', '{"version":2,"roles":{"coach":{"navigation":true,"evidence":true}}}', '{"version":1,"roles":{"coach":{"navigation":"yes","evidence":true}}}', '{"version":1,"roles":{"coach":{"navigation":true}}}']) {
    const v = view([], { storage: layoutStorage(value) }); v.call('renderLayout()');
    assert.equal(v.nodes.get('navigation').hidden, true, value);
    assert.equal(v.nodes.get('evidence-panel').hidden, true, value);
  }
  for (const storage of [undefined, { getItem() { throw new Error('blocked read'); }, setItem() { throw new Error('blocked write'); } }]) {
    const v = view([], { storage }); v.call("renderLayout(); setPanel('navigation', true); setPanel('navigation', false)");
    assert.equal(v.nodes.get('navigation').hidden, true);
  }
  const readable = layoutStorage(); readable.setItem = () => { throw new Error('blocked write'); };
  const blockedWrite = view([], { storage: readable });
  blockedWrite.call("renderLayout(); setPanel('navigation', true)");
  assert.equal(blockedWrite.nodes.get('navigation').hidden, false);
  blockedWrite.context.state.role = 'owner'; blockedWrite.call('renderLayout()');
  blockedWrite.context.state.role = 'coach'; blockedWrite.call('renderLayout()');
  assert.equal(blockedWrite.nodes.get('navigation').hidden, false, 'blocked writes retain an in-memory preference');
  const v = view(); Object.defineProperty(v.context.window, 'localStorage', { get() { throw new Error('blocked access'); } });
  v.call("renderLayout(); setPanel('evidence', true)");
  assert.equal(v.nodes.get('evidence-panel').hidden, false);
});

test('request strip retains every actual pending proposal, active run and pending question outside panels', async () => {
  const v = roomView([roomRun('one', { proposal: 'p-one' }), roomRun('queued', { status: 'queued' }), roomRun('running', { status: 'running' })]);
  v.context.state.proposals = [{ id: 'p-one', status: 'pending', attribution: { run: 'one' }, changes: { 'docs/spec.md': proposalText } }, { id: 'p-two', status: 'pending', changes: { 'strategy.md': 'Actual strategy' } }, { id: 'stale', status: 'stale', changes: {} }];
  v.context.state.documents.push('strategy.md');
  v.context.state.conversation = [roomQuestion({ id: 'q-one', run: 'one' }), roomQuestion({ id: 'q-two', run: 'running', status: 'running' }), { id: 'comment', kind: 'comment', text: 'not a request' }];
  v.context.fetch = async () => reply({ accepted: proposalText });
  v.call('renderState()'); await tick();
  const strip = v.nodes.get('request-strip');
  assert.equal(descendants(strip, 'li').length, 6);
  assert.equal(descendants(strip, 'button').length, 5);
  assert.doesNotMatch(strip.textContent, /not a request|stale/);
  v.call("setPanel('navigation', true); setPanel('evidence', true); setPanel('navigation', false); setPanel('evidence', false)");
  assert.equal(descendants(strip, 'li').length, 6);
  v.call("ui.proposal=''; renderRequests()");
  await descendants(strip, 'button')[0].listeners.click();
  assert.equal(v.call('ui.proposal'), 'p-one'); assert.equal(v.call('ui.run'), 'one');
  await descendants(strip, 'button')[0].listeners.click();
  assert.equal(v.call('ui.proposal'), 'p-two'); assert.equal(v.call('ui.path'), 'strategy.md');
  await descendants(strip, 'button').find(node => node.dataset.focusKey === 'request-run-queued').listeners.click(); await tick();
  assert.equal(v.call('ui.run'), 'queued'); assert.equal(v.nodes.get('review').focused, true);
  await descendants(strip, 'button').find(node => node.dataset.focusKey === 'request-run-running').listeners.click(); await tick();
  assert.equal(v.call('ui.run'), 'running');
  await descendants(strip, 'button').find(node => node.dataset.focusKey === 'request-question-q-one').listeners.click(); await tick();
  assert.equal(v.call('ui.run'), 'one');
  const question = descendants(v.nodes.get('conversation-content'), 'article').filter(node => node.dataset.conversationId === 'q-one').at(-1);
  assert.equal(question.focused, true); assert.equal(question.revealed, true);
});

test('pending banner follows selected review context without hashes or a redundant review action', async () => {
  const v = decisionFixture();
  v.context.state.people = [{ id: 'coach', name: 'Marcus Hale' }];
  v.call('renderState()'); await tick();
  const strip = v.nodes.get('request-strip');
  assert.match(strip.textContent, /Marcus Hale.*review.*Native reviewer/is);
  assert.doesNotMatch(strip.textContent, /decision-proposal|decision-run|private-base/);
  assert.equal(descendants(strip, 'button').length, 0, 'already visible decision needs no navigation action');
  v.call("ui.proposal=''; renderState()"); await tick();
  const review = descendants(strip, 'button').find(node => node.textContent === 'Review proposal');
  assert.ok(review, 'accepted-only context exposes exactly the pending proposal route');
  review.listeners.click(); await tick();
  assert.equal(v.call('ui.proposal'), 'decision-proposal');
  assert.equal(descendants(strip, 'button').length, 0);
});

// Catch return-to-review links leaving approval behind the inner full-text scroller,
// or a reset leaking into unchanged polling instead of explicit navigation.
for (const route of ['View proposal']) {
  test(`${route} returns to the approval header on the same cached proposal`, async () => {
    const v = decisionFixture();
    v.call('renderState()'); await tick();
    const panel = v.nodes.get('document-panel');
    const content = panel.children[0];
    panel.scrollTop = 650;
    v.call('renderState()'); await tick();
    assert.equal(panel.scrollTop, 650, 'unchanged state refresh must preserve document reading position');
    if (route === 'Pending work') descendants(v.nodes.get('request-strip'), 'button')[0].listeners.click();
    else descendants(v.nodes.get('conversation-content'), 'button').find(node => node.textContent === 'View proposal').listeners.click();
    await tick();
    assert.equal(panel.scrollTop, 0, 'explicit navigation must reset the inner document scroller');
    assert.equal(panel.children[0], content, 'returning to the same context should keep the cached document');
    assert.equal(v.nodes.get('review').focused, true);
    assert.equal(v.call('ui.run'), 'decision-run');
    assert.equal(v.call('ui.proposal'), 'decision-proposal');
  });
}

test('selected document evidence opens its recorded audit and collapsing returns focus to the labeled control', async () => {
  const v = decisionFixture(); await v.call('renderDocuments()');
  const panel = v.nodes.get('document-panel');
  assert.equal(descendants(panel, 'details').some(node => node.className === 'evidence'), false);
  const launcher = descendants(panel, 'button').find(node => node.textContent === 'Evidence');
  launcher.listeners.click();
  const audit = v.nodes.get('document-evidence');
  assert.match(audit.textContent, /private-session|private-base|private raw diff/);
  assert.match(audit.textContent, /Full recorded chronology/);
  const record = descendants(audit, 'details')[0];
  assert.equal(record.open, true); assert.equal(v.nodes.get('evidence-panel').hidden, false);
  v.context.document.activeElement = record.children[0];
  v.call("setPanel('evidence', false)");
  assert.equal(v.nodes.get('evidence-toggle').focused, true);
  assert.ok(descendants(panel, 'button').some(node => node.textContent === 'Accept proposal' && !node.disabled));
});

test('question evidence remains reachable without duplicates across cached polls, persona, run and proof refreshes', () => {
  const v = roomView([roomRun(), roomRun('other', { session: 'other-session' })]);
  v.context.state.conversation = [roomQuestion({ status: 'answered', answer: 'Recorded answer' })];
  const refreshes = [() => {}, () => { v.context.state.role = 'owner'; }, () => { v.call("ui.run='other'"); }, () => { v.call("ui.run='room-run'; ui.verified.add('room-run'); ui.proofs.set('room-run', {chainValid:true})"); }];
  for (const refresh of refreshes) {
    refresh(); v.call('renderConversation()');
    const audit = v.nodes.get('conversation-evidence');
    for (const key of ['disclosure-room-question-1', 'disclosure-answer-question-1']) {
      const records = descendants(audit, 'details').filter(node => node.id === key);
      assert.equal(records.length, 1);
      const launcher = descendants(v.nodes.get('conversation-content'), 'button').find(node => node.dataset.focusKey === `open-${key}`);
      assert.ok(launcher); launcher.listeners.click();
      assert.equal(records[0].open, true);
      assert.ok(audit.contains(records[0]));
      assert.match(records[0].textContent, /recorded-p3-session/);
    }
  }
});

test('conversation launchers restore focus after their replacements are collected, including no selected run', () => {
  const v = roomView(); v.context.state.conversation = [roomQuestion()];
  for (const run of ['room-run', null]) {
    v.context.run = run; v.call('ui.run=run; renderConversation()');
    const key = 'open-disclosure-answer-question-1';
    const launcher = descendants(v.nodes.get('conversation-content'), 'button').find(node => node.dataset.focusKey === key);
    v.context.document.activeElement = launcher;
    v.context.state.conversation[0].seconds = (v.context.state.conversation[0].seconds || 0) + 1;
    v.call('renderConversation()');
    const replacement = descendants(v.nodes.get('conversation-content'), 'button').find(node => node.dataset.focusKey === key);
    assert.equal(replacement.focused, true);
  }
});

test('pending question shortcut focuses a stable destination through duration and answer transitions', () => {
  const v = roomView(); v.context.state.conversation = [roomQuestion()];
  v.call('renderRequests(); renderConversation()');
  descendants(v.nodes.get('request-strip'), 'button')[0].listeners.click();
  for (const [status, seconds] of [['queued', 2], ['running', 3], ['answered', 4]]) {
    const row = descendants(v.nodes.get('conversation-content'), 'article').filter(node => node.dataset.conversationId === 'question-1').at(-1);
    v.context.document.activeElement = row;
    Object.assign(v.context.state.conversation[0], { status, seconds, answer: 'Recorded answer' });
    v.call('renderConversation()');
    const replacement = descendants(v.nodes.get('conversation-content'), 'article').filter(node => node.dataset.conversationId === 'question-1').at(-1);
    assert.equal(replacement.focused, true, status);
    assert.equal(replacement.tabIndex, -1);
  }
});

test('document audit stays attached during same-owner refresh but never survives a failed cross-document switch', async () => {
  const v = decisionFixture();
  v.context.state.documents.push('strategy.md');
  v.context.state.conversation = [roomQuestion()];
  v.context.state.history = [{ commit: 'checkpoint', actor: 'coach', summary: 'Actual history' }];
  v.call('renderConversation(); renderHistory()'); await v.call('renderDocuments()');
  const audit = v.nodes.get('document-evidence'); const prior = audit.children[0];
  const conversationAudit = v.nodes.get('conversation-evidence').children[0];
  const historyAudit = v.nodes.get('history-evidence').children[0];
  let finish;
  v.context.fetch = () => new Promise(resolve => { finish = resolve; });
  v.context.state.runs[0].seconds = 2;
  const refresh = v.call('renderDocuments()');
  assert.equal(audit.children[0], prior, 'same selection must retain audit while refreshing');
  finish(reply({ accepted: proposalText, proposed: proposalText })); await refresh;
  const boardAudit = audit.children[0];
  v.call("ui.path='strategy.md'");
  const switched = v.call('renderDocuments()');
  assert.equal(audit.contains(boardAudit), false, 'new document must not show unmarked prior audit while loading');
  finish({ ok: false, status: 503, json: async () => ({ error: 'Document outage' }) }); await switched;
  assert.match(audit.textContent, /Evidence unavailable.*Strategy/);
  assert.doesNotMatch(audit.textContent, /private-session|private-base|private raw diff/);
  assert.equal(v.nodes.get('conversation-evidence').children[0], conversationAudit);
  assert.equal(v.nodes.get('history-evidence').children[0], historyAudit);
});

test('unrelated run duration changes preserve review DOM, scroll and audit without document refetch', async () => {
  const v = decisionFixture();
  v.context.state.runs.push(roomRun('sibling', { seconds: 1, session: 'sibling-session' }));
  await v.call('renderDocuments()');
  const panel = v.nodes.get('document-panel'), audit = v.nodes.get('document-evidence');
  const content = panel.children[0], record = audit.children[0];
  panel.scrollTop = 400; v.nodes.get('evidence-panel').scrollTop = 240;
  let requests = 0;
  v.context.fetch = async () => { requests++; return reply({ accepted: proposalText }); };
  v.context.state.runs[1].seconds = 2;
  await v.call('renderDocuments()');
  assert.equal(panel.children[0], content, 'unrelated seconds must not reconstruct the review');
  assert.equal(audit.children[0], record);
  assert.equal(panel.scrollTop, 400);
  assert.equal(v.nodes.get('evidence-panel').scrollTop, 240);
  assert.equal(requests, 0);
});

test('same-owner meaningful refresh retains review during loading, reading position and disclosures', async () => {
  const v = decisionFixture(); await v.call('renderDocuments()');
  const panel = v.nodes.get('document-panel');
  const content = panel.children[0];
  const full = descendants(panel, 'input').find(node => node.type === 'checkbox');
  full.checked = true; full.listeners.change();
  panel.scrollTop = 400; v.nodes.get('evidence-panel').scrollTop = 230;
  let finish;
  v.context.fetch = () => new Promise(resolve => { finish = resolve; });
  v.context.state.runs[0].seconds = 12;
  const refreshing = v.call('renderDocuments()');
  assert.equal(panel.children[0], content, 'selected document stays readable while loading');
  panel.scrollTop = 460; // Reading may continue during the API request.
  finish(reply({ accepted: proposalText, proposed: proposalText })); await refreshing;
  assert.equal(panel.scrollTop, 460);
  assert.equal(v.nodes.get('evidence-panel').scrollTop, 230);
  assert.equal(descendants(panel, 'input').find(node => node.type === 'checkbox').checked, true);
  v.context.state.documents.push('strategy.md');
  v.call("ui.path='strategy.md'");
  const switched = v.call('renderDocuments()');
  finish(reply({ accepted: 'Current strategy' })); await switched;
  assert.equal(panel.scrollTop, 0, 'cross-selection deliberately starts at the document header');
});

test('authoritative stale transition hides obsolete approval while a same-owner document request is held', async () => {
  const v = decisionFixture(); await v.call('renderDocuments()');
  const panel = v.nodes.get('document-panel');
  panel.scrollTop = 400;
  let finish;
  v.context.fetch = () => new Promise(resolve => { finish = resolve; });
  v.context.state.proposals[0].status = 'stale';
  const refreshing = v.call('renderDocuments()');
  assert.doesNotMatch(panel.textContent, /Awaiting approval/);
  assert.equal(descendants(panel, 'button').some(node => node.textContent === 'Accept proposal' && !node.disabled), false);
  assert.match(panel.textContent, /Loading/);
  finish(reply({ accepted: proposalText, proposed: proposalText, status: 'pending' })); await refreshing;
  assert.doesNotMatch(panel.textContent, /Awaiting approval/);
  assert.equal(descendants(panel, 'button').find(node => node.textContent === 'Accept proposal').disabled, true);
  assert.equal(descendants(panel, 'button').find(node => node.textContent === 'Reject').disabled, false);
  assert.equal(panel.scrollTop, 400, 'same identity can resume reading after authority changes');
});

for (const fail of [false, true]) {
  test(`new same-session question removes saved proof during held document refresh (${fail ? 'error' : 'delayed success'})`, async () => {
    const proof = { chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0, head: 'obsolete-proof' };
    const v = decisionFixture({ proof }); await v.call('renderDocuments()');
    const panel = v.nodes.get('document-panel'), audit = v.nodes.get('document-evidence');
    descendants(audit, 'button').find(node => node.textContent === 'Replay verified · open saved proof').listeners.click();
    assert.match(audit.textContent, /obsolete-proof/);
    let finish;
    v.context.fetch = () => new Promise(resolve => { finish = resolve; });
    v.context.state.conversation = [roomQuestion({ run: 'decision-run', session: 'private-session', status: 'queued' })];
    const refreshing = v.call('renderDocuments()');
    assert.equal(v.call('currentRunProof(ui.state.runs[0])'), null);
    assert.doesNotMatch(panel.textContent, /Replay verified/);
    assert.doesNotMatch(audit.textContent, /obsolete-proof|Verified/);
    finish(fail ? { ok: false, status: 503, json: async () => ({ error: 'Document outage' }) }
      : reply({ accepted: proposalText, proposed: proposalText, proof }));
    await refreshing;
    assert.doesNotMatch(panel.textContent, /Replay verified/);
    assert.doesNotMatch(audit.textContent, /obsolete-proof|Verified/);
    if (!fail) assert.equal(descendants(audit, 'button').find(node => node.textContent === 'Verify replay & reconstruction').disabled, true);
  });
}

for (const extra of [{ error: 'Verification save failed' }, { status: 'failed' }, { completed: undefined }, { activity: [] }]) {
  test(`execution authority invalidates old completion while document GET is held: ${JSON.stringify(extra)}`, async () => {
    const v = decisionFixture(); await v.call('renderDocuments()');
    let deliver;
    v.context.fetch = () => new Promise(resolve => { deliver = resolve; });
    Object.assign(v.context.state.runs[0], extra);
    const refresh = v.call('renderDocuments()');
    assert.match(v.nodes.get('document-panel').textContent, /Loading/);
    assert.doesNotMatch(v.nodes.get('document-evidence').textContent, /Finished/);
    deliver(reply({ accepted: '', proposed: '' })); await refresh;
    const meta = descendants(v.nodes.get('document-evidence'), 'div').find(node => node.className === 'decision-meta');
    assert.doesNotMatch(meta.textContent, /Finished/);
  });
}

test('same-owner authority clear restores historical reader focus only when no newer destination was chosen', async () => {
  for (const newer of [false, true]) {
    const v = decisionFixture(); await v.call('renderDocuments()');
    const diff = descendants(v.nodes.get('document-panel'), 'pre').find(node => node.className === 'document-diff');
    diff.focus();
    let deliver;
    v.context.fetch = () => new Promise(resolve => { deliver = resolve; });
    v.context.state.proposals[0].status = 'stale';
    const refresh = v.call('renderDocuments()');
    if (newer) v.call("$('room-request').focus()");
    deliver(reply({ accepted: '', proposed: '', diff: '+new diff' })); await refresh;
    if (newer) assert.equal(v.context.document.activeElement, v.nodes.get('room-request'));
    else assert.equal(v.context.document.activeElement.dataset.focusKey, 'document-diff-docs/spec.md');
  }
});

for (const destination of ['diff', 'scope', 'composer']) {
  test(`held harmless refresh failure respects delivery-time ${destination} focus ownership`, async () => {
    const v = decisionFixture(); await v.call('renderDocuments()');
    descendants(v.nodes.get('document-panel'), 'button').find(node => node.className.includes('accept')).focus();
    let deliver;
    v.context.fetch = () => new Promise(resolve => { deliver = resolve; });
    v.context.state.runs[0].seconds = 120;
    const refresh = v.call('renderDocuments()');
    if (destination === 'composer') v.call("$('room-request').focus()");
    else if (destination === 'diff') descendants(v.nodes.get('document-panel'), 'pre').find(node => node.className === 'document-diff').focus();
    else descendants(v.nodes.get('document-panel'), 'button').find(node => node.dataset.focusKey?.startsWith('affected-')).focus();
    deliver({ ok: false, status: 503, json: async () => ({ error: 'Held document outage' }) }); await refresh;
    if (destination === 'composer') assert.equal(v.context.document.activeElement, v.nodes.get('room-request'));
    else assert.equal(v.context.document.activeElement.dataset.focusKey, 'retry-document', 'removed reader transfers to the attached recovery action');
  });
}

test('repeated failed Retry document retains replacement focus and bounded polite feedback', async () => {
  const v = decisionFixture();
  v.context.fetch = async () => ({ ok: false, status: 503, json: async () => ({ error: 'Document outage ' + 'x'.repeat(1200) }) });
  await v.call('renderDocuments()');
  const retry = descendants(v.nodes.get('document-panel'), 'button')[0]; retry.focus();
  await retry.listeners.click();
  assert.equal(v.context.document.activeElement.dataset.focusKey, 'retry-document');
  assert.match(v.nodes.get('poll-status').textContent, /Document review unavailable/);
  assert.ok(v.nodes.get('poll-status').textContent.length < 250);
});

test('selected proposal agent rename updates Documents as well as Conversation without sibling clocks', async () => {
  const v = decisionFixture();
  v.context.state.agents = [{ id: 'reviewer', name: 'Initial reviewer' }];
  v.call('renderConversation()'); await v.call('renderDocuments()');
  assert.match(v.nodes.get('document-evidence').textContent, /Initial reviewer/);
  v.context.state.agents[0].name = 'Renamed reviewer';
  v.call('renderConversation()'); await v.call('renderDocuments()');
  assert.match(v.nodes.get('conversation-content').textContent, /Renamed reviewer/);
  assert.match(v.nodes.get('document-evidence').textContent, /Renamed reviewer/);
  assert.doesNotMatch(v.nodes.get('document-evidence').textContent, /Initial reviewer/);
});

test('multiple proposal picker belongs to quiet navigation, not the default decision paper', async () => {
  const v = decisionFixture();
  v.context.state.proposals.push({ id: 'another-private-proposal', status: 'pending', changes: { 'docs/spec.md': proposalText } });
  await v.call('renderDocuments()');
  assert.equal(descendants(v.nodes.get('document-panel'), 'select').length, 0);
  const picker = descendants(v.nodes.get('proposal-picker'), 'select')[0];
  assert.ok(picker);
  assert.doesNotMatch(picker.textContent, /another-private-proposal|decision-proposal/);
  picker.value = 'another-private-proposal'; picker.listeners.change(); await tick();
  assert.equal(v.call('ui.proposal'), 'another-private-proposal');
  assert.equal(v.call('ui.run'), null, 'navigation never guesses an originating run');
});

test('unassociated proposal picker follows recorded agent renames without guessing an originating run', async () => {
  const v = decisionFixture();
  v.context.state.agents = [{ id: 'recorded-other', name: 'Initial other agent' }];
  v.context.state.proposals.push({ id: 'unassociated', status: 'pending', attribution: { run: 'missing-run', agent: 'recorded-other' }, changes: { 'docs/spec.md': proposalText } });
  await v.call('renderDocuments()');
  const picker = () => descendants(v.nodes.get('proposal-picker'), 'select')[0];
  assert.match(picker().textContent, /Initial other agent/);
  v.context.state.agents[0].name = 'Renamed other agent';
  await v.call('renderDocuments()');
  assert.match(picker().textContent, /Renamed other agent/);
  assert.doesNotMatch(picker().textContent, /Initial other agent/);
  picker().value = 'unassociated'; picker().listeners.change(); await tick();
  assert.equal(v.call('ui.run'), null);
  assert.equal(v.call('proposalRun(currentProposal())'), undefined);
  assert.match(v.nodes.get('document-panel').textContent, /Renamed other agent/);
});


test('repeated checkpoint history renders retain attached evidence and refreshed provenance without duplicates', () => {
  const v = view(); v.context.state.history = [{ commit: 'checkpoint-one', proposal: 'p-one', actor: 'coach', summary: 'Actual checkpoint' }];
  for (let index = 0; index < 3; index++) {
    v.call('renderHistory()');
    const records = descendants(v.nodes.get('history-evidence'), 'details');
    assert.equal(records.length, 1);
    descendants(v.nodes.get('history-list'), 'button')[0].listeners.click();
    assert.equal(records[0].open, true); assert.match(records[0].textContent, /checkpoint-one/);
  }
  v.context.state.history.push({ commit: 'checkpoint-two', proposal: 'p-two', actor: 'owner', summary: 'Next checkpoint' });
  v.call('renderHistory()');
  const records = descendants(v.nodes.get('history-evidence'), 'details');
  assert.equal(records.length, 2);
  for (const launcher of descendants(v.nodes.get('history-list'), 'button')) {
    launcher.listeners.click();
    assert.ok(records.some(record => record.children[0].focused && record.open));
  }
});

test('recorded attempts with different comparison IDs remain independently selectable', async () => {
  const v = view([scenario('old', 'Recorded fork', { comparison:'one', parentSession:'parent',forkSeq:7 }), scenario('new', 'Recorded fork', { comparison:'one',previousAttempt:'old',base:'base-b',status:'queued' }), scenario('other', 'Recorded fork', { comparison:'two' })]);
  v.context.fetch = async () => reply({accepted:'Accepted text'});
  v.call('renderNavigation()');
  for (const id of ['old','new','other']) {
    descendants(v.nodes.get('sessions'),'button').find(node=>node.dataset.focusKey===`run-${id}`).listeners.click(); await tick();
    assert.equal(v.call('ui.run'),id);
    assert.equal(v.call('currentProposal()'),undefined);
    assert.match(v.nodes.get('conversation-evidence').textContent, new RegExp(id === 'other' ? 'two' : 'one'));
  }
});
test('queued latest uses creation time, never started time', () => {
  const v = view([scenario('new', 'Trade up', { comparison: 'one', created: '2026-01-02T00:00:00Z', started: '0001-01-01T00:00:00Z', status: 'queued' }), scenario('old', 'Trade up', { comparison: 'one', created: '2026-01-01T00:00:00Z', started: '2026-01-03T00:00:00Z' })]);
  assert.equal(v.call('orderedRuns().at(-1).id'), 'new');
});

test('legacy lineage is retained as evidence without inferring an unrecorded parent', () => {
  const v = view([scenario('known', 'Recorded fork', { parentSession:'parent',forkSeq:4 }), scenario('unknown', 'Recorded fork')]);
  v.call("ui.run='known'; renderConversation()");
  assert.match(v.nodes.get('conversation-evidence').textContent, /parentSession.*parent.*forkCheckpoint.*4/s);
  v.call("ui.run='unknown'; renderConversation()");
  assert.doesNotMatch(v.nodes.get('conversation-evidence').textContent, /parentSession|forkCheckpoint/);
});
test('earlier historical attempts remain selectable and retain their own evidence disclosure', async () => {
  const v = view([scenario('old', 'Recorded fork', {comparison:'one'}),scenario('new','Recorded fork',{comparison:'one',status:'queued',previousAttempt:'old'})]);
  v.context.fetch=async()=>reply({accepted:'Accepted'});
  v.call('renderNavigation()');
  assert.equal(descendants(v.nodes.get('sessions'),'button').length,2);
  descendants(v.nodes.get('sessions'),'button')[1].listeners.click(); await tick();
  const evidence=descendants(v.nodes.get('conversation-evidence'),'details')[0];
  evidence.open=true; evidence.listeners.toggle();
  v.call("ui.conversationKey=''; renderConversation()");
  assert.equal(v.call('ui.run'),'old');
  assert.equal(descendants(v.nodes.get('conversation-evidence'),'details')[0].open,true);
  assert.match(v.nodes.get('conversation-evidence').textContent, /comparison.*one/s);
});
test('speaker names and colors use stable IDs, not current role or editable titles', () => {
 const v=view();v.context.state.people=[{id:'GM',name:'Dana Ruiz',title:'Coach renamed title'},{id:'coach',name:'Marcus Hale',title:'Owner renamed title'},{id:'owner',name:'Priya Anand',title:'Manager renamed title'}];
 const classes=[];
 for(const [actor,name,initials] of [['GM','Dana Ruiz','GM'],['coach','Marcus Hale','CO'],['owner','Priya Anand','OW']]){
  v.context.actor=actor;v.context.state.role='owner';const row=v.call("message('Scouting request','Synthetic request','Recorded actor','human',actor).row");
  assert.equal(descendants(row,'h3')[0].textContent,name);assert.equal(row.children[0].textContent,initials);classes.push(row.className);
  assert.match(row.textContent,/Scouting request/);
 }
 assert.equal(new Set(classes).size,3);
 const unknown=v.call("message('Scouting request','','','human','unknown').row");assert.match(descendants(unknown,'h3')[0].textContent,/unknown/);assert.doesNotMatch(unknown.textContent,/Dana Ruiz/);
 for(const id of ['writer','reviewer']){v.context.agent=id;const row=v.call("message(agentName(agent),'Synthetic result','',agent).row");assert.equal(descendants(row,'h3')[0].textContent,v.call('agentName(agent)'));assert.notEqual(row.className,classes[0]);}
});

test('human actor IDs cannot inherit an agent speaker palette', () => {
  const v = view();
  for (const [id, initials] of [['writer', 'SC'], ['reviewer', 'IA']]) {
    v.context.id = id;
    const human = v.call("message('Recorded request', '', '', 'human', id).row");
    assert.equal(human.className, 'message speaker-unknown');
    assert.equal(human.children[0].textContent, '?');
    assert.equal(descendants(human, 'h3')[0].textContent, `Recorded actor: ${id}`);
    const agent = v.call("message(agentName(id), '', '', id).row");
    assert.equal(agent.className, 'message speaker-unknown');
    assert.equal(agent.children[0].textContent, '?');
    assert.match(descendants(agent,'h3')[0].textContent, /Native writer|Native reviewer/);
  }
});

test('recorded speaker remains named after role switches and refreshes after metadata rename', () => {
 const v=view([{id:'scout',agent:'writer',actor:'GM',status:'queued',requestSummary:'Recorded synthetic request'}]);v.context.state.people=[{id:'GM',name:'Dana Ruiz'}];v.call("ui.run='scout';renderConversation()");
 let root=v.nodes.get('conversation-content');assert.equal(descendants(root,'h3')[0].textContent,'Dana Ruiz');
 v.context.state.role='coach';v.call('renderConversation()');assert.equal(descendants(root,'h3')[0].textContent,'Dana Ruiz');
 v.context.state.people[0].name='Dana Ruiz <script>inert</script>';v.call('renderConversation()');assert.equal(descendants(root,'h3')[0].textContent,'Dana Ruiz <script>inert</script>');assert.equal(descendants(root,'script').length,0);
 assert.match(root.children[0].className,/speaker-gm/);
});

test('single-agent triggers are nested under their agent and flows do not invent orchestration', () => {
 const v=view();v.context.state.agents=[{id:'writer',name:'Native writer',model:'fixture'},{id:'reviewer',name:'Native reviewer',model:'fixture'}];v.context.state.triggers=[{name:'scout',type:'manual',agent:'writer'},{name:'feedback',type:'file-added',agent:'reviewer',path:'feedback/*.json'}];
 v.call('renderNavigation()');const root=v.nodes.get('agents');assert.equal(root.children.length,2);
 assert.match(root.children[0].textContent,/Native writer.*When someone asks.*scout/s);assert.doesNotMatch(root.children[0].textContent,/feedback/);
 assert.match(root.children[1].textContent,/Native reviewer.*When a source file matches.*feedback/s);assert.doesNotMatch(root.children[1].textContent,/When someone asks/);
 for(const group of root.children){assert.equal(group.children[0].tagName,'button');const triggers=descendants(group,'ul')[0];assert.ok(triggers);assert.equal(descendants(triggers,'button').length,0);}
 assert.match(v.nodes.get('flows').textContent,/No automatic multi-agent workflow/);assert.doesNotMatch(v.nodes.get('flows').textContent,/Draft day|Scheme fit/);
 assert.equal(v.nodes.has('triggers'),false);
});

test('conversation evidence hides IDs but keeps actor, failure and recorded failure visible', () => {
  const run = { id: 'failed', agent: 'reviewer', status: 'failed', actor: 'coach', session: 'secret-session', base: 'secret-base', error: 'Invalid output. Regenerate with recorded evidence.' };
  const v = view([run]); v.call("ui.run = 'failed'; renderConversation()");
  const root = v.nodes.get('conversation-content');
  const details = descendants(v.nodes.get('conversation-evidence'), 'details')[0];
  assert.ok(details);
  assert.equal(details.open, false);
  assert.match(details.textContent, /secret-session/);
  const visible = root.children.filter(child => child.tagName !== 'details').map(child => child.children.filter(body => body.tagName !== 'details').map(body => body.children.filter(node => node.tagName !== 'details').map(node => node.textContent).join('')).join('')).join('');
  assert.match(visible, /coach/);
  assert.match(visible, /Invalid output/);
  assert.doesNotMatch(visible, /secret-session|secret-base/);
});


test('Markdown review renders a table while retaining full text through persisted checkbox', () => {
  const v = view();
  const section = v.call("documentSection('Proposed replacement', '| Item | Count |\\n| --- | --- |\\n| Source | 22 |', true, false, 'proposal-document')");
  const table = section.children.find(child => child.className === 'document-markdown');
  assert.equal(table.children[0].children[0].tagName, 'table');
  const toggle = section.children.find(child => child.tagName === 'label').children[0];
  assert.equal(toggle.type, 'checkbox');
  toggle.checked = true; toggle.listeners.change();
  assert.equal(section.children.find(child => child.tagName === 'pre').hidden, false);
  const rerender = v.call("documentSection('Proposed replacement', 'full document', true, false, 'proposal-document')");
  assert.equal(rerender.children.find(child => child.tagName === 'label').children[0].checked, true);
});

function descendants(node, tag) {
  return node.children.flatMap(child => [...(child.tagName === tag ? [child] : []), ...descendants(child, tag)]);
}
const proposalText = '# Proposed specification\nExact inert <script>alert(1)</script> source bytes.\n';
const canonical = entries => JSON.stringify({ version: 1, entries });
const boardEntry = { prospect: 'tavon-reyes', rank: 22, tier: 3, reason: 'Retained medical evidence | <script>alert(1)</script> is inert text' };





function visibleText(node) {
  if (node.tagName === 'details' && !node.open) return node.children[0]?.textContent || '';
  if (node.hidden) return '';
  return node._text + node.children.map(visibleText).join(' ');
}
function decisionFixture(extra = {}, data = {}) {
  const run = { id: 'decision-run', session: 'private-session', agent: 'reviewer', requestSummary: 'Review the specification', status: 'proposed', proposal: 'decision-proposal', completed: '2026-01-02T09:42:05Z', seconds: 0, model: 'observed-model', activity: [{ kind: 'snapshot', at: '2026-01-02T09:42:03Z' }, { kind: 'validated', at: '2026-01-02T09:42:04Z' }, { kind: 'finished', at: '2026-01-02T09:42:05Z' }], ...extra };
  const v = view([run]);
  v.context.state.proposals = [{ id: 'decision-proposal', status: data.status || 'pending', attribution: { run: run.id, agent: 'reviewer' }, changes: { 'docs/spec.md': proposalText } }];
  v.context.fetch = async () => reply({ accepted: '# Accepted specification', proposed: proposalText, diff: '+private raw diff', baseCommit: 'private-base', ...data });
  v.call("ui.run = 'decision-run'; ui.path='docs/spec.md'");
  return v;
}

test('conversation uses one review status and plain agent context, keeping model and attempt details in Evidence', () => {
  const v = decisionFixture();
  v.call('renderConversation(); renderRoomComposer()');
  const result = descendants(v.nodes.get('conversation-content'), 'article')[1];
  assert.equal(descendants(result, 'span').filter(node => node.className.includes('badge')).length, 1);
  assert.match(visibleText(result), /Native reviewer.*proposed a document update/is);
  assert.doesNotMatch(visibleText(result), /observed-model|decision-run|0\.0 seconds/);
  assert.doesNotMatch(v.nodes.get('room-context').textContent, /decision-run|Attempt/);
  assert.match(v.nodes.get('conversation-evidence').textContent, /decision-run|observed-model/);
});


// Catch conflating execution completion with approval, or hiding the immutable review target.
test('unknown proposal decisions expose recorded scope without inventing a historical decision', () => {
  const v = view();
  v.context.proposal = { id: 'unknown-proposal', status: 'unknown', changes: { 'strategy.md': 'Recorded replacement' } };
  const scope = v.call('proposalScope(proposal)');
  descendants(scope, 'details')[0].open = true;
  assert.match(visibleText(scope), /Review decision not recorded/);
  assert.doesNotMatch(visibleText(scope), /historical decision|Accept saves/);
  assert.equal(descendants(scope, 'button')[0].textContent, 'Review Strategy');
});



test('finished execution still awaits approval and exposes its immutable proposal revision', async () => {
  const v = decisionFixture();
  Object.assign(v.context.state.proposals[0], { commit: 'proposal-commit-abc', base: 'proposal-base-def' });
  await v.call('renderDocuments()');
  const header = descendants(v.nodes.get('document-panel'), 'div').find(node => node.className.startsWith('proposal-banner'));
  const badges = descendants(header, 'span').filter(node => node.className.includes('badge')).map(node => node.textContent);
  assert.deepEqual(badges, ['Awaiting your approval']);
  const audit = v.nodes.get('document-evidence');
  assert.match(audit.textContent, /Finished/);
  assert.match(audit.textContent, /Proposal: decision-proposal/);
  assert.match(audit.textContent, /Revision: proposal-commit-abc/);
  assert.match(audit.textContent, /Base: proposal-base-def/);
  assert.doesNotMatch(visibleText(header), /Applied|Revision [0-9]/);
  Object.assign(v.context.state.proposals[0], { status: 'accepted', acceptedCommit: 'accepted-checkpoint-xyz' });
  await v.call('renderDocuments()');
  assert.match(visibleText(v.nodes.get('document-panel')), /Accepted/);
  assert.match(v.nodes.get('document-evidence').textContent, /Accepted checkpoint: accepted-checkpoint-xyz.*historical/is);
  assert.doesNotMatch(visibleText(v.nodes.get('document-panel')), /Awaiting approval|Applied/);
});

// Catch omitting changed documents, dropping proposal context on document review,
// or posting the mutable current selection instead of the proposal shown with the action.
test('whole-proposal review exposes every affected replacement and keeps its original action target', async () => {
  const v = decisionFixture({}, { boardImpact: '[]' });
  const proposal = v.context.state.proposals[0];
  proposal.changes['docs/legacy.md'] = '# Legacy text\nExact recorded legacy replacement';
  proposal.changes['docs/source.md'] = '# Source notes\nExact proposed source replacement';
  const original = JSON.stringify(proposal);
  const requests = [];
  v.context.fetch = async (url, options) => {
    requests.push([url, options?.body && JSON.parse(options.body)]);
    if (url === '/api/state') return reply(v.context.state);
    if (url.includes('path=docs%2Fsource')) return reply({ accepted: 'Accepted dossier', proposed: proposal.changes['docs/source.md'], diff: '+Exact proposed source replacement' });
    if (url.includes('path=docs%2Flegacy')) return reply({accepted:'Accepted legacy',proposed:proposal.changes['docs/legacy.md']});
    return reply({ accepted: '# Accepted specification', proposed: proposalText });
  };
  await v.call('renderDocuments()');
  const panel = v.nodes.get('document-panel');
  const scope = descendants(panel, 'details').find(node => node.className === 'scope-detail'); scope.open = true;
  assert.match(visibleText(panel), /Accept saves all affected documents together.*Reject leaves accepted documents unchanged/is);
  assert.match(visibleText(panel), /Product requirements.*docs\/legacy.md.*Source notes/is);
  const controls = descendants(panel, 'button');
  const board = controls.find(node => node.textContent === 'Review Product requirements');
  const generated = controls.find(node => node.textContent === 'Review docs/legacy.md');
  const dossier = controls.find(node => node.textContent === 'Review Source notes');
  assert.ok(board && generated && dossier, 'every changed path needs a direct content review control');
  board.listeners.click(); await tick();
  assert.match(visibleText(panel), /Exact inert <script>/, 'Review opens the actual full replacement');
  const generatedAfterBoard = descendants(panel, 'button').find(node => node.textContent === 'Review docs/legacy.md');
  generatedAfterBoard.listeners.click(); await tick();
  assert.match(visibleText(panel), /Exact recorded legacy replacement/);
  await descendants(panel, 'button').find(node => node.textContent === dossier.textContent).listeners.click(); await tick();
  assert.equal(v.call('ui.path'), 'docs/source.md');
  assert.equal(v.call('ui.proposal'), 'decision-proposal');
  assert.equal(v.call('ui.run'), 'decision-run');
  assert.match(visibleText(panel), /Exact proposed source replacement/);
  assert.ok(requests.some(([url]) => url === '/api/document?path=docs%2Fsource.md&proposal=decision-proposal'));
  const accept = descendants(panel, 'button').find(node => node.textContent === 'Accept proposal');
  v.call("ui.proposal='another-selection'; ui.run=null");
  await accept.listeners.click(); await tick();
  assert.deepEqual(requests.find(([url]) => url === '/api/review'), ['/api/review', { proposal: 'decision-proposal', action: 'accept', role: 'coach' }]);
  assert.equal(JSON.stringify(proposal), original, 'reviewing never rewrites the original recorded proposal');
});

// Catch stale recovery that hides the reason, blocks supported rejection, or edits the old attempt.
test('stale action group explains blocked acceptance and starts a new immutable recorded attempt', async () => {
  const v = decisionFixture({}, { status: 'stale' });
  await v.call('renderDocuments()');
  const actions = descendants(v.nodes.get('document-panel'), 'div').find(node => node.className === 'review-actions');
  assert.match(visibleText(actions), /Stale: accepted documents changed after this proposal was created.*Accept is unavailable/is);
  const accept = descendants(actions, 'button').find(node => node.textContent === 'Accept proposal');
  const reject = descendants(actions, 'button').find(node => node.textContent === 'Reject');
  const again = descendants(actions, 'button').find(node => node.textContent === 'Try again');
  assert.equal(accept.disabled, true); assert.equal(reject.disabled, false);
  assert.ok(again && again.className.includes('primary'));
  assert.match(v.nodes.get('document-evidence').textContent, /new recorded attempt.*latest accepted documents.*new proposal.*not an edit.*No automatic retry/is);
  const oldRun = JSON.stringify(v.context.state.runs[0]), oldProposal = JSON.stringify(v.context.state.proposals[0]);
  const requests = [];
  v.context.fetch = async (url, options) => {
    requests.push([url, options?.body && JSON.parse(options.body)]);
    if (url === '/api/regenerate') return reply({ run: 'new-attempt' });
    if (url === '/api/state') return reply({ ...v.context.state, runs: [...v.context.state.runs, roomRun('new-attempt', { status: 'queued', previousAttempt: 'decision-run', base: 'latest-base' })] });
    return reply({ accepted: proposalText });
  };
  await again.listeners.click(); await tick();
  assert.deepEqual(requests.find(([url]) => url === '/api/regenerate'), ['/api/regenerate', { proposal: 'decision-proposal', role: 'coach' }]);
  assert.equal(v.call('ui.run'), 'new-attempt'); assert.equal(v.call('currentProposal()'), undefined);
  assert.equal(JSON.stringify(v.call('ui.state.runs[0]')), oldRun);
  assert.equal(JSON.stringify(v.call('ui.state.proposals[0]')), oldProposal);
  assert.match(v.nodes.get('notice').textContent, /new recorded attempt.*new proposal/is);
});

test('stale Reject still reviews the original visible proposal without changing accepted documents', async () => {
  const v = decisionFixture({}, { status: 'stale' }); await v.call('renderDocuments()');
  const reject = descendants(v.nodes.get('document-panel'), 'button').find(node => node.textContent === 'Reject');
  const requests = []; const head = v.context.state.head = 'accepted-head';
  v.context.fetch = async (url, options) => {
    requests.push([url, options?.body && JSON.parse(options.body)]);
    if (url === '/api/review') return reply({});
    if (url === '/api/state') return reply({ ...v.context.state, proposals: [{ ...v.context.state.proposals[0], status: 'rejected' }] });
    return reply({ accepted: proposalText });
  };
  await reject.listeners.click(); await tick();
  assert.deepEqual(requests.find(([url]) => url === '/api/review'), ['/api/review', { proposal: 'decision-proposal', action: 'reject', role: 'coach' }]);
  assert.equal(v.call('ui.state.head'), head);
  assert.match(visibleText(v.nodes.get('document-panel')), /Rejected/);
});

test('failed and uncertain recovery is explicitly Try again on latest, never an automatic retry', () => {
  for (const status of ['failed', 'uncertain']) {
    const v = roomView([roomRun('old-attempt', { status })]); v.call('renderConversation()');
    const content = v.nodes.get('conversation-content');
    assert.ok(descendants(content, 'button').some(node => node.textContent === 'Try again on latest'));
    assert.match(visibleText(content), /new recorded attempt.*latest accepted documents.*new proposal/is);
    if (status === 'uncertain') assert.match(visibleText(content), /no automatic retry/i);
    assert.equal(v.call('ui.busy'), false);
  }
});

// Catch the proposal picker leaving Ask targeted at an unrelated previously selected run.
test('selecting a recorded proposal aligns conversation and read-only Ask with its originating attempt', async () => {
  const v = decisionFixture({ questionsSupported: true, created: '2026-01-02T09:41:00Z' });
  const other = roomRun('other-run', { proposal: 'other-proposal', agent: 'writer', session: 'other-session' });
  v.context.state.runs.push(other);
  v.context.state.proposals.push({ id: 'other-proposal', status: 'accepted', attribution: { run: 'other-run', agent: 'writer' }, changes: { 'docs/spec.md': proposalText } });
  v.call("ui.run='other-run'; ui.proposal='other-proposal'; $('room-request').value='Preserve my draft'");
  await v.call('renderDocuments()');
  const select = descendants(v.nodes.get('proposal-picker'), 'select')[0];
  select.value = 'decision-proposal'; select.listeners.change(); await tick();
  assert.equal(v.call('ui.run'), 'decision-run');
  assert.equal(v.call('ui.proposal'), 'decision-proposal');
  assert.match(v.nodes.get('conversation-meta').textContent, /Review the specification/);
  assert.match(v.nodes.get('room-context').textContent, /Ask Native reviewer about Review the specification.*this recorded session.*read-only/is);
  assert.doesNotMatch(v.nodes.get('room-context').textContent, /decision-run/);
  assert.match(v.nodes.get('conversation-evidence').textContent, /decision-run.*private-session/is);
  assert.equal(v.nodes.get('room-request').value, 'Preserve my draft');
  const requests = [];
  v.context.fetch = async (url, options) => { requests.push([url, options?.body && JSON.parse(options.body)]); return reply(url === '/api/state' ? v.context.state : { accepted: proposalText, proposed: proposalText }); };
  await v.call("submitRoom('question')");
  assert.deepEqual(requests.find(([url]) => url === '/api/ask'), ['/api/ask', { run: 'decision-run', request: 'Preserve my draft', role: 'coach' }]);
});

test('missing proposal associations and accepted-content-only never invent an Ask source or approval', async () => {
  const v = decisionFixture({ questionsSupported: true });
  v.context.state.documents.push('docs/source.md');
  v.context.state.proposals.push({ id: 'unassociated', status: 'pending', attribution: { run: 'missing-run' }, changes: { 'docs/spec.md': proposalText, 'docs/source.md': 'Unassociated dossier' } });
  await v.call('renderDocuments()');
  let select = descendants(v.nodes.get('proposal-picker'), 'select')[0];
  select.value = 'unassociated'; select.listeners.change(); await tick();
  assert.equal(v.call('ui.run'), null);
  assert.equal(v.nodes.get('room-ask').disabled, true);
  assert.match(v.nodes.get('room-context').textContent, /Originating attempt not recorded.*no Ask source/is);
  assert.doesNotMatch(visibleText(v.nodes.get('document-panel')), /Proposal accepted/);
  descendants(v.nodes.get('document-tabs'), 'button').find(node => node.textContent === 'Source notes').listeners.click(); await tick();
  assert.equal(v.call('ui.proposal'), 'unassociated');
  assert.equal(v.call('ui.run'), null);
  // Return to Board, then deliberately choose accepted content only.
  descendants(v.nodes.get('document-tabs'), 'button').find(node => node.textContent === 'Product requirements').listeners.click(); await tick();
  select = descendants(v.nodes.get('proposal-picker'), 'select')[0];
  select.value = ''; select.listeners.change(); await tick();
  assert.equal(v.call('ui.run'), null);
  assert.equal(v.nodes.get('room-ask').disabled, true);
  assert.match(v.nodes.get('room-context').textContent, /Accepted content only.*no Ask source/is);
  descendants(v.nodes.get('document-tabs'), 'button').find(node => node.textContent === 'Source notes').listeners.click(); await tick();
  assert.equal(v.call('ui.proposal'), '');
});

test('ambiguous legacy attribution never chooses an unrelated review decision or Ask source', async () => {
  const v = decisionFixture({ questionsSupported: true });
  delete v.context.state.runs[0].proposal;
  const pending = v.context.state.proposals[0];
  const accepted = { ...pending, id: 'other-accepted-proposal', status: 'accepted' };
  v.context.state.proposals = [accepted, pending];
  assert.equal(v.call('runProposal(ui.state.runs[0])'), undefined, 'ambiguous run attribution cannot imply an accepted decision');
  await v.call('renderDocuments()');
  const select = descendants(v.nodes.get('proposal-picker'), 'select')[0];
  select.value = pending.id; select.listeners.change(); await tick();
  assert.equal(v.call('ui.proposal'), pending.id);
  assert.equal(v.call('ui.run'), null, 'ambiguous fallback attribution is not an exact question source');
  assert.equal(v.nodes.get('room-ask').disabled, true);
  assert.match(v.nodes.get('room-context').textContent, /not recorded or ambiguous.*no Ask source/is);
  assert.doesNotMatch(v.nodes.get('conversation-content').textContent, /Proposal accepted/);
  assert.match(visibleText(v.nodes.get('document-panel')), /Awaiting your approval/);
});

test('contradictory legacy attribution cannot override a run’s explicit recorded proposal association', async () => {
  const v = decisionFixture({ questionsSupported: true });
  v.context.state.proposals.push({ id: 'contradictory', status: 'accepted', attribution: { run: 'decision-run' }, changes: { 'docs/spec.md': proposalText } });
  await v.call('renderDocuments()');
  const select = descendants(v.nodes.get('proposal-picker'), 'select')[0];
  select.value = 'contradictory'; select.listeners.change(); await tick();
  assert.equal(v.call('ui.run'), null);
  assert.equal(v.nodes.get('room-ask').disabled, true);
  assert.match(v.nodes.get('room-context').textContent, /Originating attempt not recorded/);
  assert.doesNotMatch(visibleText(v.nodes.get('conversation-content')), /Proposal accepted/);
});

// Non-board proposals must retain a saved-proof route after a fresh page load.
for (const status of ['pending', 'accepted']) {
  test(`reloaded ${status} strategy proposal opens its saved proof without a POST`, async () => {
    const proof = { chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0, head: 'strategy-saved-head' };
    const run = scenario('strategy-proof', 'Stay put', { proposal: 'strategy-proposal', proof });
    const v = roomView([run]);
    v.context.state.proposals = [{ id: 'strategy-proposal', status, changes: { 'strategy.md': 'Recorded strategy' }, attribution: { run: run.id } }];
    v.call("ui.path='strategy.md'; ui.run='strategy-proof'; renderConversation()");
    assert.equal(v.call('ui.verified.size'), 0, 'fresh load has no local verification flag');
    const evidence = v.nodes.get('conversation-evidence');
    const saved = descendants(evidence, 'button').find(node => node.textContent === 'Replay verified · open saved proof');
    assert.ok(saved, 'returned proof must remain reachable for a non-board proposal');
    v.context.fetch = () => { throw Error('Opening saved proof must not call an API'); };
    saved.listeners.click();
    const region = descendants(evidence, 'div').find(node => node.getAttribute('aria-label') === 'Recorded replay integrity result');
    assert.ok(region && region.focused && region.revealed);
    assert.match(region.textContent, /strategy-saved-head/);
  });
}

// Catch a passive badge, a wrong-run evidence route, or re-verification merely to display saved proof.
test('Replay verified opens that proposal’s saved integrity proof and chronology in one click without a POST', async () => {
  const proof = { chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0, records: 31, head: 'correct-saved-proof-head' };
  const v = decisionFixture({ proof });
  v.context.state.runs.push(roomRun('unrelated', { proof: { ...proof, head: 'wrong-proof-head' } }));
  v.call("ui.run='unrelated'; ui.proposal='decision-proposal'");
  const requests = [];
  v.context.fetch = async (url, options) => { requests.push([url, options?.method || 'GET']); return reply({ accepted: proposalText, proposed: proposalText }); };
  await v.call('renderDocuments()');
  const panel = v.nodes.get('document-panel');
  const verified = descendants(v.nodes.get('document-evidence'), 'button').find(node => node.textContent === 'Replay verified · open saved proof');
  assert.ok(verified, 'saved verification must have an accessible actionable control');
  assert.ok(verified.getAttribute('aria-controls')?.startsWith('proof-document-'));
  verified.listeners.click();
  const evidence = descendants(v.nodes.get('document-evidence'), 'details').find(node => node.open);
  assert.ok(evidence);
  assert.equal(v.nodes.get('evidence-panel').hidden, false);
  const proofRegion = descendants(evidence, 'div').find(node => node.getAttribute('aria-label') === 'Recorded replay integrity result');
  assert.ok(proofRegion && proofRegion.focused, 'saved proof focuses its actual integrity result');
  assert.match(visibleText(evidence), /Journal chain.*Verified.*Outputs match.*Verified.*Model calls during reconstruction.*0/is);
  assert.match(visibleText(evidence), /correct-saved-proof-head/);
  assert.doesNotMatch(visibleText(evidence), /wrong-proof-head/);
  assert.match(visibleText(evidence), /Full recorded chronology.*Finished/is);
  assert.equal(v.call('ui.proposal'), 'decision-proposal');
  assert.equal(v.call('ui.run'), 'unrelated', 'displaying saved proof does not silently change selection');
  assert.equal(v.call('ui.busy'), false);
  assert.equal(requests.filter(([, method]) => method === 'POST').length, 0);
});

test('long immutable proposal revisions stay compact without losing full identity or changing review payload', async () => {
  const v = decisionFixture();
  Object.assign(v.context.state.proposals[0], { id: 'proposal-1234567890abcdef1234567890abcdef', commit: 'abcdef0123456789abcdef0123456789abcdef0123', base: '1234567890abcdef1234567890abcdef1234567890' });
  v.context.state.runs[0].proposal = v.context.state.proposals[0].id;
  await v.call('renderDocuments()');
  const panel = v.nodes.get('document-panel');
  const header = v.nodes.get('document-evidence');
  assert.doesNotMatch(visibleText(panel), /abcdef012345|1234567890ab/);
  assert.match(header.textContent, /Revision: abcdef012345…/);
  assert.match(header.textContent, /Base: 1234567890ab…/);
  const identities = descendants(header, 'span').filter(node => node.getAttribute('title'));
  assert.ok(identities.some(node => node.getAttribute('title') === 'abcdef0123456789abcdef0123456789abcdef0123'));
  assert.match(v.nodes.get('document-evidence').textContent, /abcdef0123456789abcdef0123456789abcdef0123/);
  const requests = [];
  v.context.fetch = async (url, options) => { requests.push([url, options?.body && JSON.parse(options.body)]); return reply(url === '/api/state' ? v.context.state : { accepted: proposalText }); };
  await descendants(panel, 'button').find(node => node.textContent === 'Reject').listeners.click();
  assert.deepEqual(requests.find(([url]) => url === '/api/review'), ['/api/review', { proposal: 'proposal-1234567890abcdef1234567890abcdef', action: 'reject', role: 'coach' }]);
});

test('running execution shows its recorded start instead of a fabricated zero elapsed duration', async () => {
  const v = decisionFixture({ status: 'running', seconds: 0, started: '2026-01-02T09:42:01Z', completed: undefined });
  await v.call('renderDocuments()'); v.call('renderNavigation(); renderConversation()');
  for (const id of ['document-evidence', 'conversation-meta', 'conversation-evidence', 'sessions']) {
    assert.match(v.nodes.get(id).textContent, /Running|running/, id);
    assert.doesNotMatch(visibleText(v.nodes.get(id)), /0\.0 seconds(?: so far)?/, id);
  }
  assert.ok(descendants(v.nodes.get('document-evidence'), 'time').some(node => node.getAttribute('datetime') === '2026-01-02T09:42:01Z'));
  assert.equal(v.context.state.runs[0].seconds, 0);
});

test('decision displays genuine recorded steps, observed model and finished zero time with folded evidence', async () => {
  const v = decisionFixture();
  v.context.state.agents = [{ id: 'reviewer', name: 'Native reviewer', model: 'changed-current-model' }];
  await v.call('renderDocuments()');
  const panel = v.nodes.get('document-panel');
  const visible = visibleText(panel);
  assert.match(visible, /Native reviewer proposal/);
  assert.match(v.nodes.get('document-evidence').textContent, /Recommendation validated/);
  assert.doesNotMatch(visible, /Inputs checkpointed/);
  assert.doesNotMatch(visible, /observed-model|0\.0 seconds/);
  const audit = v.nodes.get('document-evidence');
  assert.match(audit.textContent, /observed-model/);
  assert.match(audit.textContent, /0\.0 seconds/);
  assert.ok(descendants(audit, 'time').some(time => time.getAttribute('datetime') === '2026-01-02T09:42:05Z'), 'Evidence retains actual completion time');
  assert.doesNotMatch(visible, /changed-current-model/);
  assert.match(visible, /private raw diff/);
  assert.match(visible,/Exact inert <script>/);
  assert.equal(descendants(panel,'script').length,0);
  assert.ok(descendants(v.nodes.get('document-evidence'), 'details').some(node => node.children[0].textContent === 'Evidence' && !node.open));
  assert.ok(descendants(audit, 'button').some(node => node.textContent === 'Verify replay & reconstruction'));
  v.context.state.agents[0].model = 'another-current-model';
  await v.call('renderDocuments()');
  assert.match(v.nodes.get('document-evidence').textContent, /observed-model/);
  assert.doesNotMatch(v.nodes.get('document-evidence').textContent, /another-current-model/);
});

test('decision never invents old activity, timing or observed model and preserves failure states', async () => {
  for (const [extra, label] of [[{ status: 'failed' }, 'Failed'], [{ status: 'interrupted' }, 'Interrupted'], [{ status: 'uncertain' }, 'Interrupted'], [{ error: 'Recorded failure' }, 'Needs attention'], [{ status: 'queued' }, 'Queued'], [{ status: 'running' }, 'Running'], [{ completed: '0001-01-01T00:00:00Z' }, 'Proposed']]) {
    const v = decisionFixture({ ...extra, model: undefined, seconds: undefined, activity: undefined });
    await v.call('renderDocuments()');
    const panel = v.nodes.get('document-panel');
    const badges = descendants(v.nodes.get('document-evidence'), 'span').filter(node => node.className.includes('badge')).map(node => node.textContent);
    assert.ok(badges.includes(label), `${label}: ${badges}`);
    assert.ok(!badges.includes('Finished'));
    assert.match(v.nodes.get('document-evidence').textContent, /Activity not recorded/);
    assert.match(v.nodes.get('document-evidence').textContent, /Model not recorded/);
    assert.match(v.nodes.get('document-evidence').textContent, /Time not recorded/);
  }
});

test('legacy recovered publication is not finished without a recorded completion milestone', () => {
  const v = view();
  v.context.run = { status: 'proposed', completed: '2026-01-02T09:42:05Z', error: '' };
  assert.equal(v.call('runDecisionStatus(run).textContent'), 'Proposed');
  for (const activity of [[{ kind: 'recovered', at: '2026-01-02T09:42:05Z' }], [{ kind: 'finished' }], [{ kind: 'finished', at: '0001-01-01T00:00:00Z' }]]) {
    v.context.run.activity = activity;
    assert.equal(v.call('runDecisionStatus(run).textContent'), 'Proposed');
  }
  v.context.run.activity = [{ kind: 'finished', at: '2026-01-02T09:42:05Z' }];
  assert.equal(v.call('runDecisionStatus(run).textContent'), 'Finished');
  v.context.run.error = 'Bookkeeping failure';
  assert.equal(v.call('runDecisionStatus(run).textContent'), 'Needs attention');
});

test('successful recovery appears as a timestamped Resumed step in recorded chronology', async () => {
  const v = decisionFixture({ activity: [{ kind: 'resumed', at: '2026-01-02T09:42:05Z' }] });
  await v.call('renderDocuments()');
  const activity = descendants(v.nodes.get('document-evidence'), 'section').find(node => node.className === 'decision-activity');
  assert.match(activity.textContent, /Resumed/);
  assert.deepEqual(descendants(activity, 'time').map(node => node.getAttribute('datetime')), ['2026-01-02T09:42:05Z']);
});

test('decision skips unknown and untimestamped activity instead of deriving milestones from start time', async () => {
  const v = decisionFixture({ started: '2026-01-02T09:42:01Z', activity: [{ kind: 'snapshot', at: '0001-01-01T00:00:00Z' }, { kind: 'tool-read', at: '2026-01-02T09:42:03Z' }, { kind: 'validated' }, { kind: 'output-received', at: '2026-01-02T09:42:04Z' }] });
  await v.call('renderDocuments()');
  const panel = v.nodes.get('document-panel');
  const activity = descendants(v.nodes.get('document-evidence'), 'section').find(node => node.className === 'decision-activity');
  assert.match(activity.textContent, /Output received/);
  assert.doesNotMatch(activity.textContent, /Inputs checkpointed|Recommendation validated|tool-read|Execution started/);
  assert.deepEqual(descendants(activity, 'time').map(node => node.getAttribute('datetime')), ['2026-01-02T09:42:04Z']);
});

test('decision reports invalid duration as missing rather than coerced zero or a nonfinite number', async () => {
  for (const seconds of ['', '0', null, NaN, Infinity, -1]) {
    const v = decisionFixture({ seconds }); await v.call('renderDocuments()');
    assert.match(v.nodes.get('document-evidence').textContent, /Time not recorded/);
  }
});






test('document cache refreshes final run metadata and selected lineage even with unchanged accepted head', async () => {
  const v = decisionFixture({ status: 'running', completed: undefined, model: undefined, activity: undefined });
  await v.call('renderDocuments()');
  v.context.state.runs[0].status = 'proposed';
  v.context.state.runs[0].completed = '2026-01-02T09:42:05Z';
  v.context.state.runs[0].model = 'historical-observed-model';
  v.context.state.runs[0].activity = [{ kind: 'finished', at: '2026-01-02T09:42:05Z' }];
  await v.call('renderDocuments()');
  assert.match(v.nodes.get('document-evidence').textContent, /Finished.*historical-observed-model/);
  let requests = 0;
  v.context.fetch = async () => { requests++; return { ok: true, json: async () => ({ accepted: proposalText, proposed: proposalText, knockOns: '[]' }) }; };
  Object.assign(v.context.state.runs[0], { previousAttempt:'prior-attempt', parentSession:'prior-session', forkSeq:7, comparison:'recorded-lineage' });
  await v.call('renderDocuments()');
  assert.equal(requests, 1);
  assert.match(v.nodes.get('document-evidence').textContent,/prior-attempt.*prior-session/s);
});

test('decision replay badge requires returned proof with all positive flags and zero model calls', async () => {
  for (const [proof, expected] of [[undefined, false], [{ chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0 }, true], [{ chainValid: true, reconstructed: true, outputsMatch: false, liveUnchanged: true, modelCalls: 0 }, false], [{ chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 1 }, false]]) {
    const v = decisionFixture({ proof }); await v.call('renderDocuments()');
    assert.equal(v.nodes.get('document-evidence').textContent.includes('Replay verified'), expected);
  }
});




test('queued scenario never displays an unrelated or superseded pending proposal', () => {
  const v = view([scenario('old', 'Trade up', { comparison: 'one', proposal: 'old-proposal' }), scenario('new', 'Trade up', { comparison: 'one', previousAttempt: 'old', status: 'queued' })]);
  v.context.state.proposals = [{ id: 'old-proposal', status: 'pending', changes: { 'strategy.md': 'Earlier strategy' } }];
  v.call("ui.run = 'new'; ui.path = 'strategy.md'");
  assert.equal(v.call('currentProposal()'), undefined);
});



test('conversation revision evidence uses recorded provenance, not current configuration', () => {
  const run = {id:'old',agent:'reviewer',status:'proposed',proposal:'p'};
  const v = view([run]); v.context.state.agents = [{id:'reviewer',digest:'current-bundle',model:'current-model'}];
  v.context.state.proposals = [{id:'p',attribution:{run:'old',bundleDigest:'historical-bundle'},changes:{'docs/legacy.md':'synthetic'}}];
  v.call("ui.run='old'; renderConversation()");
  assert.match(v.nodes.get('conversation-evidence').textContent, /historical-bundle/);
  assert.match(v.nodes.get('conversation-evidence').textContent, /Current configuration/);
});

test('conversation evidence distinguishes authored and selected execution contracts', () => {
  const v = view([{ id: 'selected', agent: 'writer', status: 'proposed', proposal: 'p', authoredBundleDigest: 'authored-contract' }]);
  v.context.state.agents = [{ id: 'writer', digest: 'selected-execution', authoredDigest: 'current-authored', model: 'gpt-example' }];
  v.context.state.proposals = [{ id: 'p', attribution: { run: 'selected', bundleDigest: 'selected-execution' }, changes: {} }];
  v.call("ui.run='selected'; renderConversation()");
  const evidence = descendants(v.nodes.get('conversation-evidence'), 'details')[0];
  assert.equal(evidence.open, false);
  assert.match(evidence.textContent, /authored-contract/);
  assert.match(evidence.textContent, /selected-execution/);
  assert.match(evidence.textContent, /current-authored/);
});



test('decision shows only declared timestamped producer reads, folding full chronology', async () => {
  const at='2026-01-02T09:42:03Z';
  const activity=[{kind:'snapshot',at},{kind:'execution-started',at},{kind:'model-requested',at},...['docs/spec.md','docs/source.md'].map(document=>({kind:'input-read',at,document})),{kind:'input-read',at,document:'private/unrecognized'},{kind:'input-read',document:'docs/spec.md'},{kind:'validated',at},{kind:'proposed',at},{kind:'finished',at}];
  const v=decisionFixture({activity}); v.context.state.documents.push('docs/source.md');
  await v.call('renderDocuments()');
  const compact=descendants(v.nodes.get('document-evidence'),'section').find(node=>node.className==='decision-activity');
  for(const label of ['Loaded Product requirements','Loaded Source notes','Recommendation validated','Proposal published']) assert.ok(compact.textContent.includes(label),label);
  assert.doesNotMatch(compact.textContent,/Inputs checkpointed|Execution started|Model request recorded|Finished|private\/unrecognized/);
  assert.equal(descendants(compact,'time').length,4);
  assert.ok(descendants(compact,'time').every(time=>time.getAttribute('datetime')===at));
  const evidence=descendants(v.nodes.get('document-evidence'),'details')[0];
  assert.equal(evidence.open,false);
  assert.match(evidence.textContent,/Inputs checkpointed.*Execution started.*Model request recorded/s);
  assert.doesNotMatch(evidence.textContent,/private\/unrecognized/);
});
test('declared historical Markdown loads remain visible in recorded activity', async () => {
  const v=decisionFixture({activity:[{kind:'input-read',at:'2026-01-02T09:42:03Z',document:'docs/legacy.md'}]});
  v.context.state.documents.push('docs/legacy.md');
  await v.call('renderDocuments()');
  assert.match(v.nodes.get('document-evidence').textContent,/Loaded docs\/legacy.md/);
});

test('selected run without a proposal retains its recorded chronology in one-click audit evidence', () => {
  const v = roomView([roomRun('failed', { status: 'failed', activity: [{ kind: 'failed', at: '2026-03-04T12:30:00Z' }] })]);
  v.call('renderConversation()');
  const launcher = descendants(v.nodes.get('conversation-content'), 'button').find(node => node.textContent === 'Evidence');
  launcher.listeners.click();
  const audit = v.nodes.get('conversation-evidence');
  assert.match(audit.textContent, /Full recorded chronology/);
  assert.equal(descendants(audit, 'time')[0].getAttribute('datetime'), '2026-03-04T12:30:00Z');
});

test('failed agent keeps its original summary in folded Evidence without a proposal', () => {
  const reason='Recorded explanation for invalid output, retained for review.';
  const v=roomView([roomRun('failed-review',{agent:'reviewer',status:'failed',error:'Output validation failed.',summary:reason})]);
  v.call('renderConversation()');
  assert.match(visibleText(v.nodes.get('conversation-content')),/Output validation failed/);
  assert.match(v.nodes.get('conversation-evidence').textContent,new RegExp(reason));
});
test('document decisions never imply producer reads absent from recorded activity', async () => {
  const v = decisionFixture(); await v.call('renderDocuments()');
  assert.match(v.nodes.get('document-evidence').textContent, /Inputs checkpointed/);
  assert.doesNotMatch(v.nodes.get('document-evidence').textContent, /Loaded Product requirements|Loaded Source notes/);
});




const runNotice = 'Agent run requested. Accepted documents stay unchanged until review.';
test('selecting another agent clears obsolete run notice, but retains acceptance and error feedback', async () => {
  const v = view([{ id: 'scout', agent: 'writer' }, { id: 'reviewer', agent: 'reviewer', status: 'queued' }]);
  v.context.fetch = async () => reply({ accepted: '' });
  v.call("ui.run = 'scout'; ui.agent = 'writer'");
  v.context.notice = runNotice; v.call("notify(notice, false, {kind:'run',run:'scout',agent:'writer'}); selectRun(state.runs[1])");
  assert.equal(v.nodes.get('notice').hidden, true);
  for (const [message, error] of [['Accepted. Rank #4 → 22', false], ['Review failed', true]]) {
    v.context.notice = message; v.context.error = error; v.call("notify(notice, error); selectRun(state.runs[0]); selectRun(state.runs[1])");
    assert.equal(v.nodes.get('notice').hidden, false); assert.equal(v.nodes.get('notice').textContent, message);
  }
});

test('explicit proposal selection clears obsolete run notice even if Conversation retains another run', async () => {
  const v = decisionFixture();
  v.context.state.runs.push({ id: 'scout', agent: 'writer', status: 'proposed' });
  v.call("ui.run = 'scout'; ui.agent = 'writer'");
  v.context.notice = runNotice; v.call("notify(notice, false, {kind:'run',run:'scout',agent:'writer'}); ui.proposal = 'decision-proposal'; renderState()");
  assert.equal(v.nodes.get('notice').hidden, true);
  await tick();
});



test('same-selection polls retain the genuine agent operation notice', async () => {
  const v = view([{ id: 'scout', agent: 'writer', status: 'queued' }]);
  v.call("ui.run = 'scout'; ui.agent = 'writer'"); v.context.notice = runNotice; v.call("notify(notice, false, {kind:'run',run:'scout',agent:'writer'})");
  v.context.fetch = async url => reply(url === '/api/state' ? v.context.state : { accepted: '' });
  await v.call('refreshState()');
  assert.equal(v.nodes.get('notice').hidden, false); assert.equal(v.nodes.get('notice').textContent, runNotice);
});

test('newly selected recorded run during polling clears only obsolete selected-run operation notice', async () => {
  const v = view([{ id: 'scout', agent: 'writer' }]);
  v.call("ui.run = 'scout'; ui.agent = 'writer'"); v.context.notice = runNotice; v.call("notify(notice, false, {kind:'run',run:'scout',agent:'writer'})");
  v.call("ui.run='reviewer'; ui.agent='reviewer'");
  v.context.state.runs.push({ id: 'reviewer', agent: 'reviewer', prospect: 'tavon-reyes', status: 'queued' });
  v.context.fetch = async url => reply(url === '/api/state' ? v.context.state : { accepted: '' });
  await v.call('refreshState()');
  assert.equal(v.call('ui.run'), 'reviewer'); assert.equal(v.nodes.get('notice').hidden, true);
  v.call("notify('Accepted. Rank #4 → 22')"); await v.call('refreshState()');
  assert.equal(v.nodes.get('notice').hidden, false); assert.match(v.nodes.get('notice').textContent, /Accepted/);
  v.call("notify('Current review error', true)"); await v.call('refreshState()');
  assert.equal(v.nodes.get('notice').className, 'notice error'); assert.match(v.nodes.get('notice').textContent, /Current review error/);
});

test('fixture provider guidance never relabels authored or recorded observed model and leaves notices intact', () => {
  const v=view([{id:'old',agent:'writer',model:'recorded-model',status:'proposed'}]);
  v.context.state.agents=[{id:'writer',model:'authored-model'}];
  v.call("ui.run='old'; ui.fixtureProvider=true; notify('Accepted documents saved.'); renderModelGuidance()");
  assert.equal(v.nodes.get('provider-guidance').hidden,false);
  assert.match(v.nodes.get('provider-guidance').textContent,/Deterministic fixture provider.*No real inference.*authored identity.*not execution qualification/s);
  assert.equal(v.context.state.runs[0].model,'recorded-model');
  assert.equal(v.context.state.agents[0].model,'authored-model');
  assert.equal(v.nodes.get('notice').textContent,'Accepted documents saved.');
  v.call('ui.fixtureProvider=false; renderModelGuidance()');
  assert.equal(v.nodes.get('provider-guidance').hidden,true);
});
test('role updates preserve important current feedback rather than replacing it with demo-control notice', async () => {
  const v = view([], { handlers: true });
  v.context.fetch = async url => reply(url === '/api/state' ? v.context.state : { accepted: '' });
  for (const [message, error] of [['Accepted. Rank #4 → 22', false], ['Important warning', true]]) {
    v.context.notice = message; v.context.error = error; v.call("notify(notice, error); $('role').value = 'GM'");
    await v.nodes.get('role').listeners.change(); await tick();
    assert.equal(v.nodes.get('notice').textContent, message);
  }
});

const tick = () => new Promise(resolve => setImmediate(resolve));
const reply = data => ({ ok: true, json: async () => data });

test('render failures are reported apart from service outages', async () => {
  const v = view();
  const logged = [];
  v.context.console = { error: error => logged.push(error) };
  v.context.fetch = async () => { throw new Error('connection refused'); };
  await v.call('refreshState()');
  assert.match(v.nodes.get('connection').textContent, /Local service unavailable: connection refused/);
  v.context.fetch = async url => reply(url === '/api/state' ? v.context.state : { accepted: '' });
  v.call("renderState = () => { throw new Error('view broke'); }");
  await v.call('refreshState()');
  assert.equal(logged.length, 1);
  assert.match(v.nodes.get('connection').textContent, /could not show the latest state: view broke/);
  assert.doesNotMatch(v.nodes.get('connection').textContent, /unavailable/);
});

test('polled completion announcements are concise, one-time and silent for initial history or clocks', async () => {
  const v = roomView([roomRun('active', { status: 'running' }), roomRun('historic', { status: 'failed' })]);
  v.context.state.conversation = [roomQuestion({ id: 'active-question', status: 'running' }), roomQuestion({ id: 'historic-question', status: 'answered', answer: 'Historic answer' })];
  v.context.fetch = async () => reply({ accepted: proposalText });
  const status = v.call("$('poll-status')");
  let text = '', writes = 0;
  Object.defineProperty(status, 'textContent', { get: () => text, set: value => { writes++; text = String(value); } });
  v.call('renderState()'); await tick();
  assert.equal(writes, 0, 'no initial historic announcement');
  v.context.state.runs[0].seconds = 30;
  v.context.state.conversation[0].seconds = 20;
  v.call('renderState()'); await tick();
  assert.equal(writes, 0, 'poll clocks stay silent');
  Object.assign(v.context.state.runs[0], { status: 'proposed', proposal: 'new-proposal' });
  v.context.state.proposals.push({ id: 'new-proposal', status: 'pending', attribution: { run: 'active' }, changes: { 'docs/spec.md': proposalText } });
  v.context.state.conversation[0].status = 'answered';
  v.context.state.conversation[0].answer = 'Long original answer must not be read aloud as a whole conversation.';
  v.call("notify('Accepted. Rank #4 → 7'); $('room-request').value='Keep draft'; renderState()"); await tick();
  assert.equal(writes, 1);
  assert.match(text, /proposal recorded.*awaiting approval.*question answered/is);
  assert.doesNotMatch(text, /Long original answer|seconds|Historic|Accepted/);
  assert.equal(text.match(/awaiting approval/gi).length, 1, 'publication and pending approval are one notice');
  assert.ok(text.length < 200);
  assert.equal(v.nodes.get('notice').textContent, 'Accepted. Rank #4 → 7');
  assert.equal(v.nodes.get('room-request').value, 'Keep draft');
  v.call('renderState()'); await tick();
  v.context.state.runs[0].seconds = 42; v.context.state.proposals[0].status = 'accepted';
  v.call('renderState()'); await tick();
  assert.equal(writes, 1, 'stable polls and local review decisions do not duplicate notices');
});

test('published execution errors announce needs attention once without exposing private error text or inventing failure', async () => {
  for (const initialStatus of ['running', 'proposed']) {
    const v = roomView([roomRun('published', { status: initialStatus, proposal: 'p', completed: '2026-03-04T12:30:00Z', activity: [{ kind: 'finished', at: '2026-03-04T12:30:00Z' }] })]);
    v.context.state.proposals = [{ id: 'p', status: 'pending', attribution: { run: 'published' }, changes: { 'docs/spec.md': proposalText } }];
    v.context.fetch = async () => reply({ accepted: proposalText });
    const status = v.call("$('poll-status')"); let writes = 0, text = '';
    Object.defineProperty(status, 'textContent', { get: () => text, set: value => { writes++; text = String(value); } });
    v.call('renderState()'); await tick();
    Object.assign(v.context.state.runs[0], { status: 'proposed', error: 'Private /local/path bookkeeping failure retained in evidence' });
    v.call('renderState()'); await tick();
    assert.equal(writes, 1, initialStatus);
    assert.match(text, /proposal recorded.*needs attention.*awaiting approval/is);
    assert.doesNotMatch(text, /Private|local\/path|bookkeeping|failed|Finished|Applied/i);
    v.call('renderState()'); await tick();
    v.context.state.runs[0].seconds = 12;
    v.context.state.runs[0].error = 'Changed private error detail';
    v.call('renderState()'); await tick();
    assert.equal(writes, 1, 'stable error presence and duration never repeat');
    assert.equal(v.context.state.runs[0].status, 'proposed');
  }
});

for (const terminal of ['failed', 'uncertain']) {
  test(`new run and question ${terminal} transitions announce once without erasing errors`, async () => {
    const v = roomView([roomRun('active', { status: 'running' })]);
    v.context.state.conversation = [roomQuestion({ status: 'queued' })];
    v.context.fetch = async () => reply({ accepted: '' });
    v.call('renderState()'); await tick();
    Object.assign(v.context.state.runs[0], { status: terminal });
    Object.assign(v.context.state.conversation[0], { status: terminal });
    v.call("notify('Important review error', true); renderState()"); await tick();
    const text = v.nodes.get('poll-status').textContent;
    assert.match(text, new RegExp(terminal === 'uncertain' ? 'interrupted' : 'failed', 'i'));
    assert.match(text, /Read-only question/);
    assert.equal(v.nodes.get('notice').textContent, 'Important review error');
    assert.equal(v.nodes.get('notice').hidden, false);
  });
}

test('a new terminal run or unassociated recorded pending proposal after initial load is announced', async () => {
  const v = roomView([]); v.context.fetch = async () => reply({ accepted: '' });
  v.call('renderState()'); await tick();
  v.context.state.runs.push(roomRun('fast', { status: 'failed' }));
  v.context.state.proposals.push({ id: 'recorded-pending', status: 'pending', attribution: { agent: 'writer' }, changes: { 'strategy.md': 'Original' } });
  v.call('renderState()'); await tick();
  assert.match(v.nodes.get('poll-status').textContent, /failed.*Native writer proposal.*awaiting approval/is);
  assert.doesNotMatch(v.nodes.get('poll-status').textContent, /recorded-pending/);
});

for (const arrival of ['pending-first', 'completion-first', 'together']) {
  test(`${arrival} publication announces named approval once while retaining completion`, () => {
    const v = roomView([roomRun('publishing', { status: 'running', prospect: 'tavon-reyes' })]);
    const status = v.call("$('poll-status')"), notices = [];
    Object.defineProperty(status, 'textContent', { get: () => notices.at(-1) || '', set: value => notices.push(String(value)) });
    v.call('announceTransitions()');
    const proposal = { id: 'opaque-published-id', status: 'pending', attribution: { agent: 'writer', run: 'publishing' }, changes: { 'docs/spec.md': proposalText } };
    const complete = () => Object.assign(v.context.state.runs[0], { status: 'proposed', proposal: proposal.id, completed: '2026-03-04T12:30:00Z', activity: [{ kind: 'finished', at: '2026-03-04T12:30:00Z' }] });
    if (arrival === 'completion-first') complete();
    else v.context.state.proposals.push(proposal);
    if (arrival === 'together') complete();
    v.call('announceTransitions()');
    if (arrival === 'completion-first') v.context.state.proposals.push(proposal);
    else complete();
    v.call('announceTransitions()');
    const spoken = notices.join(' ');
    assert.match(spoken, /Native writer/);
    assert.match(spoken, /finished/);
    assert.doesNotMatch(spoken, /opaque-published-id/);
    assert.equal((spoken.match(/awaiting approval/gi) || []).length, 1, 'approval is announced once regardless of run/proposal arrival order');
    const writes = notices.length;
    v.context.state.runs[0].seconds = 80;
    v.call('announceTransitions()');
    assert.equal(notices.length, writes, 'unchanged state and duration polls do not repeat notices');
  });
}

test('repeated polls leave an unchanged connection banner alone', async () => {
  const v = view();
  const connection = v.call("$('connection')");
  let text = '', writes = 0;
  Object.defineProperty(connection, 'textContent', { get: () => text, set: value => { writes++; text = String(value); } });
  v.context.fetch = async () => { throw new Error('down'); };
  await v.call('refreshState()');
  await v.call('refreshState()');
  await v.call('refreshState()');
  assert.equal(writes, 1);
  assert.equal(connection.hidden, false);
});

test('a mutation never reuses a state request that started before its POST', async () => {
  const v = view();
  const pending = [];
  v.context.fetch = url => new Promise(resolve => pending.push({ url, resolve }));
  const states = () => pending.filter(request => request.url === '/api/state');
  const poll = v.call('refreshState()');
  const mutation = v.call("mutate('/api/role', { role: 'GM' })");
  await tick();
  pending.find(request => request.url === '/api/role').resolve(reply({}));
  await tick();
  assert.equal(states().length, 1, 'the fresh request waits for the older one');
  states()[0].resolve(reply({ ...v.context.state, role: 'stale' }));
  await poll; await tick();
  assert.notEqual(v.call('ui.state.role'), 'stale', 'pre-mutation state is never shown');
  assert.equal(states().length, 2);
  states()[1].resolve(reply({ ...v.context.state, role: 'GM' }));
  await mutation;
  assert.equal(v.call('ui.state.role'), 'GM');
});

test('error responses with a null body still report the status', async () => {
  const v = view();
  v.context.fetch = async () => ({ ok: false, status: 503, json: async () => null });
  await assert.rejects(v.call("api('/api/state')"), error => /Request failed \(503\)/.test(error.message));
});

test('whitespace-only form input shows a validation message', () => {
  const v = view();
  const field = value => ({ value, message: null, reported: false, setCustomValidity(message) { this.message = message; }, reportValidity() { this.reported = true; } });
  v.context.field = field('   ');
  assert.equal(v.call("trimmedValue(field, 1, 'Enter a request.')"), null);
  assert.equal(v.context.field.message, 'Enter a request.');
  assert.equal(v.context.field.reported, true);
  v.context.field = field(' ab  ');
  assert.equal(v.call("trimmedValue(field, 4, 'Too short.')"), null);
  v.context.field = field('  sprain ');
  assert.equal(v.call("trimmedValue(field, 4, 'Too short.')"), 'sprain');
  assert.equal(v.context.field.message, '');
  assert.equal(v.context.field.reported, false);
});

const roomRun = (id = 'room-run', extra = {}) => ({ id, session: 'recorded-p3-session', agent: 'writer', status: 'proposed', questionsSupported: true, model: 'original-model', ...extra });
const roomQuestion = (extra = {}) => ({ id: 'question-1', kind: 'question', actor: 'coach', text: 'Why retain this requirement?', at: '2026-03-04T12:30:00Z', run: 'room-run', session: 'recorded-p3-session', agent: 'writer', status: 'queued', ...extra });
function roomView(runs = [roomRun()]) {
  const v = view(runs);
  v.context.state.people = [{ id: 'coach', name: 'Marcus Hale' }, { id: 'owner', name: 'Priya Anand' }];
  v.context.state.agents = [{ id: 'writer', name: 'Native writer', model: 'current-config-model' }];
  v.context.state.conversation = [];
  v.call("ui.run = state.runs[0]?.id || null");
  v.call("$('room-request').value = ''");
  return v;
}

test('shared room comments render inert text, recorded actor and actual time across role and run switches', () => {
  const v = roomView([roomRun(), roomRun('other', { session: 'other-session' })]);
  v.context.state.conversation = [{ id: 'comment-1', kind: 'comment', actor: 'owner', text: '<img src=x onerror=alert(1)> Keep the evidence.', at: '2026-03-04T12:30:00Z' }];
  v.call('renderConversation()');
  let root = v.nodes.get('conversation-content');
  assert.match(root.textContent, /Priya Anand.*<img src=x onerror=alert\(1\)> Keep the evidence\./s);
  assert.equal(descendants(root, 'img').length, 0);
  assert.ok(descendants(root, 'time').some(node => node.getAttribute('datetime') === '2026-03-04T12:30:00Z'));
  v.context.state.role = 'coach'; v.call("ui.run = 'other'; renderConversation()");
  assert.match(root.textContent, /Priya Anand.*Keep the evidence/s);
  v.context.state.people[1].name = 'Renamed owner'; v.call("ui.run = null; renderConversation()");
  assert.match(root.textContent, /Renamed owner/);
  assert.ok(descendants(root, 'article').some(row => row.className.includes('speaker-owner')));
  v.context.state.conversation.push({ id: 'comment-2', kind: 'comment', actor: 'coach', text: 'New poll entry' });
  v.call('renderConversation()');
  assert.match(root.textContent, /New poll entry/);
  assert.match(root.textContent, /Time not recorded/);
});

// Controlled row offsets exercise target selection; browser smoke checks actual layout.
function conversationGeometry(v) {
  const root = v.call("$('conversation-content')");
  root.getBoundingClientRect = () => ({ top: 100 });
  const create = v.context.document.createElement;
  v.context.document.createElement = tag => {
    const node = create(tag);
    if (tag === 'article') node.getBoundingClientRect = () => ({ top: 100 + root.children.indexOf(node) * 100 - root.scrollTop });
    return node;
  };
  return root;
}

test('new room messages reveal the newest content from a historic scroll position without consuming composer draft or focus', () => {
  const v = roomView(); v.call('renderConversation()');
  const root = v.nodes.get('conversation-content');
  const input = v.call("$('room-request')"); input.value = 'Unsubmitted draft'; v.context.document.activeElement = input;
  root.scrollTop = 12;
  v.context.state.conversation.push({ id: 'comment-new', kind: 'comment', actor: 'coach', text: 'Newest shared comment', at: '2026-03-04T12:30:00Z' });
  v.call('renderConversation()');
  assert.equal(root.scrollTop, root.scrollHeight);
  assert.match(visibleText(root), /Newest shared comment/);
  assert.equal(input.value, 'Unsubmitted draft');
  assert.equal(v.context.document.activeElement, input);
});

test('question answer transitions reveal newest content without closing its recorded source disclosure', () => {
  const v = roomView(); const root = conversationGeometry(v);
  v.context.state.conversation = [roomQuestion()]; v.call('renderConversation()');
  const source = descendants(v.nodes.get('conversation-evidence'), 'details').find(node => node.children[0].dataset.focusKey === 'disclosure-answer-question-1');
  source.open = true; source.listeners.toggle();
  v.context.document.activeElement = source.children[0];
  for (const status of ['running', 'answered']) {
    root.scrollTop = 14;
    Object.assign(v.context.state.conversation[0], { status, answer: status === 'answered' ? 'Newest recorded answer' : undefined });
    v.call('renderConversation()');
    assert.equal(root.scrollTop, root.scrollHeight, status);
    const restored = descendants(v.nodes.get('conversation-evidence'), 'details').find(node => node.children[0].dataset.focusKey === 'disclosure-answer-question-1');
    assert.equal(restored.open, true);
    if (status === 'running') assert.equal(restored.children[0].focused, true, 'replacement disclosure retains focus');
  }
  assert.match(visibleText(root), /Newest recorded answer/);
});

test('unchanged polls and role, proof, metadata or navigation refreshes preserve historic conversation scroll', () => {
  const v = roomView([roomRun(), roomRun('other', { session: 'other-session' })]);
  v.context.state.conversation = [roomQuestion({ status: 'answered', answer: 'Historical answer' })];
  v.call('renderConversation()');
  const root = v.nodes.get('conversation-content'); root.scrollTop = 21;
  const mutations = [
    () => {},
    () => { v.context.state.role = 'owner'; },
    () => { v.context.state.people[0].name = 'Renamed coach'; },
    () => { v.context.state.agents[0].model = 'new-configuration'; },
    () => { v.context.state.runs[0].seconds = 42; v.context.state.conversation[0].seconds = 23; },
    () => { v.call("ui.verified.add('room-run'); ui.proofs.set('room-run', {chainValid:true}); ui.conversationKey=''"); },
    () => { v.call("ui.run='other'; ui.agent='reviewer'"); },
    () => { v.context.state.runs.push(roomRun('unrelated', { status: 'queued' })); },
    () => { v.context.state.runs[0].summary = 'Unselected run updated'; },
    () => { v.call("ui.run='room-run'; ui.agent='writer'"); },
  ];
  for (const [index, mutate] of mutations.entries()) {
    mutate(); v.call('renderConversation()');
    assert.equal(root.scrollTop, 21, `non-message refresh ${index}`);
  }
});

test('selected run results reveal newest content but duration-only polls do not', () => {
  const v = roomView([roomRun('room-run', { status: 'queued' })]); const root = conversationGeometry(v);
  v.call('renderConversation()');
  for (const [status, summary] of [['running', 'Working on recorded inputs'], ['proposed', 'New recorded recommendation']]) {
    root.scrollTop = 9; Object.assign(v.context.state.runs[0], { status, summary });
    v.call('renderConversation()');
    assert.equal(root.scrollTop, 100, status);
  }
  root.scrollTop = 9; v.context.state.runs[0].seconds = 300; v.call('renderConversation()');
  assert.equal(root.scrollTop, 9);
});

test('run completion reveals its result above long shared-room history, not the last comment', () => {
  const v = roomView([roomRun('run-"[unsafe]', { status: 'running' })]);
  const root = conversationGeometry(v); root.scrollHeight = 4000;
  v.context.state.conversation = Array.from({ length: 20 }, (_, index) => ({ id: `comment-${index}`, kind: 'comment', text: `Later room comment ${index}` }));
  v.call('renderConversation()');
  const input = v.call("$('room-request')"); input.value = 'Keep this draft'; v.context.document.activeElement = input;
  root.scrollTop = 1800;
  Object.assign(v.context.state.runs[0], { status: 'proposed', summary: 'Completed result above the history' });
  v.call('renderConversation()');
  assert.equal(root.scrollTop, 100, 'reveal the result at offset 100, not unrelated room history');
  assert.match(root.children[1].textContent, /Completed result above the history/);
  assert.equal(input.value, 'Keep this draft');
  assert.equal(v.context.document.activeElement, input);
  assert.equal(root.revealed, undefined, 'never scroll the pane into the page viewport');
});

test('older question answer is revealed beside its question even after later comments', () => {
  const v = roomView(); const root = conversationGeometry(v); root.scrollHeight = 4000;
  v.context.state.conversation = [roomQuestion({ id: 'question-"[unsafe]' }), ...Array.from({ length: 20 }, (_, index) => ({ id: `comment-${index}`, kind: 'comment', text: `Later room comment ${index}` }))];
  v.call('renderConversation()');
  const input = v.call("$('room-request')"); input.value = 'Keep another draft'; v.context.document.activeElement = input;
  root.scrollTop = 1800;
  Object.assign(v.context.state.conversation[0], { status: 'answered', answer: 'New answer to the older question' });
  v.call('renderConversation()');
  assert.equal(root.scrollTop, 400, 'reveal the answer at offset 400, not the last comment');
  assert.match(root.children[4].textContent, /New answer to the older question/);
  assert.equal(input.value, 'Keep another draft');
  assert.equal(v.context.document.activeElement, input);
  assert.equal(root.revealed, undefined);
});

for (const terminal of ['answered', 'failed', 'uncertain']) {
  test(`queued question notice expires only for its returned message ID reaching ${terminal}`, async () => {
    const v = roomView([roomRun(), roomRun('other', { session: 'other-session' })]);
    v.context.fetch = async url => reply(url === '/api/ask' ? { message: 'submitted-question' } : url === '/api/state' ? v.context.state : { accepted: '' });
    v.call("$('room-request').value = 'Why this evidence?'"); await v.call("submitRoom('question')");
    const notice = v.nodes.get('notice'); const queuedText = notice.textContent;
    assert.match(queuedText, /question queued/);
    v.context.state.conversation = [roomQuestion({ id: 'other-question', status: terminal })];
    await v.call('refreshState()');
    assert.equal(notice.hidden, false, 'another question terminal status must not expire this notice');
    v.call("ui.run='other'; ui.agent='reviewer'"); v.context.state.role = 'owner'; await v.call('refreshState()');
    assert.equal(notice.hidden, false, 'navigation or persona switches do not expire a pending question notice');
    for (const status of ['queued', 'running', 'unexpected']) {
      v.context.state.conversation.push(roomQuestion({ id: 'submitted-question', status })); await v.call('refreshState()');
      assert.equal(notice.hidden, false, status); v.context.state.conversation.pop();
    }
    v.context.state.conversation.push(roomQuestion({ id: 'submitted-question', status: terminal })); await v.call('refreshState()');
    assert.equal(notice.hidden, true);
    assert.equal(v.call('ui.noticeContext'), null);
  });
}

test('terminal questions never erase newer unrelated feedback or a newer queued question', async () => {
  const v = roomView([roomRun(), roomRun('other', { session: 'other-session' })]);
  let questionID = 'older-question';
  v.context.fetch = async url => reply(url === '/api/ask' ? { message: questionID } : url === '/api/state' ? v.context.state : { accepted: '' });
  for (const [message, error] of [['Accepted. Rank #4 → 7', false], ['Verification requested. Only returned evidence is shown.', false], ['New review error', true]]) {
    v.context.state.conversation = []; v.call("ui.run='room-run'; $('room-request').value='Why?'"); await v.call("submitRoom('question')");
    v.context.feedback = message; v.context.error = error; v.call('notify(feedback, error)');
    v.context.state.conversation.push(roomQuestion({ id: 'older-question', status: 'answered', answer: 'Older answer' })); await v.call('refreshState()');
    assert.equal(v.nodes.get('notice').hidden, false); assert.equal(v.nodes.get('notice').textContent, message);
  }
  v.context.state.conversation = []; v.call("$('room-request').value='Older question'"); await v.call("submitRoom('question')");
  questionID = 'newer-question'; v.call("ui.run='other'; $('room-request').value='Newer question'"); await v.call("submitRoom('question')");
  v.context.state.conversation.push(roomQuestion({ id: 'older-question', status: 'answered' })); await v.call('refreshState()');
  assert.equal(v.nodes.get('notice').hidden, false);
  v.context.state.conversation.push(roomQuestion({ id: 'newer-question', run: 'other', session: 'other-session', status: 'failed' })); await v.call('refreshState()');
  assert.equal(v.nodes.get('notice').hidden, true);
});

test('role control preserves a pending question notice until its matching terminal answer', async () => {
  const v = view([roomRun()], { handlers: true }); v.context.state.conversation = [];
  v.context.fetch = async url => reply(url === '/api/ask' ? { message: 'question-role' } : url === '/api/state' ? v.context.state : { accepted: '' });
  v.call("ui.run='room-run'; $('room-request').value='Why?' "); await v.call("submitRoom('question')");
  const notice = v.nodes.get('notice'); const queuedText = notice.textContent;
  v.call("$('role').value='owner'"); await v.nodes.get('role').listeners.change(); await tick();
  assert.equal(notice.textContent, queuedText); assert.equal(notice.hidden, false);
  v.context.state.conversation = [roomQuestion({ id: 'question-role', status: 'answered' })]; await v.call('refreshState()');
  assert.equal(notice.hidden, true);
});

test('question response states remain read-only and retain recorded target agent, session and model', () => {
  for (const [status, expected] of [['queued', /Queued.*read-only/s], ['running', /Answering.*read-only/s], ['answered', /Recorded answer <script>inert<\/script>/], ['failed', /Read-only question failed/], ['uncertain', /Interrupted.*not automatically retried/s], ['unexpected', /Unknown question status: unexpected/]]) {
    const v = roomView([roomRun(), roomRun('other', { agent: 'reviewer', session: 'other-session' })]);
    v.context.state.conversation = [roomQuestion({ status, answer: status === 'answered' ? 'Recorded answer <script>inert</script>' : undefined, model: 'question-model', seconds: 0 })];
    v.call("ui.run = 'other'; renderConversation()");
    const root = v.nodes.get('conversation-content');
    const roomRows = descendants(root, 'article').filter(row => row.dataset.conversationId === 'question-1');
    assert.equal(roomRows.length, 2);
    assert.match(roomRows[0].textContent, /Marcus Hale.*Why retain this requirement\?/s);
    assert.match(roomRows[1].textContent, expected);
    assert.match(roomRows[1].className, /speaker-unknown/);
    assert.match(v.nodes.get('conversation-evidence').textContent, /question-model.*0\.0 seconds/s);
    assert.match(roomRows[1].textContent, /answering a question · read-only/);
    assert.equal(descendants(roomRows[1], 'script').length, 0);
    assert.equal(descendants(roomRows[1], 'button').filter(node => node.dataset.mutation).length, 0, 'no proposed-change actions in a question answer');
    assert.match(v.nodes.get('conversation-evidence').textContent, /room-run/);
    assert.match(v.nodes.get('conversation-evidence').textContent, /recorded-p3-session/);
    assert.equal(v.context.state.runs[0].model, 'original-model');
  }
});

test('Ask follows recorded support and blocks active original runs and same-session questions only', () => {
  const v = roomView([roomRun(), roomRun('alias'), roomRun('other', { session: 'other-session' })]);
  v.call('applyBusy()');
  assert.equal(v.nodes.get('room-ask').disabled, false);
  for (const questionsSupported of [false, undefined]) {
    v.context.state.runs[0].questionsSupported = questionsSupported; v.call('applyBusy()');
    assert.equal(v.nodes.get('room-ask').disabled, true);
    assert.match(v.nodes.get('room-context').textContent, /Questions unsupported for this recorded session/);
  }
  v.context.state.runs[0].questionsSupported = true;
  for (const status of ['queued', 'running']) {
    v.context.state.runs[0].status = status; v.call('applyBusy()');
    assert.equal(v.nodes.get('room-ask').disabled, true);
    assert.match(v.nodes.get('room-context').textContent, /Wait for the original run/);
  }
  v.context.state.runs[0].status = 'proposed';
  v.context.state.conversation = [roomQuestion({ run: 'alias', status: 'running' })]; v.call('applyBusy()');
  assert.equal(v.nodes.get('room-ask').disabled, true);
  assert.match(v.nodes.get('room-context').textContent, /question.*same session/s);
  v.context.state.conversation[0].session = 'other-session'; v.call('applyBusy()');
  assert.equal(v.nodes.get('room-ask').disabled, false);
  v.call('ui.run = null; applyBusy()');
  assert.equal(v.nodes.get('room-ask').disabled, true);
  assert.equal(v.nodes.get('room-post').disabled, false);
  assert.match(v.nodes.get('room-context').textContent, /Select a recorded session/);
  v.call('ui.busy = true; applyBusy()');
  assert.equal(v.nodes.get('room-post').disabled, true);
  assert.equal(v.nodes.get('role').disabled, true);
  assert.equal(v.nodes.get('room-form').getAttribute('aria-busy'), 'true');
});

test('room POST preserves exact text and actor; successful actions clear only the submitted draft', async () => {
  const v = roomView(); const requests = [];
  v.context.fetch = async (url, options) => { requests.push([url, options?.body && JSON.parse(options.body)]); return reply(url === '/api/state' ? v.context.state : { message: 'room-id' }); };
  const input = v.call("$('room-request')"); input.value = '  <b>Owner input</b>  ';
  v.context.state.role = 'owner';
  await v.call("submitRoom('comment')");
  assert.deepEqual(requests[0], ['/api/comment', { run: 'room-run', request: '  <b>Owner input</b>  ', role: 'owner' }]);
  assert.equal(input.value, '');
  input.value = 'Why this evidence?';
  await v.call("submitRoom('question')");
  assert.deepEqual(requests.find(([url]) => url === '/api/ask'), ['/api/ask', { run: 'room-run', request: 'Why this evidence?', role: 'owner' }]);
  assert.equal(input.value, '');
  assert.equal(v.call('ui.run'), 'room-run');
  v.call('ui.run = null'); input.value = 'Room-wide comment';
  await v.call("submitRoom('comment')");
  assert.deepEqual(requests.filter(([url]) => url === '/api/comment')[1], ['/api/comment', { run: '', request: 'Room-wide comment', role: 'owner' }]);
});

test('polling and pending room actions preserve focus, replacement edits and failed drafts', async () => {
  const v = roomView(); const input = v.call("$('room-request')");
  input.value = 'First draft'; v.context.document.activeElement = input;
  let completePost;
  v.context.fetch = async url => url === '/api/comment' ? new Promise(resolve => { completePost = resolve; }) : reply(url === '/api/state' ? v.context.state : { accepted: '' });
  const posting = v.call("submitRoom('comment')");
  await tick();
  input.value = 'Edited while pending';
  await v.call('refreshState()');
  assert.equal(input.value, 'Edited while pending');
  assert.equal(v.context.document.activeElement, input);
  completePost(reply({ message: 'posted' })); await posting;
  assert.equal(input.value, 'Edited while pending');
  v.context.fetch = async url => url === '/api/ask' ? { ok: false, status: 409, json: async () => ({ error: 'Questions unsupported for this recorded session' }) } : reply(url === '/api/state' ? v.context.state : { accepted: '' });
  await v.call("submitRoom('question')");
  assert.equal(input.value, 'Edited while pending');
  assert.match(v.nodes.get('notice').textContent, /Questions unsupported/);
  input.value = '   ';
  const count = [];
  v.context.fetch = async url => { count.push(url); return reply({}); };
  await v.call("submitRoom('comment')");
  assert.equal(count.length, 0); assert.ok(input.validityReported);
});

test('new question invalidates every cached and returned proof for its session, then allows explicit reverify', async () => {
  const v = decisionFixture();
  v.context.state.runs[0].questionsSupported = true;
  v.context.state.runs.push({ ...v.context.state.runs[0], id: 'alias' }, roomRun('other', { session: 'other-session' }));
  v.context.state.conversation = [];
  v.context.proof = { chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0, records: 12 };
  v.call("for (const run of state.runs) { run.proof = proof; ui.verified.add(run.id); ui.proofs.set(run.id, proof); }");
  await v.call('renderDocuments()');
  assert.match(v.nodes.get('document-evidence').textContent, /Replay verified/);
  v.context.state.conversation.push(roomQuestion({ run: 'decision-run', session: 'private-session', status: 'queued' }));
  v.call('renderState()'); await tick();
  assert.equal(v.call("ui.verified.has('decision-run')"), false);
  assert.equal(v.call("ui.verified.has('alias')"), false);
  assert.equal(v.call("ui.proofs.has('alias')"), false);
  assert.equal(v.call("ui.verified.has('other')"), true);
  assert.doesNotMatch(v.nodes.get('document-evidence').textContent, /Replay verified/);
  assert.equal(v.call('verifyRunButton(state.runs[0]).disabled'), true);
  v.context.state.conversation[0].status = 'answered';
  v.context.fetch = async url => reply(url === '/api/verify' ? { proof: v.context.proof } : url === '/api/state' ? v.context.state : { accepted: proposalText, proposed: proposalText });
  const verify = v.call('verifyRunButton(state.runs[0])');
  assert.equal(verify.disabled, false); await verify.listeners.click(); await tick();
  assert.match(v.nodes.get('document-evidence').textContent, /Replay verified/);
  v.call('renderState()'); await tick();
  assert.match(v.nodes.get('document-evidence').textContent, /Replay verified/, 'same recorded question must not repeatedly invalidate fresh verification');
  assert.equal(v.call("currentRunProof(state.runs.find(run => run.id === 'alias'))"), null, 'a sibling run must not regain its pre-question returned proof');
});

test('reload preserves freshly recorded replay proof after an answered question', async () => {
  const v = decisionFixture();
  v.context.state.conversation = [roomQuestion({run: 'decision-run', session: 'private-session', status: 'answered', answer: 'Recorded explanation.'})];
  v.context.state.runs[0].proof = {chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0, records: 20};
  await v.call('renderDocuments()');
  assert.match(v.nodes.get('document-evidence').textContent, /Replay verified/);
});

test('a question observed during verification cannot resurrect proof captured before that question', async () => {
  const v = roomView();
  const proof = { chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0 };
  let completeVerify;
  v.context.fetch = async url => url === '/api/verify' ? new Promise(resolve => { completeVerify = resolve; }) : reply(url === '/api/state' ? v.context.state : { accepted: '' });
  const verify = v.call('verifyRunButton(state.runs[0])');
  const verifying = verify.listeners.click(); await tick();
  v.context.state.conversation.push(roomQuestion());
  v.call('renderConversation()');
  completeVerify(reply({ proof })); await verifying;
  assert.equal(v.call('currentRunProof(state.runs[0])'), null);
  assert.equal(v.call("ui.verified.has('room-run')"), false);
  v.context.state.conversation[0].status = 'answered';
  assert.equal(v.call('currentRunProof(state.runs[0])'), null, 'finishing a question cannot make an older proof fresh');
});

function resumeFixture({ question = false, inspection = {} } = {}) {
  const v = roomView([roomRun('room-run', { status: question ? 'proposed' : 'uncertain', sessionVersion: 0 })]);
  if (question) v.context.state.conversation = [roomQuestion({ status: 'uncertain' })];
  v.context.crypto = { randomUUID: () => '12345678-1234-4234-8234-123456789abc' };
  const requests = [];
  v.context.fetch = async (url, options) => {
    requests.push([url, options?.body && JSON.parse(options.body)]);
    if (url.startsWith('/api/resume?')) return reply({ available: true, session: 'recorded-p3-session', execution: 'execution-1', lastSeq: 9, head: 'recorded-head', sessionVersion: 0, ...inspection });
    if (url === '/api/state') return reply(v.context.state);
    return reply({});
  };
  v.call('renderConversation()');
  const controls = () => descendants(v.nodes.get('conversation-content'), 'button');
  const find = text => controls().find(node => node.textContent === text);
  return { v, requests, find };
}

for (const question of [false, true]) {
  test(`Resume ${question ? 'question' : 'attempt'} requires the repeat-charge confirmation and remains separate from fresh work`, async () => {
    const { v, requests, find } = resumeFixture({ question });
    assert.ok(find('Resume'));
    if (!question) assert.ok(find('Try again on latest'));
    assert.equal(requests.length, 0, 'rendering never inspects or resumes automatically');
    await find('Resume').listeners.click();
    assert.equal(requests.length, 1);
    assert.match(requests[0][0], question ? /message=question-1/ : /run=room-run/);
    assert.equal(requests[0][1], undefined, 'preparation is read-only');
    assert.match(v.nodes.get('conversation-content').textContent, /Re-sends the last model request; it may be charged again\./);
    assert.ok(find('Confirm Resume')); assert.ok(find('Cancel Resume'));
    assert.equal(find('Confirm Resume').focused, true, 'explicit preparation focuses its confirmation');
    find('Cancel Resume').listeners.click();
    assert.equal(requests.length, 1, 'Cancel never sends a Resume');
    assert.ok(find('Resume'));
    assert.equal(find('Resume').focused, true, 'Cancel returns focus to the launcher');
    await find('Resume').listeners.click();
    const confirm = find('Confirm Resume');
    await confirm.listeners.click(); await confirm.listeners.click(); await tick();
    const sent = requests.filter(([url]) => url === '/api/resume');
    assert.equal(sent.length, 1, 'double click sends once');
    assert.deepEqual(sent[0][1], { run: 'room-run', ...(question ? { message: 'question-1' } : {}), role: 'coach', confirmation: {
      confirmed: true, actionId: '12345678123442348234123456789abc', session: 'recorded-p3-session', execution: 'execution-1', lastSeq: 9, head: 'recorded-head', sessionVersion: 0,
    } });
    assert.doesNotMatch(v.nodes.get('notice').textContent, /accepted|finished/i);
    v.call('renderState(); renderState()'); await tick();
    assert.equal(requests.filter(([url]) => url === '/api/resume').length, 1, 'polling cannot resend');
  });
}

test('tool recovery refusal is visible and supplies no confirmation or Resume POST', async () => {
  const reason = 'The last tool call has no recorded result. It may already have changed something; repeating it could repeat the action.';
  const { v, requests, find } = resumeFixture({ inspection: { available: false, reason } });
  await find('Resume').listeners.click();
  assert.match(v.nodes.get('conversation-content').textContent, /last tool call has no recorded result.*could repeat the action/);
  assert.equal(find('Confirm Resume'), undefined);
  v.call('renderConversation(); renderConversation()');
  assert.equal(requests.filter(([url]) => url === '/api/resume').length, 0);
});

test('polling a changed session, target or persona invalidates prepared Resume confirmation', async () => {
  for (const change of ["state.runs[0].sessionVersion += 1", "state.runs[0].session = 'other-session'", "state.runs[0].status = 'proposed'", "state.role = 'owner'", "ui.run = null"]) {
    const { v, requests, find } = resumeFixture();
    await find('Resume').listeners.click(); const oldConfirm = find('Confirm Resume');
    v.call(`${change}; renderConversation()`);
    assert.equal(find('Confirm Resume'), undefined, change);
    await oldConfirm.listeners.click();
    assert.equal(requests.filter(([url]) => url === '/api/resume').length, 0, change);
  }
});

test('incomplete or mismatched inspection evidence never enables confirmation', async () => {
  for (const inspection of [{ session: 'wrong-session' }, { sessionVersion: 1 }, { lastSeq: 0 }, { lastSeq: '9' }, { head: '' }, { execution: '' }, { available: 'true' }]) {
    const { requests, find } = resumeFixture({ inspection });
    await find('Resume').listeners.click();
    assert.equal(find('Confirm Resume'), undefined);
    assert.equal(requests.filter(([url]) => url === '/api/resume').length, 0);
  }
});

test('uncertain Resume response requires fresh inspection and a new confirmed action', async () => {
  const { v, requests, find } = resumeFixture();
  let nonce = 0;
  v.context.crypto = { randomUUID: () => `${++nonce}`.padStart(32, '0') };
  const fetch = v.context.fetch;
  v.context.fetch = async (url, options) => url === '/api/resume'
    ? (requests.push([url, JSON.parse(options.body)]), { ok: false, status: 503, json: async () => ({ error: 'Resume outcome is uncertain. Inspect the recorded evidence before confirming again.' }) })
    : fetch(url, options);
  await find('Resume').listeners.click(); await find('Confirm Resume').listeners.click();
  assert.match(v.nodes.get('notice').textContent, /outcome is uncertain/);
  assert.equal(find('Confirm Resume'), undefined);
  v.call('renderConversation(); renderConversation()');
  assert.equal(requests.filter(([url]) => url === '/api/resume').length, 1);
  await find('Resume').listeners.click();
  assert.match(v.nodes.get('conversation-content').textContent, /may be charged again/);
  await find('Confirm Resume').listeners.click();
  const sent = requests.filter(([url]) => url === '/api/resume');
  assert.equal(sent.length, 2);
  assert.notEqual(sent[0][1].confirmation.actionId, sent[1][1].confirmation.actionId);
});

test('a delayed Resume inspection cannot arm a changed target and reload never restores confirmation', async () => {
  const { v, find, requests } = resumeFixture();
  let finish;
  v.context.fetch = async url => { requests.push([url]); return new Promise(resolve => { finish = resolve; }); };
  const inspecting = find('Resume').listeners.click(); await tick();
  v.call("state.runs[0].sessionVersion += 1; renderConversation()");
  finish(reply({ available: true, session: 'recorded-p3-session', execution: 'execution-1', lastSeq: 9, head: 'recorded-head', sessionVersion: 0 }));
  await inspecting;
  assert.equal(find('Confirm Resume'), undefined);
  const fresh = resumeFixture();
  assert.equal(fresh.find('Confirm Resume'), undefined);
  assert.equal(fresh.requests.length, 0);
});

test('a complete recovery observed from another tab invalidates saved proofs and delayed verification', async () => {
  const v = roomView([roomRun('room-run', { sessionVersion: 0 })]);
  const proof = { chainValid: true, reconstructed: true, outputsMatch: true, liveUnchanged: true, modelCalls: 0 };
  v.context.proof = proof;
  v.call("state.runs[0].proof=proof; ui.verified.add('room-run'); ui.proofs.set('room-run', proof); renderConversation()");
  let finish;
  v.context.fetch = async url => url === '/api/verify' ? new Promise(resolve => { finish = resolve; }) : reply(url === '/api/state' ? v.context.state : {});
  const verifying = v.call('verifyRunButton(state.runs[0])').listeners.click(); await tick();
  v.call('state.runs[0].sessionVersion += 1; renderConversation()');
  assert.equal(v.call('currentRunProof(state.runs[0])'), null);
  finish(reply({ proof })); await verifying;
  assert.equal(v.call('currentRunProof(state.runs[0])'), null);
  assert.equal(v.call("ui.verified.has('room-run')"), false);
});

test('unknown room kinds, missing timestamps and unknown target agents remain explicit', () => {
  const v = roomView();
  v.context.state.conversation = [{ id: 'unknown', kind: 'future-kind', actor: 'owner', text: 'Recorded text', at: '0001-01-01T00:00:00Z' }, roomQuestion({ id: 'unknown-agent', agent: 'toString', at: 'invalid', run: 'missing-run', session: undefined, status: 'answered', answer: 'Recorded answer' })];
  v.call('renderConversation()');
  const root = v.nodes.get('conversation-content');
  assert.match(root.textContent, /Unknown room entry kind: future-kind/);
  assert.match(root.textContent, /Recorded agent: toString/);
  assert.match(root.textContent, /Run not available/);
  assert.match(root.textContent, /Time not recorded/);
  assert.equal(descendants(root, 'time').length, 0);
  assert.doesNotMatch(root.textContent, /native code/);
  v.context.state.runs[0].session = undefined; v.call('applyBusy()');
  assert.equal(v.nodes.get('room-ask').disabled, true);
  assert.match(v.nodes.get('room-context').textContent, /Session UID not recorded/);
});

// Review decisions must refresh every display even though the execution ledger is frozen.
for (const [status, movement, summary] of [
  ['accepted', 'Accepted proposal change', 'Proposal accepted'],
  ['rejected', 'Not applied. Rejected proposal', 'Proposal rejected'],
  ['stale', 'Stale proposal, not accepted', 'Proposal stale'],
]) {
  test(`${status} refreshes navigation and conversation without changing recorded execution`, () => {
    const recordedSummary = 'Agent proposed a document replacement. A quoted proposed trade remains recorded.';
    const ordinary = roomRun('ordinary', { proposal: 'ordinary-p', summary: recordedSummary });
    const earlier = scenario('earlier', 'Trade up', { comparison: 'one', proposal: 'earlier-p', summary: recordedSummary });
    const latest = scenario('latest', 'Trade up', { comparison: 'one', proposal: 'latest-p', summary: recordedSummary });
    const v = roomView([ordinary, earlier, latest]);
    v.context.state.proposals = v.context.state.runs.map(run => ({ id: run.proposal, status: 'pending', attribution: { run: run.id, agent: run.agent }, changes: { 'strategy.md': 'Recorded bytes' } }));
    v.call('renderNavigation(); renderConversation()');
    const frozen = JSON.stringify(v.context.state.runs);
    for (const proposal of v.context.state.proposals) proposal.status = status;
    v.call('renderNavigation(); renderConversation()');
    for (const id of ['sessions', 'agents', 'conversation-meta']) {
      assert.match(v.nodes.get(id).textContent, new RegExp(status), id);
      assert.doesNotMatch(v.nodes.get(id).textContent, /\bproposed\b/, id);
    }
    assert.ok(descendants(v.nodes.get('sessions'), 'button').every(node => node.textContent.includes(status)), 'every earlier attempt carries its own decision');
    const root = v.nodes.get('conversation-content');
    assert.match(visibleText(root), new RegExp(summary));
    assert.doesNotMatch(visibleText(root), /Agent proposed a document|Proposed change/);
    const evidence = descendants(v.nodes.get('conversation-evidence'), 'details').find(node => node.children[0].textContent === 'Evidence');
    assert.equal(evidence.open, false);
    assert.match(evidence.textContent, /"executionStatus": "proposed"/);
    assert.ok(evidence.textContent.includes(recordedSummary));
    v.call('applyBusy()'); assert.equal(v.nodes.get('room-ask').disabled, false);
    assert.equal(JSON.stringify(v.context.state.runs), frozen);
  });

  test(`${status} document refresh keeps state authoritative over stale document metadata`, async () => {
    const v = decisionFixture({ summary: 'Agent proposed a document replacement.' }, { status: 'pending' });
    await v.call('renderDocuments()');
    v.context.state.proposals[0].status = status;
    await v.call('renderDocuments()');
    const panel = v.nodes.get('document-panel');
    const visible = visibleText(panel);
    assert.match(visible, new RegExp(status, 'i'));
    assert.match(visible, new RegExp(summary));
    assert.ok(!descendants(panel, 'span').some(node => /badge/.test(node.className) && /^(pending|proposed|Proposed)$/.test(node.textContent)));
    const accept = descendants(panel, 'button').find(node => node.textContent === 'Accept proposal');
    assert.ok(status === 'stale' ? accept?.disabled : !accept, 'review actions follow authoritative decision');
    const evidence = descendants(v.nodes.get('document-evidence'), 'details').find(node => node.children[0].textContent === 'Evidence');
    assert.equal(evidence.open, false);
    assert.match(evidence.textContent, /"executionStatus": "proposed"/);
    assert.match(evidence.textContent, /Agent proposed a document replacement/);
    assert.equal(v.context.state.runs[0].status, 'proposed');
  });
}

test('reviewed historical document proposal retains post-publication execution error in banners and document review', async () => {
  const error='Recorded bookkeeping failure after publication';
  const run=scenario('errored','Trade up',{proposal:'p',comparison:'one',summary:'Published, but bookkeeping failed.',error});
  const v=view([run]);
  v.context.state.documents.push('strategy.md');
  v.context.state.proposals=[{id:'p',status:'accepted',attribution:{run:run.id},changes:{'strategy.md':'Recorded replacement'}}];
  v.context.fetch=async()=>reply({accepted:'Current strategy',status:'accepted'});
  const banner=v.call('proposalBanner(state.proposals[0])');
  assert.match(visibleText(banner),new RegExp(error));
  v.call("ui.run='errored';ui.path='strategy.md'");
  await v.call('renderDocuments()');
  assert.match(visibleText(v.nodes.get('document-panel')),new RegExp(error));
  assert.match(visibleText(v.nodes.get('document-panel')),/accepted/);
  assert.equal(v.context.state.runs[0].status,'proposed');
});

test('terminal proposal banners replace only displayed generic summaries and retain original evidence', () => {
  const v = view([roomRun('r', { proposal: 'p', summary: 'Agent proposed a document replacement.' })]);
  v.context.state.proposals = [{ id: 'p', status: 'accepted', attribution: { run: 'r' }, changes: { 'strategy.md': 'Proposed bytes' } }];
  const banner = v.call('proposalBanner(state.proposals[0])');
  assert.match(visibleText(banner), /Proposal accepted/);
  assert.doesNotMatch(visibleText(banner), /Agent proposed/);
  assert.match(banner.textContent, /Agent proposed a document replacement/);

});
test('pre-accept response cannot overwrite post-accept confirmation or generic review', async () => {
  const v=decisionFixture();
  let completeReview, completeDocument, completeState;
  v.context.fetch=url=>new Promise(resolve=>{
    if(url==='/api/review') completeReview=()=>resolve(reply({}));
    else if(url==='/api/state') completeState=()=>resolve(reply({...v.context.state,proposals:[{...v.context.state.proposals[0],status:'accepted'}]}));
    else completeDocument=()=>resolve(reply({accepted:'Obsolete accepted bytes',proposed:'Obsolete pending bytes'}));
  });
  const banner=v.call('proposalBanner(state.proposals[0])');
  const accept=descendants(banner,'button').find(node=>node.textContent==='Accept proposal');
  const accepting=accept.listeners.click();
  const older=v.call('renderDocuments()');
  completeReview(); await tick();
  completeDocument(); await older;
  assert.match(v.nodes.get('notice').textContent,/Proposal accepted.*refreshing/);
  assert.doesNotMatch(v.nodes.get('document-panel').textContent,/Obsolete accepted bytes|Obsolete pending bytes/);
  v.context.fetch=async()=>reply({accepted:'Fresh accepted bytes',proposed:proposalText});
  completeState(); await accepting; await tick();
  assert.match(visibleText(v.nodes.get('document-panel')),/Accepted.*Fresh accepted bytes/s);
});

test('generic accepted, proposal-base, proposed full bytes and unified diff remain independently inspectable', async () => {
  const accepted='Current accepted <script>inert</script>\n';
  const base='Original proposal base\n  exact spaces\n';
  const proposed='Entire proposed replacement\n  trailing spaces  \n';
  const diff='@@ -1 +1 @@\n-Original proposal base\n+Entire proposed replacement\n';
  const v=decisionFixture({}, {accepted,base,proposed,diff});
  await v.call('renderDocuments()');
  const panel=v.nodes.get('document-panel'),audit=v.nodes.get('document-evidence');
  assert.ok(descendants(panel,'pre').some(node=>node.textContent===accepted));
  assert.ok(descendants(panel,'pre').some(node=>node.textContent===proposed));
  assert.ok(descendants(audit,'pre').some(node=>node.textContent===base));
  const renderedDiff=descendants(panel,'pre').find(node=>node.className==='document-diff');
  assert.deepEqual(renderedDiff.children.map(node=>node.textContent),['@@ -1 +1 @@','-Original proposal base','+Entire proposed replacement']);
  assert.equal(descendants(panel,'script').length,0);
});

test('historical attempts retain each proposal decision and earlier selection keeps immutable identity', async () => {
  const v=view([scenario('old','Recorded fork',{proposal:'old-p',comparison:'one',summary:'Agent proposed old text.'}),scenario('new','Recorded fork',{proposal:'new-p',comparison:'one',previousAttempt:'old'}),scenario('other','Recorded fork',{proposal:'other-p',comparison:'two'})]);
  v.context.state.proposals=v.context.state.runs.map(run=>({id:run.proposal,status:'pending',attribution:{run:run.id,agent:'writer'},changes:{'docs/spec.md':'Recorded replacement'}}));
  v.context.fetch=async()=>reply({accepted:'Current text',proposed:'Recorded replacement',status:'pending'});
  for(const [index,status] of ['rejected','accepted','stale'].entries()) v.context.state.proposals[index].status=status;
  for(const [index,status] of ['rejected','accepted','stale'].entries()) {
    v.context.run=v.context.state.runs[index];
    v.call('selectRun(run)'); await tick();
    assert.equal(v.call('ui.proposal'),v.context.state.proposals[index].id);
    assert.match(visibleText(v.nodes.get('document-panel')),new RegExp(status,'i'));
    assert.equal(v.call('ui.state.runs['+index+'].status'),'proposed');
    const accept=descendants(v.nodes.get('document-panel'),'button').find(node=>node.textContent==='Accept proposal');
    assert.ok(status==='stale'?accept?.disabled:!accept);
  }
});
test('explicit proposal association wins over earlier tuple matches; missing and unknown states invent no decision', () => {
  const v = roomView([roomRun('r', { proposal: 'explicit', summary: 'Recorded proposed quotation.' })]);
  v.context.state.proposals = [{ id: 'earlier', status: 'accepted', attribution: { run: 'r' } }, { id: 'explicit', status: 'rejected', attribution: { run: 'r' } }];
  v.call('renderConversation()');
  assert.match(visibleText(v.nodes.get('conversation-content')), /Proposal rejected/);
  assert.doesNotMatch(visibleText(v.nodes.get('conversation-content')), /Proposal accepted/);
  v.context.state.proposals[1].status = 'future-state';
  v.call('renderConversation()');
  assert.match(visibleText(v.nodes.get('conversation-content')), /Recorded proposed quotation/);
  assert.doesNotMatch(visibleText(v.nodes.get('conversation-content')), /Proposal accepted|Proposal rejected/);
  v.context.state.proposals = [];
  v.call('renderConversation()');
  assert.match(visibleText(v.nodes.get('conversation-content')), /Recorded proposed quotation/);
  v.context.state.proposals = [{ id: 'legacy', status: 'stale', attribution: { run: 'r' } }];
  delete v.context.state.runs[0].proposal;
  v.call('renderConversation()');
  assert.match(visibleText(v.nodes.get('conversation-content')), /Proposal stale/);
  v.context.state.runs[0].status = 'queued'; v.context.state.proposals[0].status = 'pending';
  v.call('renderConversation()');
  assert.match(v.nodes.get('conversation-meta').textContent, /queued/);
});

test('reviewed recovered publication stays distinct from finished execution and keeps genuine failure visible', async () => {
  const v = decisionFixture({ activity: [{ kind: 'recovered', at: '2026-01-02T09:42:05Z' }], summary: 'Agent proposed a recovered document.' }, { status: 'accepted' });
  await v.call('renderDocuments()');
  assert.match(v.nodes.get('document-evidence').textContent, /Publication recorded/);
  assert.doesNotMatch(visibleText(v.nodes.get('document-panel')), /\bProposed\b(?=\s+0\.0 seconds)|\bFinished\b/);
  v.context.state.runs[0].status = 'failed'; v.context.state.runs[0].error = 'Recorded bookkeeping failure';
  v.call('renderConversation()'); await v.call('renderDocuments()');
  for (const id of ['conversation-content', 'document-panel']) {
    assert.match(visibleText(v.nodes.get(id)), /accepted/i);
    assert.match(visibleText(v.nodes.get(id)), /Recorded bookkeeping failure/);
  }
  assert.equal(v.context.state.runs[0].status, 'failed');
});

test('accept mutation refreshes frozen runs and an older document response cannot restore pending review', async () => {
  const run = Object.freeze(roomRun('reviewed', { proposal: 'p', summary: 'Agent proposed a strategy replacement.' }));
  const v = roomView([run]);
  v.context.state.documents.push('strategy.md');
  v.context.state.proposals = [{ id: 'p', status: 'pending', attribution: { run: run.id, agent: 'writer' }, changes: { 'strategy.md': 'Recorded replacement' } }];
  v.context.fetch = async () => reply({ accepted: 'Current strategy', status: 'pending' });
  v.call("ui.path='strategy.md';renderNavigation();renderConversation()");
  await v.call('renderDocuments()');
  const accept = descendants(v.nodes.get('document-panel'), 'button').find(node => node.textContent === 'Accept proposal');
  let completeOlderDocument, documentRequests = 0;
  const requests = [];
  v.context.fetch = async (url, options) => {
    requests.push([url, options?.body && JSON.parse(options.body)]);
    if (url === '/api/review') return reply({});
    if (url === '/api/state') return reply({ ...v.context.state, proposals: [{ ...v.context.state.proposals[0], status: 'accepted' }] });
    if (++documentRequests === 1) return new Promise(resolve => { completeOlderDocument = resolve; });
    return reply({ accepted: 'Current accepted strategy', status: 'pending' });
  };
  v.call("ui.documentKey=''"); const olderDocument = v.call('renderDocuments()');
  await accept.listeners.click(); await tick();
  assert.deepEqual(requests.find(([url]) => url === '/api/review'), ['/api/review', { proposal: 'p', action: 'accept', role: 'coach' }]);
  completeOlderDocument(reply({ accepted: 'Obsolete document response', status: 'pending' }));
  await olderDocument;
  for (const id of ['sessions', 'agents', 'conversation-meta', 'document-panel']) {
    assert.match(visibleText(v.nodes.get(id)), /accepted/i, id);
    assert.doesNotMatch(visibleText(v.nodes.get(id)), /\bpending\b|Agent proposed|Obsolete document response/, id);
  }
  assert.match(v.nodes.get('notice').textContent, /Proposal accepted/);
  assert.equal(v.call('ui.state.runs[0]'), run);
  assert.equal(run.status, 'proposed');
  assert.equal(run.summary, 'Agent proposed a strategy replacement.');
});
