import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import "../content/deepseek-parse.js";

const parser = globalThis.AgentMemoryWebDeepSeek;

function fixture(name) {
  return JSON.parse(readFileSync(new URL(`./fixtures/${name}`, import.meta.url), "utf8"));
}

test("sessionPage 读取分页列表并生成会话地址", () => {
  const page = parser.sessionPage(fixture("deepseek-sessions.json"));

  assert.equal(page.rawCount, 2);
  assert.equal(page.hasMore, false);
  assert.deepEqual(page.items.map((item) => item.title), ["整理会议纪要", "第二段会话"]);
  assert.equal(
    page.items[0].url,
    "https://chat.deepseek.com/a/chat/s/8f3c2b1a-1111-4222-8333-944455556666",
  );
  assert.equal(page.items[0].startedAt, new Date(1761000000.123 * 1000).toISOString());
});

test("sessionPage 对空响应返回空列表", () => {
  const page = parser.sessionPage({});

  assert.deepEqual(page.items, []);
  assert.equal(page.rawCount, 0);
  assert.equal(page.lastRaw, null);
});

test("normalizeConversation 按时间排序并把思考块转成引用", () => {
  const conversation = parser.normalizeConversation(fixture("deepseek-history.json"), {
    id: "8f3c2b1a-1111-4222-8333-944455556666",
    title: "整理会议纪要",
    url: "https://chat.deepseek.com/a/chat/s/8f3c2b1a-1111-4222-8333-944455556666",
  });

  assert.equal(conversation.site, "deepseek");
  assert.equal(conversation.title, "整理会议纪要");
  assert.deepEqual(conversation.messages.map((message) => message.role), ["user", "assistant"]);
  assert.equal(conversation.messages[0].text, "帮我整理今天的会议纪要。");
  assert.match(conversation.messages[1].text, /^> \*\*思考过程\*\*\n> 先确认目标，再给三点。/);
  assert.match(conversation.messages[1].text, /可以按三点整理。/);
  assert.equal(conversation.messages[1].time, new Date(1760900500 * 1000).toISOString());
});

test("normalizeConversation 兼容 fragments 格式并跳过搜索片段", () => {
  const conversation = parser.normalizeConversation(fixture("deepseek-fragments.json"), {
    id: "fragment-session",
  });

  assert.deepEqual(conversation.messages.map((message) => message.role), ["user", "assistant"]);
  assert.equal(conversation.messages[0].text, "你好");
  assert.match(conversation.messages[1].text, /思考一下/);
  assert.match(conversation.messages[1].text, /你好！有什么可以帮你？/);
  assert.doesNotMatch(conversation.messages[1].text, /这条不应该进正文/);
  assert.doesNotMatch(conversation.messages[1].text, /本回答由 AI 生成/);
});

test("normalizeMessages 跳过没有正文的消息", () => {
  const messages = parser.normalizeMessages([
    { role: "USER", content_blocks: [] },
    { role: "ASSISTANT", content_blocks: [{ type: "text", content: "有内容" }] },
    null,
  ]);

  assert.deepEqual(messages.map((message) => message.text), ["有内容"]);
});
