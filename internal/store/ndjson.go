package store

// NDJSON 模型切片（F14）：把会话或检索结果导出成「每行一个 JSON 对象」的
// 文件，直接喂给模型或脚本。切片是 L2 派生视图的又一支：只读 L1，随时可重出。

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
)

// ndjsonLine 是切片文件的一行。
type ndjsonLine struct {
	Channel string `json:"channel"`
	Session string `json:"session"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Seq     int    `json:"seq"`
	Kind    string `json:"kind"`
	Ts      string `json:"ts"`
	Role    string `json:"role,omitempty"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
}

// WriteSessionNDJSON 把一个会话的全部事件写成 NDJSON，返回写的行数。
// 会话不存在返回 (0, nil)——调用方按「没有这个会话」提示，不算错误。
func (d *DB) WriteSessionNDJSON(w io.Writer, channelID, sourceRef, sessionKey string) (int, error) {
	var rowID int64
	var title, project string
	err := d.handle.QueryRow(`SELECT id, title, project_path FROM sessions
		WHERE channel = ? AND source_ref = ? AND session_key = ?
		ORDER BY revision DESC LIMIT 1`, channelID, sourceRef, sessionKey).
		Scan(&rowID, &title, &project)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return d.writeEventLines(w, func(yield func(ndjsonLine) error) error {
		events, err := d.Events(rowID)
		if err != nil {
			return err
		}
		for _, e := range events {
			if err := yield(ndjsonLine{
				Channel: channelID, Session: sessionKey, Title: title, Project: project,
				Seq: e.Seq, Kind: e.Kind, Ts: fmtTimeOrEmpty(e.Ts),
				Role: e.Role, Name: e.Name, Content: e.Content,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// WriteSearchNDJSON 把检索命中的事件写成 NDJSON，返回写的行数。
func (d *DB) WriteSearchNDJSON(w io.Writer, query string, limit int) (int, error) {
	hits, err := d.Search(query, SearchFilter{}, limit)
	if err != nil {
		return 0, err
	}
	return d.writeEventLines(w, func(yield func(ndjsonLine) error) error {
		for _, h := range hits {
			if err := yield(ndjsonLine{
				Channel: h.Channel, Session: h.SessionKey, Title: h.Title, Project: h.ProjectPath,
				Seq: h.Seq, Kind: h.Kind, Ts: fmtTimeOrEmpty(h.Ts),
				Content: h.Content,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// writeEventLines 驱动「逐行生成 → 编码 → 落盘」，并顺手统计行数。
func (d *DB) writeEventLines(w io.Writer, produce func(yield func(ndjsonLine) error) error) (int, error) {
	buf := bufio.NewWriter(w)
	encoder := json.NewEncoder(buf)
	n := 0
	err := produce(func(line ndjsonLine) error {
		if err := encoder.Encode(line); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	if err := buf.Flush(); err != nil {
		return n, err
	}
	return n, nil
}
