import { IndexStore } from "./lib/index-store.js";
import { saveListCache } from "./lib/list-cache.js";
import { runBatch } from "./lib/runner.js";
import {
  ensurePermission,
  loadDirectoryHandle,
  loadSettings,
  saveDirectoryHandle,
  saveSettings,
} from "./lib/settings.js";
import { createSiteAdapter, SITE_OPTIONS } from "./lib/sites.js";
import { chromeStorage } from "./lib/storage.js";
import { Writer } from "./lib/writer.js";

const PACES = {
  slow: { paceMs: 5000, paceJitterMs: 1500, concurrency: 1, label: "慢速（1 并发）" },
  normal: { paceMs: 2000, paceJitterMs: 800, concurrency: 2, label: "正常（2 并发）" },
  fast: { paceMs: 800, paceJitterMs: 400, concurrency: 3, label: "快速（3 并发）" },
};

const ITEM_STATUS = {
  pending: "待获取",
  saved: "已保存",
  skipped: "已存在",
  empty: "空会话",
  failed: "失败",
};

const elements = {
  chooseDir: document.getElementById("chooseDir"),
  current: document.getElementById("current"),
  desktopLayout: document.getElementById("desktopLayout"),
  dirName: document.getElementById("dirName"),
  empty: document.getElementById("empty"),
  failed: document.getElementById("failed"),
  failures: document.getElementById("failures"),
  itemsCount: document.getElementById("itemsCount"),
  itemsList: document.getElementById("itemsList"),
  saved: document.getElementById("saved"),
  savedCount: document.getElementById("savedCount"),
  savedList: document.getElementById("savedList"),
  siteSelect: document.getElementById("siteSelect"),
  skipped: document.getElementById("skipped"),
  speedSelect: document.getElementById("speedSelect"),
  start: document.getElementById("start"),
  status: document.getElementById("status"),
  stop: document.getElementById("stop"),
  total: document.getElementById("total"),
};

let directoryHandle = null;
let controller = null;
let indexStore = null;
let running = false;
let itemNodes = new Map();

function siteName(id) {
  return SITE_OPTIONS.find((site) => site.id === id)?.name || id;
}

function paceOf(value) {
  return PACES[value] || PACES.normal;
}

function setStatus(message, isError = false) {
  elements.status.textContent = message;
  elements.status.classList.toggle("error", Boolean(isError));
}

function setControls() {
  const locked = running;
  elements.chooseDir.disabled = locked;
  elements.desktopLayout.disabled = locked;
  elements.siteSelect.disabled = locked;
  elements.speedSelect.disabled = locked;
  elements.start.disabled = locked || !directoryHandle;
  elements.stop.disabled = !locked;
}

function refreshDirectory() {
  elements.dirName.textContent = directoryHandle?.name || "未选择";
  setControls();
}

function renderSaved(store, site) {
  const entries = Object.values(store?.all() || {})
    .filter((entry) => entry?.site === site)
    .sort((a, b) => String(b.savedAt || "").localeCompare(String(a.savedAt || "")));

  elements.savedCount.textContent = String(entries.length);

  if (entries.length === 0) {
    const item = document.createElement("li");
    item.className = "muted";
    item.textContent = "暂无";
    elements.savedList.replaceChildren(item);
    return;
  }

  elements.savedList.replaceChildren(...entries.map((entry) => {
    const item = document.createElement("li");
    const name = entry.title || entry.conversationId || "未命名会话";
    const when = entry.savedAt ? new Date(entry.savedAt).toLocaleString() : "";
    item.textContent = `${name}${when ? ` · ${when}` : ""}`;
    item.title = entry.archivePath || "";
    return item;
  }));
}

