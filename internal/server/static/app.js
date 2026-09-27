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
let authState = { auth_enabled: false, authenticated: false, needs_bootstrap: false, csrf_token: '' };
let authReady = false;
let productRefreshStarted = false;
let recordingCursor = '';
let recordingFilters = {};
let selectedRecordingID = '';
let selectedResourceHint = null;
let resourceParentHint = null;
let currentAdapters = [];
let resourceCursor = '';
let exportAvailable = false;
let selectedRecordingState = '';
let retentionEnabled = false;
const exportPollers = new Map();
let applicationLogCursor = '';

function isPublicAuthRequest(url) {
  return ['/api/auth/session', '/api/auth/login', '/api/auth/bootstrap'].includes(new URL(url, location.href).pathname);
}

function showAuthenticationRequired() {
  if (!authState.auth_enabled) return;
  authState.authenticated = false;
  authState.csrf_token = '';
  authReady = true;
  document.querySelector('#auth-panel').hidden = false;
  document.querySelector('#app').hidden = true;
  document.querySelector('#logout').hidden = true;
  document.querySelector('#connection-state').textContent = 'Authentication required';
  configureAuthForm();
}

async function api(url, options = {}) {
  const method = String(options.method || 'GET').toUpperCase();
  const publicAuth = isPublicAuthRequest(url);
  if (authReady && authState.auth_enabled && !authState.authenticated && !publicAuth) {
    const error = new Error('Sign in to continue.');
    error.status = 401;
    throw error;
  }
  const headers = new Headers(options.headers || {});
  if (options.body !== undefined && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && authState.auth_enabled && authState.authenticated && authState.csrf_token) {
    headers.set('X-CSRF-Token', authState.csrf_token);
  }
  const response = await fetch(url, { credentials: 'same-origin', ...options, method, headers });
  let body = null;
  if (response.status !== 204) {
    const contentType = response.headers.get('Content-Type') || '';
    if (contentType.includes('application/json')) body = await response.json();
    else if (!response.ok) body = { error: response.statusText };
  }
  if (!response.ok) {
    if (response.status === 401 && !publicAuth) showAuthenticationRequired();
    const error = new Error(body?.error || response.statusText);
    error.status = response.status;
    throw error;
  }
  return body;
}

function configureAuthForm() {
  const bootstrap = authState.needs_bootstrap;
  document.querySelector('#auth-title').textContent = bootstrap ? 'Set up administrator' : 'Sign in';
  document.querySelector('#auth-help').textContent = bootstrap ? 'Create the administrator password using the one-time bootstrap token.' : 'Enter your administrator password.';
  document.querySelector('#auth-token-help').hidden = !bootstrap;
  document.querySelector('#auth-token-help').textContent = bootstrap ? `Read the one-time token from ${authState.bootstrap_token_path || 'security/bootstrap-token'} under the configured data directory. The token is not shown in this browser.` : '';
  document.querySelector('#auth-token-wrap').hidden = !bootstrap;
  document.querySelector('#auth-token').hidden = !bootstrap;
  document.querySelector('#auth-token').required = bootstrap;
  document.querySelector('#auth-token').value = '';
  document.querySelector('#auth-password-label').textContent = bootstrap ? 'Administrator password' : 'Password';
  document.querySelector('#auth-password').autocomplete = bootstrap ? 'new-password' : 'current-password';
  document.querySelector('#auth-submit').textContent = bootstrap ? 'Create administrator' : 'Sign in';
}

async function loadAuthentication() {
  const session = await api('/api/auth/session');
  authState = { ...authState, ...session };
  authReady = true;
  if (!authState.auth_enabled || authState.authenticated) {
    document.querySelector('#auth-panel').hidden = true;
    document.querySelector('#app').hidden = false;
    document.querySelector('#logout').hidden = !authState.auth_enabled;
    document.querySelector('#connection-state').textContent = authState.auth_enabled ? 'Signed in' : 'Local access';
    await startProductRefresh();
  } else {
    showAuthenticationRequired();
  }
}

document.querySelector('#auth-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const bootstrap = authState.needs_bootstrap;
  const payload = bootstrap
    ? { token: form.elements.token.value, password: form.elements.password.value }
    : { password: form.elements.password.value };
  try {
    const result = await api(bootstrap ? '/api/auth/bootstrap' : '/api/auth/login', { method: 'POST', body: JSON.stringify(payload) });
    authState.authenticated = !!result.authenticated;
    authState.csrf_token = result.csrf_token || '';
    authState.needs_bootstrap = false;
    form.reset();
    document.querySelector('#auth-panel').hidden = true;
    document.querySelector('#app').hidden = false;
    document.querySelector('#logout').hidden = false;
    document.querySelector('#connection-state').textContent = 'Signed in';
    message.textContent = bootstrap ? 'Administrator account created.' : 'Signed in.';
    await startProductRefresh();
  } catch (error) {
    message.textContent = error.message;
  }
});

document.querySelector('#logout').addEventListener('click', async () => {
  try { await api('/api/auth/logout', { method: 'POST' }); } catch (error) { message.textContent = error.message; }
  showAuthenticationRequired();
});

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

async function loadConfiguration(id, resource, target = configFields, saveButton = document.querySelector('#save-config')) {
  const query = resource ? `?resource=${encodedResource(resource)}` : '';
  const data = await api(`/api/adapters/${encodeURIComponent(id)}/config${query}`);
  const schema = data.schema || { fields: [] };
  renderSchema(target, schema, {
    values: data.effective?.values || {},
    storedValues: data.stored?.values || {},
    secrets: data.effective?.secrets || {},
    storedSecrets: data.stored?.secrets || {},
    sources: data.value_sources || {},
    secretSources: data.secret_sources || {},
    currentScope: data.current_scope,
  });
  saveButton.disabled = !!activeWorkflow;
  saveButton.onclick = async () => {
    try {
      const submitted = collect(target, schema, true);
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
      await loadConfiguration(id, resource, target, saveButton);
      message.textContent = 'Settings saved.';
    } catch (error) {
      message.textContent = error.message;
    }
  };
}

async function loadSchema(id = adapterSelect.value) {
  inputFields.replaceChildren();
  configFields.replaceChildren();
  document.querySelector('#adapter-config-fields').replaceChildren();
  if (activeWorkflow && id && id !== activeWorkflow.adapter_id) {
    throw new Error('Adapter selection is locked while a workflow is active.');
  }
  if (!id) {
    document.querySelector('#save-config').disabled = true;
    document.querySelector('#adapter-save-config').disabled = true;
    return;
  }
  const data = await api(`/api/adapters/${encodeURIComponent(id)}/schema`);
  inputSchema = data.input_schema || { fields: [] };
  renderSchema(inputFields, inputSchema);
  await Promise.all([
    loadConfiguration(id, null, configFields, document.querySelector('#save-config')),
    loadConfiguration(id, null, document.querySelector('#adapter-config-fields'), document.querySelector('#adapter-save-config')),
  ]);
}

