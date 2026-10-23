export const LIST_CACHE_PREFIX = "webListCache:";

export function listCacheKey(site) {
  return `${LIST_CACHE_PREFIX}${site}`;
}

export async function loadListCache(storage, site) {
  try {
    const cache = await storage.get(listCacheKey(site));
    return cache && typeof cache === "object" ? cache : null;
  } catch {
    return null;
  }
}

export async function saveListCache(storage, site, items, { watermark = "", complete = false } = {}) {
  await storage.set(listCacheKey(site), {
    items: Array.isArray(items) ? items : [],
    watermark: String(watermark || ""),
    complete: complete === true,
    savedAt: new Date().toISOString(),
  });
}
