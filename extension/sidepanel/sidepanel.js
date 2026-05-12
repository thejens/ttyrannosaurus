// Force IPv4 — Chrome extensions sometimes try ::1 first and fail.
const BASE = 'http://127.0.0.1:7071';

// ── State ──────────────────────────────────────────────────────────────────

let sessions = [];
let tabMap = {};
let currentTabId = null;
let dragSourceId  = null;
let justDropped   = false; // suppress the click that fires after mouseup on a drag
let sessionOrder  = [];    // user-defined ordering: array of session IDs
let customNames = {}; // sessionId → user-set name (manual rename, never auto-overridden)
let aiNames    = {}; // sessionId → AI-generated name (updated periodically)

// Tracks sess.lastSeen at the time of the last naming attempt for each session.
// If lastSeen hasn't changed since, the session has been idle → skip renaming.
const lastNamedSeen = {};

let aiNamingEnabled   = true;
let aiNamingIntervalMs = 60_000;
let namingLoopTimer   = null;

// Keyed card elements — avoids full-container innerHTML replacement on each render.
const cardEls  = new Map(); // sessionId → div.session-card
const cardData = new Map(); // sessionId → last rendered cache key (for diffing)

// ── Bootstrap ──────────────────────────────────────────────────────────────

async function init() {
  const [activeTab] = await chrome.tabs.query({ active: true, currentWindow: true });
  currentTabId = activeTab?.id ?? null;

  // Load persisted names and AI naming settings.
  const stored = await chrome.storage.sync.get(['sessionNames', 'aiSessionNames', 'aiNaming', 'sessionOrder']);
  customNames  = stored.sessionNames   || {};
  aiNames      = stored.aiSessionNames || {};
  sessionOrder = stored.sessionOrder   || [];
  const aiCfg  = stored.aiNaming       || {};
  aiNamingEnabled    = aiCfg.enabled !== false;
  aiNamingIntervalMs = ((aiCfg.intervalMinutes || 1) * 60_000);

  chrome.storage.onChanged.addListener((changes, area) => {
    if (area !== 'sync') return;
    if ('sessionNames'   in changes) { customNames  = changes.sessionNames.newValue   || {}; scheduleRender(); }
    if ('aiSessionNames' in changes) { aiNames      = changes.aiSessionNames.newValue || {}; scheduleRender(); }
    if ('sessionOrder'   in changes) { sessionOrder = changes.sessionOrder.newValue   || []; scheduleRender(); }
    if ('aiNaming' in changes) {
      const v = changes.aiNaming.newValue || {};
      aiNamingEnabled    = v.enabled !== false;
      aiNamingIntervalMs = (v.intervalMinutes || 1) * 60_000;
      restartNamingLoop();
    }
  });

  restartNamingLoop();

  // Track the active tab for "this tab" highlighting — read-only, no tabMap writes.
  chrome.tabs.onActivated.addListener(({ tabId }) => {
    currentTabId = tabId;
    scheduleRender();
  });

  // tabMap is owned by background.js; receive it via port messages.
  connectBackground();

  // WebSocket for live session data from daemon.
  connectWS();

  // Header buttons — attached once.
  document.getElementById('new-btn').addEventListener('click', () => {
    const cwd = activeCwd();
    const url = cwd ? `${BASE}/s/tty/new?cwd=${encodeURIComponent(cwd)}` : `${BASE}/s/tty/new`;
    chrome.tabs.create({ url });
  });
  document.getElementById('settings-btn').addEventListener('click', () => {
    chrome.runtime.openOptionsPage();
  });

  // Event delegation on container — one listener handles all cards.
  initContainerEvents();
}

// ── Background port — tabMap push ─────────────────────────────────────────
// background.js is the single source of truth for which tabs show which sessions.
// We receive push updates via a persistent port rather than maintaining our own
// parallel tab event listeners.

function connectBackground() {
  const port = chrome.runtime.connect({ name: 'sidepanel' });
  port.onMessage.addListener(msg => {
    if (msg.type === 'tabMapUpdate') {
      tabMap = msg.tabMap;
      scheduleRender();
    }
  });
  // Service workers can be terminated; reconnect when the port drops.
  port.onDisconnect.addListener(() => setTimeout(connectBackground, 500));
}

