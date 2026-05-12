// Runs on all ttyrannosaurus daemon pages (http://127.0.0.1:7071/*).
// Bridges postMessage requests from the terminal page to the extension background,
// enabling the page to close its own tab even when window.close() is blocked.
window.addEventListener('message', (e) => {
  if (e.origin !== 'http://127.0.0.1:7071') return;
  // Guard: chrome.runtime becomes undefined when the extension context is
  // invalidated (e.g. after an extension reload without a page refresh).
  if (!chrome.runtime?.sendMessage) return;
  if (e.data?.type === 'ttyrannosaurus:closeTab') {
    chrome.runtime.sendMessage({ type: 'closeTab' });
  } else if (e.data?.type === 'ttyrannosaurus:openInSplit') {
    chrome.runtime.sendMessage({
      type: 'openInSplit',
      url:     e.data.url,
      screenW: e.data.screenW,
      screenH: e.data.screenH,
    });
  }
});
