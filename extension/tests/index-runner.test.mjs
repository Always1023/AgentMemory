import test from "node:test";
import assert from "node:assert/strict";

import { INDEX_KEY, IndexStore } from "../lib/index-store.js";
import { runBatch } from "../lib/runner.js";

class MemoryStorage {
  constructor() {
    this.data = {};
  }

  async get(key) {
    return this.data[key];
  }

  async set(key, value) {
    this.data[key] = value;
  }
}

function makeConversations() {
  return {
    a: {
      site: "chatgpt",
      id: "a",
      title: "A",
      url: "https://chatgpt.com/c/a",
      startedAt: "2026-10-23T00:00:00.000Z",
      messages: [{ role: "user", text: "甲" }],
    },
    b: {
      site: "chatgpt",
      id: "b",
      title: "B",
      url: "https://chatgpt.com/c/b",
      startedAt: "2026-10-23T00:00:00.000Z",
      messages: [],
    },
  };
}

class FakeAdapter {
  constructor(conversations) {
    this.site = "chatgpt";
    this.conversations = conversations;
    this.fetchCount = 0;
  }

  async listConversations() {
    return Object.keys(this.conversations).map((id) => ({
      id,
      title: this.conversations[id].title,
      url: this.conversations[id].url,
    }));
  }

  async fetchConversation(ref) {
    this.fetchCount += 1;
    if (ref.id === "c") throw new Error("读不到 C");
    return structuredClone(this.conversations[ref.id]);
  }
}

class FakeWriter {
  constructor() {
    this.layout = "standalone";
    this.directoryName = "test";
    this.writes = [];
  }

  async write(conversation, site, { previousPath = null } = {}) {
    const path = `ChatGPT/${conversation.title}.md`;
    this.writes.push({ id: conversation.id, path, previousPath });
    return path;
  }
}

async function makeIndex() {
  const index = new IndexStore(new MemoryStorage());
  await index.load();
  return index;
}

test("IndexStore 读写与版本不符时重建", async () => {
  const storage = new MemoryStorage();
  const index = new IndexStore(storage);
  await index.load();

  assert.equal(index.get("chatgpt", "a"), null);

  await index.set("chatgpt", "a", { fingerprint: "x" });
  assert.deepEqual(index.get("chatgpt", "a"), { fingerprint: "x" });

  const reloaded = new IndexStore(storage);
  await reloaded.load();
  assert.deepEqual(reloaded.get("chatgpt", "a"), { fingerprint: "x" });

  await storage.set(INDEX_KEY, { version: 99, entries: { "chatgpt:a": { fingerprint: "old" } } });
  const rebuilt = new IndexStore(storage);
  await rebuilt.load();
  assert.equal(rebuilt.get("chatgpt", "a"), null);
});

test("runBatch 保存新会话、跳过空会话、失败不中断整批", async () => {
  const conversations = makeConversations();
  const adapter = new FakeAdapter(conversations);
  const writer = new FakeWriter();
  const index = await makeIndex();

  const first = await runBatch({
    adapter,
    index,
    writer,
    now: () => new Date("2026-10-23T12:00:00.000Z"),
  });

  assert.equal(first.total, 2);
  assert.equal(first.saved, 1);
  assert.equal(first.empty, 1);
  assert.equal(first.failed, 0);
  assert.equal(writer.writes.length, 1);
  assert.equal(writer.writes[0].id, "a");
  assert.ok(index.get("chatgpt", "a"));

  const second = await runBatch({
    adapter,
    index,
    writer,
    now: () => new Date("2026-10-23T12:30:00.000Z"),
  });

  assert.equal(second.saved, 0);
  assert.equal(second.skipped, 1);
  assert.equal(second.empty, 1);
  assert.equal(writer.writes.length, 1);
});

test("runBatch 标题变化时把旧路径传给 writer", async () => {
  const conversations = makeConversations();
  const adapter = new FakeAdapter(conversations);
  const writer = new FakeWriter();
  const index = await makeIndex();

  await runBatch({ adapter, index, writer });
  conversations.a.title = "A2";

  const summary = await runBatch({ adapter, index, writer });

  assert.equal(summary.saved, 1);
  assert.equal(writer.writes.length, 2);
  assert.equal(writer.writes[1].path, "ChatGPT/A2.md");
  assert.equal(writer.writes[1].previousPath, "ChatGPT/A.md");
});

