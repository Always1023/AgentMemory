package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// A1：按会话切片——逐行合法 JSON、字段带全会话定位、内容如实。
func TestWriteSessionNDJSON(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()
	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	n, err := db.WriteSessionNDJSON(&buf, s.Channel, s.SourceFile, s.ID)
	if err != nil {
		t.Fatalf("导出: %v", err)
	}
	if n != 7 {
		t.Fatalf("导出 %d 行，要 7 行", n)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("文件 %d 行，要 7 行", len(lines))
	}
	var first ndjsonLine
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("第一行不是合法 JSON: %v", err)
	}
	if first.Channel != "claude-code" || first.Session != "sess-001" || first.Title != "修归档撞名" {
		t.Fatalf("定位字段不对: %+v", first)
	}
	if first.Kind != KindMessage || first.Role != channel.RoleUser || first.Content != "归档为什么撞名了？" {
		t.Fatalf("内容字段不对: %+v", first)
	}
	if first.Ts != "2026-10-27T09:30:00Z" {
		t.Fatalf("时间戳不对: %q", first.Ts)
	}

	// 工具调用那一行带 name。
	var toolLine ndjsonLine
	for _, l := range lines {
		var line ndjsonLine
		if json.Unmarshal([]byte(l), &line) == nil && line.Kind == KindToolCall {
			toolLine = line
			break
		}
	}
	if toolLine.Name != "read_file" || toolLine.Content == "" {
		t.Fatalf("工具行缺名称或内容: %+v", toolLine)
	}

	// 不存在的会话：0 行，不报错。
	var empty strings.Builder
	n, err = db.WriteSessionNDJSON(&empty, "nope", "nope", "nope")
	if err != nil || n != 0 {
		t.Fatalf("不存在的会话应 0 行不报错，实际 %d, %v", n, err)
	}
}

// A2：按检索结果切片——命中的事件带全定位，内容与检索一致。
func TestWriteSearchNDJSON(t *testing.T) {
	db := openTestDB(t)
	s := fixtureSession()
	if err := db.WriteSession(s, time.Now()); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	n, err := db.WriteSearchNDJSON(&buf, "撞名", 0)
	if err != nil {
		t.Fatalf("导出: %v", err)
	}
	if n != 1 {
		t.Fatalf("导出 %d 行，要 1 行", n)
	}
	var line ndjsonLine
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	if line.Session != "sess-001" || line.Content != "归档为什么撞名了？" {
		t.Fatalf("切片内容不对: %+v", line)
	}
}