async function loadAdapters() {
  const adapters = await api('/api/adapters');
  currentAdapters = Array.isArray(adapters) ? adapters : [];
  const preferredAdapterID = activeWorkflow?.adapter_id || adapterSelect.value;
  adapterSelect.replaceChildren();
  const cards = document.querySelector('#adapter-cards');
  cards.replaceChildren();
  let firstReadyAdapter = '';
  for (const item of currentAdapters) {
    const option = document.createElement('option');
    if (item.descriptor) {
      option.value = item.descriptor.id;
      option.textContent = `${item.descriptor.name} — ${item.status.state} v${item.descriptor.version}`;
      option.disabled = item.status.state !== 'ready';
      if (!option.disabled && !firstReadyAdapter) firstReadyAdapter = option.value;
      const card = document.createElement('article');
      card.className = 'panel adapter-card';
      const title = document.createElement('h3');
      title.textContent = item.descriptor.name;
      const status = document.createElement('p');
      status.textContent = `${item.status.state} · ${item.descriptor.id} · v${item.descriptor.version}`;
      const capabilities = document.createElement('p');
      capabilities.className = 'muted';
      capabilities.textContent = (item.descriptor.capabilities || []).join(', ') || 'No optional capabilities declared';
      const select = document.createElement('button');
      select.type = 'button';
      select.textContent = 'Configure';
      select.disabled = !!activeWorkflow && activeWorkflow.adapter_id !== item.descriptor.id;
      select.addEventListener('click', () => {
        if (activeWorkflow && activeWorkflow.adapter_id !== item.descriptor.id) {
          message.textContent = 'Adapter selection is locked while a workflow is active.';
          return;
        }
        adapterSelect.value = item.descriptor.id;
        loadSchema(item.descriptor.id).then(() => {
          document.querySelector('#adapters-section').scrollIntoView({ behavior: 'smooth', block: 'start' });
        }).catch((error) => { message.textContent = error.message; });
      });
      const actions = node('div', null, 'button-row');
      const disabled = item.status.state === 'disabled';
      actions.append(actionButton('Restart', () => controlAdapter(item.descriptor.id, 'restart'), 'secondary'));
      actions.append(actionButton(disabled ? 'Enable' : 'Disable', () => controlAdapter(item.descriptor.id, disabled ? 'enable' : 'disable'), disabled ? 'secondary' : 'danger'));
      if (disabled) actions.querySelector('button').disabled = true;
      card.append(title, status, capabilities, select, actions);
      cards.append(card);
    } else {
      option.value = '';
      option.textContent = `${item.status.id} — ${item.status.state}`;
      option.disabled = true;
      const card = document.createElement('article');
      card.className = 'panel adapter-card';
      const title = document.createElement('h3');
      title.textContent = item.status.id || 'Unavailable adapter';
      const status = document.createElement('p');
      status.textContent = item.status.state || 'unavailable';
      card.append(title, status);
      cards.append(card);
    }
    adapterSelect.append(option);
  }
  const preferredIsReady = currentAdapters.some((item) => item.descriptor?.id === preferredAdapterID && item.status.state === 'ready');
  if (activeAdapterID && currentAdapters.some((item) => item.descriptor?.id === activeAdapterID)) adapterSelect.value = activeAdapterID;
  else if (preferredIsReady) adapterSelect.value = preferredAdapterID;
  else if (firstReadyAdapter) adapterSelect.value = firstReadyAdapter;
  adapterSelect.disabled = !!activeWorkflow;
  const selectedIsReady = currentAdapters.some((item) => item.descriptor?.id === adapterSelect.value && item.status.state === 'ready');
  document.querySelector('#start button[type="submit"]').disabled = !!activeWorkflow || !selectedIsReady;
  await loadSchema(activeWorkflow?.adapter_id || adapterSelect.value || firstReadyAdapter);
}

async function controlAdapter(id, action) {
  if (action === 'disable' && !window.confirm(`Disable adapter “${id}”? Active recordings already resolved by it will continue.`)) return;
  try {
    await api(`/api/adapters/${encodeURIComponent(id)}/${action}`, { method: 'POST' });
    message.textContent = `Adapter ${id}: ${action} requested.`;
    await Promise.all([loadAdapters(), refreshDashboard()]);
  } catch (error) {
    message.textContent = error.message;
    try { await loadAdapters(); } catch { /* retain the last known adapter view */ }
  }
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KB', 'MB', 'GB', 'TB', 'PB'];
  let amount = bytes;
  let unit = -1;
  do { amount /= 1024; unit += 1; } while (amount >= 1024 && unit < units.length - 1);
  return `${amount.toFixed(amount >= 10 ? 0 : 1)} ${units[unit]}`;
}

function formatDuration(value) {
  const seconds = Number(value);
  if (!Number.isFinite(seconds) || seconds < 0) return '—';
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = Math.floor(seconds % 60);
  return hours ? `${hours}h ${minutes}m` : minutes ? `${minutes}m ${rest}s` : `${rest}s`;
}

function formatDate(value) {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? '—' : date.toLocaleString();
}

function setExportAvailability(available) {
  exportAvailable = available === true;
  const button = document.querySelector('#create-export');
  const note = document.querySelector('#export-note');
  const canStart = exportAvailable && selectedRecordingState !== 'recording';
  button.hidden = !canStart;
  button.disabled = !canStart;
  if (!exportAvailable) note.textContent = 'MKV remux export is unavailable in this deployment.';
  else if (selectedRecordingState === 'recording') note.textContent = 'Stop the recording before creating an export.';
  else note.textContent = 'Creates a separate MKV remux. The canonical archive remains unchanged.';
  if (!exportAvailable) document.querySelector('#export-jobs').replaceChildren();
}

function node(tag, text, className) {
  const element = document.createElement(tag);
  if (text !== undefined && text !== null) element.textContent = String(text);
  if (className) element.className = className;
  return element;
}

function actionButton(label, action, className = 'secondary') {
  const button = node('button', label, className);
  button.type = 'button';
  button.addEventListener('click', action);
  return button;
}

function renderRecordingItem(item, compact = false) {
  const article = node('article', null, 'recording-card');
  const header = node('div', null, 'recording-card-heading');
  const title = node('h3', item.title || item.id);
  const badge = node('span', item.state, `badge state-${String(item.state || 'unknown')}`);
  header.append(title, badge);
  const metadata = node('p', `${item.adapter_name || item.adapter_id || 'Unknown adapter'} · ${formatDate(item.started_at)} · ${item.segment_count ?? 0} segments · ${formatBytes(item.archive_size_bytes)}`, 'muted');
  const details = node('p', `Duration ${formatDuration(item.duration_seconds)} · ${item.gap_count ?? 0} gaps · integrity ${item.integrity || 'unknown'}`, 'muted');
  const tags = node('p', Array.isArray(item.tags) && item.tags.length ? item.tags.join(' · ') : 'No tags', 'tag-line');
  const actions = node('div', null, 'button-row');
  if (item.state === 'recording') {
    actions.append(actionButton('Stop', async () => {
      try {
        await api(`/api/recordings/${encodeURIComponent(item.id)}/stop`, { method: 'POST' });
        await Promise.all([refreshRecordings(), refreshDashboard()]);
      } catch (error) { message.textContent = error.message; }
    }));
  } else {
    actions.append(actionButton('Play VOD', () => {
      showSection('recordings-section');
      showRecordingDetail(item.id).then(() => playRecording(item.id)).catch((error) => { message.textContent = error.message; });
    }));
  }
  actions.append(actionButton('Details', () => showRecordingDetail(item.id)));
  actions.append(actionButton('Tags', () => showRecordingDetail(item.id).then(() => document.querySelector('#detail-tags input[name="tags"]').focus()).catch((error) => { message.textContent = error.message; })));
  if (item.state !== 'recording') {
    actions.append(actionButton('Verify', () => verifyRecording(item.id)));
    actions.append(actionButton('Delete', async () => {
      if (!window.confirm(`Delete “${item.title || item.id}” and its archived files? This cannot be undone.`)) return;
      try {
        await api(`/api/recordings/${encodeURIComponent(item.id)}`, { method: 'DELETE' });
        if (selectedRecordingID === item.id) closeRecordingDetail();
        message.textContent = 'Recording deleted.';
        await Promise.all([refreshRecordings(), refreshDashboard()]);
      } catch (error) { message.textContent = error.message; }
    }, 'danger'));
  }
  article.append(header, metadata, details, tags, actions);
  if (compact) article.classList.add('compact');
  return article;
}

