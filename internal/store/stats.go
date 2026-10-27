package store

// 主库概况：给 MCP 的 memory_stats 与诊断用。只读，不改任何账。

import "fmt"

// Stats 是主库的只读概况。
type Stats struct {
	// Path 是主库文件路径。
	Path string `json:"path"`
	// SchemaVersion 是当前库版本。
	SchemaVersion int `json:"schema_version"`
	// Sessions / Events 是 L1 的行数。
	Sessions int `json:"sessions"`
	Events   int `json:"events"`
	// Channels 是每个渠道的会话数（最新 revision 计）。
	Channels map[string]int `json:"channels"`
	// LatestIngestedAt 是最新一条会话的入库时间（RFC3339Nano；没有会话时为空）。
	LatestIngestedAt string `json:"latest_ingested_at"`
}

// Stats 读一遍主库概况。
func (d *DB) Stats() (Stats, error) {
	out := Stats{Path: d.path, Channels: map[string]int{}}
	version, err := d.schemaVersion()
	if err != nil {
		return Stats{}, err
	}
	out.SchemaVersion = version

	if err := d.handle.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&out.Sessions); err != nil {
		return Stats{}, fmt.Errorf("数会话: %w", err)
	}
	if err := d.handle.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&out.Events); err != nil {
		return Stats{}, fmt.Errorf("数事件: %w", err)
	}
	rows, err := d.handle.Query(`SELECT channel, COUNT(*) FROM (
			SELECT channel, source_ref, session_key, MAX(revision) AS revision
			FROM sessions GROUP BY channel, source_ref, session_key
		) GROUP BY channel ORDER BY channel`)
	if err != nil {
		return Stats{}, fmt.Errorf("按渠道数会话: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return Stats{}, fmt.Errorf("读渠道计数: %w", err)
		}
		out.Channels[name] = n
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	if err := d.handle.QueryRow(`SELECT COALESCE(MAX(ingested_at), '') FROM sessions`).Scan(&out.LatestIngestedAt); err != nil {
		return Stats{}, fmt.Errorf("读最近入库时间: %w", err)
	}
	return out, nil
}

// DimensionNames 列出库里已有的维度名（给查询入口做候选）。只读。
func (d *DB) DimensionNames() ([]string, error) {
	rows, err := d.handle.Query(`SELECT DISTINCT name FROM dimensions ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("列维度名: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
