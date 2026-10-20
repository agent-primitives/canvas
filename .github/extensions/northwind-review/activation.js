const $ = id => document.getElementById(id);

export async function setupActivation({ api, demo, reload, showRuntime }) {
  $('activation-panel').hidden = false;
  showRuntime(!!demo.activeInstance);
  const storageKey = 'canvas-frozen-activation';
  let candidate, catalog, pending, busy = false, inspectOK = false, stale = false;
  const downloads = [];
  const activationBase = demo.pendingActivation?.expectedCurrent ?? demo.activeInstance;
  const matchesPending = request => demo.pendingActivation && ['candidate', 'expectedCurrent', 'requestKey', 'confirmed', 'actor'].every(key => request[key] === demo.pendingActivation[key]);
  const status = message => { $('activation-status').textContent = message; };
  try { pending = JSON.parse(sessionStorage.getItem(storageKey) || 'null'); }
  catch { status('Saved activation cannot be read. Reopen and inspect the catalog before acting.'); return; }
  function controls() {
    const bound = !stale && inspectOK && catalog.active === activationBase;
    $('activation-prepare').disabled = busy || !!pending || !bound;
    $('activation-inspect').disabled = busy;
    $('activation-confirm').disabled = busy || !candidate || !!pending || !bound;
    $('activation-activate').disabled = $('activation-confirm').disabled || !$('activation-confirm').checked;
    $('activation-continue').hidden = !pending || !bound || pending.expectedCurrent !== activationBase;
    $('activation-continue').disabled = busy;
  }
  async function showCandidate(id) {
    $('activation-confirm').checked = false;
    candidate = null; controls();
    downloads.splice(0).forEach(url => URL.revokeObjectURL(url));
    $('activation-files').replaceChildren(); $('activation-package').textContent = '';
    if (!id) return;
    const frozen = await api(`/api/authoring/candidate?id=${encodeURIComponent(id)}`);
    if (frozen.id !== id) throw new Error('Frozen candidate identity changed');
    $('activation-package').textContent = JSON.stringify(frozen, null, 2);
    for (const file of frozen.files) {
      const details = document.createElement('details'), summary = document.createElement('summary');
      summary.textContent = `${file.path} · ${file.sha256} · ${file.size} bytes · mode ${file.mode}`;
      const bytes = document.createElement('pre'); bytes.tabIndex = 0;
      const download = document.createElement('a'); download.textContent = 'Download exact frozen bytes'; download.download = file.path.split('/').at(-1);
      let loaded = false;
      details.ontoggle = async () => {
        if (!details.open || loaded) return;
        try {
          const data = await api(`/api/authoring/file?candidate=${encodeURIComponent(id)}&path=${encodeURIComponent(file.path)}`);
          if (data.path !== file.path || data.sha256 !== file.sha256 || !['utf8', 'base64'].includes(data.encoding)) throw new Error('Frozen file identity changed');
          bytes.textContent = `${data.encoding === 'base64' ? 'Base64 (binary file)\n' : ''}${data.data}`;
          const content = data.encoding === 'base64' ? Uint8Array.from(atob(data.data), char => char.charCodeAt(0)) : data.data;
          const url = URL.createObjectURL(new Blob([content], { type: 'application/octet-stream' })); downloads.push(url); download.href = url;
          details.append(download); loaded = true;
        } catch (error) { bytes.textContent = error.message; }
      };
      details.append(summary, bytes); $('activation-files').append(details);
      details.open = file.path === frozen.definition;
    }
    candidate = frozen;
  }
  async function history(id) {
    $('activation-confirm').checked = false; controls();
    $('activation-archive').textContent = '';
    if (!id) { showRuntime(!!demo.activeInstance); return; }
    showRuntime(false);
    try {
      const archived = await api(`/api/authoring/instance?id=${encodeURIComponent(id)}`);
      $('activation-archive').textContent = JSON.stringify(archived, null, 2);
    } catch (error) { status(error.message); }
  }
  async function inspect() {
    inspectOK = false; $('activation-confirm').checked = false; controls();
    catalog = await api('/api/authoring'); inspectOK = true;
    if (!pending && catalog.pending && matchesPending(catalog.pending.request)) pending = { ...catalog.pending.request };
    const observed = pending && catalog.instances.find(instance => instance.id === catalog.active && instance.package === pending.candidate && (instance.previous || '') === pending.expectedCurrent);
    if (observed && demo.activeInstance === observed.id) { sessionStorage.removeItem(storageKey); pending = null; }
    $('activation-current').textContent = demo.activeInstance ? `Active instance: ${demo.activeInstance}` : catalog.pending ? `Activation pending. No runtime is open; predecessor: ${activationBase || 'none'}.` : 'Inert draft. No runtime is open.';
    $('activation-history').replaceChildren(new Option('Current instance', ''));
    $('activation-links').replaceChildren();
    for (const instance of catalog.instances) {
      if (instance.id !== catalog.active || instance.status === 'read-only') $('activation-history').add(new Option(`${instance.id} · ${instance.status}`, instance.id));
      const link = document.createElement('a'); link.href = '#';
      link.textContent = `${instance.id} · previous: ${instance.previous || 'none'} · next: ${instance.next || 'none'}`;
      link.onclick = event => { event.preventDefault(); const id = instance.id === demo.activeInstance && instance.status === 'active' ? '' : instance.id; $('activation-history').value = id; history(id); };
      const line = document.createElement('p'); line.append(link); $('activation-links').append(line);
    }
    if (pending) {
      const committed = catalog.instances.find(instance => instance.id === catalog.active && instance.package === pending.candidate && (instance.previous || '') === pending.expectedCurrent);
      status(committed ? 'Activation appears committed. Reopen this installed canvas to inspect it; this bridge did not observe success.' : 'Activation outcome uncertain. Inspect the catalog; only an explicit Continue sends the exact saved request again.');
    } else status(catalog.active !== activationBase ? 'Instance changed. Reopen this installed canvas; setup confirmation was cleared.' : 'Prepare explicitly to freeze working files. Reads and reloads never prepare or activate.');
    const id = pending?.candidate || catalog.candidate;
    if (id !== candidate?.id) await showCandidate(id);
    controls();
  }
  async function activate(request) {
    busy = true; $('activation-confirm').checked = false; controls();
    try {
      // Persist BEFORE sending: a lost reply must not mint a replacement key.
      sessionStorage.setItem(storageKey, JSON.stringify(request)); pending = request;
      const result = await api('/api/authoring/activate', request);
      if (!result.instance?.id || result.instance.package !== request.candidate || (result.instance.previous || '') !== request.expectedCurrent) throw new Error('Activation identity was not observed');
      sessionStorage.removeItem(storageKey); pending = null; reload();
    } catch (error) {
      if (!error.uncertain && ['ACTIVATION_CONFLICT', 'ACTIVATION_REFUSED'].includes(error.code)) {
        sessionStorage.removeItem(storageKey); pending = null;
      }
      status(error.message);
      // Catalog failure must never erase an unknown request or trigger a retry.
      try { await inspect(); } catch { inspectOK = false; status('Activation outcome unknown; saved exact request retained. Inspect catalog when available.'); }
    } finally { busy = false; controls(); }
  }
  document.addEventListener('canvas-reopen', () => { stale = true; inspectOK = false; $('activation-confirm').checked = false; controls(); status('Engine boot changed. Reopen this installed canvas; confirmation was cleared.'); });
  $('activation-confirm').onchange = controls;
  $('activation-history').onchange = () => history($('activation-history').value);
  $('activation-inspect').onclick = async () => { try { await inspect(); } catch (error) { status(error.message); controls(); } };
  $('activation-prepare').onclick = async () => {
    if (stale || busy || pending || !inspectOK || catalog.active !== activationBase) return;
    busy = true; $('activation-confirm').checked = false; controls();
    try {
      const prepared = await api('/api/authoring/prepare', { definition: $('activation-definition').value });
      await showCandidate(prepared.candidate.id); status('Frozen package prepared. Review its complete inventory, exact files and suite pins before confirming.');
    } catch (error) { status(error.message); }
    finally { busy = false; controls(); }
  };
  $('activation-activate').onclick = () => {
    if (stale || busy || pending || !candidate || !$('activation-confirm').checked || !inspectOK || catalog.active !== activationBase) return;
    activate({ candidate: candidate.id, expectedCurrent: activationBase, requestKey: crypto.randomUUID(), confirmed: true, actor: 'local person (unverified)' });
  };
  $('activation-continue').onclick = () => { if (!stale && !busy && pending && pending.expectedCurrent === activationBase && inspectOK && catalog.active === activationBase) activate(pending); };
  try { await inspect(); } catch (error) { status(error.message); controls(); }
}
