const BASE = 'http://127.0.0.1:7071';

// ── Omnibox (keyword: !) ──────────────────────────────────────────────────
// Everything typed after ! is treated as a shell command — no template magic.

chrome.omnibox.onInputChanged.addListener(async (text, suggest) => {
  text = text.trim();
  chrome.omnibox.setDefaultSuggestion({
    description: text
      ? `Run in terminal: <match>${text}</match>`
      : 'Type a command to run in a terminal',
  });

  // Suggest active sessions whose command starts with what the user is typing.
  if (!text) return;
  try {
    const resp = await fetch(`${BASE}/api/sessions`, { signal: AbortSignal.timeout(1000) });
    if (!resp.ok) return;
    const sessions = await resp.json();
    const q = text.toLowerCase();
    const suggestions = sessions
      .filter(s => s.alive && s.path.toLowerCase().startsWith(q))
      .slice(0, 5)
      .map(s => ({
        content: s.path,
        description: `Reconnect: <match>${s.path}</match>`,
      }));
    suggest(suggestions);
  } catch (_) {}
});

chrome.omnibox.onInputEntered.addListener(async (text, disposition) => {
  // Inherit CWD from the focused terminal tab if there is one.
  let cwd = '';
  try {
    const [activeTab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (activeTab) {
      const { tabMap = {} } = await chrome.storage.session.get('tabMap');
      const entry = tabMap[activeTab.id];
      if (entry?.paths?.length) {
        const resp = await fetch(`${BASE}/api/sessions`, { signal: AbortSignal.timeout(500) });
        if (resp.ok) {
          const sessions = await resp.json();
          for (const path of entry.paths) {
            const sess = sessions.find(s => s.id === path || `${s.scheme}/${s.path}` === path);
            if (sess?.meta?.cwd) { cwd = sess.meta.cwd; break; }
          }
        }
      }
    }
  } catch (_) {}

  const base = `${BASE}/s/?cmd=${encodeURIComponent(text.trim())}`;
  const url = cwd ? `${base}&cwd=${encodeURIComponent(cwd)}` : base;
  if (disposition === 'currentTab') {
    chrome.tabs.update({ url });
  } else {
    chrome.tabs.create({ url });
  }
});

// ── Sidebar mode ──────────────────────────────────────────────────────────
//
// sidebarMode (sync, default keep-open) — keep-open | open-when-terminal
//
// The icon toggle is handled natively by Chrome via openPanelOnActionClick.
// Manual async state-tracking in action.onClicked was the source of flakiness:
// chrome.storage reads are async, and by the time they resolve the user-gesture
// context Chrome requires for sidePanel.open() is gone — so open() silently
// fails and the enabled flag diverges from the visible state.

// Icon toggle is handled natively by Chrome — reliable, no async state to desync.
chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: true }).catch(() => {});

// Global enable: call without tabId to re-enable the panel on ALL tabs at once.
// This is the correct reset when leaving "open-when-terminal" mode, where
// individual tabs may have been set to enabled:false.
function enablePanelGlobally() {
  chrome.sidePanel.setOptions({ enabled: true }).catch(() => {});
}

async function updatePanelForTab(tabId, windowId) {
  const { sidebarMode } = await chrome.storage.sync.get({ sidebarMode: 'keep-open' });
  if (sidebarMode !== 'open-when-terminal') return; // keep-open: nothing to do

  const { tabMap = {} } = await chrome.storage.session.get('tabMap');
  const isTerminal = !!(tabMap[tabId]?.paths?.length);
  chrome.sidePanel.setOptions({ tabId, enabled: isTerminal }).catch(() => {});
  if (isTerminal && windowId) chrome.sidePanel.open({ windowId }).catch(() => {});
}

