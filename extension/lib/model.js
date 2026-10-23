export const ROLE_USER = "user";
export const ROLE_ASSISTANT = "assistant";
export const ROLE_SYSTEM = "system";
export const ROLE_TOOL = "tool";

export function hashString(input, length = 16) {
  const text = String(input ?? "");
  let hash = 0xcbf29ce484222325n;
  const prime = 0x100000001b3n;
  const mask = 0xffffffffffffffffn;

  for (const byte of new TextEncoder().encode(text)) {
    hash ^= BigInt(byte);
    hash = (hash * prime) & mask;
  }

  return hash.toString(16).padStart(16, "0").slice(0, length);
}

export function conversationFingerprint(conversation) {
  const title = conversation?.title ?? "";
  const startedAt = conversation?.startedAt ?? "";
  const messages = Array.isArray(conversation?.messages) ? conversation.messages : [];
  const body = messages
    .map((message) => `${message?.role ?? ""}\u0000${message?.text ?? ""}`)
    .join("\u0001");

  return hashString(`${title}\u0002${startedAt}\u0002${body}`, 16);
}

export function normalizeRole(role) {
  return String(role ?? "").trim().toLowerCase();
}
