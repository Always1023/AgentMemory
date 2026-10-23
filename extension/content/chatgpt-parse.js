(function (root, factory) {
  if (typeof module === "object" && module.exports) {
    module.exports = factory();
  } else {
    root.AgentMemoryWebChatGPT = factory();
  }
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  function oneLine(value) {
    return String(value ?? "").replace(/[\r\n]+/g, " ").replace(/\s+/g, " ").trim();
  }

  function conversationIdFromUrl(value) {
    const match = String(value ?? "").match(/\/c\/([^/?#]+)/);
    if (!match) return "";

    try {
      return decodeURIComponent(match[1]);
    } catch {
      return match[1];
    }
  }

  function cleanTitle(value) {
    const title = oneLine(value).replace(/\s*[|·-]\s*ChatGPT\s*$/i, "").trim();
    return /^ChatGPT$/i.test(title) ? "" : title;
  }

  function truncate(value, max) {
    const chars = Array.from(String(value ?? ""));
    if (chars.length <= max) return chars.join("");
    return `${chars.slice(0, max).join("")}…`;
  }

  function collectMessageNodes(document) {
    const articles = Array.from(document.querySelectorAll?.('article[data-testid^="conversation-turn"]') || []);
    if (articles.length > 0) return articles;

    return Array.from(document.querySelectorAll?.("[data-message-author-role]") || []);
  }

  function nodeRole(node) {
    const direct = node.getAttribute?.("data-message-author-role");
    if (direct) return String(direct).trim().toLowerCase();

    const child = node.querySelector?.("[data-message-author-role]");
    return String(child?.getAttribute?.("data-message-author-role") || "").trim().toLowerCase();
  }

  function nodeText(node, role) {
    const selector = role ? `[data-message-author-role="${role}"]` : "";
    const content = node.querySelector?.(".markdown")
      || (selector ? node.querySelector?.(selector) : null)
      || node;

    let text = String(content?.innerText ?? content?.textContent ?? "").trim();
    text = text.replace(/^(You said|ChatGPT said|你说|ChatGPT 说)\s*:?\s*/i, "").trim();
    return text;
  }

  function nodeTime(node) {
    const time = node.querySelector?.("time[datetime]");
    const value = time?.getAttribute?.("datetime");
    if (!value) return null;

    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : date.toISOString();
  }

  function parseConversation(document, location) {
    const nodes = collectMessageNodes(document);
    const messages = [];

    for (const node of nodes) {
      const role = nodeRole(node);
      const text = nodeText(node, role);
      if (!role || !text) continue;

      messages.push({ role, text, time: nodeTime(node) });
    }

    if (messages.length === 0) {
      throw new Error("没有找到消息；请确认会话已加载完成。");
    }

    const firstUser = messages.find((message) => message.role === "user");
    const title = cleanTitle(document?.title)
      || truncate(firstUser?.text || "", 80)
      || "未命名会话";
    const id = conversationIdFromUrl(location?.pathname)
      || conversationIdFromUrl(location?.href)
      || "";
    const startedAt = messages.find((message) => message.time)?.time || new Date().toISOString();

    return {
      site: "chatgpt",
      id,
      title,
      url: location?.href || "",
      startedAt,
      capturedAt: new Date().toISOString(),
      messages,
    };
  }

  function listConversationLinks(document, location) {
    let anchors = Array.from(document?.querySelectorAll?.('nav a[href*="/c/"]') || []);
    if (anchors.length === 0) {
      anchors = Array.from(document?.querySelectorAll?.('a[href*="/c/"]') || []);
    }

    const seen = new Set();
    const conversations = [];
    const origin = location?.origin || "https://chatgpt.com";

    for (const anchor of anchors) {
      const href = anchor.getAttribute?.("href") || "";
      if (!href) continue;

      let url;
      try {
        url = new URL(href, origin).href;
      } catch {
        continue;
      }

      const id = conversationIdFromUrl(url);
      if (!id || seen.has(id)) continue;

      const title = oneLine(anchor.textContent)
        || oneLine(anchor.getAttribute?.("aria-label"))
        || oneLine(anchor.getAttribute?.("title"));
      if (!title) continue;

      seen.add(id);
      conversations.push({ id, title, url });
    }

    if (conversations.length === 0) {
      throw new Error("没有找到会话列表；请确认已登录 ChatGPT 并打开侧边栏。");
    }

    return conversations;
  }

  function parseApiConversation(data, ref = {}) {
    const mapping = data?.mapping || {};
    let nodeId = data?.current_node;
    const nodes = [];

    while (nodeId && mapping[nodeId]) {
      nodes.push(mapping[nodeId]);
      nodeId = mapping[nodeId].parent;
    }

    nodes.reverse();
    const messages = [];

    for (const node of nodes) {
      const message = node?.message;
      if (!message) continue;

      const role = String(message.author?.role || "").trim().toLowerCase();
      if (!role || role === "system" || role === "tool") continue;

      const parts = Array.isArray(message.content?.parts) ? message.content.parts : [];
      const text = parts
        .filter((part) => typeof part === "string")
        .join("\n")
        .trim();
      if (!text) continue;

      const time = message.create_time
        ? new Date(message.create_time * 1000).toISOString()
        : null;
      messages.push({ role, text, time });
    }

    if (messages.length === 0) {
      throw new Error("接口返回的会话没有消息。");
    }

    const firstUser = messages.find((message) => message.role === "user");
    const id = String(data?.conversation_id || ref.id || "").trim();
    const title = String(data?.title || "").trim()
      || truncate(firstUser?.text || "", 80)
      || "未命名会话";
    const startedAt = data?.create_time
      ? new Date(data.create_time * 1000).toISOString()
      : (messages.find((message) => message.time)?.time || new Date().toISOString());
    const origin = typeof location !== "undefined" ? location.origin : "https://chatgpt.com";

    return {
      site: "chatgpt",
      id,
      title,
      url: ref.url || `${origin}/c/${id}`,
      startedAt,
      capturedAt: new Date().toISOString(),
      messages,
    };
  }

  function waitForConversation(document, timeoutMs = 15000, intervalMs = 250) {
    return new Promise((resolve, reject) => {
      const startedAt = Date.now();

      const check = () => {
        const node = document?.querySelector?.('article[data-testid^="conversation-turn"], [data-message-author-role]');
        if (node) {
          resolve();
          return;
        }

        if (Date.now() - startedAt >= timeoutMs) {
          reject(new Error("等待会话加载超时；请确认页面已打开。"));
          return;
        }

        setTimeout(check, intervalMs);
      };

      check();
    });
  }

  return {
    cleanTitle,
    collectMessageNodes,
    conversationIdFromUrl,
    listConversationLinks,
    oneLine,
    parseApiConversation,
    parseConversation,
    truncate,
    waitForConversation,
  };
});
