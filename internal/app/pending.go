package app

import (
	"bytes"
	"os"

	"github.com/Always1023/AgentMemory/internal/channel"
	"github.com/Always1023/AgentMemory/internal/render"
	"github.com/Always1023/AgentMemory/internal/store"
)

// Pending 是一个渠道还欠的活，按「有没有归档过」分成两类。
type Pending struct {
	// New 是还没有归档过的新会话：游标里没有，或上一轮解析失败 / 空的。
	New int
	// Updated 是已经归档过、内容又变了的会话。
	Updated int
}

// Total 是两类之和。
func (p Pending) Total() int { return p.New + p.Updated }

// PendingByChannel 数每个渠道的待收，按渠道 ID 分组。
//
// 只看游标、不重读没变的文件。唯一的例外是旧游标缺指纹、大小相同只有修改时间
// 变了的候选：重新解析并与现有归档逐字节比一次，比中就把指纹补进游标——
// 升级后每份候选只比这一次，现有文件不会全量重读。
func PendingByChannel(deps Deps, reports []channel.Report) (map[string]Pending, error) {
	out := make(map[string]Pending, len(reports))

	total := 0
	for _, report := range reports {
		total += len(report.Files)
	}
	if total == 0 {
		return out, nil
	}

	dir, err := resolveBaseDir(deps)
	if err != nil {
		return nil, err
	}
	db, err := store.OpenDB(dir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	cursor, err := db.OpenCursor()
	if err != nil {
		return nil, err
	}
	_, adapters, err := resolveChannels(deps)
	if err != nil {
		return nil, err
	}

	for _, report := range reports {
		pending := Pending{}
		for _, sf := range report.Files {
			switch classifyPending(cursor, report.Name, adapters[report.ID], sf) {
			case store.ChangeNew:
				pending.New++
			case store.ChangeUpdated:
				// 游标里有记录但没有归档路径：上一轮是空会话或失败了，
				// 这次有内容就会产出新归档，算「没归档过的新会话」。
				if entry, ok := cursor.Entry(sf.Path); ok && entry.ArchivePath != "" {
					pending.Updated++
				} else {
					pending.New++
				}
			}
		}
		out[report.ID] = pending
	}

	if cursor.Dirty() {
		// 补指纹只是缓存；存不下也照样交回这次数好的结果。
		_ = cursor.Save()
	}
	return out, nil
}

// classifyPending 判断一个源文件欠什么活；旧游标缺指纹时拿现有归档比一次。
//
// 只有「大小相同、修改时间不同、游标又没有指纹」才会走到解析与比对，
// 其余情况都只查游标。取不到归档（空会话、上一轮失败）时保守当作有更新。
func classifyPending(cursor *store.Cursor, channelName string, adapter channel.Adapter, sf channel.SourceFile) store.Change {
	change := cursor.Check(sf)
	if change != store.ChangeUnknown {
		return change
	}

	prev, ok := cursor.Entry(sf.Path)
	if !ok || prev.ArchivePath == "" || adapter == nil {
		return store.ChangeUpdated
	}

	session, err := adapter.Parse(sf)
	if err != nil || session.MessageCount() == 0 {
		return store.ChangeUpdated
	}
	rendered := render.Render(session, channelName)
	existing, err := os.ReadFile(prev.ArchivePath)
	if err != nil || !bytes.Equal(existing, []byte(rendered)) {
		return store.ChangeUpdated
	}

	cursor.Adopt(sf)
	return store.ChangeNone
}
