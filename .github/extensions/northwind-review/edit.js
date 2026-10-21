const $ = id => document.getElementById(id);
const terminal = state => ['complete', 'rejected', 'cancelled', 'denied', 'exhausted'].includes(state);
const storageKey = 'canvas-pending-edit';

export function editSection(kind, pointer, headings, occurrence) {
  if (kind === 'whole') return undefined;
  if (kind === 'json-pointer') {
    if (pointer.length > 512 || (pointer !== '' && !pointer.startsWith('/')) || /~(?![01])/u.test(pointer)) throw new Error('Enter an exact JSON Pointer. An empty pointer selects the root.');
    return { kind, pointer };
  }
  if (kind === 'markdown-heading') {
    const headingPath = headings.split('\n').map(line => line.trim());
    if (!headingPath.length || headingPath.length > 16 || headingPath.some(line => !line || new TextEncoder().encode(line).length > 128) || !/^[1-9][0-9]*$/u.test(occurrence) || !Number.isSafeInteger(Number(occurrence))) throw new Error('Enter one exact heading per line and a positive occurrence.');
    return { kind, headingPath, occurrence: Number(occurrence) };
  }
  throw new Error('Choose a supported edit scope.');
}

export function createEditPanel({ api, action, selectionChanged, currentFlow = () => null, requesterRole = 'GM' }) {
  let snapshot, state, metadata, context, contextKey = '', selectedEdit = '', evidenceKey = '', pending, sending = false, foreign = false;
  let enabled = false, ready = false;
  try { pending = JSON.parse(sessionStorage.getItem(storageKey) || 'null'); } catch { pending = null; }
  const current = () => snapshot?.edits.find(edit => edit.id === selectedEdit);
  const flowID = () => { const flow = currentFlow(); return flow && !terminal(flow.state) ? flow.id : ''; };
  const binding = () => JSON.stringify([metadata?.boot, metadata?.activeInstance || '']);
  const role = () => current()?.request.role || requesterRole;
  const blocked = () => !!snapshot?.slot || !!pending || sending;
  const isGate = proposal => current()?.state === 'proposal' && current()?.proposal === proposal && snapshot?.slot === current()?.id;
  function clear() { $('edit-confirm').checked = false; $('edit-command-confirm').checked = false; }
  function changed() { clear(); selectionChanged(); }
  function persist() { try { if (pending) sessionStorage.setItem(storageKey, JSON.stringify(pending)); else sessionStorage.removeItem(storageKey); } catch { /* Memory still retains the exact request for this view. */ } }
  function scope() { return editSection($('edit-kind').value, $('edit-pointer').value, $('edit-headings').value, $('edit-occurrence').value); }
  function preview() {
    $('edit-json-fields').hidden = $('edit-kind').value !== 'json-pointer';
    $('edit-markdown-fields').hidden = $('edit-kind').value !== 'markdown-heading';
    try {
      const section = scope();
      let target = context?.content || '';
      if (section?.kind === 'json-pointer' && context) {
        target = JSON.parse(context.content);
        for (const part of section.pointer === '' ? [] : section.pointer.slice(1).split('/')) {
          const key = part.replaceAll('~1', '/').replaceAll('~0', '~');
          if (target === null || typeof target !== 'object' || !Object.hasOwn(target, key) || (Array.isArray(target) && !/^(0|[1-9][0-9]*)$/u.test(key))) throw new Error('JSON Pointer does not resolve in the accepted document.');
          target = target[key];
        }
        target = 'The exact accepted document bytes are displayed below. Selection is resolved without numeric normalization by the engine.';
      }
      $('edit-scope').textContent = section ? JSON.stringify(section, null, 2) + (section.kind === 'json-pointer' ? '\nSelected accepted value:\n' + target : '\nExact heading scope is resolved by the engine before dispatch; no whole-document fallback.') : 'Whole document. The proposal must replace only this document.';
      return true;
    } catch (error) { $('edit-scope').textContent = error.message; return false; }
  }
  function controls(nextEnabled = enabled, nextReady = ready) {
    enabled = nextEnabled; ready = nextReady;
    const edit = current(), valid = preview();
    for (const id of ['edit-document','edit-instruction','edit-kind','edit-pointer','edit-headings','edit-occurrence']) $(id).disabled = !enabled || blocked();
    $('edit-history').disabled = !enabled || sending;
    $('edit-confirm').disabled = !enabled || blocked() || !context || !valid;
    $('edit-request').disabled = !enabled || !ready || blocked() || !context || !valid || !$('edit-confirm').checked || !$('edit-instruction').value.trim();
    $('edit-continue').hidden = !pending;
    $('edit-continue').disabled = !enabled || !ready || sending || foreign;
    $('edit-command-confirm').disabled = !enabled || !ready || !edit || sending || !!pending;
    const confirmed = enabled && ready && $('edit-command-confirm').checked && !pending && !sending;
    $('edit-reconcile').disabled = !confirmed || !(['queued','running','uncertain','proposal'].includes(edit?.state) || (edit?.state === 'abandoned' && !edit.nativeSettled));
    $('edit-abandon').disabled = !confirmed || snapshot?.slot !== edit?.id;
    $('edit-resume').disabled = !confirmed || !edit?.request.flowRun || !['rejected','failed','abandoned','stale'].includes(edit?.state);
    $('edit-rerun').disabled = !confirmed || !edit?.request.flowRun || edit?.state !== 'accepted';
  }
  async function refresh(nextState, demo) {
    if (state?.head !== nextState.head || metadata?.boot !== demo.boot || metadata?.activeInstance !== demo.activeInstance) changed();
    state = nextState; metadata = demo;
    snapshot = await api('/api/edits');
    foreign = !!pending && pending.binding !== binding();
    if (!snapshot.edits.some(edit => edit.id === selectedEdit)) selectedEdit = snapshot.slot || snapshot.edits.at(-1)?.id || '';
    if (snapshot.slot) selectedEdit = snapshot.slot;
    const nextKey = JSON.stringify([binding(), state.head, snapshot, flowID(), selectedEdit]);
    if (nextKey !== evidenceKey) { evidenceKey = nextKey; changed(); }
    const doc = $('edit-document');
    const oldDocument = doc.value;
    doc.replaceChildren();
    snapshot.documents.forEach(item => doc.add(new Option(item.title + ' (' + item.path + ')', item.id)));
    doc.value = snapshot.documents.some(item => item.id === oldDocument) ? oldDocument : snapshot.documents[0]?.id || '';
    const history = $('edit-history'); history.replaceChildren(); history.add(new Option('No edit selected', ''));
    snapshot.edits.forEach(edit => history.add(new Option(edit.request.document + ': ' + edit.state + ' (' + edit.id + ')', edit.id)));
    history.value = selectedEdit;
    const nextContext = JSON.stringify([binding(), state.head, doc.value, flowID()]);
    if (nextContext !== contextKey) {
      context = null; contextKey = ''; changed();
      if (doc.value) {
        const query = new URLSearchParams({ document: doc.value });
        if (flowID()) query.set('flowRun', flowID());
        const found = await api('/api/edit/context?' + query);
        if (found.baseRevision !== state.head || found.document !== doc.value || found.flowRun !== flowID()) return false;
        context = found;
      }
      contextKey = nextContext;
    }
    $('edit-panel').hidden = !snapshot.documents.length && !snapshot.edits.length && !pending;
    $('edit-base').textContent = context ? 'Accepted revision: ' + context.baseRevision + '\nDocument SHA-256: ' + context.baseDigest + '\nEditor: ' + context.agent + (context.flowRun ? '\nAssociated active flow: ' + context.flowRun : '\nDocument-only edit. No completed flow is reopened.') : 'No editable accepted document is available.';
    $('edit-accepted').textContent = context?.content || '';
    const edit = current();
    $('edit-status').textContent = foreign ? 'Saved request belongs to another engine boot or instance. Reopen the original view; it cannot be continued here.' : pending ? 'Reply unconfirmed. Exact request retained. Inspect, then explicitly Continue exact edit request. No automatic retry.' : edit ? 'Edit: ' + edit.state + (edit.state === 'abandoned' ? (edit.nativeSettled ? '. Native settlement proved; output stays abandoned.' : '. Explicit Reconcile may prove native settlement; output stays abandoned.') : '') + (edit.proposal ? '. Review the complete proposal below using Accept or Reject.' : '') : 'Request a proposal, not an accepted-document change.';
    $('edit-detail').textContent = edit ? JSON.stringify(edit, null, 2) : '';
    $('edit-resolved').textContent = edit ? 'Frozen resolved scope:\n' + JSON.stringify(edit.scope, null, 2) + '\nThe complete proposed document is shown in the normal proposal review below.' + (currentFlow()?.action ? '\nWarning: accepting an edit revokes the parked action. Native Cancel is unavailable; rerun, later flow starts or retirement may remain blocked.' : '') : '';
    controls();
    return true;
  }
  async function submitExact() {
    if (!pending || foreign || sending) return;
    const body = pending.body;
    sending = true; changed(); controls();
    try {
      const route = pending.route || '/api/edit/request';
      const result = await api(route, body);
      selectedEdit = route === '/api/edit/request' ? result.id : body.edit; pending = null; persist();
    } catch (error) {
      // Request admission returns 4xx only before reservation. Maintenance can
      // have persisted intent, so its exact request is retained on every error.
      if (error.code === 'EDIT_REFUSED' || ((!pending?.route || pending.route === '/api/edit/request') && error.status >= 400 && error.status < 500 && error.uncertain === false)) { pending = null; persist(); }
      throw error;
    } finally { sending = false; controls(); }
  }
  $('edit-request').onclick = () => action(async () => {
    if (blocked() || !context || !$('edit-confirm').checked) return;
    const section = scope();
    const body = { document: context.document, baseRevision: context.baseRevision, baseDigest: context.baseDigest, instruction: $('edit-instruction').value, requestKey: crypto.randomUUID(), role: requesterRole };
    if (context.flowRun) body.flowRun = context.flowRun;
    if (section) body.section = section;
    pending = { binding: binding(), body }; persist();
    await submitExact();
  }, 'edit-confirm');
  $('edit-continue').onclick = () => action(submitExact);
  for (const command of ['reconcile','abandon','resume','rerun']) $('edit-' + command).onclick = () => action(async () => {
    const edit = current();
    if (!edit || !$('edit-command-confirm').checked || pending) return;
    pending = { binding: binding(), route: '/api/edit/' + command, body: { edit: edit.id, role: edit.request.role, requestKey: crypto.randomUUID(), expectedHead: state.head, confirmed: true } }; persist();
    await submitExact();
  }, 'edit-command-confirm');
  $('edit-document').onchange = () => { context = null; contextKey = ''; changed(); controls(); };
  $('edit-history').onchange = () => { selectedEdit = $('edit-history').value; changed(); controls(); };
  for (const id of ['edit-instruction','edit-kind','edit-pointer','edit-headings','edit-occurrence']) $(id).oninput = () => { clear(); controls(); };
  $('edit-kind').onchange = () => { clear(); controls(); };
  $('edit-confirm').onchange = () => controls();
  $('edit-command-confirm').onchange = () => controls();
  document.addEventListener?.('canvas-reopen', () => { enabled = false; foreign = true; clear(); context = null; controls(false, false); });
  return { refresh, controls, blocked, isGate, current, gateRole: role, key: () => evidenceKey, proposal: () => current()?.proposal, proposals: () => (snapshot?.edits || []).map(edit => edit.proposal).filter(Boolean) };
}
