'use strict';

const $ = (id) => document.getElementById(id);
const ui = {
  state: null,
  fixtureProvider: false,
  run: null,
  agent: null,
  path: '',
  proposal: null,
  busy: false,
  refresh: null,
  stateGeneration: 0,
  documentKey: '',
  documentVersion: 0,
  navigationKey: '',
  conversationKey: '',
  conversationMessages: new Map(),
  announcedStates: new Map(),
  announcementsLoaded: false,
  verified: new Set(),
  proofs: new Map(),
  questionIntents: new Map(),
  questionIntentsLoaded: false,
  invalidatedSessions: new Map(),
  sessionVersions: new Map(),
  resumePanels: new Map(),
  disclosures: new Map(),
  fullText: new Map(),
  noticeContext: null,
  layout: null,
  layoutRole: null,
  focusEpoch: 0,
  publication: { id: '', view: null, epoch: 0, read: 0, loading: false, background: false, busy: false, error: '', actionError: '', role: null, head: null },
};
document.addEventListener('focusin', () => { ui.focusEpoch += 1; });

const tryAgainHelp = 'Try again: new recorded attempt on latest accepted documents; success creates a new proposal, not an edit. No automatic retry.';
const tryAgainNotice = 'A new recorded attempt was requested on the latest accepted documents. Successful output will create a new proposal; the previous attempt remains in the audit record.';
const staleReason = 'Stale: accepted documents changed after this proposal was created. Accept is unavailable. Try again on latest, or Reject this proposal.';
const layoutStorageKey = 'canvas.layout.v1';
const layoutRoles = ['GM', 'coach', 'owner'];
function loadLayout() {
  try {
    const saved = JSON.parse(window.localStorage.getItem(layoutStorageKey));
    if (saved?.version !== 1 || !saved.roles || typeof saved.roles !== 'object' || Array.isArray(saved.roles)) return {};
    const roles = {};
    for (const role of layoutRoles) {
      const value = saved.roles[role];
      if (value && typeof value.navigation === 'boolean' && typeof value.evidence === 'boolean') {
        roles[role] = { navigation: value.navigation, evidence: value.evidence };
      }
    }
    return roles;
  } catch { return {}; }
}

function renderLayout() {
  if (!ui.layout) ui.layout = loadLayout();
  const role = layoutRoles.includes(ui.state?.role) ? ui.state.role : null;
  const layout = role && ui.layout[role] || { navigation: false, evidence: false };
  for (const [name, id] of [['navigation', 'navigation'], ['evidence', 'evidence-panel']]) {
    const panel = $(id);
    if (!layout[name] && panel.contains(document.activeElement)) $(`${name}-toggle`).focus({ preventScroll: true });
    panel.hidden = !layout[name];
    $(`${name}-toggle`).setAttribute('aria-controls', id);
    $(`${name}-toggle`).setAttribute('aria-expanded', String(layout[name]));
    $('workspace').dataset[name] = String(layout[name]);
  }
  ui.layoutRole = role;
}

function setPanel(name, open) {
  if (!['navigation', 'evidence'].includes(name)) return;
  renderLayout();
  if (!ui.layoutRole) return;
  const current = ui.layout[ui.layoutRole] || { navigation: false, evidence: false };
  ui.layout[ui.layoutRole] = { ...current, [name]: open };
  try { window.localStorage.setItem(layoutStorageKey, JSON.stringify({ version: 1, roles: ui.layout })); } catch { /* Local preference only; API work is independent. */ }
  renderLayout();
}

function revealReview() {
  const panel = $('document-panel');
  panel.scrollTop = 0;
  $('review').focus({ preventScroll: true });
  $('review').scrollIntoView({ block: window.matchMedia('(max-width: 960px)').matches ? 'start' : 'nearest' });
}

function revealQuestion(entry) {
  const run = ui.state.runs.find(item => item.id === entry.run);
  if (run) selectRun(run);
  const row = Array.from($('conversation-content').children).filter(node => node.dataset.conversationId === entry.id).slice(-1)[0];
  if (row) { row.tabIndex = -1; row.focus({ preventScroll: true }); row.scrollIntoView({ block: 'nearest' }); }
  else $('conversation').focus({ preventScroll: true });
}

function proposalName(proposal) {
  return `${agentName(proposal.attribution?.agent || proposalRun(proposal)?.agent)} proposal`;
}

function renderRequests() {
  const strip = $('request-strip');
  const key = JSON.stringify([ui.state.proposals, ui.state.runs, ui.state.conversation, ui.state.people, ui.state.role, ui.proposal, ui.run, ui.path]);
  if (strip.dataset.key === key) return;
  strip.dataset.key = key;
  preserveFocus(strip, () => {
    strip.replaceChildren();
    const items = element('ul', 'request-items');
    const add = (label, action, key, actionLabel = 'View', visible = false) => {
      const item = element('li'); item.append(element('span', '', label));
      if (!visible) item.append(button(actionLabel, action, 'small-button', key));
      items.append(item);
    };
    const person = ui.state.people.find(item => item.id === ui.state.role)?.name || ui.state.role;
    for (const proposal of ui.state.proposals.filter(item => item.status === 'pending')) {
      add(`${person}: review ${proposalName(proposal)}`, () => {
        selectProposal(proposal);
        const paths = Object.keys(proposal.changes || {});
        ui.path = paths.includes(ui.path) ? ui.path : paths[0] || ui.path;
        renderState(); revealReview();
      }, `request-proposal-${proposal.id}`, 'Review proposal', currentProposal()?.id === proposal.id);
    }
    for (const run of ui.state.runs.filter(item => ['queued', 'running'].includes(item.status))) {
      add(`${runTitle(run)}: ${run.status === 'queued' ? 'queued' : 'working'}`, () => { selectRun(run); revealReview(); }, `request-run-${run.id}`);
    }
    for (const entry of (ui.state.conversation || []).filter(item => item.kind === 'question' && ['queued', 'running'].includes(item.status))) {
      add(`Question ${entry.status}: ${entry.text || 'Text not recorded'}`, () => revealQuestion(entry), `request-question-${entry.id}`);
    }
    strip.hidden = !items.childElementCount;
    if (items.childElementCount) strip.append(items);
  });
}

function openEvidence(detail, target = detail.children[0]) {
  const summary = detail.children[0];
  setPanel('evidence', true);
  detail.open = true;
  ui.disclosures.set(summary.dataset.focusKey.replace('disclosure-', ''), true);
  target.focus({ preventScroll: true });
  target.scrollIntoView({ block: target === summary ? 'nearest' : 'start' });
}

// Move the original detail nodes, not copies, so provenance and verification stay attached to their real item.
function collectEvidence(container, destination) {
  const target = $(destination);
  preserveFocus(target, () => {
    target.replaceChildren();
    for (const detail of Array.from(container.querySelectorAll('details.evidence'))) {
      const summary = detail.children[0];
      const trigger = button(summary.textContent, () => openEvidence(detail), 'small-button evidence-link', `open-${summary.dataset.focusKey}`);
      detail.id = summary.dataset.focusKey;
      trigger.setAttribute('aria-controls', `evidence-panel ${detail.id}`);
      detail.replaceWith(trigger);
      target.append(detail);
    }
  });
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = String(text);
  return node;
}

function button(text, action, className = '', focusKey = '') {
  const node = element('button', className, text);
  node.type = 'button';
  if (focusKey) node.dataset.focusKey = focusKey;
  node.addEventListener('click', action);
  return node;
}

function preserveFocus(container, render) {
  const active = document.activeElement;
  const key = container.contains(active) ? active.dataset.focusKey : null;
  render();
  if (key) {
    const replacement = Array.from(container.querySelectorAll('[data-focus-key]'))
      .find((node) => node.dataset.focusKey === key);
    if (replacement && !replacement.disabled) replacement.focus({ preventScroll: true });
  }
}

function disclosure(label, key, className = 'evidence') {
  const details = element('details', className);
  details.open = ui.disclosures.get(key) || false;
  const summary = element('summary', '', label);
  summary.dataset.focusKey = `disclosure-${key}`;
  details.append(summary);
  details.addEventListener('toggle', () => ui.disclosures.set(key, details.open));
  return details;
}

function evidenceRecord(title, record) {
  const section = element('div', 'evidence-record');
  section.append(element('h4', '', title), element('pre', 'evidence-json', JSON.stringify(record, null, 2)));
  return section;
}

// Creation is meaningful for queued runs. Legacy arrays already carry ledger order.
function orderedRuns(runs = ui.state.runs) {
  return runs.map((run, index) => ({ run, index })).sort((a, b) => {
    const created = (run) => run.created && !String(run.created).startsWith('0001-') ? Date.parse(run.created) : NaN;
    const first = created(a.run), second = created(b.run);
    return Number.isFinite(first) && Number.isFinite(second) ? first - second || a.index - b.index : a.index - b.index;
  }).map(({ run }) => run);
}

function proposalRun(proposal) {
  const explicit = ui.state.runs.find((run) => run.proposal === proposal.id);
  if (explicit) return explicit;
  const legacy = ui.state.runs.find((run) => !run.proposal && run.id === proposal.attribution?.run);
  return legacy && ui.state.proposals.filter((item) => item.attribution?.run === legacy.id).length === 1 ? legacy : undefined;
}

function agentName(id) {
  return ui.state?.agents.find((agent) => agent.id === id)?.name
    || (id ? `Recorded agent: ${id}` : 'Agent not recorded');
}
function runTitle(run) {
  return run?.requestSummary || agentName(run?.agent);
}
function seconds(run) {
  if (run.kind !== 'question' && ['queued', 'running'].includes(run.status) && run.seconds === 0) {
    return recordedTime(run.started) ? `Started ${recordedTime(run.started)}` : run.status === 'running' ? 'Running; start not recorded' : 'Queued; not started';
  }
  const value = Number(run.seconds);
  return Number.isFinite(value) && run.seconds !== undefined && run.seconds !== null
    ? `${value.toFixed(1)} seconds${['queued', 'running'].includes(run.status) ? ' so far' : ''}`
    : 'Timing not recorded';
}
function recordedTime(value) {
  if (!value || String(value).startsWith('0001-')) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date.toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
}
function selectedRun() { return ui.state?.runs.find((run) => run.id === ui.run); }
function runProposal(run) {
  if (!run) return undefined;
  if (run.proposal) return ui.state?.proposals.find((proposal) => proposal.id === run.proposal);
  const legacy = ui.state?.proposals.filter((proposal) => proposal.attribution?.run === run.id) || [];
  return legacy.length === 1 ? legacy[0] : undefined;
}

// Review is mutable; the run remains the recorded execution, including failures.
// An explicit proposal lets historical banners describe their own decision.
function runPresentation(run, proposal = runProposal(run)) {
  const reviewStatus = ['pending', 'accepted', 'rejected', 'stale'].includes(proposal?.status) ? proposal.status : null;
  const reviewed = ['accepted', 'rejected', 'stale'].includes(reviewStatus);
  const summaries = {
    accepted: 'Proposal accepted; an accepted-document checkpoint was recorded. This is a historical decision, not the current document state.',
    rejected: 'Proposal rejected; this proposal was not applied.',
    stale: 'Proposal stale; this proposal has not been accepted. Try again on latest before accepting.',
  };
  return {
    status: reviewed ? reviewStatus : run?.status,
    reviewed,
    summary: reviewed ? summaries[reviewStatus] : run?.summary || (run?.status === 'queued' ? 'Queued for the local model.' : run?.status === 'running' ? 'The agent is working. The accepted documents remain unchanged.' : 'No safe result summary recorded.'),
  };
}
function statusBadge(status) {
  const labels = { pending: 'Awaiting your approval', accepted: 'Accepted', rejected: 'Rejected', stale: 'Stale', running: 'Working', queued: 'Queued', proposed: 'Proposed', answered: 'Answered', failed: 'Failed', uncertain: 'Interrupted' };
  return element('span', `badge ${status || ''}`, labels[status] || status || 'Not recorded');
}

