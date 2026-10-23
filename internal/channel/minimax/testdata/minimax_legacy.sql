CREATE TABLE local_runtime_sessions (
  session_id TEXT PRIMARY KEY,
  title TEXT,
  workspace_dir TEXT,
  created_at_ms INTEGER,
  updated_at_ms INTEGER,
  visibility TEXT,
  parent_session_id TEXT,
  session_kind TEXT,
  archived INTEGER
);

INSERT INTO local_runtime_sessions VALUES
  ('mvs_legacy', '旧会话', 'D:/legacy', 1700000000000, 1700000002000, 'visible', NULL, 'conversation', 0);

CREATE TABLE local_runtime_messages (
  session_id TEXT,
  display_messages_json TEXT
);

INSERT INTO local_runtime_messages VALUES
  ('mvs_legacy', '[{"role":"user","msg_content":"旧问题","timestamp":"1700000000"},{"role":"assistant","msg_content":"旧答案","created_at":1700000001},{"role":"tool","msg_content":"隐藏工具输出"}]');

CREATE TABLE local_runtime_message_row_migrations (
  session_id TEXT
);
