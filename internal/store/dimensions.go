package store

// 维度表（L2a）的读写：derive 提取器算出来的结果落这里。
//
// 规矩：维度是派生物，整段替换是它正常的更新方式（DELETE + INSERT 一个事务）；
// 主库只有 store 写（ARCHITECTURE 模块表），所以提取器经这两个方法落账。

// DimensionRow 是一个维度条目：维度内的键与值都按字符串存，读方自己解释。
type DimensionRow struct {
	// Key 是维度内的键（如 渠道 ID、项目路径、日期）。
	Key string `json:"key"`
	// Value 是键对应的值（如 会话数、消息数），统一字符串。
	Value string `json:"value"`
}

// ReplaceDimension 整段替换一个维度的全部条目（一个事务）。
func (d *DB) ReplaceDimension(name string, rows []DimensionRow) error {
	tx, err := d.handle.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM dimensions WHERE name = ?`, name); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT INTO dimensions(name, key, value) VALUES(?, ?, ?)`,
			name, r.Key, r.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// WipeDimensions 清空全部维度表（AC-15 的「派生可弃」：删掉后能从 L1 全量重建）。
func (d *DB) WipeDimensions() error {
	_, err := d.handle.Exec(`DELETE FROM dimensions`)
	return err
}

// Dimension 读回一个维度的全部条目，按键排序。没有这个维度返回空切片。
func (d *DB) Dimension(name string) ([]DimensionRow, error) {
	rows, err := d.handle.Query(`SELECT key, value FROM dimensions WHERE name = ? ORDER BY key`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DimensionRow{}
	for rows.Next() {
		var r DimensionRow
		if err := rows.Scan(&r.Key, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EventCountsBySession 按会话行 ID 统计事件数（「规模」维度与测试用）。
func (d *DB) EventCountsBySession(rowIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(rowIDs) == 0 {
		return out, nil
	}
	stmt, err := d.handle.Prepare(`SELECT COUNT(*) FROM events WHERE session_id = ?`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	for _, id := range rowIDs {
		var n int
		if err := stmt.QueryRow(id).Scan(&n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, nil
}

// ToolCount 是一个工具在一个渠道里的调用次数。
type ToolCount struct {
	Channel string
	Tool    string
	Count   int
}

// ToolUsageLatest 统计最新 revision 里各渠道的工具调用次数（tool_call 事件）。
func (d *DB) ToolUsageLatest() ([]ToolCount, error) {
	rows, err := d.handle.Query(`SELECT s.channel, e.name, COUNT(*) AS n
		FROM events e
		JOIN sessions s ON e.session_id = s.id
		JOIN (SELECT channel, source_ref, session_key, MAX(revision) AS revision
			FROM sessions GROUP BY channel, source_ref, session_key) latest
		ON s.channel = latest.channel AND s.source_ref = latest.source_ref
			AND s.session_key = latest.session_key AND s.revision = latest.revision
		WHERE e.kind = ? AND e.name != ''
		GROUP BY s.channel, e.name
		ORDER BY s.channel, e.name`, KindToolCall)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ToolCount
	for rows.Next() {
		var tc ToolCount
		if err := rows.Scan(&tc.Channel, &tc.Tool, &tc.Count); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}
