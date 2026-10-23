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
  ('mvs_public', '项目检查', 'D:/work', 1700000000000, 1700000002000, 'visible', NULL, 'conversation', 0),
  ('mvs_child', '子会话', 'D:/work', 1700000000000, 1700000002000, 'visible', 'mvs_public', 'task', 0),
  ('mvs_hidden', '隐藏', 'D:/work', 1700000000000, 1700000002000, 'hidden', NULL, 'conversation', 0),
  ('mvs_archived', '归档', 'D:/work', 1700000000000, 1700000002000, 'visible', NULL, 'conversation', 1);

CREATE TABLE local_runtime_messages (
  session_id TEXT,
  display_messages_json TEXT
);

CREATE TABLE local_runtime_message_row_migrations (
  session_id TEXT
);

INSERT INTO local_runtime_message_row_migrations VALUES ('mvs_public');

CREATE TABLE local_runtime_message_rows (
  id INTEGER,
  session_id TEXT,
  role TEXT,
  data_json TEXT,
  created_at_ms INTEGER
);

INSERT INTO local_runtime_message_rows VALUES
  (1, 'mvs_public', 'user', '{"msg_content":"修复这个项目"}', 1700000000000),
  (2, 'mvs_public', 'assistant', '{"msg_content":"测试通过"}', 1700000001000);
