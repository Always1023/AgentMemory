import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import "../content/kimi-parse.js";

const parser = globalThis.AgentMemoryWebKimi;

function fixture(name) {
  return JSON.parse(readFileSync(new URL(`./fixtures/${name}`, import.meta.url), "utf8"));
}

test("normalizeFeeds 兼容 chat 与 item.value 两种包装", () => {
  const page = parser.normalizeFeeds(fixture("kimi-feeds.json"));

  assert.deepEqual(page.items.map((item) => item.id), [
    "c1c1c1c1-1111-4222-8333-944455556666",
    "c2c2c2c2-7777-4888-9999-aaaabbbbcccc",
  ]);
  assert.equal(
    page.items[0].url,
    "https://www.kimi.com/chat/c1c1c1c1-1111-4222-8333-944455556666",
  );
  assert.equal(page.items[0].startedAt, "2026-10-23T08:00:00.100Z");
  assert.equal(page.rawCount, 2);
  assert.equal(page.nextPageToken, "next-page");
});

test("normalizeFeeds 对空响应返回空列表", () => {
  const page = parser.normalizeFeeds({});

  assert.deepEqual(page.items, []);
  assert.equal(page.nextPageToken, "");
});

test("normalizeMessages 按微秒时间排序并展开思考块", () => {
  const messages = parser.normalizeMessages(fixture("kimi-messages.json").messages);

  assert.deepEqual(messages.map((message) => message.role), ["user", "assistant"]);
  assert.equal(messages[0].text, "你好");
  assert.match(messages[1].text, /你好，有什么可以帮你？/);
  assert.match(messages[1].text, /> \*\*思考过程\*\*\n> 先打个招呼。/);
  assert.equal(messages[0].time, "2026-10-23T08:00:00.100Z");
  assert.equal(messages[1].time, "2026-10-23T08:00:00.200Z");
  assert.ok(!("sortTime" in messages[0]));
});

test("messageText 只有思考块时也保留内容", () => {
  const text = parser.messageText({
    role: "assistant",
    blocks: [{ think: { content: "先想一步。" } }],
  });

  assert.equal(text, "> **思考过程**\n> 先想一步。");
});

test("messageText 把引用标记编号并附参考来源", () => {
  const marker = "cite[one]";
  const text = parser.messageText({
    role: "assistant",
    blocks: [{ text: { content: `答案见这里${marker}。` } }],
    references: [{
      matchedText: marker,
      items: [{ searchResult: { base: { title: "来源一", url: "https://example.com/1" } } }],
    }],
  });

  assert.match(text, /答案见这里\[1\]。/);
  assert.match(text, /参考来源：\n\[1\] 来源一 - https:\/\/example\.com\/1/);
});

test("messageText 删除无法解析的引用标记", () => {
  const text = parser.messageText({
    role: "assistant",
    blocks: [{ text: { content: "前文cite[missing]后文" } }],
  });

  assert.equal(text, "前文后文");
  assert.doesNotMatch(text, /cite/);
});

test("normalizeMessages 丢弃零时间，不把消息排到 1970", () => {
  const messages = parser.normalizeMessages([
    { role: "user", createTime: 0, blocks: [{ text: { content: "无时间" } }] },
  ]);

  assert.equal(messages[0].time, null);
});

test("normalizeConversation 组合会话与消息", () => {
  const conversation = parser.normalizeConversation(
    fixture("kimi-chat.json"),
    fixture("kimi-messages.json"),
    { id: "c1c1c1c1-1111-4222-8333-944455556666" },
  );

  assert.equal(conversation.site, "kimi");
  assert.equal(conversation.title, "Kimi 会话一");
  assert.equal(conversation.startedAt, "2026-10-23T08:00:00.000Z");
  assert.equal(conversation.messages.length, 2);
});