// ── WebSocket connection to daemon (sidebar events) ────────────────────────

let reconnectDelay = 1000;

function connectWS() {
  const sock = new WebSocket(`ws://127.0.0.1:7071/api/ws`);
  const banner = document.getElementById('offline-banner');

  sock.onopen = () => {
    reconnectDelay = 1000;
    banner.classList.remove('show');
  };

  sock.onmessage = ({ data }) => {
    try {
      /** @type {import('../protocol.js').DaemonSidebarMessage} */
      const msg = JSON.parse(data);
      if (msg.type === 'sessions') {
        sessions = msg.sessions || [];
      } else if (msg.type === 'session:created') {
        if (!sessions.find(s => s.id === msg.session.id)) sessions.push(msg.session);
        // Schedule AI naming after a short delay so the session has some output.
        setTimeout(() => maybeAiName(msg.session.id), 3000);
      } else if (msg.type === 'session:updated') {
        const prev = sessions.find(s => s.id === msg.session.id);
        sessions = sessions.map(s => s.id === msg.session.id ? msg.session : s);
        // Also trigger when first becoming idle after being busy (command finished).
        const wasActive = prev?.meta?.status && prev.meta.status !== 'idle';
        if (wasActive && msg.session.meta?.status === 'idle') {
          maybeAiName(msg.session.id);
        }
      } else if (msg.type === 'session:killed') {
        sessions = sessions.filter(s => s.id !== msg.id);
        delete lastNamedSeen[msg.id];
      }
    } catch (_) {}
    scheduleRender();
  };

  sock.onerror = () => sock.close();

  sock.onclose = () => {
    banner.classList.add('show');
    reconnectDelay = Math.min(reconnectDelay * 1.5, 30000);
    setTimeout(connectWS, reconnectDelay);
  };
}

// ── Debounced render — collapses rapid tab-event bursts into one paint ─────

let renderTimer = null;
function scheduleRender() {
  clearTimeout(renderTimer);
  renderTimer = setTimeout(renderSessions, 30);
}

// ── Tab lookup ─────────────────────────────────────────────────────────────

// activeCwd returns the cwd of the terminal running in the currently focused
// tab, or '' if the active tab is not a terminal or has no cwd set.
function activeCwd() {
  const entry = Object.entries(tabMap).find(([id]) => parseInt(id) === currentTabId);
  if (!entry) return '';
  for (const path of (entry[1].paths || [])) {
    const sess = sessions.find(s => s.id === path || `${s.scheme}/${s.path}` === path);
    if (sess?.meta?.cwd) return sess.meta.cwd;
  }
  return '';
}

function findTabEntry(sess) {
  return Object.entries(tabMap).find(([, t]) =>
    t.paths && (t.paths.includes(sess.id) || t.paths.includes(`${sess.scheme}/${sess.path}`))
  ) || null;
}

function tabSessionCounts() {
  const counts = {};
  for (const s of sessions) {
    const entry = findTabEntry(s);
    if (!entry) continue;
    counts[entry[0]] = (counts[entry[0]] || 0) + 1;
  }
  return counts;
}

// ── Render — keyed updates, no full-container replacement ──────────────────

