package derive

// 第一批机械维度：全部只靠 L1 的会话头与事件计数，不需要 LLM。
// 键的写法是各维度的对外契约，换了写法等于换口径——要在 issue 里过人。

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Always1023/AgentMemory/internal/store"
)

// byChannel：每个渠道有多少会话。
type byChannel struct{}

func (byChannel) Name() string { return "by_channel" }

func (byChannel) Extract(db *store.DB) ([]store.DimensionRow, error) {
	sessions, err := db.LatestSessions()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	order := []string{}
	for _, s := range sessions {
		if _, seen := counts[s.Channel]; !seen {
			order = append(order, s.Channel)
		}
		counts[s.Channel]++
	}
	rows := make([]store.DimensionRow, 0, len(order))
	for _, ch := range order {
		rows = append(rows, store.DimensionRow{Key: ch, Value: strconv.Itoa(counts[ch])})
	}
	return rows, nil
}

// byProject：每个渠道的每个项目有多少会话。键 = 渠道｜项目路径。
type byProject struct{}

func (byProject) Name() string { return "by_project" }

func (byProject) Extract(db *store.DB) ([]store.DimensionRow, error) {
	sessions, err := db.LatestSessions()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	order := []string{}
	for _, s := range sessions {
		key := s.Channel + "|" + s.ProjectPath
		if _, seen := counts[key]; !seen {
			order = append(order, key)
		}
		counts[key]++
	}
	rows := make([]store.DimensionRow, 0, len(order))
	for _, key := range order {
		rows = append(rows, store.DimensionRow{Key: key, Value: strconv.Itoa(counts[key])})
	}
	return rows, nil
}

// byDay：会话开始日期的分布。没有开始时间的会话记「未知」。
type byDay struct{}

func (byDay) Name() string { return "by_day" }

func (byDay) Extract(db *store.DB) ([]store.DimensionRow, error) {
	sessions, err := db.LatestSessions()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	order := []string{}
	for _, s := range sessions {
		key := "未知"
		if !s.StartedAt.IsZero() {
			key = s.StartedAt.Format("2006-01-02")
		}
		if _, seen := counts[key]; !seen {
			order = append(order, key)
		}
		counts[key]++
	}
	rows := make([]store.DimensionRow, 0, len(order))
	for _, key := range order {
		rows = append(rows, store.DimensionRow{Key: key, Value: strconv.Itoa(counts[key])})
	}
	return rows, nil
}

// sessionStats：每个会话的规模（事件数）。键 = 渠道｜会话标识。
type sessionStats struct{}

func (sessionStats) Name() string { return "session_stats" }

func (sessionStats) Extract(db *store.DB) ([]store.DimensionRow, error) {
	sessions, err := db.LatestSessions()
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.RowID)
	}
	counts, err := db.EventCountsBySession(ids)
	if err != nil {
		return nil, err
	}
	rows := make([]store.DimensionRow, 0, len(sessions))
	for _, s := range sessions {
		rows = append(rows, store.DimensionRow{
			// 键里带源文件：同一个会话标识可能出现在多个源文件里（Codex 续写会
			// 另开 rollout 文件、OpenCode 的 v1 / v2 表各留一份），只按渠道 + 标识
			// 会撞主键、整段维度落不了账（I-083 验收反馈）。
			Key:   s.Channel + "|" + s.SourceRef + "|" + s.SessionKey,
			Value: fmt.Sprintf("events=%d", counts[s.RowID]),
		})
	}
	return rows, nil
}

// toolUsage：每个渠道每个工具的调用次数。键 = 渠道｜工具名。
type toolUsage struct{}

func (toolUsage) Name() string { return "tool_usage" }

