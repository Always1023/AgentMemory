(function (root, factory) {
  if (typeof module === "object" && module.exports) {
    module.exports = factory();
  } else {
    root.AgentMemoryWebYuanbao = factory();
  }
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  const DEFAULT_AGENT = "naQivTmsDa";

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

    if (typeof value === "number" || /^-?\d+(?:\.\d+)?$/.test(String(value).trim())) {
      const number = Number(value);
      if (!Number.isFinite(number) || number <= 0) return null;
      const date = new Date(number < 1e12 ? number * 1000 : number);
      return Number.isNaN(date.getTime()) ? null : date.toISOString();
    }

    const date = new Date(String(value));
    return Number.isNaN(date.getTime()) ? null : date.toISOString();
  }

  function firstIso(...values) {
    for (const value of values) {
      const iso = toIso(value);
      if (iso) return iso;
    }
    return null;
  }

  function roleOf(value) {
    const role = String(value ?? "").trim().toLowerCase();
    if (role === "human" || role === "user") return "user";
    if (role === "ai" || role === "assistant" || role === "bot") return "assistant";
    return role;
  }

  function pickText(value) {
    if (value == null) return "";
    if (typeof value === "string") return value;
    if (typeof value === "number" || typeof value === "boolean") return String(value);
    if (Array.isArray(value)) return value.map(pickText).filter(Boolean).join("\n\n");
    if (typeof value !== "object") return "";

    for (const key of ["msg", "content", "text", "value"]) {
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

  function collectDocs(docs, refs) {
    for (const doc of (Array.isArray(docs) ? docs : [])) {
      const url = doc?.url || doc?.link || "";
      if (!url) continue;

      const rawIndex = Number(doc.index ?? doc.idx ?? refs.size + 1);
      const index = Number.isFinite(rawIndex) && rawIndex > 0 ? Math.trunc(rawIndex) : refs.size + 1;
      if (!refs.has(index)) {
        refs.set(index, {
          index,
          title: oneLine(doc.title || doc.name || ""),
          url,
        });
      }
    }
  }

  function attachmentText(item) {
    const name = oneLine(item?.fileName || item?.name || "");
    const url = item?.url || "";
    if (!url) return "";
    return `[附件] ${name || url}${name ? ` ${url}` : ""}`;
  }

  function speechParts(conversation) {
    const thoughts = [];
    const texts = [];
    const refs = new Map();

    for (const speech of (Array.isArray(conversation?.speechesV2) ? conversation.speechesV2 : [])) {
      const speechIsThought = String(speech?.speechType || "").toLowerCase() === "thought";
      let speechText = "";

      for (const item of (Array.isArray(speech?.content) ? speech.content : [])) {
        const type = String(item?.type || "").toLowerCase();

        if (type === "think" || type === "thought") {
          const text = pickText(item).trim();
          if (text) thoughts.push(text);
          continue;
        }

        if (type === "deepsearch" || type === "deep_search") {
          for (const sub of (Array.isArray(item.contents) ? item.contents : [])) {
            const subType = String(sub?.type || "").toLowerCase();
            if (subType === "text") {
              const text = pickText(sub).trim();
              if (text) thoughts.push(text);
            }
            if (Array.isArray(sub?.docs)) collectDocs(sub.docs, refs);
          }
          continue;
        }

        if (type === "searchguid" || type === "search_guid") {
          if (Array.isArray(item.docs)) collectDocs(item.docs, refs);
          continue;
        }

        if (type === "image" || type === "code" || type === "pdf") {
          const text = attachmentText(item);
          if (text) speechText += `${speechText ? "\n\n" : ""}${text}`;
          continue;
        }

        const text = pickText(item).trim();
        if (!text) continue;
        if (speechIsThought) thoughts.push(text);
        else speechText += `${speechText ? "\n\n" : ""}${text}`;
      }

      if (!speechText && !speechIsThought && speech?.newPrompt) {
        speechText = pickText(speech.newPrompt).trim();
      }

      if (speechText) texts.push(speechText);
    }

    return {
      thoughts,
      text: texts.join("\n\n").trim(),
      refs: [...refs.values()].sort((a, b) => a.index - b.index),
    };
  }

  function referenceBlock(refs) {
    return refs.map((ref, position) => {
      const title = ref.title ? `${ref.title} - ` : "";
      return `[${position + 1}] ${title}${ref.url}`;
    }).join("\n");
  }

  function replaceCitationMarkers(text, numbers) {
    return String(text ?? "").replace(/\[citation:(\d+)\]/g, (_, value) => {
      const number = numbers.get(Number(value));
      return number ? `[${number}]` : "";
    });
  }

  function speechText(conversation) {
    const parts = speechParts(conversation);
    const numbers = new Map(parts.refs.map((ref, position) => [ref.index, position + 1]));
    const sections = [];

    if (parts.thoughts.length) {
      sections.push(quoted("思考过程", replaceCitationMarkers(parts.thoughts.join("\n\n"), numbers)));
    }

    if (parts.text) {
      sections.push(replaceCitationMarkers(parts.text, numbers));
    }

    if (parts.refs.length) {
      sections.push(`参考来源：\n${referenceBlock(parts.refs)}`);
    }

    return sections.join("\n\n").trim();
  }

  function messageTime(conversation) {
    return firstIso(
      conversation?.createTime,
      conversation?.createdAt,
      conversation?.speechesV2?.[0]?.createTime,
    );
  }

  function unpackId(value, defaultAgent = DEFAULT_AGENT) {
    const parts = String(value || "").split("/").filter(Boolean);
    if (parts.length >= 2) {
      return { agentId: parts[0], conversationId: parts[1] };
    }
    return { agentId: defaultAgent, conversationId: parts[0] || "" };
  }

  function normalizeList(response, agentId = DEFAULT_AGENT) {
    const conversations = Array.isArray(response?.conversations)
      ? response.conversations
      : (Array.isArray(response?.data?.conversations) ? response.data.conversations : []);
    const items = [];

    for (const conversation of conversations) {
      const rawId = String(conversation?.id ?? "").trim();
      if (!rawId) continue;

      const owner = String(conversation?.agentId || agentId || DEFAULT_AGENT);
      const id = `${owner}/${rawId}`;

      items.push({
        id,
        title: oneLine(conversation?.title || conversation?.sessionTitle),
        url: `https://yuanbao.tencent.com/chat/${owner}/${rawId}`,
        startedAt: firstIso(conversation?.firstRepliedAt, conversation?.createTime),
        updatedAt: firstIso(conversation?.lastRepliedAt, conversation?.lastRepliedDatetime),
      });
    }

    const rawTotal = response?.pagination?.totalResults;
    const total = Number(rawTotal);

    return {
      items,
      rawCount: conversations.length,
      total: rawTotal != null && Number.isFinite(total) && total >= 0 ? total : null,
    };
  }

  function normalizeConversation(detail, ref) {
    const conversations = Array.isArray(detail?.convs) ? [...detail.convs] : [];
    conversations.sort((a, b) => (Number(a?.index) || 0) - (Number(b?.index) || 0));

    const messages = [];

    for (const conversation of conversations) {
      const text = speechText(conversation);
      if (!text) continue;

      messages.push({
        role: roleOf(conversation?.speaker),
        text,
        time: messageTime(conversation),
      });
    }

    const id = String(ref?.id || detail?.conversationId || "").trim();
    const firstUser = messages.find((message) => message.role === "user");
    const title = oneLine(detail?.sessionTitle)
      || oneLine(detail?.title)
      || oneLine(ref?.title)
      || truncate(firstUser?.text || "", 60)
      || "未命名会话";
    const startedAt = firstIso(detail?.firstRepliedAt, detail?.createTime)
      || messages.find((message) => message.time)?.time
      || firstIso(ref?.startedAt)
      || new Date().toISOString();

    return {
      site: "yuanbao",
      id,
      title,
      url: ref?.url || `https://yuanbao.tencent.com/chat/${id}`,
      startedAt,
      capturedAt: new Date().toISOString(),
      messages,
    };
  }

  return {
    DEFAULT_AGENT,
    normalizeConversation,
    normalizeList,
    speechParts,
    speechText,
    unpackId,
  };
});
