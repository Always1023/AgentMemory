import test from "node:test";
import assert from "node:assert/strict";

import { loadSettings, saveSettings, SETTINGS_KEY } from "../lib/settings.js";

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

test("loadSettings 默认正常并发，迁移旧慢速", async () => {
  const storage = new MemoryStorage();
  const defaults = await loadSettings(storage);
  assert.equal(defaults.layout, "standalone");
  assert.equal(defaults.pace, "normal");
  assert.equal(defaults.version, 2);

  storage.data[SETTINGS_KEY] = { layout: "desktop", pace: "slow" };
  const migrated = await loadSettings(storage);
  assert.equal(migrated.layout, "desktop");
  assert.equal(migrated.pace, "normal");

  storage.data[SETTINGS_KEY] = { layout: "desktop", pace: "slow", version: 2 };
  assert.equal((await loadSettings(storage)).pace, "slow");

  storage.data[SETTINGS_KEY] = { layout: "不认识", pace: "不认识" };
  const fallback = await loadSettings(storage);
  assert.equal(fallback.layout, "standalone");
  assert.equal(fallback.pace, "normal");
});

test("saveSettings 只写认识的 layout、pace 与版本", async () => {
  const storage = new MemoryStorage();

  await saveSettings(storage, { layout: "desktop", pace: "fast" });
  assert.deepEqual(storage.data[SETTINGS_KEY], { layout: "desktop", pace: "fast", version: 2 });

  await saveSettings(storage, { layout: "不认识", pace: "不认识" });
  assert.deepEqual(storage.data[SETTINGS_KEY], { layout: "standalone", pace: "normal", version: 2 });
});
