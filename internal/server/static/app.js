'use strict';

const message = document.querySelector('#message');
const list = document.querySelector('#recordings');
const video = document.querySelector('#player');
const adapterSelect = document.querySelector('#adapter');
const inputFields = document.querySelector('#input-fields');
const configFields = document.querySelector('#config-fields');
const challengeFields = document.querySelector('#challenge-fields');
let hls = null;
let inputSchema = { fields: [] };
let activeWorkflow = null;
let isStarting = false;

async function api(url, options = {}) {
  const response = await fetch(url, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  const body = response.status === 204 ? null : await response.json();
  if (!response.ok) {
    const error = new Error(body?.error || response.statusText);
    error.status = response.status;
    throw error;
  }
  return body;
}

function finishWorkflowUI() {
  activeWorkflow = null;
  document.querySelector('#challenge-panel').hidden = true;
  adapterSelect.disabled = false;
  document.querySelector('#start button[type="submit"]').disabled = false;
}

async function clearExpiredWorkflow(workflowID) {
  try {
    await api(`/api/resolve-workflows/${encodeURIComponent(workflowID)}`);
    return false;
  } catch (error) {
    if (error.status !== 404) return false;
    finishWorkflowUI();
    await loadSchema();
    return true;
  }
}

function encodedResource(resource) {
  const bytes = new TextEncoder().encode(JSON.stringify(resource));
  let binary = '';
  bytes.forEach((value) => { binary += String.fromCharCode(value); });
  return btoa(binary).replace(/=/g, '').replace(/\+/g, '-').replace(/\//g, '_');
}

function canonicalJSON(value) {
  if (Array.isArray(value)) return value.map(canonicalJSON);
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonicalJSON(value[key])]));
  }
  return value;
}

function semanticKey(value) {
  return JSON.stringify(canonicalJSON(value));
}

function fieldSemanticKey(field, value) {
  if (field.control === 'multi-select' && Array.isArray(value)) {
    return JSON.stringify(value.map(semanticKey).sort());
  }
  return semanticKey(value);
}

function readControl(field, control) {
  if (field.control === 'boolean') {
    if (control.dataset.touched !== 'true') return undefined;
    return control.checked;
  }
  if (field.control === 'number') return control.value === '' ? undefined : Number(control.value);
  if (field.control === 'select') return control.value === '' ? undefined : JSON.parse(control.value);
  if (field.control === 'multi-select') {
    if (control.dataset.touched !== 'true') return undefined;
    return Array.from(control.selectedOptions, (option) => JSON.parse(option.value));
  }
  return control.value === '' && control.dataset.touched !== 'true' ? undefined : control.value;
}

function visibilityCondition(condition, values) {
  if (!condition || typeof condition !== 'object') return true;
  if (Array.isArray(condition.all)) return condition.all.every((child) => visibilityCondition(child, values));
  if (Array.isArray(condition.any)) return condition.any.some((child) => visibilityCondition(child, values));
  const value = values[condition.field];
  if (Object.hasOwn(condition, 'equals')) return semanticKey(value) === semanticKey(condition.equals);
  if (Object.hasOwn(condition, 'not_equals')) return semanticKey(value) !== semanticKey(condition.not_equals);
  if (Object.hasOwn(condition, 'truthy')) return Boolean(value) === condition.truthy;
  return false;
}

function valuesFromControls(target, schema) {
  const result = {};
  for (const field of schema.fields || []) {
    if (field.control === 'action' || field.control === 'status') continue;
    const control = target.querySelector(`[data-key="${CSS.escape(field.key)}"]`);
    if (!control) continue;
    let value = readControl(field, control);
    if (value === undefined && field.default !== undefined && field.control !== 'secret') value = field.default;
    if (value !== undefined) result[field.key] = value;
  }
  return result;
}

function updateVisibility(target, schema) {
  const values = valuesFromControls(target, schema);
  for (const row of target.querySelectorAll('[data-field-row]')) {
    const field = (schema.fields || []).find((candidate) => candidate.key === row.dataset.fieldRow);
    row.hidden = !!field?.visible_when && !visibilityCondition(field.visible_when, values);
  }
}

