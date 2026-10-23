import { createApiSiteAdapter } from "./channel.js";

export function createYuanbaoAdapter(options = {}) {
  return createApiSiteAdapter({
    site: "yuanbao",
    baseUrl: "https://yuanbao.tencent.com/",
    ...options,
  });
}
