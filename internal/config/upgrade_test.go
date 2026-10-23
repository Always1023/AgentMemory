package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这个文件盯的是 PRD F12（I-057）：覆盖升级只换程序文件，用户的数据不跟着程序目录走。
//
// 两条都在开发机上看不出问题——升级一次、换台电脑才暴露，所以钉成用例。

// 一、旧版本的配置（没有 version、没有新字段）升级后：设置还在、引导不被重新拉起。
func TestUpgradeKeepsSettingsAndDoesNotRestartTheWizard(t *testing.T) {
	home := newHome(t)

	// v0：用户已经走完引导、选好了归档目录。没有 version，也没有 first_run_steps。
	legacy := `{
  "archive_dir": "D:\\我的归档",
  "first_run_done": true,
  "backup": { "mode": "git", "git": { "remote": "git@example.com:me/mem.git", "branch": "main" } },
  "schedule": { "enabled": true, "daily_at": "08:15" }
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
	if !c.FirstRunDone {
		t.Error("升级把「已经走完引导」丢了：下次打开会重走首次引导")
	}
	if c.ArchiveDir != `D:\我的归档` {
		t.Errorf("升级把归档目录丢了: %q", c.ArchiveDir)
	}
	if c.Backup.Mode != BackupGit || c.Backup.Git.Remote != "git@example.com:me/mem.git" {
		t.Errorf("升级把备份设置丢了: %+v", c.Backup)
	}
	if !c.Schedule.Enabled || c.Schedule.DailyAt != "08:15" {
		t.Errorf("升级把定时设置丢了: %+v", c.Schedule)
	}
}

// 二、数据路径由用户目录 / 环境变量推出来，不随程序所在的目录变化。
//
// 这里把家目录与配置根目录都指到临时目录：配置就落在指定的地方，
// 默认归档目录落在（临时的）用户目录下——两者都不在程序目录里。
func TestDataPathsStayOutsideProgramDir(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "AgentMemory")
	t.Setenv(envHome, dataDir)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if !strings.HasPrefix(path, dataDir) {
		t.Errorf("配置文件路径 = %q，不在指定的配置根目录 %q 下", path, dataDir)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.HasPrefix(c.ArchiveDir, home) {
		t.Errorf("默认归档目录 = %q，不在用户目录 %q 下：它跟着程序目录走了？", c.ArchiveDir, home)
	}

	// 配置真的落盘在指定位置，而不是写在程序旁边。
	if _, err := os.Stat(filepath.Join(dataDir, "config.json")); err != nil {
		t.Errorf("配置没有落在 %q：%v", dataDir, err)
	}
}
