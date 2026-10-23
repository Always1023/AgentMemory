import test from "node:test";
import assert from "node:assert/strict";

import { fenceFor, oneLine, renderMarkdown } from "../lib/render.js";

function conversation(overrides = {}) {
  return {
    site: "chatgpt",
    id: "abc",
    title: "测试会话",
    url: "https://chatgpt.com/c/abc",
    startedAt: new Date(2026, 9, 23, 9, 0, 0),
    capturedAt: new Date(2026, 9, 23, 10, 0, 0),
    messages: [
      { role: "user", text: "你好" },
      { role: "assistant", text: "你好，有什么可以帮你？" },
    ],
    ...overrides,
  };
}

test("renderMarkdown 按桌面端归档的结构输出", () => {
  const markdown = renderMarkdown(conversation(), "ChatGPT");

  assert.match(markdown, /^# 测试会话\n/);
  assert.match(markdown, /- 渠道：ChatGPT/);
  assert.match(markdown, /- 项目：—/);
  assert.match(markdown, /- 会话：abc/);
  assert.match(markdown, /- 开始：2026-10-23 09:00:00/);
  assert.match(markdown, /- 消息：2 条/);
  assert.match(markdown, /- 来自：https:\/\/chatgpt\.com\/c\/abc/);
  assert.match(markdown, /- 保存：2026-10-23 10:00:00/);
  assert.match(markdown, /## 1 · 我\n\n```\n你好\n```/);
  assert.match(markdown, /## 2 · 助手\n\n```\n你好，有什么可以帮你？\n```/);
});

test("renderMarkdown 给正文加足够长的围栏", () => {
  const markdown = renderMarkdown(conversation({
    messages: [
      { role: "user", text: "# 标题\n---\n没有反引号" },
      { role: "assistant", text: "```js\nconsole.log(1)\n```" },
    ],
  }), "ChatGPT");

  assert.match(markdown, /^```$/m);
  assert.match(markdown, /^````$/m);
  assert.match(markdown, /```js/);
});

test("renderMarkdown 空标题与空消息不造多余的块", () => {
  const markdown = renderMarkdown({ title: "", messages: [] }, "");
  assert.match(markdown, /^# 未命名会话/);
  assert.match(markdown, /- 消息：0 条/);
  assert.doesNotMatch(markdown, /^## /m);
});

test("fenceFor 比正文里最长的一串反引号更长", () => {
  assert.equal(fenceFor("没有反引号"), "```");
  assert.equal(fenceFor("a ` b"), "```");
  assert.equal(fenceFor("````"), "`````");
  assert.equal(fenceFor("`` ` ```"), "````");
});

test("oneLine 把换行压成一行", () => {
  assert.equal(oneLine("  a\nb\r\nc  "), "a b c");
});
