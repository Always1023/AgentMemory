CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  cwd TEXT,
  title TEXT,
  started_at REAL NOT NULL,
  ended_at REAL,
  last_activity_at REAL
);

CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT,
  tool_call_id TEXT,
  tool_calls TEXT,
  tool_name TEXT,
  timestamp REAL NOT NULL,
  active INTEGER NOT NULL DEFAULT 1,
  compacted INTEGER NOT NULL DEFAULT 0,
  display_metadata TEXT
);

INSERT INTO sessions (id, cwd, title, started_at, last_activity_at)
VALUES ('s1', 'D:/work', 'Hermes 会话', 1700000000, 1700000002);

INSERT INTO messages (session_id, role, content, timestamp)
VALUES ('s1', 'user', '看一下磁盘', 1700000000);

INSERT INTO messages (session_id, role, content, tool_calls, timestamp)
VALUES ('s1', 'assistant', '我先检查。', '[{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"df -h\"}"}}]', 1700000001);

INSERT INTO messages (session_id, role, content, tool_call_id, timestamp)
VALUES ('s1', 'tool', 'Filesystem Size', 'c1', 1700000002);
