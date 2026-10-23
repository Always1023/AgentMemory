import { createApiSiteAdapter } from "./channel.js";

export function createKimiAdapter(options = {}) {
  return createApiSiteAdapter({
    site: "kimi",
    baseUrl: "https://www.kimi.com/",
    ...options,
  });
}
