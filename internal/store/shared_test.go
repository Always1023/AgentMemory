package store

// 同一进程里各模块同时开主库（收集在写、界面在读 / 数待收）时的两条规矩：
//
//   - 同一个库文件只开一条底层连接，所有调用共用它（I-081 验收反馈：
//     收集时界面刷新撞锁，报 SQLITE_BUSY，整轮中断）；
//   - 换代后的第一次打开清一次旧水位，否则 L1 永远空着、搜索搜不到东西
//     （I-082 验收反馈）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Always1023/AgentMemory/internal/channel"
)

// 同一目录开两次，拿到的必须是同一条连接：这是「进程内单写者」的前提。
func TestOpenDBSharesOneHandle(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("第一次打开: %v", err)
	}
	defer first.Close()
	second, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("第二次打开: %v", err)
	}
	defer second.Close()

	if first.handle != second.handle {
		t.Fatal("同一目录的两次打开应当共用一条连接")
	}

	session := fixtureSession()
	if err := first.WriteSession(session, time.Now()); err != nil {
		t.Fatalf("写会话: %v", err)
	}
	if _, ok, err := second.ReadSession(session.Channel, session.SourceFile, session.ID); err != nil || !ok {
		t.Fatalf("第二个句柄读不到刚写的会话：ok=%v err=%v", ok, err)
	}

	// 关掉一个引用，另一个照常用——收集用的句柄关了不该把界面的句柄一起带走。
	if err := first.Close(); err != nil {
		t.Fatalf("关第一个句柄: %v", err)
	}
	if err := second.WriteSession(session, time.Now()); err != nil {
		t.Fatalf("还有引用时第二个句柄应当照常可用: %v", err)
	}
}

// 两个模块同时写：共用一条连接后串行执行，不该再出现 database is locked。
func TestConcurrentWritersDoNotHitLock(t *testing.T) {
	dir := t.TempDir()
	a, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("打开: %v", err)
	}
	defer a.Close()
	b, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("打开: %v", err)
	}
	defer b.Close()

	const each = 25
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	write := func(db *DB, tag string) {
		defer wg.Done()
		for i := 0; i < each; i++ {
			s := fixtureSession()
			s.ID = fmt.Sprintf("%s-%02d", tag, i)
			s.SourceFile = fmt.Sprintf(`C:\fake\%s-%02d.jsonl`, tag, i)
			if err := db.WriteSession(s, time.Now()); err != nil {
				errs <- err
				return
			}
		}
	}
	wg.Add(2)
	go write(a, "a")
	go write(b, "b")
	wg.Wait()
	close(errs)
	for err := range errs {
		if isBusy(err) {
			t.Fatalf("并发写入又撞上锁了: %v", err)
		}
		if err != nil {
			t.Fatalf("并发写入失败: %v", err)
		}
	}

	var n int
	if err := a.handle.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("数会话: %v", err)
	}
	if n != each*2 {
		t.Fatalf("会话数 = %d，要 %d", n, each*2)
	}
}

// 换代第一次收集：旧 index.json 的水位不能把源文件全跳过去（否则 L1 永远是空的）。
// 回填完成之后，水位恢复原来的作用，导出路径这些数字也还在。
func TestFirstCollectAfterUpgradeRereadsEverything(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sources", "old.jsonl")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyCursor(t, dir, Entry{
		Path:        src,
		Size:        info.Size(),
		ModTime:     info.ModTime(),
		ArchivePath: "导出/old.md",
		ProcessedAt: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
	})

	db, err := OpenDB(dir)
	if err != nil {
		t.Fatalf("打开: %v", err)
	}
	defer db.Close()

	cursor, err := db.OpenCursor()
	if err != nil {
		t.Fatalf("打开水位: %v", err)
	}
	if !cursor.Forced() {
		t.Fatal("换代回填前应当不看水位")
	}
	sf := channel.SourceFile{Path: src, Size: info.Size(), ModTime: info.ModTime()}
	if !cursor.NeedsProcessing(sf) {
		t.Fatal("换代回填前，没变过的文件也要重读一遍")
	}

	if err := db.MarkL1Backfilled(); err != nil {
		t.Fatalf("记回填完成: %v", err)
	}
	again, err := db.OpenCursor()
	if err != nil {
		t.Fatalf("重开水位: %v", err)
	}
	if again.Forced() {
		t.Fatal("回填完成后不该再强制全读")
	}
	if again.NeedsProcessing(sf) {
		t.Fatal("回填完成后，没变过的文件不该再读")
	}
	if e, ok := again.Entry(src); !ok || e.ArchivePath != "导出/old.md" {
		t.Fatalf("水位里的导出路径应保留，实际 %+v", e)
	}
}

// writeLegacyCursor 在 dir 下写一份旧版 index.json（只用于导入）。
func writeLegacyCursor(t *testing.T, dir string, entries ...Entry) {
	t.Helper()
	raw, err := json.Marshal(cursorFile{Version: legacyFileVersion, Entries: entries})
	if err != nil {
		t.Fatalf("编码旧游标: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), raw, 0o644); err != nil {
		t.Fatalf("写旧游标: %v", err)
	}
}