function renderSessions() {
  const container = document.getElementById('sessions');

  if (!sessions.length) {
    container.innerHTML = `<div class="empty">
      No sessions yet.<br>
      Press <strong>!</strong> in the address bar,<br>
      e.g. <strong>! claude</strong> or <strong>! ls -la</strong>.
    </div>`;
    cardEls.clear();
    cardData.clear();
    return;
  }

  // Remove empty placeholder if present.
  const emptyEl = container.querySelector('.empty');
  if (emptyEl) emptyEl.remove();

  const sorted = orderedSessions();
  const counts = tabSessionCounts();
  const sharedTabIds = new Set(Object.entries(counts).filter(([, n]) => n > 1).map(([id]) => id));

  // Remove cards whose sessions are gone.
  for (const [id, el] of cardEls) {
    if (!sessions.find(s => s.id === id)) {
      el.remove();
      cardEls.delete(id);
      cardData.delete(id);
    }
  }

  // Add/update each session card, maintaining sort order.
  let prevEl = null;
  for (const s of sorted) {
    const { inner, isActive } = computeCardContent(s, sharedTabIds);
    const cacheKey = inner + (isActive ? '|a' : '');

    let el = cardEls.get(s.id);
    if (!el) {
      el = document.createElement('div');
      el.className = 'session-card';
      el.dataset.id = s.id;
      el.draggable = true;
      el.innerHTML = inner;
      el.classList.toggle('active', isActive);
      container.append(el);
      cardEls.set(s.id, el);
      cardData.set(s.id, cacheKey);
    } else if (cardData.get(s.id) !== cacheKey && !el.querySelector('.rename-input')) {
      // Update inner content only — outer div stays in DOM, preserving scroll
      // position and avoiding a full-container repaint.
      // Skip while a rename <input> is active so we don't destroy it mid-edit.
      el.innerHTML = inner;
      el.classList.toggle('active', isActive);
      cardData.set(s.id, cacheKey);
    }

    // Freeze DOM ordering while a drag is active — the dragged card is
    // display:none so sibling comparisons are unreliable. The drop handler
    // calls scheduleRender() after setting dragSourceId = null, which runs
    // a clean reorder with the card restored to its new position.
    if (!dragSourceId) {
      if (prevEl) {
        if (el.previousElementSibling !== prevEl) prevEl.after(el);
      } else {
        if (el !== container.firstElementChild) container.prepend(el);
      }
    }
    prevEl = el;
  }
}

const SHELLS = new Set(['zsh', 'bash', 'fish', 'sh', 'ksh', 'dash', 'csh', 'tcsh']);

function computeCardContent(s, sharedTabIds) {
  const entry     = findTabEntry(s);
  const inTab     = !!entry;
  const tabId     = entry ? parseInt(entry[0]) : null;
  const isThisTab = inTab && tabId === currentTabId;
  const isShared  = inTab && sharedTabIds.has(String(tabId));
  const stateLabel = isThisTab ? 'this tab' : inTab ? 'in tab' : 'minimized';

  const m = s.meta || {};
  const status = m.status || '';

  const dotClass = s.dormant ? 'dormant'
    : s.alive ? `alive${status ? ' status-' + status : ''}`
    : 'dead';

  // Primary name: user override > OSC 2 window title > foreground program (if
  // not a shell, detected by detectLoop) > base command name
  const progIsShell = SHELLS.has(m.program);
  const baseCmd = s.command?.[0] ? s.command[0].split('/').pop() : '';
  const displayName = esc(customNames[s.id])
    || esc(aiNames[s.id])
    || m.name
    || esc(m.program && !progIsShell ? m.program : (baseCmd || 'terminal'));

  // CWD row — shown whenever OSC 7 has populated it; acts as the "prompt line"
  const cwd = m.cwd ? truncatePath(m.cwd, 36) : '';
  // Show program badge only when it's something other than the shell *and* it
  // isn't already obvious from the card title (avoid "claude  [claude]").
  const prog = m.program && !progIsShell && esc(m.program) !== displayName
    ? esc(m.program) : '';
  const cwdLine = cwd
    ? `${cwd}${prog ? ' <span class="sub-prog">' + prog + '</span>' : ''}`
    : prog || '';

  // Detail line: activity text + status-based icon/colour
  // Always show a "waiting" prompt even if no detail text.
  const detailText = m.detail || (status === 'waiting' ? 'Waiting for your input' : '');
  const detailIcon = status === 'waiting' ? '⚡ ' : '';

  const inner = `
    <div class="card-top">
      <span class="dot ${dotClass}"></span>
      <span class="card-path">${displayName}</span>
      ${isShared ? '<span class="shared-badge" title="Split view">⊞</span>' : ''}
      <span class="card-state ${inTab ? 'in-tab' : ''}">${stateLabel}</span>
      <span class="card-age">${formatAge(s.lastSeen)}</span>
      <button class="kill-btn" title="Kill session">×</button>
    </div>
    ${detailText ? `<div class="card-preview ${status ? 'status-' + status : ''}">${detailIcon}${esc(detailText)}</div>` : ''}
    ${cwdLine   ? `<div class="card-cwd">${cwdLine}</div>` : ''}`;

  return { inner, isActive: isThisTab };
}

// ── Event delegation — single listener on container handles all cards ──────

// ── Context menu ──────────────────────────────────────────────────────────────

let ctxTargetId = null;