function queryFromForm(form) {
  const data = new FormData(form);
  const values = {};
  for (const [key, value] of data.entries()) if (String(value).trim() !== '') values[key] = String(value).trim();
  return values;
}

function renderRecordingPage(items, append = false) {
  if (!append) list.replaceChildren();
  for (const item of items || []) list.append(renderRecordingItem(item));
  if (!append && !items?.length) list.append(node('p', 'No recordings match the current filters.', 'muted'));
}

async function refreshRecordings(append = false) {
  const params = new URLSearchParams(recordingFilters);
  params.set('limit', '25');
  if (append && recordingCursor) params.set('cursor', recordingCursor);
  const page = await api(`/api/v2/recordings?${params.toString()}`);
  renderRecordingPage(page.items || [], append);
  recordingCursor = page.next_cursor || '';
  document.querySelector('#recording-total').textContent = `${page.total ?? (page.items || []).length} recordings`;
  document.querySelector('#recordings-next').hidden = !recordingCursor;
}

function showSection(sectionID) {
  for (const section of document.querySelectorAll('main > .page-section')) section.hidden = section.id !== sectionID;
  document.querySelector('#recording-detail').hidden = true;
  for (const button of document.querySelectorAll('.tabs [data-section]')) {
    button.setAttribute('aria-current', button.dataset.section === sectionID ? 'page' : 'false');
  }
}

async function showRecordingDetail(id) {
  selectedRecordingID = id;
  const detail = await api(`/api/recordings/${encodeURIComponent(id)}`);
  const recording = detail?.recording || detail;
  selectedRecordingState = recording.state || '';
  setExportAvailability(exportAvailable);
  const stats = detail?.statistics || null;
  document.querySelector('#detail-title').textContent = recording.title || recording.id || id;
  document.querySelector('#verify-recording').hidden = recording.state === 'recording';
  const detailStats = document.querySelector('#detail-stats');
  detailStats.replaceChildren();
  const statsList = [
    ['State', recording.state], ['Started', formatDate(recording.started_at)],
    ['Duration', formatDuration(stats?.duration_seconds ?? recording.duration_seconds)],
    ['Archive size', stats ? formatBytes(stats.archive_size_bytes) : 'Unavailable'],
    ['Media payload', stats ? formatBytes(stats.media_payload_size_bytes) : 'Unavailable'],
    ['Init payload', stats ? formatBytes(stats.init_payload_size_bytes) : 'Unavailable'],
    ['Manifest bytes', stats ? formatBytes(stats.manifest_size_bytes) : 'Unavailable'],
    ['Manifest snapshots', stats?.manifest_snapshot_count ?? recording.manifest_snapshots?.length ?? 0],
    ['Segments', stats?.segment_count ?? recording.segment_count ?? countSegments(recording)],
    ['Init segments', stats?.init_segment_count ?? 0],
    ['Gap records', stats?.gap_count ?? (recording.gaps || []).length],
    ['Missing segments', stats?.gap_segment_count ?? 'Unavailable'],
    ['Gap duration', stats?.gap_duration_seconds === null || stats?.gap_duration_seconds === undefined ? 'Unavailable' : formatDuration(stats.gap_duration_seconds)],
    ['Resource', recording.resource ? `${recording.resource.resource_type || recording.resource.type || ''} ${recording.resource.resource_id || recording.resource.id || ''}`.trim() : '—'],
  ];
  for (const [label, value] of statsList) detailStats.append(metricCard(label, value ?? '—'));
  const gaps = document.querySelector('#detail-gaps');
  gaps.replaceChildren();
  for (const gap of recording.gaps || []) {
    const range = gap.from_sequence === gap.to_sequence ? String(gap.from_sequence) : `${gap.from_sequence}–${gap.to_sequence}`;
    gaps.append(node('li', `Track ${gap.track_id || '—'} · epoch ${gap.source_epoch ?? 0} · sequence ${range}${gap.reason ? ` · ${gap.reason}` : ''}`));
  }
  if (!gaps.childElementCount) gaps.append(node('li', 'No recorded gaps.'));
  const tagResult = await api(`/api/recordings/${encodeURIComponent(id)}/tags`);
  document.querySelector('#detail-tags input[name="tags"]').value = (tagResult.tags || []).join(', ');
  await Promise.allSettled([loadRecordingIntegrity(id), loadArchiveIndex(id), loadRecordingEvents(id), loadExportJobs(id), loadThumbnail(id)]);
  document.querySelector('#recording-detail').hidden = false;
  document.querySelector('#recording-detail').scrollIntoView({ behavior: 'smooth', block: 'start' });
}

function thumbnailPanel() {
  let panel = document.querySelector('#detail-thumbnail');
  if (panel) return panel;
  panel = node('section');
  panel.id = 'detail-thumbnail';
  panel.className = 'panel thumbnail-panel';
  const heading = node('div', null, 'section-heading');
  heading.append(node('h3', 'Thumbnail projection'));
  const regenerate = actionButton('Regenerate', regenerateThumbnail);
  regenerate.id = 'regenerate-thumbnail';
  heading.append(regenerate);
  const placeholder = node('p', 'No thumbnail is available for this recording.', 'muted');
  placeholder.id = 'thumbnail-placeholder';
  const image = document.createElement('img');
  image.id = 'recording-thumbnail';
  image.className = 'thumbnail-preview';
  image.alt = 'Generated recording thumbnail';
  image.hidden = true;
  image.addEventListener('load', () => {
    image.hidden = false;
    placeholder.hidden = true;
  });
  image.addEventListener('error', () => {
    image.hidden = true;
    placeholder.hidden = false;
  });
  panel.append(heading, placeholder, image);
  document.querySelector('#recording-detail').insertBefore(panel, document.querySelector('#detail-stats'));
  return panel;
}

async function loadThumbnail(id) {
  thumbnailPanel();
  const image = document.querySelector('#recording-thumbnail');
  const button = document.querySelector('#regenerate-thumbnail');
  const placeholder = document.querySelector('#thumbnail-placeholder');
  image.hidden = true;
  placeholder.hidden = false;
  button.hidden = !exportAvailable || selectedRecordingState === 'recording';
  image.src = `/api/recordings/${encodeURIComponent(id)}/thumbnail?cache=${Date.now()}`;
}