function proposalIdentity(proposal) {
  const identity = element('div', 'proposal-identity');
  const revisions = element('p');
  for (const [label, value] of [['Proposal', proposal.id], ['Revision', proposal.commit], ['Base', proposal.base]]) {
    const field = element('span', '', `${label}: ${typeof value === 'string' && value.length > 24 ? `${value.slice(0, 12)}…` : value || 'not recorded'}`);
    if (value) field.setAttribute('title', value);
    if (revisions.childElementCount) revisions.append(document.createTextNode(' · '));
    revisions.append(field);
  }
  identity.append(revisions);
  if (proposal.status === 'accepted') {
    const checkpoint = proposal.acceptedCommit;
    const history = element('p', '', `Accepted checkpoint: ${checkpoint?.length > 24 ? `${checkpoint.slice(0, 12)}…` : checkpoint || 'not recorded'}. Historical decision, not the current documents.`);
    if (checkpoint) history.setAttribute('title', checkpoint);
    identity.append(history);
  }
  return identity;
}

async function api(path, body) {
  const response = await fetch(path, body === undefined ? { credentials: 'same-origin' } : {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  ui.fixtureProvider = response.headers?.get?.('X-Canvas-Provider') === 'deterministic-fixture';
  let data;
  try { data = await response.json(); }
  catch { throw new Error(`The local service returned an unreadable response (${response.status}).`); }
  if (!response.ok) throw new Error(String(data?.error || `Request failed (${response.status}).`).slice(0, 1000));
  return data;
}

function notify(message, error = false, context = null) {
  // Replacing feedback also replaces ownership, so an older operation cannot clear it.
  ui.noticeContext = context;
  $('notice').textContent = message;
  $('notice').className = error ? 'notice error' : 'notice';
  $('notice').hidden = false;
}

function clearObsoleteNotice() {
  if (ui.noticeContext?.kind === 'question') {
    if (ui.state?.conversation?.some(entry => entry.kind === 'question' && entry.id === ui.noticeContext.message
      && ['answered', 'failed', 'uncertain'].includes(entry.status))) {
      $('notice').hidden = true;
      ui.noticeContext = null;
    }
    return;
  }
  const proposal = ui.state?.proposals.find((item) => item.id === ui.proposal);
  const reviewRun = proposal && proposalRun(proposal);
  if (ui.noticeContext && (ui.noticeContext.run !== ui.run || ui.noticeContext.agent !== ui.agent
    || (reviewRun && ui.noticeContext.run !== reviewRun.id))) {
    $('notice').hidden = true;
    ui.noticeContext = null;
  }
}

function renderModelGuidance() {
  const provider = $('provider-guidance');
  provider.hidden = !ui.fixtureProvider;
  const notice = ui.fixtureProvider ? 'Deterministic fixture provider. No real inference. Suite model names are authored identity, not execution qualification.' : '';
  if (provider.textContent !== notice) provider.textContent = notice;

}

function applyBusy() {
  renderRoomComposer();
  document.querySelectorAll('[data-mutation]').forEach((node) => {
    node.disabled = ui.busy || !genericMode() || node.dataset.unavailable === 'true';
  });
  $('role').disabled = ui.busy || !genericMode();
  for (const kind of ['run', 'input']) {
    $(`generic-${kind}-submit`).disabled = ui.busy || !genericMode() || $(`generic-${kind}-submit`).dataset.unavailable === 'true';
    $(`generic-${kind}-form`).setAttribute('aria-busy', String(ui.busy));
  }
}

function mutationButton(text, action, className, focusKey, unavailable = false) {
  const node = button(text, action, className, focusKey);
  node.dataset.mutation = 'true';
  node.dataset.unavailable = String(unavailable);
  node.disabled = ui.busy || unavailable;
  return node;
}

async function mutate(path, body, success) {
  if (ui.busy || !genericMode()) return;
  ui.busy = true;
  applyBusy();
  try {
    const data = await api(path, body);
    if (success) success(data);
    ui.stateGeneration += 1;
    await refreshState();
  } catch (error) {
    notify(error.message, true);
    // A rejected CAS may have made a proposal stale; refresh the review surface.
    ui.stateGeneration += 1;
    await refreshState();
  } finally {
    ui.busy = false;
    applyBusy();
  }
}

function resumeTarget(runID, messageID = '') {
  if (!genericMode()) return null;
  const run = ui.state?.runs.find(item => item.id === runID);
  const entry = messageID && ui.state?.conversation?.find(item => item.id === messageID && item.kind === 'question' && item.run === runID);
  if (!run?.session || !Number.isSafeInteger(run.sessionVersion) || run.sessionVersion < 0
    || (messageID ? !entry || entry.session !== run.session : run.proposal)
    || !['failed', 'uncertain'].includes(messageID ? entry.status : run.status)) return null;
  return { run, entry, stamp: JSON.stringify([ui.run, ui.state.role, run.id, run.session, run.sessionVersion, run.status, run.proposal, messageID, entry?.status, entry?.lastSeq]) };
}

function resumeKey(runID, messageID) { return JSON.stringify([runID, messageID]); }

function observeResumeState() {
  for (const [key, panel] of ui.resumePanels) {
    if (resumeTarget(panel.runID, panel.messageID)?.stamp !== panel.stamp) ui.resumePanels.delete(key);
  }
}

function redrawResume() {
  ui.conversationKey = '';
  renderConversation();
  applyBusy();
}

async function inspectResume(runID, messageID = '') {
  if (ui.busy) return;
  const target = resumeTarget(runID, messageID);
  if (!target) return;
  const key = resumeKey(runID, messageID);
  const panel = { runID, messageID, stamp: target.stamp, status: 'checking' };
  ui.resumePanels.set(key, panel);
  const focusEpoch = ui.focusEpoch;
  redrawResume();
  try {
    const query = new URLSearchParams({ run: runID });
    if (messageID) query.set('message', messageID);
    const check = await api(`/api/resume?${query}`);
    if (ui.resumePanels.get(key) !== panel || resumeTarget(runID, messageID)?.stamp !== panel.stamp) return;
    if (check?.available !== true) {
      panel.status = 'blocked';
      panel.reason = typeof check?.reason === 'string' ? check.reason.slice(0, 1000) : 'Recorded request could not be verified.';
    } else if (check.session !== target.run.session || check.sessionVersion !== target.run.sessionVersion
      || !Number.isSafeInteger(check.lastSeq) || check.lastSeq < 1 || typeof check.execution !== 'string' || !check.execution
      || typeof check.head !== 'string' || !check.head) {
      panel.status = 'blocked'; panel.reason = 'The session changed or its evidence is incomplete. Refresh and check again.';
    } else {
      panel.check = check;
      panel.actionId = crypto.randomUUID().replaceAll('-', '');
      panel.status = 'ready';
    }
  } catch (error) {
    if (ui.resumePanels.get(key) !== panel) return;
    panel.status = 'blocked'; panel.reason = error.message;
  }
  if (ui.resumePanels.get(key) !== panel || resumeTarget(runID, messageID)?.stamp !== panel.stamp) return;
  redrawResume();
  if (panel.status === 'ready' && ui.focusEpoch === focusEpoch) {
    const confirm = Array.from($('conversation-content').querySelectorAll('[data-focus-key]')).find(node => node.dataset.focusKey === `confirm-resume-${key}`);
    confirm?.focus({ preventScroll: true });
  }
}

function confirmResume(key, panel) {
  if (ui.busy || ui.resumePanels.get(key) !== panel || panel.status !== 'ready'
    || resumeTarget(panel.runID, panel.messageID)?.stamp !== panel.stamp) return;
  // Consume this browser confirmation before sending. An uncertain response never reuses it.
  ui.resumePanels.delete(key);
  invalidateSessionProof(panel.check.session);
  redrawResume();
  const { session, execution, lastSeq, head, sessionVersion } = panel.check;
  return mutate('/api/resume', { run: panel.runID, ...(panel.messageID ? { message: panel.messageID } : {}), role: ui.state.role,
    confirmation: { confirmed: true, actionId: panel.actionId, session, execution, lastSeq, head, sessionVersion } }, () => {
    notify('Resume requested on the recorded session. Review the recorded result when it is available.');
  });
}

function appendResumeControls(container, run, messageID = '') {
  if (!resumeTarget(run.id, messageID)) return;
  const key = resumeKey(run.id, messageID);
  const panel = ui.resumePanels.get(key);
  const controls = element('div', 'message-actions');
  if (panel?.status === 'ready') {
    container.append(element('p', 'action-warning', 'Re-sends the last model request; it may be charged again.'));
    controls.append(mutationButton('Confirm Resume', () => confirmResume(key, panel), 'small-button', `confirm-resume-${key}`));
    controls.append(button('Cancel Resume', () => {
      ui.resumePanels.delete(key); redrawResume();
      Array.from($('conversation-content').querySelectorAll('[data-focus-key]')).find(node => node.dataset.focusKey === `resume-${key}`)?.focus({ preventScroll: true });
    }, 'small-button', `cancel-resume-${key}`));
  } else {
    if (panel?.status === 'blocked') container.append(element('p', 'action-warning', `Resume unavailable: ${panel.reason}`));
    const checking = panel?.status === 'checking';
    controls.append(mutationButton(checking ? 'Checking recorded request…' : 'Resume', () => inspectResume(run.id, messageID), 'small-button', `resume-${key}`, checking));
  }
  container.append(controls);
}

function navItem(name, detail, action, selected, kind, focusKey) {
  const node = action ? button('', action, 'nav-item', focusKey) : element('div', 'nav-item');
  if (selected) { node.classList.add('selected'); node.setAttribute('aria-current', 'true'); }
  node.append(element('span', `dot ${kind || ''}`));
  const copy = element('span', 'nav-copy');
  copy.append(element('span', 'nav-name', name), element('span', 'nav-detail', detail));
  node.append(copy);
  return node;
}

function selectProposal(proposal) {
  const run = proposal && proposalRun(proposal);
  ui.run = run?.id || null;
  if (run) ui.agent = run.agent;
  ui.proposal = proposal?.id || '';
}

function selectRun(run) {
  ui.run = run.id;
  ui.agent = run.agent;
  ui.proposal = runProposal(run)?.id || null;
  const proposal = runProposal(run);
  if (proposal) {
    const paths = Object.keys(proposal.changes || {});
    if (!paths.includes(ui.path)) ui.path = paths[0] || ui.path;
  }
  renderState();
}

function renderNavigation() {
  const state = ui.state;
  const key = JSON.stringify([state.runs, state.runs.map((run) => [runProposal(run)?.id, runProposal(run)?.status]), state.agents, state.people, state.triggers, ui.run, ui.agent]);
  if (key === ui.navigationKey) return;
  ui.navigationKey = key;
  preserveFocus($('navigation'), () => {
    $('sessions').replaceChildren();
    const ordinary = orderedRuns().reverse();
    for (const run of ordinary) {
      $('sessions').append(navItem(runTitle(run), `${runPresentation(run).status} · ${seconds(run)}`, () => selectRun(run), ui.run === run.id, runPresentation(run).status, `run-${run.id}`));
    }
    if (!ordinary.length) $('sessions').append(element('p', 'muted', 'No sessions yet. Request a declared agent run from New….'));
    $('agents').replaceChildren();
    for (const agent of state.agents) {
      const latest = orderedRuns().filter((run) => run.agent === agent.id).slice(-1)[0];
      const node = navItem(agent.name, `${latest ? runPresentation(latest).status : 'No runs yet'} · ${agent.model}`, () => {
        ui.agent = agent.id;
        ui.run = latest?.id || null;
        ui.proposal = null;
        renderState();
      }, ui.agent === agent.id, runPresentation(latest).status, `agent-${agent.id}`);
      const group = element('div', `agent-group speaker-${agent.id}`);
      group.append(node);
      const triggers = element('ul', 'agent-triggers');
      triggers.setAttribute('aria-label', `${agent.name} triggers`);
      for (const trigger of state.triggers.filter((item) => item.agent === agent.id)) {
        const entry = element('li');
        const label = trigger.type === 'manual' ? 'When someone asks' : `When a source file matches ${trigger.path}`;
        const count = state.runs.filter((run) => run.agent === trigger.agent && run.trigger === trigger.name).length;
        entry.append(element('span', 'trigger-label', label), element('span', 'trigger-detail', `${trigger.name} · ${count} recorded ${count === 1 ? 'run' : 'runs'} for agent`));
        triggers.append(entry);
      }
      if (triggers.childElementCount) group.append(triggers);
      group.append(element('p', 'muted', evaluationLabel(agent)));
      const suite = disclosure('Suite identity', `suite-${agent.id}`, 'source-identity');
      suite.append(suiteEvidence(agent));
      group.append(suite);
      $('agents').append(group);
    }
    $('flows').replaceChildren(element('p', 'muted', 'Each trigger starts one agent. No automatic multi-agent workflow.'));
    $('people').replaceChildren();
    for (const person of state.people) {
      const speaker = speakerInfo('human', person.id);
      const node = element('div', `nav-item speaker-${speaker.color}`);
      const avatar = element('span', 'person-avatar', speaker.initials);
      avatar.setAttribute('aria-hidden', 'true');
      node.append(avatar);
      const copy = element('span', 'nav-copy');
      copy.append(element('span', 'nav-name', person.name), element('span', 'nav-detail', person.title));
      node.append(copy);
      $('people').append(node);
    }
  });
}

function speakerInfo(kind, actor = '') {
  const id = kind === 'human' ? actor : kind;
  const palette = kind === 'human'
    ? { GM: ['gm', 'GM'], coach: ['coach', 'CO'], owner: ['owner', 'OW'] }
    : {};
  const [color, initials] = Object.hasOwn(palette, id) ? palette[id] : ['unknown', '?'];
  const person = kind === 'human' && ui.state.people.find((item) => item.id === actor);
  const name = kind === 'human' ? person?.name || (actor ? `Recorded actor: ${actor}` : 'Actor not recorded') : agentName(kind);
  return { color, initials, name };
}

function message(title, text, meta, kind = '', actor = '') {
  const speaker = speakerInfo(kind, actor);
  const row = element('article', `message speaker-${speaker.color}`);
  const avatar = element('span', 'message-avatar', speaker.initials);
  avatar.setAttribute('aria-hidden', 'true');
  const body = element('div', 'message-body');
  body.append(element('h3', 'speaker-name', speaker.name));
  if (title !== speaker.name) body.append(element('span', 'message-context', title));
  if (meta) body.append(element('span', 'message-meta', meta));
  if (text) body.append(element('p', '', text));
  row.append(avatar, body);
  return { row, body };
}

function showProposal(run) {
  const proposal = runProposal(run);
  if (!proposal) return;
  selectProposal(proposal);
  const paths = Object.keys(proposal.changes || {});
  if (!paths.includes(ui.path)) ui.path = paths[0] || ui.path;
  renderState();
  revealReview();
}

function proofContent(proof, key = '') {
  const wrapper = element('div', 'proof-region');
  wrapper.setAttribute('role', 'region');
  wrapper.setAttribute('aria-label', 'Recorded replay integrity result');
  wrapper.tabIndex = -1;
  if (key) { wrapper.id = `proof-${key}`; wrapper.dataset.focusKey = wrapper.id; }
  wrapper.append(element('p', 'muted', 'Recorded verification evidence. Replay delivers recorded events; reconstruction must make no new model calls.'));
  const list = element('dl', 'proof-list');
  const entries = [
    ['Journal chain', proof.chainValid], ['Reconstructed', proof.reconstructed],
    ['Outputs match', proof.outputsMatch], ['Verifier left live journal untouched', proof.liveUnchanged],
  ];
  for (const [label, value] of entries) {
    list.append(element('dt', '', label), element('dd', '', value === true ? 'Verified' : value === false ? 'Failed' : 'Evidence missing'));
  }
  list.append(element('dt', '', 'Model calls during reconstruction'), element('dd', '', proof.modelCalls ?? 'Evidence missing'));
  list.append(element('dt', '', 'Journal records'), element('dd', '', proof.records ?? 'Evidence missing'));
  wrapper.append(list, element('p', 'muted', `Copied snapshot head: ${proof.head || 'not recorded'}. Reconstruction covers completed turns; unfinished effects are not regenerated.`));
  const models = Array.isArray(proof.requestedModels) ? proof.requestedModels.join(', ') : proof.requestedModels;
  wrapper.append(element('p', 'muted', `Observed requested models: ${models || 'evidence missing'}`));
  return wrapper;
}

function questionSession(entry) {
  return entry.session || ui.state.runs.find((run) => run.id === entry.run)?.session;
}

function pendingQuestion(session) {
  return Boolean(session && (ui.state.conversation || []).some((entry) => entry.kind === 'question'
    && questionSession(entry) === session && ['queued', 'running'].includes(entry.status)));
}

function askUnavailable(run = selectedRun()) {
  if (!run) {
    if (ui.proposal === '') return 'Accepted content only: no Ask source is selected. Select a recorded attempt to ask its agent why.';
    if (ui.proposal) return 'Originating attempt not recorded or ambiguous for this proposal: no Ask source is assumed. You can still post to the room.';
    return 'Select a recorded session to ask its agent why. You can still post to the room.';
  }
  if (run.questionsSupported !== true) return 'Questions unsupported for this recorded session';
  if (!run.session) return 'Session UID not recorded. Questions unavailable.';
  if (ui.state.runs.some((item) => item.session === run.session && ['queued', 'running'].includes(item.status))) {
    return 'Wait for the original run to finish before asking why.';
  }
  if (pendingQuestion(run.session)) return 'A read-only question is pending on this same session. Wait for its answer.';
  return '';
}

function renderRoomComposer() {
  const unavailable = !ui.state ? 'Waiting for the room to load.' : !genericMode() ? 'Unsupported state. Load a declared definition to work in Canvas.' : askUnavailable();
  const run = selectedRun();
  const context = unavailable || `Ask ${agentName(run.agent)} about ${runTitle(run)}, on this recorded session, read-only.`;
  if ($('room-context').textContent !== context) $('room-context').textContent = context;
  $('room-ask').dataset.unavailable = String(!ui.state || Boolean(unavailable));
  $('room-post').dataset.unavailable = String(!genericMode());
  $('room-ask').disabled = ui.busy || !ui.state || Boolean(unavailable);
  $('room-post').disabled = ui.busy || !genericMode();
  $('room-form').setAttribute('aria-busy', String(ui.busy));
}

function invalidateSessionProof(session) {
  if (!session) return;
  ui.invalidatedSessions.set(session, (ui.invalidatedSessions.get(session) || 0) + 1);
  for (const run of ui.state.runs.filter((item) => item.session === session)) {
    ui.verified.delete(run.id);
    ui.proofs.delete(run.id);
  }
  ui.conversationKey = '';
  ui.documentKey = '';
}

function observeQuestionIntents() {
  // A completed recovery in another tab can change the source without a new question.
  for (const run of ui.state.runs) {
    if (!run.session || !Number.isSafeInteger(run.sessionVersion)) continue;
    if (ui.sessionVersions.has(run.session) && ui.sessionVersions.get(run.session) !== run.sessionVersion) invalidateSessionProof(run.session);
    ui.sessionVersions.set(run.session, run.sessionVersion);
  }
  const initial = !ui.questionIntentsLoaded;
  for (const entry of ui.state.conversation || []) {
    if (entry.kind !== 'question') continue;
    const session = questionSession(entry);
    // A new durable question intent changes the raw source even before its answer.
    if (!ui.questionIntents.has(entry.id) || ui.questionIntents.get(entry.id) !== session) {
      // Initial answered history does not make a returned backend proof stale.
      // Pending turns still block proof display until fresh verification.
      if (!initial || ['queued', 'running'].includes(entry.status)) invalidateSessionProof(session);
      ui.questionIntents.set(entry.id, session);
    }
  }
  ui.questionIntentsLoaded = true;
}

function currentRunProof(run) {
  if (!run) return null;
  const cached = ui.proofs.get(run.id);
  // Once a question changes this source, only explicit fresh verification can
  // restore proof. A sibling run's returned proof may still predate the question.
  return ui.invalidatedSessions.has(run.session) ? cached || null : cached || run.proof;
}

async function submitRoom(kind) {
  if (!genericMode() || ui.busy) return;
  if (kind === 'question' && askUnavailable()) { notify(askUnavailable(), true); return; }
  const field = $('room-request');
  if (trimmedValue(field, 1, 'Enter a comment or question.') === null) return;
  const request = field.value;
  const run = selectedRun();
  return mutate(kind === 'question' ? '/api/ask' : '/api/comment', { run: ui.run || '', request, role: ui.state.role }, (data) => {
    // Do not consume a new draft typed while this particular POST was in flight.
    if (field.value === request) field.value = '';
    if (kind === 'question') invalidateSessionProof(run.session);
    notify(kind === 'question' ? 'Read-only question queued on the recorded session. No document changes requested.'
      : 'Comment posted to the shared room. No agent was invoked.', false,
      kind === 'question' && data.message ? { kind: 'question', message: data.message } : null);
  });
}

function appendRoomConversation(content, changedKey) {
  let changedRow = null;
  const entries = ui.state.conversation || [];
  content.append(element('h3', 'room-thread-heading', 'Shared room'));
  if (!entries.length) content.append(element('p', 'muted', 'Post a comment for the room, or select a supported recorded session and ask its agent why. Comments do not invoke agents.'));
  for (const entry of entries) {
    const question = entry.kind === 'question';
    const known = question || entry.kind === 'comment';
    const request = message(question ? `Ask ${agentName(entry.agent)} why` : known ? 'Room comment' : `Unknown room entry kind: ${entry.kind || 'not recorded'}`,
      entry.text || 'Text not recorded', '', 'human', entry.actor || '');
    request.row.dataset.conversationId = entry.id;
    if (question) { request.row.dataset.focusKey = `question-request-${entry.id}`; request.row.tabIndex = -1; }
    const at = recordedTime(entry.at);
    const time = element(at ? 'time' : 'span', 'message-meta', at || 'Time not recorded');
    if (at) time.setAttribute('datetime', entry.at);
    request.body.append(time);
    if (entry.run) {
      const target = ui.state.runs.find((run) => run.id === entry.run);
      request.body.append(element('p', 'muted', `Recorded target: ${target ? runTitle(target) : 'Run not available'} · ${agentName(entry.agent || target?.agent)}`));
      const source = disclosure('Source', `room-${entry.id}`);
      source.append(evidenceRecord('Recorded room context', { run: entry.run, session: entry.session || 'not recorded', agent: entry.agent || 'not recorded' }));
      request.body.append(source);
    }
    content.append(request.row);
    if (`room-${entry.id}` === changedKey) changedRow = request.row;
    if (!question) continue;
    const states = {
      queued: 'Queued for a read-only answer on the recorded session. No document changes requested.',
      running: 'Answering a read-only question on the recorded session. Accepted and proposed documents stay unchanged.',
      answered: entry.answer || 'Answer text not recorded.',
      failed: 'Read-only question failed. No document changes requested.',
      uncertain: 'Interrupted read-only question. Recorded evidence is retained; this is not automatically retried.',
    };
    const text = Object.hasOwn(states, entry.status) ? states[entry.status] : `Unknown question status: ${entry.status || 'not recorded'}. No completion is assumed.`;
    const answer = message('answering a question · read-only', text, '', entry.agent || 'unknown');
    answer.row.dataset.conversationId = entry.id;
    answer.row.dataset.focusKey = `question-answer-${entry.id}`;
    answer.row.tabIndex = -1;
    answer.body.append(statusBadge(entry.status));
    if (entry.error) answer.body.append(element('p', 'action-warning', entry.error));
    if (entry.recoveryError) answer.body.append(element('p', 'action-warning', entry.recoveryError));
    const target = ui.state.runs.find(item => item.id === entry.run);
    if (target) appendResumeControls(answer.body, target, entry.id);
    const source = disclosure('Source', `answer-${entry.id}`);
    source.append(evidenceRecord('Recorded answer context', { run: entry.run || 'not recorded', session: entry.session || 'not recorded', agent: entry.agent || 'not recorded', model: entry.model || 'Model not recorded', duration: seconds(entry), status: entry.status, lastSeq: entry.lastSeq, resumeStatus: entry.resumeStatus, recoveryError: entry.recoveryError }));
    answer.body.append(source);
    content.append(answer.row);
    if (`room-${entry.id}` === changedKey) changedRow = answer.row;
  }
  return changedRow;
}

function observeConversationMessages() {
  let changed = null;
  // Record every run so selecting an already-known session is not a new message.
  for (const run of ui.state.runs) {
    const key = `run-${run.id}`;
    const value = JSON.stringify([run.requestSummary, run.status, run.summary, run.error]);
    if (run.id === ui.run && ui.conversationMessages.get(key) !== value) changed = key;
    ui.conversationMessages.set(key, value);
  }
  for (const entry of ui.state.conversation || []) {
    const key = `room-${entry.id}`;
    const value = JSON.stringify([entry.kind, entry.text, entry.status, entry.answer, entry.error]);
    if (ui.conversationMessages.get(key) !== value) changed = key;
    ui.conversationMessages.set(key, value);
  }
  return changed;
}

function renderConversation() {
  observeQuestionIntents();
  observeResumeState();
  const run = selectedRun();
  const proposal = runProposal(run);
  const proof = ui.verified.has(run?.id) ? currentRunProof(run) : null;
  const key = JSON.stringify([run, proposal?.status, ui.agent, ui.path, ui.state.role, ui.state.people, ui.state.agents, ui.state.conversation, proof, ui.verified.has(run?.id), Array.from(ui.resumePanels)]);
  const changedKey = observeConversationMessages();
  if (key === ui.conversationKey) return;
  ui.conversationKey = key;
  const content = $('conversation-content');
  const previousScroll = content.scrollTop;
  let changedRow = null;
  $('conversation-title').textContent = 'Room conversation';
  $('conversation-meta').textContent = run ? `${runTitle(run)} · ${runPresentation(run).status} · ${seconds(run)}` : 'Shared comments and recorded session summaries';
  preserveFocus(content, () => {
    content.replaceChildren();
    if (!run) {
      const welcome = element('div', 'empty-state');
      welcome.append(element('h3', '', 'Work on the declared documents'));
      welcome.append(element('p', '', 'Request an agent run from New… or save a declared input file. Review the complete proposed replacements before accepting.'));
      welcome.append(element('p', 'muted', 'Fictional project data. Released evaluation evidence is separate from local execution qualification.'));
      content.append(welcome);
      changedRow = appendRoomConversation(content, changedKey);
      collectEvidence(content, 'conversation-evidence');
      return;
    }
    const request = message('Agent run requested', run.requestSummary || 'Request summary not recorded.', '', 'human', run.actor || '');
    content.append(request.row);
    const presentation = runPresentation(run);
    const result = message(proposal ? 'proposed a document update' : ['queued', 'running'].includes(run.status) ? 'working on a recommendation' : 'recorded assessment', presentation.summary, '', run.agent);
    result.body.append(statusBadge(proposal?.status || presentation.status));
    result.body.append(element('p', 'muted', evaluationLabel(run)));
    if (run.error) result.body.append(element('p', 'action-warning', run.error));
    if (run.recoveryError) result.body.append(element('p', 'action-warning', run.recoveryError));
    if (run.status === 'uncertain') result.body.append(element('p', 'action-warning', 'Interrupted attempt. Review the evidence or choose Try again on latest; no automatic retry.'));
    if (run.status === 'failed' && !run.error) result.body.append(element('p', 'action-warning', proposal ? 'The recorded execution failed; the proposal decision is shown separately.' : 'This attempt failed without a proposal. Review the evidence or choose Try again on latest.'));
    const actions = element('div', 'message-actions');
    if (proposal) actions.append(button('View proposal', () => showProposal(run), 'small-button', 'view-proposal'));
    if (!proposal && ['failed', 'uncertain'].includes(run.status)) actions.append(mutationButton('Try again on latest', () => mutate('/api/regenerate', { proposal: run.id, role: ui.state.role }, (data) => {
      ui.run = data.run;
      ui.agent = run.agent;
      ui.proposal = null;
      notify(tryAgainNotice);
    }), 'small-button', `regenerate-attempt-${run.id}`));
    const evidence = disclosure('Evidence', `run-${run.id}`);
    evidence.append(evidenceRecord('Recorded run', { run: run.id, actor: run.actor, model: run.model, duration: seconds(run), executionStatus: run.status, recordedSummary: run.summary, session: run.session || 'not recorded', sessionVersion: run.sessionVersion, resumeStatus: run.resumeStatus, recoveryError: run.recoveryError, base: run.base, scenario: run.scenario, comparison: run.comparison, previousAttempt: run.previousAttempt, parentSession: run.parentSession, forkCheckpoint: run.forkSeq, created: run.created, started: run.started, completed: run.completed, recovery: run.status === 'uncertain' ? 'Recorded evidence retained. Interrupted attempts are not silently retried.' : undefined }));
    evidence.append(recordedActivity(run, true));
    evidence.append(suiteEvidence({ ...run, suite: run.suite || proposal?.attribution?.suite }));
    const agent = ui.state.agents.find((item) => item.id === run.agent);
    evidence.append(evidenceRecord('Recorded agent revision', { executionBundleDigest: run.executionBundleDigest || run.bundleDigest || proposal?.attribution?.bundleDigest || 'not recorded', authoredBundleDigest: run.authoredBundleDigest || proposal?.attribution?.authoredBundleDigest || 'not recorded' }));
    if (agent) evidence.append(evidenceRecord('Current configuration, not historical evidence', { executionBundleDigest: agent.digest, authoredBundleDigest: agent.authoredDigest || agent.digest, model: agent.model }));
    const saved = currentRunProof(run);
    if (!replayVerified(saved)) { const verify = verifyRunButton(run); if (verify) evidence.append(verify); }
    else {
      const region = proofContent(saved, `run-${run.id}`);
      evidence.append(button('Replay verified · open saved proof', () => openEvidence(evidence, region), 'small-button', `saved-proof-${run.id}`), region);
    }
    if (actions.childElementCount) result.body.append(actions);
    if (!proposal && ['failed', 'uncertain'].includes(run.status)) {
      result.body.append(element('p', 'muted', tryAgainHelp));
      appendResumeControls(result.body, run);
    }
    if (ui.verified.has(run.id)) {
      if (proof) evidence.append(proofContent(proof));
      else evidence.append(element('p', 'muted', 'Verification evidence has not been returned. No chain or replay success is assumed.'));
      if (!replayVerified(proof)) {
        result.body.append(element('p', 'action-warning', 'Verification incomplete or failed. Inspect Evidence before relying on replay or reconstruction.'));
      }
    }
    result.body.append(evidence);
    content.append(result.row);
    if (`run-${run.id}` === changedKey) changedRow = result.row;

    changedRow = appendRoomConversation(content, changedKey) || changedRow;
    collectEvidence(content, 'conversation-evidence');
  });
  content.scrollTop = previousScroll;
  // Answers can change above later comments. Reveal that row, not unrelated history.
  if (changedRow) {
    if (changedRow === content.lastElementChild) content.scrollTop = content.scrollHeight;
    else content.scrollTop += changedRow.getBoundingClientRect().top - content.getBoundingClientRect().top - content.clientTop;
  }
}

function documentPaths() {
  const paths = new Set(ui.state.documents.filter((path) => !path.includes('*')));
  for (const proposal of ui.state.proposals) Object.keys(proposal.changes || {}).forEach((path) => paths.add(path));
  return [...paths];
}

function proposalHasDocument(proposal, path = ui.path) {
  return Object.hasOwn(proposal.changes || {}, path);
}

function documentLabel(path) {
  return ui.state.definition.documentTitles?.[path] || path;
}

function currentProposal() {
  if (ui.proposal === '') return undefined;
  const candidates = ui.state.proposals.filter((proposal) => proposalHasDocument(proposal));
  const explicit = candidates.find((proposal) => proposal.id === ui.proposal);
  if (explicit) return explicit;
  const run = selectedRun();
  if (run) return candidates.find((proposal) => proposal.id === runProposal(run)?.id);
  return candidates.slice().reverse().find((proposal) => ['pending', 'stale'].includes(proposal.status));
}

function proposalScope(proposal) {
  const scope = element('div', 'proposal-scope');
  const paths = Object.keys(proposal.changes || {});
  scope.append(element('p', 'scope-line', paths.map(documentLabel).join(' · ') || 'Affected documents not recorded'));
  const detail = disclosure('Review affected documents', `scope-${proposal.id}`, 'scope-detail');
  detail.append(element('p', 'muted', ['pending', 'stale'].includes(proposal.status)
    ? 'Accept saves all affected documents together. Reject leaves accepted documents unchanged.'
    : ['accepted', 'rejected'].includes(proposal.status) ? 'Historical decision; not the current accepted documents.' : 'Review decision not recorded.'));
  const list = element('ul', 'affected-documents');
  for (const path of paths) {
    const item = element('li');
    const review = button(`Review ${documentLabel(path)}`, () => {
      selectProposal(proposal);
      ui.path = path;
      ui.documentKey = '';
      renderState(); revealReview();
    }, 'small-button', `affected-${proposal.id}-${path}`);
    review.setAttribute('title', path);
    item.append(review); list.append(item);
  }
  detail.append(list.childElementCount ? list : element('p', 'muted', 'Affected documents not recorded.'));
  scope.append(detail);
  return scope;
}

function proposalBanner(proposal) {
  const banner = element('div', `proposal-banner ${proposal.status}`);
  const agent = proposal.attribution?.agent;
  banner.append(element('h3', '', proposalName(proposal)), statusBadge(proposal.status));
  const attribution = proposal.attribution || {};
  const run = proposalRun(proposal);
  banner.append(element('p', 'muted', agentName(agent)));
  const presentation = runPresentation(run, proposal);
  if (presentation.reviewed || run?.summary) banner.append(element('p', '', presentation.summary));
  if (run?.error) banner.append(element('p', 'action-warning', run.error));
  const evidence = disclosure('Evidence', `proposal-${proposal.id}`);
  const meta = element('div', 'decision-meta');
  meta.append(runDecisionStatus(run), element('span', '', typeof run?.seconds === 'number' && Number.isFinite(run.seconds) && run.seconds >= 0 ? seconds(run) : 'Time not recorded'), element('span', '', run?.model || 'Model not recorded'));
  for (const [label, value] of [['Started', run?.started], ['Completed', run?.completed]]) {
    if (recordedTime(value)) { const time = element('time', '', `${label} ${recordedTime(value)}`); time.setAttribute('datetime', value); meta.append(time); }
  }
  evidence.append(element('p', 'muted', agentName(run?.agent || agent)), meta, proposalIdentity(proposal), element('p', 'muted', tryAgainHelp));
  const proof = currentRunProof(run);
  const proofRegion = proof ? proofContent(proof, `document-${ui.path}-${proposal.id}`) : null;
  if (replayVerified(proof)) {
    const saved = button('Replay verified · open saved proof', () => openEvidence(evidence, proofRegion), 'small-button', `saved-proof-${run.id}`);
    saved.setAttribute('aria-controls', proofRegion.id);
    evidence.append(saved);
  } else { const verify = verifyRunButton(run); if (verify) evidence.append(verify); }
  if (proofRegion) evidence.append(proofRegion);
  evidence.append(evidenceRecord('Proposal provenance and input revisions', { proposal: proposal.id, commit: proposal.commit, acceptedCommit: proposal.acceptedCommit, executionStatus: run?.status, recordedSummary: run?.summary, base: proposal.base, attribution, session: run?.session, scenario: run?.scenario, comparison: run?.comparison, previousAttempt: run?.previousAttempt, parentSession: run?.parentSession, forkCheckpoint: run?.forkSeq }));
  evidence.append(recordedActivity(run), recordedActivity(run, true));
  banner.append(evidence, proposalScope(proposal));
  const actions = proposalActions(proposal);
  if (actions) banner.append(actions);
  return banner;
}

function proposalActions(proposal) {
  if (['pending', 'stale'].includes(proposal.status)) {
    const actions = element('div', 'review-actions');
    actions.append(mutationButton('Accept proposal', () => mutate('/api/review', { proposal: proposal.id, action: 'accept', role: ui.state.role }, () => {
      ui.documentVersion += 1; // Discard document responses initiated before acceptance.
      ui.documentKey = '';
      notify('Proposal accepted. Accepted documents are refreshing.');
    }), 'accept primary', `accept-${proposal.id}`, proposal.status === 'stale'));
    actions.append(mutationButton('Reject', () => mutate('/api/review', { proposal: proposal.id, action: 'reject', role: ui.state.role }, () => notify('Proposal rejected. Accepted documents are unchanged.')), 'tertiary', `reject-${proposal.id}`));
    const again = mutationButton('Try again', () => mutate('/api/regenerate', { proposal: proposal.id, role: ui.state.role }, (data) => {
      if (data.run) { ui.run = data.run; ui.proposal = null; ui.documentKey = ''; }
      notify(tryAgainNotice);
    }), proposal.status === 'stale' ? 'primary' : '', `regenerate-${proposal.id}`);
    if (proposal.status === 'stale') actions.append(again);
    else {
      const more = disclosure('…', `proposal-menu-${proposal.id}`, 'proposal-menu');
      more.children[0].setAttribute('aria-label', 'More proposal actions');
      more.children[0].setAttribute('title', 'More proposal actions');
      const menu = element('div', 'menu');
      menu.append(again, element('p', 'muted', tryAgainHelp));
      more.append(menu); actions.append(more);
    }
    if (proposal.status === 'stale') {
      const reason = element('p', 'action-warning', staleReason);
      reason.id = `stale-${proposal.id}`;
      actions.children[0].setAttribute('aria-describedby', reason.id);
      actions.append(reason);
    }
    if (proposal.status !== 'stale') actions.append(element('span', 'muted action-note', 'Accepts all affected documents.'));
    return actions;
  }
  return null;
}

function runDecisionStatus(run) {
  if (!run) return statusBadge('not recorded');
  const labels = { queued: 'Queued', running: 'Running', failed: 'Failed', uncertain: 'Interrupted', interrupted: 'Interrupted' };
  if (labels[run.status]) {
    const badge = statusBadge(run.status);
    badge.textContent = labels[run.status];
    return badge;
  }
  if (run.status === 'proposed') {
    const finished = Boolean(recordedTime(run.completed)) && !run.error
      && Array.isArray(run.activity) && run.activity.some((step) => step?.kind === 'finished' && recordedTime(step.at));
    const reviewed = runPresentation(run).reviewed;
    const badge = statusBadge(run.error ? 'failed' : finished ? 'finished' : reviewed ? 'published' : 'proposed');
    badge.textContent = run.error ? 'Needs attention' : finished ? 'Finished' : reviewed ? 'Publication recorded' : 'Proposed';
    return badge;
  }
  return statusBadge(run.status);
}

function replayVerified(proof) {
  return proof && [proof.chainValid, proof.reconstructed, proof.outputsMatch, proof.liveUnchanged].every((value) => value === true) && proof.modelCalls === 0;
}

function verifyRunButton(run) {
  if (!run?.session || ['queued', 'running'].includes(run.status)) return null;
  return mutationButton('Verify replay & reconstruction', () => {
    observeQuestionIntents();
    const version = ui.invalidatedSessions.get(run.session);
    return mutate('/api/verify', { run: run.id }, (data) => {
      observeQuestionIntents();
      if (version !== ui.invalidatedSessions.get(run.session)) {
        notify('The session source changed during verification. Verify again after the current session work finishes.');
        return;
      }
      ui.verified.add(run.id);
      const proof = data.proof || (Object.hasOwn(data, 'chainValid') ? data : null);
      if (proof) ui.proofs.set(run.id, proof);
      ui.conversationKey = '';
      ui.documentKey = '';
      notify('Verification requested. Only returned evidence is shown.');
    });
  }, 'small-button', `verify-${run.id}`, pendingQuestion(run.session));
}

function activityLabel(step) {
  if (step.kind === 'input-read') return typeof step.document === 'string' && documentPaths().includes(step.document) ? `Loaded ${documentLabel(step.document)}` : null;
  const labels = { snapshot: 'Inputs checkpointed', 'execution-started': 'Execution started', 'model-requested': 'Model request recorded', 'output-received': 'Output received', validated: 'Recommendation validated', proposed: 'Proposal published', finished: 'Finished', resumed: 'Resumed', failed: 'Failed', interrupted: 'Interrupted', recovered: 'Proposal recovered' };
  return Object.hasOwn(labels, step.kind) ? labels[step.kind] : null;
}

function recordedActivity(run, full = false) {
  const section = element('section', 'decision-activity');
  section.append(element('h3', '', full ? 'Full recorded chronology' : 'Recorded activity'));
  const recorded = Array.isArray(run?.activity) ? run.activity.filter((step) => step && activityLabel(step) && recordedTime(step.at)) : [];
  const meaningful = recorded.filter((step) => ['input-read', 'validated', 'proposed', 'failed', 'interrupted', 'recovered'].includes(step.kind));
  const activity = full || !meaningful.length ? recorded : meaningful;
  if (!activity.length) section.append(element('p', 'muted', 'Activity not recorded'));
  else {
    const list = element('ol', 'activity-trail');
    for (const step of activity) {
      const row = element('li');
      const time = element('time', '', new Date(step.at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit', second: '2-digit' }));
      time.setAttribute('datetime', step.at);
      row.append(time, element('span', '', activityLabel(step)));
      list.append(row);
    }
    section.append(list);
  }
  return section;
}

function documentContent(text, fullText = false) {
  if (fullText) return element('pre', 'document-text', text);
  const root = element('div', 'document-markdown');
  const lines = String(text).split('\n');
  const cells = (line) => line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((cell) => cell.trim());
  const isRule = (line) => /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$/.test(line);
  let index = 0;
  while (index < lines.length) {
    if (!lines[index].trim()) { index += 1; continue; }
    if (lines[index].includes('|') && index + 1 < lines.length && isRule(lines[index + 1])) {
      const table = element('table', 'document-table');
      const head = element('thead');
      const row = element('tr');
      const headers = cells(lines[index]);
      for (const text of headers) { const cell = element('th', '', text); cell.scope = 'col'; row.append(cell); }
      head.append(row);
      const body = element('tbody');
      index += 2;
      while (index < lines.length && lines[index].includes('|') && lines[index].trim()) {
        const row = element('tr');
        const values = cells(lines[index]);
        values.forEach((text) => row.append(element('td', '', text)));
        body.append(row);
        index += 1;
      }
      table.append(head, body);
      const scroll = element('div', 'table-scroll');
      scroll.tabIndex = 0;
      scroll.setAttribute('role', 'region');
      scroll.setAttribute('aria-label', 'Document table');
      scroll.append(table);
      root.append(scroll);
    } else if (/^#{1,6}\s/.test(lines[index])) {
      root.append(element('h4', '', lines[index].replace(/^#{1,6}\s+/, '')));
      index += 1;
    } else {
      const paragraph = [];
      do { paragraph.push(lines[index]); index += 1; }
      while (index < lines.length && lines[index].trim() && !/^#{1,6}\s/.test(lines[index]) && !(lines[index].includes('|') && index + 1 < lines.length && isRule(lines[index + 1])));
      root.append(element('p', '', paragraph.join('\n')));
    }
  }
  return root;
}

function documentSection(title, text, pending = false, fullText = false, key = title) {
  const section = element('section', `document-section${pending ? ' pending-content' : ''}`);
  section.append(element('h3', '', title));
  if (fullText) { section.append(documentContent(text, true)); return section; }
  const formatted = documentContent(text);
  const raw = documentContent(text, true);
  const label = element('label', 'full-text-control');
  const toggle = element('input');
  toggle.type = 'checkbox';
  toggle.dataset.focusKey = `full-text-${key}`;
  toggle.checked = ui.fullText.get(key) || false;
  const update = () => { formatted.hidden = toggle.checked; raw.hidden = !toggle.checked; };
  toggle.addEventListener('change', () => { ui.fullText.set(key, toggle.checked); update(); });
  update();
  label.append(toggle, document.createTextNode('Show full document text'));
  section.append(label, formatted, raw);
  return section;
}

function documentDiff(data) {
  const section = element('section', 'document-section');
  section.append(element('h3', '', 'Diff against proposal base'));
  section.append(element('p', 'muted', 'Removed lines (−) and added lines (+) compare with the proposal’s original document snapshot, not the current accepted content.'));
  if (!data.diff) {
    section.append(element('p', 'muted', data.diff === '' ? 'No changes against the proposal base.' : 'Diff not returned by the service. Full documents remain below.'));
    return section;
  }
  const view = element('pre', 'document-diff');
  view.dataset.focusKey = `document-diff-${ui.path}`;
  view.tabIndex = 0;
  view.setAttribute('role', 'region');
  view.setAttribute('aria-label', 'Unified diff against proposal base');
  let inHunk = false;
  const lines = data.diff.split('\n');
  if (lines[lines.length - 1] === '') lines.pop();
  for (const line of lines) {
    if (line.startsWith('@@')) inHunk = true;
    const kind = line.startsWith('@@') || !inHunk ? 'metadata'
      : line.startsWith('-') ? 'removed' : line.startsWith('+') ? 'added' : 'context';
    view.append(element('span', `diff-line diff-${kind}`, line));
  }
  section.append(view);
  return section;
}

function selectDocument(path) {
  const proposal = currentProposal();
  ui.proposal = proposal && proposalHasDocument(proposal, path) ? proposal.id : ui.proposal === '' ? '' : null;
  ui.path = path;
  renderState();
}

function renderDocumentTabs(paths) {
  const tabs = $('document-tabs');
  const key = JSON.stringify([paths, ui.path]);
  if (tabs.dataset.key === key) return;
  tabs.dataset.key = key;
  preserveFocus(tabs, () => {
    tabs.replaceChildren();
    for (const [index, path] of paths.entries()) {
      const tab = button(documentLabel(path), () => {
        selectDocument(path);
      }, '', `tab-${path}`);
      tab.id = `document-tab-${index}`;
      tab.setAttribute('role', 'tab');
      tab.setAttribute('aria-controls', 'document-panel');
      tab.setAttribute('aria-selected', String(path === ui.path));
      tab.tabIndex = path === ui.path ? 0 : -1;
      tab.addEventListener('keydown', (event) => {
        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
        event.preventDefault();
        const next = event.key === 'Home' ? 0 : event.key === 'End' ? paths.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + paths.length) % paths.length;
        selectDocument(paths[next]);
        $('document-tabs').children[next].focus();
      });
      tabs.append(tab);
    }
  });
  const active = Array.from(tabs.children).find((node) => node.getAttribute('aria-selected') === 'true');
  if (active) $('document-panel').setAttribute('aria-labelledby', active.id);
}

async function renderDocuments() {
  if (!genericMode()) return;
  observeQuestionIntents();
  const paths = documentPaths();
  if (!paths.includes(ui.path)) ui.path = paths[0] || '';
  renderDocumentTabs(paths);
  const proposal = currentProposal();
  const run = proposal && proposalRun(proposal);
  const candidatesKey = ui.state.proposals.filter(proposal => proposalHasDocument(proposal))
    .map(item => {
      const candidateRun = proposalRun(item);
      return [item.id, item.status, item.created, item.attribution?.run, item.attribution?.agent,
        agentName(item.attribution?.agent), candidateRun ? runTitle(candidateRun) : null];
    });
  const key = JSON.stringify([ui.path, ui.state.head, candidatesKey,
    proposal, run, agentName(run?.agent), currentRunProof(run), pendingQuestion(run?.session)]);
  if (key === ui.documentKey) return;
  ui.documentKey = key;
  const version = ++ui.documentVersion;
  const audit = $('document-evidence');
  const selectionKey = JSON.stringify([ui.path, proposal?.id || null]);
  const auditLabel = documentLabel(ui.path);
  const sameSelection = audit.dataset.selectionKey === selectionKey;
  const executionAuthority = item => [item?.status, Boolean(item?.error), runDecisionStatus(item).textContent];
  const authorityKey = JSON.stringify([ui.state.head, proposal?.status, run?.session, executionAuthority(run), currentRunProof(run), pendingQuestion(run?.session)]);
  const retainReview = sameSelection && audit.dataset.authorityKey === authorityKey;
  const panel = $('document-panel');
  const active = document.activeElement;
  const lostKey = sameSelection && (panel.contains(active) || audit.contains(active)) ? active?.dataset.focusKey : null;
  const safeReader = lostKey && /^(affected-|document-diff-|full-text-|retry-document$)/.test(lostKey);
  const focusEpoch = ui.focusEpoch;
  const restoreReader = () => {
    if (!safeReader || ui.focusEpoch !== focusEpoch || document.activeElement !== document.body) return;
    const replacement = [...panel.querySelectorAll('[data-focus-key]'), ...audit.querySelectorAll('[data-focus-key]')].find(node => node.dataset.focusKey === lostKey);
    if (replacement && !replacement.disabled) replacement.focus({ preventScroll: true });
  };
  // Retain harmless metadata refreshes, never obsolete approval gates or evidence.
  audit.dataset.selectionKey = selectionKey;
  audit.dataset.authorityKey = authorityKey;
  if (!retainReview) {
    audit.replaceChildren(element('p', 'muted', `Loading evidence for ${auditLabel}…`));
  }
  const previousScroll = panel.scrollTop;
  panel.setAttribute('aria-busy', 'true');
  if (!retainReview) {
    if (!sameSelection) panel.scrollTop = 0;
    panel.replaceChildren(element('p', 'muted', 'Loading document review…'));
  }
  const showReview = content => {
    // Capture at delivery, since reading and focus can move while the API request is in flight.
    const scroll = retainReview ? panel.scrollTop : previousScroll, auditScroll = $('evidence-panel').scrollTop;
    preserveFocus(panel, () => {
      panel.replaceChildren(content);
      collectEvidence(panel, 'document-evidence');
    });
    if (sameSelection) {
      panel.scrollTop = scroll;
      $('evidence-panel').scrollTop = auditScroll;
    }
    restoreReader();
  };
  try {
    const query = new URLSearchParams({ path: ui.path });
    if (proposal) query.set('proposal', proposal.id);
    const data = await api(`/api/document?${query}`);
    if (version !== ui.documentVersion) return;
    const content = element('div');
    const candidates = ui.state.proposals.filter((candidate) => proposalHasDocument(candidate));
    if (candidates.length > 1) {
      const label = element('label', '', 'Review proposal');
      label.htmlFor = 'proposal-select';
      const select = element('select');
      select.id = 'proposal-select';
      select.dataset.focusKey = 'proposal-select';
      const acceptedOnly = element('option', '', 'Accepted content only');
      acceptedOnly.value = '';
      select.append(acceptedOnly);
      for (const candidate of candidates.slice().reverse()) {
        const candidateRun = proposalRun(candidate);
        const option = element('option', '', `${candidate.status === 'pending' ? 'Awaiting your approval' : candidate.status} · ${candidateRun ? runTitle(candidateRun) : agentName(candidate.attribution?.agent)} · ${recordedTime(candidate.created) || 'Time not recorded'}`);
        option.value = candidate.id;
        select.append(option);
      }
      select.value = proposal?.id || '';
      select.addEventListener('change', () => {
        selectProposal(ui.state.proposals.find(candidate => candidate.id === select.value));
        renderState();
      });
      preserveFocus($('proposal-picker'), () => $('proposal-picker').replaceChildren(label, select));
    } else $('proposal-picker').replaceChildren();
    const acceptedSection = documentSection('Currently accepted content', data.accepted || '', false, false, `accepted-${ui.path}`);
    if (proposal) content.append(proposalBanner(proposal));
    else content.append(element('p', 'muted', ui.proposal === '' ? 'Accepted content only.' : 'No pending change to this document.'));
    const documentEvidence = disclosure('Evidence', `document-${ui.path}-${proposal?.id || 'accepted'}`);
    documentEvidence.append(evidenceRecord('Document revisions', { acceptedCheckpoint: ui.state.head, proposalBase: data.baseCommit, comparison: data.comparison, previousAttempt: data.previousAttempt }));
    if (data.base != null) documentEvidence.append(documentSection('Proposal base · full text', data.base, false, true));
    content.append(documentEvidence);
    if (proposal) content.append(documentDiff(data));
    content.append(acceptedSection);
    if (data.proposed != null) content.append(documentSection('Proposed replacement · full text', data.proposed, true, true, `proposed-${proposal?.id}-${ui.path}`));
    else if (proposal) content.append(element('p', 'muted', 'Proposed document content is not available from the service. Review cannot be assumed complete.'));
    showReview(content);
  } catch (error) {
    if (version !== ui.documentVersion) return;
    ui.documentKey = '';
    const ownsFocus = panel.contains(document.activeElement) || audit.contains(document.activeElement);
    const retry = button('Retry document', () => renderDocuments(), 'small-button', 'retry-document');
    audit.replaceChildren(element('p', 'muted', `Evidence unavailable for ${auditLabel}. Retry document review to load its recorded evidence.`));
    preserveFocus(panel, () => panel.replaceChildren(element('p', 'muted', error.message), retry));
    // Use delivery-time ownership: a reader can move here while the GET is held.
    if (ownsFocus) retry.focus({ preventScroll: true });
    else restoreReader();
    $('poll-status').textContent = 'Document review unavailable. Retry document to load the recorded evidence.';
  } finally {
    if (version === ui.documentVersion) {
      panel.setAttribute('aria-busy', 'false');
      applyBusy();
    }
  }
}

function renderHistory() {
  const history = ui.state.history;
  const counts = $('review-counts');
  counts.replaceChildren();
  const checkpoints = element('span');
  checkpoints.append(element('strong', '', history.length), document.createTextNode(' checkpoints'));
  const pending = element('span');
  pending.append(element('strong', '', ui.state.proposals.filter((proposal) => ['pending', 'stale'].includes(proposal.status)).length), document.createTextNode(' open proposals'));
  counts.append(checkpoints, pending);
  const key = JSON.stringify(history);
  if ($('history-list').dataset.key === key) return;
  $('history-list').dataset.key = key;
  $('history-list').replaceChildren();
  for (const checkpoint of history.slice().reverse()) {
    const item = element('li', '', checkpoint.summary);
    item.append(element('small', '', checkpoint.actor || 'Actor not recorded'));
    const evidence = disclosure('Evidence', `checkpoint-${checkpoint.commit}`);
    evidence.append(evidenceRecord('Checkpoint provenance', { commit: checkpoint.commit, proposal: checkpoint.proposal, actor: checkpoint.actor }));
    item.append(evidence);
    $('history-list').append(item);
  }
  if (!history.length) $('history-list').append(element('li', 'muted', 'No checkpoints have been returned by the service.'));
  collectEvidence($('history-list'), 'history-evidence');
}

function announceTransitions() {
  const messages = [], published = new Set();
  const approvalNotice = proposal => {
    if (proposal?.status !== 'pending') return false;
    const key = `approval-${proposal.id}`;
    if (ui.announcedStates.has(key)) return false;
    ui.announcedStates.set(key, true);
    return true;
  };
  const changed = (key, status) => {
    const previous = ui.announcedStates.get(key);
    ui.announcedStates.set(key, status);
    return ui.announcementsLoaded && previous !== status;
  };
  for (const run of ui.state.runs) {
    if (!changed(`run-${run.id}`, JSON.stringify([run.status, Boolean(run.error)]))) continue;
    if (run.status === 'proposed') {
      const proposal = runProposal(run);
      const completion = run.error ? 'proposal recorded, needs attention'
        : runDecisionStatus(run).textContent === 'Finished' ? 'finished' : 'proposal recorded';
      messages.push(`${runTitle(run)}: ${completion}${approvalNotice(proposal) ? ', awaiting approval' : ''}.`);
      if (proposal) published.add(proposal.id);
    } else if (['failed', 'uncertain'].includes(run.status)) {
      messages.push(`${runTitle(run)}: ${run.status === 'uncertain' ? 'interrupted' : 'failed'}.`);
    }
  }
  for (const proposal of ui.state.proposals) {
    if (changed(`proposal-${proposal.id}`, proposal.status) && !published.has(proposal.id) && approvalNotice(proposal)) {
      messages.push(`${proposalName(proposal)}: awaiting approval.`);
    }
  }
  for (const entry of ui.state.conversation || []) {
    if (entry.kind !== 'question' || !changed(`question-${entry.id}`, entry.status)) continue;
    if (['answered', 'failed', 'uncertain'].includes(entry.status)) {
      messages.push(`Read-only question ${entry.status === 'uncertain' ? 'interrupted' : entry.status} (${agentName(entry.agent)}).`);
    }
  }
  ui.announcementsLoaded = true;
  // A separate concise region leaves operation feedback, drafts and conversation untouched.
  if (messages.length) $('poll-status').textContent = messages.join(' ');
}

function genericMode() { return Boolean(ui.state?.definition); }
function evaluationLabel(subject) {
  const evidence = subject?.suite?.release?.evaluation;
  if (evidence?.status === 'evaluation consistent; unsigned' && evidence.unsigned === true && evidence.cases > 0 && evidence.runs >= evidence.cases && evidence.completedAt && evidence.testedExecution?.runtime) return 'evaluation consistent; unsigned';
  return `evaluation: not verified${evidence?.reason ? ` (${evidence.reason})` : ''}`;
}
function suiteEvidence(subject) {
  const suite = subject?.suite;
  const identity = suite?.release ? { ...suite, release: { ...suite.release, runtimeProfile: suite.release.runtimeProfile || 'unprofiled/native' } } : suite;
  const record = evidenceRecord(suite?.release ? evaluationLabel(subject) : 'Recorded suite identity, not evaluation evidence', identity || 'Suite identity not recorded');
  if (subject?.suite?.release) record.append(element('p', 'muted', 'Release evaluation consistency is unsigned. Tested model/runtime and associated build images are not qualification of this Canvas execution. The Northwind release fixture used mocked native transport.'));
  return record;
}

function renderDefinition() {
  const definition = ui.state.definition;
  $('room-brand').textContent = `/ ${definition.title || definition.id}`;
  $('generic-run-toggle').hidden = false;
  $('generic-input-toggle').hidden = !ui.state.triggers.some(item => item.type === 'file-added');
  $('navigation').setAttribute('aria-label', 'Canvas navigation');
  $('fictional-note').textContent = 'Fictional project data. Personas record attribution, not authorization.';
  $('suite-status').hidden = false;
  const manual = ui.state.agents.filter(agent => ui.state.triggers.some(trigger => trigger.type === 'manual' && trigger.agent === agent.id));
  if (!ui.state.agents.some(agent => agent.id === ui.agent)) ui.agent = manual[0]?.id || ui.state.agents[0]?.id;
  if (!documentPaths().includes(ui.path)) ui.path = definition.defaultDocument || documentPaths()[0] || '';
  const select = $('generic-agent');
  const key = JSON.stringify(manual);
  if (select.dataset.key !== key) {
    const prior = select.value;
    select.replaceChildren();
    for (const agent of manual) { const option = element('option', '', agent.name); option.value = agent.id; select.append(option); }
    select.value = manual.some(agent => agent.id === prior) ? prior : manual[0]?.id || '';
    select.dataset.key = key;
  }
  $('generic-run-submit').dataset.unavailable = String(!manual.length);
  $('generic-input-submit').dataset.unavailable = String($('generic-input-toggle').hidden);
  const historical = Boolean(ui.run || currentProposal());
  const recorded = selectedRun();
  const source = historical ? recorded : ui.state.agents.find(agent => agent.id === ui.agent);
  const label = historical && !recorded?.suite ? 'evaluation: not verified (historical provenance unavailable)' : evaluationLabel(source);
  $('suite-status').textContent = `Local-only · ${label}`;
}

// Confirmation is ephemeral and belongs to one inspected native snapshot, never local storage.
const announcementWarning = 'A newer version exists; approving publishes this older version';
const announcementReceipt = 'SIMULATED_ANNOUNCEMENT: recorded locally; nothing sent or published';
function revokeAnnouncement(clear = false) {
  $('announcement-confirm').checked = false;
  if (clear) {
    ui.publication.epoch += 1;
    ui.publication.view = null;
    ui.publication.loading = false;
    ui.publication.background = false;
    ui.publication.error = '';
    ui.publication.actionError = '';
  }
}
function announcementOwner() { return JSON.stringify([ui.publication.epoch, ui.publication.id, ui.state?.role]); }
function announcementEligible(action) {
  return action ? (ui.state?.proposals || []).filter(p => p.status === 'accepted'
    && typeof p.acceptedCommit === 'string' && /^[a-f0-9]{40}$/.test(p.acceptedCommit)
    && p.changes && Object.hasOwn(p.changes, action.path) && typeof p.changes[action.path] === 'string') : [];
}
function announcementOptions(id, items, placeholder = '') {
  const select = $(id), key = JSON.stringify([items, placeholder]);
  if (select.dataset.key === key) return;
  const previous = select.value;
  select.replaceChildren();
  if (placeholder) { const option = element('option', '', placeholder); option.value = ''; select.append(option); }
  for (const item of items) { const option = element('option', '', item.label); option.value = item.id; select.append(option); }
  select.value = items.some(item => item.id === previous) ? previous : placeholder ? '' : items[0]?.id || '';
  select.dataset.key = key;
}
function validAnnouncementView(v, id) {
  const text = value => typeof value === 'string' && value.length > 0 && value.length <= 1024;
  const revision = value => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value);
  const digest = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
  const s = v?.snapshot, c = v?.confirmation;
  if (!v || v.id !== id || !s || !c || s.operation !== id
    || !['pending-approval', 'approved', 'denied', 'recoverable', 'simulated', 'failed', 'uncertain', 'incomplete', 'unavailable', 'abandoned'].includes(v.status)
    || typeof v.concluded !== 'boolean' || typeof v.newer !== 'boolean' || typeof v.session !== 'string'
    || !['operation', 'action', 'document', 'path', 'proposal'].every(key => text(s[key]))
    || !digest(s.definition) || !revision(s.revision) || !digest(s.digest)
    || typeof s.content !== 'string' || new TextEncoder().encode(s.content).length > 65536
    || !revision(v.currentRevision) || c.confirmed !== false || c.session !== v.session
    || typeof c.executionID !== 'string' || typeof c.toolCallID !== 'string'
    || !Number.isSafeInteger(c.head) || c.head < 0 || !Number.isSafeInteger(c.requestSeq) || c.requestSeq < 0
    || c.revision !== s.revision || c.digest !== s.digest || c.currentRevision !== v.currentRevision || c.newer !== v.newer) return false;
  if ((c.unavailable !== undefined && typeof c.unavailable !== 'boolean') || (c.unavailable === true) !== (v.status === 'unavailable')) return false;
  if (v.status === 'unavailable' && (!text(v.session) || v.concluded || !((c.head === 0 && c.executionID === '' && c.toolCallID === '' && c.requestSeq === 0)
    || (c.head > 0 && text(c.executionID) && text(c.toolCallID) && c.requestSeq > 0)))) return false;
  if (['pending-approval', 'approved', 'denied'].includes(v.status)
    && (!text(v.session) || !text(c.executionID) || !text(c.toolCallID) || c.requestSeq < 1)) return false;
  if (v.status === 'incomplete' && (!text(v.session) || !text(c.executionID) || c.requestSeq !== 0)) return false;
  if (v.receipt && (v.status !== 'simulated' || !v.concluded || v.receipt.kind !== 'SIMULATED_ANNOUNCEMENT'
    || JSON.stringify(v.receipt.snapshot) !== JSON.stringify(s))) return false;
  return v.status !== 'simulated' || Boolean(v.receipt);
}
function renderAnnouncementControls() {
  const p = ui.publication, v = p.view;
  const ready = Boolean(v && validAnnouncementView(v, p.id));
  // Background reads must lock activation without native disabling blurring keyboard focus.
  const inspecting = p.loading && !p.background;
  const locked = ui.busy || p.busy || inspecting || !ready || !$('announcement-confirm').checked;
  const decision = ready && v.status === 'pending-approval' && !v.concluded;
  const continuation = ready && ['approved', 'denied'].includes(v.status) && !v.concluded;
  const recovery = ready && v.status === 'recoverable' && Boolean(v.session) && !v.concluded;
  const abandon = ready && !v.concluded && ((v.status === 'uncertain' && !v.session)
    || (['incomplete', 'recoverable', 'unavailable'].includes(v.status) && Boolean(v.session)));
  for (const [id, available] of [['approve', decision], ['deny', decision], ['continue', continuation], ['recover', recovery], ['abandon', abandon]]) {
    $('announcement-' + id).hidden = !available;
    $('announcement-' + id).disabled = locked || !available;
    $('announcement-' + id).setAttribute('aria-disabled', String(locked || p.loading || !available));
  }
  $('announcement-confirm').disabled = ui.busy || p.busy || inspecting || !ready || !(decision || continuation || recovery || abandon);
  $('announcement-confirm').setAttribute('aria-disabled', String($('announcement-confirm').disabled || p.loading));
  $('announcement-confirm').parentNode.hidden = !(decision || continuation || recovery || abandon);
  $('announcement-consequence').textContent = decision ? 'Record a decision only. This does not run the simulation.'
    : continuation ? v.status === 'denied' ? 'Continue records the denied outcome. No announcement is simulated.' : 'Continue runs only this pinned local simulation. Nothing is sent or published.'
    : recovery ? 'Recover the known native session, or abandon only the local record. Inspect and confirm again afterward.'
    : abandon ? 'Keep the local audit record. Native work may already have progressed; local Abandon does not cancel or undo it.' : '';
}
function renderAnnouncement() {
  const p = ui.publication;
  const actions = genericMode() && Array.isArray(ui.state.actions) ? ui.state.actions.filter(a => a && typeof a.id === 'string' && typeof a.path === 'string') : [];
  $('announcement-panel').hidden = !actions.length;
  if (!actions.length) { if (p.id || p.view) { revokeAnnouncement(true); p.id = ''; } return; }
  if (p.role !== ui.state.role) { revokeAnnouncement(true); p.role = ui.state.role; }
  if (p.head !== ui.state.head) { revokeAnnouncement(); p.head = ui.state.head; }
  announcementOptions('announcement-action', actions.map(a => ({ id: a.id, label: a.name || a.id })));
  const action = actions.find(a => a.id === $('announcement-action').value);
  const proposals = announcementEligible(action);
  announcementOptions('announcement-proposal', proposals.map(item => ({ id: item.id, label: item.id })));
  const summaries = Array.isArray(ui.state.announcements) ? ui.state.announcements : [];
  announcementOptions('announcement-operation', summaries.map(item => ({ id: item.id, label: item.id + ' · ' + item.stage })), 'Choose an operation…');
  if (p.id && !summaries.some(item => item.id === p.id)) { revokeAnnouncement(true); p.id = ''; }
  $('announcement-operation').value = p.id;
  $('announcement-request').disabled = ui.busy || p.busy || !action || !proposals.length;
  $('announcement-refresh').hidden = !p.id;
  $('announcement-refresh').disabled = (p.loading && !p.background) || p.busy;
  $('announcement-refresh').setAttribute('aria-disabled', String(p.loading || p.busy));
  $('announcement-inspection').hidden = !p.view;
  const status = p.actionError || p.error || (p.loading ? 'Inspecting native evidence…' : p.busy ? 'Recording local action…' : p.view ? 'Native status: ' + p.view.status : proposals.length ? 'Choose an operation to inspect its exact snapshot.' : 'Accept a proposal changing this action’s document before requesting approval.');
  if ($('announcement-status').textContent !== status) $('announcement-status').textContent = status;
  $('announcement-status').className = p.actionError || p.error ? 'error' : 'muted';
  $('announcement-panel').setAttribute('aria-busy', String(p.loading || p.busy));
  if (p.view) {
    const v = p.view, s = v.snapshot;
    const key = JSON.stringify(v);
    if ($('announcement-identity').dataset.key !== key) {
      $('announcement-identity').replaceChildren();
      for (const [label, value] of [['Operation', s.operation], ['Definition', s.definition], ['Action', s.action], ['Document', s.document], ['Path', s.path], ['Proposal', s.proposal], ['Revision', s.revision], ['SHA-256', s.digest], ['Current revision', v.currentRevision], ['Native session', v.session || 'Not durably recorded']]) {
        $('announcement-identity').append(element('dt', '', label), element('dd', '', value));
      }
      $('announcement-identity').dataset.key = key;
      $('announcement-content').textContent = s.content;
    }
    $('announcement-warning').hidden = !v.newer;
    $('announcement-warning').textContent = v.newer ? announcementWarning : '';
    $('announcement-receipt').hidden = !v.receipt;
    $('announcement-receipt').textContent = v.receipt ? announcementReceipt : '';
    $('announcement-receipt-detail').hidden = !v.receipt;
    $('announcement-receipt-content').textContent = v.receipt ? JSON.stringify(v.receipt, null, 2) : '';
  }
  renderAnnouncementControls();
}
async function inspectAnnouncement(background = false) {
  const p = ui.publication;
  if (!p.id || p.loading || p.busy || !genericMode()) return;
  const owner = announcementOwner(), id = p.id, read = ++p.read;
  p.loading = true; p.background = background; p.error = ''; renderAnnouncement();
  try {
    const view = await api('/api/announcement?id=' + encodeURIComponent(id));
    if (owner !== announcementOwner() || read !== p.read) return;
    if (!validAnnouncementView(view, id)) throw new Error('Incomplete or mismatched native evidence. Inspect again before acting.');
    if (JSON.stringify(p.view) !== JSON.stringify(view)) revokeAnnouncement();
    p.view = view;
  } catch (error) {
    if (owner !== announcementOwner() || read !== p.read) return;
    revokeAnnouncement(); p.view = null; p.error = error.message;
  } finally {
    if (owner === announcementOwner() && read === p.read) { p.loading = false; p.background = false; renderAnnouncement(); }
  }
}
async function announcementCommand(kind, approved) {
  const p = ui.publication;
  if (ui.busy || p.busy || p.loading || !genericMode()) return;
  let body;
  if (kind === 'request') {
    const action = (ui.state.actions || []).find(a => a.id === $('announcement-action').value);
    const proposal = announcementEligible(action).find(item => item.id === $('announcement-proposal').value);
    if (!action || !proposal) return;
    body = { role: ui.state.role, action: action.id, proposal: proposal.id };
  } else {
    const v = p.view;
    if (!validAnnouncementView(v, p.id) || !$('announcement-confirm').checked) return;
    const allowed = kind === 'decision' ? typeof approved === 'boolean' && v.status === 'pending-approval' && !v.concluded
      : kind === 'continue' ? ['approved', 'denied'].includes(v.status) && !v.concluded
      : kind === 'recover' ? v.status === 'recoverable' && Boolean(v.session) && !v.concluded
      : kind === 'abandon' && !v.concluded && ((v.status === 'uncertain' && !v.session)
        || (['incomplete', 'recoverable', 'unavailable'].includes(v.status) && Boolean(v.session)));
    if (!allowed) return;
    body = { id: p.id, role: ui.state.role, confirmation: { ...v.confirmation, confirmed: true } };
    if (kind === 'decision') body.approved = approved;
  }
  const owner = announcementOwner(), focusEpoch = ui.focusEpoch;
  revokeAnnouncement(); p.busy = true; ui.busy = true; p.error = ''; p.actionError = '';
  applyBusy(); renderAnnouncement();
  try {
    const result = await api('/api/announcement/' + kind, body);
    if (owner === announcementOwner() && kind === 'request') {
      if (typeof result?.id !== 'string' || !result.id) throw new Error('The request returned no operation. Inspect the operation list before retrying.');
      revokeAnnouncement(true); p.id = result.id;
    }
  } catch (error) {
    if (owner === announcementOwner()) p.actionError = error.message;
  } finally {
    ui.stateGeneration += 1;
    await refreshState();
    p.busy = false; ui.busy = false;
    applyBusy(); renderAnnouncement();
    // Even a refused or uncertain action consumes consent and requires fresh inspection.
    await inspectAnnouncement();
    if (owner === announcementOwner() && focusEpoch === ui.focusEpoch) {
      const target = $('announcement-confirm').disabled ? $('announcement-status') : $('announcement-confirm');
      target.focus({ preventScroll: true });
    }
  }
}

function renderState() {
  if (!ui.state) return;
  if (!genericMode()) {
    // Retired state cannot retain ownership of in-flight review or confirmation UI.
    ui.documentVersion += 1;
    ui.documentKey = ''; ui.navigationKey = ''; ui.conversationKey = '';
    ui.run = null; ui.proposal = null; ui.agent = null; ui.path = '';
    ui.resumePanels.clear();
    revokeAnnouncement(true);
    renderAnnouncement();
    for (const id of ['document-panel', 'document-evidence', 'conversation-content', 'conversation-evidence', 'history-list', 'history-evidence', 'review-counts', 'document-tabs', 'proposal-picker', 'request-strip', 'sessions', 'agents', 'flows', 'people']) {
      $(id).replaceChildren();
      delete $(id).dataset.key;
    }
    $('document-panel').setAttribute('aria-busy', 'false');
    for (const kind of ['run', 'input']) {
      $(`generic-${kind}-panel`).hidden = true;
      $(`generic-${kind}-toggle`).hidden = true;
      $(`generic-${kind}-toggle`).setAttribute('aria-expanded', 'false');
    }
    $('suite-status').hidden = true;
    $('room-brand').textContent = '/ Canvas';
    $('room-title').textContent = 'Document review';
    const notice = 'Unsupported state. Load a declared definition to work in Canvas.';
    if ($('notice').hidden || $('notice').textContent !== notice) notify(notice, false, { kind: 'unsupported' });
    applyBusy();
    return;
  }
  if (ui.noticeContext?.kind === 'unsupported') { $('notice').hidden = true; ui.noticeContext = null; }
  renderDefinition();
  renderAnnouncement();
  announceTransitions();
  clearObsoleteNotice();
  renderLayout();
  const role = $('role');
  const people = ui.state.people.filter(person => layoutRoles.includes(person.id));
  const selectedRole = ui.busy && role.value ? role.value : ui.state.role;
  const fallbackRoles = [...new Set([ui.state.role, selectedRole])].filter(id => !people.some(person => person.id === id));
  const peopleKey = JSON.stringify([people, fallbackRoles, selectedRole]);
  if (role.dataset.key !== peopleKey) {
    role.replaceChildren(...people.map(person => {
      const option = element('option', '', `${person.name} · ${person.title || person.id}`);
      option.value = person.id;
      return option;
    }), ...fallbackRoles.map(id => {
      const option = element('option', '', `Attribution ID · ${id}`);
      option.value = id;
      return option;
    }));
    role.value = selectedRole;
    role.dataset.key = peopleKey;
  }
  $('room-title').textContent = documentLabel(ui.path);
  renderRequests();
  renderModelGuidance();
  renderNavigation();
  renderConversation();
  renderHistory();
  renderDocuments();
  applyBusy();
}

// role="status" re-announces on every write, so only touch the text when it changes.
function showConnection(text) {
  const node = $('connection');
  if (node.textContent !== text) node.textContent = text;
  node.hidden = false;
}

// Native required/minlength accept whitespace, so check the trimmed value too.
function trimmedValue(field, minLength, message) {
  const value = field.value.trim();
  const valid = value.length >= minLength;
  field.setCustomValidity(valid ? '' : message);
  if (!valid) field.reportValidity();
  return valid ? value : null;
}

async function refreshState() {
  const generation = ui.stateGeneration;
  if (ui.refresh?.generation === generation) return ui.refresh.promise;
  // A mutation bumps the generation: let the older request finish, then fetch again.
  const previous = ui.refresh?.promise;
  const promise = (async () => {
    if (previous) await previous;
    let state;
    try { state = await api('/api/state'); }
    catch (error) {
      if (generation === ui.stateGeneration) showConnection(`Local service unavailable: ${error.message} Retrying automatically.`);
      return;
    }
    // A newer request owns the view; this response may predate a mutation.
    if (generation !== ui.stateGeneration) return;
    try {
      for (const key of ['agents', 'people', 'documents', 'runs', 'proposals', 'history', 'triggers', 'errors', 'conversation']) {
        if (!Array.isArray(state[key])) state[key] = [];
      }
      const initial = !ui.state;
      ui.state = state;
      if (selectedRun()) ui.agent = selectedRun().agent;
      if (initial && state.runs.length) {
        const latest = orderedRuns(state.runs).slice(-1)[0];
        ui.run = latest.id;
        ui.agent = latest.agent;
      }
      if (!ui.busy) $('role').value = state.role;
      if (state.errors.length) showConnection(`Local service notices: ${state.errors.join(' · ')}`);
      else $('connection').hidden = true;
      renderState();
      if (!ui.publication.busy) inspectAnnouncement(true);
    } catch (error) {
      // The service answered; this is a page bug, not an outage.
      console.error(error);
      showConnection(`The page could not show the latest state: ${error.message} Reload to try again.`);
    }
  })();
  ui.refresh = { generation, promise };
  promise.finally(() => { if (ui.refresh?.promise === promise) ui.refresh = null; });
  return promise;
}

$('generic-run-form').addEventListener('submit', (event) => {
  event.preventDefault();
  if (!genericMode()) return;
  const request = trimmedValue($('generic-request'), 1, 'Enter a request for the declared agent.');
  const agent = $('generic-agent').value;
  if (request === null || !ui.state.agents.some(item => item.id === agent) || !ui.state.triggers.some(trigger => trigger.type === 'manual' && trigger.agent === agent)) return;
  return mutate('/api/run', { agent, request, role: ui.state.role }, data => {
    ui.run = data.run; ui.agent = agent; ui.proposal = null;
    if ($('generic-request').value.trim() === request) $('generic-request').value = '';
    notify('Agent run requested. Accepted documents stay unchanged until review.');
  });
});
$('generic-input-form').addEventListener('submit', event => {
  event.preventDefault();
  if (!genericMode()) return;
  const path = trimmedValue($('generic-input-path'), 1, 'Enter a declared input path.');
  const field = $('generic-input-content');
  const content = field.value;
  if (new TextEncoder().encode(content).length > 64 * 1024) {
    field.setCustomValidity('File content must be at most 64 KiB of UTF-8 text.');
    field.reportValidity();
    return;
  }
  field.setCustomValidity('');
  if (path === null || !content.trim()) return;
  return mutate('/api/input', { path, content, role: ui.state.role }, () => {
    if ($('generic-input-content').value === content) $('generic-input-content').value = '';
    notify('Source file saved. A declared trigger may start a recorded run.');
  });
});

$('room-form').addEventListener('submit', (event) => {
  event.preventDefault();
  submitRoom('comment');
});
$('room-ask').addEventListener('click', () => submitRoom('question'));

$('role').addEventListener('change', () => {
  const role = $('role').value;
  if (!ui.state) return;
  revokeAnnouncement(true);
  renderAnnouncement();
  mutate('/api/role', { role }, () => {
    if ($('notice').hidden || !$('notice').textContent || (ui.noticeContext && ui.noticeContext.kind !== 'question') || $('notice').textContent.startsWith('Acting as ')) {
      notify(`Acting as ${role}. Demo control only, not sign-in.`);
    }
  }).finally(() => { $('role').value = ui.state?.role || 'GM'; });
});

for (const id of ['room-request', 'generic-request', 'generic-input-path', 'generic-input-content']) $(id).addEventListener('input', () => $(id).setCustomValidity(''));

async function poll() {
  await refreshState();
  window.setTimeout(poll, 1000);
}

for (const [name, id] of [['navigation', 'navigation'], ['evidence', 'evidence-panel']]) {
  $(`${name}-toggle`).addEventListener('click', () => setPanel(name, $(id).hidden));
  $(`${name}-close`).addEventListener('click', () => { setPanel(name, false); revealPanelToggle(name); });
  $(id).addEventListener('keydown', event => {
    if (event.key === 'Escape') { event.preventDefault(); setPanel(name, false); revealPanelToggle(name); }
  });
}
function revealPanelToggle(name) {
  const target = $(`${name}-toggle`);
  target.focus({ preventScroll: true });
  const rect = target.getBoundingClientRect();
  if (rect.top < 0 || rect.bottom > window.innerHeight) target.scrollIntoView({ block: 'nearest' });
}

$('room-form').addEventListener('focusin', () => { $('room-form').dataset.expanded = 'true'; });
$('room-form').addEventListener('focusout', event => {
  if (!$('room-form').contains(event.relatedTarget)) $('room-form').dataset.expanded = 'false';
});
$('new-menu').addEventListener('keydown', event => {
  if (event.key === 'Escape') { event.preventDefault(); $('new-menu').open = false; $('new-menu').children[0].focus(); }
});
$('room-form').addEventListener('keydown', event => {
  if (event.key === 'Escape') {
    event.preventDefault();
    $('room-request').focus({ preventScroll: true });
    $('room-form').dataset.expanded = 'false';
  }
});
function setGenericForm(kind, open) {
  if (!genericMode()) return;
  $(`generic-${kind}-panel`).hidden = !open;
  $(`generic-${kind}-toggle`).setAttribute('aria-expanded', String(open));
  $('new-menu').open = false;
  if (open) $(kind === 'run' ? 'generic-agent' : 'generic-input-path').focus({ preventScroll: true });
  else $('new-menu').children[0]?.focus();
}
for (const kind of ['run', 'input']) {
  $(`generic-${kind}-toggle`).addEventListener('click', () => setGenericForm(kind, true));
  $(`generic-${kind}-close`).addEventListener('click', () => setGenericForm(kind, false));
}

$('announcement-operation').addEventListener('change', () => {
  revokeAnnouncement(true);
  ui.publication.id = $('announcement-operation').value;
  renderAnnouncement();
  inspectAnnouncement();
});
$('announcement-action').addEventListener('change', () => { revokeAnnouncement(); renderAnnouncement(); });
$('announcement-proposal').addEventListener('change', () => { revokeAnnouncement(); renderAnnouncement(); });
$('announcement-confirm').addEventListener('click', event => { if (ui.publication.loading) event.preventDefault(); });
$('announcement-confirm').addEventListener('change', renderAnnouncementControls);
$('announcement-refresh').addEventListener('click', () => { if (ui.publication.loading || ui.publication.busy) return; revokeAnnouncement(); ui.publication.actionError = ''; inspectAnnouncement(); });
$('announcement-request').addEventListener('click', () => announcementCommand('request'));
$('announcement-approve').addEventListener('click', () => announcementCommand('decision', true));
$('announcement-deny').addEventListener('click', () => announcementCommand('decision', false));
for (const kind of ['continue', 'recover', 'abandon']) $('announcement-' + kind).addEventListener('click', () => announcementCommand(kind));

// Polling never resets disclosures or drafts.
renderLayout();
applyBusy();
poll();
