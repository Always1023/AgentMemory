import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import "../content/yuanbao-parse.js";

const parser = globalThis.AgentMemoryWebYuanbao;

function fixture(name) {
  return JSON.parse(readFileSync(new URL(`./fixtures/${name}`, import.meta.url), "utf8"));
}

test("normalizeList 把 agentId 打包进会话 ID", () => {
  const page = parser.normalizeList(fixture("yuanbao-list.json"), "naQivTmsDa");

  assert.deepEqual(page.items.map((item) => item.id), [
    "agentA/conv-1111",
    "naQivTmsDa/conv-2222",
  ]);
  assert.equal(page.items[0].url, "https://yuanbao.tencent.com/chat/agentA/conv-1111");
  assert.equal(page.total, 2);
});

test("unpackId 兼容打包与裸 ID", () => {
  assert.deepEqual(parser.unpackId("agentA/conv-1111"), {
    agentId: "agentA",
    conversationId: "conv-1111",
  });
  assert.deepEqual(parser.unpackId("conv-2222"), {
    agentId: parser.DEFAULT_AGENT,
    conversationId: "conv-2222",
  });
});

test("normalizeConversation 按 index 排序并收集搜索来源", () => {
  const conversation = parser.normalizeConversation(fixture("yuanbao-detail.json"), {
    id: "agentA/conv-1111",
  });

  assert.equal(conversation.site, "yuanbao");
  assert.equal(conversation.title, "元宝会话一");
  assert.deepEqual(conversation.messages.map((message) => message.role), ["user", "assistant"]);
  assert.equal(conversation.messages[0].text, "答案是多少？");
  assert.match(conversation.messages[1].text, /> \*\*思考过程\*\*\n> 先算一下。/);
  assert.match(conversation.messages[1].text, /答案是 42。/);
  assert.match(conversation.messages[1].text, /参考来源：\n\[1\] 忽略 - https:\/\/example\.com/);
  assert.equal(conversation.messages[0].time, new Date(1760900000 * 1000).toISOString());
});

test("speechText 把 citation 标记映射到参考来源编号", () => {
  const text = parser.speechText({
    speechesV2: [{
      content: [
        { type: "text", msg: "结论[citation:2]。" },
        { type: "searchGuid", docs: [{ index: 2, title: "来源二", url: "https://example.com/2" }] },
      ],
    }],
  });

  assert.match(text, /结论\[1\]。/);
  assert.match(text, /参考来源：\n\[1\] 来源二 - https:\/\/example\.com\/2/);
});

test("speechText 收集 deepSearch 思考与文档", () => {
  const text = parser.speechText({
    speechesV2: [{
      content: [
        {
          type: "deepSearch",
          contents: [
            { type: "text", msg: "搜索过程" },
            { type: "docs", docs: [{ index: 1, title: "文档", url: "https://example.com/d" }] },
          ],
        },
        { type: "text", msg: "最终答案[citation:1]" },
      ],
    }],
  });

  assert.match(text, /> \*\*思考过程\*\*\n> 搜索过程/);
  assert.match(text, /最终答案\[1\]/);
  assert.match(text, /\[1\] 文档 - https:\/\/example\.com\/d/);
});

test("normalizeList 缺少分页合计时返回 null，不把本页条数当总数", () => {
  const page = parser.normalizeList({ conversations: [{ id: "conv-1", title: "一" }] });

  assert.equal(page.total, null);
  assert.equal(page.items.length, 1);
});

test("normalizeConversation 丢弃零时间，不把会话放到 1970", () => {
  const conversation = parser.normalizeConversation({
    createTime: 0,
    convs: [{
      speaker: "human",
      createTime: 0,
      speechesV2: [{ content: [{ type: "text", msg: "无时间戳" }] }],
    }],
  }, { id: "agentA/conv-empty" });

  assert.equal(conversation.messages[0].time, null);
  assert.notEqual(conversation.startedAt, "1970-01-01T00:00:00.000Z");
});

test("normalizeConversation 对空详情返回空消息", () => {
  const conversation = parser.normalizeConversation({}, { id: "agentA/conv-empty", title: "空会话" });

  assert.deepEqual(conversation.messages, []);
  assert.equal(conversation.title, "空会话");
});
