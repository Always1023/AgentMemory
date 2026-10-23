create table topic (
  id text primary key,
  name text not null default '',
  active_node_id text,
  created_at integer not null,
  updated_at integer not null,
  deleted_at integer
);

create table message (
  id text primary key,
  parent_id text,
  topic_id text not null,
  role text not null,
  data text not null,
  searchable_text text not null default '',
  status text not null default 'success',
  siblings_group_id integer not null default 0,
  created_at integer not null,
  updated_at integer not null,
  deleted_at integer
);

insert into topic (id, name, active_node_id, created_at, updated_at, deleted_at) values
  ('topic-1', '示例会话', 'a2', 1760000000000, 1760000006000, null),
  ('topic-2', '', '', 1760000000000, 1760000001000, null),
  ('topic-3', '删掉的话题', 'x1', 1760000000000, 1760000001000, 1760000002000);

insert into message (id, parent_id, topic_id, role, data, searchable_text, status, siblings_group_id, created_at, updated_at, deleted_at) values
  ('r1', null, 'topic-1', 'root', '{"parts":[]}', '', 'success', 0, 1760000000000, 1760000000000, null),
  ('u1', 'r1', 'topic-1', 'user', '{"parts":[{"type":"text","text":"你好"}]}', '你好', 'success', 0, 1760000001000, 1760000001000, null),
  ('a1', 'u1', 'topic-1', 'assistant', '{"parts":[{"type":"text","text":"你好，有什么可以帮你？"},{"type":"reasoning","text":"思考中"},{"type":"data-error","data":{"name":"AbortError","message":"pause_placeholder"}}]}', '你好，有什么可以帮你？', 'success', 0, 1760000002000, 1760000002000, null),
  ('u2', 'a1', 'topic-1', 'user', '{"parts":[{"type":"text","text":"看下文件"},{"type":"file","mediaType":"text/plain","filename":"report.txt","url":"file:///report.txt"}]}', '看下文件', 'success', 0, 1760000003000, 1760000003000, null),
  ('a2', 'u2', 'topic-1', 'assistant', '{"parts":[{"type":"text","text":"好的"},{"type":"reasoning","text":"不该出现的思考"},{"type":"dynamic-tool","toolName":"read","input":{"path":"a.txt"},"state":"output-available","output":{"content":[{"type":"text","text":"文件内容"}]}}]}', '好的', 'success', 1, 1760000004000, 1760000004000, null),
  ('a3', 'u2', 'topic-1', 'assistant', '{"parts":[{"type":"text","text":"另一家的回答"}]}', '另一家的回答', 'success', 1, 1760000005000, 1760000005000, null),
  ('r2', null, 'topic-2', 'root', '{"parts":[]}', '', 'success', 0, 1760000000000, 1760000000000, null),
  ('x1', null, 'topic-3', 'root', '{"parts":[]}', '', 'success', 0, 1760000000000, 1760000000000, null);
