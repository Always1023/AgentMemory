import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const SCRIPTS = [
  "content/request.js",
  "content/api-content.js",
];

function jsonResponse(body, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async text() {
      return JSON.stringify(body);
    },
  };
}

function createHarness({ parser, adapter, pathname = "/", tokens = {}, fetch }) {
  const listeners = [];
  const calls = [];

  const context = vm.createContext({
    console,
    setTimeout,
    clearTimeout,
    Intl,
    Date,
    URLSearchParams,
    AbortController,
    navigator: { language: "zh-CN" },
    location: { pathname, href: `https://example.test${pathname}` },
    localStorage: {
      getItem(key) {
        return tokens[key] ?? null;
      },
    },
    chrome: {
      runtime: {
        onMessage: {
          addListener(listener) {
            listeners.push(listener);
          },
        },
      },
    },
    fetch: async (url, options = {}) => {
      calls.push({ url: String(url), options });
      return fetch(String(url), options);
    },
  });

  for (const file of [...SCRIPTS, parser, adapter]) {
    const source = readFileSync(new URL(`../${file}`, import.meta.url), "utf8");
    vm.runInContext(source, context, { filename: file });
  }

  assert.equal(listeners.length, 1, "站点脚本应注册一个消息监听器");

  return {
    calls,
    send(message) {
      return new Promise((resolve) => {
        listeners[0](message, {}, resolve);
      });
    },
  };
}

function callBody(call) {
  return JSON.parse(call.options.body);
}

test("Kimi content script 分页读取列表与消息，并附带引用", async () => {
  let feedsPage = 0;
  const kimi = createHarness({
    parser: "content/kimi-parse.js",
    adapter: "content/kimi.js",
    tokens: { access_token: "kimi-token" },
    fetch: async (url, options) => {
      if (url.includes("ListFeeds")) {
        feedsPage += 1;
        if (feedsPage === 1) {
          return jsonResponse({
            items: [{ chat: { id: "c1", name: "会话一", createTime: "2026-10-23T08:00:00.100000Z" } }],
            nextPageToken: "feeds-2",
          });
        }
        return jsonResponse({
          items: [
            { item: { value: { id: "c2", name: "会话二", createTime: "2026-10-23T09:00:00.000000Z" } } },
            { chat: { id: "c1", name: "会话一" } },
          ],
        });
      }

      if (url.includes("GetChat")) {
        return jsonResponse({ chat: { id: "c1", name: "会话一" } });
      }

      if (url.includes("ListMessages")) {
        const body = JSON.parse(options.body || "{}");
        if (!body.pageToken) {
          return jsonResponse({
            messages: [{
              id: "m2",
              role: "assistant",
              createTime: "2026-10-23T08:00:00.200000Z",
              blocks: [
                { text: { content: "答案cite[1]" } },
                { think: { content: "先想一想。" } },
              ],
              references: [{
                matchedText: "cite[1]",
                items: [{ search: { base: { title: "来源一", url: "https://example.com/1" } } }],
              }],
            }],
            nextPageToken: "messages-2",
          });
        }
        return jsonResponse({
          messages: [{
            id: "m1",
            role: "user",
            createTime: "2026-10-23T08:00:00.100000Z",
            blocks: [{ text: { content: "你好" } }],
          }],
        });
      }

      throw new Error(`未预期的请求：${url}`);
    },
  });

  const listReply = await kimi.send({ type: "list-conversations" });
  assert.equal(listReply.ok, true);
  assert.deepEqual([...listReply.conversations].map((item) => item.id), ["c1", "c2"]);

  kimi.calls.forEach((call) => {
    assert.equal(call.options.headers.authorization, "Bearer kimi-token");
  });

  const feedsCalls = kimi.calls.filter((call) => call.url.includes("ListFeeds"));
  assert.equal(feedsCalls.length, 2);
  assert.equal(callBody(feedsCalls[0]).pageToken, "");
  assert.equal(callBody(feedsCalls[1]).pageToken, "feeds-2");

  const detailReply = await kimi.send({
    type: "extract-conversation",
    ref: { id: "c1", title: "会话一" },
  });

  assert.equal(detailReply.ok, true);
  const conversation = detailReply.conversation;
  assert.deepEqual([...conversation.messages].map((message) => message.role), ["user", "assistant"]);
  assert.match(conversation.messages[1].text, /答案\[1\]/);
  assert.match(conversation.messages[1].text, /> \*\*思考过程\*\*\n> 先想一想。/);
  assert.match(conversation.messages[1].text, /参考来源：\n\[1\] 来源一 - https:\/\/example\.com\/1/);

  const messageCalls = kimi.calls.filter((call) => call.url.includes("ListMessages"));
  assert.equal(messageCalls.length, 2);
  assert.equal(callBody(messageCalls[0]).pageToken, "");
  assert.equal(callBody(messageCalls[1]).pageToken, "messages-2");
});