function appendSafeExternalLink(target, prompt) {
  if (prompt.type !== 'navigate' && prompt.type !== 'action') return;
  let data;
  if (prompt.data && typeof prompt.data === 'object' && !Array.isArray(prompt.data)) {
    data = prompt.data;
  } else if (typeof prompt.data === 'string') {
    try { data = JSON.parse(prompt.data); } catch { return; }
  } else {
    data = null;
  }
  if (!data || typeof data !== 'object' || Array.isArray(data) || typeof data.url !== 'string') return;
  let url;
  try { url = new URL(data.url); } catch { return; }
  if ((url.protocol !== 'https:' && url.protocol !== 'http:') || !url.hostname || url.username || url.password) return;
  const link = document.createElement('a');
  link.href = url.href;
  link.target = '_blank';
  link.rel = 'noopener noreferrer';
  link.textContent = typeof data.label === 'string' && data.label.trim() ? data.label : prompt.type === 'action' ? 'Continue' : 'Open the requested page';
  target.append(link);
}

function renderSchema(target, schema, projection = {}) {
  target.replaceChildren();
  const values = projection.values || {};
  const storedValues = projection.storedValues || {};
  const secrets = projection.secrets || {};
  const storedSecrets = projection.storedSecrets || {};
  const sources = projection.sources || {};
  const secretSources = projection.secretSources || {};
  const currentScope = projection.currentScope || 'plugin';

  for (const field of schema.fields || []) {
    const row = document.createElement('div');
    row.className = 'schema-field';
    row.dataset.fieldRow = field.key;
    const title = document.createElement('label');
    title.append(document.createTextNode(`${field.label} `));

    if (field.control === 'action' || field.control === 'status') {
      const display = document.createElement('span');
      display.textContent = field.description || '';
      row.append(title, display);
      target.append(row);
      continue;
    }

    let control;
    if (field.control === 'textarea') {
      control = document.createElement('textarea');
    } else if (field.control === 'select' || field.control === 'multi-select') {
      control = document.createElement('select');
      control.multiple = field.control === 'multi-select';
      if (field.control === 'select') {
        const empty = document.createElement('option');
        empty.value = '';
        empty.textContent = field.required ? 'Select an option' : 'No value';
        control.append(empty);
      }
      for (const option of field.options || []) {
        const item = document.createElement('option');
        item.value = JSON.stringify(option.value);
        item.textContent = option.label;
        control.append(item);
      }
    } else {
      control = document.createElement('input');
      control.type = field.control === 'secret' ? 'password' : field.control === 'number' ? 'number' : field.control === 'boolean' ? 'checkbox' : 'text';
    }
    control.dataset.key = field.key;
    control.addEventListener('input', () => {
      if (field.control !== 'secret') control.dataset.touched = 'true';
      updateVisibility(target, schema);
    });
    control.addEventListener('change', () => {
      if (field.control !== 'secret') control.dataset.touched = 'true';
      updateVisibility(target, schema);
    });

    if (field.control === 'secret') {
      const state = secrets[field.key] || { configured: false };
      const localState = storedSecrets[field.key] || { configured: false };
      const status = document.createElement('small');
      status.textContent = state.configured ? (secretSources[field.key] && secretSources[field.key] !== currentScope ? ' configured (inherited)' : ' configured') : ' not configured';
      title.append(status);
      control.value = '';
      control.required = !!field.required && !state.configured;
      if (localState.configured) {
        const clearLabel = document.createElement('label');
        const clear = document.createElement('input');
        clear.type = 'checkbox';
        clear.dataset.clearSecret = field.key;
        clearLabel.append(clear, document.createTextNode(' Clear local value'));
        row.append(clearLabel);
      }
    } else {
      let initial = values[field.key];
      if (initial === undefined && field.default !== undefined) initial = field.default;
      if (field.control === 'multi-select') control.dataset.touched = 'false';
      if (initial !== undefined) {
        if (field.control === 'boolean') {
          control.checked = !!initial;
          control.dataset.touched = 'false';
        } else if (field.control === 'select') {
          const match = Array.from(control.options).find((option) => semanticKey(JSON.parse(option.value)) === semanticKey(initial));
          if (match) control.value = match.value;
        } else if (field.control === 'multi-select') {
          const selected = new Set((initial || []).map(semanticKey));
          Array.from(control.options).forEach((option) => { option.selected = selected.has(semanticKey(JSON.parse(option.value))); });
        } else {
          control.value = initial;
        }
      }
      if (field.required && field.control !== 'boolean' && field.control !== 'multi-select') control.required = true;
      const constraint = field.constraints || {};
      if (control.type === 'number') {
        if (constraint.min !== undefined) control.min = constraint.min;
        if (constraint.max !== undefined) control.max = constraint.max;
      }
      if (control.type === 'text' || control.tagName === 'TEXTAREA') {
        if (constraint.min_length !== undefined) control.minLength = constraint.min_length;
        if (constraint.max_length !== undefined) control.maxLength = constraint.max_length;
        if (constraint.pattern) control.pattern = constraint.pattern;
      }
      if (field.control === 'multi-select') {
        if (constraint.min_items !== undefined) control.dataset.minItems = constraint.min_items;
        if (constraint.max_items !== undefined) control.dataset.maxItems = constraint.max_items;
      }

      const source = sources[field.key];
      if (source && source !== currentScope) {
        const inherited = document.createElement('small');
        inherited.textContent = `Inherited from ${source}`;
        row.append(inherited);
      }
      if (Object.hasOwn(storedValues, field.key)) {
        const resetLabel = document.createElement('label');
        const reset = document.createElement('input');
        reset.type = 'checkbox';
        reset.dataset.clearValue = field.key;
        resetLabel.append(reset, document.createTextNode(' Use inherited/default value'));
        row.append(resetLabel);
      }
      control.dataset.initial = initial === undefined ? '' : fieldSemanticKey(field, initial);
    }

    title.append(control);
    row.append(title);
    if (field.description) {
      const hint = document.createElement('small');
      hint.textContent = field.description;
      row.append(hint);
    }
    target.append(row);
  }
  updateVisibility(target, schema);
}

