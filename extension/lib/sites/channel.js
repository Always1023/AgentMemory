function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function withTimeout(promise, ms, message) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(message)), ms);
  });

  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function waitForTabComplete(tabs, tabId, timeoutMs) {
  return new Promise((resolve, reject) => {
    let settled = false;

    const cleanup = () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      tabs.onUpdated.removeListener(listener);
    };

    const finish = () => {
      cleanup();
      resolve();
    };

    const listener = (updatedTabId, info) => {
      if (updatedTabId === tabId && info.status === "complete") finish();
    };

    const timer = setTimeout(() => {
      cleanup();
      reject(new Error("等待页面加载超时。"));
    }, timeoutMs);

    tabs.onUpdated.addListener(listener);
    tabs.get(tabId).then((tab) => {
      if (tab?.status === "complete") finish();
    }).catch(() => {});
  });
}

async function sendMessageWithRetry(tabs, tabId, message, attempts = 5, intervalMs = 500, timeoutMs = 60000) {
  let lastError;

  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      return await withTimeout(tabs.sendMessage(tabId, message), timeoutMs, "页面响应超时。");
    } catch (error) {
      lastError = error;
      await delay(intervalMs);
    }
  }

  throw new Error(`扩展与页面通信失败：${lastError?.message || "未知错误"}`);
}

export function createApiSiteAdapter({
  site,
  baseUrl,
  tabs = chrome.tabs,
  listTimeoutMs = 180000,
  fetchTimeoutMs = 120000,
}) {
  let tabId = null;

  return {
    site,
    maxConcurrency: 2,

    async open() {
      const tab = await tabs.create({ url: baseUrl, active: false });
      tabId = tab.id;
      await waitForTabComplete(tabs, tabId, 60000);
      await delay(1200 + Math.random() * 600);
      await sendMessageWithRetry(tabs, tabId, { type: "ping" }, 8, 500, 5000);
    },

    async listConversations({ onProgress } = {}) {
      if (tabId == null) throw new Error("批处理标签页还没有打开。");

      let done = false;
      const polling = onProgress
        ? (async () => {
            while (!done) {
              await delay(400);
              if (done) break;

              try {
                const progressReply = await withTimeout(
                  tabs.sendMessage(tabId, { type: "list-progress" }),
                  5000,
                  "读取列表进度超时。",
                );
                if (progressReply?.ok && progressReply.progress) {
                  onProgress({
                    conversations: (progressReply.progress.conversations || []).map((conversation) => ({
                      ...conversation,
                      site,
                    })),
                    meta: progressReply.progress.meta || null,
                    loading: progressReply.progress.loading !== false,
                  });
                }
              } catch {
                // 进度消息失败不影响列表本身。
              }
            }
          })()
        : null;

      try {
        const reply = await sendMessageWithRetry(
          tabs,
          tabId,
          { type: "list-conversations" },
          2,
          1000,
          listTimeoutMs,
        );
        if (!reply?.ok) throw new Error(reply?.error || "没有找到会话列表。");
        const conversations = (reply.conversations || []).map((conversation) => ({
          ...conversation,
          site,
        }));
        if (reply.meta) conversations.meta = reply.meta;
        return conversations;
      } finally {
        done = true;
        if (polling) await polling;
      }
    },

    async fetchConversation(ref) {
      if (tabId == null) throw new Error("批处理标签页还没有打开。");
      const reply = await sendMessageWithRetry(
        tabs,
        tabId,
        { type: "extract-conversation", ref },
        3,
        1000,
        fetchTimeoutMs,
      );
      if (!reply?.ok) throw new Error(reply?.error || "没有找到会话内容。");

      const conversation = reply.conversation || {};
      const id = conversation.id || ref?.id;
      const url = conversation.url || ref?.url || "";

      return {
        ...conversation,
        site,
        id,
        url,
        source: "api",
        title: conversation.title || ref?.title || "未命名会话",
      };
    },

    async close() {
      if (tabId == null) return;
      const closing = tabId;
      tabId = null;
      try {
        await tabs.remove(closing);
      } catch {
        // 标签页可能已经被用户关掉。
      }
    },
  };
}
