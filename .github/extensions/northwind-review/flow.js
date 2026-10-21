const $ = id => document.getElementById(id);
const terminal = state => ['complete', 'rejected', 'cancelled', 'denied', 'exhausted'].includes(state);
const node = (tag, text) => { const element = document.createElement(tag); element.textContent = text; return element; };

// Presentation only. Every command still goes through the engine's existing
// admission, generation, review and native-effect fences.
export function createFlowPanel({ api, action, selectionChanged, requesterRole }) {
  let snapshot, state, selectedRun = '', selectedDefinition = '', formKey = '', formSupported = false, pendingStart, uncertainStart = false, inspectedKey = '';
  const current = () => snapshot?.runs.find(run => run.id === selectedRun);
  const definition = () => snapshot?.flows.find(flow => flow.id === current()?.flow);
  const formDefinition = () => snapshot?.flows.find(flow => flow.id === selectedDefinition);
  const gateRole = () => {
    const steps = definition()?.definition.steps || [];
    return steps.find(step => step.id === current()?.step)?.gate?.role || steps.findLast(step => step.gate)?.gate.role || requesterRole;
  };
  const key = () => JSON.stringify([snapshot?.head, selectedRun, current()]);
  const command = (name, body, confirmation) => action(() => api('/api/flow/' + name, body), confirmation);
  const withRun = () => ({ run: current().id, role: current().role });
  const fields = () => [...$('flow-fields').querySelectorAll('[data-property]')];

  function renderSteps(target, flow, run) {
    $(target).replaceChildren();
    for (const [i, step] of (flow?.definition.steps || []).entries()) {
      const li = document.createElement('li'); li.dataset.step = step.id;
      const done = run && (i < run.position || run.state === 'complete');
      if (run && i === run.position && !terminal(run.state)) li.setAttribute('aria-current', 'step');
      li.append(node('strong', `${step.id}: ${step.agent ? state.agents.find(agent => agent.id === step.agent)?.name || step.agent : 'Simulated action'}`));
      if (step.gate) {
        const documents = step.gate.accept.map(id => snapshot.documents[id]?.title || id).join(', ');
        li.append(node('p', `Required proposal: ${documents}. Human gate: ${step.gate.role} (unverified local role). All additional replacements are reviewed too.`));
      } else li.append(node('p', 'Review the exact accepted snapshot → record approval or denial → fresh confirmation → Continue simulation → local receipt.'));
      if (done) li.append(node('span', 'Completed'));
      $(target).append(li);
    }
  }

  function renderForm() {
    const flow = formDefinition();
    const next = JSON.stringify([flow, snapshot.operatorAgentRuns]);
    if (next === formKey) return;
    formKey = next; $('flow-fields').replaceChildren();
    const schema = flow?.schema;
    const unsupported = object => ['$ref', '$dynamicRef', 'allOf', 'anyOf', 'oneOf', 'not', 'if', 'then', 'else', 'dependentSchemas'].some(key => Object.hasOwn(object || {}, key));
    formSupported = schema?.type === 'object' && schema.properties && !unsupported(schema);
    for (const [name, property] of Object.entries(schema?.properties || {})) {
      const required = schema.required?.includes(name), enums = property.enum;
      const supportedEnum = Array.isArray(enums) && enums.length && enums.every(value => typeof value === 'string' || typeof value === 'boolean' || (typeof value === 'number' && Number.isSafeInteger(value)));
      const supported = !unsupported(property) && (supportedEnum || ['string', 'boolean'].includes(property.type));
      if (!supported) { formSupported = false; $('flow-fields').append(node('p', `${name}: this schema control is not supported by the Run form.`)); continue; }
      const label = node('label', `${property.title || name}${required ? ' (required)' : ' (optional)'}`);
      const input = document.createElement(supportedEnum || property.type === 'boolean' ? 'select' : 'textarea');
      input.id = 'flow-input-' + name; input.dataset.property = name; input.dataset.type = property.type || 'enum'; input.required = !!required;
      label.htmlFor = input.id;
      if (input.tagName === 'SELECT') {
        input.add(new Option(required ? 'Choose a value' : 'Not provided', ''));
        for (const value of supportedEnum ? enums : [true, false]) input.add(new Option(String(value), JSON.stringify(value)));
      } else {
        input.rows = 2;
        input.oninput = () => {
          const count = Array.from(input.value).length;
          input.setCustomValidity((property.minLength !== undefined && count < property.minLength && (required || input.value !== '')) || (property.maxLength !== undefined && count > property.maxLength) ? 'Use the displayed character bounds.' : '');
        };
      }
      const hints = [property.description];
      if (property.minLength !== undefined) hints.push(`Minimum ${property.minLength} characters.`);
      if (property.maxLength !== undefined) hints.push(`Maximum ${property.maxLength} characters.`);
      if (Object.hasOwn(property, 'default')) hints.push(`Schema default hint: ${JSON.stringify(property.default)}. Not inserted automatically.`);
      const hint = node('p', hints.filter(Boolean).join(' ')); hint.className = 'hint'; hint.id = input.id + '-help'; input.setAttribute('aria-describedby', hint.id);
      $('flow-fields').append(label, input, hint);
    }
    $('flow-form-help').textContent = formSupported ? 'Every agent step waits for human review. Optional empty controls are not provided. Runs retain this workspace; use a new instance for an independent topic.' : 'This schema needs controls the panel does not support. Nothing can run from this form; no fields or constraints were silently dropped.';
    const max = Math.min(flow?.definition.limits.agentRuns || 0, snapshot.operatorAgentRuns);
    $('flow-budget').max = String(max); $('flow-budget').value = String(max);
    $('flow-preview-title').textContent = `New run preview: ${flow?.id || ''}`;
    renderSteps('flow-preview', flow);
  }

  function renderStatus(view) {
    const run = current();
    if (view?.id !== run?.action) view = null;
    let status = 'No flow run yet. Choose Run on a topic.';
    if (run) {
      const labels = { starting: 'Starting', running: 'Running', review: `Waiting for ${gateRole()} — unverified local role`, paused: 'Failed / paused — inspect before an explicit retry', uncertain: 'Uncertain — explicit reconciliation required', ready: 'Recovered acceptance — explicit Advance required', cancelling: 'Cancellation requested — native settlement pending', cancelled: 'Cancelled — accepted history retained', rejected: 'Rejected — accepted history retained', exhausted: 'Budget exhausted — no further dispatch', action: 'Action approval — waiting for reviewer', 'approved-not-executed': 'Approved; not executed — fresh confirmation and Continue required', complete: !run.action ? 'Complete — documents accepted; no action configured' : view?.receipt && view.concluded ? 'Complete — local simulated receipt only' : 'Complete — inspect recorded action evidence', denied: view?.concluded ? 'Denied — concluded without simulation' : 'Denied — explicit cleanup required; no simulation' };
      status = labels[run.state] || `Flow: ${run.state}`;
    }
    $('flow-status').textContent = status;
    $('flow-location').textContent = run ? `Step ${Math.min(run.position + 1, definition().definition.steps.length)}: ${run.step}. Attempts ${run.attempts} / ${run.agentRuns}.` : 'No step is running.';
    $('flow-reason').textContent = run?.reason || '';
    $('flow-record').textContent = run ? `Run ${run.id}; requester role ${run.role} (unverified local attribution); starting revision ${run.startBase}.` : 'Operator-loaded definition. Document editing requires a compiled editor binding and a separate proposal review.';
    const listKey = JSON.stringify([definition() || formDefinition(), run?.position, run?.state, state.agents]);
    if ($('flow-steps').dataset.key !== listKey) {
      renderSteps('flow-steps', definition() || formDefinition(), run);
      $('flow-steps').dataset.key = listKey;
    }
  }

  function update(next, nextState) {
    snapshot = next; state = nextState;
    if (pendingStart) {
      const found = snapshot.runs.find(run => run.requestKey === pendingStart.requestKey);
      if (found) { selectedRun = found.id; pendingStart = null; uncertainStart = false; $('flow-form').open = false; }
    }
    if (!snapshot.runs.some(run => run.id === selectedRun)) selectedRun = snapshot.runs.find(run => !terminal(run.state))?.id || snapshot.runs.at(-1)?.id || '';
    if (!snapshot.flows.some(flow => flow.id === selectedDefinition)) selectedDefinition = current()?.flow || snapshot.flows[0]?.id || '';
    $('flow-panel').hidden = false;
    $('canvas-title').textContent = snapshot.definition.title;
    $('flow-definition').textContent = `Definition ${snapshot.definition.id} / ${snapshot.definition.digest}. Current accepted revision ${snapshot.head}. Operator ceiling ${snapshot.operatorAgentRuns} attempts.`;
    const optionKey = JSON.stringify(snapshot.flows.map(flow => flow.id));
    if ($('flow-definition-select').dataset.key !== optionKey) {
      $('flow-definition-select').replaceChildren(); snapshot.flows.forEach(flow => { $('flow-definition-select').add(new Option(flow.id, flow.id)); }); $('flow-definition-select').dataset.key = optionKey;
    }
    $('flow-definition-select').value = selectedDefinition;
    const historyKey = JSON.stringify(snapshot.runs.map(run => [run.id, run.state]));
    if ($('flow-history').dataset.key !== historyKey) {
      $('flow-history').replaceChildren();
      if (!snapshot.runs.length) $('flow-history').add(new Option('No runs yet', ''));
      snapshot.runs.forEach((run, i) => { $('flow-history').add(new Option(`Run ${i + 1}: ${run.flow} — ${run.state}`, run.id)); });
      $('flow-history').dataset.key = historyKey;
    }
    $('flow-history').value = selectedRun;
    renderForm(); renderStatus();
  }

  async function inspect() {
    const run = current(), binding = key();
    if (!run) { $('flow-run-record').textContent = 'No run selected.'; $('flow-attempts').textContent = ''; inspectedKey = ''; return true; }
    if (inspectedKey !== binding) {
      const detail = await api('/api/flow?run=' + encodeURIComponent(run.id));
      if (key() !== binding || ['id', 'state', 'step', 'attempt', 'proposal', 'action'].some(field => detail[field] !== run[field])) return false;
      $('flow-run-record').textContent = JSON.stringify(detail, null, 2); inspectedKey = binding;
    }
    $('flow-attempts').textContent = JSON.stringify(state.runs.filter(attempt => attempt.flowRun === run.id).map(attempt => ({ attempt: attempt.id, step: attempt.flowStep, agent: attempt.agent, status: attempt.status, proposal: attempt.proposal, session: attempt.session, requestedBy: attempt.requestedBy, triggeredBy: attempt.triggeredBy, triggeringReview: attempt.triggeringReview })), null, 2);
    return true;
  }

  function controls(enabled, ready, confirmed, view, gate) {
    const run = current(), unsettled = run?.state === 'denied' && view && !view.concluded;
    const runnable = enabled && snapshot && !snapshot.busy && !unsettled && !pendingStart;
    $('flow-open').disabled = !runnable;
    $('flow-start').disabled = !runnable || !formSupported;
    for (const input of [...fields(), $('flow-budget'), $('flow-definition-select')]) input.disabled = !runnable;
    $('flow-history').disabled = !enabled;
    $('flow-resubmit').hidden = !pendingStart || !uncertainStart;
    $('flow-resubmit').disabled = !enabled;
    $('flow-pending').hidden = !pendingStart;
    $('flow-feedback').disabled = !enabled || !gate || run?.state !== 'review';
    $('flow-changes').disabled = !ready || !gate || run?.state !== 'review' || !confirmed || !$('flow-feedback').value.trim();
    $('flow-retry').disabled = !ready || run?.state !== 'paused';
    $('flow-cancel').disabled = !ready || !['starting', 'running', 'review', 'paused', 'uncertain', 'ready', 'editing', 'edited', 'edit-paused'].includes(run?.state);
    $('flow-reconcile').disabled = !ready || !['uncertain', 'cancelling', 'action', 'approved-not-executed', 'denied'].includes(run?.state);
    $('flow-advance').disabled = !ready || run?.state !== 'ready';
    $('flow-verify').disabled = !ready || !run;
  }

  async function submitStart(body) {
    pendingStart = body; uncertainStart = false;
    try { const result = await api('/api/flow/start', body); selectedRun = result.id; pendingStart = null; $('flow-form').open = false; }
    catch (error) { uncertainStart = !!error.uncertain; if (!uncertainStart) pendingStart = null; throw error; }
  }
  $('flow-open').onclick = () => { $('flow-form').open = true; fields()[0]?.focus(); };
  $('flow-run-form').onsubmit = event => {
    event.preventDefault(); if (pendingStart || snapshot?.busy || !formSupported || !$('flow-run-form').reportValidity()) return;
    const inputs = Object.create(null);
    for (const field of fields()) if (field.value !== '') inputs[field.dataset.property] = field.tagName === 'SELECT' ? JSON.parse(field.value) : field.value;
    const body = { flow: selectedDefinition, requestKey: crypto.randomUUID(), role: requesterRole, inputs, agentRuns: Number($('flow-budget').value) };
    action(() => submitStart(body));
  };
  $('flow-resubmit').onclick = () => { const body = pendingStart; if (body) action(() => submitStart(body)); };
  $('flow-history').onchange = () => { selectedRun = $('flow-history').value; selectionChanged(); };
  $('flow-definition-select').onchange = () => { selectedDefinition = $('flow-definition-select').value; formKey = ''; renderForm(); selectionChanged(); };
  $('flow-feedback').oninput = () => $('document-confirm').dispatchEvent(new Event('change'));
  $('flow-changes').onclick = () => command('changes', { ...withRun(), role: gateRole(), proposal: current().proposal, requestKey: crypto.randomUUID(), feedback: $('flow-feedback').value }, 'document-confirm');
  $('flow-retry').onclick = () => command('retry', { ...withRun(), attempt: current().attempt, requestKey: crypto.randomUUID() });
  $('flow-cancel').onclick = () => command('cancel', withRun());
  $('flow-reconcile').onclick = () => command('reconcile', withRun());
  $('flow-advance').onclick = () => command('advance', { ...withRun(), requestKey: crypto.randomUUID() });
  $('flow-verify').onclick = () => action(async () => {
    const proof = await api('/api/flow/verify', { run: current().id });
    const native = (proof.sessions || []).filter(session => session.Coverage === 'reconstructed');
    const zero = native.length && native.every(session => {
      const p = session.Proof;
      return p?.chainValid && p.reconstructed && p.outputsMatch && p.liveUnchanged && p.modelCalls === 0 && p.executorCalls === 0 && p.executionIDs?.length === 1 && p.executionIDs[0] === session.Execution;
    });
    $('flow-proof').textContent = proof.complete && zero ? `Flow replay verified: model 0 / executor 0. Native executions covered: ${native.length}.` : `Flow replay incomplete; 0/0 not verified. ${proof.reason || 'Exact native coverage is unavailable.'}`;
    $('flow-evidence').textContent = JSON.stringify(proof, null, 2);
  });
  return { update, inspect, controls, renderStatus, key, current, gateRole,
    proposals: () => {
      // The flow binding can be published before the public attempt association.
      const run = current();
      const ids = new Set([run?.proposal, ...Object.values(run?.accepted || {}), ...(state?.runs || []).filter(attempt => attempt.flowRun === selectedRun).map(attempt => attempt.proposal)]);
      return (state?.proposals || []).filter(p => ids.has(p.id));
    },
    isGate: proposal => current()?.state === 'review' && current()?.proposal === proposal,
    actionAllowed: () => ['action', 'approved-not-executed', 'denied'].includes(current()?.state),
  };
}
