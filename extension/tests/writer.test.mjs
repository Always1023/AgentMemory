import test from "node:test";
import assert from "node:assert/strict";

import { Writer } from "../lib/writer.js";

class FakeDirectory {
  constructor(name = "root") {
    this.name = name;
    this.files = new Map();
    this.directories = new Map();
    this.aborts = [];
  }

  async getDirectoryHandle(name, { create = false } = {}) {
    if (!this.directories.has(name)) {
      if (!create) {
        const error = new Error("目录不存在");
        error.name = "NotFoundError";
        throw error;
      }
      this.directories.set(name, new FakeDirectory(name));
    }
    return this.directories.get(name);
  }

  async getFileHandle(name, { create = false } = {}) {
    if (!this.files.has(name)) {
      if (!create) {
        const error = new Error("文件不存在");
        error.name = "NotFoundError";
        throw error;
      }
      this.files.set(name, "");
    }

    return {
      name,
      createWritable: async () => ({
        write: async (content) => {
          this.files.set(name, `${this.files.get(name)}${content}`);
        },
        close: async () => {},
        abort: async () => {
          this.aborts.push(name);
          this.files.delete(name);
        },
      }),
    };
  }

  async *entries() {
    for (const name of this.files.keys()) yield [name, null];
    for (const name of this.directories.keys()) yield [name, null];
  }

  async removeEntry(name) {
    if (this.files.delete(name)) return;
    if (this.directories.delete(name)) return;

    const error = new Error("不存在");
    error.name = "NotFoundError";
    throw error;
  }
}

function conversation(overrides = {}) {
  return {
    site: "chatgpt",
    id: "abc",
    title: "测试标题",
    url: "https://chatgpt.com/c/abc",
    startedAt: new Date(2026, 9, 23, 9, 0, 0),
    capturedAt: new Date(2026, 9, 23, 10, 0, 0),
    messages: [{ role: "user", text: "你好" }],
    ...overrides,
  };
}

test("Writer 按独立目录写入 Markdown", async () => {
  const root = new FakeDirectory();
  const writer = new Writer({ rootHandle: root, layout: "standalone" });
  const path = await writer.write(conversation(), "chatgpt");

  assert.match(path, /^ChatGPT\/2026-10-23-测试标题-[0-9a-f]{10}\.md$/);

  const siteDir = root.directories.get("ChatGPT");
  const file = siteDir.files.get(path.split("/").pop());
  assert.match(file, /^# 测试标题/);
  assert.match(file, /- 渠道：ChatGPT/);
  assert.match(file, /## 1 · 我/);
});

test("Writer 在桌面端归档目录下加 网页版/ 前缀", async () => {
  const root = new FakeDirectory();
  const writer = new Writer({ rootHandle: root, layout: "desktop" });
  const path = await writer.write(conversation(), "chatgpt");

  assert.match(path, /^网页版\/ChatGPT\/2026-10-23-测试标题-[0-9a-f]{10}\.md$/);
  assert.ok(root.directories.get("网页版").directories.get("ChatGPT"));
});

test("Writer 标题变化时删掉旧文件，不留下第二份", async () => {
  const root = new FakeDirectory();
  const writer = new Writer({ rootHandle: root, layout: "standalone" });
  const firstPath = await writer.write(conversation({ title: "甲" }), "chatgpt");

  const secondPath = await writer.write(conversation({ title: "乙" }), "chatgpt", {
    previousPath: firstPath,
  });

  assert.notEqual(firstPath, secondPath);
  const files = root.directories.get("ChatGPT").files;
  assert.equal(files.has(firstPath.split("/").pop()), false);
  assert.equal(files.has(secondPath.split("/").pop()), true);
});

test("Writer 旧路径所在目录已经不在时不报错", async () => {
  const root = new FakeDirectory();
  const writer = new Writer({ rootHandle: root, layout: "standalone" });

  const path = await writer.write(conversation(), "chatgpt", {
    previousPath: "旧目录/旧文件.md",
  });

  assert.match(path, /^ChatGPT\//);
});

test("Writer 索引丢失后按身份哈希清理同会话旧文件", async () => {
  const root = new FakeDirectory();
  const writer = new Writer({ rootHandle: root, layout: "standalone" });
  const firstPath = await writer.write(conversation({ title: "甲" }), "chatgpt");

  const secondPath = await writer.write(conversation({ title: "乙" }), "chatgpt");

  assert.notEqual(firstPath, secondPath);
  const files = root.directories.get("ChatGPT").files;
  assert.equal(files.has(firstPath.split("/").pop()), false);
  assert.equal(files.has(secondPath.split("/").pop()), true);
  assert.equal(files.size, 1);
});

test("Writer 渲染失败时中止写入，不留下半份文件", async () => {
  const root = new FakeDirectory();
  const writer = new Writer({
    rootHandle: root,
    layout: "standalone",
    renderer: () => {
      throw new Error("渲染失败");
    },
  });

  await assert.rejects(writer.write(conversation(), "chatgpt"), /渲染失败/);
  const siteDir = root.directories.get("ChatGPT");
  assert.equal(siteDir.aborts.length, 1);
  assert.equal(siteDir.files.size, 0);
});
