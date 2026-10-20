const $ = id => document.getElementById(id);
const token = new URLSearchParams(location.hash.slice(1)).get('token');
history.replaceState(null, '', location.pathname);
let state, demo, selected = '', view, documentKey = '', actionKey = '', pendingRun = '', busy = false, refreshing = false, online = false, refreshPromise;
let selectionGeneration = 0, inspected = '', reopen = false, outcomeKey = '', actionError = '';
const inspectionKey = () => `${selectionGeneration}:${selected}`;
function setText(id, text) { if ($(id).textContent !== text) $(id).textContent = text; }
function announce(message) { setText('status', message); }
const reviewRole = () => demo?.reviewerRole || 'GM';
const remoteSessions = () => ['authenticated-sessions', 'trusted-loopback-sessions'].includes(demo?.mode);

async function api(path, body) {
  try {
    const response = await fetch(path, { method: body ? 'POST' : 'GET', headers: { 'x-canvas-review-token': token || '', 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    const result = await response.json();
    if (!response.ok) throw Object.assign(new Error(result.error || 'Review action unavailable'), { code: result.code, recovery: result.recovery, uncertain: !!body && response.status >= 500 });
    return result;
  } catch (error) {
    if (body && error.uncertain === undefined) error.uncertain = true;
    throw error;
  }
}
function proposal() { return state?.proposals?.find(item => item.id === selected); }
function run() { return state?.runs?.find(item => item.proposal === selected); }
function clearConfirmations() { $('document-confirm').checked = false; $('action-confirm').checked = false; }
function controls() {
  const p = proposal(), enabled = online && !reopen && !busy && !pendingRun;
  const ready = enabled && inspected === inspectionKey();
  $('draft').disabled = !enabled;
  $('refresh').disabled = busy || refreshing;
  $('proposal').disabled = busy || !online || reopen;
  $('document-confirm').disabled = !enabled || !p;
  $('accept').disabled = !ready || p?.status !== 'pending' || !$('document-confirm').checked;
  $('reject').disabled = !ready || !['pending', 'stale'].includes(p?.status) || !$('document-confirm').checked;
  $('verify').disabled = !ready || !run() || remoteSessions();
  $('request').disabled = !ready || p?.status !== 'accepted' || !Object.hasOwn(p?.changes || {}, 'announcement.md') || !!view;
  $('action-confirm').disabled = !enabled || !view || view.concluded;
  const decision = ready && view?.status === 'pending-approval' && $('action-confirm').checked;
  $('approve').disabled = !decision; $('deny').disabled = !decision;
  $('continue').disabled = !ready || !view || view.concluded || !['approved', 'denied'].includes(view.status) || !$('action-confirm').checked;
  $('verify-action').disabled = !ready || view?.status !== 'simulated' || !view?.concluded || remoteSessions();
  setText('replay-help', remoteSessions() ? 'Remote replay diagnostics are unavailable. No 0/0 claim is made.' : !online || reopen ? 'Reconnect before verifying replay.' : view?.status === 'simulated' && view?.concluded ? 'Verify recorded integrity, not approval or evaluation quality.' : 'Action replay verification is available after a completed simulation, not a denial.');
}
function proofLabel(proof) {
  return proof?.chainValid && proof.reconstructed && proof.outputsMatch && proof.liveUnchanged && proof.modelCalls === 0 && proof.executorCalls === 0 && Array.isArray(proof.executionIDs) && proof.executionIDs.length
    ? `Replay verified: model 0 / executor 0. Completed executions covered: ${proof.executionIDs.length}.`
    : 'Replay 0/0 not verified. Coverage or call evidence is unavailable.';
}
function renderReleaseEvidence() {
  const historical = run();
  const agents = state.agents || [];
  const records = selected ? (historical?.suite ? [historical] : []) : agents;
  const releases = records.filter(record => record.suite?.release);
  const unavailable = Boolean(selected && !releases.length && agents.some(record => record.suite?.release));
  $('release-evidence').hidden = !releases.length && !unavailable;
  const key = JSON.stringify([unavailable, releases.map(record => [record.id, record.agent, record.model, record.suite])]);
  if ($('suite-evidence').dataset.key === key) return;
  $('suite-evidence').replaceChildren();
  if (unavailable) {
    const status = document.createElement('p'); status.textContent = 'not verified (historical provenance unavailable). No checked released identity is recorded for this proposal.';
    $('suite-evidence').append(status);
  }
  for (const record of releases) {
    const suite = record.suite, release = suite.release, evaluation = release.evaluation;
    const title = document.createElement('h3'); title.textContent = record.agent || record.id;
    const status = document.createElement('p');
    const consistent = evaluation?.status === 'evaluation consistent; unsigned' && evaluation.unsigned === true && evaluation.cases > 0 && evaluation.runs >= evaluation.cases && evaluation.completedAt && evaluation.testedExecution?.runtime;
    status.textContent = consistent ? 'evaluation consistent; unsigned' : `not verified${evaluation?.reason ? ` (${evaluation.reason})` : ''}`;
    const caution = document.createElement('p'); caution.textContent = 'Released evaluation is separate from Canvas execution qualification. The Northwind fixture used mocked native transport; no model or image execution is qualified.';
    const bytes = document.createElement('pre'); bytes.tabIndex = 0; bytes.setAttribute('aria-label', 'Released suite version and evaluation details'); bytes.textContent = JSON.stringify({ authoredModel: release.authoredModel, canvasModel: record.model || 'not recorded', runtimeProfile: release.runtimeProfile || 'unprofiled/native', suite }, null, 2);
    $('suite-evidence').append(title, status, caution, bytes);
  }
  $('suite-evidence').dataset.key = key;
}
function stage() {
  const p = proposal(), completed = p && !['pending', 'stale'].includes(p.status);
  $('empty').hidden = !!p; $('review').hidden = !p;
  $('draft').classList.toggle('secondary', !!p);
  $('document-stage').classList.toggle('current', !!p && !completed);
  $('document-decision').hidden = !p || completed;
  $('document-help').hidden = !!completed;
  $('action-stage').hidden = p?.status !== 'accepted';
  $('action-stage').classList.toggle('current', p?.status === 'accepted');
  $('request').hidden = !!view;
  const deciding = view?.status === 'pending-approval', continuing = view && !view.concluded && ['approved', 'denied'].includes(view.status);
  $('action-decision').hidden = !deciding && !continuing;
  $('approve').hidden = !deciding; $('deny').hidden = !deciding; $('continue').hidden = !continuing;
  $('continue').textContent = view?.status === 'denied' ? 'Conclude without simulation' : 'Continue simulation';
  $('action-confirm-label').textContent = continuing ? 'I rechecked this exact accepted snapshot and recorded decision.' : 'I reviewed this exact accepted snapshot and request.';
  $('action-help').textContent = deciding ? 'Confirm the snapshot before recording a decision. Recording approval does not run the simulation.' : continuing ? 'Fresh confirmation is required because recording a decision is separate from continuing. Recheck the snapshot, then confirm again.' : view?.concluded ? 'This action has concluded. Nothing was sent or published.' : 'Request action approval to inspect the accepted announcement snapshot. No decision or simulation happens automatically.';
}
function showReviewer(id, reviewer, selectedRole = false) {
  const present = reviewer?.issuer && reviewer?.subject && reviewer?.role;
  $(id).hidden = !present;
  $(id).textContent = present ? `Reviewed by ${reviewer.subject} (${reviewer.issuer}); role: ${reviewer.role}${selectedRole ? ' (selected attribution, not a sessions role grant)' : ''}. Identity: engine-verified, not sessions-attested.` : '';
}
function actionSummary() {
  if (!view) return proposal()?.status === 'accepted' ? 'Documents accepted. Request action approval to review the separate simulated action.' : 'Accept documents before requesting action approval.';
  if (view.status === 'approved') return 'Recorded; not executed. Confirm again, then Continue simulation.';
  if (view.status === 'denied') return view.concluded ? 'Denial concluded. No simulation ran. Nothing sent or published.' : 'Denial recorded; no simulation will run. Confirm again, then conclude without simulation.';
  if (view.status === 'simulated') return 'SIMULATED_ANNOUNCEMENT: recorded locally. Nothing sent or published.';
  if (view.status === 'pending-approval') return 'Action decision pending (pending-approval). Review the exact snapshot, then record approval or denial.';
  return `Action: ${view.status}. Inspect the native evidence before proceeding.`;
}
async function render(generation) {
  const proposals = state.proposals || [];
  if (pendingRun) {
    const candidate = state.runs.find(item => item.id === pendingRun);
    if (candidate?.proposal) { selected = candidate.proposal; pendingRun = ''; }
    else if (candidate && ['failed', 'uncertain', 'refused'].includes(candidate.status)) { pendingRun = ''; actionError = 'Draft did not complete. Inspect the engine browser before trying again.'; }
  }
  if (!proposals.some(item => item.id === selected)) selected = proposals.at(-1)?.id || '';
  const optionKey = JSON.stringify(proposals.map(p => [p.id, p.status]));
  if ($('proposal').dataset.key !== optionKey) {
    $('proposal').replaceChildren();
    if (!proposals.length) $('proposal').add(new Option('No proposals yet', ''));
    proposals.forEach((p, i) => $('proposal').add(new Option(`Draft ${i + 1}: ${p.status}`, p.id)));
    $('proposal').dataset.key = optionKey;
  }
  $('proposal').value = selected;
  renderReleaseEvidence();
  const authenticated = demo.mode === 'authenticated-sessions';
  if (authenticated) {
    setText('identity', 'Identity is verified per action. The engine authorizes document-review roles; sessions authorizes tool approval.');
    setText('engine-description', 'Authenticated sessions connection. Document review and action approval remain separate.');
  } else if (remoteSessions()) {
    setText('identity', 'Identity not exercised. Unauthenticated trusted-loopback sessions.');
    setText('engine-description', 'Remote document producer. Document review does not grant action approval.');
  } else if (['hosted-model', 'offline-model'].includes(demo.mode)) {
    setText('identity', 'Identity not exercised. Roles are local attribution, not verified identity.');
    setText('engine-description', 'Real-model document proposals. Review documents before any separate action decision.');
  }
  const p = proposal(), key = JSON.stringify([demo.boot, state.head, p]);
  stage();
  if (key !== documentKey) {
    documentKey = ''; inspected = ''; clearConfirmations(); controls(); $('documents').replaceChildren();
    $('completed-documents').open = ['pending', 'stale'].includes(p?.status);
    $('documents-summary').textContent = p?.status === 'accepted' ? 'Documents accepted. Reopen the complete review' : p?.status === 'rejected' ? 'Documents rejected. Reopen the complete review' : 'Review all changed documents';
    $('scope').textContent = p ? `Whole-proposal scope: ${Object.keys(p.changes).sort().join(', ')}. All changes are decided together.` : '';
    $('document-status').textContent = p ? `Document decision: ${p.status}. ${Object.keys(p.changes).length} documents in this proposal.` : 'Start a writer draft to review its PRD and announcement.';
    if (p) {
      const selectedID = selected;
      const docs = await Promise.all(Object.keys(p.changes).sort().map(path => api(`/api/document?path=${encodeURIComponent(path)}&proposal=${encodeURIComponent(p.id)}`)));
      if (generation !== selectionGeneration || selected !== selectedID) return false;
      for (const doc of docs) {
        const details = document.createElement('details'); details.open = true;
        const summary = document.createElement('summary'); summary.textContent = doc.path;
        const comparisonLabel = document.createElement('p'); comparisonLabel.className = 'hint'; comparisonLabel.textContent = 'Changes against this proposal’s recorded base. Removed (−), added (+).';
        const comparison = document.createElement('pre'); comparison.className = 'comparison'; comparison.tabIndex = 0; comparison.setAttribute('aria-label', `${doc.path} readable comparison`);
        for (const line of (doc.comparison || doc.diff).split(/(?<=\n)/)) {
          const span = document.createElement('span'); span.textContent = line;
          span.className = line.startsWith('-') ? 'removed' : line.startsWith('+') ? 'added' : 'context'; comparison.append(span);
        }
        const raw = document.createElement('details'), rawLabel = document.createElement('summary'), diff = document.createElement('pre');
        rawLabel.textContent = 'Raw diff'; diff.tabIndex = 0; diff.setAttribute('aria-label', `${doc.path} raw diff`); diff.textContent = doc.diff; raw.append(rawLabel, diff);
        const full = document.createElement('details'), label = document.createElement('summary'), bytes = document.createElement('pre');
        label.textContent = 'Exact proposed bytes'; bytes.tabIndex = 0; bytes.setAttribute('aria-label', `${doc.path} exact proposed bytes`); bytes.textContent = doc.proposed;
        full.append(label, bytes); details.append(summary, comparisonLabel, comparison, raw, full); $('documents').append(details);
      }
    }
    $('document-confirm').checked = false;
    documentKey = key;
  }
  showReviewer('document-reviewer', p?.reviewer);
  const operation = state.announcements?.find(item => item.stage !== 'abandoned' && item.proposal === selected && item.action === 'announce');
  const inspectedView = operation ? await api(`/api/announcement?id=${encodeURIComponent(operation.id)}`) : null;
  if (generation !== selectionGeneration) return false;
  view = inspectedView;
  const nextActionKey = JSON.stringify([demo.boot, view]);
  if (nextActionKey !== actionKey) { actionKey = nextActionKey; $('action-confirm').checked = false; controls(); $('action-proof').textContent = ''; }
  $('action-status').textContent = actionSummary();
  showReviewer('action-reviewer', view?.identitySource === 'engine-verified' ? view.reviewer : null, true);
  $('warning').hidden = !view?.warning; $('warning').textContent = view?.warning || '';
  $('snapshot').hidden = !view; $('snapshot').textContent = view?.snapshot?.content || '';
  $('evidence').textContent = view ? JSON.stringify(view, null, 2) : 'No action requested.';
  setText('draft-proof', !remoteSessions() && run()?.proof ? proofLabel(run().proof) : '');
  $('meter').textContent = remoteSessions() ? 'Remote model/executor counts are unknown. Replay diagnostics are not reported by this host.' : demo.mode === 'deterministic-northwind' ? `Fixture provider calls this boot: ${demo.providerCalls}. Real model calls: 0.` : `Provider invocations this boot: ${demo.providerInvocations}. Completion-token ceiling: ${demo.completionTokenCeiling}. Not a billing ledger.`;
  stage();
  const nextOutcome = JSON.stringify([selected, p?.status, view?.status, view?.concluded, pendingRun]);
  if (nextOutcome !== outcomeKey) {
    outcomeKey = nextOutcome;
    const actionOutcome = view?.status === 'approved' ? 'Approval recorded; not executed.' : view?.status === 'denied' ? (view.concluded ? 'Denial concluded without simulation.' : 'Denial recorded. Confirmation needed to conclude.') : view?.status === 'pending-approval' ? 'Action decision awaiting review.' : view?.status === 'simulated' && view.concluded ? 'Simulation concluded. Nothing sent or published.' : view ? 'Inspect the recorded action state.' : 'Documents accepted. Review the separate action.';
    if (!actionError) announce(pendingRun ? 'Writer is preparing a draft…' : !p ? 'Start a writer draft. Review its documents before any action decision.' : p.status === 'pending' ? `Review ${Object.keys(p.changes).length} proposal documents, then accept or reject the complete proposal.` : p.status === 'stale' ? 'Proposal stale. Acceptance is unavailable. Reject it or create a new draft against the current documents.' : p.status === 'rejected' ? 'Documents rejected. No action approved. Start a new draft when ready.' : actionOutcome);
  }
  return true;
}
function refresh() {
  if (refreshPromise) return refreshPromise;
  if (reopen) return Promise.resolve();
  refreshPromise = (async () => {
    refreshing = true; controls();
    try {
      // A selection made during held reads owns a new inspection, never the old bytes.
      let complete = false;
      while (!complete) {
        const generation = selectionGeneration;
        [state, demo] = await Promise.all([api('/api/state'), api('/api/demo')]);
        if (generation !== selectionGeneration) continue;
        complete = await render(generation);
      }
      online = true; inspected = inspectionKey(); $('offline').hidden = true;
      setText('connection', 'Connected. Current engine record inspected.');
      if (actionError) announce(actionError);
    } catch (error) {
      online = false; inspected = ''; outcomeKey = ''; clearConfirmations(); documentKey = ''; actionKey = '';
      reopen = error.recovery === 'reopen'; $('offline').hidden = false;
      const recovery = reopen ? 'Last known record. Reopen this installed canvas to renew the view. Refresh cannot recover an engine restart or lost capability.' : 'Last known record. Engine unavailable or view invalid. Check the engine, then Refresh connection. If it restarted or the address changed, reopen this installed canvas.';
      setText('connection', recovery);
      announce(actionError || (['UNAUTHENTICATED', 'PERMISSION_DENIED'].includes(error.code) ? error.message : 'Connection lost. Reconnect before relying on the retained record or making another decision.'));
      setText('action-status', `Last known action: ${view?.status === 'approved' ? 'approval recorded; not executed' : view?.status || 'unknown until this draft is inspected'}.`);
      setText('action-help', recovery);
      $('document-stage').classList.remove('current'); $('action-stage').classList.remove('current');
    }
    finally { refreshing = false; refreshPromise = null; controls(); }
  })();
  return refreshPromise;
}
async function action(callback, confirmation) {
  if (busy || !online || reopen || (confirmation && !$(confirmation).checked)) return;
  if (inspected !== inspectionKey()) { announce('Evidence changed. Wait for this draft’s inspection, then review and confirm again.'); return; }
  const binding = JSON.stringify([documentKey, actionKey, selected]);
  busy = true; controls();
  try {
    // Keep the human click, but never dispatch it against changed inspection evidence.
    if (refreshPromise) await refreshPromise;
    if (!online) throw new Error('Connection lost. Use Refresh connection before another action.');
    if (inspected !== inspectionKey() || binding !== JSON.stringify([documentKey, actionKey, selected]) || (confirmation && !$(confirmation).checked)) throw new Error('Evidence changed. Review and confirm again before acting.');
    actionError = ''; await callback(); outcomeKey = '';
  }
  catch (error) { actionError = error.uncertain ? 'The action may have been recorded; it was not retried. Inspect the refreshed record before another decision.' : error.message; announce(actionError); }
  finally { clearConfirmations(); busy = false; if (online) await refresh(); else controls(); }
}
$('draft').onclick = () => action(async () => { const result = await api('/api/run', { agent: 'writer', request: 'Draft the fictional local pilot PRD and announcement.', role: reviewRole() }); pendingRun = result.run; });
$('refresh').onclick = () => refresh();
$('proposal').onchange = () => {
  selected = $('proposal').value; selectionGeneration++; inspected = ''; documentKey = ''; actionKey = ''; view = null; actionError = '';
  // Selection and evidence move together, even if the next state read fails.
  $('documents').replaceChildren(); setText('scope', '');
  setText('document-status', 'Inspecting this draft. Document evidence is not available yet.');
  setText('documents-summary', 'Document inspection pending');
  $('snapshot').hidden = true; setText('snapshot', ''); $('warning').hidden = true; setText('warning', '');
  setText('evidence', 'Action state unknown until this draft is inspected.');
  setText('action-status', 'Action state unknown until this draft is inspected.');
  setText('action-proof', ''); setText('draft-proof', '');
  $('suite-evidence').replaceChildren(); $('suite-evidence').dataset.key = ''; $('release-evidence').hidden = true;
  clearConfirmations(); stage(); controls(); refresh();
};
$('document-confirm').onchange = controls; $('action-confirm').onchange = controls;
for (const decision of ['accept', 'reject']) $(decision).onclick = () => action(() => api('/api/review', { proposal: selected, action: decision, role: reviewRole() }), 'document-confirm');
$('request').onclick = () => action(() => api('/api/announcement/request', { proposal: selected, action: 'announce', role: reviewRole() }));
for (const [id, approved] of [['approve', true], ['deny', false]]) $(id).onclick = () => action(() => api('/api/announcement/decision', { id: view.id, role: reviewRole(), approved, confirmation: { ...view.confirmation, confirmed: true } }), 'action-confirm');
$('continue').onclick = () => action(() => api('/api/announcement/continue', { id: view.id, role: reviewRole(), confirmation: { ...view.confirmation, confirmed: true } }), 'action-confirm');
$('verify').onclick = () => action(() => api('/api/verify', { run: run().id }));
$('verify-action').onclick = () => action(async () => { $('action-proof').textContent = proofLabel(await api('/api/announcement/verify', { id: view.id })); });
clearConfirmations(); controls(); refresh();
setInterval(() => { if (!busy && online) refresh(); }, 1500);