async function regenerateThumbnail() {
  if (!selectedRecordingID || !exportAvailable || selectedRecordingState === 'recording') return;
  const button = document.querySelector('#regenerate-thumbnail');
  button.disabled = true;
  try {
    await api(`/api/recordings/${encodeURIComponent(selectedRecordingID)}/thumbnail/regenerate`, { method: 'POST' });
    message.textContent = 'Thumbnail projection generated.';
    await loadThumbnail(selectedRecordingID);
  } catch (error) {
    message.textContent = error.message;
  } finally {
    button.disabled = false;
    button.hidden = !exportAvailable || selectedRecordingState === 'recording';
  }
}

function countSegments(recording) {
  let count = 0;
  for (const track of Object.values(recording.tracks || {})) count += (track.segments || []).length;
  return count;
}

function metricCard(label, value) {
  const card = node('article', null, 'metric-card');
  card.append(node('span', label, 'metric-label'), node('strong', value, 'metric-value'));
  return card;
}

function closeRecordingDetail() {
  selectedRecordingID = '';
  selectedRecordingState = '';
  setExportAvailability(exportAvailable);
  document.querySelector('#recording-detail').hidden = true;
  if (hls) { hls.destroy(); hls = null; }
  video.removeAttribute('src');
  video.load();
}

async function loadRecordingIntegrity(id) {
  const target = document.querySelector('#detail-integrity');
  try {
    const result = await api(`/api/recordings/${encodeURIComponent(id)}/integrity`);
    target.replaceChildren(node('p', `Status: ${result.status || 'unknown'}`), node('p', `Verified ${result.objects_verified ?? 0} of ${result.objects_total ?? 0} objects · missing ${result.objects_missing ?? 0} · corrupt ${result.objects_corrupt ?? 0}`));
    if (result.last_verified_at) target.append(node('small', `Last verified ${formatDate(result.last_verified_at)}`, 'muted'));
  } catch (error) { target.replaceChildren(node('p', error.message)); }
}

async function loadArchiveIndex(id) {
  const target = document.querySelector('#archive-index');
  target.replaceChildren();
  try {
    const result = await api(`/api/recordings/${encodeURIComponent(id)}/archive/index`);
    for (const entry of result.entries || []) {
      const row = node('li', null, 'archive-entry');
      row.append(node('span', entry.path), node('small', `${entry.kind} · ${formatBytes(entry.size)}${entry.sha256 ? ` · SHA-256 ${entry.sha256}` : ''}`, 'muted'));
      target.append(row);
    }
    if (!target.childElementCount) target.append(node('li', 'No archive entries.'));
  } catch (error) { target.append(node('li', `Archive index unavailable: ${error.message}`, 'muted')); }
}

async function loadRecordingEvents(id) {
  const target = document.querySelector('#detail-events');
  target.replaceChildren();
  try {
    const result = await api(`/api/recordings/${encodeURIComponent(id)}/events?limit=100`);
    for (const item of result.items || []) target.append(node('li', `${formatDate(item.at || item.created_at)} · ${item.type || item.kind || 'event'}${item.count ? ` · ${item.count}` : ''}${item.message ? ` · ${item.message}` : ''}`));
    if (!target.childElementCount) target.append(node('li', 'No events recorded.'));
  } catch (error) { target.append(node('li', `Events unavailable: ${error.message}`, 'muted')); }
}

function terminalExportState(state) {
  return ['completed', 'failed', 'canceled'].includes(state);
}

function renderExportJob(job) {
  const li = node('li', null, 'export-job');
  const summary = node('span', `${job.kind || 'export'} · ${job.state} · ${formatDate(job.created_at)}${job.size ? ` · ${formatBytes(job.size)}` : ''}`);
  li.append(summary);
  const actions = node('span', null, 'button-row');
  if (job.state === 'completed') {
    const download = node('a', 'Download MKV');
    download.href = `/api/exports/${encodeURIComponent(job.id)}/download`;
    download.setAttribute('download', '');
    actions.append(download);
  }
  if (job.state === 'queued' || job.state === 'running') {
    actions.append(actionButton('Cancel', async () => {
      if (!window.confirm('Cancel this export job?')) return;
      try {
        await api(`/api/exports/${encodeURIComponent(job.id)}`, { method: 'DELETE' });
        await loadExportJobs(job.recording_id || selectedRecordingID);
      } catch (error) { message.textContent = error.message; }
    }, 'secondary'));
    startExportPolling(job.id, job.recording_id || selectedRecordingID);
  } else {
    actions.append(actionButton('Delete job', async () => {
      if (!window.confirm('Delete this export job and its derived file?')) return;
      try {
        await api(`/api/exports/${encodeURIComponent(job.id)}`, { method: 'DELETE' });
        await loadExportJobs(job.recording_id || selectedRecordingID);
      } catch (error) { message.textContent = error.message; }
    }, 'danger'));
  }
  li.append(actions);
  return li;
}

async function loadExportJobs(recordingID) {
  const target = document.querySelector('#export-jobs');
  if (!recordingID || !exportAvailable) {
    if (!exportAvailable) setExportAvailability(false);
    else target.replaceChildren();
    return;
  }
  try {
    const result = await api(`/api/recordings/${encodeURIComponent(recordingID)}/exports`);
    if (result.available === false) {
      setExportAvailability(false);
      return;
    }
    setExportAvailability(true);
    target.replaceChildren();
    const jobs = (result.items || []).slice(0, 100);
    for (const job of jobs) target.append(renderExportJob(job));
    if (!jobs.length) target.append(node('li', 'No exports for this recording.', 'muted'));
  } catch (error) {
    if (error.status === 501) {
      setExportAvailability(false);
      return;
    }
    target.replaceChildren(node('li', `Export jobs unavailable: ${error.message}`, 'muted'));
  }
}

function startExportPolling(jobID, recordingID) {
  if (!jobID || exportPollers.has(jobID)) return;
  const poller = { canceled: false };
  exportPollers.set(jobID, poller);
  (async () => {
    try {
      for (let attempt = 0; attempt < 60 && !poller.canceled; attempt += 1) {
        await new Promise((resolve) => window.setTimeout(resolve, 2000));
        if (poller.canceled) break;
        const job = await api(`/api/exports/${encodeURIComponent(jobID)}`);
        if (terminalExportState(job.state)) {
          if (selectedRecordingID === recordingID) {
            await loadExportJobs(recordingID);
            message.textContent = `Export ${job.state}.`;
          }
          return;
        }
      }
      if (!poller.canceled && selectedRecordingID === recordingID) {
        message.textContent = 'Export is still running. Refresh the export list to check again.';
      }
    } catch (error) {
      if (!poller.canceled && selectedRecordingID === recordingID) message.textContent = `Export status unavailable: ${error.message}`;
    } finally {
      exportPollers.delete(jobID);
    }
  })();
}

async function createExport() {
  if (!selectedRecordingID || !exportAvailable || selectedRecordingState === 'recording') return;
  const button = document.querySelector('#create-export');
  button.disabled = true;
  try {
    const job = await api(`/api/recordings/${encodeURIComponent(selectedRecordingID)}/exports`, {
      method: 'POST', body: JSON.stringify({ format: 'mkv' }),
    });
    message.textContent = 'MKV remux export queued.';
    await loadExportJobs(selectedRecordingID);
    startExportPolling(job.id, selectedRecordingID);
  } catch (error) {
    if (error.status === 501) setExportAvailability(false);
    message.textContent = error.message;
  } finally {
    button.disabled = !exportAvailable || selectedRecordingState === 'recording';
  }
}