test("runBatch 单条失败也继续后面的会话", async () => {
  const conversations = {
    ...makeConversations(),
    c: {
      site: "chatgpt",
      id: "c",
      title: "C",
      url: "https://chatgpt.com/c/c",
      messages: [{ role: "user", text: "丙" }],
    },
  };
  const adapter = new FakeAdapter(conversations);
  const writer = new FakeWriter();
  const index = await makeIndex();

  const summary = await runBatch({ adapter, index, writer });

  assert.equal(summary.total, 3);
  assert.equal(summary.failed, 1);
  assert.equal(summary.failures[0].id, "c");
  assert.equal(summary.saved, 1);
});

test("runBatch 被中止时不拉会话", async () => {
  const adapter = new FakeAdapter(makeConversations());
  const writer = new FakeWriter();
  const index = await makeIndex();
  const controller = new AbortController();
  controller.abort();

  const summary = await runBatch({
    adapter,
    index,
    writer,
    signal: controller.signal,
  });

  assert.equal(summary.stopped, true);
  assert.equal(summary.saved, 0);
  assert.equal(adapter.fetchCount, 0);
});


test("runBatch 列表指纹一致时不拉详情", async () => {
  const index = await makeIndex();
  await index.set("chatgpt", "a", {
    site: "chatgpt",
    conversationId: "a",
    archivePath: "ChatGPT/A.md",
    title: "A",
    fingerprint: "old",
    listFingerprint: "u1:A",
    savedAt: "2026-10-23T00:00:00.000Z",
    layout: "standalone",
    directoryName: "test",
  });

  let fetched = 0;
  const adapter = {
    site: "chatgpt",
    async listConversations() {
      return [{ id: "a", title: "A", url: "https://chatgpt.com/c/a", listFingerprint: "u1:A" }];
    },
    async fetchConversation() {
      fetched += 1;
      throw new Error("不应拉详情");
    },
  };
  const writer = new FakeWriter();

  const summary = await runBatch({ adapter, index, writer });

  assert.equal(summary.total, 1);
  assert.equal(summary.skipped, 1);
  assert.equal(summary.saved, 0);
  assert.equal(fetched, 0);
  assert.equal(writer.writes.length, 0);
  assert.equal(summary.items[0].status, "skipped");
});


test("runBatch 按适配器上限并发拉详情", async () => {
  const conversations = {};
  for (let i = 0; i < 6; i += 1) {
    conversations[`c${i}`] = {
      site: "chatgpt",
      id: `c${i}`,
      title: `C${i}`,
      url: `https://chatgpt.com/c/c${i}`,
      messages: [{ role: "user", text: `${i}` }],
    };
  }

  const adapter = new FakeAdapter(conversations);
  adapter.maxConcurrency = 3;
  const original = adapter.fetchConversation.bind(adapter);
  let active = 0;
  let max = 0;
  adapter.fetchConversation = async (ref) => {
    active += 1;
    max = Math.max(max, active);
    await new Promise((resolve) => setTimeout(resolve, 25));
    try {
      return await original(ref);
    } finally {
      active -= 1;
    }
  };

  const writer = new FakeWriter();
  const index = await makeIndex();
  const summary = await runBatch({ adapter, index, writer, concurrency: 3 });

  assert.equal(summary.saved, 6);
  assert.ok(max > 1 && max <= 3, `max=${max}`);
});


test("IndexStore 批量落盘：persist=false 不进 storage，flush 才写", async () => {
  const storage = new MemoryStorage();
  const index = new IndexStore(storage);
  await index.load();

  await index.set("chatgpt", "a", { site: "chatgpt", conversationId: "a" }, { persist: false });
  assert.equal(storage.data[INDEX_KEY], undefined);
  assert.equal(index.get("chatgpt", "a").conversationId, "a");

  await index.flush();
  assert.equal(storage.data[INDEX_KEY].entries["chatgpt:a"].conversationId, "a");
});

test("runBatch 结束时把批量索引落盘", async () => {
  const conversations = {};
  for (let i = 0; i < 12; i += 1) {
    conversations[`k${i}`] = {
      site: "chatgpt",
      id: `k${i}`,
      title: `K${i}`,
      url: `https://chatgpt.com/c/k${i}`,
      messages: [{ role: "user", text: `${i}` }],
    };
  }

  const storage = new MemoryStorage();
  const index = new IndexStore(storage);
  await index.load();
  const adapter = new FakeAdapter(conversations);
  const writer = new FakeWriter();

  const summary = await runBatch({ adapter, index, writer });

  assert.equal(summary.saved, 12);
  assert.equal(Object.keys(storage.data[INDEX_KEY].entries).length, 12);
});