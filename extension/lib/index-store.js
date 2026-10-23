export const INDEX_KEY = "webIndex";
export const INDEX_VERSION = 1;

export function indexKey(site, conversationId) {
  return `${site}:${conversationId}`;
}

export class IndexStore {
  constructor(storage) {
    this.storage = storage;
    this.data = { version: INDEX_VERSION, entries: {} };
    this.dirty = false;
    this.saveChain = Promise.resolve();
  }

  async load() {
    const raw = await this.storage.get(INDEX_KEY);
    if (!raw || raw.version !== INDEX_VERSION || typeof raw.entries !== "object" || raw.entries === null) {
      this.data = { version: INDEX_VERSION, entries: {} };
      this.dirty = false;
      return;
    }
    this.data = raw;
    this.dirty = false;
  }

  get(site, conversationId) {
    return this.data.entries[indexKey(site, conversationId)] || null;
  }

  all() {
    return this.data.entries;
  }

  async set(site, conversationId, entry, { persist = true } = {}) {
    this.data.entries[indexKey(site, conversationId)] = entry;
    this.dirty = true;
    if (persist) await this.flush();
  }

  async flush() {
    if (!this.dirty) {
      await this.saveChain;
      return;
    }
    this.dirty = false;
    await this.save();
  }

  save() {
    const run = this.saveChain.then(() => this.storage.set(INDEX_KEY, this.data));
    this.saveChain = run.catch(() => {});
    return run;
  }
}
