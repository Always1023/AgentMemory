import test from "node:test";
import assert from "node:assert/strict";

import { listCacheKey, loadListCache, saveListCache } from "../lib/list-cache.js";

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

test("list cache 保存站点、水位与完整标记", async () => {
  const storage = new MemoryStorage();
  await saveListCache(storage, "chatgpt", [{ id: "a", listFingerprint: "u1:A" }], {
    watermark: "2026-10-23T00:00:00.000Z",
    complete: true,
  });

  const cache = await loadListCache(storage, "chatgpt");
  assert.deepEqual(cache.items, [{ id: "a", listFingerprint: "u1:A" }]);
  assert.equal(cache.watermark, "2026-10-23T00:00:00.000Z");
  assert.equal(cache.complete, true);
  assert.equal(storage.data[listCacheKey("chatgpt")].complete, true);
});