function showContextMenu(id, x, y) {
  ctxTargetId = id;
  const menu = document.getElementById('ctx-menu');
  // Show first so offsetWidth/Height are available, then clamp to viewport.
  menu.style.left = `${x}px`;
  menu.style.top  = `${y}px`;
  menu.classList.add('show');
  const r = menu.getBoundingClientRect();
  if (r.right  > window.innerWidth)  menu.style.left = `${window.innerWidth  - r.width  - 4}px`;
  if (r.bottom > window.innerHeight) menu.style.top  = `${window.innerHeight - r.height - 4}px`;
}

function hideContextMenu() {
  document.getElementById('ctx-menu').classList.remove('show');
  ctxTargetId = null;
}

document.addEventListener('click', hideContextMenu);
document.addEventListener('keydown', e => { if (e.key === 'Escape') hideContextMenu(); });

document.getElementById('ctx-rename').addEventListener('click', () => {
  const id = ctxTargetId;
  hideContextMenu();
  if (id) startRename(id);
});

// ── Inline rename ─────────────────────────────────────────────────────────────

function startRename(id) {
  const el = cardEls.get(id);
  if (!el) return;
  const nameEl = el.querySelector('.card-path');
  if (!nameEl) return;

  const original = customNames[id]
    || sessions.find(s => s.id === id)?.meta?.name
    || nameEl.textContent;

  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'rename-input';
  input.value = original;
  nameEl.replaceWith(input);
  input.select();

  let done = false;
  function finish(save) {
    if (done) return;
    done = true;
    if (save) saveCustomName(id, input.value.trim());
    // Remove the input before re-rendering so the render guard
    // (!querySelector('.rename-input')) doesn't skip this card.
    input.remove();
    cardData.delete(id);
    scheduleRender();
  }

  input.addEventListener('click', e => e.stopPropagation());
  input.addEventListener('blur',    () => finish(true));
  input.addEventListener('keydown', e => {
    if (e.key === 'Enter')  { e.preventDefault(); finish(true); }
    if (e.key === 'Escape') { e.preventDefault(); finish(false); } // revert: don't save
    e.stopPropagation();
  });
  input.focus();
}

async function saveCustomName(id, name) {
  const { sessionNames = {} } = await chrome.storage.sync.get('sessionNames');
  if (name) {
    sessionNames[id] = name;
  } else {
    delete sessionNames[id];
  }
  customNames = sessionNames; // update local copy immediately
  await chrome.storage.sync.set({ sessionNames });
}

// ─────────────────────────────────────────────────────────────────────────────

