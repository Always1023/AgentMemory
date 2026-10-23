import test from "node:test";
import assert from "node:assert/strict";

import { formatDate, planArchivePath, sanitizeTitle, shortHash } from "../lib/paths.js";

test("sanitizeTitle 去掉文件名非法字符并限长", () => {
  assert.equal(sanitizeTitle('  a/b:c*?"<>|  '), "a b c");
  assert.equal(sanitizeTitle(""), "未命名会话");
  assert.equal(Array.from(sanitizeTitle("x".repeat(80))).length, 60);
});

test("shortHash 稳定且取 10 位", () => {
  assert.match(shortHash("abc"), /^[0-9a-f]{10}$/);
  assert.equal(shortHash("abc"), shortHash("abc"));
  assert.notEqual(shortHash("abc"), shortHash("abd"));
});

test("planArchivePath 独立目录的形态", () => {
  const conversation = {
    id: "abc",
    title: "项目结构",
    startedAt: new Date(2026, 9, 23, 9, 0, 0),
  };

  const path = planArchivePath({ layout: "standalone", site: "chatgpt", conversation });
  assert.match(path, /^ChatGPT\/2026-10-23-项目结构-[0-9a-f]{10}\.md$/);
  assert.ok(path.endsWith(`${shortHash("abc")}.md`));
});

test("planArchivePath 桌面端归档目录的形态", () => {
  const conversation = {
    id: "abc",
    title: "项目结构",
    startedAt: new Date(2026, 9, 23, 9, 0, 0),
  };

  const path = planArchivePath({ layout: "desktop", site: "chatgpt", conversation });
  assert.match(path, /^网页版\/ChatGPT\/2026-10-23-项目结构-[0-9a-f]{10}\.md$/);
});

test("标题变化只改标题段，标识不变", () => {
  const base = {
    id: "abc",
    startedAt: new Date(2026, 9, 23, 9, 0, 0),
  };

  const first = planArchivePath({ layout: "standalone", site: "chatgpt", conversation: { ...base, title: "甲" } });
  const second = planArchivePath({ layout: "standalone", site: "chatgpt", conversation: { ...base, title: "乙" } });

  assert.notEqual(first, second);
  assert.ok(first.endsWith(`-${shortHash("abc")}.md`));
  assert.ok(second.endsWith(`-${shortHash("abc")}.md`));
});

test("formatDate 用本地日期，坏值回退到今天", () => {
  assert.equal(formatDate(new Date(2026, 9, 23, 23, 0, 0)), "2026-10-23");
  assert.match(formatDate("not-a-date"), /^\d{4}-\d{2}-\d{2}$/);
});
