import { conversationFingerprint } from "./model.js";

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function snapshot(summary, current) {
  return {
    ...summary,
    current,
    failures: summary.failures.map((failure) => ({ ...failure })),
    items: summary.items.map((item) => ({ ...item })),
  };
}

function setItemStatus(summary, id, status, message = "") {
  const item = summary.items.find((entry) => entry.id === id);
  if (!item) return;
  item.status = status;
  item.message = message;
}

function itemFromRef(ref) {
  return {
    id: ref.id || ref.url || "",
    title: ref.title || "",
    url: ref.url || "",
    updatedAt: ref.updatedAt || "",
    listFingerprint: ref.listFingerprint || "",
    archived: Boolean(ref.archived),
    status: "pending",
    message: "",
  };
}

export async function runBatch({
  adapter,
  index,
  writer,
  onProgress = () => {},
  signal = null,
  paceMs = 0,
  paceJitterMs = 0,
  concurrency = 1,
  now = () => new Date(),
}) {
  const seen = new Set();
  const listed = await adapter.listConversations({
    onProgress: (progress) => {
      const items = (progress?.conversations || []).map(itemFromRef);
      onProgress({
        phase: "list",
        total: items.length,
        saved: 0,
        skipped: 0,
        empty: 0,
        failed: 0,
        stopped: false,
        failures: [],
        listMeta: progress?.meta || null,
        items,
      });
    },
  });
  const listMeta = listed.meta || null;
  const refs = listed.filter((ref) => {
    const key = ref?.id || ref?.url;
    if (!key || seen.has(key)) return false;
    seen.add(key);
    return true;
  });

  const summary = {
    phase: "fetch",
    total: refs.length,
    saved: 0,
    skipped: 0,
    empty: 0,
    failed: 0,
    stopped: false,
    failures: [],
    listMeta,
    items: refs.map(itemFromRef),
  };

  onProgress(snapshot(summary, null));

  const workerCount = Math.max(
    1,
    Math.min(concurrency, adapter.maxConcurrency || 1, refs.length || 1),
  );
  const queue = refs.slice();

  let nextStart = 0;
  let cooldownUntil = 0;
  let commitChain = Promise.resolve();
  let pendingIndexWrites = 0;

  function commit(task) {
    const run = commitChain.then(task);
    commitChain = run.catch(() => {});
    return run;
  }

  async function worker() {
    for (;;) {
      if (signal?.aborted) {
        summary.stopped = true;
        return;
      }

      const ref = queue.shift();
      if (!ref) return;

      const itemId = ref.id || ref.url || "";
      onProgress(snapshot(summary, ref));

      const refSite = ref.site || adapter.site;
      const listFingerprint = String(ref.listFingerprint || "").trim();
      const previousForList = ref.id ? index.get(refSite, ref.id) : null;
      const savedAt = String(previousForList?.savedAt || "");
      const updatedAt = String(ref.updatedAt || "");
      const olderThanSave = Boolean(savedAt && updatedAt && updatedAt <= savedAt);
      const sameList = Boolean(
        previousForList
        && listFingerprint
        && previousForList.listFingerprint === listFingerprint,
      );
      const sameRootForList = Boolean(
        previousForList
        && previousForList.layout === writer.layout
        && previousForList.directoryName === writer.directoryName,
      );

      if (previousForList && sameRootForList && (sameList || olderThanSave)) {
        summary.skipped += 1;
        setItemStatus(
          summary,
          itemId,
          "skipped",
          sameList ? "与上次列表一致，未重新获取" : "上次保存后没有更新",
        );
        onProgress(snapshot(summary, ref));
        continue;
      }

      const startAt = Math.max(Date.now(), nextStart, cooldownUntil);
      nextStart = startAt + paceMs + Math.random() * paceJitterMs;

      if (startAt > Date.now()) await delay(startAt - Date.now());

      if (signal?.aborted) {
        summary.stopped = true;
        return;
      }

      const fetchStarted = Date.now();

      try {
        const conversation = await adapter.fetchConversation(ref);

        if (signal?.aborted) {
          summary.stopped = true;
          return;
        }

        const site = conversation?.site || refSite;
        const conversationId = conversation?.id || ref.id;

        if (!site || !conversationId) throw new Error("会话缺少站点或 ID。");

        conversation.site = site;
        conversation.id = conversationId;
        conversation.url ||= ref?.url;

        const fetchMs = Date.now() - fetchStarted;
        const fetchedItem = summary.items.find((entry) => entry.id === itemId);
        if (fetchedItem) {
          fetchedItem.fetchMs = fetchMs;
          fetchedItem.source = conversation.source || "";
          fetchedItem.fallbackReason = conversation.fallbackReason || "";
        }

        const messages = Array.isArray(conversation.messages) ? conversation.messages : [];
        if (messages.length === 0) {
          summary.empty += 1;
          setItemStatus(summary, itemId, "empty", "没有消息");
          onProgress(snapshot(summary, ref));
          continue;
        }

        conversation.capturedAt = now().toISOString();
        const fingerprint = conversationFingerprint(conversation);
        const previous = index.get(site, conversationId);

        if (previous?.fingerprint === fingerprint) {
          summary.skipped += 1;
          setItemStatus(summary, itemId, "skipped", "内容未变化");
          onProgress(snapshot(summary, ref));
          continue;
        }

        const sameRoot = previous?.layout === writer.layout
          && previous?.directoryName === writer.directoryName;
        const previousPath = sameRoot ? previous?.archivePath || null : null;

        const saveStarted = Date.now();

        await commit(async () => {
          const archivePath = await writer.write(conversation, site, { previousPath });
          await index.set(site, conversationId, {
            site,
            conversationId,
            archivePath,
            title: conversation.title || "",
            fingerprint,
            listFingerprint,
            updatedAt: ref?.updatedAt || "",
            savedAt: conversation.capturedAt,
            layout: writer.layout,
            directoryName: writer.directoryName,
          }, { persist: false });
        });

        const saveItem = summary.items.find((entry) => entry.id === itemId);
        if (saveItem) saveItem.saveMs = Date.now() - saveStarted;

        pendingIndexWrites += 1;
        if (pendingIndexWrites >= 10) {
          pendingIndexWrites = 0;
          index.flush().catch(() => {});
        }

        summary.saved += 1;
        setItemStatus(summary, itemId, "saved", "");
        onProgress(snapshot(summary, ref));
      } catch (error) {
        const failedItem = summary.items.find((entry) => entry.id === itemId);
        if (failedItem) failedItem.fetchMs = Date.now() - fetchStarted;

        if (/429|too many requests/i.test(error?.message || "")) {
          cooldownUntil = Math.max(cooldownUntil, Date.now() + 15000);
        }

        summary.failed += 1;
        setItemStatus(summary, itemId, "failed", error?.message || String(error));
        summary.failures.push({
          id: ref?.id || "",
          title: ref?.title || "",
          message: error?.message || String(error),
        });
        onProgress(snapshot(summary, ref));
      }
    }
  }

  try {
    await Promise.all(Array.from({ length: workerCount }, () => worker()));
  } finally {
    try {
      await index.flush();
    } catch {
      // 文件已经保存；索引落盘失败时下次重跑会补。
    }
  }

  return snapshot(summary, null);
}