function initContainerEvents() {
  const container = document.getElementById('sessions');

  container.addEventListener('contextmenu', e => {
    const card = e.target.closest('.session-card');
    if (!card) return;
    e.preventDefault();
    showContextMenu(card.dataset.id, e.clientX, e.clientY);
  });

  container.addEventListener('click', e => {
    if (justDropped) return; // drag just ended — suppress the synthetic click
    const card = e.target.closest('.session-card');
    if (!card) return;
    if (e.target.closest('.kill-btn')) {
      handleKill(card.dataset.id);
      return;
    }
    if (e.target.closest('.rename-input')) return;
    handleCardClick(card.dataset.id);
  });

  // dropPoint: { type:'reorder', insertBeforeId: string|null } | { type:'split', targetId: string }
  let dropPoint = null;

  function clearIndicators() {
    container.querySelectorAll('.drop-gap-above, .drop-gap-below, .drag-over')
      .forEach(el => el.classList.remove('drop-gap-above', 'drop-gap-below', 'drag-over'));
    dropPoint = null;
  }

  // Determines where the dragged card would land given a cursor Y position.
  //
  // Reorder logic uses nearest-gap: compute the Y midpoint of every gap
  // (before first card, between consecutive cards, after last card) and snap
  // to the closest one. This means the indicator only moves when the cursor
  // crosses a gap midpoint, not a card midpoint — no jitter at card edges.
  //
  // Split logic: if the cursor is in the centre 40% of a card, it's a split.
  // Split is checked first so it takes priority when inside a card.
  function computeDropPoint(clientY) {
    const cards = [...container.querySelectorAll('.session-card:not(.dragging)')];
    if (!cards.length) return { type: 'reorder', insertBeforeId: null };
    const rects = cards.map(c => c.getBoundingClientRect());

    // Split zone: cursor in middle 40% of any card
    for (let i = 0; i < cards.length; i++) {
      const r = rects[i];
      if (clientY >= r.top + r.height * 0.3 && clientY <= r.bottom - r.height * 0.3) {
        return { type: 'split', targetId: cards[i].dataset.id };
      }
    }

    // Reorder: find nearest gap.
    // Gaps: top of first card, midpoints between consecutive cards, bottom of last card.
    const gaps = [
      { y: rects[0].top,                    insertBeforeId: cards[0].dataset.id },
      ...rects.slice(0, -1).map((r, i) => ({
        y: (r.bottom + rects[i + 1].top) / 2,
        insertBeforeId: cards[i + 1].dataset.id,
      })),
      { y: rects[rects.length - 1].bottom,  insertBeforeId: null },
    ];

    let best = gaps[0];
    let bestDist = Math.abs(clientY - gaps[0].y);
    for (const g of gaps) {
      const d = Math.abs(clientY - g.y);
      if (d < bestDist) { bestDist = d; best = g; }
    }
    return { type: 'reorder', insertBeforeId: best.insertBeforeId };
  }

  function applyIndicator(point) {
    // Skip DOM update when nothing changed
    const key = JSON.stringify(point);
    if (dropPoint && JSON.stringify(dropPoint) === key) return;
    clearIndicators();
    dropPoint = point;
    if (point.type === 'split') {
      cardEls.get(point.targetId)?.classList.add('drag-over');
    } else if (point.insertBeforeId) {
      cardEls.get(point.insertBeforeId)?.classList.add('drop-gap-above');
    } else {
      // Append at end — show indicator below last visible card
      const last = container.querySelector('.session-card:not(.dragging):last-of-type');
      last?.classList.add('drop-gap-below');
    }
  }

  container.addEventListener('dragstart', e => {
    const card = e.target.closest('.session-card');
    if (!card) return;
    dragSourceId = card.dataset.id;
    e.dataTransfer.effectAllowed = 'move';

    // Custom ghost: clone the card, park it offscreen, tilt it, let the browser
    // rasterise it, then remove. requestAnimationFrame collapses the real card
    // AFTER the ghost has been captured so the ghost still looks full.
    const { offsetWidth: w, offsetHeight: h } = card;
    const clone = card.cloneNode(true);
    clone.style.cssText = `position:absolute;left:-${w + 20}px;top:0;width:${w}px;pointer-events:none;`;
    if (clone.firstElementChild) clone.firstElementChild.style.transform = 'scale(0.92) rotate(-2deg)';
    document.body.appendChild(clone);
    e.dataTransfer.setDragImage(clone, w / 2, h / 2);
    setTimeout(() => document.body.removeChild(clone), 0);

    requestAnimationFrame(() => card.classList.add('dragging'));
  });

  container.addEventListener('dragend', () => {
    clearIndicators();
    if (dragSourceId) cardEls.get(dragSourceId)?.classList.remove('dragging');
    dragSourceId = null;
    justDropped = true;
    setTimeout(() => { justDropped = false; }, 100);
  });

  container.addEventListener('dragover', e => {
    if (!dragSourceId) return;
    // Always prevent default — this marks the container as a valid drop target
    // for the browser, preventing the "ghost flies back" return animation.
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    applyIndicator(computeDropPoint(e.clientY));
  });

  container.addEventListener('drop', e => {
    e.preventDefault();
    const srcId = dragSourceId;
    const point = dropPoint ?? computeDropPoint(e.clientY);

    clearIndicators();
    if (srcId) cardEls.get(srcId)?.classList.remove('dragging');
    dragSourceId = null;
    justDropped = true;
    setTimeout(() => { justDropped = false; }, 100);

    if (!srcId) return;
    const src = sessions.find(s => s.id === srcId);
    if (!src) return;

    if (point.type === 'split') {
      const dst = sessions.find(s => s.id === point.targetId);
      if (!dst || srcId === point.targetId) return;
      isInSplitTab(dst) ? reorderSession(srcId, point.targetId, 'after') : openSplit(src, dst);
    } else {
      reorderSession(srcId, point.insertBeforeId);
    }
  });
}

// ── Session ordering ────────────────────────────────────────────────────────