function collect(target, schema, partial) {
  const values = {};
  const secrets = {};
  const clearValues = [];
  const clearSecrets = [];
  for (const field of schema.fields || []) {
    if (field.control === 'action' || field.control === 'status') continue;
    const row = target.querySelector(`[data-field-row="${CSS.escape(field.key)}"]`);
    if (!row || row.hidden) continue;
    if (field.control === 'secret') {
      const control = row.querySelector(`[data-key="${CSS.escape(field.key)}"]`);
      const clear = row.querySelector(`[data-clear-secret="${CSS.escape(field.key)}"]`);
      if (clear?.checked) clearSecrets.push(field.key);
      else if (control?.value !== '') secrets[field.key] = control.value;
      continue;
    }
    const clear = row.querySelector(`[data-clear-value="${CSS.escape(field.key)}"]`);
    if (clear?.checked) {
      clearValues.push(field.key);
      continue;
    }
    const control = row.querySelector(`[data-key="${CSS.escape(field.key)}"]`);
    if (!control) continue;
    let value = readControl(field, control);
    if (value === undefined && field.default !== undefined) value = field.default;
    if (field.control === 'boolean' && value === undefined && (field.required || field.default !== undefined)) {
      value = field.default === undefined ? false : !!field.default;
    }
    if (field.control === 'multi-select' && value === undefined && (field.required || field.default !== undefined)) {
      value = field.default === undefined ? [] : field.default;
    }
    if (value === undefined) continue;
    if (partial && control.dataset.initial === fieldSemanticKey(field, value)) continue;
    if (field.control === 'multi-select') {
      const minimum = Number(control.dataset.minItems || 0);
      const maximum = Number(control.dataset.maxItems || Number.MAX_SAFE_INTEGER);
      if (value.length < minimum || value.length > maximum) throw new Error(`${field.label} has an invalid number of selections`);
    }
    values[field.key] = value;
  }
  return { values, secrets, clearValues, clearSecrets };
}

async function loadConfiguration(id, resource) {
  const query = resource ? `?resource=${encodedResource(resource)}` : '';
  const data = await api(`/api/adapters/${encodeURIComponent(id)}/config${query}`);
  const schema = data.schema || { fields: [] };
  renderSchema(configFields, schema, {
    values: data.effective?.values || {},
    storedValues: data.stored?.values || {},
    secrets: data.effective?.secrets || {},
    storedSecrets: data.stored?.secrets || {},
    sources: data.value_sources || {},
    secretSources: data.secret_sources || {},
    currentScope: data.current_scope,
  });
  const save = document.querySelector('#save-config');
  save.disabled = !!activeWorkflow;
  save.onclick = async () => {
    try {
      const submitted = collect(configFields, schema, true);
      await api(`/api/adapters/${encodeURIComponent(id)}/config`, {
        method: 'PUT',
        body: JSON.stringify({
          resource: resource || undefined,
          values: submitted.values,
          secrets: submitted.secrets,
          clear_values: submitted.clearValues,
          clear_secrets: submitted.clearSecrets,
        }),
      });
      await loadConfiguration(id, resource);
      message.textContent = 'Settings saved.';
    } catch (error) {
      message.textContent = error.message;
    }
  };
}

