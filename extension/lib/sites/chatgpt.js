import { loadListCache } from "../list-cache.js";

const CHATGPT_URL = "https://chatgpt.com/";

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

async function sendMessageWithRetry(tabs, tabId, message, attempts = 5, intervalMs = 500, timeoutMs = 30000) {
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

function defaultSession() {
  try {
    return typeof chrome !== "undefined" ? chrome.storage?.session || null : null;
  } catch {
    return null;
  }
}

export function createChatGPTAdapter({
  tabs = chrome.tabs,
  session = defaultSession(),
  keepTab = false,
  listCache = null,
} = {}) {
  let tabId = null;
  let fallbackChain = Promise.resolve();

  function queueFallback(task) {
    const run = fallbackChain.then(task, task);
    fallbackChain = run.catch(() => {});
    return run;
  }

  return {
    site: "chatgpt",
    maxConcurrency: 3,

    async open() {
      if (tabId != null) return;

      let storedId = null;
      if (session) {
        try {
          const stored = await session.get("chatgptTabId");
          storedId = stored?.chatgptTabId;
        } catch {
          // 没有会话存储时照常新开标签页。
        }
      }

      if (storedId != null) {
        try {
          const tab = await tabs.get(storedId);
          if (tab && /^https:\/\/(chatgpt\.com|chat\.openai\.com)\//.test(tab.url || "")) {
            tabId = storedId;
          }
        } catch {
          // 标签页已经关掉。
        }
      }

      let created = false;
      if (tabId == null) {
        const tab = await tabs.create({ url: CHATGPT_URL, active: false });
        tabId = tab.id;
        created = true;
        if (session) {
          try {
            await session.set({ chatgptTabId: tabId });
          } catch {
            // 存不住也不影响本次运行。
          }
        }
      }

      try {
        await waitForTabComplete(tabs, tabId, 60000);
        await delay(1200 + Math.random() * 600);
        const ping = await sendMessageWithRetry(tabs, tabId, { type: "ping" }, 8, 500, 5000);
        if (ping?.version !== 2) throw new Error("页面脚本版本过旧。");
      } catch (error) {
        if (created) throw error;
        await tabs.update(tabId, { url: CHATGPT_URL });
        await waitForTabComplete(tabs, tabId, 60000);
        await delay(1200 + Math.random() * 600);
        const ping = await sendMessageWithRetry(tabs, tabId, { type: "ping" }, 8, 500, 5000);
        if (ping?.version !== 2) throw new Error("页面脚本版本过旧。");
      }
    },

    async listConversations({ onProgress } = {}) {
      if (tabId == null) throw new Error("批处理标签页还没有打开。");
      const cache = listCache ? await loadListCache(listCache, "chatgpt") : null;

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
                      site: "chatgpt",
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
          { type: "list-conversations", cache },
          2,
          1000,
          240000,
        );
        if (!reply?.ok) throw new Error(reply?.error || "没有找到会话列表。");
        const conversations = (reply.conversations || []).map((conversation) => ({
          ...conversation,
          site: "chatgpt",
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
      const url = ref.url || `https://chatgpt.com/c/${ref.id}`;
      let apiError = null;

      try {
        const apiReply = await sendMessageWithRetry(
          tabs,
          tabId,
          { type: "extract-conversation", id: ref.id },
          1,
          1000,
          240000,
        );

        if (apiReply?.ok) {
          return {
            ...apiReply.conversation,
            site: "chatgpt",
            id: ref.id,
            url,
            source: "api",
            title: apiReply.conversation?.title || ref.title || "未命名会话",
          };
        }

        apiError = new Error(apiReply?.error || "接口读取失败。");
      } catch (error) {
        apiError = error;
        // 接口失败时退回逐条打开页面；同一标签页上的兜底要串行。
      }

      return queueFallback(async () => {
        if (tabId == null) throw new Error("批处理标签页还没有打开。");
        await tabs.update(tabId, { url });
        await waitForTabComplete(tabs, tabId, 60000);
        await delay(1500 + Math.random() * 1000);

        const domReply = await sendMessageWithRetry(
          tabs,
          tabId,
          { type: "extract-conversation" },
          3,
          1000,
          30000,
        );
        if (!domReply?.ok) throw new Error(domReply?.error || "没有找到会话内容。");

        return {
          ...domReply.conversation,
          site: "chatgpt",
          id: ref.id,
          url,
          source: "dom",
          fallbackReason: apiError?.message || "",
          title: domReply.conversation?.title || ref.title || "未命名会话",
        };
      });
    },

    async close() {
      if (tabId == null) return;
      if (keepTab) {
        tabId = null;
        return;
      }

      const closing = tabId;
      tabId = null;
      try {
        await tabs.remove(closing);
      } catch {
        // 标签页可能已经被用户关掉。
      }
      if (session && typeof session.remove === "function") {
        try {
          await session.remove("chatgptTabId");
        } catch {
          // 清理失败不影响下一次运行。
        }
      }
    },
  };
}