async function verifyRecording(id) {
  try {
    const job = await api(`/api/recordings/${encodeURIComponent(id)}/integrity/verify`, { method: 'POST', body: '{}' });
    const jobID = job.job_id || job.id;
    message.textContent = jobID ? 'Integrity verification started.' : 'Integrity verification accepted.';
    if (!jobID) return;
    const integrityTarget = document.querySelector('#detail-integrity');
    integrityTarget.querySelectorAll('[data-integrity-job]').forEach((element) => element.remove());
    const cancelButton = actionButton('Cancel verification', async () => {
      cancelButton.disabled = true;
      try {
        await api(`/api/integrity/jobs/${encodeURIComponent(jobID)}/cancel`, { method: 'POST', body: '{}' });
        message.textContent = 'Integrity verification cancellation requested.';
      } catch (error) {
        cancelButton.disabled = false;
        message.textContent = error.message;
      }
    });
    cancelButton.dataset.integrityJob = jobID;
    const progress = node('p', `Verification job ${jobID} is queued or running.`, 'muted');
    progress.dataset.integrityJob = jobID;
    integrityTarget.append(progress, cancelButton);
    for (let attempt = 0; attempt < 120; attempt += 1) {
      await new Promise((resolve) => window.setTimeout(resolve, 1000));
      const status = await api(`/api/integrity/jobs/${encodeURIComponent(jobID)}`);
      const current = status.job || status;
      if (['completed', 'failed', 'canceled'].includes(current.state)) {
        integrityTarget.querySelectorAll('[data-integrity-job]').forEach((element) => element.remove());
        message.textContent = `Integrity verification ${current.state}.`;
        await Promise.all([loadRecordingIntegrity(id), refreshRecordings(), refreshDashboard()]);
        return;
      }
    }
    message.textContent = 'Verification is still running; refresh the recording later for its result.';
  } catch (error) { message.textContent = error.message; }
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

function renderPairs(target, pairs) {
  target.replaceChildren();
  for (const [label, value] of pairs) {
    const row = node('div', null, 'summary-item');
    row.append(node('span', label, 'muted'), node('strong', value));
    target.append(row);
  }
}

function renderShortList(target, items, renderItem, emptyMessage) {
  target.replaceChildren();
  for (const item of items || []) target.append(renderItem(item));
  if (!target.childElementCount) target.append(node('li', emptyMessage, 'muted'));
}

async function refreshDashboard() {
  const [dashboard, storage] = await Promise.all([api('/api/dashboard'), api('/api/system/storage')]);
  if (typeof dashboard.export_available === 'boolean') setExportAvailability(dashboard.export_available);
  const cards = document.querySelector('#dashboard-cards');
  cards.replaceChildren(
    metricCard('Active', dashboard.active_recordings_count),
    metricCard('Completed · 24h', dashboard.completed_last_24h),
    metricCard('Interrupted · 24h', dashboard.interrupted_last_24h),
    metricCard('Recordings', dashboard.recordings_total),
    metricCard('Segments', dashboard.segments_total),
    metricCard('Gaps', dashboard.gaps_total),
    metricCard('Archive size', formatBytes(dashboard.archive_bytes)),
    metricCard('Adapters ready', `${dashboard.adapters?.ready ?? 0} / ${dashboard.adapters?.total ?? 0}`),
    metricCard('Integrity verified', dashboard.integrity?.verified ?? 0),
    metricCard('Integrity degraded', dashboard.integrity?.degraded ?? 0),
    metricCard('Integrity failed', dashboard.integrity?.failed ?? 0),
    metricCard('Adapters unavailable', dashboard.adapters?.unavailable ?? 0),
  );
  renderShortList(document.querySelector('#dashboard-active'), dashboard.active_recordings, (item) => {
    const li = node('li', null);
    li.append(node('span', `${item.title || item.id} · ${item.state}`), actionButton('Open', () => showRecordingDetail(item.id)));
    return li;
  }, 'No active recordings.');
  renderShortList(document.querySelector('#dashboard-recent'), dashboard.recent_recordings, (item) => {
    const li = node('li', null);
    li.append(node('span', `${item.title || item.id} · ${item.state}`), actionButton('Details', () => showRecordingDetail(item.id)));
    return li;
  }, 'No recordings yet.');
  renderPairs(document.querySelector('#dashboard-storage'), [
    ['Filesystem used', formatBytes(storage.filesystem_used_bytes)],
    ['Filesystem free', formatBytes(storage.filesystem_available_bytes)],
    ['Archive', formatBytes(storage.recordings_bytes)],
    ['Media objects', storage.segment_count + storage.init_segment_count],
  ]);
}

async function refreshSystem() {
  const [storage, info] = await Promise.all([api('/api/system/storage'), api('/api/system/info')]);
  if (typeof info.export_available === 'boolean') setExportAvailability(info.export_available);
  renderPairs(document.querySelector('#system-storage'), [
    ['Archive root', storage.archive_root || 'recordings/'],
    ['Filesystem total', formatBytes(storage.filesystem_total_bytes)],
    ['Filesystem used', formatBytes(storage.filesystem_used_bytes)],
    ['Available', formatBytes(storage.filesystem_available_bytes)],
    ['Recording files', formatBytes(storage.recordings_bytes)],
    ['Recordings', storage.recording_count], ['Segments', storage.segment_count],
    ['Init segments', storage.init_segment_count], ['Manifests', storage.manifest_count],
  ]);
  renderPairs(document.querySelector('#system-info'), [
    ['Version', info.version || 'unknown'], ['Commit', info.commit || 'unknown'],
    ['Go', info.go_version || 'unknown'], ['Platform', `${info.goos || ''}/${info.goarch || ''}`],
    ['Started', formatDate(info.started_at)], ['Uptime', formatDuration(info.uptime_seconds)],
  ]);
  await loadSystemSettings();
  await previewRetention();
  if (selectedRecordingID && !document.querySelector('#recording-detail').hidden) await loadExportJobs(selectedRecordingID);
}

function applyTheme(theme) {
  if (['system', 'light', 'dark'].includes(theme)) document.documentElement.dataset.theme = theme;
}

function showRestartRequirements(values) {
  const note = document.querySelector('#settings-restart-note');
  const restart = Array.isArray(values) ? values : [];
  note.textContent = restart.includes('integrity.concurrency')
    ? 'The integrity worker count is saved and takes effect after the server restarts.'
    : restart.length ? `Restart required: ${restart.join(', ')}` : 'No restart is required for the current settings.';
}

async function loadSystemSettings() {
  const form = document.querySelector('#system-settings-form');
  const save = document.querySelector('#save-system-settings');
  try {
    const result = await api('/api/settings');
    const settings = result.settings || {};
    const theme = settings.ui?.theme;
    const concurrency = settings.integrity?.concurrency;
    const retention = settings.retention || {};
    if (typeof theme === 'string') {
      form.elements.theme.value = theme;
      applyTheme(theme);
    }
    if (Number.isInteger(concurrency)) form.elements.concurrency.value = String(concurrency);
    if (typeof retention.enabled === 'boolean') {
      retentionEnabled = retention.enabled;
      form.elements.retention_enabled.checked = retention.enabled;
    }
    if (Number.isInteger(retention.completed_after_days)) form.elements.retention_completed_after_days.value = String(retention.completed_after_days);
    updateRetentionControls();
    showRestartRequirements(result.restart_required);
    save.disabled = !(typeof theme === 'string' && Number.isInteger(concurrency) && typeof retention.enabled === 'boolean' && Number.isInteger(retention.completed_after_days));
  } catch (error) {
    retentionEnabled = false;
    updateRetentionControls();
    save.disabled = true;
    document.querySelector('#settings-restart-note').textContent = `System settings are unavailable: ${error.message}`;
  }
}

function updateRetentionControls() {
  document.querySelector('#run-retention').disabled = !retentionEnabled;
}

async function previewRetention() {
  const status = document.querySelector('#retention-status');
  const target = document.querySelector('#retention-candidates');
  target.replaceChildren();
  try {
    const result = await api('/api/retention/candidates');
    retentionEnabled = result.enabled === true;
    updateRetentionControls();
    const count = Number.isInteger(result.candidate_count) ? result.candidate_count : 0;
    status.textContent = retentionEnabled
      ? `${count} completed recordings qualify for cleanup. Preview only; at most 100 are deleted per pass.`
      : `Automatic cleanup is OFF. ${count} completed recordings meet the age rule; preview does not delete them.`;
    for (const item of (Array.isArray(result.candidates) ? result.candidates : [])) {
      target.append(node('li', `${item.id} · completed ${formatDate(item.stopped_at)}`));
    }
    if (count > (result.candidates || []).length) {
      target.append(node('li', `Showing the first ${(result.candidates || []).length} of ${count} candidates.`));
    }
  } catch (error) {
    retentionEnabled = false;
    updateRetentionControls();
    status.textContent = `Retention preview is unavailable: ${error.message}`;
  }
}

async function runRetentionNow() {
  const button = document.querySelector('#run-retention');
  if (!retentionEnabled) return;
  button.disabled = true;
  try {
    const result = await api('/api/retention/run', { method: 'POST', body: '{}' });
    message.textContent = `Retention pass deleted ${result.deleted_count || 0} recording(s).`;
    await previewRetention();
    await refreshDashboard();
  } catch (error) {
    document.querySelector('#retention-status').textContent = `Retention cleanup did not complete: ${error.message}`;
  } finally {
    updateRetentionControls();
  }
}

async function refreshWorkflows() {
  const response = await api('/api/resolve-workflows?limit=100');
  const items = Array.isArray(response) ? response : (response.items || []);
  renderShortList(document.querySelector('#workflow-list'), items, (item) => {
    const li = node('li', null, 'workflow-row');
    const resource = item.resource ? `${item.resource.resource_type || item.resource.type || ''} ${item.resource.resource_id || item.resource.id || ''}`.trim() : 'Resource not identified';
    const challenge = item.challenge ? ` · ${item.challenge.field_count} fields${item.challenge.has_secret_fields ? ' · secret prompt' : ''}` : '';
    const progress = item.in_progress ? ' · in progress' : '';
    li.append(node('span', `${item.adapter_id} · ${item.state}${progress} · ${resource}${challenge} · updated ${formatDate(item.updated_at)} · expires ${formatDate(item.expires_at)}`));
    const actions = node('span', null, 'button-row');
    actions.append(actionButton('Resume', async () => {
      try {
        const detail = await api(`/api/resolve-workflows/${encodeURIComponent(item.workflow_id)}`);
        await showWorkflow(detail);
        showSection('new-section');
      } catch (error) { message.textContent = error.message; await refreshWorkflows(); }
    }));
    actions.append(actionButton('Cancel', async () => {
      try {
        await api(`/api/resolve-workflows/${encodeURIComponent(item.workflow_id)}`, { method: 'DELETE' });
        if (activeWorkflow?.workflow_id === item.workflow_id) finishWorkflowUI();
        await refreshWorkflows();
      } catch (error) { message.textContent = error.message; }
    }, 'danger'));
    li.append(actions);
    return li;
  }, 'No active workflows.');
}

function selectedAdapterDescriptor() {
  const item = currentAdapters.find((candidate) => candidate.descriptor?.id === adapterSelect.value);
  return item?.descriptor || null;
}

async function browseResources(search, append = false) {
  const descriptor = selectedAdapterDescriptor();
  const capability = descriptor?.capabilities || [];
  const output = document.querySelector('#resource-results');
  const messageTarget = document.querySelector('#resource-capability');
  if (!descriptor || !capability.includes('resource_browse')) {
    messageTarget.textContent = 'Resource browsing is unavailable for this adapter.';
    document.querySelector('#resource-next').hidden = true;
    if (!append) output.replaceChildren();
    return;
  }
  const form = document.querySelector('#resource-search');
  const values = queryFromForm(form);
  const params = new URLSearchParams({ limit: '50' });
  if (values.resource_type) params.set('resource_type', values.resource_type);
  if (search && values.q) params.set('q', values.q);
  if (append && resourceCursor) params.set('cursor', resourceCursor);
  if (resourceParentHint) params.set('parent', encodedResource(resourceParentHint));
  const suffix = search ? `/search?${params.toString()}` : `?${params.toString()}`;
  messageTarget.textContent = 'Loading resources…';
  try {
    const result = await api(`/api/adapters/${encodeURIComponent(descriptor.id)}/resources${suffix}`);
    const items = result.items || [];
    if (!append) output.replaceChildren();
    for (const item of items) {
      const li = node('li', null);
      const title = item.display_name || item.resource_id || item.resource?.resource_id || 'Unnamed resource';
      const type = item.resource_type || item.resource?.resource_type || '';
      const id = item.resource_id || item.resource?.resource_id || '';
      li.append(node('span', `${title} · ${type} · ${id}`));
      li.append(actionButton('Use as input hint', () => {
        selectedResourceHint = {
          resource_type: type,
          resource_id: id,
          ...(item.parent ? { parent: item.parent } : {}),
        };
        messageTarget.textContent = `Selected resource hint: ${type} ${id}`;
      }));
      li.append(actionButton('Browse children', () => {
        resourceParentHint = {
          resource_type: type,
          resource_id: id,
          ...(item.parent ? { parent: item.parent } : {}),
        };
        resourceCursor = '';
        browseResources(false).catch((error) => { messageTarget.textContent = error.message; });
      }));
      output.append(li);
    }
    if (!output.childElementCount) output.append(node('li', 'No resources found.', 'muted'));
    resourceCursor = result.next_cursor || '';
    document.querySelector('#resource-next').hidden = !resourceCursor;
    messageTarget.textContent = `${items.length} resource${items.length === 1 ? '' : 's'} returned.`;
  } catch (error) {
    if (error.status === 501) {
      messageTarget.textContent = 'Resource browsing is not supported by this adapter.';
      document.querySelector('#resource-next').hidden = true;
      if (!append) output.replaceChildren();
      return;
    }
    messageTarget.textContent = error.message;
  }
}

async function refreshActivity() {
  await api('/api/notifications/sync', { method: 'POST', body: '{}' });
  const [notifications, audit, history] = await Promise.all([
    api('/api/notifications?unread=true&limit=50'), api('/api/audit?limit=50'), api('/api/workflow-history?limit=50'),
  ]);
  const notificationItems = Array.isArray(notifications) ? notifications : (notifications.items || []);
  renderShortList(document.querySelector('#notifications-list'), notificationItems, (item) => {
    const li = node('li', null);
    li.append(node('span', `${formatDate(item.at || item.created_at)} · ${item.title || item.type || item.message || 'Notification'}${item.read ? ' · read' : ' · unread'}`));
    if (!item.read && item.id) li.append(actionButton('Mark read', async () => {
      try { await api(`/api/notifications/${encodeURIComponent(item.id)}/read`, { method: 'POST', body: '{}' }); await refreshActivity(); }
      catch (error) { message.textContent = error.message; }
    }));
    return li;
  }, 'No unread notifications.');
  const auditItems = Array.isArray(audit) ? audit : (audit.items || []);
  renderShortList(document.querySelector('#audit-list'), auditItems, (item) => node('li', `${formatDate(item.at)} · ${item.type || 'event'}${item.object_id ? ` · ${item.object_id}` : ''}`), 'No audit events.');
  const historyItems = Array.isArray(history) ? history : (history.items || []);
  renderShortList(document.querySelector('#workflow-history-list'), historyItems, (item) => node('li', `${formatDate(item.updated_at || item.created_at)} · ${item.adapter_id} · ${item.state} · ${item.workflow_id}`), 'No workflow history.');
  await refreshApplicationLogs(false);
}

function ensureApplicationLogPanel() {
  if (document.querySelector('#application-logs')) return;
  const section = document.querySelector('#activity-section');
  const panel = node('article', null, 'panel');
  panel.append(node('h3', 'Application logs'));
  const form = node('form');
  form.id = 'application-log-filters';
  const levelLabel = node('label', 'Level');
  const level = node('select');
  level.name = 'level';
  for (const [value, label] of [['', 'Any level'], ['debug', 'Debug'], ['info', 'Info'], ['warn', 'Warning'], ['error', 'Error']]) {
    const option = node('option', label);
    option.value = value;
    level.append(option);
  }
  levelLabel.append(level);
  const componentLabel = node('label', 'Component');
  const component = node('input');
  component.name = 'component';
  component.maxLength = 64;
  componentLabel.append(component);
  const queryLabel = node('label', 'Search logs');
  const query = node('input');
  query.name = 'q';
  query.type = 'search';
  query.maxLength = 128;
  queryLabel.append(query);
  const apply = node('button', 'Apply');
  apply.type = 'submit';
  form.append(levelLabel, componentLabel, queryLabel, apply);
  const list = node('ul', null, 'plain-list');
  list.id = 'application-logs';
  const next = node('button', 'Older entries');
  next.type = 'button';
  next.id = 'application-logs-next';
  next.hidden = true;
  panel.append(form, list, next);
  const globalSearchPanel = document.querySelector('#global-search').closest('.panel');
  section.insertBefore(panel, globalSearchPanel);
  form.addEventListener('submit', (event) => {
    event.preventDefault();
    refreshApplicationLogs(false).catch((error) => { message.textContent = error.message; });
  });
  next.addEventListener('click', () => refreshApplicationLogs(true).catch((error) => { message.textContent = error.message; }));
}

async function refreshApplicationLogs(append) {
  ensureApplicationLogPanel();
  const target = document.querySelector('#application-logs');
  const form = document.querySelector('#application-log-filters');
  if (!append) applicationLogCursor = '';
  if (append && !applicationLogCursor) return;
  const params = new URLSearchParams();
  for (const key of ['level', 'component', 'q']) {
    const value = String(form.elements[key].value || '').trim();
    if (value) params.set(key, value);
  }
  params.set('limit', '100');
  if (append && applicationLogCursor) params.set('cursor', applicationLogCursor);
  try {
    const result = await api(`/api/logs?${params.toString()}`);
    if (!append) target.replaceChildren();
    const entries = result.items || [];
    for (const entry of entries) {
      target.append(node('li', `${formatDate(entry.at)} · ${entry.level} · ${entry.component} · ${entry.message}`));
    }
    if (!append && !entries.length) target.append(node('li', 'No application log entries recorded yet.', 'muted'));
    applicationLogCursor = result.next_cursor || '';
    document.querySelector('#application-logs-next').hidden = !applicationLogCursor;
  } catch (error) {
    if (!append) target.replaceChildren();
    target.append(node('li', `Application logs are unavailable: ${error.message}`, 'muted'));
    applicationLogCursor = '';
    document.querySelector('#application-logs-next').hidden = true;
  }
}

async function globalSearch(query) {
  const target = document.querySelector('#global-search-results');
  target.replaceChildren();
  const params = new URLSearchParams({ q: query, limit: '50' });
  const response = await api(`/api/search?${params.toString()}`);
  for (const item of response.results || []) {
    const type = item.type || 'result';
    const title = type === 'workflow' ? `${item.adapter_id || 'Adapter'} workflow ${item.id || ''}`
      : type === 'resource' ? `${item.resource_type || 'Resource'} · ${item.display_name || item.resource_id || ''}`
        : item.title || item.name || item.display_name || item.adapter_id || item.id || 'Untitled';
    const li = node('li', null);
    li.append(node('span', `${type} · ${title}${item.state ? ` · ${item.state}` : ''}`));
    if (type === 'recording' && item.id) li.append(actionButton('Open', () => showRecordingDetail(item.id)));
    if (type === 'workflow' && item.id) li.append(actionButton('Resume', async () => {
      try { const detail = await api(`/api/resolve-workflows/${encodeURIComponent(item.id)}`); await showWorkflow(detail); showSection('new-section'); }
      catch (error) { message.textContent = error.message; }
    }));
    if (type === 'adapter' && item.id) li.append(actionButton('Configure', async () => {
      try {
        if (activeWorkflow && activeWorkflow.adapter_id !== item.id) throw new Error('Adapter selection is locked while a workflow is active.');
        adapterSelect.value = item.id;
        await loadSchema(item.id);
        showSection('adapters-section');
      }
      catch (error) { message.textContent = error.message; }
    }));
    if (type === 'resource' && item.adapter_id && item.resource_type && item.resource_id) li.append(actionButton('Use as input hint', async () => {
      if (activeWorkflow && activeWorkflow.adapter_id !== item.adapter_id) {
        message.textContent = 'Adapter selection is locked while a workflow is active.';
        return;
      }
      adapterSelect.value = item.adapter_id;
      try { await loadSchema(item.adapter_id); }
      catch (error) { message.textContent = error.message; return; }
      selectedResourceHint = { resource_type: item.resource_type, resource_id: item.resource_id };
      showSection('new-section');
      message.textContent = `Selected resource hint: ${item.resource_type} ${item.resource_id}`;
    }));
    target.append(li);
  }
  if (!target.childElementCount) target.append(node('li', 'No matching results.', 'muted'));
}

async function startProductRefresh() {
  document.querySelector('#app').hidden = false;
  const results = await Promise.allSettled([
    loadAdapters(), refreshRecordings(), refreshDashboard(), refreshWorkflows(), refreshActivity(), refreshSystem(),
  ]);
  if (results.some((result) => result.status === 'rejected')) {
    message.textContent = 'Some management data could not be loaded. Use the section refresh controls to retry.';
  }
  if (!productRefreshStarted) {
    productRefreshStarted = true;
    window.setInterval(() => {
      Promise.allSettled([refreshDashboard(), refreshRecordings(), refreshWorkflows()]);
    }, 15000);
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
  if (progress.adapter_id) adapterSelect.value = progress.adapter_id;
  adapterSelect.disabled = true;
  document.querySelector('#start button[type="submit"]').disabled = true;
  document.querySelector('#challenge-panel').hidden = false;
  renderPrompt(progress);
  await loadSchema(progress.adapter_id);
  if (progress.resource) await loadConfiguration(progress.adapter_id, progress.resource);
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
    selectedResourceHint = null;
    await Promise.all([refreshRecordings(), refreshDashboard(), refreshWorkflows()]);
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
    await refreshWorkflows();
    message.textContent = 'Workflow canceled.';
  } catch (error) {
    const workflowID = activeWorkflow?.workflow_id;
    if (error.status === 404 && workflowID) {
      finishWorkflowUI();
      await loadSchema();
      await refreshWorkflows();
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
        resource: selectedResourceHint || undefined,
      }),
    });
    if (result.workflow_id) {
      await showWorkflow(result);
      message.textContent = 'Adapter needs additional information.';
      return;
    }
    message.textContent = `Started ${result.id}`;
    selectedResourceHint = null;
    event.currentTarget.elements.title.value = '';
    await Promise.all([refreshRecordings(), refreshDashboard(), refreshWorkflows()]);
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

