// Package config 负责配置文件的读写与版本迁移。
//
// 单一写入者：本项目中只有本包读写配置文件（见 docs/ARCHITECTURE.md 的 AC-10）。
// 路径不硬编码：一律来自 BaseDir，便于测试与绿色版（AC-09）。
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// Version 是配置结构版本。字段增删或语义变化时加一，
	// 并在 migrations 里补一步，否则 migrate 会拒绝升级。
	Version = 1

	// appDirName 是配置目录名。
	appDirName = "AgentMemory"

	// envHome 覆盖配置根目录，用于测试与绿色版。
	envHome = "AGENTMEMORY_HOME"

	// envPrefix 是逐项覆盖配置的环境变量前缀。
	envPrefix = "AGENTMEMORY_"
)

// 备份方式的取值。
const (
	// BackupNone 只留本地归档，不做任何网络访问。
	BackupNone = "none"
	// BackupGit 提交并推送到远程仓库。PRD F5 的默认方式。
	BackupGit = "git"
	// BackupSync 镜像到同步目录，交给网盘客户端（阶段 2）。
	BackupSync = "sync"
)

// GitBackup 是 git 备份的配置。
type GitBackup struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	// TimeoutSeconds 是单次 git 调用的上限，0 表示用默认值（AC-07）。
	TimeoutSeconds int `json:"timeout_seconds"`
}

