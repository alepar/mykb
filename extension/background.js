const DEFAULT_SERVER = "http://api.mykb.k3s";

async function getServer() {
  const result = await browser.storage.local.get("server");
  return result.server || DEFAULT_SERVER;
}

function setBadge(text, color) {
  browser.browserAction.setBadgeText({ text });
  browser.browserAction.setBadgeBackgroundColor({ color });
}

function clearBadge() {
  setBadge("", "#000");
}

async function pollStatus(server, docId) {
  const startTime = Date.now();
  const TIMEOUT = 5 * 60 * 1000;
  const INTERVAL = 2000;

  const poll = async () => {
    if (Date.now() - startTime > TIMEOUT) {
      setBadge("!", "#ff0000");
      setTimeout(clearBadge, 5000);
      return;
    }

    try {
      const resp = await fetch(`${server}/api/ingest/${docId}`);
      const data = await resp.json();

      if (data.error) {
        setBadge("!", "#ff0000");
        setTimeout(clearBadge, 5000);
        return;
      }

      switch (data.status) {
        case "DONE":
          setBadge("\u2713", "#00aa00");
          setTimeout(clearBadge, 3000);
          return;
        case "CRAWLING":
          setBadge("CRL", "#ddaa00");
          break;
        case "CHUNKING":
          setBadge("CHK", "#ddaa00");
          break;
        case "EMBEDDING":
          setBadge("EMB", "#ddaa00");
          break;
        case "INDEXING":
          setBadge("IDX", "#ddaa00");
          break;
        default:
          setBadge("...", "#ddaa00");
      }

      setTimeout(poll, INTERVAL);
    } catch (e) {
      setBadge("!", "#ff0000");
      setTimeout(clearBadge, 5000);
    }
  };

  poll();
}

browser.browserAction.onClicked.addListener(async (tab) => {
  if (!tab.url || tab.url.startsWith("about:") || tab.url.startsWith("moz-extension:")) {
    setBadge("!", "#ff0000");
    setTimeout(clearBadge, 3000);
    return;
  }

  const server = await getServer();
  setBadge("...", "#ddaa00");

  try {
    // Capture rendered HTML from the active tab.
    const results = await browser.tabs.executeScript(tab.id, {
      code: "document.documentElement.outerHTML",
    });
    const html = results && results[0] ? results[0] : "";

    const resp = await fetch(`${server}/api/ingest`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url: tab.url, html, force: true }),
    });

    if (resp.status === 409) {
      setBadge("dup", "#4488ff");
      setTimeout(clearBadge, 3000);
      return;
    }

    if (!resp.ok) {
      setBadge("!", "#ff0000");
      setTimeout(clearBadge, 5000);
      return;
    }

    const data = await resp.json();
    pollStatus(server, data.id);
  } catch (e) {
    setBadge("ERR", "#ff0000");
    setTimeout(clearBadge, 5000);
  }
});
