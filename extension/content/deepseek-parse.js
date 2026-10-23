(function (root, factory) {
  if (typeof module === "object" && module.exports) {
    module.exports = factory();
  } else {
    root.AgentMemoryWebDeepSeek = factory();
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

  function toIso(value) {
    if (value == null || value === "") return null;
    if (value instanceof Date) {
      return Number.isNaN(value.getTime()) ? null : value.toISOString();
    }
    if (typeof value === "number" || /^\d+$/.test(String(value).trim())) {
      const number = Number(value);
      const date = new Date(number < 1e12 ? number * 1000 : number);
      return Number.isNaN(date.getTime()) ? null : date.toISOString();
    }
    const date = new Date(String(value));
    return Number.isNaN(date.getTime()) ? null : date.toISOString();
  }

  function roleOf(value) {
    const role = String(value ?? "").trim().toLowerCase();
    if (role === "user" || role === "human") return "user";
    if (role === "assistant" || role === "ai" || role === "bot") return "assistant";
    if (role === "system") return "system";
    if (role === "tool" || role === "function") return "tool";
    return role;
  }

  function pickText(value) {
    if (value == null) return "";
    if (typeof value === "string") return value;
    if (typeof value === "number" || typeof value === "boolean") return String(value);
    if (Array.isArray(value)) return value.map(pickText).filter(Boolean).join("\n\n");
    if (typeof value !== "object") return "";

    for (const key of ["content", "text", "msg", "value", "thinking", "data"]) {
      if (value[key] != null && value[key] !== value) {
        const text = pickText(value[key]);
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

  function fragmentText(fragment) {
    if (!fragment || typeof fragment !== "object") return "";
    const type = String(fragment.type || "").toUpperCase();
    const text = pickText(fragment.content ?? fragment.text ?? fragment.msg);

    if (!text) return "";
    if (type.includes("THINK")) return quoted("思考过程", text);
    if (type === "TIP" || type === "SEARCH" || type === "TOOL_SEARCH") return "";
    return text;
  }

  function blockText(block) {
    if (block == null) return "";
    if (typeof block === "string") return block;
    if (typeof block !== "object") return String(block);

    const type = String(block.type || block.kind || "").toLowerCase();
    const text = pickText(block);

    if (type.includes("think")) return quoted("思考过程", text);
    if (type === "image") {
      const url = block.url || block.src || block.image_url || "";
      return url ? `![图片](${url})` : text;
    }
    if (type === "file" || type === "pdf") {
      const name = block.name || block.fileName || block.filename || "";
      const url = block.url || "";
      return `[附件] ${name || url}${url && name ? ` ${url}` : ""}`.trim();
    }
    return text;
  }

  function messageText(message) {
    if (!message || typeof message !== "object") return "";

    if (Array.isArray(message.fragments) && message.fragments.length > 0) {
      const parts = message.fragments.map(fragmentText).filter(Boolean);
      if (parts.length > 0) return parts.join("\n\n").trim();
    }

    if (Array.isArray(message.content_blocks) && message.content_blocks.length > 0) {
      const parts = message.content_blocks.map(blockText).filter(Boolean);
      if (parts.length > 0) return parts.join("\n\n").trim();
    }

    for (const key of ["content", "text", "message_content", "raw_content"]) {
      const text = pickText(message[key]);
      if (text) return text.trim();
    }

    return "";
  }

  function messageTime(message) {
    if (!message || typeof message !== "object") return null;
    return toIso(message.inserted_at ?? message.created_at ?? message.create_time ?? message.updated_at);
  }

  function normalizeMessages(raw) {
    const list = Array.isArray(raw) ? raw : [];
    const messages = [];

    for (const message of list) {
      const text = messageText(message);
      if (!text) continue;
      messages.push({
        role: roleOf(message?.role),
        text,
        time: messageTime(message),
      });
    }

    const withTime = messages.filter((message) => message.time).length;
    if (withTime >= 2 && withTime === messages.length) {
      messages.sort((a, b) => (a.time < b.time ? -1 : a.time > b.time ? 1 : 0));
    }

    return messages;
  }

  function sessionPage(response) {
    const biz = response?.data?.biz_data || response?.biz_data || response || {};
    const sessions = Array.isArray(biz.chat_sessions) ? biz.chat_sessions : [];
    const items = [];

    for (const session of sessions) {
      const id = String(session?.id ?? "").trim();
      if (!id) continue;
      items.push({
        id,
        title: oneLine(session?.title),
        url: `https://chat.deepseek.com/a/chat/s/${id}`,
        startedAt: toIso(session?.inserted_at ?? session?.created_at ?? session?.updated_at),
      });
    }

    return {
      items,
      rawCount: sessions.length,
      hasMore: biz.has_more !== false,
      lastRaw: sessions[sessions.length - 1] || null,
    };
  }

  function normalizeConversation(detail, ref) {
    const biz = detail?.data?.biz_data || detail?.biz_data || detail || {};
    const session = biz.chat_session || biz.chatSession || {};
    const rawMessages = biz.chat_messages || biz.chatMessages || biz.messages || [];
    const messages = normalizeMessages(rawMessages);
    const id = String(ref?.id || session?.id || "").trim();
    const firstUser = messages.find((message) => message.role === "user");
    const title = oneLine(session?.title)
      || oneLine(ref?.title)
      || truncate(firstUser?.text || "", 60)
      || "未命名会话";
    const startedAt = toIso(session?.inserted_at ?? session?.created_at ?? session?.updated_at)
      || messages.find((message) => message.time)?.time
      || toIso(ref?.startedAt)
      || new Date().toISOString();

    return {
      site: "deepseek",
      id,
      title,
      url: ref?.url || `https://chat.deepseek.com/a/chat/s/${id}`,
      startedAt,
      capturedAt: new Date().toISOString(),
      messages,
    };
  }

  return {
    messageText,
    normalizeConversation,
    normalizeMessages,
    sessionPage,
  };
});
