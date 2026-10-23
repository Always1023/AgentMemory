
(function (root, factory) {
  if (typeof module === "object" && module.exports) {
    module.exports = factory();
  } else {
    root.AgentMemoryWebContent = factory();
  }
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  function registerSite({ site, listConversations, fetchConversation }) {
    if (typeof chrome === "undefined" || !chrome.runtime?.onMessage) return;

    let listProgress = null;

    chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
      if (message?.type === "ping") {
        sendResponse({ ok: true, site, url: location.href });
        return undefined;
      }

      if (message?.type === "list-conversations") {
        listProgress = { site, loading: true, conversations: [], meta: null };

        Promise.resolve()
          .then(() => listConversations((progress) => {
            listProgress = {
              site,
              loading: true,
              conversations: Array.isArray(progress?.conversations) ? progress.conversations : [],
              meta: progress?.meta || null,
            };
          }))
          .then((conversations) => {
            listProgress = {
              site,
              loading: false,
              conversations,
              meta: listProgress?.meta || null,
            };
            sendResponse({ ok: true, conversations, meta: listProgress.meta });
          })
          .catch((error) => {
            const messageText = error?.message || String(error);
            listProgress = { ...(listProgress || { site }), loading: false, error: messageText };
            sendResponse({ ok: false, error: messageText });
          });
        return true;
      }

      if (message?.type === "list-progress") {
        sendResponse({ ok: true, progress: listProgress });
        return undefined;
      }

      if (message?.type === "extract-conversation") {
        Promise.resolve()
          .then(() => fetchConversation(message.ref))
          .then((conversation) => sendResponse({ ok: true, conversation }))
          .catch((error) => sendResponse({ ok: false, error: error?.message || String(error) }));
        return true;
      }

      return undefined;
    });
  }

  return { registerSite };
});
