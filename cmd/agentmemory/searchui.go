package main

// 检索绑定（I-084 / F13）：界面搜索框走这里查主库的 FTS5 索引。
// 只读；渠道显示名借注册表翻一遍，前端不认渠道 ID。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/channels"
	"github.com/Always1023/AgentMemory/internal/config"
	"github.com/Always1023/AgentMemory/internal/store"
)

// UISearchHit 是一条检索结果。
type UISearchHit struct {
	Channel string `json:"channel"`
	Session string `json:"session"`
	Kind    string `json:"kind"`
	When    string `json:"when"`
	Snippet string `json:"snippet"`
}

// searchLimit 是一次最多带回来多少条：搜索框不是翻页器，先给第一屏。
const searchLimit = 50

// SearchMemory 按关键词查全部会话，可叠加渠道 / 项目 / 时间过滤（I-084 A2）；
// 查询词为空时返回空结果。
func (a *App) SearchMemory(query, channelID, project string, sinceDays int) ([]UISearchHit, error) {
	db, err := openStoreDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	filter := store.SearchFilter{Channel: channelID, Project: project}
	if sinceDays > 0 {
		filter.Since = time.Now().AddDate(0, 0, -sinceDays)
	}
	// 检索是只读的用户动作，日志只记「查了多大范围、命中多少、花了多久」，
	// 不记查询词本身（人可能搜的是正文里的私密内容）。
	started := time.Now()
	hits, err := db.Search(query, filter, searchLimit)
	a.logger.Info("检索",
		"词长", len([]rune(query)), "命中", len(hits),
		"用时", time.Since(started).Round(time.Millisecond),
		"限渠道", channelID != "", "限项目", project != "", "天数", sinceDays,
		"错误", err)
	if err != nil {
		return nil, fmt.Errorf("检索没做成：%w", err)
	}
	names := a.channelNames()
	out := make([]UISearchHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, UISearchHit{
			Channel: names[h.Channel],
			Session: sessionLabel(h),
			Kind:    kindLabel(h.Kind),
			When:    whenLabel(h.Ts),
			Snippet: h.Snippet,
		})
	}
	return out, nil
}

// channelNames 渠道 ID → 显示名；注册表里没有的渠道原样给 ID。
func (a *App) channelNames() map[string]string {
	registry := a.deps.Channels
	if registry == nil || len(registry.Entries()) == 0 {
		registry = channel.NewRegistry()
		if err := channels.RegisterInto(registry); err != nil {
			return map[string]string{}
		}
	}
	names := map[string]string{}
	for _, entry := range registry.Entries() {
		names[entry.ID] = entry.Name
	}
	return names
}

// sessionLabel 优先给会话标题，没标题退回会话标识。
func sessionLabel(h store.SearchHit) string {
	if h.Title != "" {
		return h.Title
	}
	return h.SessionKey
}

// kindLabel 把事件种类说成人话。
func kindLabel(kind string) string {
	switch kind {
	case store.KindMessage:
		return "消息"
	case store.KindToolCall:
		return "工具调用"
	case store.KindToolResult:
		return "工具结果"
	}
	return "内容"
}

// whenLabel 事件时间的界面写法；没有时间就不写。
func whenLabel(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.In(time.Local).Format("2006-01-02 15:04")
}

// UISearchExport 是一次切片导出的结果：条数与落点，界面上原样给人看。
type UISearchExport struct {
	Count int    `json:"count"`
	Path  string `json:"path"`
}

// searchExportLimit 是切片一次最多导出多少行：比搜索框的一屏给得足。
const searchExportLimit = 500

// ExportSearchNDJSON 把当前检索词的命中导出成 NDJSON 切片（F14）。
// 落点固定在归档目录的「切片」子目录，文件名带时刻，重跑出新文件不覆盖旧文件。
func (a *App) ExportSearchNDJSON(query string) (UISearchExport, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return UISearchExport{}, errors.New("还没有搜索词，先搜一次再导出")
	}
	cfg, err := config.Load()
	if err != nil {
		return UISearchExport{}, err
	}
	if err := cfg.Validate(); err != nil {
		return UISearchExport{}, fmt.Errorf("配置不可用：%w", err)
	}
	db, err := openStoreDB()
	if err != nil {
		return UISearchExport{}, err
	}
	defer db.Close()

	dir := filepath.Join(cfg.ArchiveDir, "切片")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return UISearchExport{}, fmt.Errorf("建切片目录：%w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("搜索-%s.ndjson", time.Now().Format("20060102-150405")))
	f, err := os.Create(path)
	if err != nil {
		return UISearchExport{}, fmt.Errorf("建切片文件：%w", err)
	}
	defer f.Close()

	n, err := db.WriteSearchNDJSON(f, query, searchExportLimit)
	if err != nil {
		return UISearchExport{}, fmt.Errorf("写切片：%w", err)
	}
	return UISearchExport{Count: n, Path: path}, nil
}

// UISearchOption 是一个过滤下拉项。
type UISearchOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// UISearchOptions 是搜索页的过滤候选：渠道与项目（来自维度表，收集后就是新的）。
type UISearchOptions struct {
	Channels []UISearchOption `json:"channels"`
	Projects []string         `json:"projects"`
}

// SearchOptions 给搜索页填过滤下拉；维度还没建出来时给空列表（只搜不过滤）。
func (a *App) SearchOptions() UISearchOptions {
	out := UISearchOptions{Channels: []UISearchOption{}, Projects: []string{}}
	db, err := openStoreDB()
	if err != nil {
		return out
	}
	defer db.Close()
	names := a.channelNames()

	if rows, err := db.Dimension("by_channel"); err == nil {
		for _, r := range rows {
			label := names[r.Key]
			if label == "" {
				label = r.Key
			}
			out.Channels = append(out.Channels, UISearchOption{Value: r.Key, Label: label})
		}
	}
	if rows, err := db.Dimension("by_project"); err == nil {
		for _, r := range rows {
			// 维度键是「渠道|项目」；竖线后的项目部分去重保序，空项目跳过。
			if i := strings.LastIndex(r.Key, "|"); i >= 0 {
				project := r.Key[i+1:]
				if project != "" && !containsString(out.Projects, project) {
					out.Projects = append(out.Projects, project)
				}
			}
		}
	}
	return out
}

// containsString 报告列表里有没有这一项。
func containsString(list []string, want string) bool {
	for _, one := range list {
		if one == want {
			return true
		}
	}
	return false
}
