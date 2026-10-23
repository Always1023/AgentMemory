import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const manifest = JSON.parse(readFileSync(new URL("../manifest.json", import.meta.url), "utf8"));

test("manifest 是 Manifest V3 且只申请必要权限", () => {
  assert.equal(manifest.manifest_version, 3);
  assert.equal(manifest.name, "AgentMemory Web");
  assert.deepEqual([...manifest.permissions].sort(), ["storage", "tabs"]);
});

test("manifest 只匹配支持的站点域名，不用 all_urls", () => {
  const hosts = [
    "https://chat.deepseek.com/*",
    "https://www.kimi.com/*",
    "https://yuanbao.tencent.com/*",
    "https://chatgpt.com/*",
    "https://chat.openai.com/*",
  ];

  for (const host of hosts) {
    assert.ok(manifest.host_permissions.includes(host), `缺少 host_permission: ${host}`);
  }
  assert.ok(!manifest.host_permissions.includes("<all_urls>"));

  const scripts = manifest.content_scripts || [];
  assert.equal(scripts.length, 4);

  const byHost = new Map(scripts.map((script) => [script.matches[0], script]));
  const expected = {
    "https://chat.deepseek.com/*": [
      "content/request.js",
      "content/api-content.js",
      "content/deepseek-parse.js",
      "content/deepseek.js",
    ],
    "https://www.kimi.com/*": [
      "content/request.js",
      "content/api-content.js",
      "content/kimi-parse.js",
      "content/kimi.js",
    ],
    "https://yuanbao.tencent.com/*": [
      "content/request.js",
      "content/api-content.js",
      "content/yuanbao-parse.js",
      "content/yuanbao.js",
    ],
  };

  for (const [host, files] of Object.entries(expected)) {
    const script = byHost.get(host);
    assert.ok(script, `缺少 content_script: ${host}`);
    assert.deepEqual(script.js, files);
    assert.ok(!script.matches.includes("<all_urls>"));
  }

  const chatgpt = scripts.find((script) => script.matches.includes("https://chatgpt.com/*"));
  assert.ok(chatgpt);
  assert.deepEqual(chatgpt.js, ["content/chatgpt-parse.js", "content/chatgpt.js"]);
});

test("manifest 的 popup 指向扩展页", () => {
  assert.equal(manifest.action.default_popup, "popup.html");
});