function renderItems(items = []) {
  elements.itemsCount.textContent = String(items.length);

  if (items.length === 0) {
    itemNodes.clear();
    const item = document.createElement("li");
    item.className = "muted";
    item.textContent = "尚未开始";
    elements.itemsList.replaceChildren(item);
    return;
  }

  if (itemNodes.size === 0) {
    elements.itemsList.replaceChildren();
  }

  for (const entry of items) {
    const key = entry.id || entry.title || "";
    let item = itemNodes.get(key);

    if (!item) {
      item = document.createElement("li");
      itemNodes.set(key, item);
      elements.itemsList.append(item);
    }

    const name = entry.title || entry.id || "未命名会话";
    const status = ITEM_STATUS[entry.status] || entry.status || "待获取";
    const message = entry.message ? `：${entry.message}` : "";
    const timing = entry.fetchMs > 0 ? ` · 取 ${(entry.fetchMs / 1000).toFixed(1)}s` : "";
    const saving = entry.saveMs > 0 ? ` · 写 ${entry.saveMs}ms` : "";
    const source = entry.source === "dom" ? " · 页面兜底" : "";
    const text = `[${status}] ${name}${message}${timing}${saving}${source}`;
    if (item.textContent !== text) item.textContent = text;
    item.title = entry.fallbackReason || "";

    const className = `item-${entry.status || "pending"}`;
    if (item.className !== className) item.className = className;
  }
}

function resetProgress() {
  elements.total.textContent = "0";
  elements.saved.textContent = "0";
  elements.skipped.textContent = "0";
  elements.empty.textContent = "0";
  elements.failed.textContent = "0";
  elements.current.textContent = "";
  elements.failures.replaceChildren();
  renderItems([]);
}

function renderProgress(summary) {
  if (summary.phase === "list") {
    setStatus(`正在读取 ${siteName(elements.siteSelect.value)} 会话列表...（已读 ${summary.total ?? 0} 条）`);
  } else if (summary.phase === "fetch" && elements.status.textContent.startsWith("正在读取")) {
    setStatus(`正在获取 ${siteName(elements.siteSelect.value)} 会话详情...`);
  }

  elements.total.textContent = String(summary.total ?? 0);
  elements.saved.textContent = String(summary.saved ?? 0);
  elements.skipped.textContent = String(summary.skipped ?? 0);
  elements.empty.textContent = String(summary.empty ?? 0);
  elements.failed.textContent = String(summary.failed ?? 0);

  if (summary.current) {
    const label = summary.current.title || summary.current.id || summary.current.url || "";
    elements.current.textContent = label ? `正在处理：${label}` : "";
  } else {
    elements.current.textContent = "";
  }

  elements.failures.replaceChildren(...(summary.failures || []).map((failure) => {
    const item = document.createElement("li");
    const name = failure.title || failure.id || "未命名会话";
    item.textContent = `${name}：${failure.message}`;
    return item;
  }));

  renderItems(summary.items);
}

function listSummary(summary) {
  const meta = summary.listMeta;
  if (!meta) return "";
  if (meta.source === "api" && Number.isFinite(meta.active) && Number.isFinite(meta.archived)) {
    const cache = meta.cacheUsed
      ? (meta.stoppedEarly ? "，复用上次列表" : "，有列表缓存")
      : "";
    const incomplete = meta.incomplete ? "，列表可能不全" : "";
    return `；列表 ${meta.active} 进行中 + ${meta.archived} 归档，${meta.pages} 页${cache}${incomplete}`;
  }
  if (meta.pages) return `；列表 ${summary.total} 条，${meta.pages} 页`;
  if (meta.apiError) return `；列表来自页面（接口：${meta.apiError}）`;
  return "";
}

function summarize(summary) {
  const parts = [
    `保存 ${summary.saved}`,
    `跳过 ${summary.skipped}`,
    `空会话 ${summary.empty}`,
    `失败 ${summary.failed}`,
  ];
  return `${summary.stopped ? "已停止" : "完成"}：${parts.join("，")}${listSummary(summary)}。`;
}

async function persistSettings() {
  await saveSettings(chromeStorage, {
    layout: elements.desktopLayout.checked ? "desktop" : "standalone",
    pace: elements.speedSelect.value,
  });
}

async function chooseDirectory() {
  if (running) return;

  if (typeof window.showDirectoryPicker !== "function") {
    setStatus("当前浏览器不支持目录写入，请使用 Chrome / Edge。", true);
    return;
  }

  try {
    directoryHandle = await window.showDirectoryPicker({ mode: "readwrite" });
    await saveDirectoryHandle(directoryHandle);
    await persistSettings();
    refreshDirectory();
    setStatus("保存目录已选好。");
  } catch (error) {
    if (error?.name === "AbortError") return;
    setStatus(error?.message || "选择目录失败。", true);
  }
}