func (toolUsage) Extract(db *store.DB) ([]store.DimensionRow, error) {
	usage, err := db.ToolUsageLatest()
	if err != nil {
		return nil, err
	}
	rows := make([]store.DimensionRow, 0, len(usage))
	for _, u := range usage {
		rows = append(rows, store.DimensionRow{
			Key:   u.Channel + "|" + u.Tool,
			Value: strconv.Itoa(u.Count),
		})
	}
	return rows, nil
}

// ── 视角（I-092）──────────────────────────────────────────────────────────
//
// 视角和维度的区别只在口径：维度回答「有多少」，视角回答「从哪个角度看」。
// 三者都只读 L1，落同一个 dimensions 表，重建规则与维度一样（AC-15）。

// weeklyActivity：按 ISO 周看活跃度。键 = 2026-W43；值 = sessions=N events=M。
type weeklyActivity struct{}

func (weeklyActivity) Name() string { return "weekly_activity" }

func (weeklyActivity) Extract(db *store.DB) ([]store.DimensionRow, error) {
	sessions, err := db.LatestSessions()
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.RowID)
	}
	events, err := db.EventCountsBySession(ids)
	if err != nil {
		return nil, err
	}
	weeks := map[string]int{}
	evs := map[string]int{}
	order := []string{}
	for _, s := range sessions {
		if s.StartedAt.IsZero() {
			continue
		}
		y, w := s.StartedAt.ISOWeek()
		key := fmt.Sprintf("%d-W%02d", y, w)
		if _, seen := weeks[key]; !seen {
			order = append(order, key)
		}
		weeks[key]++
		evs[key] += events[s.RowID]
	}
	sortStrings(order)
	rows := make([]store.DimensionRow, 0, len(order))
	for _, key := range order {
		rows = append(rows, store.DimensionRow{
			Key:   key,
			Value: fmt.Sprintf("sessions=%d events=%d", weeks[key], evs[key]),
		})
	}
	return rows, nil
}

// weekdayHour：会话在「星期几 × 时段」上的分布。键 = 周一|14（本地时间，小时）。
type weekdayHour struct{}

func (weekdayHour) Name() string { return "weekday_hour" }

func (weekdayHour) Extract(db *store.DB) ([]store.DimensionRow, error) {
	sessions, err := db.LatestSessions()
	if err != nil {
		return nil, err
	}
	names := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	counts := map[string]int{}
	for _, s := range sessions {
		if s.StartedAt.IsZero() {
			continue
		}
		at := s.StartedAt.In(time.Local)
		key := fmt.Sprintf("%s|%02d", names[int(at.Weekday())], at.Hour())
		counts[key]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sortStrings(keys)
	rows := make([]store.DimensionRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, store.DimensionRow{Key: key, Value: strconv.Itoa(counts[key])})
	}
	return rows, nil
}

// sizeBuckets：会话规模分布（按事件数分档）。键 = 1-5 / 6-20 / 21-100 / 100+。
type sizeBuckets struct{}

func (sizeBuckets) Name() string { return "size_buckets" }

func (sizeBuckets) Extract(db *store.DB) ([]store.DimensionRow, error) {
	sessions, err := db.LatestSessions()
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.RowID)
	}
	events, err := db.EventCountsBySession(ids)
	if err != nil {
		return nil, err
	}
	buckets := []string{"1-5", "6-20", "21-100", "100+"}
	counts := map[string]int{}
	for _, s := range sessions {
		n := events[s.RowID]
		switch {
		case n <= 5:
			counts["1-5"]++
		case n <= 20:
			counts["6-20"]++
		case n <= 100:
			counts["21-100"]++
		default:
			counts["100+"]++
		}
	}
	rows := make([]store.DimensionRow, 0, len(buckets))
	for _, key := range buckets {
		if counts[key] == 0 {
			continue
		}
		rows = append(rows, store.DimensionRow{Key: key, Value: strconv.Itoa(counts[key])})
	}
	return rows, nil
}

// sortStrings 就地升序排（键要稳定，重建才幂等）。
func sortStrings(items []string) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j] < items[j-1]; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
