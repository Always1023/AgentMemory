(function (root, factory) {
  if (typeof module === "object" && module.exports) {
    module.exports = factory();
  } else {
    root.AgentMemoryWebKimi = factory();
  }
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  function oneLine(value) {
    return String(value ?? "").replace(/[\r\n]+/g, " ").replace(/\s+/g, " ").trim();
  }

  function truncate(value, max) {
    const chars = Array.from(String(value ?? ""));
    if (chars.length <= max) return chars.join("");
    return `${chars.slice(0, max).join("")}…`;
  }

  function sortTime(value) {
    if (value == null || value === "") return null;
    if (typeof value === "number") {
      if (!Number.isFinite(value) || value <= 0) return null;
      return value < 1e12 ? value * 1000 : value;
    }

    const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:?\d{2})?$/.exec(String(value));
    if (!match) {
      const fallback = Date.parse(String(value));
      return Number.isNaN(fallback) ? null : fallback;
    }

    const base = Date.parse(`${match[1]}${match[3] || "Z"}`);
    if (!Number.isFinite(base)) return null;
    const fraction = match[2] ? Number(`0.${match[2]}`) : 0;
    return base + fraction * 1000;
  }

  function toIso(value) {
    const time = sortTime(value);
    return time == null || time <= 0 ? null : new Date(time).toISOString();
  }

  function roleOf(value) {
    const role = String(value ?? "").trim().toLowerCase();
    if (role === "user" || role === "human") return "user";
    if (role === "assistant" || role === "ai" || role === "bot") return "assistant";
    if (role === "system") return "system";
    if (role === "tool" || role === "function") return "tool";
    return role;
  }

  function plainText(value) {
    if (value == null) return "";
    if (typeof value === "string") return value;
    if (typeof value === "number" || typeof value === "boolean") return String(value);
    if (Array.isArray(value)) return value.map(plainText).filter(Boolean).join("\n\n");
    if (typeof value !== "object") return "";

    for (const key of ["content", "text", "msg", "value"]) {
      if (value[key] != null && value[key] !== value) {
        const text = plainText(value[key]);
        if (text) return text;
      }
    }

    return "";
  }

  function quoted(label, text) {
    const body = String(text ?? "").trim();
    if (!body) return "";
    const lines = body.split("\n").map((line) => `> ${line}`).join("\n");
    return `> **${label}**\n${lines}`;
  }

  const CITATION_MARKER = /cite[\s\S]*?/g;

  function citationInfo(reference) {
    const item = reference?.items?.[0];
    const base = item?.search?.base || item?.searchResult?.base || item?.base || {};
    return {
      title: oneLine(base.title),
      url: base.url || "",
    };
  }

  function createReferenceCollector() {
    const references = [];
    const indexByKey = new Map();

    return {
      add(title, url) {
        const key = `${url}\n${title}`;
        if (indexByKey.has(key)) return indexByKey.get(key);
        const index = references.length + 1;
        indexByKey.set(key, index);
        references.push({ index, title, url });
        return index;
      },
      render() {
        return references.map(({ index, title, url }) => {
          return `[${index}] ${title ? `${title} - ` : ""}${url}`;
        }).join("\n");
      },
      get size() {
        return references.length;
      },
    };
  }

  function replaceCitations(text, message, collector) {
    const body = String(text ?? "");
    if (!body) return "";

    const references = Array.isArray(message?.references) ? message.references : [];
    const byMarker = new Map();
    for (const reference of references) {
      if (reference?.matchedText) byMarker.set(reference.matchedText, reference);
    }

    return body.replace(CITATION_MARKER, (marker) => {
      const info = citationInfo(byMarker.get(marker));
      if (!info.url) return "";
      return `[${collector.add(info.title, info.url)}]`;
    });
  }

  function messageText(message) {
    if (!message || typeof message !== "object") return "";

    const collector = createReferenceCollector();
    let body;

    if (!Array.isArray(message.blocks)) {
      body = replaceCitations(plainText(message).trim(), message, collector);
    } else {
      const thoughts = [];
      const texts = [];

      for (const block of message.blocks) {
        if (!block || typeof block !== "object") continue;

        const think = plainText(block.think).trim();
        if (think) thoughts.push(think);

        const text = replaceCitations(plainText(block.text).trim(), message, collector);
        if (text) texts.push(text);
      }

      const sections = [];
      if (thoughts.length) sections.push(quoted("思考过程", thoughts.join("\n\n")));
      if (texts.length) sections.push(texts.join("\n\n"));
      body = sections.join("\n\n").trim();
    }

    if (!body) return "";
    const references = collector.size ? `\n\n参考来源：\n${collector.render()}` : "";
    return `${body}${references}`;
  }

  function normalizeFeeds(response) {
    const items = Array.isArray(response?.items)
      ? response.items
      : (Array.isArray(response?.data?.items) ? response.data.items : []);
    const conversations = [];

    for (const item of items) {
      const chat = item?.chat || item?.item?.value || item?.feed?.chat || null;
      const id = String(chat?.id ?? "").trim();
      if (!id) continue;

      conversations.push({
        id,
        title: oneLine(chat?.name || chat?.title),
        url: `https://www.kimi.com/chat/${id}`,
        startedAt: toIso(chat?.createTime || chat?.createdAt),
        updatedAt: toIso(chat?.updateTime || chat?.updatedAt),
      });
    }

    return {
      items: conversations,
      rawCount: items.length,
      nextPageToken: String(
        response?.nextPageToken
        || response?.next_page_token
        || response?.data?.nextPageToken
        || "",
      ),
    };
  }

  function normalizeMessages(raw) {
    const messages = [];

    for (const message of Array.isArray(raw) ? raw : []) {
      const text = messageText(message);
      if (!text) continue;

      messages.push({
        role: roleOf(message?.role),
        text,
        time: toIso(message?.createTime || message?.createdAt),
        sortTime: sortTime(message?.createTime || message?.createdAt),
      });
    }

    const withTime = messages.filter((message) => message.sortTime != null).length;
    if (withTime >= 2 && withTime === messages.length) {
      messages.sort((a, b) => a.sortTime - b.sortTime);
    }

    return messages.map(({ sortTime: _sortTime, ...message }) => message);
  }

  function normalizeConversation(chatResponse, messagesResponse, ref) {
    const chat = chatResponse?.chat || chatResponse?.data?.chat || chatResponse || {};
    const messages = normalizeMessages(messagesResponse?.messages);
    const id = String(ref?.id || chat?.id || "").trim();
    const firstUser = messages.find((message) => message.role === "user");
    const title = oneLine(chat?.name)
      || oneLine(ref?.title)
      || truncate(firstUser?.text || "", 60)
      || "未命名会话";
    const startedAt = toIso(chat?.createTime || chat?.createdAt)
      || messages.find((message) => message.time)?.time
      || toIso(ref?.startedAt)
      || new Date().toISOString();

    return {
      site: "kimi",
      id,
      title,
      url: ref?.url || `https://www.kimi.com/chat/${id}`,
      startedAt,
      capturedAt: new Date().toISOString(),
      messages,
    };
  }

  return {
    messageText,
    normalizeConversation,
    normalizeFeeds,
    normalizeMessages,
  };
});
