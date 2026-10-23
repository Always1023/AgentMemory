import { hashString } from "./model.js";

export const SITE_NAMES = {
  chatgpt: "ChatGPT",
  deepseek: "DeepSeek",
  kimi: "Kimi",
  yuanbao: "腾讯元宝",
};

export const SITE_DIRS = {
  chatgpt: "ChatGPT",
  deepseek: "DeepSeek",
  kimi: "Kimi",
  yuanbao: "腾讯元宝",
};

const ILLEGAL_FILENAME = /[<>:"/\\|?*\u0000-\u001f]/g;
const DEFAULT_TITLE = "未命名会话";
const MAX_TITLE = 60;

export function sanitizeTitle(value, limit = MAX_TITLE) {
  let title = String(value ?? "")
    .replace(/[\r\n\t]+/g, " ")
    .replace(/\s+/g, " ")
    .replace(ILLEGAL_FILENAME, " ")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/^[.\s]+|[.\s]+$/g, "");

  if (!title) title = DEFAULT_TITLE;

  const chars = Array.from(title);
  if (chars.length > limit) title = chars.slice(0, limit).join("").trim();

  return title || DEFAULT_TITLE;
}

export function formatDate(value) {
  const date = value instanceof Date ? value : new Date(value ?? Date.now());
  const safe = Number.isNaN(date.getTime()) ? new Date() : date;
  const year = safe.getFullYear();
  const month = String(safe.getMonth() + 1).padStart(2, "0");
  const day = String(safe.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function shortHash(input) {
  return hashString(input, 10);
}

export function archiveIdentity(conversation) {
  return shortHash(conversation?.id || conversation?.url || conversation?.title || "");
}

export function planArchivePath({ layout, site, conversation, now = new Date() }) {
  const siteId = String(site || conversation?.site || "").trim();
  const siteDir = SITE_DIRS[siteId] || sanitizeTitle(siteId || "未知站点");
  const date = formatDate(conversation?.startedAt || now);
  const title = sanitizeTitle(conversation?.title || DEFAULT_TITLE);
  const identity = archiveIdentity(conversation);
  const parts = layout === "desktop" ? ["网页版", siteDir] : [siteDir];

  parts.push(`${date}-${title}-${identity}.md`);
  return parts.join("/");
}
