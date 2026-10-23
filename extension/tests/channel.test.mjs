import test from "node:test";
import assert from "node:assert/strict";

import { createApiSiteAdapter } from "../lib/sites/channel.js";

class FakeTabs {
  constructor({ listReply } = {}) {
    this.listReply = listReply;
    this.created = [];
    this.removed = [];
    this.completed = new Set();
    this.listeners = new Set();
    this.nextId = 1;
    this.onUpdated = {
      addListener: (listener) => this.listeners.add(listener),
      removeListener: (listener) => this.listeners.delete(listener),
    };
  }

  async create(props) {
    const tab = { id: this.nextId, status: "loading", ...props };
    this.nextId += 1;
    this.created.push(tab);

    queueMicrotask(() => {
      tab.status = "complete";
      this.completed.add(tab.id);
      for (const listener of this.listeners) listener(tab.id, { status: "complete" });
    });

    return tab;
  }

  async get(id) {
    return { id, status: this.completed.has(id) ? "complete" : "loading" };
  }

  async sendMessage(_id, message) {
    if (message.type === "ping") return { ok: true, site: "demo" };
    if (message.type === "list-conversations") {
      return this.listReply || {
        ok: true,
        conversations: [{ id: "c1", title: "会话一" }],
      };
    }
    if (message.type === "extract-conversation") {
      return {
        ok: true,
        conversation: {
          id: message.ref.id,
          url: "https://demo.test/chat/c1",
          title: message.ref.title,
          messages: [],
        },
      };
    }
    return { ok: false, error: "未知消息" };
  }

  async remove(id) {
    this.removed.push(id);
  }
}

test("createApiSiteAdapter 打开隐藏标签页、读写会话并关闭", async () => {
  const tabs = new FakeTabs();
  const adapter = createApiSiteAdapter({
    site: "demo",
    baseUrl: "https://demo.test/",
    tabs,
  });

  await adapter.open();
  assert.equal(tabs.created.length, 1);
  assert.equal(tabs.created[0].url, "https://demo.test/");
  assert.equal(tabs.created[0].active, false);

  const conversations = await adapter.listConversations();
  assert.deepEqual(conversations, [{ id: "c1", title: "会话一", site: "demo" }]);

  const conversation = await adapter.fetchConversation({ id: "c1", title: "会话一" });
  assert.equal(conversation.site, "demo");
  assert.equal(conversation.title, "会话一");
  assert.equal(conversation.url, "https://demo.test/chat/c1");

  await adapter.close();
  assert.deepEqual(tabs.removed, [tabs.created[0].id]);
});

test("createApiSiteAdapter 把页面返回的错误抛给批量页", async () => {
  const tabs = new FakeTabs({
    listReply: { ok: false, error: "未登录，请先登录。" },
  });
  const adapter = createApiSiteAdapter({
    site: "demo",
    baseUrl: "https://demo.test/",
    tabs,
  });

  await adapter.open();
  await assert.rejects(adapter.listConversations(), /未登录，请先登录。/);
  await adapter.close();
});
