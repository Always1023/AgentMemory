(function () {
  const parser = globalThis.AgentMemoryWebChatGPT;
  if (!parser) return;

  const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const DEVICE_ID = typeof crypto !== "undefined" && crypto.randomUUID
    ? crypto.randomUUID()
    : `agentmemory-${Date.now()}`;
  let accessToken = null;
  let listProgress = null;

  function findScrollable(start) {
    let element = start;

    while (element && element !== document.body) {
      const style = getComputedStyle(element);
      if (/(auto|scroll)/.test(style.overflowY) && element.scrollHeight > element.clientHeight) {
        return element;
      }
      element = element.parentElement;
    }

    return null;
  }

  async function getAccessToken(force = false) {
    if (accessToken && !force) return accessToken;

    try {
      const response = await fetch("/api/auth/session", { credentials: "include" });
      if (!response.ok) return null;
      const data = await response.json();
      accessToken = data?.accessToken || null;
      return accessToken;
    } catch {
      return null;
    }
  }

  function authHeaders(token) {
    return {
      Authorization: `Bearer ${token}`,
      Accept: "application/json",
      "Oai-Device-Id": DEVICE_ID,
      "Oai-Language": "zh-CN",
    };
  }

  async function apiFetch(path, options = {}, attempt = 0) {
    const token = await getAccessToken(attempt > 0 && !accessToken);
    if (!token) throw new Error("没有找到登录会话；请确认已登录 ChatGPT。");

    let response;

    try {
      response = await fetch(path, {
        credentials: "include",
        ...options,
        headers: {
          ...authHeaders(token),
          ...(options.headers || {}),
        },
      });
    } catch (error) {
      if (attempt < 2) {
        await delay(2500 * (attempt + 1));
        return apiFetch(path, options, attempt + 1);
      }
      throw error;
    }

    if (response.status === 401 && attempt < 2) {
      accessToken = null;
      await delay(1200);
      return apiFetch(path, options, attempt + 1);
    }

    if (response.status === 429) {
      const retryAfter = Number(response.headers.get("Retry-After"));
      const wait = Number.isFinite(retryAfter) && retryAfter > 0
        ? retryAfter * 1000
        : 6000 * 2 ** attempt;

      if (attempt < 6) {
        await delay(Math.min(wait, 60000));
        return apiFetch(path, options, attempt + 1);
      }

      throw new Error("ChatGPT 接口连续返回 429，已停止。");
    }

    if (response.status >= 500 && attempt < 3) {
      await delay(2500 * (attempt + 1));
      return apiFetch(path, options, attempt + 1);
    }

    return response;
  }

  async function listViaApi(cache = null, onProgress = null) {
    const previous = Array.isArray(cache?.items) ? cache.items : [];
    const previousById = new Map(
      previous
        .map((item) => [String(item?.id || ""), item])
        .filter(([id]) => id),
    );
    const watermark = String(cache?.watermark || "");
    const canStopEarly = cache?.complete === true && previous.length > 0;
    const conversations = [];
    const seen = new Set();
    const meta = {
      source: "api",
      pages: 0,
      active: 0,
      archived: 0,
      activeTotal: null,
      archivedTotal: null,
      cacheUsed: canStopEarly,
      stoppedEarly: false,
      incomplete: false,
    };

    const emit = () => {
      if (!onProgress) return;
      onProgress({
        source: "api",
        conversations: [...conversations],
        meta: { ...meta },
      });
    };

    for (const isArchived of [false, true]) {
      let offset = 0;
      let pages = 0;
      let stopped = false;

      while (offset < 5000 && pages < 250) {
        const archived = isArchived ? "&is_archived=true" : "";
        const response = await apiFetch(`/backend-api/conversations?offset=${offset}&limit=100&order=updated${archived}`);
        if (!response.ok) throw new Error(`读取会话列表失败：HTTP ${response.status}`);

        const data = await response.json();
        const rawItems = Array.isArray(data) ? data : (Array.isArray(data?.items) ? data.items : []);
        if (rawItems.length === 0) break;

        if (isArchived) {
          if (meta.archivedTotal == null) meta.archivedTotal = Number(data?.total) || null;
        } else if (meta.activeTotal == null) {
          meta.activeTotal = Number(data?.total) || null;
        }

        const page = [];
        let added = 0;

        for (const item of rawItems) {
          const id = String(item?.id || "").trim();
          if (!id) continue;

          const rawUpdated = item?.update_time;
          const updatedAt = rawUpdated == null || rawUpdated === ""
            ? ""
            : (typeof rawUpdated === "number"
              ? new Date(rawUpdated * 1000).toISOString()
              : String(rawUpdated));
          const entry = {
            id,
            title: String(item?.title || "").trim() || "未命名会话",
            url: `${location.origin}/c/${id}`,
            updatedAt,
            listFingerprint: updatedAt ? `${updatedAt}:${item?.title || ""}` : "",
            archived: isArchived,
          };
          page.push(entry);

          if (seen.has(id)) continue;
          seen.add(id);
          added += 1;
          conversations.push(entry);
          if (isArchived) meta.archived += 1;
          else meta.active += 1;
        }

        pages += 1;
        meta.pages += 1;
        offset += rawItems.length;
        emit();

        if (canStopEarly) {
          const allKnown = page.length > 0 && page.every((entry) => {
            const previousEntry = previousById.get(entry.id);
            return previousEntry && previousEntry.listFingerprint === entry.listFingerprint;
          });
          const pageMax = page.reduce(
            (max, entry) => (entry.updatedAt > max ? entry.updatedAt : max),
            "",
          );

          if (allKnown && watermark && pageMax && pageMax <= watermark) {
            for (const previousEntry of previous) {
              if (Boolean(previousEntry?.archived) !== isArchived) continue;
              const id = String(previousEntry?.id || "");
              if (!id || seen.has(id)) continue;
              seen.add(id);
              conversations.push({ ...previousEntry, archived: isArchived });
              if (isArchived) meta.archived += 1;
              else meta.active += 1;
            }
            meta.stoppedEarly = true;
            stopped = true;
            emit();
            break;
          }
        }

        if (added === 0) break;
        await delay(600 + Math.random() * 300);
      }

      const streamTotal = isArchived ? meta.archivedTotal : meta.activeTotal;
      if (!stopped && streamTotal && offset < streamTotal) meta.incomplete = true;
    }

    return { conversations, meta };
  }

  async function fetchConversationViaApi(id) {
    const response = await apiFetch(`/backend-api/conversation/${encodeURIComponent(id)}`);
    if (!response.ok) throw new Error(`读取会话失败：HTTP ${response.status}`);

    const data = await response.json();
    return parser.parseApiConversation(data, {
      id,
      url: `${location.origin}/c/${id}`,
    });
  }

  async function collectWithScroll(cache = null, onProgress = null) {
    let apiError = null;

    try {
      const api = await listViaApi(cache, onProgress);
      if (api.conversations.length > 0) return api;
    } catch (error) {
      apiError = error?.message || String(error);
    }

    const byId = new Map();

    const emitDom = () => {
      if (!onProgress) return;
      onProgress({
        source: "dom",
        conversations: [...byId.values()],
        meta: {
          source: "dom",
          pages: 0,
          active: byId.size,
          archived: 0,
          activeTotal: null,
          archivedTotal: null,
          cacheUsed: false,
          stoppedEarly: false,
          apiError,
        },
      });
    };

    const collect = () => {
      try {
        for (const conversation of parser.listConversationLinks(document, location)) {
          byId.set(conversation.id, conversation);
        }
      } catch {
        // 列表还没渲染出来。
      }
    };

    for (let attempt = 0; attempt < 20 && byId.size === 0; attempt += 1) {
      collect();
      if (byId.size > 0) break;
      await delay(500);
    }

    emitDom();

    const anchor = document.querySelector('nav a[href*="/c/"]') || document.querySelector('a[href*="/c/"]');
    const scroller = findScrollable(anchor);

    if (scroller) {
      let stable = 0;

      for (let index = 0; index < 30 && stable < 2; index += 1) {
        const before = byId.size;
        scroller.scrollTop = scroller.scrollHeight;
        await delay(800);
        collect();

        if (byId.size === before) stable += 1;
        else stable = 0;

        emitDom();
      }
    }

    if (byId.size === 0) {
      throw new Error(`没有找到会话列表；接口和页面都没有返回会话。${apiError ? `接口：${apiError}` : ""}`);
    }

    emitDom();

    return {
      conversations: [...byId.values()],
      meta: {
        source: "dom",
        pages: 0,
        active: byId.size,
        archived: 0,
        activeTotal: null,
        archivedTotal: null,
        cacheUsed: false,
        stoppedEarly: false,
        apiError,
      },
    };
  }

  chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (message?.type === "ping") {
      sendResponse({ ok: true, url: location.href, version: 2 });
      return undefined;
    }

    if (message?.type === "list-conversations") {
      listProgress = { source: "api", loading: true, conversations: [], meta: null };
      collectWithScroll(message.cache, (progress) => {
        listProgress = { ...progress, loading: true };
      })
        .then((result) => {
          listProgress = { ...result, loading: false };
          sendResponse({ ok: true, conversations: result.conversations, meta: result.meta });
        })
        .catch((error) => {
          listProgress = { ...(listProgress || {}), loading: false, error: error.message };
          sendResponse({ ok: false, error: error.message });
        });
      return true;
    }

    if (message?.type === "list-progress") {
      sendResponse({ ok: true, progress: listProgress });
      return undefined;
    }

    if (message?.type === "extract-conversation") {
      (async () => {
        if (message.id) {
          return fetchConversationViaApi(message.id);
        }

        await parser.waitForConversation(document, 15000);
        await delay(500);
        return parser.parseConversation(document, location);
      })()
        .then((conversation) => sendResponse({ ok: true, conversation }))
        .catch((error) => sendResponse({ ok: false, error: error?.message || String(error) }));
      return true;
    }

    return undefined;
  });
})();
