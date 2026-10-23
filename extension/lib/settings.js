export const SETTINGS_KEY = "webSettings";
export const DEFAULT_SETTINGS = { layout: "standalone", pace: "normal", version: 2 };

const DB_NAME = "agentmemory-web";
const DB_VERSION = 1;
const HANDLE_STORE = "handles";
const HANDLE_KEY = "saveDirectory";

export async function loadSettings(storage) {
  const raw = await storage.get(SETTINGS_KEY);
  const value = raw && typeof raw === "object" ? raw : {};
  const rawPace = value.pace === "slow" || value.pace === "normal" || value.pace === "fast"
    ? value.pace
    : "normal";
  const storedVersion = Number(value.version) || 1;
  const pace = storedVersion < 2 && rawPace === "slow" ? "normal" : rawPace;
  return {
    ...DEFAULT_SETTINGS,
    ...value,
    layout: value.layout === "desktop" ? "desktop" : "standalone",
    pace,
    version: 2,
  };
}

export async function saveSettings(storage, settings) {
  const pace = settings?.pace === "slow" || settings?.pace === "normal" || settings?.pace === "fast"
    ? settings.pace
    : "normal";
  await storage.set(SETTINGS_KEY, {
    layout: settings?.layout === "desktop" ? "desktop" : "standalone",
    pace,
    version: 2,
  });
}

function hasIndexedDB() {
  return typeof indexedDB !== "undefined";
}

function openDb() {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, DB_VERSION);

    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(HANDLE_STORE)) {
        db.createObjectStore(HANDLE_STORE);
      }
    };

    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

export async function saveDirectoryHandle(handle) {
  if (!hasIndexedDB()) throw new Error("当前环境不支持目录授权。");
  const db = await openDb();

  return new Promise((resolve, reject) => {
    const transaction = db.transaction(HANDLE_STORE, "readwrite");
    transaction.objectStore(HANDLE_STORE).put(handle, HANDLE_KEY);
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error);
  });
}

export async function loadDirectoryHandle() {
  if (!hasIndexedDB()) return null;
  const db = await openDb();

  return new Promise((resolve, reject) => {
    const transaction = db.transaction(HANDLE_STORE, "readonly");
    const request = transaction.objectStore(HANDLE_STORE).get(HANDLE_KEY);
    request.onsuccess = () => resolve(request.result || null);
    request.onerror = () => reject(request.error);
  });
}

export async function clearDirectoryHandle() {
  if (!hasIndexedDB()) return;
  const db = await openDb();

  return new Promise((resolve, reject) => {
    const transaction = db.transaction(HANDLE_STORE, "readwrite");
    transaction.objectStore(HANDLE_STORE).delete(HANDLE_KEY);
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error);
  });
}

export async function ensurePermission(handle, mode = "readwrite") {
  if (!handle) return false;

  const options = { mode };

  try {
    if (typeof handle.queryPermission === "function") {
      const state = await handle.queryPermission(options);
      if (state === "granted") return true;
    }

    if (typeof handle.requestPermission === "function") {
      const state = await handle.requestPermission(options);
      return state === "granted";
    }
  } catch {
    return false;
  }

  return true;
}