function orderedSessions() {
  const byId = Object.fromEntries(sessions.map(s => [s.id, s]));
  const result = [];
  for (const id of sessionOrder) {
    if (byId[id]) { result.push(byId[id]); delete byId[id]; }
  }
  const rest = Object.values(byId).sort((a, b) => new Date(b.lastSeen) - new Date(a.lastSeen));
  return [...result, ...rest];
}

// insertBeforeId: the session ID to insert src before, or null to append at end.
function reorderSession(srcId, insertBeforeId) {
  const ids = orderedSessions().map(s => s.id);
  const srcIdx = ids.indexOf(srcId);
  if (srcIdx !== -1) ids.splice(srcIdx, 1);
  if (insertBeforeId) {
    const dstIdx = ids.indexOf(insertBeforeId);
    if (dstIdx === -1) ids.push(srcId);
    else ids.splice(dstIdx, 0, srcId);
  } else {
    ids.push(srcId);
  }
  sessionOrder = ids;
  chrome.storage.sync.set({ sessionOrder });
  scheduleRender();
}

function isInSplitTab(sess) {
  return Object.values(tabMap).some(
    ({ paths }) => (paths?.length ?? 0) >= 2 && paths.some(p => p === sess.id || p.endsWith(sess.id))
  );
}

// ── Action handlers ────────────────────────────────────────────────────────

function handleCardClick(id) {
  const sess = sessions.find(s => s.id === id);
  if (!sess) return;
  const entry = findTabEntry(sess);
  if (!entry) {
    // Not in any tab (minimized or dormant) → open in new tab.
    chrome.tabs.create({ url: sessionURL(sess) });
  } else {
    const [tabIdStr, { windowId }] = entry;
    const tabId = parseInt(tabIdStr);
    if (tabId === currentTabId) return; // already here
    // Focus the existing tab rather than closing and reopening it.
    chrome.tabs.update(tabId, { active: true });
    if (windowId) chrome.windows.update(windowId, { focused: true });
  }
}

// ── AI session naming ─────────────────────────────────────────────────────────
// Uses Chrome's built-in Gemini Nano (LanguageModel API, Chrome 138+) to generate
// a short descriptive name for sessions that have no title.
// Runs entirely locally — no data leaves the device.

// ── AI session naming ──────────────────────────────────────────────────────

function restartNamingLoop() {
  clearTimeout(namingLoopTimer);
  if (!aiNamingEnabled) return;
  namingLoopTimer = setTimeout(async () => {
    for (const sess of sessions.filter(s => s.alive)) {
      await maybeAiName(sess.id);
    }
    restartNamingLoop(); // reschedule with current interval
  }, aiNamingIntervalMs);
}

