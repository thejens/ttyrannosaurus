const BASE = 'http://127.0.0.1:7071';

document.getElementById('options-link').addEventListener('click', (e) => {
  e.preventDefault();
  chrome.runtime.openOptionsPage();
});

async function load() {
  const content = document.getElementById('content');

  let sessions;
  try {
    const resp = await fetch(`${BASE}/api/sessions`, { signal: AbortSignal.timeout(2000) });
    sessions = await resp.json();
  } catch (_) {
    content.innerHTML = `
      <div class="empty">
        Daemon not running.<br>
        <a href="https://github.com/thejens/ttyrannosaurus" target="_blank">Start ttyrannosaurus</a>
      </div>`;
    return;
  }

  if (!sessions || sessions.length === 0) {
    content.innerHTML = `<div class="empty">No active sessions.<br>Type <strong>t claude/new</strong> to start one.</div>`;
    return;
  }

  // Group by scheme
  const byScheme = {};
  for (const s of sessions) {
    if (!byScheme[s.scheme]) byScheme[s.scheme] = [];
    byScheme[s.scheme].push(s);
  }

  const html = Object.entries(byScheme).map(([scheme, list]) => `
    <div class="scheme-group">
      <div class="scheme-label">${esc(scheme)}</div>
      ${list.map(s => `
        <div class="session${s.alive ? '' : ' session-dead'}"
             data-path="${esc(scheme + '/' + s.path)}">
          <span class="session-path">${esc(s.path)}</span>
          <span class="session-age">${formatAge(s.lastSeen)}</span>
        </div>
      `).join('')}
    </div>
  `).join('');

  content.innerHTML = html;

  content.querySelectorAll('.session[data-path]').forEach(el => {
    el.addEventListener('click', () => {
      chrome.tabs.create({ url: `${BASE}/s/${el.dataset.path}` });
      window.close();
    });
  });
}

function formatAge(iso) {
  const secs = Math.floor((Date.now() - new Date(iso)) / 1000);
  if (secs < 60) return `${secs}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  return `${Math.floor(secs / 3600)}h`;
}

function esc(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

load();
