(function () {
  const parser = globalThis.AgentMemoryWebDeepSeek;
  const { delay, requestJson } = globalThis.AgentMemoryWebRequest;
  const content = globalThis.AgentMemoryWebContent;

  if (!parser || !requestJson || !content) return;

  const MAX_PAGES = 200;

  function readToken() {
    try {
      const raw = localStorage.getItem("userToken");
      if (!raw) return "";
      try {
        const value = JSON.parse(raw);
        if (typeof value === "string") return value;
        return String(value?.value || value?.token || "");
      } catch {
        return raw;
      }
    } catch {
      return "";
    }
  }

  function headers() {
    const token = readToken();
    if (!token) throw new Error("未登录 DeepSeek；请先在浏览器里登录后再试。");
    return {
      accept: "application/json",
      authorization: `Bearer ${token}`,
      "x-client-platform": "web",
      "x-client-version": "2.2.0",
      "x-client-locale": "zh_CN",
      "x-client-bundle-id": "com.deepseek.chat",
    };
  }

  function explain(error) {
    if (error?.status === 401 || error?.status === 403) {
      const wrapped = new Error("DeepSeek 登录已失效；请重新登录后再试。");
      wrapped.status = error.status;
      return wrapped;
    }
    return error;
  }

  async function listConversations(onProgress) {
    const conversations = [];
    const seen = new Set();
    let last = null;

    for (let page = 0; page < MAX_PAGES; page += 1) {
      const params = new URLSearchParams();
      params.set("page_size", "100");

      if (last) {
        params.set("lte_cursor.pinned", String(last.pinned || false));
        if (last.updated_at != null) params.set("lte_cursor.updated_at", String(last.updated_at));
        if (last.id) params.set("lte_cursor.id", String(last.id));
      } else {
        params.set("lte_cursor.pinned", "false");
      }

      let response;
      try {
        response = await requestJson(`/api/v0/chat_session/fetch_page?${params.toString()}`, {
          headers: headers(),
          timeoutMs: 30000,
        });
      } catch (error) {
        throw explain(error);
      }

      const pageData = parser.sessionPage(response);
      let added = 0;

      for (const item of pageData.items) {
        if (seen.has(item.id)) continue;
        seen.add(item.id);
        conversations.push(item);
        added += 1;
      }

      onProgress?.({ conversations: [...conversations], meta: { pages: page + 1 } });

      if (pageData.rawCount < 100 || pageData.hasMore === false || pageData.items.length === 0) break;
      if (added === 0 || !pageData.lastRaw?.id) break;

      last = pageData.lastRaw;
      await delay(500 + Math.random() * 400);
    }

    return conversations;
  }

  async function fetchConversation(ref) {
    if (!ref?.id) throw new Error("会话缺少 ID。");

    let response;
    try {
      response = await requestJson(
        `/api/v0/chat/history_messages?chat_session_id=${encodeURIComponent(ref.id)}`,
        { headers: headers(), timeoutMs: 60000 },
      );
    } catch (error) {
      throw explain(error);
    }

    return parser.normalizeConversation(response, ref);
  }

  content.registerSite({ site: "deepseek", listConversations, fetchConversation });
})();
