(function (root, factory) {
  if (typeof module === "object" && module.exports) {
    module.exports = factory();
  } else {
    root.AgentMemoryWebRequest = factory();
  }
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  function delay(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  function messageOf(data, fallback) {
    if (!data || typeof data !== "object") return fallback;
    return data.message
      || data.msg
      || data.error?.message
      || data.error_msg
      || fallback;
  }

  async function requestJson(url, options = {}) {
    const {
      method = "GET",
      headers = {},
      body = null,
      timeoutMs = 30000,
      attempts = 3,
      retryDelayMs = 500,
    } = options;

    let lastError;

    for (let attempt = 0; attempt < attempts; attempt += 1) {
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), timeoutMs);

      try {
        const response = await fetch(url, {
          method,
          headers,
          body: body == null ? undefined : JSON.stringify(body),
          credentials: "same-origin",
          signal: controller.signal,
        });
        const text = await response.text();
        let data = null;

        if (text) {
          try {
            data = JSON.parse(text);
          } catch {
            data = null;
          }
        }

        if (response.ok) return data;

        const error = new Error(messageOf(data, `接口返回 ${response.status}。`));
        error.status = response.status;
        if (response.status >= 400 && response.status < 500 && response.status !== 429) throw error;
        lastError = error;
      } catch (error) {
        const failure = error?.name === "AbortError"
          ? new Error("接口请求超时。")
          : error;

        if (failure?.status >= 400 && failure.status < 500 && failure.status !== 429) throw failure;
        lastError = failure;
      } finally {
        clearTimeout(timer);
      }

      if (attempt < attempts - 1) await delay(retryDelayMs * (attempt + 1));
    }

    throw lastError || new Error("接口请求失败。");
  }

  return { delay, requestJson };
});