adapterSelect.addEventListener('change', () => {
  selectedResourceHint = null;
  resourceParentHint = null;
  resourceCursor = '';
  document.querySelector('#resource-results').replaceChildren();
  document.querySelector('#resource-capability').textContent = '';
  loadSchema().catch((error) => { message.textContent = error.message; });
});

document.querySelectorAll('.tabs [data-section]').forEach((button) => button.addEventListener('click', () => {
  showSection(button.dataset.section);
  if (button.dataset.section === 'dashboard-section') refreshDashboard().catch((error) => { message.textContent = error.message; });
  if (button.dataset.section === 'adapters-section') loadAdapters().catch((error) => { message.textContent = error.message; });
  if (button.dataset.section === 'activity-section') refreshActivity().catch((error) => { message.textContent = error.message; });
  if (button.dataset.section === 'system-section') refreshSystem().catch((error) => { message.textContent = error.message; });
  if (button.dataset.section === 'new-section') refreshWorkflows().catch((error) => { message.textContent = error.message; });
}));

document.querySelector('#recording-filters').addEventListener('submit', async (event) => {
  event.preventDefault();
  recordingFilters = queryFromForm(event.currentTarget);
  recordingCursor = '';
  try { await refreshRecordings(); } catch (error) { message.textContent = error.message; }
});
document.querySelector('#recordings-next').addEventListener('click', () => refreshRecordings(true).catch((error) => { message.textContent = error.message; }));
document.querySelector('#refresh-dashboard').addEventListener('click', () => refreshDashboard().catch((error) => { message.textContent = error.message; }));
document.querySelector('#refresh-adapters').addEventListener('click', () => loadAdapters().catch((error) => { message.textContent = error.message; }));
document.querySelector('#refresh-workflows').addEventListener('click', () => refreshWorkflows().catch((error) => { message.textContent = error.message; }));
document.querySelector('#close-detail').addEventListener('click', closeRecordingDetail);
document.querySelector('#create-export').addEventListener('click', createExport);
document.querySelector('#refresh-exports').addEventListener('click', () => {
  if (selectedRecordingID) loadExportJobs(selectedRecordingID).catch((error) => { message.textContent = error.message; });
});
document.querySelector('#verify-recording').addEventListener('click', () => {
  if (selectedRecordingID) verifyRecording(selectedRecordingID);
});
document.querySelector('#detail-tags').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (!selectedRecordingID) return;
  const values = event.currentTarget.elements.tags.value.split(',').map((tag) => tag.trim()).filter(Boolean);
  try {
    const result = await api(`/api/recordings/${encodeURIComponent(selectedRecordingID)}/tags`, { method: 'PUT', body: JSON.stringify({ tags: values }) });
    event.currentTarget.elements.tags.value = (result.tags || []).join(', ');
    message.textContent = 'Tags saved.';
    await refreshRecordings();
  } catch (error) { message.textContent = error.message; }
});
document.querySelector('#resource-search').addEventListener('submit', (event) => {
  event.preventDefault();
  resourceCursor = '';
  browseResources(true).catch((error) => { message.textContent = error.message; });
});
document.querySelector('#resource-list').addEventListener('click', () => {
  resourceCursor = '';
  resourceParentHint = null;
  document.querySelector('#resource-search').elements.q.value = '';
  browseResources(false).catch((error) => { message.textContent = error.message; });
});
document.querySelector('#resource-next').addEventListener('click', () => browseResources(document.querySelector('#resource-search').elements.q.value.trim() !== '', true));
document.querySelector('#global-search').addEventListener('submit', async (event) => {
  event.preventDefault();
  try { await globalSearch(event.currentTarget.elements.q.value.trim()); } catch (error) { message.textContent = error.message; }
});
document.querySelector('#refresh-activity').addEventListener('click', () => refreshActivity().catch((error) => { message.textContent = error.message; }));
document.querySelector('#notifications-read-all').addEventListener('click', async () => {
  try { await api('/api/notifications/read-all', { method: 'POST', body: '{}' }); await refreshActivity(); }
  catch (error) { message.textContent = error.message; }
});
document.querySelector('#refresh-system').addEventListener('click', () => refreshSystem().catch((error) => { message.textContent = error.message; }));
document.querySelector('#preview-retention').addEventListener('click', () => previewRetention());
document.querySelector('#run-retention').addEventListener('click', () => runRetentionNow());
document.querySelector('#system-settings-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const save = document.querySelector('#save-system-settings');
  save.disabled = true;
  try {
    const result = await api('/api/settings', {
      method: 'PUT',
      body: JSON.stringify({
        ui: { theme: form.elements.theme.value },
        integrity: { concurrency: Number(form.elements.concurrency.value) },
        retention: {
          enabled: form.elements.retention_enabled.checked,
          completed_after_days: Number(form.elements.retention_completed_after_days.value),
        },
      }),
    });
    const settings = result.settings || {};
    if (typeof settings.ui?.theme === 'string') applyTheme(settings.ui.theme);
    if (typeof settings.retention?.enabled === 'boolean') retentionEnabled = settings.retention.enabled;
    updateRetentionControls();
    showRestartRequirements(result.restart_required);
    message.textContent = 'System settings saved.';
    await previewRetention();
    await refreshDashboard();
  } catch (error) {
    message.textContent = error.message;
  } finally {
    save.disabled = false;
  }
});

ensureApplicationLogPanel();
loadAuthentication().catch((error) => {
  document.querySelector('#connection-state').textContent = 'Server unavailable';
  message.textContent = error.message;
});