// Re-evaluate panel when mode changes from the options page.
chrome.storage.onChanged.addListener(async (changes, area) => {
  if (area !== 'sync' || !('sidebarMode' in changes)) return;
  if (changes.sidebarMode.newValue === 'keep-open') {
    // Switching to keep-open: restore panel on all tabs that were disabled.
    enablePanelGlobally();
  } else {
    const [activeTab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (activeTab) updatePanelForTab(activeTab.id, activeTab.windowId);
  }
});

// Messages forwarded from terminal pages via content.js.
chrome.runtime.onMessage.addListener((msg, sender) => {
  if (msg.type === 'closeTab' && sender.tab?.id) {
    chrome.tabs.remove(sender.tab.id);
    return;
  }

  if (msg.type === 'openInSplit' && msg.url && sender.tab?.windowId) {
    // Alt+click on a link: snap the terminal window to the left half and open
    // the URL in a new window on the right half.
    const screenW = msg.screenW || 1440;
    const screenH = msg.screenH || 900;
    const halfW   = Math.floor(screenW / 2);

    chrome.windows.update(sender.tab.windowId, {
      left: 0, top: 0, width: halfW, height: screenH,
    });
    chrome.windows.create({
      url:    msg.url,
      left:   halfW,
      top:    0,
      width:  halfW,
      height: screenH,
      focused: true,
    });
  }
});

// ── Tab tracking for sidebar session state ────────────────────────────────

// tabMap shape: tabId → { paths: string[], windowId: number }
// paths contains session IDs (from /connect/{id}) or scheme/path strings.
// Split view tabs carry two paths.

function extractPathsFromURL(url) {
  const sPrefix = `${BASE}/s/`;
  const cPrefix = `${BASE}/connect/`;
  const splitBase = `${BASE}/split`;
  if (url.startsWith(cPrefix)) return [url.slice(cPrefix.length).split('?')[0]];
  if (url.startsWith(sPrefix)) return [url.slice(sPrefix.length).split('?')[0]];
  if (url.startsWith(splitBase)) {
    try {
      const p = new URL(url).searchParams;
      return [p.get('a'), p.get('b')].filter(Boolean);
    } catch (_) { return []; }
  }
  return [];
}

// ── Sidepanel port management ─────────────────────────────────────────────
// background.js is the single source of truth for tabMap.
// Sidepanels connect via a named port and receive push updates instead of
// maintaining their own parallel tab listeners.

const sidepanelPorts = new Set();

chrome.runtime.onConnect.addListener(port => {
  if (port.name !== 'sidepanel') return;
  sidepanelPorts.add(port);
  port.onDisconnect.addListener(() => sidepanelPorts.delete(port));
  // Send current tabMap immediately on connect so the sidepanel renders without delay.
  chrome.storage.session.get('tabMap').then(({ tabMap = {} }) =>
    port.postMessage({ type: 'tabMapUpdate', tabMap }));
});

function pushTabMapToSidepanels() {
  if (!sidepanelPorts.size) return;
  chrome.storage.session.get('tabMap').then(({ tabMap = {} }) =>
    sidepanelPorts.forEach(p => p.postMessage({ type: 'tabMapUpdate', tabMap })));
}

async function onTabUpdated(tabId, changeInfo, tab) {
  if (changeInfo.status !== 'complete') return;
  const paths = extractPathsFromURL(tab.url || '');
  const existing = await chrome.storage.session.get('tabMap');
  const tabMap = existing.tabMap || {};
  if (paths.length > 0) {
    tabMap[tabId] = { paths, windowId: tab.windowId };
  } else {
    delete tabMap[tabId];
  }
  await chrome.storage.session.set({ tabMap });
  pushTabMapToSidepanels();

  // If this tab is currently active in its window, update panel visibility.
  const [activeInWindow] = await chrome.tabs.query({ active: true, windowId: tab.windowId });
  if (activeInWindow?.id === tabId) updatePanelForTab(tabId, tab.windowId);
}

async function onTabRemoved(tabId) {
  const existing = await chrome.storage.session.get('tabMap');
  const tabMap = existing.tabMap || {};
  delete tabMap[tabId];
  await chrome.storage.session.set({ tabMap });
  pushTabMapToSidepanels();
}

chrome.tabs.onActivated.addListener(({ tabId, windowId }) => {
  updatePanelForTab(tabId, windowId);
});

chrome.tabs.onUpdated.addListener(onTabUpdated);
chrome.tabs.onRemoved.addListener(onTabRemoved);

// On service worker startup, rebuild tabMap from all currently open tabs so
// the sidebar recognises existing terminal tabs after an extension reload.
// pushTabMapToSidepanels() is called after the write in case the sidepanel
// connected during startup before this scan completed (race on SW restart).
(async () => {
  const tabs = await chrome.tabs.query({});
  const tabMap = {};
  for (const tab of tabs) {
    const paths = extractPathsFromURL(tab.url || '');
    if (paths.length > 0) {
      tabMap[tab.id] = { paths, windowId: tab.windowId };
    }
  }
  await chrome.storage.session.set({ tabMap });
  pushTabMapToSidepanels();

  // On startup, ensure no stale per-tab enabled:false values remain from a
  // previous "open-when-terminal" session if the mode is now "keep-open".
  const { sidebarMode } = await chrome.storage.sync.get({ sidebarMode: 'keep-open' });
  if (sidebarMode === 'keep-open') {
    enablePanelGlobally();
  } else {
    const [activeTab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (activeTab) updatePanelForTab(activeTab.id, activeTab.windowId);
  }
})();