async function loadSchema(id = adapterSelect.value) {
  inputFields.replaceChildren();
  configFields.replaceChildren();
  if (!id) return;
  const data = await api(`/api/adapters/${encodeURIComponent(id)}/schema`);
  inputSchema = data.input_schema || { fields: [] };
  renderSchema(inputFields, inputSchema);
  await loadConfiguration(id, null);
}

async function loadAdapters() {
  const adapters = await api('/api/adapters');
  adapterSelect.replaceChildren();
  for (const item of adapters) {
    const option = document.createElement('option');
    if (item.descriptor) {
      option.value = item.descriptor.id;
      option.textContent = `${item.descriptor.name} — ${item.status.state} v${item.descriptor.version}`;
    } else {
      option.value = '';
      option.textContent = `${item.status.id} — ${item.status.state}`;
      option.disabled = true;
    }
    adapterSelect.append(option);
  }
  await loadSchema();
}

async function refreshRecordings() {
  const items = await api('/api/recordings');
  list.replaceChildren(...items.map((item) => {
    const li = document.createElement('li');
    li.append(document.createTextNode(`${item.title || item.id} — ${item.state} — ${item.segment_count} segments `));
    const button = document.createElement('button');
    if (item.state === 'recording') {
      button.textContent = 'Stop';
      button.onclick = async () => {
        try {
          await api(`/api/recordings/${encodeURIComponent(item.id)}/stop`, { method: 'POST' });
          await refreshRecordings();
        } catch (error) { message.textContent = error.message; }
      };
    } else {
      button.textContent = 'Play VOD';
      button.onclick = () => playRecording(item.id);
    }
    li.append(button);
    return li;
  }));
}

function playRecording(id) {
  const source = `/api/recordings/${encodeURIComponent(id)}/play/master.m3u8`;
  if (hls) hls.destroy();
  if (video.canPlayType('application/vnd.apple.mpegurl')) {
    video.src = source;
    video.play().catch(() => {});
  } else if (window.Hls && Hls.isSupported()) {
    hls = new Hls();
    hls.loadSource(source);
    hls.attachMedia(video);
    hls.on(Hls.Events.MANIFEST_PARSED, () => video.play().catch(() => {}));
  } else {
    message.textContent = 'This browser has no HLS playback support.';
  }
}

function promptSchema(progress) {
  const challenge = progress.challenge || {};
  if (challenge.schema?.fields?.length) return challenge.schema;
  const fields = (challenge.prompt?.fields || []).map((field) => ({ ...field }));
  return { fields };
}

function renderPrompt(progress) {
  const prompt = progress.challenge?.prompt;
  const details = document.querySelector('#challenge-message');
  details.replaceChildren();
  if (!prompt) return;
  const text = document.createElement('p');
  text.textContent = `${prompt.title || 'Additional information'} ${prompt.message || ''}`;
  details.append(text);
  if (prompt.type === 'display' || prompt.type === 'status' || prompt.type === 'complete' || prompt.type === 'error') {
    const display = document.createElement('p');
    display.textContent = prompt.message || prompt.title || '';
    details.append(display);
  }
  appendSafeExternalLink(details, prompt);
}

