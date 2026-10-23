package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// newHome 把配置根目录指到临时目录，测试不碰真实的 %APPDATA%。
func newHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(envHome, home)
	return home
}

func TestLoadCreatesDefaultWhenMissing(t *testing.T) {
	home := newHome(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Version != Version {
		t.Errorf("版本 = %d，想要 %d", c.Version, Version)
	}
	if c.ArchiveDir == "" {
		t.Error("归档目录为空，默认值应当给出位置")
	}
	if c.Backup.Mode != BackupNone {
		t.Errorf("默认备份方式 = %q，想要 %q（默认只存本地，Git 可选——F5/I-032）", c.Backup.Mode, BackupNone)
	}
	if c.Channels == nil {
		t.Error("渠道表为 nil")
	}

	// 默认配置要落盘，这样用户才有一份能打开来改的文件。
	if _, err := os.Stat(filepath.Join(home, "config.json")); err != nil {
		t.Errorf("默认配置没有落盘: %v", err)
	}
}

// 带 UTF-8 BOM 的配置也要能读：记事本另存就会加（I-019 / I-052）。
func TestLoadAcceptsBOM(t *testing.T) {
	home := newHome(t)

	body := `{"version":1,"archive_dir":"D:\\archives\\bom"}`
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte(body)...)
	if err := os.WriteFile(filepath.Join(home, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("带 BOM 的配置没读进来: %v", err)
	}
	if c.ArchiveDir != `D:\archives\bom` {
		t.Errorf("归档目录 = %q，想要 %q", c.ArchiveDir, `D:\archives\bom`)
	}
}

func TestLoadMigratesVersionlessFileAndKeepsFields(t *testing.T) {
	home := newHome(t)

	// v0 的配置文件：没有 version 字段，但用户已经填了内容。
	legacy := `{
  "archive_dir": "D:\\archives\\mine",
  "backup": { "mode": "none", "git": { "remote": "git@example.com:x/y.git", "branch": "trunk", "timeout_seconds": 30 } },
  "schedule": { "enabled": true, "daily_at": "21:30" }
}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if c.Version != Version {
		t.Errorf("迁移后版本 = %d，想要 %d", c.Version, Version)
	}
	if c.ArchiveDir != `D:\archives\mine` {
		t.Errorf("归档目录被迁移弄丢了: %q", c.ArchiveDir)
	}
	if c.Backup.Mode != BackupNone {
		t.Errorf("备份方式被迁移弄丢了: %q", c.Backup.Mode)
	}
	if c.Backup.Git.Remote != "git@example.com:x/y.git" || c.Backup.Git.Branch != "trunk" || c.Backup.Git.TimeoutSeconds != 30 {
		t.Errorf("git 备份配置被迁移弄丢了: %+v", c.Backup.Git)
	}
	if !c.Schedule.Enabled || c.Schedule.DailyAt != "21:30" {
		t.Errorf("定时配置被迁移弄丢了: %+v", c.Schedule)
	}

	// 迁移结果要写回文件，下次启动不必再迁一遍。
	raw, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != Version {
		t.Errorf("文件里的版本 = %d，想要 %d", got.Version, Version)
	}
}

func TestLoadFillsMissingFieldsFromDefaults(t *testing.T) {
	home := newHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ArchiveDir == "" {
		t.Error("缺字段应当补默认值，归档目录却为空")
	}
	if c.Backup.Git.TimeoutSeconds == 0 {
		t.Error("缺字段应当补默认值，git 超时却为 0")
	}
	if c.Backup.Git.Branch != "main" {
		t.Errorf("缺字段应当补默认值，分支 = %q", c.Backup.Git.Branch)
	}
}

func TestLoadRejectsNewerVersion(t *testing.T) {
	home := newHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("配置版本高于程序支持时应当报错")
	}
}

func TestEnvOverrides(t *testing.T) {
	newHome(t)
	t.Setenv(envPrefix+"ARCHIVE_DIR", `D:\archives\from-env`)
	t.Setenv(envPrefix+"BACKUP_MODE", BackupNone)
	t.Setenv(envPrefix+"SCHEDULE_ENABLED", "true")
	t.Setenv(envPrefix+"SCHEDULE_DAILY_AT", "07:05")
	// 空值不算覆盖。
	t.Setenv(envPrefix+"BACKUP_GIT_BRANCH", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ArchiveDir != `D:\archives\from-env` {
		t.Errorf("归档目录未被环境变量覆盖: %q", c.ArchiveDir)
	}
	if c.Backup.Mode != BackupNone {
		t.Errorf("备份方式未被环境变量覆盖: %q", c.Backup.Mode)
	}
	if !c.Schedule.Enabled || c.Schedule.DailyAt != "07:05" {
		t.Errorf("定时未被环境变量覆盖: %+v", c.Schedule)
	}
	if c.Backup.Git.Branch != "main" {
		t.Errorf("空的环境变量不该覆盖，分支 = %q", c.Backup.Git.Branch)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	newHome(t)

	c := Default()
	c.ArchiveDir = "D:/archives/round-trip"
	c.Backup.Mode = BackupNone
	c.Schedule.Enabled = true

	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ArchiveDir != c.ArchiveDir || got.Backup.Mode != c.Backup.Mode || got.Schedule.Enabled != c.Schedule.Enabled {
		t.Errorf("读回来和写出去不一致:\n写入 %+v\n读出 %+v", c, got)
	}
}

func TestValidate(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Errorf("默认配置应当合法: %v", err)
	}

	bad := Default()
	bad.Backup.Mode = "dropbox"
	if err := bad.Validate(); err == nil {
		t.Error("不认识的备份方式应当报错")
	}

	bad = Default()
	bad.ArchiveDir = "  "
	if err := bad.Validate(); err == nil {
		t.Error("归档目录为空应当报错")
	}

	bad = Default()
	bad.Schedule.DailyAt = "25:00"
	if err := bad.Validate(); err == nil {
		t.Error("超范围时刻应当报错")
	}
}

func TestParseDailyAt(t *testing.T) {
	hour, minute, err := ParseDailyAt("09:05")
	if err != nil || hour != 9 || minute != 5 {
		t.Fatalf("ParseDailyAt(09:05) = %d:%d, %v", hour, minute, err)
	}
	for _, bad := range []string{"", "9", "9:5:1", "aa:bb", "24:00", "12:60"} {
		if _, _, err := ParseDailyAt(bad); err == nil {
			t.Errorf("ParseDailyAt(%q) 应当报错", bad)
		}
	}
}