test("Kimi content script 空列表返回空数组而不是报错", async () => {
  const kimi = createHarness({
    parser: "content/kimi-parse.js",
    adapter: "content/kimi.js",
    tokens: { access_token: "kimi-token" },
    fetch: async (url) => {
      if (url.includes("ListFeeds")) return jsonResponse({ items: [] });
      throw new Error(`未预期的请求：${url}`);
    },
  });

  const reply = await kimi.send({ type: "list-conversations" });
  assert.equal(reply.ok, true);
  assert.deepEqual([...reply.conversations], []);
});

test("元宝 content script 用 cookie 分页读取列表与详情，兼容字符串 hasMore", async () => {
  const yuanbao = createHarness({
    parser: "content/yuanbao-parse.js",
    adapter: "content/yuanbao.js",
    pathname: "/chat/agentA/conv-0",
    fetch: async (url, options) => {
      const body = JSON.parse(options.body || "{}");

      if (url.includes("/conversation/list")) {
        if (body.offset === 0) {
          return jsonResponse({
            conversations: [
              { id: "conv-2", agentId: "agentA", title: "乙", firstRepliedAt: 1760900200 },
              { id: "conv-1", agentId: "agentA", title: "甲", firstRepliedAt: 1760900000 },
            ],
            pagination: { totalResults: 3 },
          });
        }
        return jsonResponse({
          conversations: [{ id: "conv-3", agentId: "agentA", title: "丙" }],
          pagination: { totalResults: 3 },
        });
      }

      if (url.includes("/conversation/v1/detail")) {
        if (body.offset === 0) {
          return jsonResponse({
            sessionTitle: "元宝会话",
            firstRepliedAt: 1760900000,
            hasMore: "true",
            convs: [
              {
                id: "a2", speaker: "ai", index: 101, createTime: 1760900030,
                speechesV2: [{
                  content: [
                    { type: "think", msg: "先算一下。" },
                    { type: "text", msg: "答案是 42[citation:1]。" },
                    { type: "searchGuid", docs: [{ index: 1, title: "来源", url: "https://example.com/y" }] },
                  ],
                }],
              },
              { id: "h2", speaker: "human", index: 100, createTime: 1760900020, speechesV2: [{ content: [{ type: "text", msg: "再问一句" }] }] },
            ],
          });
        }
        return jsonResponse({
          hasMore: "false",
          convs: [
            { id: "a1", speaker: "ai", index: 99, createTime: 1760900010, speechesV2: [{ content: [{ type: "text", msg: "第一问的答案" }] }] },
            { id: "h1", speaker: "human", index: 98, createTime: 1760900000, speechesV2: [{ content: [{ type: "text", msg: "第一问" }] }] },
          ],
        });
      }

      throw new Error(`未预期的请求：${url}`);
    },
  });

  const listReply = await yuanbao.send({ type: "list-conversations" });
  assert.equal(listReply.ok, true);
  assert.deepEqual([...listReply.conversations].map((item) => item.id), [
    "agentA/conv-2",
    "agentA/conv-1",
    "agentA/conv-3",
  ]);

  const listCalls = yuanbao.calls.filter((call) => call.url.includes("/conversation/list"));
  assert.equal(listCalls.length, 2);
  assert.equal(callBody(listCalls[0]).agentId, "agentA");
  assert.equal(callBody(listCalls[1]).offset, 2);

  const detailReply = await yuanbao.send({
    type: "extract-conversation",
    ref: { id: "agentA/conv-2", title: "乙" },
  });

  assert.equal(detailReply.ok, true);
  const conversation = detailReply.conversation;
  assert.equal(conversation.title, "元宝会话");
  assert.deepEqual([...conversation.messages].map((message) => message.role), [
    "user",
    "assistant",
    "user",
    "assistant",
  ]);
  assert.equal(conversation.messages[0].text, "第一问");
  assert.match(conversation.messages[3].text, /答案是 42\[1\]。/);
  assert.match(conversation.messages[3].text, /参考来源：\n\[1\] 来源 - https:\/\/example\.com\/y/);

  const detailCalls = yuanbao.calls.filter((call) => call.url.includes("/conversation/v1/detail"));
  assert.equal(detailCalls.length, 2);
  assert.equal(callBody(detailCalls[1]).offset, 100);
});

test("元宝 content script 把嵌套错误码转成可读错误", async () => {
  const yuanbao = createHarness({
    parser: "content/yuanbao-parse.js",
    adapter: "content/yuanbao.js",
    pathname: "/chat/agentA/conv-0",
    fetch: async (url) => {
      if (url.includes("/conversation/list")) {
        return jsonResponse({ error: { code: 1001, message: "登录已失效。" } });
      }
      throw new Error(`未预期的请求：${url}`);
    },
  });

  const reply = await yuanbao.send({ type: "list-conversations" });
  assert.equal(reply.ok, false);
  assert.match(reply.error, /登录已失效。/);
});
