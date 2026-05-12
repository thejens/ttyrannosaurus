const BASE = 'http://127.0.0.1:7071';

async function loadConfig() {
  try {
    const resp = await fetch(`${BASE}/api/config`, { signal: AbortSignal.timeout(2000) });
    if (!resp.ok) throw new Error(resp.statusText);
    return await resp.json();
  } catch (e) {
    document.getElementById('daemon-status').innerHTML =
      `<div class="offline">⚠ Daemon not running — start ttyrannosaurus to edit settings.</div>`;
    return null;
  }
}

async function save() {
  const status = document.getElementById('status');
  status.textContent = 'Saving…';
  status.className = 'status';

  const tmuxEnabled = document.querySelector('input[name="tmuxEnabled"]:checked')?.value || 'auto';
  const socket      = document.getElementById('tmux-socket').value.trim();
  const extraRaw    = document.getElementById('tmux-extra').value.trim();
  const extraArgs   = extraRaw ? extraRaw.split(/\s+/) : [];
  const tmux        = { enabled: tmuxEnabled };
  if (socket) tmux.socket = socket;
  if (extraArgs.length) tmux.extraArgs = extraArgs;

  try {
    const resp = await fetch(`${BASE}/api/config`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tmux }),
      signal: AbortSignal.timeout(3000),
    });
    if (!resp.ok) throw new Error(await resp.text());
    status.textContent = 'Saved!';
    status.className = 'status ok';
    setTimeout(() => { status.textContent = ''; }, 2500);
  } catch (e) {
    status.textContent = 'Error: ' + e.message;
    status.className = 'status err';
  }
}

// ── Sidebar mode ──────────────────────────────────────────────────────────

async function loadSidebarMode() {
  const { sidebarMode = 'keep-open' } = await chrome.storage.sync.get('sidebarMode');
  const radio = document.querySelector(`input[name="sidebarMode"][value="${sidebarMode}"]`);
  if (radio) radio.checked = true;
}

document.querySelectorAll('input[name="sidebarMode"]').forEach(radio => {
  radio.addEventListener('change', () => {
    if (radio.checked) chrome.storage.sync.set({ sidebarMode: radio.value });
  });
});

// ── AI naming ─────────────────────────────────────────────────────────────

function applyAiNamingFieldState() {
  const enabled = document.getElementById('ai-naming-enabled').checked;
  document.getElementById('ai-naming-interval').disabled = !enabled;
}

function loadAiNaming(cfg) {
  const v = cfg?.aiNaming ?? {};
  document.getElementById('ai-naming-enabled').checked = v.enabled !== false;
  document.getElementById('ai-naming-interval').value  = v.intervalMinutes ?? 1;
  applyAiNamingFieldState();
}

document.getElementById('ai-naming-enabled').addEventListener('change', () => {
  applyAiNamingFieldState();
  saveAiNaming();
});
document.getElementById('ai-naming-interval').addEventListener('change', saveAiNaming);

async function saveAiNaming() {
  const enabled         = document.getElementById('ai-naming-enabled').checked;
  const intervalMinutes = Math.max(1, parseInt(document.getElementById('ai-naming-interval').value, 10) || 1);
  await chrome.storage.sync.set({ aiNaming: { enabled, intervalMinutes } });
}

// ── Tmux config ───────────────────────────────────────────────────────────

function applyTmuxFieldState() {
  const disabled = document.querySelector('input[name="tmuxEnabled"]:checked')?.value === 'false';
  document.getElementById('tmux-socket').disabled = disabled;
  document.getElementById('tmux-extra').disabled  = disabled;
}

function loadTmuxConfig(cfg) {
  const tmux  = cfg?.tmux || {};
  const val   = tmux.enabled || 'auto';
  const radio = document.querySelector(`input[name="tmuxEnabled"][value="${val}"]`);
  if (radio) radio.checked = true;
  document.getElementById('tmux-socket').value = tmux.socket    || '';
  document.getElementById('tmux-extra').value  = (tmux.extraArgs || []).join(' ');
  applyTmuxFieldState();
}

document.querySelectorAll('input[name="tmuxEnabled"]').forEach(radio => {
  radio.addEventListener('change', applyTmuxFieldState);
});

// ── Theme ─────────────────────────────────────────────────────────────────

const ANSI_NAMES = [
  'black','red','green','yellow','blue','magenta','cyan','white',
  'bright black','bright red','bright green','bright yellow',
  'bright blue','bright magenta','bright cyan','bright white',
];

async function loadTheme() {
  try {
    const resp = await fetch(`${BASE}/api/theme`, { signal: AbortSignal.timeout(2000) });
    if (!resp.ok) return;
    const theme = await resp.json();
    renderSwatches(theme);
  } catch (_) {}
}

function renderSwatches(theme) {
  const container = document.getElementById('color-swatches');
  const special = [
    { label: 'bg',     color: theme.background },
    { label: 'fg',     color: theme.foreground },
    { label: 'cursor', color: theme.cursor },
  ].filter(s => s.color);

  const ansiSwatches = (theme.colors || []).map((c, i) => ({ label: ANSI_NAMES[i], color: c })).filter(s => s.color);

  if (!special.length && !ansiSwatches.length) {
    container.style.display = 'none';
    return;
  }

  container.style.display = 'flex';
  container.innerHTML = [
    ...special.map(s => swatchHTML(s.label, s.color, true)),
    special.length ? '<div class="swatch-sep"></div>' : '',
    ...ansiSwatches.slice(0, 8).map(s => swatchHTML(s.label, s.color)),
    ansiSwatches.length > 8 ? '<div class="swatch-sep"></div>' : '',
    ...ansiSwatches.slice(8).map(s => swatchHTML(s.label, s.color)),
  ].join('');
}

function swatchHTML(label, color, wide = false) {
  if (!color) return '';
  return `<div class="swatch-group">
    <div class="swatch${wide ? ' swatch-wide' : ''}" style="background:${color}" title="${color}"></div>
    <span>${label}</span>
  </div>`;
}

async function applyTheme() {
  const text   = document.getElementById('theme-config').value.trim();
  const status = document.getElementById('theme-status');
  status.textContent = 'Applying…';
  status.className = 'status';
  try {
    const resp = await fetch(`${BASE}/api/theme`, {
      method: 'PUT',
      headers: { 'Content-Type': 'text/plain' },
      body: text,
      signal: AbortSignal.timeout(3000),
    });
    if (!resp.ok) throw new Error(await resp.text());
    const theme = await resp.json();
    renderSwatches(theme);
    status.textContent = 'Applied! New tabs will use this theme.';
    status.className = 'status ok';
    setTimeout(() => { status.textContent = ''; }, 3000);
  } catch (e) {
    status.textContent = 'Error: ' + e.message;
    status.className = 'status err';
  }
}

document.getElementById('save-btn').addEventListener('click', save);
document.getElementById('apply-theme-btn').addEventListener('click', applyTheme);

(async () => {
  await loadSidebarMode();

  // AI naming settings live in chrome.storage.sync, not the daemon config.
  const { aiNaming } = await chrome.storage.sync.get('aiNaming');
  loadAiNaming({ aiNaming });

  const cfg = await loadConfig();
  if (!cfg) return;
  loadTmuxConfig(cfg);
  await loadTheme();
})();
