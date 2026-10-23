const ROLE_LABELS = {
  user: "我",
  assistant: "助手",
  system: "系统",
  tool: "工具结果",
};

const EMPTY = "—";

export function oneLine(value) {
  return String(value ?? "").replace(/[\r\n]+/g, " ").trim();
}

export function fenceFor(body) {
  let longest = 0;
  let run = 0;

  for (const char of String(body ?? "")) {
    if (char === "`") {
      run += 1;
      if (run > longest) longest = run;
      continue;
    }
    run = 0;
  }

  return "`".repeat(Math.max(3, longest + 1));
}

function valueOr(value) {
  const text = oneLine(value);
  return text || EMPTY;
}

function timeOr(value) {
  if (value == null || value === "") return EMPTY;
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return EMPTY;

  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  const hour = String(date.getHours()).padStart(2, "0");
  const minute = String(date.getMinutes()).padStart(2, "0");
  const second = String(date.getSeconds()).padStart(2, "0");
  return `${year}-${month}-${day} ${hour}:${minute}:${second}`;
}

function messageTime(value) {
  if (value == null || value === "") return "";
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return timeOr(date);
}

function roleLabel(role) {
  const key = String(role ?? "").trim().toLowerCase();
  if (!key) return "未知";
  return ROLE_LABELS[key] || oneLine(key);
}

function fenced(body) {
  const fence = fenceFor(body);
  return [fence, String(body).replace(/\n+$/, ""), fence];
}

export function renderMarkdown(conversation, channelName = "") {
  const title = oneLine(conversation?.title) || "未命名会话";
  const messages = Array.isArray(conversation?.messages) ? conversation.messages : [];
  const channel = oneLine(channelName) || oneLine(conversation?.site) || EMPTY;
  const lines = [];

  lines.push(`# ${title}`, "");
  lines.push(`- 渠道：${channel}`);
  lines.push(`- 项目：${EMPTY}`);
  lines.push(`- 会话：${valueOr(conversation?.id)}`);
  lines.push(`- 开始：${timeOr(conversation?.startedAt)}`);
  lines.push(`- 消息：${messages.length} 条`);
  lines.push(`- 来自：${valueOr(conversation?.url)}`);
  lines.push(`- 保存：${timeOr(conversation?.capturedAt)}`);

  for (let index = 0; index < messages.length; index += 1) {
    const message = messages[index];
    const time = messageTime(message?.time);
    const heading = `## ${index + 1} · ${roleLabel(message?.role)}${time ? ` · ${time}` : ""}`;
    lines.push("", heading, "");

    const body = String(message?.text ?? "").trim();
    if (body) lines.push(...fenced(body));
  }

  if (messages.length > 0) lines.push("");
  return `${lines.join("\n")}\n`;
}
