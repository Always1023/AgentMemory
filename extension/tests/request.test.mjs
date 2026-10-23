import test from "node:test";
import assert from "node:assert/strict";

import "../content/request.js";

const { requestJson } = globalThis.AgentMemoryWebRequest;

function jsonResponse(body, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async text() {
      return JSON.stringify(body);
    },
  };
}

test("requestJson 发同源 JSON 请求并解析响应", async () => {
  const calls = [];

  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    return jsonResponse({ ok: true });
  };

  const data = await requestJson("/api/test", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: { hello: "world" },
  });

  assert.deepEqual(data, { ok: true });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, "/api/test");
  assert.equal(calls[0].options.method, "POST");
  assert.equal(calls[0].options.body, JSON.stringify({ hello: "world" }));
  assert.equal(calls[0].options.credentials, "same-origin");
});

test("requestJson 遇到 4xx 不重试并带上状态码", async () => {
  let calls = 0;

  globalThis.fetch = async () => {
    calls += 1;
    return jsonResponse({ message: "登录已失效" }, 401);
  };

  await assert.rejects(
    requestJson("/api/test", { attempts: 3, retryDelayMs: 0 }),
    (error) => error.status === 401 && /登录已失效/.test(error.message),
  );
  assert.equal(calls, 1);
});

test("requestJson 遇到 5xx 重试", async () => {
  let calls = 0;

  globalThis.fetch = async () => {
    calls += 1;
    return calls === 1 ? jsonResponse({}, 500) : jsonResponse({ ok: true });
  };

  const data = await requestJson("/api/test", { attempts: 2, retryDelayMs: 0 });

  assert.deepEqual(data, { ok: true });
  assert.equal(calls, 2);
});

test("requestJson 超时后报可读错误", async () => {
  globalThis.fetch = (_url, options) => new Promise((_, reject) => {
    options.signal.addEventListener("abort", () => {
      const error = new Error("aborted");
      error.name = "AbortError";
      reject(error);
    });
  });

  await assert.rejects(
    requestJson("/api/test", { timeoutMs: 10, attempts: 1 }),
    /接口请求超时/,
  );
});
