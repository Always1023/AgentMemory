(function () {
  const parser = globalThis.AgentMemoryWebKimi;
  const { delay, requestJson } = globalThis.AgentMemoryWebRequest;
  const content = globalThis.AgentMemoryWebContent;

  if (!parser || !requestJson || !content) return;

  const MAX_PAGES = 200;

  function readToken() {
    try {
      const raw = localStorage.getItem("access_token");
      if (!raw) return "";
      try {
        const value = JSON.parse(raw);
        return typeof value === "string" ? value : String(value?.value || value?.token || "");
      } catch {
        return raw;
      }
    } catch {
      return "";
    }
  }

  function headers() {
    const token = readToken();
    if (!token) throw new Error("未登录 Kimi；请先在浏览器里登录后再试。");

    return {
      accept: "*/*",
      "content-type": "application/json",
      "connect-protocol-version": "1",
      "x-msh-platform": "web",
      "x-msh-version": "2.0.0",
      "r-timezone": Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai",
      "x-language": navigator.language || "zh-CN",
      authorization: `Bearer ${token}`,
    };
  }

  function explain(error) {
    if (error?.status === 401 || error?.status === 403) {
      const wrapped = new Error("Kimi 登录已失效；请重新登录后再试。");
      wrapped.status = error.status;
      return wrapped;
    }
    return error;
  }

  async function post(path, body, timeoutMs = 30000) {
    try {
      return await requestJson(path, {
        method: "POST",
        headers: headers(),
        body: body || {},
        timeoutMs,
      });
    } catch (error) {
      throw explain(error);
    }
  }

  async function listConversations(onProgress) {
    const conversations = [];
    const seen = new Set();
    let pageToken = "";
    let pageSize = 15;

    for (let page = 0; page < MAX_PAGES; page += 1) {
      const response = await post("/apiv2/kimi.gateway.feed.v1.FeedService/ListFeeds", {
        pageToken,
        pageSize,
        projectId: "",
        filterTypes: [1],
        includePinned: false,
      });

      const pageData = parser.normalizeFeeds(response);
      let added = 0;

      for (const item of pageData.items) {
        if (seen.has(item.id)) continue;
        seen.add(item.id);
        conversations.push(item);
        added += 1;
      }

      onProgress?.({ conversations: [...conversations], meta: { pages: page + 1 } });

      if (!pageData.nextPageToken || pageData.rawCount === 0 || added === 0) break;

      pageToken = pageData.nextPageToken;
      pageSize = 50;
      await delay(500 + Math.random() * 400);
    }

    return conversations;
  }

  async function fetchConversation(ref) {
    if (!ref?.id) throw new Error("会话缺少 ID。");

    let chatResponse = {};
    try {
      chatResponse = await post("/apiv2/kimi.gateway.chat.v1.ChatService/GetChat", {
        chatId: ref.id,
      });
    } catch (error) {
      if (error?.status === 401 || error?.status === 403) throw error;
    }

    const messages = [];
    const seen = new Set();
    let pageToken = "";

    for (let page = 0; page < MAX_PAGES; page += 1) {
      const response = await post("/apiv2/kimi.gateway.chat.v1.ChatService/ListMessages", {
        chatId: ref.id,
        pageSize: 100,
        pageToken,
      }, 60000);

      for (const message of (response?.messages || [])) {
        const key = message?.id || `${message?.role}:${message?.createTime}:${messages.length}`;
        if (seen.has(key)) continue;
        seen.add(key);
        messages.push(message);
      }

      const next = response?.nextPageToken || response?.next_page_token || "";
      if (!next) break;

      pageToken = next;
      await delay(1200 + Math.random() * 600);
    }

    return parser.normalizeConversation(chatResponse, { messages }, ref);
  }

  content.registerSite({ site: "kimi", listConversations, fetchConversation });
})();
