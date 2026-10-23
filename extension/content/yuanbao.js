(function () {
  const parser = globalThis.AgentMemoryWebYuanbao;
  const { delay, requestJson } = globalThis.AgentMemoryWebRequest;
  const content = globalThis.AgentMemoryWebContent;

  if (!parser || !requestJson || !content) return;

  const MAX_PAGES = 200;
  const HEADERS = {
    accept: "application/json",
    "content-type": "application/json",
    "x-requested-with": "XMLHttpRequest",
  };

  function currentAgentId() {
    const parts = location.pathname.split("/").filter(Boolean);
    if (parts[0] === "chat" && parts.length >= 3) return parts[1];
    return parser.DEFAULT_AGENT;
  }

  function explain(error) {
    if (error?.status === 401 || error?.status === 403) {
      const wrapped = new Error("腾讯元宝登录已失效；请重新登录后再试。");
      wrapped.status = error.status;
      return wrapped;
    }
    return error;
  }

  async function post(path, body, timeoutMs = 30000) {
    let data;

    try {
      data = await requestJson(path, {
        method: "POST",
        headers: HEADERS,
        body: body || {},
        timeoutMs,
      });
    } catch (error) {
      throw explain(error);
    }

    const code = data?.code ?? data?.error?.code;
    if (code != null && ![0, "0", 200, "200"].includes(code)) {
      const message = data?.message || data?.msg || data?.error?.message || `元宝接口返回 ${code}。`;
      throw new Error(message);
    }

    return data;
  }

  function truthyFlag(value) {
    if (typeof value === "string") {
      return !["", "0", "false", "no"].includes(value.trim().toLowerCase());
    }
    return Boolean(value);
  }

  async function listConversations(onProgress) {
    const agentId = currentAgentId();
    const conversations = [];
    const seen = new Set();
    let offset = 0;
    let total = Infinity;

    for (let page = 0; page < MAX_PAGES && offset < total; page += 1) {
      const response = await post("/api/user/agent/conversation/list", {
        agentId,
        offset,
        limit: 40,
        filterGoodQuestion: true,
      });

      const pageData = parser.normalizeList(response, agentId);
      let added = 0;

      for (const item of pageData.items) {
        if (seen.has(item.id)) continue;
        seen.add(item.id);
        conversations.push(item);
        added += 1;
      }

      total = pageData.total ?? total;

      onProgress?.({
        conversations: [...conversations],
        meta: { pages: page + 1, total: Number.isFinite(total) ? total : null },
      });

      if (pageData.rawCount === 0 || added === 0) break;

      offset += pageData.rawCount;
      await delay(500 + Math.random() * 400);
    }

    return conversations;
  }

  async function fetchConversation(ref) {
    if (!ref?.id) throw new Error("会话缺少 ID。");

    const { agentId, conversationId } = parser.unpackId(ref.id, currentAgentId());
    if (!conversationId) throw new Error("会话缺少 ID。");

    const all = [];
    const seen = new Set();
    let offset = 0;
    let meta = null;

    for (let page = 0; page < MAX_PAGES; page += 1) {
      const response = await post("/api/user/agent/conversation/v1/detail", {
        conversationId,
        offset,
        limit: 50,
        agentId,
      }, 60000);

      if (!meta) meta = response;

      const batch = Array.isArray(response?.convs) ? response.convs : [];
      let added = 0;
      let minIndex = Infinity;

      for (const conversation of batch) {
        const index = Number(conversation?.index);
        if (Number.isFinite(index)) minIndex = Math.min(minIndex, index);

        const key = conversation?.id || `${conversation?.speaker || ""}:${index}`;
        if (!key || seen.has(key)) continue;

        seen.add(key);
        all.push(conversation);
        added += 1;
      }

      if (!truthyFlag(response?.hasMore) || batch.length === 0 || added === 0) break;
      if (!Number.isFinite(minIndex) || minIndex === offset) break;

      offset = minIndex;
      await delay(700 + Math.random() * 400);
    }

    return parser.normalizeConversation({ ...(meta || {}), convs: all }, ref);
  }

  content.registerSite({ site: "yuanbao", listConversations, fetchConversation });
})();
