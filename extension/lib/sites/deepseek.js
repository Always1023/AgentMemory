import { createApiSiteAdapter } from "./channel.js";

export function createDeepSeekAdapter(options = {}) {
  return createApiSiteAdapter({
    site: "deepseek",
    baseUrl: "https://chat.deepseek.com/",
    ...options,
  });
}