async function maybeAiName(id) {
  if (!aiNamingEnabled) return;
  if (customNames[id]) return; // never override a user-set name

  const sess = sessions.find(s => s.id === id);
  if (!sess || !sess.alive) return;
  if (sess.meta?.name) return; // session has an OSC 2 title set by the running program

  // Idle check: skip if the session hasn't had any new output since we last named it.
  const currentLastSeen = sess.lastSeen;
  if (lastNamedSeen[id] === currentLastSeen) return;
  lastNamedSeen[id] = currentLastSeen; // record attempt regardless of outcome

  if (typeof LanguageModel === 'undefined') return;
  const langOpts = {
    expectedInputLanguages: ['en'],
    expectedOutputs: [{ type: 'text', languages: ['en'] }],
  };
  try {
    const avail = await LanguageModel.availability(langOpts);
    if (avail !== 'available') return;
  } catch (_) { return; }

  let lines = [];
  try {
    const resp = await fetch(`${BASE}/api/sessions/${encodeURIComponent(id)}/lines`,
      { signal: AbortSignal.timeout(2000) });
    if (resp.ok) lines = (await resp.json()).lines || [];
  } catch (_) { return; }

  const meaningful = lines.filter(l => l.trim().length > 2);
  if (meaningful.length < 3) return;

  try {
    const ai = await LanguageModel.create({
      ...langOpts,
      systemPrompt: 'You label terminal sessions with a 2–4 word phrase.',
    });
    const input = meaningful.slice(-15).join('\n');

    // responseConstraint (Chrome 137+) forces the model to return valid JSON
    // matching the schema — no reasoning, no markdown, no explanation possible.
    const schema = {
      type: 'object',
      properties: { name: { type: 'string' } },
      required: ['name'],
      additionalProperties: false,
    };
    const raw = await ai.prompt(
      `Label this terminal session in 2–4 words based on its recent output:\n\n${input}`,
      { responseConstraint: schema },
    );
    ai.destroy();

    let name = '';
    try {
      name = JSON.parse(raw).name ?? '';
    } catch (_) {
      // Fallback: model didn't honour the constraint — strip markdown and take first words
      name = raw.replace(/\*+([^*]*)\*+/g, '$1').split(/\n|explanation:|based on/i)[0];
    }
    name = name.replace(/^["']|["'.,!?]$/g, '').trim().slice(0, 50);

    if (name && name.length >= 3 && name.split(/\s+/).length <= 6) {
      await saveAiName(id, name);
    }
  } catch (_) {}
}

async function saveAiName(id, name) {
  const { aiSessionNames = {} } = await chrome.storage.sync.get('aiSessionNames');
  if (name) aiSessionNames[id] = name;
  else delete aiSessionNames[id];
  aiNames = { ...aiNames, [id]: name };
  await chrome.storage.sync.set({ aiSessionNames });
}

async function handleKill(id) {
  const sess = sessions.find(s => s.id === id);
  if (!sess) return;

  // Remove from local list immediately so the card disappears without waiting
  // for the daemon's session:killed event.
  sessions = sessions.filter(s => s.id !== id);
  scheduleRender();

  // Close the tab before killing the daemon session so the "session ended"
  // overlay doesn't flash in the terminal before the tab closes.
  const entry = findTabEntry(sess);
  if (entry) await chrome.tabs.remove(parseInt(entry[0])).catch(() => {});

  fetch(`${BASE}/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }).catch(() => {});
}

// ── Navigation helpers ─────────────────────────────────────────────────────

function sessionURL(sess) {
  return sess.id ? `${BASE}/connect/${sess.id}` : `${BASE}/s/${sess.scheme}/new`;
}

function openSplit(sessA, sessB) {
  const a = sessA.id || `${sessA.scheme}/${sessA.path || 'new'}`;
  const b = sessB.id || `${sessB.scheme}/${sessB.path || 'new'}`;
  const url = `${BASE}/split?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}`;

  // Reuse an existing tab rather than always spawning a new one.
  // Prefer to navigate sessA's tab (the one being dragged), then sessB's.
  // Close the other tab if it's a plain single-session tab (not already a split).
  const entryA = findTabEntry(sessA);
  const entryB = findTabEntry(sessB);
  const tabIdA = entryA ? parseInt(entryA[0]) : null;
  const tabIdB = entryB ? parseInt(entryB[0]) : null;

  // Decide which existing tab to reuse and which to close.
  const reuseId = tabIdA ?? tabIdB;
  const closeId = reuseId === tabIdA ? tabIdB : tabIdA;

  if (reuseId) {
    chrome.tabs.update(reuseId, { url, active: true });
  } else {
    chrome.tabs.create({ url });
  }

  // Close the other single-session tab — but only if it's not a split view
  // already (those paths.length >= 2) and not the same tab we just reused.
  if (closeId && closeId !== reuseId) {
    const closeEntry = closeId === tabIdA ? entryA : entryB;
    const isSplit = (closeEntry?.[1]?.paths?.length ?? 0) >= 2;
    if (!isSplit) chrome.tabs.remove(closeId);
  }
}

// ── Utilities ──────────────────────────────────────────────────────────────

function truncatePath(path, max) {
  if (!path) return '';
  const home = path.match(/^\/Users\/[^/]+/)?.[0];
  if (home) path = '~' + path.slice(home.length);
  if (path.length <= max) return esc(path);
  return '…' + esc(path.slice(path.length - max + 1));
}

function formatAge(iso) {
  if (!iso) return '';
  const secs = Math.floor((Date.now() - new Date(iso)) / 1000);
  if (secs < 60)   return `${secs}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  return `${Math.floor(secs / 3600)}h`;
}

function esc(str) {
  return String(str ?? '')
    .replace(/&/g, '&amp;').replace(/</g, '&lt;')
    .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

init();