// SyncBackup 是镜像到同步目录的配置。
type SyncBackup struct {
	TargetDir      string `json:"target_dir"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Backup 是备份环节的配置。
type Backup struct {
	Mode string     `json:"mode"`
	Git  GitBackup  `json:"git"`
	Sync SyncBackup `json:"sync"`
}

// Schedule 是定时收集的配置。
type Schedule struct {
	Enabled bool `json:"enabled"`
	// DailyAt 是每天触发时刻，形如 "09:00"。
	DailyAt string `json:"daily_at"`
}

// Config 是配置文件的结构。
type Config struct {
	Version      int  `json:"version"`
	FirstRunDone bool `json:"first_run_done"`
	// FirstRunStep 是首次运行引导走到了第几步（0 起）。
	//
	// 为什么要记：引导中途关掉窗口，下次打开得接着走（I-013 验收标准 4）。
	// FirstRunDone 为真之后这个字段没有意义。
	FirstRunStep int `json:"first_run_step,omitempty"`
	// ArchiveDir 是归档目录，收上来的 Markdown 落在这里。
	ArchiveDir string `json:"archive_dir"`
	// Channels 是各渠道的启用状态，键是渠道 ID。
	Channels map[string]bool `json:"channels"`
	Backup   Backup          `json:"backup"`
	Schedule Schedule        `json:"schedule"`
	// MCPEnabled 是「对外只读接口」的总开关（F15）：关着时 `--mcp` 拒绝启动。
	// 默认关（PRD 待确认项按最保守取值）。
	MCPEnabled bool `json:"mcp_enabled,omitempty"`
}

// Default 返回一份安全的默认配置：宁可空，不要错。
func Default() Config {
	return Config{
		Version:      Version,
		FirstRunDone: false,
		ArchiveDir:   defaultArchiveDir(),
		Channels:     map[string]bool{},
		Backup: Backup{
			// PRD F5：默认方式是 GitHub。仓库地址留空，
			// 未配置时备份环节跳过并如实报告，不发起网络请求（AC-02）。
			// 默认只存本地：新装的人不动设置就是最省事那档，不碰网络、不动别人的账号（F5，I-032）。
			Mode: BackupNone,
			Git:  GitBackup{Branch: "main", TimeoutSeconds: 120},
			Sync: SyncBackup{TimeoutSeconds: 300},
		},
		Schedule: Schedule{Enabled: false, DailyAt: "09:00"},
	}
}

// BaseDir 返回配置与日志的根目录。
//
// 默认 %APPDATA%\AgentMemory；环境变量 AGENTMEMORY_HOME 可覆盖。
func BaseDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(envHome)); v != "" {
		return v, nil
	}
	appData := strings.TrimSpace(os.Getenv("APPDATA"))
	if appData == "" {
		return "", errors.New("环境变量 APPDATA 为空，无法确定配置目录（可用 AGENTMEMORY_HOME 指定）")
	}
	return filepath.Join(appData, appDirName), nil
}

// Path 返回配置文件的完整路径。
func Path() (string, error) {
	dir, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load 读取配置，按需建默认值、按需迁移，最后套用环境变量覆盖。
func Load() (Config, error) {
	dir, err := BaseDir()
	if err != nil {
		return Config{}, err
	}
	return loadFrom(dir)
}

// Save 把配置写回默认位置。
func Save(c Config) error {
	dir, err := BaseDir()
	if err != nil {
		return err
	}
	return saveTo(dir, c)
}

func defaultArchiveDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Documents", appDirName)
}

func loadFrom(dir string) (Config, error) {
	path := filepath.Join(dir, "config.json")

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		c := Default()
		if err := saveTo(dir, c); err != nil {
			return Config{}, err
		}
		return applyEnv(c), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("读取配置 %s: %w", path, err)
	}

	c, err := decode(raw)
	if err != nil {
		return Config{}, fmt.Errorf("解析配置 %s: %w", path, err)
	}

	changed, err := migrate(&c)
	if err != nil {
		return Config{}, fmt.Errorf("迁移配置 %s: %w", path, err)
	}
	if changed {
		if err := saveTo(dir, c); err != nil {
			return Config{}, err
		}
	}
	return applyEnv(c), nil
}

func saveTo(dir string, c Config) error {
	if c.Version <= 0 {
		c.Version = Version
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录 %s: %w", dir, err)
	}

	path := filepath.Join(dir, "config.json")
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	raw = append(raw, '\n')

	// 先写临时文件再改名：中途失败不会留下半个配置文件。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写配置 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("落盘配置 %s: %w", path, err)
	}
	return nil
}

// decode 先按默认值铺底再覆盖，缺字段的文件因此能保住默认值。
func decode(raw []byte) (Config, error) {
	// 记事本这类编辑器会在 UTF-8 前面加 BOM；不剥掉的话第一个字符就解析失败，
	// 人只会看到「配置打不开」（I-052 / I-019）。
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	var head struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Config{}, err
	}

	c := Default()
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, err
	}

	// 文件里没有 version 字段 → 按 v0 处理，交给 migrate 升上来。
	if head.Version == nil {
		c.Version = 0
	}
	if c.Channels == nil {
		c.Channels = map[string]bool{}
	}
	return c, nil
}

// migrations 是版本迁移步骤表：migrations[N] 把 v(N-1) 升到 vN。
//
// 新增配置版本时，在这里补一个函数并把 Version 加一。
// 迁移只做加法：丢字段是 bug，不是迁移。
var migrations = map[int]func(*Config) error{
	// v0（配置里还没有 version 字段）→ v1：版本号是后加的，这里只盖章、不动数据。
	1: func(*Config) error { return nil },
}

// migrate 把配置升到当前版本，返回是否发生了改动。
func migrate(c *Config) (bool, error) {
	if c.Version > Version {
		return false, fmt.Errorf("配置版本 %d 高于本程序支持的 %d，请升级 AgentMemory", c.Version, Version)
	}

	changed := false
	for c.Version < Version {
		next := c.Version + 1
		step, ok := migrations[next]
		if !ok {
			return false, fmt.Errorf("缺少 v%d → v%d 的迁移步骤", c.Version, next)
		}
		if err := step(c); err != nil {
			return false, fmt.Errorf("v%d → v%d: %w", c.Version, next, err)
		}
		c.Version = next
		changed = true
	}

	if c.Channels == nil {
		c.Channels = map[string]bool{}
		changed = true
	}
	return changed, nil
}

// applyEnv 用环境变量覆盖配置项。空字符串不算覆盖。
func applyEnv(c Config) Config {
	if v, ok := lookupEnv(envPrefix + "ARCHIVE_DIR"); ok {
		c.ArchiveDir = v
	}
	if v, ok := lookupEnv(envPrefix + "BACKUP_MODE"); ok {
		c.Backup.Mode = v
	}
	if v, ok := lookupEnv(envPrefix + "BACKUP_GIT_REMOTE"); ok {
		c.Backup.Git.Remote = v
	}
	if v, ok := lookupEnv(envPrefix + "BACKUP_GIT_BRANCH"); ok {
		c.Backup.Git.Branch = v
	}
	if v, ok := lookupEnv(envPrefix + "SCHEDULE_DAILY_AT"); ok {
		c.Schedule.DailyAt = v
	}
	if v, ok := lookupEnv(envPrefix + "SCHEDULE_ENABLED"); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Schedule.Enabled = b
		}
	}
	return c
}

func lookupEnv(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return v, true
}

// Validate 检查配置是否自洽。Load 之后、使用之前调用。
func (c Config) Validate() error {
	switch c.Backup.Mode {
	case BackupNone, BackupGit, BackupSync:
	default:
		return fmt.Errorf("备份方式 %q 不认识（可选 %s / %s / %s）", c.Backup.Mode, BackupNone, BackupGit, BackupSync)
	}
	if strings.TrimSpace(c.ArchiveDir) == "" {
		return errors.New("归档目录为空")
	}
	if strings.TrimSpace(c.Schedule.DailyAt) != "" {
		if _, _, err := ParseDailyAt(c.Schedule.DailyAt); err != nil {
			return err
		}
	}
	return nil
}

// ParseDailyAt 解析 "HH:MM" 形式的时刻。
func ParseDailyAt(s string) (hour, minute int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("时刻 %q 格式不对，应为 HH:MM", s)
	}
	hour, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("时刻 %q 的小时部分不是数字", s)
	}
	minute, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("时刻 %q 的分钟部分不是数字", s)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("时刻 %q 超出范围", s)
	}
	return hour, minute, nil
}