async function startBatch() {
  if (running) return;

  if (!directoryHandle) {
    setStatus("先选择保存目录。", true);
    return;
  }

  const granted = await ensurePermission(directoryHandle);
  if (!granted) {
    setStatus("目录授权失效；请重新选择目录，或在浏览器提示里点允许。", true);
    return;
  }

  running = true;
  controller = new AbortController();
  resetProgress();
  setControls();

  const siteId = elements.siteSelect.value;
  const pace = paceOf(elements.speedSelect.value);
  setStatus(`正在打开 ${siteName(siteId)}（${pace.label}）...`);

  let adapter = null;

  try {
    const layout = elements.desktopLayout.checked ? "desktop" : "standalone";
    await persistSettings();

    indexStore = new IndexStore(chromeStorage);
    await indexStore.load();
    renderSaved(indexStore, siteId);

    const writer = new Writer({
      rootHandle: directoryHandle,
      layout,
      directoryName: directoryHandle.name,
    });

    adapter = createSiteAdapter(siteId, { keepTab: true, listCache: chromeStorage });
    await adapter.open();
    setStatus("正在读取会话列表...");

    const summary = await runBatch({
      adapter,
      index: indexStore,
      writer,
      signal: controller.signal,
      paceMs: pace.paceMs,
      paceJitterMs: pace.paceJitterMs,
      concurrency: pace.concurrency,
      onProgress: renderProgress,
    });

    renderProgress(summary);
    if (indexStore) renderSaved(indexStore, siteId);

    if (summary.listMeta?.source === "api") {
      try {
        const items = summary.items.map((item) => ({
          id: item.id,
          title: item.title,
          url: item.url,
          updatedAt: item.updatedAt || "",
          listFingerprint: item.listFingerprint || "",
          archived: Boolean(item.archived),
        })).filter((item) => item.id);
        const watermark = items.reduce(
          (max, item) => (item.updatedAt > max ? item.updatedAt : max),
          "",
        );
        await saveListCache(chromeStorage, siteId, items, {
          watermark,
          complete: summary.listMeta.incomplete !== true,
        });
      } catch {
        // 列表缓存失败不影响本次保存。
      }
    }

    if (summary.total === 0) {
      setStatus(`没有读到 ${siteName(siteId)} 会话：请确认已登录，且账号里至少有一个会话。`, true);
    } else {
      setStatus(summarize(summary), summary.failed > 0);
    }
  } catch (error) {
    setStatus(error?.message || "批量保存失败。", true);
  } finally {
    if (adapter) await adapter.close();
    if (indexStore) renderSaved(indexStore, siteId);
    running = false;
    controller = null;
    setControls();
  }
}

function stopBatch() {
  if (!controller) return;
  controller.abort();
  setStatus("正在停止...");
}

async function init() {
  for (const site of SITE_OPTIONS) {
    const option = document.createElement("option");
    option.value = site.id;
    option.textContent = site.name;
    elements.siteSelect.append(option);
  }

  try {
    const settings = await loadSettings(chromeStorage);
    elements.desktopLayout.checked = settings.layout === "desktop";
    elements.speedSelect.value = settings.pace || "normal";
    directoryHandle = await loadDirectoryHandle();
    indexStore = new IndexStore(chromeStorage);
    await indexStore.load();
  } catch {
    setStatus("读取设置失败；重新选择保存目录即可。", true);
  }

  refreshDirectory();
  renderSaved(indexStore, elements.siteSelect.value);
  renderItems([]);

  elements.chooseDir.addEventListener("click", chooseDirectory);
  elements.desktopLayout.addEventListener("change", () => {
    persistSettings().catch(() => {});
  });
  elements.speedSelect.addEventListener("change", () => {
    persistSettings().catch(() => {});
  });
  elements.siteSelect.addEventListener("change", () => {
    renderSaved(indexStore, elements.siteSelect.value);
    renderItems([]);
  });
  elements.start.addEventListener("click", startBatch);
  elements.stop.addEventListener("click", stopBatch);
}

init();