async function showWorkflow(progress) {
  if (activeWorkflow && activeWorkflow.workflow_id !== progress.workflow_id) {
    throw new Error('Another adapter workflow is already active. Cancel it before starting another.');
  }
  activeWorkflow = progress;
  adapterSelect.disabled = true;
  document.querySelector('#start button[type="submit"]').disabled = true;
  document.querySelector('#challenge-panel').hidden = false;
  renderPrompt(progress);
  await loadConfiguration(progress.adapter_id, progress.resource || null);
  const schema = promptSchema(progress);
  renderSchema(challengeFields, schema);
  for (const field of schema.fields || []) {
    if (!field.persistence) continue;
    const policy = field.persistence;
    const row = challengeFields.querySelector(`[data-field-row="${CSS.escape(field.key)}"]`);
    if (!row || policy.mode === 'forbidden') continue;
    const label = document.createElement('label');
    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox';
    checkbox.dataset.persistField = field.key;
    checkbox.checked = policy.mode === 'required';
    checkbox.disabled = policy.mode === 'required';
    label.append(checkbox, document.createTextNode(policy.mode === 'required' ? ' Save this value (required)' : ' Save this value'));
    row.append(label);
  }
  if (progress.challenge?.persistable) {
    for (const field of schema.fields || []) {
      if (field.persistence || field.control === 'action' || field.control === 'status') continue;
      const row = challengeFields.querySelector(`[data-field-row="${CSS.escape(field.key)}"]`);
      if (!row) continue;
      const label = document.createElement('label');
      const checkbox = document.createElement('input');
      checkbox.type = 'checkbox';
      checkbox.dataset.persistField = field.key;
      label.append(checkbox, document.createTextNode(' Save this value'));
      row.append(label);
    }
  }
}

document.querySelector('#continue-workflow').onclick = async () => {
  if (!activeWorkflow) return;
  try {
    const schema = promptSchema(activeWorkflow);
    const submitted = collect(challengeFields, schema, false);
    const persistFields = Array.from(challengeFields.querySelectorAll('[data-persist-field]:checked'), (item) => item.dataset.persistField);
    const result = await api(`/api/resolve-workflows/${encodeURIComponent(activeWorkflow.workflow_id)}/continue`, {
      method: 'POST',
      body: JSON.stringify({ values: submitted.values, secrets: submitted.secrets, persist_fields: persistFields }),
    });
    if (result.workflow_id) {
      await showWorkflow(result);
      return;
    }
    document.querySelector('#challenge-panel').hidden = true;
    activeWorkflow = null;
    adapterSelect.disabled = false;
    document.querySelector('#start button[type="submit"]').disabled = false;
    message.textContent = `Started ${result.id}`;
    await refreshRecordings();
  } catch (error) {
    const workflowID = activeWorkflow?.workflow_id;
    if (workflowID && await clearExpiredWorkflow(workflowID)) {
      message.textContent = 'Workflow expired or was canceled. Start again.';
    } else {
      message.textContent = error.message;
    }
  }
};

document.querySelector('#cancel-workflow').onclick = async () => {
  if (!activeWorkflow) return;
  try {
    await api(`/api/resolve-workflows/${encodeURIComponent(activeWorkflow.workflow_id)}`, { method: 'DELETE' });
    finishWorkflowUI();
    await loadSchema();
    message.textContent = 'Workflow canceled.';
  } catch (error) {
    const workflowID = activeWorkflow?.workflow_id;
    if (error.status === 404 && workflowID) {
      finishWorkflowUI();
      await loadSchema();
      message.textContent = 'Workflow expired or was already canceled.';
      return;
    }
    message.textContent = error.message;
  }
};

document.querySelector('#start').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (activeWorkflow || isStarting) return;
  isStarting = true;
  const submit = event.currentTarget.querySelector('button[type="submit"]');
  submit.disabled = true;
  adapterSelect.disabled = true;
  try {
    const submitted = collect(inputFields, inputSchema, false);
    const result = await api('/api/recordings', {
      method: 'POST',
      body: JSON.stringify({
        adapter_id: adapterSelect.value,
        input: submitted.values,
        title: event.currentTarget.elements.title.value,
      }),
    });
    if (result.workflow_id) {
      await showWorkflow(result);
      message.textContent = 'Adapter needs additional information.';
      return;
    }
    message.textContent = `Started ${result.id}`;
    event.currentTarget.elements.title.value = '';
    await refreshRecordings();
  } catch (error) {
    message.textContent = error.message;
  } finally {
    isStarting = false;
    if (!activeWorkflow) {
      submit.disabled = false;
      adapterSelect.disabled = false;
    }
  }
});

adapterSelect.addEventListener('change', () => loadSchema().catch((error) => { message.textContent = error.message; }));
loadAdapters().catch((error) => { message.textContent = error.message; });
refreshRecordings().catch((error) => { message.textContent = error.message; });
setInterval(() => refreshRecordings().catch(() => {}), 5000);
