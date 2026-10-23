import { createChatGPTAdapter } from "./sites/chatgpt.js";
import { createDeepSeekAdapter } from "./sites/deepseek.js";
import { createKimiAdapter } from "./sites/kimi.js";
import { createYuanbaoAdapter } from "./sites/yuanbao.js";

export const SITE_OPTIONS = [
  { id: "deepseek", name: "DeepSeek" },
  { id: "kimi", name: "Kimi" },
  { id: "yuanbao", name: "腾讯元宝" },
  { id: "chatgpt", name: "ChatGPT" },
];

export function createSiteAdapter(id, options = {}) {
  if (id === "deepseek") return createDeepSeekAdapter(options);
  if (id === "kimi") return createKimiAdapter(options);
  if (id === "yuanbao") return createYuanbaoAdapter(options);
  if (id === "chatgpt") return createChatGPTAdapter(options);
  throw new Error(`暂不支持的站点：${id}`);
}
