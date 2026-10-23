import test from "node:test";
import assert from "node:assert/strict";

import "../content/chatgpt-parse.js";

const parser = globalThis.AgentMemoryWebChatGPT;

function makeNode({ role, text, markdown, time }) {
  return {
    getAttribute(name) {
      return name === "data-message-author-role" ? role : null;
    },

    querySelector(selector) {
      if (selector === ".markdown" && markdown != null) {
        return { innerText: markdown, textContent: markdown };
      }

      if (selector === `[data-message-author-role="${role}"]`) {
        return {
          getAttribute: () => role,
          innerText: text,
          textContent: text,
        };
      }

      if (selector === "time[datetime]" && time) {
        return { getAttribute: () => time };
      }

      return null;
    },

    innerText: text,
    textContent: text,
  };
}

function makeDocument({ title = "测试会话 | ChatGPT", nodes = [], links = [], readyNode = null } = {}) {
  return {
    title,

    querySelector(selector) {
      if (selector.includes("conversation-turn") || selector.includes("data-message-author-role")) {
        return readyNode;
      }
      return null;
    },

    querySelectorAll(selector) {
      if (selector.includes("conversation-turn") || selector.includes("data-message-author-role")) {
        return nodes;
      }
      if (selector.includes("/c/")) return links;
      return [];
    },
  };
}

const location = {
  origin: "https://chatgpt.com",
  pathname: "/c/abc",
  href: "https://chatgpt.com/c/abc",
};

test("parseConversation 取标题、会话 ID、角色与消息", () => {
  const document = makeDocument({
    nodes: [
      makeNode({ role: "user", text: "你好" }),
      makeNode({ role: "assistant", text: "你好，有什么可以帮你？", markdown: "你好，有什么可以帮你？", time: "2026-10-23T01:00:00.000Z" }),
    ],
  });

  const conversation = parser.parseConversation(document, location);

  assert.equal(conversation.site, "chatgpt");
  assert.equal(conversation.id, "abc");
  assert.equal(conversation.title, "测试会话");
  assert.equal(conversation.url, location.href);
  assert.equal(conversation.messages.length, 2);
  assert.equal(conversation.messages[0].role, "user");
  assert.equal(conversation.messages[1].role, "assistant");
  assert.equal(conversation.startedAt, "2026-10-23T01:00:00.000Z");
});

test("parseConversation 没有消息时报错", () => {
  assert.throws(
    () => parser.parseConversation(makeDocument({ nodes: [] }), location),
    /没有找到消息/,
  );
});

test("parseConversation 跳过缺角色或正文的节点", () => {
  const document = makeDocument({
    nodes: [
      makeNode({ role: "", text: "没有角色" }),
      makeNode({ role: "assistant", text: "" }),
      makeNode({ role: "user", text: "留下" }),
    ],
  });

  const conversation = parser.parseConversation(document, location);
  assert.equal(conversation.messages.length, 1);
  assert.equal(conversation.messages[0].text, "留下");
  assert.equal(conversation.title, "测试会话");
});

test("parseApiConversation 走 active branch 并跳过 system/tool", () => {
  const data = {
    conversation_id: "api-1",
    title: "接口会话",
    create_time: 1760000000,
    current_node: "n3",
    mapping: {
      root: { id: "root", parent: null, children: ["n1"] },
      n1: {
        id: "n1",
        parent: "root",
        children: ["n2"],
        message: {
          author: { role: "system" },
          content: { parts: ["系统"] },
          create_time: 1760000001,
        },
      },
      n2: {
        id: "n2",
        parent: "n1",
        children: ["n3"],
        message: {
          author: { role: "user" },
          content: { parts: ["你好"] },
          create_time: 1760000002,
        },
      },
      n3: {
        id: "n3",
        parent: "n2",
        children: [],
        message: {
          author: { role: "assistant" },
          content: { parts: ["你好，有什么可以帮你？"] },
          create_time: 1760000003,
        },
      },
    },
  };

  const conversation = parser.parseApiConversation(data, {
    id: "api-1",
    url: "https://chatgpt.com/c/api-1",
  });

  assert.equal(conversation.id, "api-1");
  assert.equal(conversation.title, "接口会话");
  assert.deepEqual(conversation.messages.map((message) => message.role), ["user", "assistant"]);
  assert.equal(conversation.messages[0].text, "你好");
});

test("listConversationLinks 去重、跳过空标题、拼绝对地址", () => {
  const links = [
    { getAttribute: () => "/c/a", textContent: "甲" },
    { getAttribute: () => "/c/b", textContent: "乙" },
    { getAttribute: () => "/c/a", textContent: "甲（重复）" },
    { getAttribute: (name) => (name === "href" ? "/c/c" : null), textContent: "  " },
    { getAttribute: () => "", textContent: "没有地址" },
  ];

  const list = parser.listConversationLinks(makeDocument({ links }), location);

  assert.deepEqual(list, [
    { id: "a", title: "甲", url: "https://chatgpt.com/c/a" },
    { id: "b", title: "乙", url: "https://chatgpt.com/c/b" },
  ]);
});

test("listConversationLinks 没有列表时报错", () => {
  assert.throws(
    () => parser.listConversationLinks(makeDocument({ links: [] }), location),
    /没有找到会话列表/,
  );
});

test("conversationIdFromUrl 取 /c/ 后的 ID", () => {
  assert.equal(parser.conversationIdFromUrl("/c/abc"), "abc");
  assert.equal(parser.conversationIdFromUrl("https://chatgpt.com/c/abc?x=1"), "abc");
  assert.equal(parser.conversationIdFromUrl("/"), "");
});

test("waitForConversation 等到节点出现", async () => {
  let calls = 0;
  const document = {
    querySelector() {
      calls += 1;
      return calls >= 3 ? {} : null;
    },
  };

  await parser.waitForConversation(document, 200, 5);
  assert.ok(calls >= 3);
});

test("waitForConversation 超时报错", async () => {
  await assert.rejects(
    parser.waitForConversation({ querySelector: () => null }, 30, 5),
    /等待会话加载超时/,
  );
});
